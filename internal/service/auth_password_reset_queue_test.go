package service

// The password reset queue: POST /auth/password-reset submits the email and
// answers at once; one worker issues the reset and writes the audit row.

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const resetQueueTestEmail = "queued.person@example.com"

// resetQueueIssuer records the emails it is asked to issue, in order. When
// gate is set every call waits on it; started reports each call's start.
type resetQueueIssuer struct {
	mu      sync.Mutex
	emails  []string
	gate    chan struct{}
	started chan string
	panicOn string
}

func (i *resetQueueIssuer) IssuePasswordReset(email string) (uint, error) {
	if i.started != nil {
		i.started <- email
	}
	if i.gate != nil {
		<-i.gate
	}
	if email == i.panicOn {
		panic("issuer failed")
	}
	i.mu.Lock()
	i.emails = append(i.emails, email)
	i.mu.Unlock()
	return 0, authRefusal(AuthAuditReasonAccountUnknown, 0, errPasswordResetNotIssued)
}

func (i *resetQueueIssuer) issued() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.emails...)
}

func resetQueueAuditRows(t *testing.T, db *gorm.DB) []models.AuthAuditLog {
	t.Helper()
	var rows []models.AuthAuditLog
	require.NoError(t, db.Where("method = ?", AuthAuditMethodResetRequest).Order("id").Find(&rows).Error)
	return rows
}

func stopResetQueue(t *testing.T, q *PasswordResetQueue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, q.Stop(ctx))
}

