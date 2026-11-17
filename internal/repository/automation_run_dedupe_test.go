package repository

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A duplicate dedupe_key must report "not inserted" on every engine. MariaDB
// 10.5 and later answered the duplicate with the existing row when the insert
// carried a RETURNING clause, so the repository executes it without one and
// decides from the affected-row count (#367). The MySQL root runs against
// whatever server TEST_MYSQL_DSN names, MySQL or MariaDB.

func TestAutomationRunDedupeSQLite(t *testing.T) {
	runAutomationRunDedupe(t, openAutomationRepoDB(t, false, 1))
}

func TestAutomationRunDedupeMySQL(t *testing.T) {
	runAutomationRunDedupe(t, openAutomationRepoDB(t, true, 2))
}

func runAutomationRunDedupe(t *testing.T, db *gorm.DB) {
	repo := NewAutomationRepository(db)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)

	newRun := func(key string) *models.AutomationRun {
		return &models.AutomationRun{RuleID: rule.ID, RuleVersion: rule.Version, EntityType: rule.EntityType, EntityID: 1,
			DedupeKey: key, Status: models.AutomationRunPending, NextPosition: 1}
	}
	countRuns := func(t *testing.T, handle *gorm.DB) int64 {
		t.Helper()
		var n int64
		require.NoError(t, handle.Model(&models.AutomationRun{}).Count(&n).Error)
		return n
	}
	storedID := func(t *testing.T, key string) uint {
		t.Helper()
		var ids []uint
		require.NoError(t, db.Model(&models.AutomationRun{}).Where("dedupe_key = ?", key).Pluck("id", &ids).Error)
		require.Len(t, ids, 1, "exactly one stored run per dedupe key")
		return ids[0]
	}

	stamp := time.Now().UnixNano()
	firstKey := fmt.Sprintf("dedupe:%d:first", stamp)
	first := newRun(firstKey)

	t.Run("first insert reports a new row", func(t *testing.T) {
		inserted, err := repo.CreateRunIgnoringDuplicate(first)
		require.NoError(t, err)
		require.True(t, inserted)
		require.NotZero(t, first.ID)
		assert.False(t, first.CreatedAt.IsZero(), "timestamps are filled as on a normal create")
		assert.Equal(t, first.ID, storedID(t, firstKey))
		assert.Equal(t, int64(1), countRuns(t, db))
	})

	t.Run("duplicate key reports no insert and leaves the id zero", func(t *testing.T) {
		before := countRuns(t, db)
		dup := newRun(firstKey)
		inserted, err := repo.CreateRunIgnoringDuplicate(dup)
		require.NoError(t, err)
		assert.False(t, inserted, "a duplicate dedupe key must not be reported as inserted")
		assert.Zero(t, dup.ID, "the passed run keeps id 0 on a duplicate")
		assert.Equal(t, before, countRuns(t, db))
		assert.Equal(t, first.ID, storedID(t, firstKey), "the stored row is still the first one")
	})

	t.Run("fresh key after a duplicate reports a new row", func(t *testing.T) {
		before := countRuns(t, db)
		freshKey := fmt.Sprintf("dedupe:%d:fresh", stamp)
		fresh := newRun(freshKey)
		inserted, err := repo.CreateRunIgnoringDuplicate(fresh)
		require.NoError(t, err)
		require.True(t, inserted)
		require.NotZero(t, fresh.ID)
		assert.NotEqual(t, first.ID, fresh.ID)
		assert.Equal(t, fresh.ID, storedID(t, freshKey))
		assert.Equal(t, before+1, countRuns(t, db))
	})

	t.Run("duplicate inside an open transaction", func(t *testing.T) {
		before := countRuns(t, db)
		txKey := fmt.Sprintf("dedupe:%d:tx", stamp)
		var txRunID uint

		// Committed: the duplicate is refused and a fresh key is stored.
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			txRepo := NewAutomationRepository(tx)
			dup := newRun(firstKey)
			inserted, err := txRepo.CreateRunIgnoringDuplicate(dup)
			if err != nil {
				return err
			}
			assert.False(t, inserted)
			assert.Zero(t, dup.ID)

			fresh := newRun(txKey)
			inserted, err = txRepo.CreateRunIgnoringDuplicate(fresh)
			if err != nil {
				return err
			}
			assert.True(t, inserted)
			assert.NotZero(t, fresh.ID)
			txRunID = fresh.ID
			assert.Equal(t, before+1, countRuns(t, tx), "the transaction sees its own insert")
			return nil
		}))
		assert.Equal(t, before+1, countRuns(t, db))
		assert.Equal(t, txRunID, storedID(t, txKey))
		assert.Equal(t, first.ID, storedID(t, firstKey))

		// Rolled back: the insert ran on the transaction, not on the pool,
		// so it is gone with it.
		rolledBackKey := fmt.Sprintf("dedupe:%d:rolled-back", stamp)
		errRollback := errors.New("roll back")
		err := db.Transaction(func(tx *gorm.DB) error {
			txRepo := NewAutomationRepository(tx)
			dup := newRun(firstKey)
			inserted, err := txRepo.CreateRunIgnoringDuplicate(dup)
			if err != nil {
				return err
			}
			assert.False(t, inserted)
			assert.Zero(t, dup.ID)

			fresh := newRun(rolledBackKey)
			inserted, err = txRepo.CreateRunIgnoringDuplicate(fresh)
			if err != nil {
				return err
			}
			assert.True(t, inserted)
			assert.NotZero(t, fresh.ID)
			return errRollback
		})
		require.ErrorIs(t, err, errRollback)
		assert.Equal(t, before+1, countRuns(t, db), "the rolled-back insert is not stored")
		var n int64
		require.NoError(t, db.Model(&models.AutomationRun{}).Where("dedupe_key = ?", rolledBackKey).Count(&n).Error)
		assert.Zero(t, n)
	})
}
