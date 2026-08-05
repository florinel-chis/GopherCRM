package integration

import (
	"errors"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"io"
)

// A bulk status update is all-or-nothing. Every listed row is checked — it
// exists, the caller is allowed to touch it, the transition is legal — before
// any row is written, and the whole thing runs inside one transaction, so a
// single bad item leaves the database exactly as it was. These tests compare
// what the API *reports* against what was actually *persisted*; a mock cannot
// catch a transaction that commits half a batch.

func setupBulkStatusDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Lead{},
		&models.Customer{},
		&models.Ticket{},
		&models.Task{},
		&models.APIKey{},
		&models.RefreshToken{},
		&models.BulkOperation{},
		&models.BulkOperationItem{},
	))
	return db
}

func newBulkStatusService(db *gorm.DB) service.BulkOperationService {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return service.NewBulkOperationService(
		repository.NewBulkOperationRepository(db),
		repository.NewBulkRepository(db),
		repository.NewUserRepository(db),
		repository.NewLeadRepository(db),
		repository.NewCustomerRepository(db),
		repository.NewTaskRepository(db),
		repository.NewTicketRepository(db),
		utils.NewTransactionManager(db),
		logger,
	)
}

func seedStatusUser(t *testing.T, db *gorm.DB, email string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{
		Email:     email,
		Password:  "hashed",
		FirstName: "Status",
		LastName:  "Actor",
		Role:      role,
		IsActive:  true,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func seedStatusLead(t *testing.T, db *gorm.DB, ownerID uint, email string, status models.LeadStatus) *models.Lead {
	t.Helper()
	lead := &models.Lead{
		FirstName: "Bulk",
		LastName:  "Lead",
		Email:     email,
		Status:    status,
		OwnerID:   ownerID,
	}
	require.NoError(t, db.Create(lead).Error)
	return lead
}

func seedStatusTicket(t *testing.T, db *gorm.DB, assignedTo *uint, status models.TicketStatus) *models.Ticket {
	t.Helper()
	ticket := &models.Ticket{
		Title:        "Bulk ticket",
		Description:  "Bulk ticket description",
		Status:       status,
		Priority:     models.TicketPriorityMedium,
		CustomerID:   1,
		AssignedToID: assignedTo,
	}
	require.NoError(t, db.Create(ticket).Error)
	return ticket
}

func seedStatusTask(t *testing.T, db *gorm.DB, assignedTo uint, status models.TaskStatus) *models.Task {
	t.Helper()
	task := &models.Task{
		Title:        "Bulk task",
		Status:       status,
		Priority:     models.TaskPriorityMedium,
		AssignedToID: assignedTo,
	}
	require.NoError(t, db.Create(task).Error)
	return task
}

func leadStatus(t *testing.T, db *gorm.DB, id uint) models.LeadStatus {
	t.Helper()
	var lead models.Lead
	require.NoError(t, db.First(&lead, id).Error)
	return lead.Status
}

func ticketStatus(t *testing.T, db *gorm.DB, id uint) models.TicketStatus {
	t.Helper()
	var ticket models.Ticket
	require.NoError(t, db.First(&ticket, id).Error)
	return ticket.Status
}

func taskStatus(t *testing.T, db *gorm.DB, id uint) models.TaskStatus {
	t.Helper()
	var task models.Task
	require.NoError(t, db.First(&task, id).Error)
	return task.Status
}

// detailIDs reads a list of IDs back out of an AppError's details.
func detailIDs(t *testing.T, err error, key string) []uint {
	t.Helper()
	appErr, ok := apperrors.AsAppError(err)
	require.True(t, ok, "error must be an AppError, got %T", err)
	raw, ok := appErr.Details[key]
	require.True(t, ok, "details must name %q, got %#v", key, appErr.Details)
	ids, ok := raw.([]uint)
	require.True(t, ok, "%s must be a []uint, got %#v", key, raw)
	return ids
}

// --- Leads -------------------------------------------------------------------

func TestBulkSetLeadStatus_AdminUpdatesEveryLead(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-leads@example.com", models.RoleAdmin)
	owner := seedStatusUser(t, db, "sales-leads@example.com", models.RoleSales)
	first := seedStatusLead(t, db, owner.ID, "lead-a@example.com", models.LeadStatusNew)
	second := seedStatusLead(t, db, owner.ID, "lead-b@example.com", models.LeadStatusNew)

	result, err := svc.BulkSetLeadStatus(admin.ID, models.RoleAdmin,
		[]uint{first.ID, second.ID}, models.LeadStatusQualified)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Updated)
	assert.Equal(t, models.LeadStatusQualified, leadStatus(t, db, first.ID))
	assert.Equal(t, models.LeadStatusQualified, leadStatus(t, db, second.ID))
}

// The all-or-nothing proof: the caller owns the first lead but not the second,
// and the first must be left exactly as it was.
func TestBulkSetLeadStatus_SalesNotOwner_LeavesEarlierLeadsUnchanged(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	sales := seedStatusUser(t, db, "sales-owner@example.com", models.RoleSales)
	other := seedStatusUser(t, db, "sales-other@example.com", models.RoleSales)
	mine := seedStatusLead(t, db, sales.ID, "mine@example.com", models.LeadStatusNew)
	theirs := seedStatusLead(t, db, other.ID, "theirs@example.com", models.LeadStatusNew)

	result, err := svc.BulkSetLeadStatus(sales.ID, models.RoleSales,
		[]uint{mine.ID, theirs.ID}, models.LeadStatusQualified)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, errors.Is(err, apperrors.ErrForbidden))
	assert.Equal(t, []uint{theirs.ID}, detailIDs(t, err, "forbidden_ids"))

	assert.Equal(t, models.LeadStatusNew, leadStatus(t, db, mine.ID),
		"the lead listed before the rejected one must not have been updated")
	assert.Equal(t, models.LeadStatusNew, leadStatus(t, db, theirs.ID))
}