func TestPasswordResetQueue(t *testing.T) {
	t.Run("answers_before_the_issue_runs", func(t *testing.T) {
		issuer := &resetQueueIssuer{gate: make(chan struct{})}
		q := NewPasswordResetQueue(issuer, nil)
		q.Start(context.Background())
		defer stopResetQueue(t, q)
		defer close(issuer.gate)

		for _, email := range []string{"known@example.com", "unknown@example.com"} {
			done := make(chan bool, 1)
			go func() { done <- q.Submit(context.Background(), email) }()
			select {
			case ok := <-done:
				assert.True(t, ok, email)
			case <-time.After(time.Second):
				t.Fatalf("Submit blocked while the issue was held")
			}
		}
	})

	t.Run("fifo_single_worker", func(t *testing.T) {
		issuer := &resetQueueIssuer{}
		q := NewPasswordResetQueue(issuer, nil)
		want := []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com", "e@example.com"}
		// Submitted before the worker starts, so they are all waiting at once.
		for _, email := range want {
			require.True(t, q.Submit(context.Background(), email))
		}
		q.Start(context.Background())
		require.Eventually(t, func() bool { return len(issuer.issued()) == len(want) }, 5*time.Second, 10*time.Millisecond)
		stopResetQueue(t, q)
		assert.Equal(t, want, issuer.issued())
	})

	t.Run("full_queue_refused", func(t *testing.T) {
		db, _ := openOIDCTestSQLite(t)
		issuer := &resetQueueIssuer{}
		q := NewPasswordResetQueue(issuer, NewAuthAuditService(repository.NewAuthAuditRepository(db)), withPasswordResetQueueCapacity(1))
		require.True(t, q.Submit(authAuditCtx("req-queued"), "first@example.com"))
		require.False(t, q.Submit(authAuditCtx("req-full"), "second@example.com"))

		rows := resetQueueAuditRows(t, db)
		require.Len(t, rows, 1)
		assert.Equal(t, AuthAuditOutcomeRefused, rows[0].Outcome)
		assert.Equal(t, AuthAuditReasonUnavailable, rows[0].Reason)
		assert.True(t, rows[0].UserID == nil || *rows[0].UserID == 0)
		assert.Equal(t, "req-full", rows[0].RequestID)
		stopResetQueue(t, q)
		assert.Empty(t, issuer.issued(), "the queue never started, so nothing was issued")
	})

	t.Run("audit_row_keeps_request_facts", func(t *testing.T) {
		db, _ := openOIDCTestSQLite(t)
		issuer := &resetQueueIssuer{gate: make(chan struct{})}
		q := NewPasswordResetQueue(issuer, NewAuthAuditService(repository.NewAuthAuditRepository(db)))
		q.Start(context.Background())
		defer stopResetQueue(t, q)

		reqCtx, cancelRequest := context.WithCancel(authAuditCtx("req-facts"))
		require.True(t, q.Submit(reqCtx, resetQueueTestEmail))
		cancelRequest() // the request has ended before the worker writes
		close(issuer.gate)

		require.Eventually(t, func() bool { return len(resetQueueAuditRows(t, db)) == 1 }, 5*time.Second, 10*time.Millisecond)
		row := resetQueueAuditRows(t, db)[0]
		assert.Equal(t, "req-facts", row.RequestID)
		assert.Equal(t, authAuditTestNetwork, row.ClientNetwork)
		assert.Equal(t, AuthAuditReasonAccountUnknown, row.Reason)
	})

	t.Run("stop_finishes_in_flight_drops_rest_and_refuses_submit", func(t *testing.T) {
		issuer := &resetQueueIssuer{gate: make(chan struct{}), started: make(chan string, 10)}
		q := NewPasswordResetQueue(issuer, nil)
		q.Start(context.Background())
		require.True(t, q.Submit(context.Background(), "inflight@example.com"))
		select {
		case email := <-issuer.started:
			require.Equal(t, "inflight@example.com", email)
		case <-time.After(5 * time.Second):
			t.Fatal("the worker did not pick up the first item")
		}
		require.True(t, q.Submit(context.Background(), "waiting-1@example.com"))
		require.True(t, q.Submit(context.Background(), "waiting-2@example.com"))

		stopped := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stopped <- q.Stop(ctx)
		}()
		// Stop marks the queue stopped before it waits for the worker.
		require.Eventually(t, func() bool { return !q.Submit(context.Background(), "late@example.com") }, 5*time.Second, 10*time.Millisecond)
		select {
		case <-stopped:
			t.Fatal("Stop returned while the item in flight was still running")
		default:
		}
		// A Stop whose deadline passes first returns the context's error.
		short, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := q.Stop(short)
		cancelShort()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		close(issuer.gate)
		select {
		case err := <-stopped:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Stop did not join the worker")
		}
		assert.Equal(t, []string{"inflight@example.com"}, issuer.issued(), "the in-flight item finished, the waiting ones were dropped")
		assert.False(t, q.Submit(context.Background(), "after@example.com"))
		require.NoError(t, q.Stop(context.Background()), "a repeated Stop is safe")
	})

	t.Run("panic_in_item_keeps_worker", func(t *testing.T) {
		issuer := &resetQueueIssuer{panicOn: "boom@example.com"}
		q := NewPasswordResetQueue(issuer, nil)
		q.Start(context.Background())
		defer stopResetQueue(t, q)
		require.True(t, q.Submit(context.Background(), "boom@example.com"))
		require.True(t, q.Submit(context.Background(), "after-panic@example.com"))
		require.Eventually(t, func() bool { return len(issuer.issued()) == 1 }, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, []string{"after-panic@example.com"}, issuer.issued())
	})

	t.Run("logs_carry_no_email", func(t *testing.T) {
		previous := utils.Logger
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
		var output bytes.Buffer
		var outputMu sync.Mutex
		utils.Logger.SetOutput(lockedWriter{w: &output, mu: &outputMu})
		t.Cleanup(func() { utils.Logger = previous })

		db, _ := openOIDCTestSQLite(t)
		audit := NewAuthAuditService(repository.NewAuthAuditRepository(db))

		// Full: capacity one, never started.
		full := NewPasswordResetQueue(&resetQueueIssuer{}, audit, withPasswordResetQueueCapacity(1))
		require.True(t, full.Submit(authAuditCtx("req-log-1"), resetQueueTestEmail))
		require.False(t, full.Submit(authAuditCtx("req-log-2"), resetQueueTestEmail))
		// Stopped, with one waiting item dropped.
		stopResetQueue(t, full)
		require.False(t, full.Submit(authAuditCtx("req-log-3"), resetQueueTestEmail))
		// Panic.
		panicking := &resetQueueIssuer{panicOn: resetQueueTestEmail}
		worker := NewPasswordResetQueue(panicking, audit)
		worker.Start(context.Background())
		require.True(t, worker.Submit(authAuditCtx("req-log-4"), resetQueueTestEmail))
		require.True(t, worker.Submit(authAuditCtx("req-log-5"), "marker@example.com"))
		require.Eventually(t, func() bool { return len(panicking.issued()) == 1 }, 5*time.Second, 10*time.Millisecond)
		stopResetQueue(t, worker)

		outputMu.Lock()
		text := output.String()
		outputMu.Unlock()
		for _, code := range []string{"reset_queue_full", "reset_queue_stopped", "reset_worker_panic", `"dropped":1`} {
			assert.Contains(t, text, code)
		}
		assert.NotContains(t, text, resetQueueTestEmail)
		assert.NotContains(t, text, "queued.person")
		assert.NotContains(t, text, "marker@example.com")
	})

}

// lockedWriter serialises writes into a shared buffer.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
