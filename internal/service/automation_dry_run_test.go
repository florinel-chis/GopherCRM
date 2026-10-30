package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-10: a dry run returns a per-step report — outcome, simulated time and
// details such as the resolved user ids, the task due date and link, the
// owner from/to and the branch taken; waits advance the simulated clock
// without pausing and earlier simulated assigns feed later conditions; the
// dry run changes no row count or column value in any table, makes zero
// Mailer.Send calls and creates no run, notification, task or event.

func TestAutomationDryRunNoSideEffectsSQLite(t *testing.T) {
	runAutomationDryRunNoSideEffects(t, openAutomationSQLite(t))
}

func TestAutomationDryRunNoSideEffectsMySQL(t *testing.T) {
	runAutomationDryRunNoSideEffects(t, openAutomationMySQL(t, 2))
}

// snapshotDatabase reads every row of every table as the engine stores it,
// so a before/after comparison catches any changed count or column value.
func snapshotDatabase(t *testing.T, db *gorm.DB) map[string][]map[string]interface{} {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	snapshot := map[string][]map[string]interface{}{}
	for _, table := range tables {
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		var rows []map[string]interface{}
		require.NoError(t, db.Table(table).Order("1").Find(&rows).Error, table)
		for _, row := range rows {
			for column, value := range row {
				if b, ok := value.([]byte); ok {
					row[column] = string(b)
				}
			}
		}
		snapshot[table] = rows
	}
	return snapshot
}

func runAutomationDryRunNoSideEffects(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	customer := env.createCustomer()
	lead := env.createLead(env.sales, models.LeadStatusNew)
	deal := env.createDeal(env.sales, models.DealStageProposal, 250000, "EUR", &customer.ID, nil)
	rule := env.leadRule(models.AutomationEventLeadCreated,
		waitStep(120),
		assignStep(env.other.ID),
		conditionStep("owner_id", "eq", env.other.ID, models.AutomationElseStop, 0),
		taskStep(models.AutomationRecipientOwner, 0, 3),
		emailStep(models.AutomationRecipientUser, env.sales.ID),
		notifyStep(models.AutomationRecipientOwner, 0))
	dealRule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{},
		conditionStep("stage", "eq", "won", models.AutomationElseJump, 3),
		dealNotifyStep(models.AutomationRecipientOwner, 0),
		dealTaskStep(models.AutomationRecipientOwner, 0, 1))
	// Something to compare against: an unprocessed event, a run and a
	// notification already in the tables.
	env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadCreated, "", "", models.AutomationOriginUser)
	env.dispatch()
	env.executeDue()
	require.Equal(t, models.AutomationRunWaiting, env.runsOf(rule.ID)[0].Status, "the real run is parked at its wait")
	env.mailer.reset(nil, nil)

	before := snapshotDatabase(t, db)
	start := env.clock.Now().UTC()

	report, err := env.svc.DryRun(rule.ID, models.AutomationEntityLead, lead.ID)
	require.NoError(t, err)
	assert.Equal(t, rule.ID, report.RuleID)
	assert.Equal(t, rule.Version, report.RuleVersion)
	assert.Equal(t, lead.ID, report.EntityID)
	assert.WithinDuration(t, start, report.SimulatedStart, time.Second)
	assert.WithinDuration(t, start.Add(120*time.Minute), report.SimulatedEnd, time.Second)
	require.Len(t, report.Steps, 6)

	wait := report.Steps[0]
	assert.Equal(t, models.AutomationStepWait, wait.StepType)
	assert.Equal(t, models.AutomationOutcomeWaiting, wait.Outcome)
	assert.WithinDuration(t, start, wait.SimulatedAt, time.Second)
	assert.Equal(t, start.Add(120*time.Minute).Format(time.RFC3339), wait.Details["resume_at"])

	assign := report.Steps[1]
	assert.WithinDuration(t, start.Add(120*time.Minute), assign.SimulatedAt, time.Second, "the wait advanced the simulated clock")
	assert.Equal(t, models.AutomationOutcomeSucceeded, assign.Outcome)
	assert.EqualValues(t, env.sales.ID, assign.Details["from_owner_id"])
	assert.EqualValues(t, env.other.ID, assign.Details["to_owner_id"])

	condition := report.Steps[2]
	assert.Equal(t, true, condition.Details["result"], "the simulated assign fed the condition")
	assert.Equal(t, "next", condition.Details["branch"])

	task := report.Steps[3]
	assert.EqualValues(t, env.other.ID, task.Details["assignee_id"], "the owner after the simulated assign")
	assert.Equal(t, start.Add(120*time.Minute).Add(3*24*time.Hour).Format(time.RFC3339), task.Details["due_date"])
	assert.EqualValues(t, lead.ID, task.Details["lead_id"])
	_, hasCustomer := task.Details["customer_id"]
	assert.False(t, hasCustomer)

	email := report.Steps[4]
	assert.EqualValues(t, env.sales.ID, email.Details["recipient_id"])
	notify := report.Steps[5]
	assert.EqualValues(t, env.other.ID, notify.Details["recipient_id"])

	dealReport, err := env.svc.DryRun(dealRule.ID, models.AutomationEntityDeal, deal.ID)
	require.NoError(t, err)
	require.Len(t, dealReport.Steps, 2, "the false condition jumped over the notify")
	assert.Equal(t, models.AutomationElseJump, dealReport.Steps[0].Details["branch"])
	assert.EqualValues(t, 3, dealReport.Steps[0].Details["next_position"])
	assert.Equal(t, 3, dealReport.Steps[1].Position)
	assert.EqualValues(t, customer.ID, dealReport.Steps[1].Details["customer_id"])

	// Refusals write nothing either.
	_, err = env.svc.DryRun(rule.ID, models.AutomationEntityDeal, deal.ID)
	assert.True(t, errors.Is(err, apperrors.ErrValidation), "a record of the wrong entity type, got %v", err)
	_, err = env.svc.DryRun(rule.ID, models.AutomationEntityLead, 999999)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound), "a missing record, got %v", err)
	_, err = env.svc.DryRun(999999, models.AutomationEntityLead, lead.ID)
	assert.True(t, errors.Is(err, apperrors.ErrAutomationRuleNotFound), "a missing rule, got %v", err)

	after := snapshotDatabase(t, db)
	assert.Equal(t, before, after, "the dry run changed a row count or a column value")
	assert.Empty(t, env.mailer.messages(), "zero Mailer.Send calls")
	assert.Equal(t, env.sales.ID, env.lead(lead.ID).OwnerID, "the simulated assign changed nothing")
}