func TestBulkSetLeadStatus_SalesOwnsEveryLead_Succeeds(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	sales := seedStatusUser(t, db, "sales-all@example.com", models.RoleSales)
	first := seedStatusLead(t, db, sales.ID, "own-a@example.com", models.LeadStatusNew)
	second := seedStatusLead(t, db, sales.ID, "own-b@example.com", models.LeadStatusContacted)

	result, err := svc.BulkSetLeadStatus(sales.ID, models.RoleSales,
		[]uint{first.ID, second.ID}, models.LeadStatusContacted)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Updated)
	assert.Equal(t, models.LeadStatusContacted, leadStatus(t, db, first.ID))
}

func TestBulkSetLeadStatus_MissingID_ChangesNothing(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-missing@example.com", models.RoleAdmin)
	lead := seedStatusLead(t, db, admin.ID, "present@example.com", models.LeadStatusNew)

	result, err := svc.BulkSetLeadStatus(admin.ID, models.RoleAdmin,
		[]uint{lead.ID, 4242}, models.LeadStatusQualified)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, apperrors.IsNotFound(err))
	assert.Equal(t, []uint{4242}, detailIDs(t, err, "missing_ids"))
	assert.Equal(t, models.LeadStatusNew, leadStatus(t, db, lead.ID))
}

// A soft-deleted lead is gone as far as the API is concerned.
func TestBulkSetLeadStatus_DeletedLead_IsNotFound(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-deleted@example.com", models.RoleAdmin)
	lead := seedStatusLead(t, db, admin.ID, "deleted@example.com", models.LeadStatusNew)
	require.NoError(t, db.Delete(&models.Lead{}, lead.ID).Error)

	_, err := svc.BulkSetLeadStatus(admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusQualified)

	require.Error(t, err)
	assert.True(t, apperrors.IsNotFound(err))
}

func TestBulkSetLeadStatus_SupportRole_Forbidden(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	support := seedStatusUser(t, db, "support-leads@example.com", models.RoleSupport)
	lead := seedStatusLead(t, db, support.ID, "support-lead@example.com", models.LeadStatusNew)

	_, err := svc.BulkSetLeadStatus(support.ID, models.RoleSupport, []uint{lead.ID}, models.LeadStatusQualified)

	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrForbidden))
	assert.Equal(t, models.LeadStatusNew, leadStatus(t, db, lead.ID))
}

