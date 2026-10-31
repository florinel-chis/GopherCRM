package service

// Regression roots for issue #101: a double-opt-in confirmation token is
// claimed inside the transaction that confirms the submission, so a failed or
// competing confirmation can neither spend the link nor announce a lead that
// was rolled back.
//
// Every root runs on a real database — in-memory SQLite with the production
// one-connection pool, and a disposable MySQL schema when TEST_MYSQL_DSN is
// set — and every assertion reads committed rows outside any transaction. The
// hooks below interpose on the transaction-bound repositories so a scenario
// can fail, or alter rows the way a competing request would, at an exact point
// of the unit of work. All waits are bounded: a confirmation that held the only
// SQLite connection while sending mail fails at the deadline instead of hanging.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// confirmationTimeout bounds every wait and verification read. It is generous
// for a loaded CI host but far below the check timeout, so a hung confirmation
// fails the scenario instead of the package.
const confirmationTimeout = 20 * time.Second

// confirmationCompetitors is how many requests race for one link.
const confirmationCompetitors = 3

var formTestDBSeq atomic.Uint64

func confirmationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), confirmationTimeout)
	t.Cleanup(cancel)
	return ctx
}

// openFormTestMySQL creates a uniquely named disposable schema on the server
// identified by TEST_MYSQL_DSN and drops it afterwards. The named acceptance
// checks require the variable; the ordinary `go test ./...` run on a machine
// without a MySQL server skips, which the acceptance parser rejects. A
// configured but unreachable server fails rather than skips.
func openFormTestMySQL(t *testing.T, maxConns int) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, confirmationTimeout, confirmationTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_form_%d_%d", time.Now().UnixNano(), formTestDBSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	// Registered after the DROP cleanup, so it runs first and the schema is
	// dropped only once its last connection is closed.
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateFormTestSchema(t, db)
	return db
}

// ------------------------------------------------------------------- hooks ---

// confirmationHooks interposes on the repositories the service confirms
// through. Every hook fires on the transaction-bound repository, and the
// token-lookup hook receives the transaction's handle, so a scenario can alter
// rows the way a competing request would — or fail the way a broken database
// would — at an exact point of the unit of work. Access is mutex-guarded
// because the concurrent scenario installs and fires hooks from different
// goroutines.
type confirmationHooks struct {
	mu                     sync.Mutex
	afterTokenLookup       func(tx *gorm.DB, token *models.FormConfirmationToken) error
	beforeSubmissionUpdate func(sub *models.FormSubmission) error
	beforeLeadCreate       func(lead *models.Lead) error
	beforeLeadUpdate       func(lead *models.Lead) error
}

func (h *confirmationHooks) install(fn func(h *confirmationHooks)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *confirmationHooks) clear() {
	h.install(func(h *confirmationHooks) {
		h.afterTokenLookup = nil
		h.beforeSubmissionUpdate = nil
		h.beforeLeadCreate = nil
		h.beforeLeadUpdate = nil
	})
}

func (h *confirmationHooks) tokenLookup() func(*gorm.DB, *models.FormConfirmationToken) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.afterTokenLookup
}

func (h *confirmationHooks) submissionUpdate() func(*models.FormSubmission) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeSubmissionUpdate
}

func (h *confirmationHooks) leadCreate() func(*models.Lead) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeLeadCreate
}

func (h *confirmationHooks) leadUpdate() func(*models.Lead) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeLeadUpdate
}

// hookedFormRepository wraps the real repository; WithTx keeps the hooks and
// remembers the handle the wrapped repository is bound to.
type hookedFormRepository struct {
	repository.FormRepository
	db    *gorm.DB
	hooks *confirmationHooks
}

func (r *hookedFormRepository) WithTx(tx *gorm.DB) repository.FormRepository {
	return &hookedFormRepository{FormRepository: r.FormRepository.WithTx(tx), db: tx, hooks: r.hooks}
}

