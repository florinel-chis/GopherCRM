package service

// Regression roots for the retention purge (issue #217), on in-memory SQLite with
// the production one-connection pool and on a disposable MySQL schema from
// TEST_MYSQL_DSN. The MySQL roots skip without the variable, which the named
// acceptance checks reject, and fail when it names an unreachable server.
//
// The seams are GORM callbacks registered on the test's own handle, so a scenario
// can pause or fail one statement of a real chunk without changing the code under
// test. Every wait is bounded. On MySQL a blocked statement is confirmed in
// information_schema.PROCESSLIST on two consecutive polls; the single SQLite
// connection cannot overlap two transactions, so those roots prove the same
// outcomes in sequence.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	formPurgeTestRetention = 72 * time.Hour
	formPurgeBacklog       = 1201
)

// The statements the concurrency roots wait on. MariaDB can keep `?` in INFO, so
// no pattern depends on a parameter's value.
var (
	formPurgeTokenLock = regexp.MustCompile("^SELECT \\* FROM `form_confirmation_tokens` .*FOR UPDATE$")
	formResubmitSpend  = regexp.MustCompile("^UPDATE `form_confirmation_tokens` SET `used_at`=")
)

// ------------------------------------------------------------------- seams ---

// formPurgeDBHooks runs a function before a delete or an insert reaches the
// database, on the handle the hooks were installed on.
type formPurgeDBHooks struct {
	mu           sync.Mutex
	beforeDelete func(table string) error
	beforeCreate func(table string) error
}

func installFormPurgeDBHooks(t *testing.T, db *gorm.DB) *formPurgeDBHooks {
	t.Helper()
	hooks := &formPurgeDBHooks{}
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("form_purge_test:before_delete", func(tx *gorm.DB) {
		hooks.mu.Lock()
		fn := hooks.beforeDelete
		hooks.mu.Unlock()
		if fn != nil {
			if err := fn(tx.Statement.Table); err != nil {
				_ = tx.AddError(err)
			}
		}
	}))
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("form_purge_test:before_create", func(tx *gorm.DB) {
		hooks.mu.Lock()
		fn := hooks.beforeCreate
		hooks.mu.Unlock()
		if fn != nil {
			if err := fn(tx.Statement.Table); err != nil {
				_ = tx.AddError(err)
			}
		}
	}))
	return hooks
}

func (h *formPurgeDBHooks) onDelete(fn func(table string) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeDelete = fn
}

func (h *formPurgeDBHooks) onCreate(fn func(table string) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeCreate = fn
}

func (h *formPurgeDBHooks) clear() {
	h.onDelete(nil)
	h.onCreate(nil)
}

// formPurgeGate parks the first caller until released. It fires once, so a replay
// of the parked transaction runs straight through.
type formPurgeGate struct {
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func newFormPurgeGate() *formPurgeGate {
	return &formPurgeGate{reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *formPurgeGate) pause() {
	first := false
	g.once.Do(func() {
		first = true
		close(g.reached)
	})
	if !first {
		return
	}
	select {
	case <-g.release:
	case <-time.After(confirmationTimeout):
	}
}

func (g *formPurgeGate) awaitReached(t *testing.T) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(confirmationTimeout):
		t.Fatal("the transaction never reached the gate")
	}
}

