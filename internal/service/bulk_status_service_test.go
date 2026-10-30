package service

// Regression roots of the bulk status service (POST /leads|tickets|tasks/bulk/status)
// on a real database: the SQLite file with the production pragmas and the
// one-connection pool, and a disposable MySQL schema when TEST_MYSQL_DSN is set.
// Every assertion reads committed rows outside any transaction.
//
// The roots pin what the service promises beyond its HTTP surface: a batch that
// fails part way through its write rolls back every record AND every automation
// event, the history row records the outcome either way, a failure to write the
// history row's outcome never changes what the caller is told, and a failure to
// create the history row refuses the batch before anything is written.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBulkStatusServiceSQLite(t *testing.T) {
	runBulkStatusService(t, func(t *testing.T) *gorm.DB { return openAutomationSQLite(t) })
}

func TestBulkStatusServiceMySQL(t *testing.T) {
	runBulkStatusService(t, func(t *testing.T) *gorm.DB { return openAutomationMySQL(t, 2) })
}

// writeThenFailStatusRepo performs the real status write and then reports a
// failure, so the transaction has something to roll back.
type writeThenFailStatusRepo struct {
	repository.BulkStatusRepository
}

var errStatusWriteFault = errors.New("injected status write fault")

func (r writeThenFailStatusRepo) WithTx(tx *gorm.DB) repository.BulkStatusRepository {
	return writeThenFailStatusRepo{r.BulkStatusRepository.WithTx(tx)}
}

func (r writeThenFailStatusRepo) SetLeadStatus(ids []uint, status models.LeadStatus) error {
	if err := r.BulkStatusRepository.SetLeadStatus(ids, status); err != nil {
		return err
	}
	return errStatusWriteFault
}

func (r writeThenFailStatusRepo) SetTicketStatus(ids []uint, status models.TicketStatus) error {
	if err := r.BulkStatusRepository.SetTicketStatus(ids, status); err != nil {
		return err
	}
	return errStatusWriteFault
}

func (r writeThenFailStatusRepo) SetTaskStatus(ids []uint, status models.TaskStatus) error {
	if err := r.BulkStatusRepository.SetTaskStatus(ids, status); err != nil {
		return err
	}
	return errStatusWriteFault
}

// faultyOperationRepo fails the history writes it is told to fail.
type faultyOperationRepo struct {
	repository.BulkOperationRepository
	failCreate, failUpdate bool
}

func (r faultyOperationRepo) Create(ctx context.Context, operation *models.BulkOperation) error {
	if r.failCreate {
		return errors.New("injected history create fault")
	}
	return r.BulkOperationRepository.Create(ctx, operation)
}

func (r faultyOperationRepo) UpdateStatus(ctx context.Context, id uint, status models.BulkOperationStatus) error {
	if r.failUpdate {
		return errors.New("injected history update fault")
	}
	return r.BulkOperationRepository.UpdateStatus(ctx, id, status)
}

// eventFaultRepo lets the first `allow` automation events through the
// transaction-bound repository and fails every one after that, so a batch
// fails only once some of its events are already written inside the
// transaction.
type eventFaultRepo struct {
	repository.AutomationRepository
	allow    int
	recorded *int
}

var errEventWriteFault = errors.New("injected event write fault")

func (r eventFaultRepo) WithTx(tx *gorm.DB) repository.AutomationRepository {
	return eventFaultRepo{AutomationRepository: r.AutomationRepository.WithTx(tx), allow: r.allow, recorded: r.recorded}
}

func (r eventFaultRepo) RecordEvent(event *models.AutomationEvent) error {
	if *r.recorded >= r.allow {
		return errEventWriteFault
	}
	if err := r.AutomationRepository.RecordEvent(event); err != nil {
		return err
	}
	*r.recorded++
	return nil
}

