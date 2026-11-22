package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

// The runtimes main drains in parallel under one shutdown limit (#561/#606),
// and the lifecycle the loop and owner runtimes share. SQLite only: nothing
// here depends on the driver.

// TestRuntimeShutdownWebhookInFlightAEODrains stops a webhook runtime whose
// delivery is held by its consumer and an AEO runtime with one blocked run
// together under one context: the AEO drain does not wait for the webhook
// attempt, and both return before the shared deadline.
func TestRuntimeShutdownWebhookInFlightAEODrains(t *testing.T) {
	const (
		shared         = 3 * time.Second
		requestTimeout = 2 * time.Second
	)

	// The webhook half: the consumer holds the POST until the client gives up.
	env := webhookRuntimeEnv(t)
	consumer := newWebhookConsumer(t)
	entered := make(chan struct{}, 1)
	consumer.setHandler(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		w.WriteHeader(http.StatusOK)
	})
	env.endpoint(consumer.url("/hook"), true, models.WebhookEventLeadCreated)
	require.NoError(t, env.leads.Create(env.newLead()))
	hooks := env.runtime(consumer.guard(nil), webhook.WithWebhookRequestTimeout(requestTimeout),
		webhook.WithWebhookTicker(func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }))
	hooks.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hooks.Stop(ctx)
	})
	select {
	case <-entered:
	case <-time.After(aeoRuntimeTimeout):
		t.Fatal("the delivery did not reach the consumer")
	}

	// The AEO half: one admitted run blocked in its provider until its
	// context is cancelled.
	provider := newGateProvider("gate")
	f := newAEORuntimeFixture(t, openAEORuntimeSQLite(t), provider)
	f.seedProfile()
	f.seedPrompts(1)
	run, err := f.service.StartRun(context.Background(), "manual", nil)
	require.NoError(t, err)
	provider.awaitEntry(t)
	require.Equal(t, 1, f.runtime.ActiveRuns())

	ctx, cancel := context.WithTimeout(context.Background(), shared)
	defer cancel()
	started := time.Now()
	type result struct {
		err     error
		elapsed time.Duration
	}
	aeoDone := make(chan result, 1)
	hooksDone := make(chan result, 1)
	go func() {
		err := f.runtime.Shutdown(ctx)
		aeoDone <- result{err: err, elapsed: time.Since(started)}
	}()
	go func() {
		err := hooks.Stop(ctx)
		hooksDone <- result{err: err, elapsed: time.Since(started)}
	}()

	var aeoResult, hooksResult result
	select {
	case aeoResult = <-aeoDone:
	case <-time.After(aeoRuntimeTimeout):
		t.Fatal("the AEO drain did not return")
	}
	select {
	case hooksResult = <-hooksDone:
	case <-time.After(aeoRuntimeTimeout):
		t.Fatal("the webhook stop did not return")
	}

	require.NoError(t, aeoResult.err)
	assert.Less(t, aeoResult.elapsed, time.Second, "the AEO drain does not wait for the webhook attempt")
	assertTerminal(t, f.run(run.ID), models.AEORunStatusFailed, 1, 1)
	assert.Equal(t, 0, f.runtime.ActiveRuns())

	require.NoError(t, hooksResult.err, "the held attempt ends at its request timeout, inside the shared limit")
	assert.Less(t, hooksResult.elapsed, shared)
	assert.Less(t, aeoResult.elapsed, shared)
}

// TestRuntimeLifecyclePortDifferences pins the three places where the shared
// lifecycle changed a runtime's behaviour.
func TestRuntimeLifecyclePortDifferences(t *testing.T) {
	t.Run("automation start after stop stays stopped", func(t *testing.T) {
		env := newAutomationEnv(t, openAutomationSQLite(t))
		var ticks atomic.Int32
		clock := env.clock.Now
		env.engine.SetClock(func() time.Time {
			ticks.Add(1)
			return clock()
		})
		runtime := NewAutomationRuntime(env.engine, time.Hour)
		require.NoError(t, runtime.Stop(context.Background()))
		runtime.Start(context.Background())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), automationTestTimeout)
			defer cancel()
			_ = runtime.Stop(ctx)
		})
		assert.False(t, runtime.Running(), "a stopped worker is not armed again")
		runtime.Wake()
		// A negative check needs a window: an armed worker ticks at Start.
		time.Sleep(200 * time.Millisecond)
		assert.Zero(t, ticks.Load(), "no tick reaches the engine")
	})

	t.Run("automation deadline error wraps the context error", func(t *testing.T) {
		env := newAutomationEnv(t, openAutomationSQLite(t))
		entered := make(chan struct{})
		release := make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		releaseTick := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseTick)
		clock := env.clock.Now
		// The tick's first engine call reads the clock; the first tick
		// blocks there until the test releases it.
		env.engine.SetClock(func() time.Time {
			first := false
			enterOnce.Do(func() { first = true })
			if first {
				close(entered)
				<-release
			}
			return clock()
		})
		runtime := NewAutomationRuntime(env.engine, time.Hour)
		runtime.Start(context.Background())
		t.Cleanup(func() {
			releaseTick()
			ctx, cancel := context.WithTimeout(context.Background(), automationTestTimeout)
			defer cancel()
			assert.NoError(t, runtime.Stop(ctx), "the released worker joins")
		})
		select {
		case <-entered:
		case <-time.After(automationTestTimeout):
			t.Fatal("the worker did not tick")
		}

		expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := runtime.Stop(expired)
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.DeadlineExceeded))
		assert.Contains(t, err.Error(), "automation worker did not stop before the deadline")
		// The engine holds no admitted work, so the error is the worker's
		// alone; it is the one that must wrap the context error.
		var worker error
		for _, part := range unwrapJoined(err) {
			if strings.Contains(part.Error(), "automation worker did not stop before the deadline") {
				worker = part
			}
		}
		require.Error(t, worker)
		assert.Equal(t, "automation worker did not stop before the deadline: context deadline exceeded", worker.Error())
		assert.True(t, errors.Is(worker, context.DeadlineExceeded), "the worker's deadline error wraps ctx.Err()")
		releaseTick()
	})

	t.Run("a drained aeo runtime always shuts down cleanly", func(t *testing.T) {
		f := newAEORuntimeFixture(t, openAEORuntimeSQLite(t))
		require.NoError(t, f.shutdown())
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		failures := 0
		for i := 0; i < 200; i++ {
			if f.runtime.Shutdown(ended) != nil {
				failures++
			}
		}
		assert.Zero(t, failures, "a repeated Shutdown after the drain returns nil whatever its context")
	})
}

// unwrapJoined returns the parts of an errors.Join result, or err itself.
func unwrapJoined(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}