func (r *hookedFormRepository) GetConfirmationTokenByHash(hash string) (*models.FormConfirmationToken, error) {
	token, err := r.FormRepository.GetConfirmationTokenByHash(hash)
	if err != nil {
		return nil, err
	}
	if hook := r.hooks.tokenLookup(); hook != nil {
		if err := hook(r.db, token); err != nil {
			return nil, err
		}
	}
	return token, nil
}

func (r *hookedFormRepository) UpdateSubmission(sub *models.FormSubmission) error {
	if hook := r.hooks.submissionUpdate(); hook != nil {
		if err := hook(sub); err != nil {
			return err
		}
	}
	return r.FormRepository.UpdateSubmission(sub)
}

type hookedLeadRepository struct {
	repository.LeadRepository
	hooks *confirmationHooks
}

func (r *hookedLeadRepository) WithTx(tx *gorm.DB) repository.LeadRepository {
	return &hookedLeadRepository{LeadRepository: r.LeadRepository.WithTx(tx), hooks: r.hooks}
}

func (r *hookedLeadRepository) Create(lead *models.Lead) error {
	if hook := r.hooks.leadCreate(); hook != nil {
		if err := hook(lead); err != nil {
			return err
		}
	}
	return r.LeadRepository.Create(lead)
}

func (r *hookedLeadRepository) Update(lead *models.Lead) error {
	if hook := r.hooks.leadUpdate(); hook != nil {
		if err := hook(lead); err != nil {
			return err
		}
	}
	return r.LeadRepository.Update(lead)
}

// --------------------------------------------------------- committed state ---

// dbSnapshot is what the database holds, read outside any transaction.
type dbSnapshot struct {
	pending   int64 // submissions still awaiting confirmation
	confirmed int64 // submissions confirmed
	linked    int64 // submissions pointing at a lead
	leads     int64 // live leads
	unspent   int64 // confirmation tokens with no used_at stamp
}

var (
	pendingState   = dbSnapshot{pending: 1, unspent: 1}
	confirmedState = dbSnapshot{confirmed: 1, linked: 1, leads: 1}
)

func snapshotDB(ctx context.Context, db *gorm.DB) (dbSnapshot, error) {
	var s dbSnapshot
	count := func(dst *int64, model interface{}, where string, args ...interface{}) error {
		return db.WithContext(ctx).Model(model).Where(where, args...).Count(dst).Error
	}
	if err := count(&s.pending, &models.FormSubmission{}, "status = ?", models.FormSubmissionPending); err != nil {
		return s, err
	}
	if err := count(&s.confirmed, &models.FormSubmission{}, "status = ?", models.FormSubmissionConfirmed); err != nil {
		return s, err
	}
	if err := count(&s.linked, &models.FormSubmission{}, "lead_id IS NOT NULL"); err != nil {
		return s, err
	}
	if err := count(&s.leads, &models.Lead{}, "1 = 1"); err != nil {
		return s, err
	}
	if err := count(&s.unspent, &models.FormConfirmationToken{}, "used_at IS NULL"); err != nil {
		return s, err
	}
	return s, nil
}

type mailObservation struct {
	to    string
	state dbSnapshot
	err   error
}

// committedStateMailer records, with every message, what the database held at
// the moment the service handed the message over. It reads with its own
// bounded context outside any transaction, so a message sent before the commit
// is caught either way: with several connections the read sees the row as it
// still is for everyone else (pending, unlinked, token unspent), and on the
// one-connection pool the read cannot get a connection until the transaction
// ends and fails at the deadline instead of hanging.
type committedStateMailer struct {
	fakeFormMailer
	db       *gorm.DB
	mu       sync.Mutex
	observed []mailObservation
}

