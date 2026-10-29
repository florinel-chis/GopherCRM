package service

// Shared fixture of the follow-up automation regression roots (issue #164).
// Every root runs the real engine and services over the real repositories on
// a real database — a file-backed SQLite database with the production
// pragmas and the one-connection pool, and a disposable MySQL schema when
// TEST_MYSQL_DSN is set — and every assertion reads committed rows outside
// any transaction. The clock is injected so waits and schedule passes are
// deterministic; the mailer is the recording stand-in of the quote roots.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const automationTestTimeout = 20 * time.Second

var automationTestSeq atomic.Uint64

// Canary values of the test lead. The privacy root asserts none of them ever
// reaches a log line; the event roots assert none of them reaches an event.
const (
	automationCanaryFirstName = "Zelda-Canary"
	automationCanaryLastName  = "Fitzgerald-Canary"
	automationCanaryPhone     = "+40 700 CANARY 1"
	automationCanaryNotes     = "notes-canary: met at the Bucharest fair"
)

func automationGormConfig() *gorm.Config {
	cfg := &gorm.Config{Logger: logger.Discard}
	cfg.DisableForeignKeyConstraintWhenMigrating = true
	return cfg
}

// migrateAutomationTestSchema creates every table the automation flows and
// the lead, deal, form and bulk write paths touch.
func migrateAutomationTestSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		&models.Deal{}, &models.DealStageChange{}, &models.Label{}, &models.Task{},
		&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{},
		&models.BulkOperation{}, &models.BulkOperationItem{}, &models.Ticket{}, &models.TicketComment{},
		&models.AutomationRule{}, &models.AutomationStep{}, &models.AutomationEvent{},
		&models.AutomationRun{}, &models.AutomationRunStep{}, &models.Notification{},
	))
}

// openAutomationSQLite is a file-backed database in a per-test directory
// with the production pragmas and the pool pinned to one connection.
func openAutomationSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "automation.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), automationGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateAutomationTestSchema(t, db)
	return db
}

// openAutomationMySQL creates a uniquely named disposable schema on the
// server identified by TEST_MYSQL_DSN and drops it afterwards. Without the
// variable the root skips; a configured but unreachable server fails.
func openAutomationMySQL(t *testing.T, maxConns int) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, automationTestTimeout, automationTestTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_automation_%d_%d", time.Now().UnixNano(), automationTestSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), automationGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateAutomationTestSchema(t, db)
	return db
}

func isMySQL(db *gorm.DB) bool {
	return db.Dialector.Name() == "mysql"
}