func runBulkStatusService(t *testing.T, open func(t *testing.T) *gorm.DB) {
	quiet := logrus.New()
	quiet.SetLevel(logrus.PanicLevel)

	type fixture struct {
		env    *automationEnv
		lead   *models.Lead
		ticket *models.Ticket
		task   *models.Task
	}
	setup := func(t *testing.T) fixture {
		env := newAutomationEnv(t, open(t))
		ticket := &models.Ticket{Title: "Bulk", Description: "Bulk", Status: models.TicketStatusOpen,
			Priority: models.TicketPriorityMedium, CustomerID: env.createCustomer().ID}
		require.NoError(t, env.db.Create(ticket).Error)
		task := &models.Task{Title: "Bulk", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium,
			AssignedToID: env.admin.ID}
		require.NoError(t, env.db.Create(task).Error)
		return fixture{env: env, lead: env.createLead(env.sales, models.LeadStatusNew), ticket: ticket, task: task}
	}
	operations := func(t *testing.T, db *gorm.DB) []models.BulkOperation {
		t.Helper()
		var rows []models.BulkOperation
		require.NoError(t, db.Order("id ASC").Find(&rows).Error)
		return rows
	}
	reload := func(t *testing.T, db *gorm.DB, dest interface{}, id uint) {
		t.Helper()
		require.NoError(t, db.First(dest, id).Error)
	}

	t.Run("a write that fails after it ran rolls back records and events", func(t *testing.T) {
		f := setup(t)
		db := f.env.db
		svc := NewBulkStatusService(repository.NewBulkOperationRepository(db),
			writeThenFailStatusRepo{repository.NewBulkStatusRepository(db)}, f.env.txManager, quiet,
			WithBulkAutomationEvents(f.env.repo))

		_, err := svc.BulkSetLeadStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.lead.ID}, models.LeadStatusQualified)
		require.ErrorIs(t, err, errStatusWriteFault)
		_, err = svc.BulkSetTicketStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.ticket.ID}, models.TicketStatusResolved)
		require.ErrorIs(t, err, errStatusWriteFault)
		_, err = svc.BulkSetTaskStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.task.ID}, models.TaskStatusCompleted)
		require.ErrorIs(t, err, errStatusWriteFault)

		var lead models.Lead
		var ticket models.Ticket
		var task models.Task
		reload(t, db, &lead, f.lead.ID)
		reload(t, db, &ticket, f.ticket.ID)
		reload(t, db, &task, f.task.ID)
		assert.Equal(t, models.LeadStatusNew, lead.Status)
		assert.Equal(t, models.TicketStatusOpen, ticket.Status)
		assert.Equal(t, models.TaskStatusPending, task.Status)
		// The lead write fails before any event is recorded; event rollback
		// after a written event is pinned by the next subtest.
		assert.Empty(t, f.env.eventsOf(models.AutomationEntityLead, f.lead.ID))

		rows := operations(t, db)
		require.Len(t, rows, 3, "the history row survives the rollback")
		for i, resource := range []string{"leads", "tickets", "tasks"} {
			assert.Equal(t, resource, rows[i].ResourceType)
			assert.Equal(t, models.StatusFailed, rows[i].Status)
			assert.Equal(t, models.BulkAction, rows[i].Type)
			assert.Equal(t, f.env.admin.ID, rows[i].UserID)
			assert.Equal(t, 1, rows[i].TotalItems)
		}
	})

	t.Run("an event write that fails after earlier events rolls back statuses and events", func(t *testing.T) {
		f := setup(t)
		db := f.env.db
		second := f.env.createLead(f.env.sales, models.LeadStatusContacted)
		recorded := 0
		// The first lead records lead_updated and lead_status_changed; the
		// second lead's first event fails.
		events := eventFaultRepo{AutomationRepository: f.env.repo, allow: 2, recorded: &recorded}
		svc := NewBulkStatusService(repository.NewBulkOperationRepository(db),
			repository.NewBulkStatusRepository(db), f.env.txManager, quiet, WithBulkAutomationEvents(events))

		_, err := svc.BulkSetLeadStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.lead.ID, second.ID}, models.LeadStatusQualified)
		require.ErrorIs(t, err, errEventWriteFault)
		require.Equal(t, 2, recorded, "the fault must come after events were written inside the transaction")

		var first, other models.Lead
		reload(t, db, &first, f.lead.ID)
		reload(t, db, &other, second.ID)
		assert.Equal(t, models.LeadStatusNew, first.Status, "the status write rolled back")
		assert.Equal(t, models.LeadStatusContacted, other.Status)
		assert.Empty(t, f.env.eventsOf(models.AutomationEntityLead, f.lead.ID), "the events written before the fault rolled back")
		assert.Empty(t, f.env.eventsOf(models.AutomationEntityLead, second.ID))
		assert.Empty(t, f.env.events())

		rows := operations(t, db)
		require.Len(t, rows, 1)
		assert.Equal(t, models.StatusFailed, rows[0].Status)
		assert.Equal(t, 2, rows[0].TotalItems)
	})

	t.Run("a successful batch commits records, events and a completed history row", func(t *testing.T) {
		f := setup(t)
		db := f.env.db
		svc := NewBulkStatusService(repository.NewBulkOperationRepository(db),
			repository.NewBulkStatusRepository(db), f.env.txManager, quiet, WithBulkAutomationEvents(f.env.repo))

		result, err := svc.BulkSetLeadStatus(context.Background(), f.env.sales.ID, models.RoleSales, []uint{f.lead.ID, f.lead.ID}, models.LeadStatusQualified)
		require.NoError(t, err)
		assert.Equal(t, &models.BulkStatusUpdateResult{Updated: 1}, result)

		var lead models.Lead
		reload(t, db, &lead, f.lead.ID)
		assert.Equal(t, models.LeadStatusQualified, lead.Status)
		events := f.env.eventsOf(models.AutomationEntityLead, f.lead.ID)
		require.Equal(t, []string{models.AutomationEventLeadUpdated, models.AutomationEventLeadStatusChanged}, eventTypes(events))
		assert.Equal(t, models.AutomationOriginBulk, events[1].Origin)
		require.NotNil(t, events[1].ActorID)
		assert.Equal(t, f.env.sales.ID, *events[1].ActorID)

		rows := operations(t, db)
		require.Len(t, rows, 1)
		assert.Equal(t, models.StatusCompleted, rows[0].Status)
		assert.Equal(t, 1, rows[0].TotalItems)
	})

	t.Run("ticket and task batches record no lead events", func(t *testing.T) {
		f := setup(t)
		db := f.env.db
		svc := NewBulkStatusService(repository.NewBulkOperationRepository(db),
			repository.NewBulkStatusRepository(db), f.env.txManager, quiet, WithBulkAutomationEvents(f.env.repo))

		_, err := svc.BulkSetTicketStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.ticket.ID}, models.TicketStatusResolved)
		require.NoError(t, err)
		_, err = svc.BulkSetTaskStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.task.ID}, models.TaskStatusCompleted)
		require.NoError(t, err)
		assert.Empty(t, f.env.events())
	})

	t.Run("a failed outcome write does not change the answer", func(t *testing.T) {
		f := setup(t)
		db := f.env.db
		svc := NewBulkStatusService(faultyOperationRepo{BulkOperationRepository: repository.NewBulkOperationRepository(db), failUpdate: true},
			repository.NewBulkStatusRepository(db), f.env.txManager, quiet)

		result, err := svc.BulkSetTaskStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.task.ID}, models.TaskStatusInProgress)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Updated)
		var task models.Task
		reload(t, db, &task, f.task.ID)
		assert.Equal(t, models.TaskStatusInProgress, task.Status)

		_, err = svc.BulkSetTaskStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.task.ID, 999999}, models.TaskStatusPending)
		require.ErrorIs(t, err, apperrors.ErrNotFound, "the refusal is reported as itself, not as the history fault")

		rows := operations(t, db)
		require.Len(t, rows, 2)
		for _, row := range rows {
			assert.Equal(t, models.StatusPending, row.Status, "the outcome write failed, so the row stays pending")
		}
	})

	t.Run("a failed history create refuses the batch before any write", func(t *testing.T) {
		f := setup(t)
		db := f.env.db
		svc := NewBulkStatusService(faultyOperationRepo{BulkOperationRepository: repository.NewBulkOperationRepository(db), failCreate: true},
			repository.NewBulkStatusRepository(db), f.env.txManager, quiet, WithBulkAutomationEvents(f.env.repo))

		_, err := svc.BulkSetLeadStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, []uint{f.lead.ID}, models.LeadStatusContacted)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create bulk operation")

		var lead models.Lead
		reload(t, db, &lead, f.lead.ID)
		assert.Equal(t, models.LeadStatusNew, lead.Status)
		assert.Empty(t, f.env.eventsOf(models.AutomationEntityLead, f.lead.ID))
		assert.Empty(t, operations(t, db))
	})

	t.Run("refusals carry the offending ids", func(t *testing.T) {
		f := setup(t)
		db := f.env.db
		svc := NewBulkStatusService(repository.NewBulkOperationRepository(db),
			repository.NewBulkStatusRepository(db), f.env.txManager, quiet, WithBulkAutomationEvents(f.env.repo))
		mine := f.env.createLead(f.env.other, models.LeadStatusNew)

		_, err := svc.BulkSetLeadStatus(context.Background(), f.env.other.ID, models.RoleSales, []uint{mine.ID, f.lead.ID}, models.LeadStatusContacted)
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		appErr, ok := apperrors.AsAppError(err)
		require.True(t, ok)
		assert.Equal(t, []uint{f.lead.ID}, appErr.Details["forbidden_ids"])

		var reloaded models.Lead
		reload(t, db, &reloaded, mine.ID)
		assert.Equal(t, models.LeadStatusNew, reloaded.Status, "the caller's own lead is untouched by the refused batch")
		assert.Empty(t, f.env.eventsOf(models.AutomationEntityLead, mine.ID))

		tooMany := make([]uint, models.MaxBulkStatusItems+1)
		for i := range tooMany {
			tooMany[i] = uint(i + 1)
		}
		_, err = svc.BulkSetTaskStatus(context.Background(), f.env.admin.ID, models.RoleAdmin, tooMany, models.TaskStatusPending)
		appErr, ok = apperrors.AsAppError(err)
		require.True(t, ok, fmt.Sprintf("got %T", err))
		assert.Equal(t, models.MaxBulkStatusItems, appErr.Details["max"])
		assert.Equal(t, models.MaxBulkStatusItems+1, appErr.Details["received"])
	})
}
