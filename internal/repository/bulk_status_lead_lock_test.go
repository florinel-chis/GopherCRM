package repository

// Regression roots of #182: BulkStatusRepository.GetLeadsByIDs is a locking
// read of the batch's live leads in id order — `SELECT … FOR UPDATE … ORDER BY
// id ASC` on MySQL/MariaDB, a plain read on SQLite — and soft-deleted leads
// still read as missing. It is the "leads by id ascending" step of the shared
// lock order; the batch takes no company lock.

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var bulkLeadLockStatement = regexp.MustCompile("(?s)^SELECT \\* FROM `leads` WHERE id IN .*ORDER BY id ASC FOR UPDATE$")

// seedBulkLockLeads creates three live leads and one soft-deleted lead.
func seedBulkLockLeads(t *testing.T, db *gorm.DB) (live []models.Lead, deleted models.Lead) {
	t.Helper()
	seq := lockOrderRepoSeq.Add(1)
	owner := models.User{Email: fmt.Sprintf("bulk-lock-owner-%d@example.com", seq), Password: "x",
		FirstName: "Bulk", LastName: "Owner", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	for i := 0; i < 4; i++ {
		lead := models.Lead{FirstName: "Bulk", LastName: fmt.Sprintf("Lead %d", i),
			Email: fmt.Sprintf("bulk-lock-%d-%d@example.com", seq, i), Status: models.LeadStatusNew, OwnerID: owner.ID}
		require.NoError(t, db.Create(&lead).Error)
		live = append(live, lead)
	}
	deleted = live[2]
	require.NoError(t, db.Delete(&models.Lead{}, deleted.ID).Error)
	live = append(live[:2], live[3])
	return live, deleted
}

func bulkLockLeadIDs(leads []models.Lead) []uint {
	ids := make([]uint, 0, len(leads))
	for _, lead := range leads {
		ids = append(ids, lead.ID)
	}
	return ids
}

func TestBulkStatusLeadsLockingReadSQLite(t *testing.T) {
	db := openLockOrderRepoDB(t, false, 1)
	live, deleted := seedBulkLockLeads(t, db)

	recorder := newLockOrderRecorder()
	repo := NewBulkStatusRepository(db.Session(&gorm.Session{Logger: recorder}))
	// Requested out of order, with the deleted lead in the middle.
	got, err := repo.GetLeadsByIDs([]uint{live[2].ID, deleted.ID, live[0].ID, live[1].ID})
	require.NoError(t, err)
	assert.Equal(t, bulkLockLeadIDs(live), bulkLockLeadIDs(got), "the live leads, id ascending; the soft-deleted one reads as missing")

	empty, err := repo.GetLeadsByIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	reads := recorder.matching("FROM `leads`")
	require.Len(t, reads, 1, "one read:\n%s", recorder.all())
	assert.Contains(t, reads[0], "ORDER BY id ASC")
	assert.NotContains(t, reads[0], "FOR UPDATE", "SQLite drops the locking clause")
}

func TestBulkStatusLeadsLockingReadMySQL(t *testing.T) {
	db := openLockOrderRepoDB(t, true, 4)
	live, deleted := seedBulkLockLeads(t, db)

	recorder := newLockOrderRecorder()
	repo := NewBulkStatusRepository(db.Session(&gorm.Session{Logger: recorder}))
	got, err := repo.GetLeadsByIDs([]uint{live[2].ID, deleted.ID, live[0].ID, live[1].ID})
	require.NoError(t, err)
	assert.Equal(t, bulkLockLeadIDs(live), bulkLockLeadIDs(got), "the live leads, id ascending; the soft-deleted one reads as missing")

	reads := recorder.matching("FROM `leads`")
	require.Len(t, reads, 1, "one read:\n%s", recorder.all())
	assert.Regexp(t, bulkLeadLockStatement, reads[0])
	assert.Contains(t, reads[0], "`leads`.`deleted_at` IS NULL")

	// The read blocks a competing lead lock until its transaction ends.
	holder := db.Begin()
	require.NoError(t, holder.Error)
	defer func() { _ = holder.Rollback().Error }()
	_, err = NewBulkStatusRepository(db).WithTx(holder).GetLeadsByIDs([]uint{live[0].ID, live[1].ID})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		waiter := db.Begin()
		if waiter.Error != nil {
			done <- waiter.Error
			return
		}
		_, err := NewBulkStatusRepository(db).WithTx(waiter).GetLeadsByIDs([]uint{live[1].ID, live[2].ID})
		_ = waiter.Rollback().Error
		done <- err
	}()
	awaitLockOrderWait(t, db, bulkLeadLockStatement)
	select {
	case err := <-done:
		t.Fatalf("the competing read returned while the leads were locked: %v", err)
	default:
	}
	require.NoError(t, holder.Rollback().Error)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(lockOrderRepoTimeout):
		t.Fatal("the competing read never returned")
	}
}