// G3: a dry run of an unsaved rule body (POST /automation/rules/dry-run)
// validates the body exactly like a create, simulates it, and touches
// nothing: besides the before/after snapshot, an instrumented connection pool
// and GORM callbacks fail the test on any transaction start, insert, update,
// delete, raw statement or locking read while the draft runs, and the mailer
// records no send. Every effect step of the draft is reachable (active
// recipients), so the zero-effect assertions are not met by skipped steps.

func TestAutomationDraftDryRunNoSideEffectsSQLite(t *testing.T) {
	runAutomationDraftDryRunNoSideEffects(t, openAutomationSQLite(t))
}

func TestAutomationDraftDryRunNoSideEffectsMySQL(t *testing.T) {
	runAutomationDraftDryRunNoSideEffects(t, openAutomationMySQL(t, 2))
}

// draftSpyPool is the database's connection pool with every transaction
// start counted while armed.
type draftSpyPool struct {
	gorm.ConnPool
	sqlDB  *sql.DB
	armed  atomic.Bool
	begins atomic.Int64
}

func (p *draftSpyPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if p.armed.Load() {
		p.begins.Add(1)
	}
	return p.sqlDB.BeginTx(ctx, opts)
}

func (p *draftSpyPool) GetDBConn() (*sql.DB, error) { return p.sqlDB, nil }

// instrumentDraftDryRun installs the spy pool and the write and lock guards.
// It must run before the fixture is built so every handle shares the pool.
func instrumentDraftDryRun(t *testing.T, db *gorm.DB) (*draftSpyPool, func() []string) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	spy := &draftSpyPool{ConnPool: db.ConnPool, sqlDB: sqlDB}
	db.ConnPool = spy
	db.Statement.ConnPool = spy

	var mu sync.Mutex
	var violations []string
	note := func(kind string) func(*gorm.DB) {
		return func(tx *gorm.DB) {
			if !spy.armed.Load() {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			violations = append(violations, kind+" "+tx.Statement.Table)
		}
	}
	locking := func(tx *gorm.DB) {
		if _, ok := tx.Statement.Clauses["FOR"]; ok {
			note("locking read")(tx)
		}
	}
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:draft_create", note("insert")))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:draft_update", note("update")))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:draft_delete", note("delete")))
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:draft_raw", note("raw")))
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:draft_lock_query", locking))
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:draft_lock_row", locking))
	return spy, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), violations...)
	}
}