func (m *committedStateMailer) Send(to, subject, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), confirmationTimeout)
	defer cancel()
	state, err := snapshotDB(ctx, m.db)

	m.mu.Lock()
	m.observed = append(m.observed, mailObservation{to: to, state: state, err: err})
	m.mu.Unlock()
	return m.fakeFormMailer.Send(to, subject, body)
}

func (m *committedStateMailer) observations() []mailObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mailObservation, len(m.observed))
	copy(out, m.observed)
	return out
}

func (m *committedStateMailer) reset() {
	m.fakeFormMailer.reset()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observed = nil
}

// ----------------------------------------------------------------- fixture ---

type confirmationFixture struct {
	*formFixture
	hooks *confirmationHooks
	mail  *committedStateMailer
	form  *models.Form
}

func newConfirmationFixture(t *testing.T, db *gorm.DB) *confirmationFixture {
	t.Helper()
	f := newFormFixtureOn(t, config.FormsConfig{PublicBaseURL: formTestPublicBase}, db)
	c := &confirmationFixture{
		formFixture: f,
		hooks:       &confirmationHooks{},
		mail:        &committedStateMailer{db: db},
	}
	f.mailer = &c.mail.fakeFormMailer
	f.wire(
		&hookedFormRepository{FormRepository: f.repo, db: db, hooks: c.hooks},
		&hookedLeadRepository{LeadRepository: f.leadRepo, hooks: c.hooks},
		c.mail,
	)
	c.form = f.publish(t, f.optInForm())
	return c
}

