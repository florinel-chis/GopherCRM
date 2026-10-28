package service

import (
	"context"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The service half of AC-15: the runtime's Stop closes admission, cancels
// the worker and waits for a step in flight, which finishes its own
// transaction; no write happens after Stop returns and the runs the worker
// never reached stay pending. cmd/automation_runtime_test.go pins the wiring
// through setupDependencies.
func TestAutomationRuntimeDrain(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	rule := env.leadRule(models.AutomationEventLeadCreated, taskStep(models.AutomationRecipientOwner, 0, 1), notifyStep(models.AutomationRecipientOwner, 0))
	lead := env.createLead(env.sales, models.LeadStatusNew)
	first := env.startRun(rule, lead)
	second := env.startRun(rule, lead)

	// The first step of the first run pauses after its effect until the test
	// releases it; Stop must wait for that transaction.
	entered := make(chan struct{})
	release := make(chan struct{})
	env.engine.afterEffect = func(runID uint, position int) error {
		if runID == first.ID && position == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	runtime := NewAutomationRuntime(env.engine, time.Hour)
	runtime.Start(context.Background())
	select {
	case <-entered:
	case <-time.After(automationTestTimeout):
		t.Fatal("the worker did not reach the paused step")
	}
	assert.Equal(t, 1, env.engine.ActiveWork(), "the step is admitted work")

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationTestTimeout)
		defer cancel()
		stopped <- runtime.Stop(ctx)
	}()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned while a step was in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(automationTestTimeout):
		t.Fatal("Stop did not return after the step was released")
	}
	assert.Zero(t, env.engine.ActiveWork(), "a drained runtime holds no work")
	assert.False(t, runtime.Running())

	// The paused step committed; the first run was handed back at its next
	// position when admission closed; the second run was never claimed.
	assert.EqualValues(t, 1, env.count(&models.Task{}), "the in-flight step finished its transaction")
	done := env.run(first.ID)
	assert.Equal(t, models.AutomationRunPending, done.Status)
	assert.Equal(t, 2, done.NextPosition)
	assert.Len(t, env.stepsOf(first.ID), 1)
	assert.Equal(t, models.AutomationRunPending, env.run(second.ID).Status)
	assert.Empty(t, env.stepsOf(second.ID))

	// Nothing is written after Stop returned.
	before := snapshotDatabase(t, env.db)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, snapshotDatabase(t, env.db))
	require.NoError(t, runtime.Stop(context.Background()), "stopping again is a no-op")

	// A fresh runtime on the same database finishes the work.
	env.rebuild()
	fresh := NewAutomationRuntime(env.engine, time.Hour)
	fresh.Start(context.Background())
	require.Eventually(t, func() bool {
		return env.run(first.ID).Status == models.AutomationRunCompleted && env.run(second.ID).Status == models.AutomationRunCompleted
	}, automationTestTimeout, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), automationTestTimeout)
	defer cancel()
	require.NoError(t, fresh.Stop(ctx))
	assert.EqualValues(t, 2, env.count(&models.Task{}))
}