// automationClock is the injected time source.
type automationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *automationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *automationClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func (c *automationClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// automationEnv is the real engine and services over one database with an
// admin, a sales owner and a second sales user.
type automationEnv struct {
	t             *testing.T
	db            *gorm.DB
	repo          repository.AutomationRepository
	notifications repository.NotificationRepository
	tasks         repository.TaskRepository
	users         repository.UserRepository
	leads         repository.LeadRepository
	deals         repository.DealRepository
	customers     repository.CustomerRepository
	mailer        *quoteTestMailer
	txManager     *utils.TransactionManager
	clock         *automationClock
	engine        *AutomationEngine
	svc           *automationService
	admin         *models.User
	sales         *models.User
	other         *models.User
}

func newAutomationEnv(t *testing.T, db *gorm.DB) *automationEnv {
	t.Helper()
	if utils.Logger == nil {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	}
	env := &automationEnv{
		t:             t,
		db:            db,
		repo:          repository.NewAutomationRepository(db),
		notifications: repository.NewNotificationRepository(db),
		tasks:         repository.NewTaskRepository(db),
		users:         repository.NewUserRepository(db),
		leads:         repository.NewLeadRepository(db),
		deals:         repository.NewDealRepository(db),
		customers:     repository.NewCustomerRepository(db),
		mailer:        &quoteTestMailer{},
		txManager:     utils.NewTransactionManager(db),
		clock:         &automationClock{now: time.Date(2026, time.October, 28, 10, 0, 0, 0, time.UTC)},
	}
	env.rebuild()
	env.admin = env.createUser("admin", models.RoleAdmin, true)
	env.sales = env.createUser("sales", models.RoleSales, true)
	env.other = env.createUser("other", models.RoleSales, true)
	return env
}

// rebuild replaces the engine and the service with fresh instances on the
// same database and clock: what a process restart looks like to the rows.
func (e *automationEnv) rebuild() {
	e.engine = NewAutomationEngine(e.repo, e.notifications, e.tasks, e.users, e.mailer, e.txManager)
	e.engine.SetClock(e.clock.Now)
	e.svc = NewAutomationService(e.repo, e.users, e.engine, e.txManager).(*automationService)
	e.svc.now = e.clock.Now
}

func (e *automationEnv) createUser(label string, role models.UserRole, active bool) *models.User {
	e.t.Helper()
	seq := automationTestSeq.Add(1)
	user := &models.User{
		Email:     fmt.Sprintf("%s-%d@automation.test", label, seq),
		Password:  "x",
		FirstName: "Staff",
		LastName:  label,
		Role:      role,
		IsActive:  true,
	}
	require.NoError(e.t, e.db.Create(user).Error)
	if !active {
		require.NoError(e.t, e.db.Model(user).Update("is_active", false).Error)
		user.IsActive = false
	}
	return user
}

// createLead inserts a lead with the canary values directly, bypassing the
// service so no event is recorded for the fixture itself.
func (e *automationEnv) createLead(owner *models.User, status models.LeadStatus) *models.Lead {
	e.t.Helper()
	seq := automationTestSeq.Add(1)
	lead := &models.Lead{
		FirstName: automationCanaryFirstName,
		LastName:  automationCanaryLastName,
		Email:     fmt.Sprintf("zelda-canary-%d@example.com", seq),
		Phone:     automationCanaryPhone,
		Company:   "Canary Works",
		Source:    "trade_show",
		Status:    status,
		Notes:     automationCanaryNotes,
		OwnerID:   owner.ID,
	}
	require.NoError(e.t, e.db.Create(lead).Error)
	return lead
}

func (e *automationEnv) createCustomer() *models.Customer {
	e.t.Helper()
	seq := automationTestSeq.Add(1)
	customer := &models.Customer{FirstName: "Cust", LastName: "Omer", Email: fmt.Sprintf("customer-%d@example.com", seq)}
	require.NoError(e.t, e.db.Create(customer).Error)
	return customer
}

func (e *automationEnv) createDeal(owner *models.User, stage models.DealStage, amount int64, currency string, customerID, leadID *uint) *models.Deal {
	e.t.Helper()
	deal := &models.Deal{
		Title: "Engine licences", Stage: stage, AmountCents: amount, Currency: currency,
		Probability: stage.DefaultProbability(), OwnerID: owner.ID, CustomerID: customerID, LeadID: leadID,
	}
	require.NoError(e.t, e.db.Create(deal).Error)
	return deal
}

// step builders. The text of a task, an email and a notification carries
// placeholders of the rule's entity type: the lead builders render lead.*
// values, the deal builders deal.* values. A placeholder of the other entity
// type is an unknown placeholder and the rule is refused. A task may only
// carry the non-personal placeholders (ids, status, source, deal fields): a
// task survives the erasure of its lead, so the name, email, phone and
// company placeholders the email and notify builders use are refused in it.
func waitStep(minutes int64) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepWait, Config: models.AutomationStepConfig{DurationMinutes: minutes}}
}

func conditionStep(field, operator string, value interface{}, elseBranch string, elsePosition int) models.AutomationStep {
	raw, _ := json.Marshal(value)
	return models.AutomationStep{StepType: models.AutomationStepCondition, Config: models.AutomationStepConfig{
		Field: field, Operator: operator, Value: raw, Else: elseBranch, ElsePosition: elsePosition,
	}}
}

func taskStep(recipient string, userID uint, dueInDays int) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepCreateTask, Config: models.AutomationStepConfig{
		Recipient: recipient, UserID: userID, Title: "Follow up with lead {{lead.id}}", Description: "Status {{lead.status}}, source {{lead.source}}", DueInDays: dueInDays,
	}}
}

func assignStep(userID uint) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepAssignOwner, Config: models.AutomationStepConfig{UserID: userID}}
}

func emailStep(recipient string, userID uint) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepSendEmail, Config: models.AutomationStepConfig{
		Recipient: recipient, UserID: userID, Subject: "EMAIL-SUBJECT-CANARY {{lead.first_name}}", Body: "EMAIL-BODY-CANARY {{lead.email}}",
	}}
}

func notifyStep(recipient string, userID uint) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{
		Recipient: recipient, UserID: userID, Title: "NOTIFY-TITLE-CANARY {{lead.last_name}}", Body: "NOTIFY-BODY-CANARY {{lead.company}}",
	}}
}

// dealTaskStep is the create_task step of a deal rule.
func dealTaskStep(recipient string, userID uint, dueInDays int) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepCreateTask, Config: models.AutomationStepConfig{
		Recipient: recipient, UserID: userID, Title: "Follow up on {{deal.title}}", Description: "Stage {{deal.stage}}, {{deal.amount}}", DueInDays: dueInDays,
	}}
}

// dealNotifyStep is the notify step of a deal rule.
func dealNotifyStep(recipient string, userID uint) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{
		Recipient: recipient, UserID: userID, Title: "NOTIFY-TITLE-CANARY {{deal.title}}", Body: "NOTIFY-BODY-CANARY {{deal.stage}}",
	}}
}

// createRule creates an enabled rule through the service.
func (e *automationEnv) createRule(entityType, triggerType string, trigger models.AutomationTrigger, steps ...models.AutomationStep) *models.AutomationRule {
	e.t.Helper()
	rule := &models.AutomationRule{
		Name: "Rule " + triggerType, EntityType: entityType, TriggerType: triggerType, Trigger: trigger, Enabled: true, Steps: steps,
	}
	require.NoError(e.t, e.svc.CreateRule(rule, e.admin.ID))
	stored, err := e.svc.GetRule(rule.ID)
	require.NoError(e.t, err)
	return stored
}