func runAutomationDraftDryRunNoSideEffects(t *testing.T, db *gorm.DB) {
	spy, violations := instrumentDraftDryRun(t, db)
	env := newAutomationEnv(t, db)
	customer := env.createCustomer()
	lead := env.createLead(env.sales, models.LeadStatusNew)
	deal := env.createDeal(env.sales, models.DealStageProposal, 250000, "EUR", &customer.ID, nil)

	// A saved rule at version 2 with a run parked at its wait.
	saved := env.leadRule(models.AutomationEventLeadCreated, waitStep(120), notifyStep(models.AutomationRecipientOwner, 0))
	edit := &models.AutomationRule{Name: saved.Name, EntityType: saved.EntityType, TriggerType: saved.TriggerType,
		Steps: []models.AutomationStep{waitStep(120), notifyStep(models.AutomationRecipientOwner, 0)}}
	saved, err := env.svc.UpdateRule(saved.ID, edit, nil)
	require.NoError(t, err)
	require.Equal(t, 2, saved.Version)
	env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadCreated, "", "", models.AutomationOriginUser)
	env.dispatch()
	env.executeDue()
	require.Equal(t, models.AutomationRunWaiting, env.runsOf(saved.ID)[0].Status)
	env.mailer.reset(nil, nil)

	counted := []interface{}{
		&models.AutomationRule{}, &models.AutomationStep{}, &models.AutomationRun{}, &models.AutomationRunStep{},
		&models.AutomationEvent{}, &models.Task{}, &models.Notification{},
	}
	counts := func() []int64 {
		out := make([]int64, len(counted))
		for i, model := range counted {
			out[i] = env.count(model)
		}
		return out
	}
	beforeCounts := counts()
	before := snapshotDatabase(t, db)

	spy.armed.Store(true)
	// The saved rule edited in the form and not saved: a shorter wait, an
	// owner change and one effect step of every kind, each to an active user.
	draft := &models.AutomationRule{Name: saved.Name, EntityType: models.AutomationEntityLead, TriggerType: saved.TriggerType,
		Steps: []models.AutomationStep{
			waitStep(30),
			assignStep(env.other.ID),
			taskStep(models.AutomationRecipientOwner, 0, 2),
			emailStep(models.AutomationRecipientUser, env.sales.ID),
			notifyStep(models.AutomationRecipientUser, env.admin.ID),
		}}
	report, err := env.svc.DryRunDraft(draft, models.AutomationEntityLead, lead.ID)
	require.NoError(t, err)
	assert.True(t, report.Draft)
	assert.Zero(t, report.RuleID)
	assert.Zero(t, report.RuleVersion)
	require.Len(t, report.Steps, 5, "the draft's steps are simulated, not the saved rule's")
	assert.Equal(t, models.AutomationOutcomeWaiting, report.Steps[0].Outcome)
	for _, step := range report.Steps[1:] {
		assert.Equalf(t, models.AutomationOutcomeSucceeded, step.Outcome, "step %d (%s) is reachable", step.Position, step.StepType)
	}
	assert.EqualValues(t, env.other.ID, report.Steps[1].Details["to_owner_id"])
	assert.EqualValues(t, env.other.ID, report.Steps[2].Details["assignee_id"], "the simulated assign fed the owner recipient")
	assert.EqualValues(t, env.sales.ID, report.Steps[3].Details["recipient_id"])
	assert.EqualValues(t, env.admin.ID, report.Steps[4].Details["recipient_id"])
	assert.Equal(t, env.clock.Now().UTC().Add(30*time.Minute), report.SimulatedEnd)

	// A brand-new deal rule that was never saved.
	dealDraft := &models.AutomationRule{Name: "New deal rule", EntityType: models.AutomationEntityDeal, TriggerType: models.AutomationEventDealCreated,
		Steps: []models.AutomationStep{
			conditionStep("stage", "eq", "won", models.AutomationElseJump, 3),
			dealNotifyStep(models.AutomationRecipientOwner, 0),
			dealTaskStep(models.AutomationRecipientUser, env.other.ID, 1),
		}}
	dealReport, err := env.svc.DryRunDraft(dealDraft, models.AutomationEntityDeal, deal.ID)
	require.NoError(t, err)
	require.Len(t, dealReport.Steps, 2)
	assert.Equal(t, models.AutomationElseJump, dealReport.Steps[0].Details["branch"])
	assert.EqualValues(t, customer.ID, dealReport.Steps[1].Details["customer_id"])

	// Refusals touch nothing either.
	_, err = env.svc.DryRunDraft(&models.AutomationRule{Name: "Bad", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
		Steps: []models.AutomationStep{notifyStep(models.AutomationRecipientUser, 999999)}}, models.AutomationEntityLead, lead.ID)
	var ruleErr *RuleValidationError
	assert.True(t, errors.As(err, &ruleErr), "an invalid draft is a field-level refusal, got %v", err)
	_, err = env.svc.DryRunDraft(&models.AutomationRule{Name: "Lead", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
		Steps: []models.AutomationStep{notifyStep(models.AutomationRecipientOwner, 0)}}, models.AutomationEntityDeal, deal.ID)
	assert.True(t, errors.Is(err, apperrors.ErrValidation), "a record of the other entity type, got %v", err)
	_, err = env.svc.DryRunDraft(&models.AutomationRule{Name: "Lead", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
		Steps: []models.AutomationStep{notifyStep(models.AutomationRecipientOwner, 0)}}, models.AutomationEntityLead, 999999)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound), "a missing record, got %v", err)
	spy.armed.Store(false)

	assert.Empty(t, violations(), "no write, raw statement or locking read during a draft dry run")
	assert.Zero(t, spy.begins.Load(), "no transaction started during a draft dry run")
	assert.Equal(t, beforeCounts, counts(), "rules, steps, runs, run steps, events, tasks, notifications")
	assert.Equal(t, before, snapshotDatabase(t, db), "the draft dry run changed a row")
	assert.Empty(t, env.mailer.messages(), "zero Mailer.Send calls")
	stored, err := env.svc.GetRule(saved.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, stored.Version, "the saved rule's version is unchanged")
	require.Len(t, stored.Steps, 2, "the saved rule's steps are unchanged")
	assert.Equal(t, int64(120), stored.Steps[0].Config.DurationMinutes)
	assert.Equal(t, models.AutomationRunWaiting, env.runsOf(saved.ID)[0].Status, "the saved rule's run is untouched")
	assert.Equal(t, env.sales.ID, env.lead(lead.ID).OwnerID)
}

