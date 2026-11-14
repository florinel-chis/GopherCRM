package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// --- AC-8: concurrent workers -----------------------------------------------

func TestAutomationRunLeaseConcurrentWorkersSQLite(t *testing.T) {
	dbA, dbB := openLeasePairSQLite(t)
	runAutomationRunLeaseConcurrentWorkers(t, dbA, dbB, 12)
}

func TestAutomationRunLeaseConcurrentWorkersMySQL(t *testing.T) {
	dbA, dbB := openLeasePairMySQL(t, 4)
	runAutomationRunLeaseConcurrentWorkers(t, dbA, dbB, 16)
}

// runAutomationRunLeaseConcurrentWorkers seeds pending runs of multi-step
// rules, unprocessed events and a schedule rule, then lets both engines tick
// concurrently until the database is quiet, while a rule change cancels the
// runs of one rule. Every assertion reads committed rows.
func runAutomationRunLeaseConcurrentWorkers(t *testing.T, dbA, dbB *gorm.DB, leadCount int) {
	p := newLeasePair(t, dbA, dbB)
	main := p.leadRule(models.AutomationEventLeadCreated,
		taskStep(models.AutomationRecipientOwner, 0, 1), notifyStep(models.AutomationRecipientOwner, 0),
		assignStep(p.other.ID), notifyStep(models.AutomationRecipientOwner, 0))
	changing := p.leadRule(models.AutomationEventLeadCreated,
		notifyStep(models.AutomationRecipientOwner, 0), taskStep(models.AutomationRecipientOwner, 0, 1), notifyStep(models.AutomationRecipientOwner, 0))
	schedule := p.createRule(models.AutomationEntityLead, models.AutomationTriggerSchedule, models.AutomationTrigger{HourUTC: 0},
		notifyStep(models.AutomationRecipientOwner, 0))

	leads := make([]*models.Lead, 0, leadCount)
	for i := 0; i < leadCount; i++ {
		leads = append(leads, p.createLead(p.sales, models.LeadStatusNew))
	}
	// Half the events are dispatched up front (pending runs); the other half
	// stay unprocessed for the workers to dispatch.
	for i, lead := range leads {
		p.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadCreated, "", "", models.AutomationOriginUser)
		if i == leadCount/2-1 {
			p.dispatch()
		}
	}
	var pending int64
	require.NoError(t, p.db.Model(&models.AutomationRun{}).Where("status = ?", models.AutomationRunPending).Count(&pending).Error)
	require.EqualValues(t, leadCount, pending, "pending runs of both rules for the first half")

	// The rule change runs on its own goroutine once the first step of any
	// run has committed, so the workers are mid-flight when it cancels the
	// changing rule's active runs.
	started := make(chan struct{})
	var startOnce sync.Once
	signal := func(uint, int) error {
		startOnce.Do(func() { close(started) })
		return nil
	}
	p.a.afterEffect = signal
	p.b.afterEffect = signal
	var changed atomic.Bool
	changeDone := goAsync(func() error {
		select {
		case <-started:
		case <-time.After(automationTestTimeout):
			return errors.New("no step committed")
		}
		enabled := true
		var err error
		for attempt := 0; attempt < 100; attempt++ {
			replacement := &models.AutomationRule{Name: "Changed", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadCreated,
				Enabled: true, Steps: []models.AutomationStep{notifyStep(models.AutomationRecipientOwner, 0)}}
			if _, err = p.svc.UpdateRule(changing.ID, replacement, &enabled); err == nil {
				changed.Store(true)
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		return err
	})

	today := p.clock.Now().UTC().Format("2006-01-02")
	quiet := func(db *gorm.DB) bool {
		var active, unprocessed int64
		if db.Model(&models.AutomationRun{}).Where("status IN ?", []string{models.AutomationRunPending, models.AutomationRunRunning}).Count(&active).Error != nil {
			return false
		}
		if db.Model(&models.AutomationEvent{}).Where("processed_at IS NULL").Count(&unprocessed).Error != nil {
			return false
		}
		var rule models.AutomationRule
		if db.First(&rule, schedule.ID).Error != nil {
			return false
		}
		if !changed.Load() {
			return false
		}
		return active == 0 && unprocessed == 0 && rule.ScheduleCompletedOn != nil && *rule.ScheduleCompletedOn == today
	}
	workerBound := 3 * automationTestTimeout
	worker := func(e *AutomationEngine, db *gorm.DB) func() error {
		return func() error {
			deadline := time.Now().Add(workerBound)
			for time.Now().Before(deadline) {
				// Tick errors (SQLite busy, a deadlock victim on MySQL) are
				// what concurrent workers meet; the outcome is asserted below.
				_ = e.Tick(context.Background())
				if quiet(db) {
					return nil
				}
				// A short pause between passes, as a polling worker has,
				// so the two engines do not starve each other's writes.
				time.Sleep(5 * time.Millisecond)
			}
			return errors.New("the workers did not finish")
		}
	}
	doneA := goAsync(worker(p.a, dbA))
	doneB := goAsync(worker(p.b, dbB))
	require.NoError(t, waitDone(t, changeDone), "the rule change committed")
	// The workers bound themselves; wait a little longer than that bound so
	// their own error, not this wait, reports a stall.
	require.NoError(t, waitDoneWithin(t, doneA, workerBound+automationTestTimeout))
	require.NoError(t, waitDoneWithin(t, doneB, workerBound+automationTestTimeout))
	p.a.afterEffect = nil
	p.b.afterEffect = nil

	runs := p.allRuns()
	require.NotEmpty(t, runs)
	for _, run := range runs {
		assert.Contains(t, []string{models.AutomationRunCompleted, models.AutomationRunFailed, models.AutomationRunCancelled, models.AutomationRunWaiting},
			run.Status, "run %d ends terminal or waiting", run.ID)
		assert.Nil(t, run.OwnerInstance, "run %d (%s) has no owner", run.ID, run.Status)
		assert.Nil(t, run.HeartbeatAt, "run %d (%s) has no heartbeat", run.ID, run.Status)
	}

	// Every (run_id, position) step row exists once.
	var duplicates []struct {
		RunID    uint
		Position int
		N        int64
	}
	require.NoError(t, p.db.Model(&models.AutomationRunStep{}).Select("run_id, position, COUNT(*) AS n").
		Group("run_id, position").Having("COUNT(*) > 1").Scan(&duplicates).Error)
	assert.Empty(t, duplicates)

	// Every effect exists once per step.
	var steps []models.AutomationRunStep
	require.NoError(t, p.db.Order("id ASC").Find(&steps).Error)
	taskIDs := map[uint]bool{}
	notifySteps := map[uint]int64{}
	assignSteps := 0
	for _, step := range steps {
		if step.Outcome != models.AutomationOutcomeSucceeded {
			continue
		}
		switch step.StepType {
		case models.AutomationStepCreateTask:
			id, ok := step.Details["task_id"].(float64)
			require.True(t, ok, "step %d names its task", step.ID)
			assert.False(t, taskIDs[uint(id)], "task %d is named by one step only", uint(id))
			taskIDs[uint(id)] = true
		case models.AutomationStepNotify:
			notifySteps[step.RunID]++
		case models.AutomationStepAssignOwner:
			assignSteps++
		}
	}
	tasks := p.tasksList()
	assert.Len(t, tasks, len(taskIDs), "one task per create_task step")
	for _, task := range tasks {
		assert.True(t, taskIDs[task.ID], "task %d belongs to a step", task.ID)
	}
	notificationsByRun := map[uint]int64{}
	for _, notification := range p.notificationsList() {
		require.NotNil(t, notification.RunID)
		notificationsByRun[*notification.RunID]++
	}
	assert.Equal(t, notifySteps, notificationsByRun, "one notification per notify step")
	var ownerChanges int64
	require.NoError(t, p.db.Model(&models.AutomationEvent{}).Where("origin = ?", models.AutomationOriginAutomation).Count(&ownerChanges).Error)
	assert.EqualValues(t, assignSteps, ownerChanges, "one owner-change event per assign_owner step")
	assert.Positive(t, assignSteps)

	// Every event was claimed once and started one run per matching rule.
	for _, event := range p.events() {
		require.NotNil(t, event.ProcessedAt, "event %d was claimed", event.ID)
		var started []models.AutomationRun
		require.NoError(t, p.db.Where("event_id = ?", event.ID).Find(&started).Error)
		if event.Origin == models.AutomationOriginAutomation {
			assert.Empty(t, started, "an automation event starts nothing")
			continue
		}
		rules := map[uint]int{}
		for _, run := range started {
			rules[run.RuleID]++
		}
		assert.Equal(t, map[uint]int{main.ID: 1, changing.ID: 1}, rules, "event %d starts one run per rule", event.ID)
	}

	// The schedule batch handed out each record once.
	scheduled := p.runsOf(schedule.ID)
	assert.Len(t, scheduled, leadCount)
	seen := map[uint]bool{}
	for _, run := range scheduled {
		assert.False(t, seen[run.EntityID], "lead %d is handed out once", run.EntityID)
		seen[run.EntityID] = true
		assert.Equal(t, fmt.Sprintf("sch:%d:%s:%d", schedule.ID, today, run.EntityID), run.DedupeKey)
	}

	// The rule change cancelled runs that were active, and cleared their
	// lease like every other terminal run.
	cancelled := 0
	for _, run := range p.runsOf(changing.ID) {
		if run.Status == models.AutomationRunCancelled {
			assert.Equal(t, models.AutomationCancelRuleChanged, run.CancelReason)
			cancelled++
		}
	}
	assert.Positive(t, cancelled, "the rule change cancelled runs during the test")
}

// --- AC-9: lock order on MySQL ----------------------------------------------

// TestAutomationRunLeaseLockOrderMySQL forces a heartbeat and a sweep against
// a step transaction of the same run. A step locks the record row and then
// the run row; a renewal and a recovery are single run-row writes, so each
// waits (seen in information_schema.PROCESSLIST on two consecutive polls) and
// none ends in error 1213. The step commits once in every ordering.
func TestAutomationRunLeaseLockOrderMySQL(t *testing.T) {
	dbA, dbB := openLeasePairMySQL(t, 4)
	p := newLeasePair(t, dbA, dbB)
	lead := p.createLead(p.sales, models.LeadStatusNew)
	noDeadlocks := func(t *testing.T) {
		t.Helper()
		for _, err := range append(p.recA.writeErrors(), p.recB.writeErrors()...) {
			assertNotDeadlock(t, err)
			assert.NotContains(t, strings.ToLower(err.Error()), "deadlock")
		}
		for _, err := range p.recA.renewErrors() {
			if err != nil {
				assertNotDeadlock(t, err)
			}
		}
	}

	t.Run("a heartbeat waits for the step that holds the lead and the run", func(t *testing.T) {
		rule := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
		run := p.startRun(rule, lead)
		gate := newLeaseGate(t)
		p.a.afterEffect = func(id uint, position int) error {
			if id == run.ID && position == 1 {
				gate.park()
			}
			return nil
		}
		p.a.heartbeatInterval = 50 * time.Millisecond
		done := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		gate.await(t)
		awaitLeaseLockWait(t, dbA, leaseRenewalSQL)
		gate.open()
		require.NoError(t, waitDone(t, done))
		p.a.afterEffect = nil
		p.a.heartbeatInterval = time.Hour

		require.Positive(t, p.recA.renewCount())
		noDeadlocks(t)
		assert.EqualValues(t, 1, p.stepRowsAt(run.ID, 1), "the step committed once")
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(run.ID).Status)
	})

	t.Run("a sweep waits for the step that holds the run", func(t *testing.T) {
		rule := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
		run := p.startRun(rule, lead)
		inStep := newLeaseGate(t)
		betweenSteps := newLeaseGate(t)
		p.a.afterEffect = func(id uint, position int) error {
			if id == run.ID && position == 1 {
				inStep.park()
			}
			return nil
		}
		p.a.beforeStep = func(id uint, position int) {
			if id == run.ID && position == 2 {
				betweenSteps.park()
			}
		}
		done := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		inStep.await(t)

		p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
		type sweepResult struct {
			recovered, failed int
			err               error
		}
		swept := make(chan sweepResult, 1)
		go func() {
			recovered, failed, err := p.b.SweepStaleRuns(context.Background())
			swept <- sweepResult{recovered, failed, err}
		}()
		awaitLeaseLockWait(t, dbA, leaseRecoverSQL)
		inStep.open()
		betweenSteps.await(t)
		var result sweepResult
		select {
		case result = <-swept:
		case <-time.After(automationTestTimeout):
			t.Fatal("the sweep did not finish")
		}
		require.NoError(t, result.err)
		assertNotDeadlock(t, result.err)
		assert.Equal(t, 1, result.recovered, "the sweep recovered the run once the step committed")
		betweenSteps.open()
		require.NoError(t, waitDone(t, done))
		p.a.afterEffect = nil
		p.a.beforeStep = nil

		assert.EqualValues(t, 1, p.stepRowsAt(run.ID, 1), "the step committed once")
		recovered := p.runRow(run.ID)
		assert.Equal(t, models.AutomationRunPending, recovered.Status)
		assert.Equal(t, 2, recovered.NextPosition)
		require.NoError(t, p.b.ExecuteDue(context.Background()))
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(run.ID).Status)
		assert.EqualValues(t, 1, p.stepRowsAt(run.ID, 2))
		assert.EqualValues(t, 2, p.notificationsOfRun(run.ID))
		noDeadlocks(t)
	})

	t.Run("a step waits for the sweep that holds the run", func(t *testing.T) {
		rule := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0))
		run := p.startRun(rule, lead)
		inStep := newLeaseGate(t)
		p.a.afterRecordRead = func(id uint, _ int) {
			if id == run.ID {
				inStep.park()
			}
		}
		done := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
		inStep.await(t)

		p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
		inSweep := newLeaseGate(t)
		p.b.afterStaleRecover = func(id uint) {
			if id == run.ID {
				inSweep.park()
			}
		}
		swept := goAsync(func() error {
			_, _, err := p.b.SweepStaleRuns(context.Background())
			return err
		})
		inSweep.await(t)
		inStep.open()
		awaitLeaseLockWait(t, dbA, leaseAdvanceSQL)
		inSweep.open()
		err := waitDone(t, swept)
		require.NoError(t, err)
		require.NoError(t, waitDone(t, done))
		p.a.afterRecordRead = nil
		p.b.afterStaleRecover = nil

		assert.Zero(t, p.stepRowsAt(run.ID, 1), "A's advance matched nothing and rolled back")
		assert.Equal(t, models.AutomationRunPending, p.runRow(run.ID).Status)
		require.NoError(t, p.b.ExecuteDue(context.Background()))
		assert.EqualValues(t, 1, p.stepRowsAt(run.ID, 1), "the step commits once")
		assert.EqualValues(t, 1, p.notificationsOfRun(run.ID))
		assert.Equal(t, models.AutomationRunCompleted, p.runRow(run.ID).Status)
		noDeadlocks(t)
	})
}

