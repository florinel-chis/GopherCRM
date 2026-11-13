package repository

// AC-7 of #242: AutomationRepository's ticket reads and write and the ticket
// branch of ListScheduleCandidateIDs, and the bulk ticket read used by the
// bulk ticket status path: a locking read in id order (FOR UPDATE on MySQL).
// Full production schema on SQLite and on a disposable MySQL schema.

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationTicketRepositorySQLite(t *testing.T) {
	runAutomationTicketRepository(t, openIdentityErasureDB(t, false))
}

func TestAutomationTicketRepositoryMySQL(t *testing.T) {
	requireTicketMailMySQL(t)
	runAutomationTicketRepository(t, openIdentityErasureDB(t, true))
}

// repoStatementLog records the SQL of every query, update, delete and create
// the database runs while it is attached.
type repoStatementLog struct {
	mu   sync.Mutex
	sqls []string
}

func captureRepoStatements(t *testing.T, db *gorm.DB) *repoStatementLog {
	t.Helper()
	log := &repoStatementLog{}
	record := func(tx *gorm.DB) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.sqls = append(log.sqls, tx.Statement.SQL.String())
	}
	name := "automation_ticket_capture_" + nextTicketDeleteKey()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, record))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(name, record))
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register(name, record))
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(name, record))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(name)
		_ = db.Callback().Update().Remove(name)
		_ = db.Callback().Delete().Remove(name)
		_ = db.Callback().Create().Remove(name)
	})
	return log
}

func (l *repoStatementLog) first(pattern *regexp.Regexp) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, sql := range l.sqls {
		if pattern.MatchString(sql) {
			return i
		}
	}
	return -1
}

func (l *repoStatementLog) matching(pattern *regexp.Regexp) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, sql := range l.sqls {
		if pattern.MatchString(sql) {
			out = append(out, sql)
		}
	}
	return out
}

func (l *repoStatementLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.sqls, "\n")
}

