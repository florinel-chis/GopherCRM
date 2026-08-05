package repository

import (
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupAnalyticsTestDB gives each test its own in-memory schema. Production is
// MySQL 8; everything exercised here is deliberately driver-neutral SQL, which
// is the whole reason the time bucketing lives in Go rather than in a
// DATE_FORMAT/strftime expression that only one of the two would understand.
func setupAnalyticsTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Customer{},
		&models.Lead{},
		&models.Ticket{},
		&models.Task{},
	))
	return db
}

// stampTimes forces created_at/updated_at to fixed values. GORM owns those
// columns on write, so a test that needs a specific event time has to set them
// afterwards with UpdateColumns, which bypasses the auto-timestamp callbacks.
func stampTimes(t *testing.T, db *gorm.DB, record interface{}, createdAt, updatedAt time.Time) {
	t.Helper()
	require.NoError(t, db.Model(record).UpdateColumns(map[string]interface{}{
		"created_at": createdAt,
		"updated_at": updatedAt,
	}).Error)
}

func makeUser(t *testing.T, db *gorm.DB, email string) models.User {
	t.Helper()
	user := models.User{Email: email, Password: "x", FirstName: "Test", LastName: "User", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	return user
}

// --- leads -------------------------------------------------------------------

func TestLeadRepository_CountByStatus(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")

	for _, status := range []models.LeadStatus{
		models.LeadStatusNew,
		models.LeadStatusNew,
		models.LeadStatusConverted,
	} {
		lead := models.Lead{FirstName: "A", LastName: "B", Email: string(status) + "@example.com", Status: status, OwnerID: owner.ID}
		require.NoError(t, db.Create(&lead).Error)
	}

	counts, err := repo.CountByStatus()

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"new": 2, "converted": 1}, counts)
}

// Erased leads are soft-deleted; they must not keep inflating the chart.
func TestLeadRepository_CountByStatus_ExcludesSoftDeleted(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")

	live := models.Lead{FirstName: "Live", Email: "live@example.com", Status: models.LeadStatusNew, OwnerID: owner.ID}
	gone := models.Lead{FirstName: "Gone", Email: "gone@example.com", Status: models.LeadStatusNew, OwnerID: owner.ID}
	require.NoError(t, db.Create(&live).Error)
	require.NoError(t, db.Create(&gone).Error)
	require.NoError(t, db.Delete(&gone).Error)

	counts, err := repo.CountByStatus()

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"new": 1}, counts)
}

func TestLeadRepository_CountByStatus_EmptyTable(t *testing.T) {
	repo := NewLeadRepository(setupAnalyticsTestDB(t))

	counts, err := repo.CountByStatus()

	require.NoError(t, err)
	assert.NotNil(t, counts)
	assert.Empty(t, counts)
}

func TestLeadRepository_ListRecent_NewestFirstAndLimited(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	for i, offset := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		lead := models.Lead{FirstName: string(rune('A' + i)), Email: string(rune('a'+i)) + "@example.com", OwnerID: owner.ID}
		require.NoError(t, db.Create(&lead).Error)
		stampTimes(t, db, &lead, base.Add(offset), base.Add(offset))
	}

	leads, err := repo.ListRecent(nil, 2)

	require.NoError(t, err)
	require.Len(t, leads, 2)
	assert.Equal(t, "C", leads[0].FirstName)
	assert.Equal(t, "B", leads[1].FirstName)
	// The owner is preloaded so the activity feed can name the actor.
	assert.Equal(t, "owner@example.com", leads[0].Owner.Email)
}

func TestLeadRepository_ListRecent_ScopedToOwner(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	mine := makeUser(t, db, "mine@example.com")
	theirs := makeUser(t, db, "theirs@example.com")

	require.NoError(t, db.Create(&models.Lead{FirstName: "Mine", Email: "m@example.com", OwnerID: mine.ID}).Error)
	require.NoError(t, db.Create(&models.Lead{FirstName: "Theirs", Email: "t@example.com", OwnerID: theirs.ID}).Error)

	leads, err := repo.ListRecent(&mine.ID, 10)

	require.NoError(t, err)
	require.Len(t, leads, 1)
	assert.Equal(t, "Mine", leads[0].FirstName)
}

func TestLeadRepository_ListRecent_EmptyTableReturnsEmptySlice(t *testing.T) {
	repo := NewLeadRepository(setupAnalyticsTestDB(t))

	leads, err := repo.ListRecent(nil, 5)

	require.NoError(t, err)
	assert.NotNil(t, leads)
	assert.Empty(t, leads)
}