// --- AC-10: no personal data and no instance id in the lease logs ----------

// leaseLogMessages are the lines of the claim, heartbeat, heartbeat-loss,
// pre-send check, sweep, takeover and fenced-write paths.
var leaseLogMessages = stringSet(
	"Automation run claim failed",
	"Automation run lease lost; the executor stops",
	"Could not renew the automation run lease",
	"Automation run lease lost; no further step",
	"Automation email refused: the run is no longer held by this executor",
	"Automation email not sent: the run is no longer held by this executor",
	"Automation email outcome not recorded: the step row was already finalized",
	"Automation run moved between the two email halves; stopping",
	"Automation run advance matched nothing; the run moved under the executor",
	"Automation run changed under the executor; stopping",
	"Automation run recovered from a stale lease",
	"Automation run spared by the sweep: it moved since it was read",
	"Automation stale run recovery failed",
	"Automation runs recovered from stale leases",
	"Automation run write failed; the stale sweep will recover the run",
)

// leaseLogFields are the only fields those lines may carry (error is the
// sanitised database error of a failed renewal, claim or recovery).
var leaseLogFields = stringSet(
	"component", "operation", "run_id", "rule_id", "event_id",
	"entity_type", "entity_id", "position", "step_type", "outcome",
	"error_code", "recovered", "failed", logrus.ErrorKey,
)

func stringSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func TestAutomationRunLeaseLogsOmitPersonalData(t *testing.T) {
	output, hook := capturePrivacyLogs(t)
	dbA, dbB := openLeasePairSQLite(t)
	p := newLeasePair(t, dbA, dbB)
	lead := p.createLead(p.sales, models.LeadStatusNew)

	// Claim, renewals (one failing), and a heartbeat lost to a cancel.
	twoSteps := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
	run := p.startRun(twoSteps, lead)
	gate := newLeaseGate(t)
	p.a.beforeStep = func(id uint, position int) {
		if id == run.ID && position == 2 {
			gate.park()
		}
	}
	p.recA.failRenewals(1)
	p.a.heartbeatInterval = 20 * time.Millisecond
	done := goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
	gate.await(t)
	waitUntil(t, "a failed and a successful renewal", func() bool { return p.recA.renewCount() >= 2 })
	_, err := p.repo.CancelRun(run.ID, models.AutomationCancelManual, p.clock.Now())
	require.NoError(t, err)
	select {
	case <-p.recA.notOwned:
	case <-time.After(automationTestTimeout):
		t.Fatal("the heartbeat never saw the cancel")
	}
	gate.open()
	require.NoError(t, waitDone(t, done))
	p.a.beforeStep = nil
	p.a.heartbeatInterval = time.Hour

	email := p.leadRule(models.AutomationEventLeadCreated, emailStep(models.AutomationRecipientOwner, 0))

	// Parked before the send: B's sweep fails the run, A's pre-send check
	// refuses.
	parkedBefore := p.startRun(email, lead)
	before := newLeaseGate(t)
	p.a.beforeSend = func(id uint, _ int) {
		if id == parkedBefore.ID {
			before.park()
		}
	}
	done = goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
	before.await(t)
	p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
	require.NoError(t, p.b.Tick(context.Background()))
	before.open()
	require.NoError(t, waitDone(t, done))
	p.a.beforeSend = nil

	// Parked after the send: the late stamp and advance are fenced.
	parkedAfter := p.startRun(email, lead)
	after := newLeaseGate(t)
	p.a.afterSend = func(id uint, _ int) error {
		if id == parkedAfter.ID {
			after.park()
		}
		return nil
	}
	done = goAsync(func() error { return p.a.ExecuteDue(context.Background()) })
	after.await(t)
	p.clock.Advance(automationRunLeaseStaleAfter + time.Second)
	require.NoError(t, p.b.Tick(context.Background()))
	after.open()
	require.NoError(t, waitDone(t, done))
	p.a.afterSend = nil

	// A legacy running run without a lease, taken over to pending.
	oneStep := p.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0))
	legacy := p.startRun(oneStep, lead)
	require.NoError(t, p.db.Model(&models.AutomationRun{}).Where("id = ?", legacy.ID).Update("status", models.AutomationRunRunning).Error)
	recovered, _, err := p.b.SweepStaleRuns(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	logs := output.String()
	for _, needle := range []string{
		p.a.instanceID, p.b.instanceID,
		automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone, automationCanaryNotes, lead.Email,
		p.sales.Email, p.other.Email, p.admin.Email,
		"EMAIL-SUBJECT-CANARY", "EMAIL-BODY-CANARY", "NOTIFY-TITLE-CANARY", "NOTIFY-BODY-CANARY",
	} {
		assert.NotContains(t, logs, needle)
	}
	for _, expected := range []string{
		`"msg":"Could not renew the automation run lease"`,
		`"msg":"Automation run lease lost; the executor stops"`,
		`"msg":"Automation run lease lost; no further step"`,
		`"msg":"Automation email not sent: the run is no longer held by this executor"`,
		`"msg":"Automation run moved between the two email halves; stopping"`,
		`"msg":"Automation run recovered from a stale lease"`,
		`"msg":"Automation runs recovered from stale leases"`,
		`"operation":"takeover"`, `"operation":"heartbeat"`, `"operation":"pre_send_check"`, `"operation":"fenced_advance"`,
		`"error_code":"run_not_owned"`, `"error_code":"email_unconfirmed"`, `"outcome":"pending"`, `"outcome":"failed"`,
	} {
		assert.Contains(t, logs, expected)
	}

	checked := 0
	for _, entry := range hook.AllEntries() {
		if !leaseLogMessages[entry.Message] {
			continue
		}
		checked++
		for key, value := range entry.Data {
			assert.True(t, leaseLogFields[key], "%q carries the field %q", entry.Message, key)
			text := fmt.Sprint(value)
			assert.NotContains(t, text, p.a.instanceID)
			assert.NotContains(t, text, p.b.instanceID)
		}
	}
	assert.Positive(t, checked)
}