func runAutomationTicketRepository(t *testing.T, db *gorm.DB) {
	mysql := db.Dialector.Name() == "mysql"
	repo := NewAutomationRepository(db)
	support := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSupport)
	other := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSupport)
	customer := seedExternalIDCustomer(t, db, "trepo")
	newTicket := func(status models.TicketStatus, updatedAt time.Time) *models.Ticket {
		ticket := &models.Ticket{Title: "Repo " + nextTicketDeleteKey(), Description: "d", Status: status,
			Priority: models.TicketPriorityMedium, CustomerID: customer.ID}
		ticket.CreatedAt, ticket.UpdatedAt = updatedAt, updatedAt
		require.NoError(t, db.Create(ticket).Error)
		return ticket
	}
	lockingTicketRead := regexp.MustCompile("(?is)^SELECT .*FROM [`\"]?tickets[`\"]?")
	forUpdate := regexp.MustCompile("(?i)FOR UPDATE")

	t.Run("GetTicketForUpdate and GetTicket", func(t *testing.T) {
		live := newTicket(models.TicketStatusOpen, time.Now().UTC())
		gone := newTicket(models.TicketStatusOpen, time.Now().UTC())
		require.NoError(t, db.Delete(&models.Ticket{}, gone.ID).Error)

		log := captureRepoStatements(t, db)
		var locked *models.Ticket
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			var err error
			locked, err = repo.WithTx(tx).GetTicketForUpdate(live.ID)
			return err
		}))
		assert.Equal(t, live.ID, locked.ID)
		assert.Equal(t, models.TicketStatusOpen, locked.Status)
		reads := log.matching(lockingTicketRead)
		require.Len(t, reads, 1, "one bare read, nothing preloaded:\n%s", log.String())
		assert.Equal(t, mysql, forUpdate.MatchString(reads[0]), "FOR UPDATE on MySQL only: %s", reads[0])

		_, err := repo.GetTicketForUpdate(gone.ID)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "a soft-deleted ticket is not found")

		log.mu.Lock()
		log.sqls = nil
		log.mu.Unlock()
		plain, err := repo.GetTicket(live.ID)
		require.NoError(t, err)
		assert.Equal(t, live.ID, plain.ID)
		reads = log.matching(lockingTicketRead)
		require.Len(t, reads, 1)
		assert.False(t, forUpdate.MatchString(reads[0]), "GetTicket is a plain read")
		_, err = repo.GetTicket(gone.ID)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("SetTicketAssignee writes only assigned_to_id and updated_at", func(t *testing.T) {
		old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
		ticket := newTicket(models.TicketStatusResolved, old)
		stamp := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
		require.NoError(t, db.Model(&models.Ticket{}).Where("id = ?", ticket.ID).UpdateColumn("first_responded_at", stamp).Error)
		var before models.Ticket
		require.NoError(t, db.First(&before, ticket.ID).Error)

		log := captureRepoStatements(t, db)
		now := time.Now().UTC().Truncate(time.Second)
		require.NoError(t, repo.SetTicketAssignee(ticket.ID, support.ID, now))

		var after models.Ticket
		require.NoError(t, db.First(&after, ticket.ID).Error)
		require.NotNil(t, after.AssignedToID)
		assert.Equal(t, support.ID, *after.AssignedToID)
		assert.True(t, after.UpdatedAt.Equal(now), "updated_at is the step's clock: %v", after.UpdatedAt)
		assert.Equal(t, before.Title, after.Title)
		assert.Equal(t, before.Description, after.Description)
		assert.Equal(t, before.Status, after.Status)
		assert.Equal(t, before.Priority, after.Priority)
		assert.Equal(t, before.CustomerID, after.CustomerID)
		require.NotNil(t, after.FirstRespondedAt)
		assert.True(t, after.FirstRespondedAt.Equal(*before.FirstRespondedAt), "first_responded_at is not written")

		updates := log.matching(regexp.MustCompile("(?is)^UPDATE [`\"]?tickets[`\"]?"))
		require.Len(t, updates, 1)
		set := regexp.MustCompile("(?is)SET (.*) WHERE").FindStringSubmatch(updates[0])
		require.Len(t, set, 2, updates[0])
		columns := regexp.MustCompile("[`\"]?([a-z_]+)[`\"]?=").FindAllStringSubmatch(set[1], -1)
		var names []string
		for _, column := range columns {
			names = append(names, column[1])
		}
		assert.ElementsMatch(t, []string{"assigned_to_id", "updated_at"}, names, updates[0])

		require.NoError(t, repo.SetTicketAssignee(ticket.ID, other.ID, now))
		require.NoError(t, db.First(&after, ticket.ID).Error)
		assert.Equal(t, other.ID, *after.AssignedToID)
	})

	t.Run("ListScheduleCandidateIDs ticket branch", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		stale := now.Add(-10 * 24 * time.Hour)
		cutoff := now.Add(-3 * 24 * time.Hour)
		var resolvedStale []uint
		for i := 0; i < 5; i++ {
			resolvedStale = append(resolvedStale, newTicket(models.TicketStatusResolved, stale).ID)
		}
		fresh := newTicket(models.TicketStatusResolved, now)
		openStale := newTicket(models.TicketStatusOpen, stale)
		deleted := newTicket(models.TicketStatusResolved, stale)
		require.NoError(t, db.Delete(&models.Ticket{}, deleted.ID).Error)

		ids, err := repo.ListScheduleCandidateIDs(AutomationScheduleQuery{EntityType: models.AutomationEntityTicket,
			TicketStatus: string(models.TicketStatusResolved), StaleBefore: &cutoff, AfterID: resolvedStale[0] - 1, Limit: 3})
		require.NoError(t, err)
		assert.Equal(t, resolvedStale[:3], ids, "id order, limit, status and staleness applied")

		ids, err = repo.ListScheduleCandidateIDs(AutomationScheduleQuery{EntityType: models.AutomationEntityTicket,
			TicketStatus: string(models.TicketStatusResolved), StaleBefore: &cutoff, AfterID: resolvedStale[2], Limit: 500})
		require.NoError(t, err)
		assert.Equal(t, resolvedStale[3:], ids, "strictly above the cursor; the fresh and deleted tickets are out")
		assert.NotContains(t, ids, fresh.ID)
		assert.NotContains(t, ids, deleted.ID)

		ids, err = repo.ListScheduleCandidateIDs(AutomationScheduleQuery{EntityType: models.AutomationEntityTicket,
			AfterID: resolvedStale[0] - 1, Limit: 500})
		require.NoError(t, err)
		assert.Contains(t, ids, fresh.ID, "no filter: every live ticket")
		assert.Contains(t, ids, openStale.ID)
		assert.NotContains(t, ids, deleted.ID)
		for i := 1; i < len(ids); i++ {
			assert.Less(t, ids[i-1], ids[i])
		}

		// A ticket filter set on another entity's query is ignored there,
		// and a lead's status filter is never applied to tickets.
		ids, err = repo.ListScheduleCandidateIDs(AutomationScheduleQuery{EntityType: models.AutomationEntityTicket,
			Status: string(models.LeadStatusNew), AfterID: resolvedStale[0] - 1, Limit: 500})
		require.NoError(t, err)
		assert.Contains(t, ids, openStale.ID)
	})

	t.Run("bulk ticket read is a locking read in id order", func(t *testing.T) {
		a := newTicket(models.TicketStatusOpen, time.Now().UTC())
		b := newTicket(models.TicketStatusInProgress, time.Now().UTC())
		c := newTicket(models.TicketStatusOpen, time.Now().UTC())
		require.NoError(t, db.Delete(&models.Ticket{}, b.ID).Error)

		log := captureRepoStatements(t, db)
		var tickets []models.Ticket
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			var err error
			tickets, err = NewBulkStatusRepository(db).WithTx(tx).GetTicketsByIDs([]uint{c.ID, b.ID, a.ID})
			return err
		}))
		require.Len(t, tickets, 2, "the deleted ticket reads as missing")
		assert.Equal(t, a.ID, tickets[0].ID)
		assert.Equal(t, c.ID, tickets[1].ID)
		reads := log.matching(lockingTicketRead)
		require.Len(t, reads, 1)
		assert.Regexp(t, regexp.MustCompile("(?i)ORDER BY id ASC"), reads[0])
		assert.Equal(t, mysql, forUpdate.MatchString(reads[0]), "FOR UPDATE on MySQL only: %s", reads[0])

		empty, err := NewBulkStatusRepository(db).GetTicketsByIDs(nil)
		require.NoError(t, err)
		assert.Empty(t, empty)
	})

	t.Run("ticket runs never reach the sales scope", func(t *testing.T) {
		ticket := newTicket(models.TicketStatusOpen, time.Now().UTC())
		rule := &models.AutomationRule{Name: "Scope", EntityType: models.AutomationEntityTicket,
			TriggerType: models.AutomationEventTicketCreated, Enabled: true}
		rule.Steps = []models.AutomationStep{{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{Title: "T"}}}
		require.NoError(t, repo.CreateRule(rule))
		run := &models.AutomationRun{RuleID: rule.ID, RuleVersion: 1, EntityType: models.AutomationEntityTicket,
			EntityID: ticket.ID, DedupeKey: fmt.Sprintf("scope:%s", nextTicketDeleteKey()), Status: models.AutomationRunPending, NextPosition: 1}
		require.NoError(t, db.Create(run).Error)
		owner := support.ID
		visible, err := repo.RunInScope(run.ID, AutomationRunScope{OwnerID: &owner})
		require.NoError(t, err)
		assert.False(t, visible)
		visible, err = repo.RunInScope(run.ID, AutomationRunScope{})
		require.NoError(t, err)
		assert.True(t, visible)
		runs, total, err := repo.ListRuns(0, 20, AutomationRunListFilter{EntityType: models.AutomationEntityTicket}, AutomationRunScope{})
		require.NoError(t, err)
		assert.Positive(t, total)
		assert.Equal(t, models.AutomationEntityTicket, runs[0].EntityType)
	})
}