func TestLeadRepository_ListRecentlyConverted(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	newLead := models.Lead{FirstName: "Fresh", Email: "fresh@example.com", Status: models.LeadStatusNew, OwnerID: owner.ID}
	older := models.Lead{FirstName: "Older", Email: "older@example.com", Status: models.LeadStatusConverted, OwnerID: owner.ID}
	newer := models.Lead{FirstName: "Newer", Email: "newer@example.com", Status: models.LeadStatusConverted, OwnerID: owner.ID}
	require.NoError(t, db.Create(&newLead).Error)
	require.NoError(t, db.Create(&older).Error)
	require.NoError(t, db.Create(&newer).Error)
	stampTimes(t, db, &older, base, base)
	stampTimes(t, db, &newer, base, base.Add(time.Hour))

	leads, err := repo.ListRecentlyConverted(10)

	require.NoError(t, err)
	require.Len(t, leads, 2)
	assert.Equal(t, "Newer", leads[0].FirstName)
	assert.Equal(t, "Older", leads[1].FirstName)
}

func TestLeadRepository_ConversionTimestampsSince(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")
	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	tooOld := models.Lead{FirstName: "Old", Email: "old@example.com", Status: models.LeadStatusConverted, OwnerID: owner.ID}
	onCutoff := models.Lead{FirstName: "Edge", Email: "edge@example.com", Status: models.LeadStatusConverted, OwnerID: owner.ID}
	inWindow := models.Lead{FirstName: "In", Email: "in@example.com", Status: models.LeadStatusConverted, OwnerID: owner.ID}
	notConverted := models.Lead{FirstName: "Nope", Email: "nope@example.com", Status: models.LeadStatusQualified, OwnerID: owner.ID}
	for _, lead := range []*models.Lead{&tooOld, &onCutoff, &inWindow, &notConverted} {
		require.NoError(t, db.Create(lead).Error)
	}
	stampTimes(t, db, &tooOld, cutoff, cutoff.Add(-time.Second))
	stampTimes(t, db, &onCutoff, cutoff, cutoff)
	stampTimes(t, db, &inWindow, cutoff, cutoff.Add(48*time.Hour))
	stampTimes(t, db, &notConverted, cutoff, cutoff.Add(72*time.Hour))

	timestamps, err := repo.ConversionTimestampsSince(cutoff)

	require.NoError(t, err)
	require.Len(t, timestamps, 2, "the cutoff is inclusive, the second before it is not, and unconverted leads never count")
	assert.True(t, timestamps[0].UTC().Equal(cutoff))
	assert.True(t, timestamps[1].UTC().Equal(cutoff.Add(48*time.Hour)))
}

func TestLeadRepository_ConversionTimestampsSince_NoRows(t *testing.T) {
	repo := NewLeadRepository(setupAnalyticsTestDB(t))

	timestamps, err := repo.ConversionTimestampsSince(time.Now().AddDate(-1, 0, 0))

	require.NoError(t, err)
	assert.NotNil(t, timestamps)
	assert.Empty(t, timestamps)
}

// --- tickets -----------------------------------------------------------------

func TestTicketRepository_CountByPriority(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTicketRepository(db)

	for _, priority := range []models.TicketPriority{
		models.TicketPriorityUrgent,
		models.TicketPriorityLow,
		models.TicketPriorityLow,
	} {
		require.NoError(t, db.Create(&models.Ticket{Title: "T", Description: "D", Priority: priority, Status: models.TicketStatusOpen}).Error)
	}

	counts, err := repo.CountByPriority()

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"urgent": 1, "low": 2}, counts)
}

func TestTicketRepository_CountByPriority_EmptyTable(t *testing.T) {
	repo := NewTicketRepository(setupAnalyticsTestDB(t))

	counts, err := repo.CountByPriority()

	require.NoError(t, err)
	assert.Empty(t, counts)
}

func TestTicketRepository_ListRecent_NewestFirst(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTicketRepository(db)
	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

	first := models.Ticket{Title: "First", Description: "d", Status: models.TicketStatusOpen}
	second := models.Ticket{Title: "Second", Description: "d", Status: models.TicketStatusOpen}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Create(&second).Error)
	stampTimes(t, db, &first, base, base)
	stampTimes(t, db, &second, base.Add(time.Hour), base.Add(time.Hour))

	tickets, err := repo.ListRecent(10)

	require.NoError(t, err)
	require.Len(t, tickets, 2)
	assert.Equal(t, "Second", tickets[0].Title)
	assert.Equal(t, "First", tickets[1].Title)
}