func TestBulkSetLeadStatus_RejectsEmptyAndOversizedLists(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)
	admin := seedStatusUser(t, db, "admin-limits@example.com", models.RoleAdmin)

	_, err := svc.BulkSetLeadStatus(admin.ID, models.RoleAdmin, nil, models.LeadStatusQualified)
	require.Error(t, err)
	appErr, ok := apperrors.AsAppError(err)
	require.True(t, ok)
	assert.Equal(t, 400, appErr.HTTPStatus)

	tooMany := make([]uint, models.MaxBulkStatusItems+1)
	for i := range tooMany {
		tooMany[i] = uint(i + 1)
	}
	_, err = svc.BulkSetLeadStatus(admin.ID, models.RoleAdmin, tooMany, models.LeadStatusQualified)
	require.Error(t, err)
	appErr, ok = apperrors.AsAppError(err)
	require.True(t, ok)
	assert.Equal(t, 400, appErr.HTTPStatus)
}

func TestBulkSetLeadStatus_RejectsInvalidStatus(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-status@example.com", models.RoleAdmin)
	lead := seedStatusLead(t, db, admin.ID, "invalid-status@example.com", models.LeadStatusNew)

	_, err := svc.BulkSetLeadStatus(admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatus("archived"))

	require.Error(t, err)
	appErr, ok := apperrors.AsAppError(err)
	require.True(t, ok)
	assert.Equal(t, 400, appErr.HTTPStatus)
	assert.Equal(t, models.LeadStatusNew, leadStatus(t, db, lead.ID))
}

// --- Tickets -----------------------------------------------------------------

func TestBulkSetTicketStatus_SupportUpdatesOwnTickets(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	support := seedStatusUser(t, db, "support-tickets@example.com", models.RoleSupport)
	first := seedStatusTicket(t, db, &support.ID, models.TicketStatusOpen)
	second := seedStatusTicket(t, db, &support.ID, models.TicketStatusOpen)

	result, err := svc.BulkSetTicketStatus(support.ID, models.RoleSupport,
		[]uint{first.ID, second.ID}, models.TicketStatusInProgress)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Updated)
	assert.Equal(t, models.TicketStatusInProgress, ticketStatus(t, db, first.ID))
	assert.Equal(t, models.TicketStatusInProgress, ticketStatus(t, db, second.ID))
}

func TestBulkSetTicketStatus_SupportNotAssigned_LeavesEarlierTicketsUnchanged(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	support := seedStatusUser(t, db, "support-mine@example.com", models.RoleSupport)
	other := seedStatusUser(t, db, "support-other@example.com", models.RoleSupport)
	mine := seedStatusTicket(t, db, &support.ID, models.TicketStatusOpen)
	theirs := seedStatusTicket(t, db, &other.ID, models.TicketStatusOpen)
	unassigned := seedStatusTicket(t, db, nil, models.TicketStatusOpen)

	_, err := svc.BulkSetTicketStatus(support.ID, models.RoleSupport,
		[]uint{mine.ID, theirs.ID, unassigned.ID}, models.TicketStatusResolved)

	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrForbidden))
	assert.Equal(t, []uint{theirs.ID, unassigned.ID}, detailIDs(t, err, "forbidden_ids"))
	assert.Equal(t, models.TicketStatusOpen, ticketStatus(t, db, mine.ID),
		"the ticket listed before the rejected one must not have been updated")
}

func TestBulkSetTicketStatus_SalesAndCustomerAreForbidden(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	sales := seedStatusUser(t, db, "sales-tickets@example.com", models.RoleSales)
	customer := seedStatusUser(t, db, "customer-tickets@example.com", models.RoleCustomer)
	ticket := seedStatusTicket(t, db, &sales.ID, models.TicketStatusOpen)

	_, err := svc.BulkSetTicketStatus(sales.ID, models.RoleSales, []uint{ticket.ID}, models.TicketStatusResolved)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrForbidden))

	_, err = svc.BulkSetTicketStatus(customer.ID, models.RoleCustomer, []uint{ticket.ID}, models.TicketStatusResolved)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrForbidden))

	assert.Equal(t, models.TicketStatusOpen, ticketStatus(t, db, ticket.ID))
}