// The instrumentation is only worth something if it catches what it guards:
// the same pool and callbacks flag a saved rule's update.
func TestAutomationDraftDryRunInstrumentationCatchesWrites(t *testing.T) {
	db := openAutomationSQLite(t)
	spy, violations := instrumentDraftDryRun(t, db)
	env := newAutomationEnv(t, db)
	rule := env.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0))
	spy.armed.Store(true)
	_, err := env.svc.UpdateRule(rule.ID, &models.AutomationRule{Name: rule.Name, EntityType: rule.EntityType, TriggerType: rule.TriggerType,
		Steps: []models.AutomationStep{notifyStep(models.AutomationRecipientOwner, 0)}}, nil)
	spy.armed.Store(false)
	require.NoError(t, err)
	assert.NotZero(t, spy.begins.Load(), "the update's transaction is counted")
	assert.NotEmpty(t, violations(), "the update's writes are flagged")
	assert.Contains(t, strings.Join(violations(), ","), "update automation_rules")
}

// The draft goes through the same validation as a create: every body a
// create refuses, a draft dry run refuses with the same fields and message.
func TestAutomationDraftDryRunValidatesLikeCreate(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	lead := env.createLead(env.sales, models.LeadStatusNew)
	inactive := env.createUser("inactive", models.RoleSales, false)
	tooMany := make([]models.AutomationStep, models.AutomationMaxSteps+1)
	for i := range tooMany {
		tooMany[i] = notifyStep(models.AutomationRecipientOwner, 0)
	}
	personalTask := taskStep(models.AutomationRecipientOwner, 0, 1)
	personalTask.Config.Title = "Call {{lead.first_name}}"
	for name, steps := range map[string][]models.AutomationStep{
		"21 steps":                tooMany,
		"backward jump":           {conditionStep("status", "eq", "new", models.AutomationElseJump, 1), notifyStep(models.AutomationRecipientOwner, 0)},
		"unknown placeholder":     {{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{Title: "Hi {{lead.nickname}}"}}},
		"personal task text":      {personalTask},
		"inactive user":           {assignStep(inactive.ID)},
		"more than 90 days waits": {waitStep(models.AutomationMaxWaitMinutes), waitStep(1), notifyStep(models.AutomationRecipientOwner, 0)},
	} {
		t.Run(name, func(t *testing.T) {
			body := func() *models.AutomationRule {
				return &models.AutomationRule{Name: "R", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
					Steps: append([]models.AutomationStep(nil), steps...)}
			}
			createErr := env.svc.CreateRule(body(), env.admin.ID)
			_, draftErr := env.svc.DryRunDraft(body(), models.AutomationEntityLead, lead.ID)
			require.Error(t, createErr)
			require.Error(t, draftErr)
			assert.Equal(t, createErr.Error(), draftErr.Error())
			assert.Equal(t, fieldCodes(t, createErr), fieldCodes(t, draftErr))
		})
	}
	assert.Zero(t, env.count(&models.AutomationRule{}))
}