// reset empties the tables a scenario writes, the recorded mail and the hooks,
// so every scenario starts from a blank slate on the shared schema.
func (c *confirmationFixture) reset(t *testing.T) {
	t.Helper()
	c.hooks.clear()
	c.mail.reset()
	ctx := confirmationContext(t)
	for _, table := range []string{"form_confirmation_tokens", "form_submissions", "leads"} {
		require.NoError(t, c.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
}

// pendingLink stores a fresh opt-in submission of the fixture's form and
// returns the raw token of its confirmation link.
func (c *confirmationFixture) pendingLink(t *testing.T) string {
	t.Helper()
	return c.pendingLinkFor(t, c.form)
}

func (c *confirmationFixture) pendingLinkFor(t *testing.T, form *models.Form) string {
	t.Helper()
	c.reset(t)
	_, err := c.service.SubmitPublic(context.Background(), form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	messages := c.mail.messages()
	require.Len(t, messages, 1, "only the confirmation mail goes out before the address is proven")
	token := tokenFromLink(t, messages[0].Body)
	c.mail.reset()
	assert.Equal(t, pendingState, c.snapshot(t))
	return token
}

func (c *confirmationFixture) snapshot(t *testing.T) dbSnapshot {
	t.Helper()
	state, err := snapshotDB(confirmationContext(t), c.db)
	require.NoError(t, err)
	return state
}

func (c *confirmationFixture) exec(t *testing.T, statement string, args ...interface{}) {
	t.Helper()
	require.NoError(t, c.db.WithContext(confirmationContext(t)).Exec(statement, args...).Error)
}

// createLead stores a lead with the submitted address ahead of time, so the
// confirmation takes the link-and-append path instead of the insert.
func (c *confirmationFixture) createLead(t *testing.T, notes string) *models.Lead {
	t.Helper()
	lead := &models.Lead{
		FirstName: "Ada",
		LastName:  "Lovelace",
		Email:     "ada@example.com",
		Status:    models.LeadStatusNew,
		OwnerID:   c.owner.ID,
		Notes:     notes,
	}
	require.NoError(t, c.leadRepo.Create(lead))
	return lead
}

func (c *confirmationFixture) leadNotes(t *testing.T, id uint) string {
	t.Helper()
	var lead models.Lead
	require.NoError(t, c.db.WithContext(confirmationContext(t)).First(&lead, id).Error)
	return lead.Notes
}

// assertConfirmedAndAnnounced checks the committed state of a successful
// confirmation and that every message was handed over only once that state
// was visible outside the transaction: two team notifications and one
// follow-up, each observing the confirmed, linked submission and the spent
// token.
func (c *confirmationFixture) assertConfirmedAndAnnounced(t *testing.T) {
	t.Helper()
	assert.Equal(t, confirmedState, c.snapshot(t))

	observations := c.mail.observations()
	require.Len(t, observations, 3, "two notifications and one follow-up, once")
	recipients := map[string]int{}
	for _, o := range observations {
		recipients[o.to]++
		require.NoError(t, o.err, "the mail to %s could not read committed state", o.to)
		assert.Equal(t, confirmedState, o.state, "the mail to %s was sent before the confirmation was committed", o.to)
	}
	assert.Equal(t, map[string]int{"sales@example.com": 1, "support@example.com": 1, "ada@example.com": 1}, recipients)
}

// assertNothingHappened checks that a rejected or failed confirmation left the
// database exactly as it was and sent nothing.
func (c *confirmationFixture) assertNothingHappened(t *testing.T, expected dbSnapshot) {
	t.Helper()
	assert.Equal(t, expected, c.snapshot(t))
	assert.Empty(t, c.mail.messages(), "a rejected or failed confirmation announces nothing")
}

// ------------------------------------------------------------------- roots ---

func TestFormConfirmationTokenClaimSQLite(t *testing.T) {
	runFormConfirmationTokenClaimScenarios(t, openFormTestSQLite(t))
}

func TestFormConfirmationTokenClaimMySQL(t *testing.T) {
	runFormConfirmationTokenClaimScenarios(t, openFormTestMySQL(t, 1))
}

func TestFormConfirmationRollbackSQLite(t *testing.T) {
	runFormConfirmationRollbackScenarios(t, openFormTestSQLite(t))
}

func TestFormConfirmationRollbackMySQL(t *testing.T) {
	runFormConfirmationRollbackScenarios(t, openFormTestMySQL(t, 1))
}

func TestFormConfirmationConcurrentSQLite(t *testing.T) {
	runFormConfirmationConcurrentScenarios(t, openFormTestSQLite(t), 1)
}

func TestFormConfirmationConcurrentMySQL(t *testing.T) {
	runFormConfirmationConcurrentScenarios(t, openFormTestMySQL(t, confirmationCompetitors+1), confirmationCompetitors+1)
}

// ------------------------------------------------------------- token claim ---

func runFormConfirmationTokenClaimScenarios(t *testing.T, db *gorm.DB) {
	c := newConfirmationFixture(t, db)

	t.Run("a link works once and a replay is rejected", func(t *testing.T) {
		token := c.pendingLink(t)

		require.NoError(t, c.service.ConfirmSubmission(context.Background(), token))
		c.assertConfirmedAndAnnounced(t)

		c.mail.reset()
		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, confirmedState)
	})

	t.Run("a token spent elsewhere is rejected without touching the submission", func(t *testing.T) {
		token := c.pendingLink(t)
		c.exec(t, "UPDATE form_confirmation_tokens SET used_at = ?", time.Now().Add(-time.Minute))

		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, dbSnapshot{pending: 1})
	})

	t.Run("unknown and empty tokens are rejected", func(t *testing.T) {
		c.pendingLink(t)

		for _, token := range []string{"", "never-issued"} {
			assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		}
		c.assertNothingHappened(t, pendingState)
	})

	t.Run("an expired token is rejected and stays unspent", func(t *testing.T) {
		token := c.pendingLink(t)
		c.exec(t, "UPDATE form_confirmation_tokens SET expires_at = ?", time.Now().Add(-time.Second))

		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, pendingState)
	})

	t.Run("a token that expires between the lookup and the claim is rejected", func(t *testing.T) {
		token := c.pendingLink(t)
		c.hooks.install(func(h *confirmationHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.FormConfirmationToken) error {
				// The lookup has just returned the token as spendable; the clock
				// crosses its expiry before the claim runs. Written through the
				// transaction's own handle: on the one-connection pool a second
				// handle would wait for the connection the transaction holds.
				return tx.Model(&models.FormConfirmationToken{}).Where("id = ?", stored.ID).
					Update("expires_at", time.Now().Add(-time.Second)).Error
			}
		})

		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, pendingState)

		// The rollback discarded the simulated expiry with everything else, so
		// the same link completes once the clock no longer crosses it.
		c.hooks.clear()
		require.NoError(t, c.service.ConfirmSubmission(context.Background(), token))
		c.assertConfirmedAndAnnounced(t)
	})

	t.Run("a token spent between the lookup and the claim is rejected", func(t *testing.T) {
		token := c.pendingLink(t)
		c.hooks.install(func(h *confirmationHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.FormConfirmationToken) error {
				// A competing request stamped the row after this one read it.
				return tx.Model(&models.FormConfirmationToken{}).Where("id = ?", stored.ID).
					Update("used_at", time.Now()).Error
			}
		})

		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, pendingState)
	})

	t.Run("a submission that is no longer pending is rejected without spending the token", func(t *testing.T) {
		for _, status := range []models.FormSubmissionStatus{
			models.FormSubmissionReceived, models.FormSubmissionConfirmed, models.FormSubmissionSpam,
		} {
			t.Run(string(status), func(t *testing.T) {
				token := c.pendingLink(t)
				c.exec(t, "UPDATE form_submissions SET status = ?", status)

				assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)

				expected := dbSnapshot{unspent: 1}
				if status == models.FormSubmissionConfirmed {
					expected.confirmed = 1
				}
				c.assertNothingHappened(t, expected)
			})
		}
	})

	t.Run("a token whose submission is gone is rejected", func(t *testing.T) {
		token := c.pendingLink(t)
		c.exec(t, "DELETE FROM form_submissions")

		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, dbSnapshot{unspent: 1})
	})

	t.Run("a token whose form is gone is rejected", func(t *testing.T) {
		doomed := c.optInForm()
		doomed.Name = "Doomed form"
		c.publish(t, doomed)
		token := c.pendingLinkFor(t, doomed)
		require.NoError(t, c.service.Delete(doomed.ID))

		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, pendingState)
	})
}