func (e *automationEnv) leadRule(triggerType string, steps ...models.AutomationStep) *models.AutomationRule {
	e.t.Helper()
	return e.createRule(models.AutomationEntityLead, triggerType, models.AutomationTrigger{}, steps...)
}

// recordEvent writes one outbox event directly, as a write path would.
func (e *automationEnv) recordEvent(entityType string, entityID uint, eventType, from, to, origin string) *models.AutomationEvent {
	e.t.Helper()
	event := &models.AutomationEvent{EntityType: entityType, EntityID: entityID, EventType: eventType, FromValue: from, ToValue: to, Origin: origin}
	require.NoError(e.t, e.repo.RecordEvent(event))
	return event
}

func (e *automationEnv) dispatch() int {
	e.t.Helper()
	started, err := e.engine.Dispatch(context.Background())
	require.NoError(e.t, err)
	return started
}

func (e *automationEnv) executeDue() {
	e.t.Helper()
	require.NoError(e.t, e.engine.ExecuteDue(context.Background()))
}

func (e *automationEnv) tick() {
	e.t.Helper()
	require.NoError(e.t, e.engine.Tick(context.Background()))
}

func (e *automationEnv) run(id uint) *models.AutomationRun {
	e.t.Helper()
	run, err := e.repo.GetRunByID(id)
	require.NoError(e.t, err)
	return run
}

func (e *automationEnv) runsOf(ruleID uint) []models.AutomationRun {
	e.t.Helper()
	var runs []models.AutomationRun
	require.NoError(e.t, e.db.Where("rule_id = ?", ruleID).Order("id ASC").Find(&runs).Error)
	return runs
}

func (e *automationEnv) allRuns() []models.AutomationRun {
	e.t.Helper()
	var runs []models.AutomationRun
	require.NoError(e.t, e.db.Order("id ASC").Find(&runs).Error)
	return runs
}

func (e *automationEnv) stepsOf(runID uint) []models.AutomationRunStep {
	e.t.Helper()
	steps, err := e.repo.ListRunSteps(runID)
	require.NoError(e.t, err)
	return steps
}

func (e *automationEnv) events() []models.AutomationEvent {
	e.t.Helper()
	var events []models.AutomationEvent
	require.NoError(e.t, e.db.Order("id ASC").Find(&events).Error)
	return events
}

func (e *automationEnv) eventsOf(entityType string, entityID uint) []models.AutomationEvent {
	e.t.Helper()
	var events []models.AutomationEvent
	require.NoError(e.t, e.db.Where("entity_type = ? AND entity_id = ?", entityType, entityID).Order("id ASC").Find(&events).Error)
	return events
}

func (e *automationEnv) tasksList() []models.Task {
	e.t.Helper()
	var tasks []models.Task
	require.NoError(e.t, e.db.Order("id ASC").Find(&tasks).Error)
	return tasks
}

func (e *automationEnv) notificationsList() []models.Notification {
	e.t.Helper()
	var notifications []models.Notification
	require.NoError(e.t, e.db.Order("id ASC").Find(&notifications).Error)
	return notifications
}

func (e *automationEnv) lead(id uint) *models.Lead {
	e.t.Helper()
	var lead models.Lead
	require.NoError(e.t, e.db.Unscoped().First(&lead, id).Error)
	return &lead
}

func (e *automationEnv) count(model interface{}) int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Model(model).Count(&count).Error)
	return count
}

// eventTypes lists the event types of a slice, in order.
func eventTypes(events []models.AutomationEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.EventType)
	}
	return types
}

// startRun records an event, dispatches it and returns the single run the
// rule started, still pending.
//
// The roots create several enabled rules with the same trigger in one
// environment, one per subtest, and the dispatcher rightly starts one run per
// matching rule (the dispatch root asserts exactly that). Here the subtests
// want only the run of the rule under test, so the pending runs the same
// event started for the earlier rules are removed before anything executes
// them; they have no step row and no effect yet, so nothing else changes.
func (e *automationEnv) startRun(rule *models.AutomationRule, lead *models.Lead) *models.AutomationRun {
	e.t.Helper()
	before := len(e.runsOf(rule.ID))
	event := e.recordEvent(models.AutomationEntityLead, lead.ID, rule.TriggerType, "", "", models.AutomationOriginUser)
	e.dispatch()
	runs := e.runsOf(rule.ID)
	require.Len(e.t, runs, before+1, "the event starts exactly one run of the rule")
	require.NoError(e.t, e.db.
		Where("event_id = ? AND rule_id <> ? AND status = ?", event.ID, rule.ID, models.AutomationRunPending).
		Delete(&models.AutomationRun{}).Error, "the sibling runs of the other rules on the same trigger are removed")
	return &runs[len(runs)-1]
}
