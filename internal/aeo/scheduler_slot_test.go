package aeo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// waitCalled waits for one StartScheduledRun of the fake starter.
func waitCalled(t *testing.T, starter *fakeRunStarter) {
	t.Helper()
	select {
	case <-starter.called:
	case <-time.After(5 * time.Second):
		t.Fatal("the due tick did not reach the starter")
	}
}

// TestSchedulerPassesSlot proves the scheduler hands StartScheduledRun the
// instant it computed with NextRunAt, not the time its timer fired: a replica
// that wakes up late still names the same slot as one that woke on time (#229).
func TestSchedulerPassesSlot(t *testing.T) {
	starter := newFakeRunStarter()
	clock := newManualClock(time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC))
	scheduler := NewScheduler(starter, 6, WithSchedulerClock(clock))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)

	// The timer fires 7 seconds late.
	fire := clock.armed(t)
	clock.advance(time.Hour + 7*time.Second)
	fire()
	waitCalled(t, starter)

	// The next day's timer fires two minutes late.
	fire = clock.armed(t)
	clock.advance(24*time.Hour + 2*time.Minute)
	fire()
	waitCalled(t, starter)
	clock.armed(t)

	slots := starter.recordedSlots()
	require.Len(t, slots, 2)
	assert.True(t, slots[0].Equal(time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)),
		"first slot is the configured hour, not the wake-up time: got %s", slots[0])
	assert.True(t, slots[1].Equal(time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)),
		"second slot is the next day's hour: got %s", slots[1])
	assert.Equal(t, "2026-10-24T06:00Z", slots[0].UTC().Format(models.AEOScheduleSlotFormat))

	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	require.NoError(t, scheduler.Wait(waitCtx))
}

// TestSchedulerSlotTakenSkip proves a slot another replica already started is
// logged once at info level as a skip, without the slot value, and that the
// loop survives to arm the next day.
func TestSchedulerSlotTakenSkip(t *testing.T) {
	read := captureSchedulerLogs(t)
	starter := newFakeRunStarter()
	starter.err = apperrors.ErrAEOScheduleSlotTaken
	clock := newManualClock(time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC))
	scheduler := NewScheduler(starter, 6, WithSchedulerClock(clock))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)

	fire := clock.armed(t)
	clock.advance(time.Hour)
	fire()
	waitCalled(t, starter)

	// The loop survived the refusal and armed tomorrow's timer.
	clock.armed(t)
	requested := clock.requested()
	require.Len(t, requested, 2)
	assert.Equal(t, 24*time.Hour, requested[1])

	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	require.NoError(t, scheduler.Wait(waitCtx))

	var skips []map[string]any
	for _, entry := range read() {
		assert.NotEqual(t, "error", entry["level"], "a taken slot is not a failure: %v", entry)
		if msg, _ := entry["msg"].(string); msg == "AEO scheduled run skipped: this slot was already started" {
			skips = append(skips, entry)
		}
	}
	require.Len(t, skips, 1, "one skip line per tick")
	skip := skips[0]
	assert.Equal(t, "info", skip["level"])
	assert.Equal(t, "schedule_slot_taken", skip["error_code"])
	for key, value := range skip {
		text, _ := value.(string)
		assert.NotContains(t, text, "2026-10-24", "field %s must not carry the slot", key)
	}

	// The direct call classifies the sentinel the same way, wrapped or not.
	direct := captureSchedulerLogs(t)
	wrapped := newFakeRunStarter()
	wrapped.err = errors.Join(errors.New("claim"), apperrors.ErrAEOScheduleSlotTaken)
	triggerScheduledRun(context.Background(), wrapped, time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC))
	entries := direct()
	require.Len(t, entries, 1)
	assert.Equal(t, "info", entries[0]["level"])
	assert.Equal(t, "AEO scheduled run skipped: this slot was already started", entries[0]["msg"])
}

// notOwnedRepo refuses every finalizing write the way FinishRun does when
// another replica has failed the run as abandoned.
type notOwnedRepo struct {
	*fakeAEORepo
	finishCalls int
	finishErr   error
	bound       []boundWrite
}

func (r *notOwnedRepo) WithContext(ctx context.Context) repository.AEORepository {
	r.fakeAEORepo.WithContext(ctx)
	return &notOwnedBound{repo: r, ctx: ctx}
}