// Both resolved and closed are resolutions, mirroring CountOpen's split; open
// and in_progress are not.
func TestTicketRepository_ListRecentlyResolved(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTicketRepository(db)
	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

	resolved := models.Ticket{Title: "Resolved", Description: "d", Status: models.TicketStatusResolved}
	closed := models.Ticket{Title: "Closed", Description: "d", Status: models.TicketStatusClosed}
	open := models.Ticket{Title: "Open", Description: "d", Status: models.TicketStatusOpen}
	inProgress := models.Ticket{Title: "InProgress", Description: "d", Status: models.TicketStatusInProgress}
	for _, ticket := range []*models.Ticket{&resolved, &closed, &open, &inProgress} {
		require.NoError(t, db.Create(ticket).Error)
	}
	stampTimes(t, db, &resolved, base, base.Add(2*time.Hour))
	stampTimes(t, db, &closed, base, base.Add(time.Hour))
	stampTimes(t, db, &open, base, base.Add(3*time.Hour))
	stampTimes(t, db, &inProgress, base, base.Add(4*time.Hour))

	tickets, err := repo.ListRecentlyResolved(10)

	require.NoError(t, err)
	require.Len(t, tickets, 2)
	assert.Equal(t, "Resolved", tickets[0].Title)
	assert.Equal(t, "Closed", tickets[1].Title)
}

// --- tasks -------------------------------------------------------------------

func TestTaskRepository_CountByStatus(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	user := makeUser(t, db, "assignee@example.com")

	for _, status := range []models.TaskStatus{
		models.TaskStatusPending,
		models.TaskStatusPending,
		models.TaskStatusCancelled,
	} {
		require.NoError(t, db.Create(&models.Task{Title: "T", Status: status, Priority: models.TaskPriorityMedium, AssignedToID: user.ID}).Error)
	}

	counts, err := repo.CountByStatus()

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"pending": 2, "cancelled": 1}, counts)
}

func TestTaskRepository_CountByStatus_EmptyTable(t *testing.T) {
	repo := NewTaskRepository(setupAnalyticsTestDB(t))

	counts, err := repo.CountByStatus()

	require.NoError(t, err)
	assert.Empty(t, counts)
}

func TestTaskRepository_ListUpcoming_OrdersByDueDateAndSkipsClosedWork(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	user := makeUser(t, db, "assignee@example.com")
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	due := func(d time.Duration) *time.Time { at := base.Add(d); return &at }

	require.NoError(t, db.Create(&models.Task{Title: "Later", Status: models.TaskStatusPending, DueDate: due(48 * time.Hour), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "Sooner", Status: models.TaskStatusInProgress, DueDate: due(24 * time.Hour), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "Done", Status: models.TaskStatusCompleted, DueDate: due(time.Hour), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "Cancelled", Status: models.TaskStatusCancelled, DueDate: due(time.Hour), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "NoDueDate", Status: models.TaskStatusPending, AssignedToID: user.ID}).Error)

	tasks, err := repo.ListUpcoming(nil, 10)

	require.NoError(t, err)
	require.Len(t, tasks, 2, "completed, cancelled and undated tasks are not upcoming work")
	assert.Equal(t, "Sooner", tasks[0].Title)
	assert.Equal(t, "Later", tasks[1].Title)
	assert.Equal(t, "assignee@example.com", tasks[0].AssignedTo.Email)
}

// Overdue tasks are the ones that most need attention, so the due-soonest list
// keeps them rather than starting the window at now.
func TestTaskRepository_ListUpcoming_KeepsOverdueTasks(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	user := makeUser(t, db, "assignee@example.com")

	overdue := time.Now().Add(-72 * time.Hour)
	require.NoError(t, db.Create(&models.Task{Title: "Overdue", Status: models.TaskStatusPending, DueDate: &overdue, AssignedToID: user.ID}).Error)

	tasks, err := repo.ListUpcoming(nil, 10)

	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Overdue", tasks[0].Title)
}

func TestTaskRepository_ListUpcoming_ScopedToAssignee(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	mine := makeUser(t, db, "mine@example.com")
	theirs := makeUser(t, db, "theirs@example.com")
	dueAt := time.Now().Add(time.Hour)

	require.NoError(t, db.Create(&models.Task{Title: "Mine", Status: models.TaskStatusPending, DueDate: &dueAt, AssignedToID: mine.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "Theirs", Status: models.TaskStatusPending, DueDate: &dueAt, AssignedToID: theirs.ID}).Error)

	tasks, err := repo.ListUpcoming(&mine.ID, 10)

	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Mine", tasks[0].Title)
}