// awaitFormPurgeLockWait returns once a statement matching target is seen
// executing on the same connection on two consecutive PROCESSLIST polls.
func awaitFormPurgeLockWait(t *testing.T, db *gorm.DB, target *regexp.Regexp) {
	t.Helper()
	probe := &lockWaitProbe{target: target}
	deadline := time.Now().Add(confirmationTimeout)
	for time.Now().Before(deadline) {
		var rows []executingStatement
		err := db.Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
			AND INFO IS NOT NULL`).Scan(&rows).Error
		if err != nil {
			rows = nil
		}
		if probe.observe(rows) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no statement matching %s waited on a lock", target)
}

// purgeChunkLog records what each chunk returned, including those of an attempt
// that later rolled back.
type purgeChunkLog struct {
	mu     sync.Mutex
	chunks []repository.FormSubmissionPurgeChunk
}

func (l *purgeChunkLog) add(chunk repository.FormSubmissionPurgeChunk) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chunks = append(l.chunks, chunk)
}

func (l *purgeChunkLog) all() []repository.FormSubmissionPurgeChunk {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]repository.FormSubmissionPurgeChunk(nil), l.chunks...)
}

type purgeRecordingRepo struct {
	repository.FormRepository
	log *purgeChunkLog
}

func (r *purgeRecordingRepo) WithTx(tx *gorm.DB) repository.FormRepository {
	return &purgeRecordingRepo{FormRepository: r.FormRepository.WithTx(tx), log: r.log}
}

func (r *purgeRecordingRepo) PurgeSubmissionChunk(q repository.FormSubmissionPurgeQuery) (repository.FormSubmissionPurgeChunk, error) {
	chunk, err := r.FormRepository.PurgeSubmissionChunk(q)
	if err == nil {
		r.log.add(chunk)
	}
	return chunk, err
}

// ------------------------------------------------------------------- seeds ---

func formPurgeTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*confirmationTimeout)
	t.Cleanup(cancel)
	return ctx
}

func newFormPurger(db *gorm.DB, opts ...FormSubmissionPurgerOption) *FormSubmissionPurger {
	return NewFormSubmissionPurger(repository.NewFormRepository(db), utils.NewTransactionManager(db), formPurgeTestRetention, opts...)
}

func fixedFormPurgeClock(now time.Time) FormSubmissionPurgerOption {
	return WithFormPurgeClock(func() time.Time { return now })
}

// seedFormPurgeForm stores a form directly, for roots that need no service.
func seedFormPurgeForm(t *testing.T, db *gorm.DB) *models.Form {
	t.Helper()
	form := &models.Form{
		Name:     "Purge form",
		PublicID: fmt.Sprintf("purge-%d", formTestDBSeq.Add(1)),
		Status:   models.FormStatusPublished,
		Fields:   []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
	}
	require.NoError(t, db.Create(form).Error)
	return form
}

func seedFormPurgeRows(t *testing.T, db *gorm.DB, formID uint, status models.FormSubmissionStatus, count int, createdAt time.Time) []uint {
	t.Helper()
	rows := make([]models.FormSubmission, count)
	for i := range rows {
		rows[i] = models.FormSubmission{
			FormID:    formID,
			Status:    status,
			Email:     fmt.Sprintf("visitor-%d@example.com", i),
			Data:      map[string]string{"email": fmt.Sprintf("visitor-%d@example.com", i)},
			IPAddress: "203.0.113.7",
		}
		rows[i].CreatedAt = createdAt
		rows[i].UpdatedAt = createdAt
	}
	require.NoError(t, db.CreateInBatches(rows, 200).Error)
	ids := make([]uint, count)
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}

func seedFormPurgeToken(t *testing.T, db *gorm.DB, submissionID uint, expiresAt time.Time, usedAt *time.Time) uint {
	t.Helper()
	token := &models.FormConfirmationToken{
		SubmissionID: submissionID,
		TokenHash:    fmt.Sprintf("purge-%058d", formTestDBSeq.Add(1)),
		ExpiresAt:    expiresAt,
		UsedAt:       usedAt,
	}
	require.NoError(t, db.Create(token).Error)
	return token.ID
}

func unscopedFormRows(t *testing.T, db *gorm.DB, model interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Unscoped().Model(model).Where(where, args...).Count(&count).Error)
	return count
}

func allFormSubmissions(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	return unscopedFormRows(t, db, &models.FormSubmission{}, "1 = 1")
}

func allFormTokens(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	return unscopedFormRows(t, db, &models.FormConfirmationToken{}, "1 = 1")
}

func oldFormPurgeTime() time.Time {
	return time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Second)
}

// ---------------------------------------------------------- AC-4: backlog ---

func TestFormSubmissionPurgeBacklogChunksSQLite(t *testing.T) {
	runFormSubmissionPurgeBacklogChunks(t, openFormTestSQLite(t))
}

func TestFormSubmissionPurgeBacklogChunksMySQL(t *testing.T) {
	runFormSubmissionPurgeBacklogChunks(t, openFormTestMySQL(t, 2))
}

func runFormSubmissionPurgeBacklogChunks(t *testing.T, db *gorm.DB) {
	form := seedFormPurgeForm(t, db)
	old := oldFormPurgeTime()
	seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, formPurgeBacklog, old)
	pending := seedFormPurgeRows(t, db, form.ID, models.FormSubmissionPending, 1, old)
	usedAt := time.Now().Add(-time.Hour)
	seedFormPurgeToken(t, db, pending[0], time.Now().Add(time.Hour), &usedAt)
	seedFormPurgeToken(t, db, pending[0], time.Now().Add(-time.Hour), nil)

	log := &purgeChunkLog{}
	repo := &purgeRecordingRepo{FormRepository: repository.NewFormRepository(db), log: log}
	purger := NewFormSubmissionPurger(repo, utils.NewTransactionManager(db), formPurgeTestRetention)

	result, err := purger.Sweep(formPurgeTestContext(t))
	require.NoError(t, err)
	assert.Equal(t, FormPurgeResult{Spam: formPurgeBacklog, Pending: 1, Tokens: 2, Chunks: 4}, result)
	assert.Zero(t, allFormSubmissions(t, db))
	assert.Zero(t, allFormTokens(t, db))

	chunks := log.all()
	require.Len(t, chunks, 4, "3 spam chunks and 1 pending chunk, in separate transactions")
	for _, chunk := range chunks {
		assert.LessOrEqual(t, chunk.Submissions, int64(formPurgeChunkSize))
	}
	assert.EqualValues(t, 500, chunks[0].Submissions)
	assert.EqualValues(t, 500, chunks[1].Submissions)
	assert.EqualValues(t, 201, chunks[2].Submissions)
	assert.EqualValues(t, 1, chunks[3].Submissions)
	assert.EqualValues(t, 2, chunks[3].Tokens)

	again, err := purger.Sweep(formPurgeTestContext(t))
	require.NoError(t, err)
	assert.Equal(t, FormPurgeResult{}, again, "a second sweep removes nothing and reports zeros")
}

// ------------------------------------------------------ AC-5: chunk atomic ---

func TestFormSubmissionPurgeChunkAtomicSQLite(t *testing.T) {
	runFormSubmissionPurgeChunkAtomic(t, openFormTestSQLite(t))
}

func TestFormSubmissionPurgeChunkAtomicMySQL(t *testing.T) {
	runFormSubmissionPurgeChunkAtomic(t, openFormTestMySQL(t, 2))
}

func runFormSubmissionPurgeChunkAtomic(t *testing.T, db *gorm.DB) {
	hooks := installFormPurgeDBHooks(t, db)
	form := seedFormPurgeForm(t, db)
	old := oldFormPurgeTime()
	seedPending := func(count int) {
		for _, id := range seedFormPurgeRows(t, db, form.ID, models.FormSubmissionPending, count, old) {
			seedFormPurgeToken(t, db, id, time.Now().Add(-time.Hour), nil)
		}
	}

	t.Run("a failed submission delete keeps earlier chunks and rolls this one back", func(t *testing.T) {
		seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, 501, old)
		seedPending(3)

		injected := errors.New("injected submission delete failure")
		var submissionDeletes, tokenDeletes atomic.Int32
		hooks.onDelete(func(table string) error {
			switch table {
			case "form_confirmation_tokens":
				tokenDeletes.Add(1)
			case "form_submissions":
				// Two spam chunks commit; the third delete is the pending chunk.
				if submissionDeletes.Add(1) == 3 {
					return injected
				}
			}
			return nil
		})

		purger := newFormPurger(db)
		result, err := purger.Sweep(formPurgeTestContext(t))
		require.ErrorIs(t, err, injected)
		assert.Equal(t, FormPurgeResult{Spam: 501, Chunks: 2}, result, "only committed chunks are reported")
		assert.EqualValues(t, 1, tokenDeletes.Load(), "the token delete ran and has to be rolled back")
		assert.Zero(t, unscopedFormRows(t, db, &models.FormSubmission{}, "status = ?", models.FormSubmissionSpam),
			"every earlier committed chunk stays removed")
		assert.EqualValues(t, 3, unscopedFormRows(t, db, &models.FormSubmission{}, "status = ?", models.FormSubmissionPending))
		assert.EqualValues(t, 3, allFormTokens(t, db), "the chunk's token deletes are rolled back")

		hooks.clear()
		result, err = purger.Sweep(formPurgeTestContext(t))
		require.NoError(t, err)
		assert.Equal(t, FormPurgeResult{Pending: 3, Tokens: 3, Chunks: 1}, result)
		assert.Zero(t, allFormSubmissions(t, db))
		assert.Zero(t, allFormTokens(t, db))
	})

	t.Run("a deadlock is replayed inside the chunk and counted once", func(t *testing.T) {
		seedPending(2)
		var failed atomic.Bool
		hooks.onDelete(func(table string) error {
			if table == "form_submissions" && failed.CompareAndSwap(false, true) {
				return errors.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction")
			}
			return nil
		})
		defer hooks.clear()

		result, err := newFormPurger(db).Sweep(formPurgeTestContext(t))
		require.NoError(t, err)
		assert.True(t, failed.Load())
		assert.Equal(t, FormPurgeResult{Pending: 2, Tokens: 2, Chunks: 1}, result)
		assert.Zero(t, allFormSubmissions(t, db))
		assert.Zero(t, allFormTokens(t, db))
	})

	t.Run("a cancelled context starts no chunk", func(t *testing.T) {
		seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, 1, old)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := newFormPurger(db).Sweep(ctx)
		require.ErrorIs(t, err, context.Canceled)
		assert.EqualValues(t, 1, allFormSubmissions(t, db))
	})
}

// -------------------------------------------------- AC-6: confirmation race ---

func TestFormSubmissionPurgeConcurrentConfirmationSQLite(t *testing.T) {
	runFormSubmissionPurgeConcurrentConfirmation(t, openFormTestSQLite(t), false)
}

func TestFormSubmissionPurgeConcurrentConfirmationMySQL(t *testing.T) {
	runFormSubmissionPurgeConcurrentConfirmation(t, openFormTestMySQL(t, 6), true)
}

func runFormSubmissionPurgeConcurrentConfirmation(t *testing.T, db *gorm.DB, concurrent bool) {
	c := newConfirmationFixture(t, db)

	// The token is unexpired for the real clock the confirmation reads, and
	// expired by the purge's selection: selection skips a spendable token, so
	// this is the only way a chunk can pick a row a confirmation is claiming.
	purgeClock := time.Now().Add(formConfirmationTTL + time.Hour)
	purger := newFormPurger(db, fixedFormPurgeClock(purgeClock))

	// The clock above moved the cutoff forward by 49 hours, so the row is
	// backdated well past it.
	backdate := func(t *testing.T) {
		t.Helper()
		c.exec(t, "UPDATE form_submissions SET created_at = ?", oldFormPurgeTime())
	}

	t.Run("a confirmation that claimed first keeps its row", func(t *testing.T) {
		token := c.pendingLink(t)
		backdate(t)

		var result FormPurgeResult
		var confirmErr, sweepErr error
		if concurrent {
			claimed := newFormPurgeGate()
			c.hooks.install(func(h *confirmationHooks) {
				h.beforeLeadCreate = func(*models.Lead) error {
					claimed.pause()
					return nil
				}
			})
			confirmed := make(chan error, 1)
			go func() { confirmed <- c.service.ConfirmSubmission(context.Background(), token) }()
			claimed.awaitReached(t)

			swept := make(chan error, 1)
			go func() {
				var err error
				result, err = purger.Sweep(context.Background())
				swept <- err
			}()
			awaitFormPurgeLockWait(t, db, formPurgeTokenLock)

			close(claimed.release)
			confirmErr = <-confirmed
			sweepErr = <-swept
		} else {
			confirmErr = c.service.ConfirmSubmission(context.Background(), token)
			result, sweepErr = purger.Sweep(context.Background())
		}

		require.NoError(t, confirmErr)
		require.NoError(t, sweepErr)
		assert.Equal(t, FormPurgeResult{}, result, "the purge removes nothing")
		c.assertConfirmedAndAnnounced(t)
		assert.EqualValues(t, 1, allFormSubmissions(t, db))
		assert.EqualValues(t, 1, allFormTokens(t, db), "the spent token stays with its confirmed row")
		stored := c.submissions(t, c.form.ID)
		require.Len(t, stored, 1)
		assert.Equal(t, models.FormSubmissionConfirmed, stored[0].Status)
		assert.NotNil(t, stored[0].LeadID, "the row stays linked to its lead")
	})

	t.Run("a purge that removed the row first refuses the confirmation", func(t *testing.T) {
		token := c.pendingLink(t)
		backdate(t)

		var result FormPurgeResult
		var confirmErr, sweepErr error
		if concurrent {
			looked := newFormPurgeGate()
			c.hooks.install(func(h *confirmationHooks) {
				h.afterTokenLookup = func(*gorm.DB, *models.FormConfirmationToken) error {
					looked.pause()
					return nil
				}
			})
			confirmed := make(chan error, 1)
			go func() { confirmed <- c.service.ConfirmSubmission(context.Background(), token) }()
			looked.awaitReached(t)

			// The lookup holds no lock, so the purge finishes while the
			// confirmation still believes the token is spendable.
			result, sweepErr = purger.Sweep(formPurgeTestContext(t))
			close(looked.release)
			confirmErr = <-confirmed
		} else {
			result, sweepErr = purger.Sweep(formPurgeTestContext(t))
			confirmErr = c.service.ConfirmSubmission(context.Background(), token)
		}

		require.NoError(t, sweepErr)
		assert.Equal(t, FormPurgeResult{Pending: 1, Tokens: 1, Chunks: 1}, result)
		require.ErrorIs(t, confirmErr, ErrInvalidConfirmationToken)
		assert.Zero(t, allFormSubmissions(t, db), "a confirmation never restores a purged row")
		assert.Zero(t, allFormTokens(t, db))
		assert.Zero(t, unscopedFormRows(t, db, &models.Lead{}, "1 = 1"), "no lead comes out of it")
		assert.Empty(t, c.mail.messages())
	})
}

// ----------------------------------------------------- AC-7: competing sweeps ---

func TestFormSubmissionPurgeCompetingSweepsSQLite(t *testing.T) {
	runFormSubmissionPurgeCompetingSweeps(t, openFormTestSQLite(t))
}

func TestFormSubmissionPurgeCompetingSweepsMySQL(t *testing.T) {
	runFormSubmissionPurgeCompetingSweeps(t, openFormTestMySQL(t, 4))
}

func runFormSubmissionPurgeCompetingSweeps(t *testing.T, db *gorm.DB) {
	form := seedFormPurgeForm(t, db)
	seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, formPurgeBacklog, oldFormPurgeTime())
	ctx := formPurgeTestContext(t)

	type outcome struct {
		result FormPurgeResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			result, err := newFormPurger(db).Sweep(ctx)
			outcomes <- outcome{result, err}
		}()
	}
	close(start)

	var removed int64
	for i := 0; i < 2; i++ {
		got := <-outcomes
		require.NoError(t, got.err, "a lock conflict is retried or resolved by finding nothing left")
		removed += got.result.Spam
		assert.Zero(t, got.result.Pending)
	}
	assert.EqualValues(t, formPurgeBacklog, removed, "each row is counted once")
	assert.Zero(t, allFormSubmissions(t, db), "no eligible row remains")
}

// ------------------------------------------------------- AC-8: a late link ---

func TestFormSubmissionPurgeLateConfirmationLinkSQLite(t *testing.T) {
	runFormSubmissionPurgeLateConfirmationLink(t, openFormTestSQLite(t))
}

func TestFormSubmissionPurgeLateConfirmationLinkMySQL(t *testing.T) {
	runFormSubmissionPurgeLateConfirmationLink(t, openFormTestMySQL(t, 3))
}

func runFormSubmissionPurgeLateConfirmationLink(t *testing.T, db *gorm.DB) {
	c := newConfirmationFixture(t, db)
	token := c.pendingLink(t)
	c.exec(t, "UPDATE form_submissions SET created_at = ?", oldFormPurgeTime())
	c.exec(t, "UPDATE form_confirmation_tokens SET expires_at = ?", time.Now().Add(-time.Hour))

	result, err := newFormPurger(db).Sweep(formPurgeTestContext(t))
	require.NoError(t, err)
	assert.Equal(t, FormPurgeResult{Pending: 1, Tokens: 1, Chunks: 1}, result)

	assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
	assert.Zero(t, allFormSubmissions(t, db), "no submission comes back")
	assert.Zero(t, allFormTokens(t, db))
	assert.Zero(t, unscopedFormRows(t, db, &models.Lead{}, "1 = 1"), "no lead is created")
	assert.Empty(t, c.mail.messages(), "no mail goes out")
}

// ------------------------------------------------------ AC-10: clock basis ---

func TestFormSubmissionPurgeCutoffClockBasisSQLite(t *testing.T) {
	runFormSubmissionPurgeCutoffClockBasis(t, openFormTestSQLite(t))
}

func TestFormSubmissionPurgeCutoffClockBasisMySQL(t *testing.T) {
	runFormSubmissionPurgeCutoffClockBasis(t, openFormTestMySQL(t, 2))
}

func runFormSubmissionPurgeCutoffClockBasis(t *testing.T, db *gorm.DB) {
	form := seedFormPurgeForm(t, db)
	for _, tc := range []struct {
		name string
		zone *time.Location
	}{
		{"+10:00", time.FixedZone("EAST", 10*60*60)},
		{"-08:00", time.FixedZone("WEST", -8*60*60)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Exec("DELETE FROM form_confirmation_tokens").Error)
			require.NoError(t, db.Exec("DELETE FROM form_submissions").Error)

			now := time.Date(2026, 10, 15, 12, 0, 0, 0, tc.zone)
			cutoff := now.UTC().Add(-formPurgeTestRetention)

			atCutoff := seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, 1, cutoff)[0]
			older := seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, 1, cutoff.Add(-time.Second))[0]

			// Token expiry is written from the local clock, so it carries the
			// clock's zone, the way the submission path writes it.
			unexpired := seedFormPurgeRows(t, db, form.ID, models.FormSubmissionPending, 1, cutoff.Add(-time.Hour))[0]
			unexpiredToken := seedFormPurgeToken(t, db, unexpired, now.Add(time.Hour), nil)
			expired := seedFormPurgeRows(t, db, form.ID, models.FormSubmissionPending, 1, cutoff.Add(-time.Hour))[0]
			expiredToken := seedFormPurgeToken(t, db, expired, now.Add(-time.Hour), nil)

			result, err := newFormPurger(db, fixedFormPurgeClock(now)).Sweep(formPurgeTestContext(t))
			require.NoError(t, err)
			assert.Equal(t, FormPurgeResult{Spam: 1, Pending: 1, Tokens: 1, Chunks: 2}, result)

			exists := func(id uint) int64 {
				return unscopedFormRows(t, db, &models.FormSubmission{}, "id = ?", id)
			}
			assert.EqualValues(t, 1, exists(atCutoff), "a row exactly at the cutoff stays")
			assert.Zero(t, exists(older), "a row one second older goes")
			assert.EqualValues(t, 1, exists(unexpired), "a pending row with an unexpired token stays")
			assert.Zero(t, exists(expired), "a pending row with an expired token goes")
			assert.EqualValues(t, 1, unscopedFormRows(t, db, &models.FormConfirmationToken{}, "id = ?", unexpiredToken))
			assert.Zero(t, unscopedFormRows(t, db, &models.FormConfirmationToken{}, "id = ?", expiredToken))
		})
	}
}

// ------------------------------------------------ AC-11: too short a retention ---

func TestFormSubmissionPurgeRefusesShortRetention(t *testing.T) {
	db := openFormTestSQLite(t)
	form := seedFormPurgeForm(t, db)
	seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, 1, oldFormPurgeTime())

	for _, retention := range []time.Duration{-time.Hour, 0, time.Hour, formPurgeTestRetention - time.Nanosecond} {
		t.Run(retention.String(), func(t *testing.T) {
			// Nil dependencies panic on any use, so this proves the refusal comes
			// before anything is read or written.
			_, err := NewFormSubmissionPurger(nil, nil, retention).Sweep(context.Background())
			require.ErrorIs(t, err, ErrFormRetentionTooShort)

			result, err := NewFormSubmissionPurger(repository.NewFormRepository(db), utils.NewTransactionManager(db), retention).Sweep(context.Background())
			require.ErrorIs(t, err, ErrFormRetentionTooShort)
			assert.Equal(t, FormPurgeResult{}, result)
			assert.EqualValues(t, 1, allFormSubmissions(t, db), "no row is removed")
		})
	}

	t.Run("72 hours exactly is accepted", func(t *testing.T) {
		result, err := newFormPurger(db).Sweep(context.Background())
		require.NoError(t, err)
		assert.EqualValues(t, 1, result.Spam)
	})

	t.Run("a sweep refuses to nest in a transaction", func(t *testing.T) {
		manager := utils.NewTransactionManager(db)
		err := manager.WithTransaction(context.Background(), func(ctx context.Context) error {
			_, err := newFormPurger(db).Sweep(ctx)
			return err
		})
		require.ErrorIs(t, err, utils.ErrTransactionRootRequired)
	})
}

// -------------------------------------------------------- AC-15: log privacy ---

func TestFormSubmissionPurgeLogsOmitPersonalData(t *testing.T) {
	const (
		email     = "purge-email-canary@example.test"
		ip        = "198.51.100.77"
		userAgent = "purge-user-agent-canary"
		referrer  = "https://purge-referrer-canary.example/page"
		value     = "purge-submitted-value-canary"
	)
	canaries := []string{email, ip, userAgent, referrer, value}

	output, hook := capturePrivacyLogs(t)
	db := openFormTestSQLite(t)
	hooks := installFormPurgeDBHooks(t, db)
	form := seedFormPurgeForm(t, db)

	seed := func(status models.FormSubmissionStatus) {
		row := &models.FormSubmission{
			FormID:    form.ID,
			Status:    status,
			Email:     email,
			Data:      map[string]string{"email": email, "message": value},
			IPAddress: ip,
			UserAgent: userAgent,
			Referrer:  referrer,
		}
		row.CreatedAt = oldFormPurgeTime()
		require.NoError(t, db.Create(row).Error)
	}

	awaitEntry := func(t *testing.T, message string) map[string]interface{} {
		t.Helper()
		deadline := time.Now().Add(confirmationTimeout)
		for time.Now().Before(deadline) {
			for _, entry := range hook.AllEntries() {
				if entry.Message == message {
					return entry.Data
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no log line %q", message)
		return nil
	}
	fieldNames := func(data map[string]interface{}) []string {
		names := make([]string, 0, len(data))
		for name := range data {
			names = append(names, name)
		}
		return names
	}
	stop := func(t *testing.T, runtime *FormRetentionRuntime) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), confirmationTimeout)
		defer cancel()
		require.NoError(t, runtime.Stop(ctx))
	}

	t.Run("a sweep that removes rows", func(t *testing.T) {
		seed(models.FormSubmissionSpam)
		seed(models.FormSubmissionPending)
		runtime := NewFormRetentionRuntime(newFormPurger(db), time.Hour)
		runtime.Start(context.Background())
		data := awaitEntry(t, "form retention sweep completed")
		stop(t, runtime)

		assert.ElementsMatch(t, []string{"spam_removed", "pending_removed", "tokens_removed", "chunks"}, fieldNames(data))
		assert.EqualValues(t, 1, data["spam_removed"])
		assert.EqualValues(t, 1, data["pending_removed"])
		assert.EqualValues(t, 0, data["tokens_removed"])
		assert.EqualValues(t, 2, data["chunks"])
		assert.Zero(t, allFormSubmissions(t, db))
	})

	t.Run("a sweep that fails", func(t *testing.T) {
		seed(models.FormSubmissionSpam)
		// The way a driver error repeats a value in its message.
		hooks.onDelete(func(string) error {
			return fmt.Errorf("Error 1451: cannot delete '%s' from %s for %s", email, ip, value)
		})
		defer hooks.clear()

		runtime := NewFormRetentionRuntime(newFormPurger(db), time.Hour)
		runtime.Start(context.Background())
		awaitEntry(t, "form retention sweep failed")
		stop(t, runtime)
		assert.EqualValues(t, 1, allFormSubmissions(t, db))
	})

	t.Run("the enabled line", func(t *testing.T) {
		LogFormRetentionEnabled(90)
		data := awaitEntry(t, "form retention purge enabled")
		assert.Equal(t, []string{"retention_days"}, fieldNames(data))
		assert.EqualValues(t, 90, data["retention_days"])
	})

	logged := output.String()
	require.NotEmpty(t, logged)
	for _, canary := range canaries {
		assert.NotContains(t, logged, canary)
	}
}

// ---------------------------------------------- AC-16: a concurrent re-submit ---

func TestFormSubmissionPurgeConcurrentResubmissionSQLite(t *testing.T) {
	runFormSubmissionPurgeConcurrentResubmission(t, openFormTestSQLite(t), false)
}

func TestFormSubmissionPurgeConcurrentResubmissionMySQL(t *testing.T) {
	runFormSubmissionPurgeConcurrentResubmission(t, openFormTestMySQL(t, 6), true)
}

// resubmissionFaultRepo fails the first InvalidatePendingTokens the way a deadlock
// victim does, and records the id each attempt starts from.
type resubmissionFaultRepo struct {
	repository.FormRepository
	state *resubmissionFaultState
}

type resubmissionFaultState struct {
	mu         sync.Mutex
	entryIDs   []uint
	invalidate int
}

func (r *resubmissionFaultRepo) WithTx(tx *gorm.DB) repository.FormRepository {
	return &resubmissionFaultRepo{FormRepository: r.FormRepository.WithTx(tx), state: r.state}
}

func (r *resubmissionFaultRepo) CreateSubmission(sub *models.FormSubmission) error {
	r.state.mu.Lock()
	r.state.entryIDs = append(r.state.entryIDs, sub.ID)
	r.state.mu.Unlock()
	return r.FormRepository.CreateSubmission(sub)
}

func (r *resubmissionFaultRepo) InvalidatePendingTokens(formID uint, email string) error {
	r.state.mu.Lock()
	r.state.invalidate++
	first := r.state.invalidate == 1
	r.state.mu.Unlock()
	if first {
		return errors.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction")
	}
	return r.FormRepository.InvalidatePendingTokens(formID, email)
}

func runFormSubmissionPurgeConcurrentResubmission(t *testing.T, db *gorm.DB, concurrent bool) {
	c := newConfirmationFixture(t, db)
	hooks := installFormPurgeDBHooks(t, db)
	purger := newFormPurger(db)

	// seedOld stores an older pending row of the address validSubmission uses,
	// holding an unused, expired token: eligible, and also what a re-submission
	// has to spend.
	seedOld := func(t *testing.T) (uint, uint) {
		t.Helper()
		c.reset(t)
		hooks.clear()
		old := &models.FormSubmission{
			FormID: c.form.ID,
			Status: models.FormSubmissionPending,
			Email:  "ada@example.com",
			Data:   map[string]string{"email": "ada@example.com"},
		}
		old.CreatedAt = oldFormPurgeTime()
		require.NoError(t, db.Create(old).Error)
		return old.ID, seedFormPurgeToken(t, db, old.ID, time.Now().Add(-time.Hour), nil)
	}
	submit := func() error {
		_, err := c.service.SubmitPublic(context.Background(), c.form.PublicID, validSubmission(), submissionMeta())
		return err
	}
	assertOutcome := func(t *testing.T, oldID, oldToken uint) {
		t.Helper()
		assert.Zero(t, unscopedFormRows(t, db, &models.FormSubmission{}, "id = ?", oldID), "the old row is gone")
		assert.Zero(t, unscopedFormRows(t, db, &models.FormConfirmationToken{}, "id = ?", oldToken), "its token is gone")

		var rows []models.FormSubmission
		require.NoError(t, db.Unscoped().Find(&rows).Error)
		require.Len(t, rows, 1, "the new pending row stays")
		assert.Equal(t, models.FormSubmissionPending, rows[0].Status)
		assert.Nil(t, rows[0].LeadID)

		var tokens []models.FormConfirmationToken
		require.NoError(t, db.Unscoped().Where("submission_id = ?", rows[0].ID).Find(&tokens).Error)
		require.Len(t, tokens, 1, "with exactly one token")
		assert.Nil(t, tokens[0].UsedAt, "which is spendable")
		assert.True(t, tokens[0].ExpiresAt.After(time.Now()))
		assert.EqualValues(t, 1, allFormTokens(t, db))

		messages := c.mail.messages()
		require.Len(t, messages, 1, "exactly one confirmation mail goes out")
		raw := tokenFromLink(t, messages[0].Body)

		c.mail.reset()
		require.NoError(t, c.service.ConfirmSubmission(context.Background(), raw), "the token confirms")
		stored := c.submissions(t, c.form.ID)
		require.Len(t, stored, 1)
		assert.Equal(t, models.FormSubmissionConfirmed, stored[0].Status)
		assert.NotNil(t, stored[0].LeadID)
	}

	t.Run("the purge holds its locks first", func(t *testing.T) {
		oldID, oldToken := seedOld(t)
		var result FormPurgeResult
		var sweepErr, submitErr error

		if concurrent {
			locked := newFormPurgeGate()
			hooks.onDelete(func(table string) error {
				if table == "form_confirmation_tokens" {
					locked.pause()
				}
				return nil
			})
			swept := make(chan error, 1)
			go func() {
				var err error
				result, err = purger.Sweep(context.Background())
				swept <- err
			}()
			locked.awaitReached(t)

			// Both locks are held before the first delete has run.
			for _, probe := range []struct {
				table string
				id    uint
			}{{"form_confirmation_tokens", oldToken}, {"form_submissions", oldID}} {
				var ids []uint
				err := db.Raw("SELECT id FROM "+probe.table+" WHERE id = ? FOR UPDATE NOWAIT", probe.id).Scan(&ids).Error
				assert.Errorf(t, err, "%s must be locked before the chunk's first delete", probe.table)
			}

			submitted := make(chan error, 1)
			go func() { submitted <- submit() }()
			awaitFormPurgeLockWait(t, db, formResubmitSpend)

			close(locked.release)
			sweepErr = <-swept
			submitErr = <-submitted
		} else {
			result, sweepErr = purger.Sweep(formPurgeTestContext(t))
			submitErr = submit()
		}

		require.NoError(t, sweepErr)
		require.NoError(t, submitErr, "the re-submission commits, on a replay if it was the deadlock victim")
		assert.Equal(t, FormPurgeResult{Pending: 1, Tokens: 1, Chunks: 1}, result)
		hooks.clear()
		assertOutcome(t, oldID, oldToken)
	})

	t.Run("the re-submission spends the old token first", func(t *testing.T) {
		oldID, oldToken := seedOld(t)
		var result FormPurgeResult
		var sweepErr, submitErr error

		if concurrent {
			spent := newFormPurgeGate()
			hooks.onCreate(func(table string) error {
				if table == "form_confirmation_tokens" {
					spent.pause()
				}
				return nil
			})
			submitted := make(chan error, 1)
			go func() { submitted <- submit() }()
			spent.awaitReached(t)

			swept := make(chan error, 1)
			go func() {
				var err error
				result, err = purger.Sweep(context.Background())
				swept <- err
			}()
			awaitFormPurgeLockWait(t, db, formPurgeTokenLock)

			close(spent.release)
			submitErr = <-submitted
			sweepErr = <-swept
		} else {
			submitErr = submit()
			result, sweepErr = purger.Sweep(formPurgeTestContext(t))
		}

		require.NoError(t, submitErr)
		require.NoError(t, sweepErr)
		assert.Equal(t, FormPurgeResult{Pending: 1, Tokens: 1, Chunks: 1}, result, "the purge then removes the old row")
		hooks.clear()
		assertOutcome(t, oldID, oldToken)
	})

	// Last, because it rewires the service.
	t.Run("a deadlock victim is replayed with a fresh submission id", func(t *testing.T) {
		seedOld(t)
		state := &resubmissionFaultState{}
		c.wire(&resubmissionFaultRepo{FormRepository: c.repo, state: state}, c.leadRepo, c.mail)

		require.NoError(t, submit())

		state.mu.Lock()
		defer state.mu.Unlock()
		assert.Equal(t, 2, state.invalidate, "the attempt that lost was replayed once")
		assert.Equal(t, []uint{0, 0}, state.entryIDs, "each attempt starts with a fresh submission id")
		assert.Len(t, c.mail.messages(), 1, "mail goes out once, after the attempt that committed")

		var rows []models.FormSubmission
		require.NoError(t, db.Where("id <> 0 AND created_at > ?", time.Now().Add(-time.Hour)).Find(&rows).Error)
		require.Len(t, rows, 1, "the rolled-back attempt left no row")
		assert.False(t, rows[0].CreatedAt.IsZero())
		assert.True(t, strings.EqualFold(rows[0].Email, "ada@example.com"))
	})
}