// The single-item update refuses to reopen a closed ticket; going through the
// bulk endpoint must not be a way around that rule.
func TestBulkSetTicketStatus_ReopeningClosedTicket_RollsBackTheBatch(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-tickets@example.com", models.RoleAdmin)
	open := seedStatusTicket(t, db, &admin.ID, models.TicketStatusOpen)
	closed := seedStatusTicket(t, db, &admin.ID, models.TicketStatusClosed)

	_, err := svc.BulkSetTicketStatus(admin.ID, models.RoleAdmin,
		[]uint{open.ID, closed.ID}, models.TicketStatusInProgress)

	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrClosedTicketReopen))
	assert.Equal(t, []uint{closed.ID}, detailIDs(t, err, "closed_ids"))
	assert.Equal(t, models.TicketStatusOpen, ticketStatus(t, db, open.ID))
	assert.Equal(t, models.TicketStatusClosed, ticketStatus(t, db, closed.ID))
}

func TestBulkSetTicketStatus_MissingID_ChangesNothing(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-ticket-missing@example.com", models.RoleAdmin)
	ticket := seedStatusTicket(t, db, &admin.ID, models.TicketStatusOpen)

	_, err := svc.BulkSetTicketStatus(admin.ID, models.RoleAdmin,
		[]uint{ticket.ID, 9999}, models.TicketStatusResolved)

	require.Error(t, err)
	assert.True(t, apperrors.IsNotFound(err))
	assert.Equal(t, []uint{9999}, detailIDs(t, err, "missing_ids"))
	assert.Equal(t, models.TicketStatusOpen, ticketStatus(t, db, ticket.ID))
}

// --- Tasks -------------------------------------------------------------------

func TestBulkSetTaskStatus_AdminUpdatesAnyTask(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-tasks@example.com", models.RoleAdmin)
	assignee := seedStatusUser(t, db, "assignee-tasks@example.com", models.RoleSupport)
	first := seedStatusTask(t, db, assignee.ID, models.TaskStatusPending)
	second := seedStatusTask(t, db, assignee.ID, models.TaskStatusPending)

	result, err := svc.BulkSetTaskStatus(admin.ID, models.RoleAdmin,
		[]uint{first.ID, second.ID}, models.TaskStatusInProgress)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Updated)
	assert.Equal(t, models.TaskStatusInProgress, taskStatus(t, db, first.ID))
	assert.Equal(t, models.TaskStatusInProgress, taskStatus(t, db, second.ID))
}

func TestBulkSetTaskStatus_NotAssigned_LeavesEarlierTasksUnchanged(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	support := seedStatusUser(t, db, "support-tasks@example.com", models.RoleSupport)
	other := seedStatusUser(t, db, "other-tasks@example.com", models.RoleSupport)
	mine := seedStatusTask(t, db, support.ID, models.TaskStatusPending)
	theirs := seedStatusTask(t, db, other.ID, models.TaskStatusPending)

	_, err := svc.BulkSetTaskStatus(support.ID, models.RoleSupport,
		[]uint{mine.ID, theirs.ID}, models.TaskStatusInProgress)

	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrForbidden))
	assert.Equal(t, []uint{theirs.ID}, detailIDs(t, err, "forbidden_ids"))
	assert.Equal(t, models.TaskStatusPending, taskStatus(t, db, mine.ID),
		"the task listed before the rejected one must not have been updated")
}