func TestTaskRepository_ListUpcoming_HonoursLimit(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	user := makeUser(t, db, "assignee@example.com")

	for i := 0; i < 5; i++ {
		dueAt := time.Now().Add(time.Duration(i) * time.Hour)
		require.NoError(t, db.Create(&models.Task{Title: "T", Status: models.TaskStatusPending, DueDate: &dueAt, AssignedToID: user.ID}).Error)
	}

	tasks, err := repo.ListUpcoming(nil, 2)

	require.NoError(t, err)
	assert.Len(t, tasks, 2)
}

func TestTaskRepository_ListDueBetween_WindowIsInclusiveOnBothEnds(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	user := makeUser(t, db, "assignee@example.com")

	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	at := func(d time.Duration) *time.Time { moment := from.Add(d); return &moment }

	require.NoError(t, db.Create(&models.Task{Title: "BeforeWindow", Status: models.TaskStatusPending, DueDate: at(-time.Second), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "OnFrom", Status: models.TaskStatusPending, DueDate: at(0), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "Middle", Status: models.TaskStatusPending, DueDate: at(72 * time.Hour), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "OnTo", Status: models.TaskStatusPending, DueDate: at(7 * 24 * time.Hour), AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "AfterWindow", Status: models.TaskStatusPending, DueDate: at(7*24*time.Hour + time.Second), AssignedToID: user.ID}).Error)

	tasks, err := repo.ListDueBetween(nil, from, to, 10)

	require.NoError(t, err)
	require.Len(t, tasks, 3)
	assert.Equal(t, []string{"OnFrom", "Middle", "OnTo"}, []string{tasks[0].Title, tasks[1].Title, tasks[2].Title})
}

func TestTaskRepository_ListDueBetween_ScopedToAssignee(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	mine := makeUser(t, db, "mine@example.com")
	theirs := makeUser(t, db, "theirs@example.com")

	from := time.Now()
	to := from.AddDate(0, 0, 7)
	dueAt := from.Add(time.Hour)

	require.NoError(t, db.Create(&models.Task{Title: "Mine", Status: models.TaskStatusPending, DueDate: &dueAt, AssignedToID: mine.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "Theirs", Status: models.TaskStatusPending, DueDate: &dueAt, AssignedToID: theirs.ID}).Error)

	tasks, err := repo.ListDueBetween(&mine.ID, from, to, 10)

	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Mine", tasks[0].Title)
}

func TestTaskRepository_ListDueBetween_ExcludesClosedWork(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	user := makeUser(t, db, "assignee@example.com")

	from := time.Now()
	to := from.AddDate(0, 0, 7)
	dueAt := from.Add(time.Hour)

	require.NoError(t, db.Create(&models.Task{Title: "Done", Status: models.TaskStatusCompleted, DueDate: &dueAt, AssignedToID: user.ID}).Error)
	require.NoError(t, db.Create(&models.Task{Title: "Cancelled", Status: models.TaskStatusCancelled, DueDate: &dueAt, AssignedToID: user.ID}).Error)

	tasks, err := repo.ListDueBetween(nil, from, to, 10)

	require.NoError(t, err)
	assert.Empty(t, tasks)
	assert.NotNil(t, tasks)
}

func TestTaskRepository_ListRecentlyCompleted(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewTaskRepository(db)
	user := makeUser(t, db, "assignee@example.com")
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	older := models.Task{Title: "Older", Status: models.TaskStatusCompleted, AssignedToID: user.ID}
	newer := models.Task{Title: "Newer", Status: models.TaskStatusCompleted, AssignedToID: user.ID}
	pending := models.Task{Title: "Pending", Status: models.TaskStatusPending, AssignedToID: user.ID}
	for _, task := range []*models.Task{&older, &newer, &pending} {
		require.NoError(t, db.Create(task).Error)
	}
	stampTimes(t, db, &older, base, base)
	stampTimes(t, db, &newer, base, base.Add(time.Hour))
	stampTimes(t, db, &pending, base, base.Add(2*time.Hour))

	tasks, err := repo.ListRecentlyCompleted(10)

	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "Newer", tasks[0].Title)
	assert.Equal(t, "Older", tasks[1].Title)
	assert.Equal(t, "assignee@example.com", tasks[0].AssignedTo.Email)
}