type notOwnedBound struct {
	repository.AEORepository
	repo *notOwnedRepo
	ctx  context.Context
}

func (b *notOwnedBound) CreateAnswerWithCitations(answer *models.AEOAnswer, citations []models.AEOCitation) error {
	return b.repo.fakeAEORepo.CreateAnswerWithCitations(answer, citations)
}

func (b *notOwnedBound) FinishRun(*models.AEORun) error {
	_, hasDeadline := b.ctx.Deadline()
	b.repo.mu.Lock()
	defer b.repo.mu.Unlock()
	b.repo.finishCalls++
	b.repo.bound = append(b.repo.bound, boundWrite{err: b.ctx.Err(), hasDeadline: hasDeadline})
	return b.repo.finishErr
}

// TestEngineFinalizeRefusedWhenNotOwned proves the engine finalizes through
// FinishRun on a bounded fresh context and logs a Warn line carrying run_id
// only when the write is refused with ErrAEORunNotOwned; a successful write
// logs no refusal and an ordinary failure stays an error line.
func TestEngineFinalizeRefusedWhenNotOwned(t *testing.T) {
	const refusal = "AEO run finalization refused: the run is no longer owned by this instance"

	t.Run("refused", func(t *testing.T) {
		read := captureSchedulerLogs(t)
		repo := &notOwnedRepo{fakeAEORepo: &fakeAEORepo{}, finishErr: apperrors.ErrAEORunNotOwned}
		provider := &fakeProvider{name: "alpha", model: "a1", answer: ProviderAnswer{Text: "Acme leads."}}
		run := newTestRun()
		owner := "owner-a"
		run.OwnerInstance = &owner

		engine := NewEngine(repo, []Provider{provider}, EngineOptions{CleanupTimeout: time.Second})
		require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("Which CRM is best?"), testProfile()))

		require.Equal(t, 1, repo.finishCalls, "the engine attempts exactly one finalizing write")
		require.Len(t, repo.bound, 1)
		assert.NoError(t, repo.bound[0].err, "the finalizing write runs on a live context")
		assert.True(t, repo.bound[0].hasDeadline, "the finalizing context is bounded")

		var refusals []map[string]any
		for _, entry := range read() {
			if entry["msg"] == refusal {
				refusals = append(refusals, entry)
			}
		}
		require.Len(t, refusals, 1)
		entry := refusals[0]
		assert.Equal(t, "warning", entry["level"])
		assert.EqualValues(t, run.ID, entry["run_id"])
		for key := range entry {
			assert.Contains(t, []string{"level", "msg", "time", "component", "run_id"}, key,
				"the refusal line carries run_id only")
		}
		for _, value := range entry {
			text, _ := value.(string)
			assert.NotContains(t, text, "owner-a")
			assert.NotContains(t, text, "Which CRM")
			assert.NotContains(t, text, "Acme")
		}
	})

	t.Run("accepted", func(t *testing.T) {
		read := captureSchedulerLogs(t)
		repo := &notOwnedRepo{fakeAEORepo: &fakeAEORepo{}}
		run := newTestRun()

		engine := NewEngine(repo, []Provider{&fakeProvider{name: "alpha", model: "a1"}}, EngineOptions{})
		require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("q1"), testProfile()))

		require.Equal(t, 1, repo.finishCalls)
		for _, entry := range read() {
			assert.NotEqual(t, refusal, entry["msg"])
		}
	})

	t.Run("other failure", func(t *testing.T) {
		read := captureSchedulerLogs(t)
		repo := &notOwnedRepo{fakeAEORepo: &fakeAEORepo{}, finishErr: errors.New("connection reset")}
		run := newTestRun()

		engine := NewEngine(repo, []Provider{&fakeProvider{name: "alpha", model: "a1"}}, EngineOptions{})
		require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("q1"), testProfile()))

		var levels []any
		for _, entry := range read() {
			assert.NotEqual(t, refusal, entry["msg"])
			if entry["msg"] == "could not finalize AEO run; left for abandoned-run recovery" {
				levels = append(levels, entry["level"])
			}
		}
		assert.Equal(t, []any{"error"}, levels)
	})
}