// ---------------------------------------------------------------- rollback ---

func runFormConfirmationRollbackScenarios(t *testing.T, db *gorm.DB) {
	c := newConfirmationFixture(t, db)
	injected := errors.New("injected database failure")
	const earlierNote = "Earlier note from a call."

	failLeadCreate := func(h *confirmationHooks) {
		h.beforeLeadCreate = func(*models.Lead) error { return injected }
	}
	failLeadUpdate := func(h *confirmationHooks) {
		h.beforeLeadUpdate = func(*models.Lead) error { return injected }
	}
	failSubmissionUpdate := func(h *confirmationHooks) {
		h.beforeSubmissionUpdate = func(*models.FormSubmission) error { return injected }
	}

	cases := []struct {
		name         string
		existingLead bool
		install      func(h *confirmationHooks)
	}{
		{name: "lead insert fails", install: failLeadCreate},
		{name: "lead update fails", existingLead: true, install: failLeadUpdate},
		{name: "submission update fails after inserting the lead", install: failSubmissionUpdate},
		{name: "submission update fails after linking an existing lead", existingLead: true, install: failSubmissionUpdate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := c.pendingLink(t)
			before := pendingState
			var existing *models.Lead
			if tc.existingLead {
				existing = c.createLead(t, earlierNote)
				before.leads = 1
			}
			c.hooks.install(tc.install)

			err := c.service.ConfirmSubmission(context.Background(), token)
			require.ErrorIs(t, err, injected, "the failure reaches the caller as it is")
			assert.NotErrorIs(t, err, ErrInvalidConfirmationToken)
			c.assertNothingHappened(t, before)
			if existing != nil {
				assert.Equal(t, earlierNote, c.leadNotes(t, existing.ID), "the appended block is rolled back with the rest")
			}

			// The same link, retried once the database behaves, completes the
			// confirmation: the token was handed back by the rollback.
			c.hooks.clear()
			require.NoError(t, c.service.ConfirmSubmission(context.Background(), token))
			c.assertConfirmedAndAnnounced(t)
			if existing != nil {
				notes := c.leadNotes(t, existing.ID)
				assert.Contains(t, notes, earlierNote)
				assert.Contains(t, notes, "--- Form submission: Contact us (")
				stored := c.submissions(t, c.form.ID)
				require.Len(t, stored, 1)
				require.NotNil(t, stored[0].LeadID)
				assert.Equal(t, existing.ID, *stored[0].LeadID, "the retry links the lead that already existed")
			}
		})
	}
}