// The rollback proof named in the plan: one completed task in the middle of an
// otherwise valid batch aborts the whole request, and the tasks listed before
// it keep their original status.
func TestBulkSetTaskStatus_CompletedTaskInBatch_RollsBackTheBatch(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-completed@example.com", models.RoleAdmin)
	pending := seedStatusTask(t, db, admin.ID, models.TaskStatusPending)
	completed := seedStatusTask(t, db, admin.ID, models.TaskStatusCompleted)
	trailing := seedStatusTask(t, db, admin.ID, models.TaskStatusPending)

	result, err := svc.BulkSetTaskStatus(admin.ID, models.RoleAdmin,
		[]uint{pending.ID, completed.ID, trailing.ID}, models.TaskStatusInProgress)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, errors.Is(err, apperrors.ErrCompletedTaskModify))
	assert.Equal(t, []uint{completed.ID}, detailIDs(t, err, "completed_ids"))

	assert.Equal(t, models.TaskStatusPending, taskStatus(t, db, pending.ID))
	assert.Equal(t, models.TaskStatusCompleted, taskStatus(t, db, completed.ID))
	assert.Equal(t, models.TaskStatusPending, taskStatus(t, db, trailing.ID))
}

// Setting a completed task to "completed" changes nothing and is not a
// forbidden transition.
func TestBulkSetTaskStatus_CompletedToCompleted_IsAllowed(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-idempotent@example.com", models.RoleAdmin)
	completed := seedStatusTask(t, db, admin.ID, models.TaskStatusCompleted)
	pending := seedStatusTask(t, db, admin.ID, models.TaskStatusPending)

	result, err := svc.BulkSetTaskStatus(admin.ID, models.RoleAdmin,
		[]uint{completed.ID, pending.ID}, models.TaskStatusCompleted)

	require.NoError(t, err)
	assert.Equal(t, 2, result.Updated)
	assert.Equal(t, models.TaskStatusCompleted, taskStatus(t, db, pending.ID))
}

func TestBulkSetTaskStatus_MissingID_ChangesNothing(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-task-missing@example.com", models.RoleAdmin)
	task := seedStatusTask(t, db, admin.ID, models.TaskStatusPending)

	_, err := svc.BulkSetTaskStatus(admin.ID, models.RoleAdmin,
		[]uint{task.ID, 7777}, models.TaskStatusCancelled)

	require.Error(t, err)
	assert.True(t, apperrors.IsNotFound(err))
	assert.Equal(t, []uint{7777}, detailIDs(t, err, "missing_ids"))
	assert.Equal(t, models.TaskStatusPending, taskStatus(t, db, task.ID))
}

// The same ID twice is one row, counted once.
func TestBulkSetTaskStatus_DuplicateIDsCountOnce(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-dupes@example.com", models.RoleAdmin)
	task := seedStatusTask(t, db, admin.ID, models.TaskStatusPending)

	result, err := svc.BulkSetTaskStatus(admin.ID, models.RoleAdmin,
		[]uint{task.ID, task.ID}, models.TaskStatusInProgress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.Updated)
	assert.Equal(t, models.TaskStatusInProgress, taskStatus(t, db, task.ID))
}

// Every bulk status update is recorded like the other bulk operations, so a
// rejected batch is visible afterwards as a failed operation.
func TestBulkSetTaskStatus_RecordsTheOperation(t *testing.T) {
	db := setupBulkStatusDB(t)
	svc := newBulkStatusService(db)

	admin := seedStatusUser(t, db, "admin-audit@example.com", models.RoleAdmin)
	task := seedStatusTask(t, db, admin.ID, models.TaskStatusPending)
	completed := seedStatusTask(t, db, admin.ID, models.TaskStatusCompleted)

	_, err := svc.BulkSetTaskStatus(admin.ID, models.RoleAdmin, []uint{task.ID}, models.TaskStatusInProgress)
	require.NoError(t, err)

	_, err = svc.BulkSetTaskStatus(admin.ID, models.RoleAdmin, []uint{completed.ID}, models.TaskStatusPending)
	require.Error(t, err)

	operations, total, err := svc.GetUserBulkOperations(admin.ID, 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)

	statuses := map[models.BulkOperationStatus]int{}
	for _, op := range operations {
		assert.Equal(t, "tasks", op.ResourceType)
		statuses[op.Status]++
	}
	assert.Equal(t, 1, statuses[models.StatusCompleted])
	assert.Equal(t, 1, statuses[models.StatusFailed])
}