// -------------------------------------------------------------- concurrent ---

// runFormConfirmationConcurrentScenarios races several requests for one link.
// With several connections every competitor is held after its lookup until
// all of them have read the token as spendable, so the claim alone decides
// the winner. On the one-connection SQLite pool that barrier cannot be used —
// the second request cannot even begin until the first has ended — so the
// competitors start together and the pool serialises them; the outcome is the
// same and just as deterministic: one commit, one set of mail, every other
// request rejected.
func runFormConfirmationConcurrentScenarios(t *testing.T, db *gorm.DB, connections int) {
	c := newConfirmationFixture(t, db)

	t.Run("competing requests on one link: exactly one confirmation commits", func(t *testing.T) {
		token := c.pendingLink(t)

		var barrierFailed atomic.Bool
		if connections > 1 {
			arrived := make(chan struct{}, confirmationCompetitors)
			release := make(chan struct{})
			c.hooks.install(func(h *confirmationHooks) {
				h.afterTokenLookup = func(*gorm.DB, *models.FormConfirmationToken) error {
					arrived <- struct{}{}
					select {
					case <-release:
						return nil
					case <-time.After(confirmationTimeout):
						barrierFailed.Store(true)
						return errors.New("barrier: the other competitors never reached the lookup")
					}
				}
			})
			go func() {
				defer close(release)
				deadline := time.After(confirmationTimeout)
				for i := 0; i < confirmationCompetitors; i++ {
					select {
					case <-arrived:
					case <-deadline:
						barrierFailed.Store(true)
						return
					}
				}
			}()
		}

		start := make(chan struct{})
		results := make(chan error, confirmationCompetitors)
		for i := 0; i < confirmationCompetitors; i++ {
			go func() {
				<-start
				results <- c.service.ConfirmSubmission(context.Background(), token)
			}()
		}
		close(start)

		successes, rejections := 0, 0
		deadline := time.After(confirmationTimeout)
		for i := 0; i < confirmationCompetitors; i++ {
			select {
			case err := <-results:
				switch {
				case err == nil:
					successes++
				case errors.Is(err, ErrInvalidConfirmationToken):
					rejections++
				default:
					t.Errorf("a competitor failed with something other than a rejection: %v", err)
				}
			case <-deadline:
				t.Fatal("the competing confirmations did not finish within the deadline")
			}
		}
		c.hooks.clear()

		assert.False(t, barrierFailed.Load(), "every competitor must pass the lookup before any claim runs")
		assert.Equal(t, 1, successes, "exactly one competitor confirms")
		assert.Equal(t, confirmationCompetitors-1, rejections, "every other competitor is told the link is no longer valid")
		c.assertConfirmedAndAnnounced(t)

		// A later replay of the same link is rejected and announces nothing more.
		c.mail.reset()
		assert.ErrorIs(t, c.service.ConfirmSubmission(context.Background(), token), ErrInvalidConfirmationToken)
		c.assertNothingHappened(t, confirmedState)
	})
}
