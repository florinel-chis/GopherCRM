package service

// Regression roots for issue #107: the request context reaches lead
// conversion, the public form submission and confirmation, the reCAPTCHA call
// and the routed bulk status updates. A request that is cancelled while it
// waits — for SQLite's single pooled connection, for a MySQL row lock or for
// the captcha service — returns promptly with the cancellation and leaves
// nothing behind. Every assertion reads committed rows.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// cancellationPromptBound is how long a cancelled call may take to return.
// Every wait these scenarios provoke is otherwise unbounded (the SQLite pool)
// or far longer (MySQL's 50 s lock wait timeout, the 5 s captcha timeout).
const cancellationPromptBound = 3 * time.Second

// cancellationSetupBound bounds the polling that waits for a call to start
// waiting.
const cancellationSetupBound = 10 * time.Second

// cancellationPollInterval spaces the probes that detect the wait. The MySQL
// lock-wait probe requires the same statement to be executing on two
// consecutive polls, so the spacing is what tells a waiting statement from
// one that merely happened to be running.
const cancellationPollInterval = 100 * time.Millisecond

var cancellationDBSeq atomic.Uint64

// migrateCancellationSchema creates every table the three call chains touch.
func migrateCancellationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		&models.Configuration{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{},
		&models.BulkOperation{},
	))
}

// openCancellationSQLite is a file-backed database with the production
// pragmas and one pooled connection. A file is deliberate: database/sql
// discards a connection whose transaction ended by cancellation, and an
// in-memory database would vanish with it.
func openCancellationSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("cancellation_%d.db", cancellationDBSeq.Add(1)))
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"),
		quoteTestGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateCancellationSchema(t, db)
	return db
}

func openCancellationMySQL(t *testing.T, maxConns int) *gorm.DB {
	t.Helper()
	db := openQuoteTestMySQL(t, maxConns)
	migrateCancellationSchema(t, db)
	return db
}

func TestRequestCancellationSQLite(t *testing.T) {
	runRequestCancellationScenarios(t, openCancellationSQLite)
}

// TestRequestCancellationMySQL runs the same scenarios with the MySQL pool
// pinned to one connection, which is what makes a waiting call observable.
func TestRequestCancellationMySQL(t *testing.T) {
	runRequestCancellationScenarios(t, func(t *testing.T) *gorm.DB { return openCancellationMySQL(t, 1) })
}

// cancellationEnv wires the three services on one database.
type cancellationEnv struct {
	db    *gorm.DB
	leads *leadConversionEnv
	forms *formFixture
	bulk  BulkStatusService
	admin models.User
}

func newCancellationEnv(t *testing.T, db *gorm.DB) *cancellationEnv {
	t.Helper()
	quiet := logrus.New()
	quiet.SetLevel(logrus.PanicLevel)

	env := &cancellationEnv{db: db}
	env.forms = newFormFixtureOn(t, config.FormsConfig{PublicBaseURL: formTestPublicBase}, db)
	env.leads = newLeadConversionEnv(t, db)
	env.admin = models.User{Email: "cancellation-admin@example.com", Password: "x", FirstName: "Ada", LastName: "Admin",
		Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(&env.admin).Error)
	env.bulk = NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		utils.NewTransactionManager(db), quiet)
	return env
}

// count returns the number of live rows of model.
func count(t *testing.T, db *gorm.DB, model interface{}) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(model).Count(&n).Error)
	return n
}

// holdOnlyConnection begins a transaction on the pool's single connection and
// returns its release. Until then every other statement on db must wait.
func holdOnlyConnection(t *testing.T, db *gorm.DB) (release func()) {
	t.Helper()
	holder := db.Begin()
	require.NoError(t, holder.Error)
	// Make sure the connection is really checked out before anyone waits.
	require.NoError(t, holder.Exec("SELECT 1").Error)
	released := false
	release = func() {
		if !released {
			released = true
			_ = holder.Rollback().Error
		}
	}
	t.Cleanup(release)
	return release
}

// cancelWhileWaiting runs call on a cancellable context, waits until waiting
// reports that the call is blocked, cancels, and requires the call to return
// within cancellationPromptBound. The returned error is the call's.
//
// release is always invoked before the function returns, so a call that
// ignores its context (the defect) is unblocked and finishes inside the test
// instead of leaking into the next one.
func cancelWhileWaiting(t *testing.T, waiting func() bool, release func(), call func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- call(ctx) }()

	var err error
	func() {
		defer release()
		if !assert.Eventually(t, waiting, cancellationSetupBound, cancellationPollInterval, "the call never started waiting") {
			cancel()
			release()
			<-done
			t.FailNow()
		}
		cancel()
		select {
		case err = <-done:
		case <-time.After(cancellationPromptBound):
			release()
			<-done
			t.Fatalf("the call did not return within %s of its request being cancelled", cancellationPromptBound)
		}
	}()
	return err
}

// poolWaiter reports when a statement has started waiting for one of db's
// pooled connections.
func poolWaiter(t *testing.T, db *gorm.DB) func() bool {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	before := sqlDB.Stats().WaitCount
	return func() bool { return sqlDB.Stats().WaitCount > before }
}

func runRequestCancellationScenarios(t *testing.T, open func(t *testing.T) *gorm.DB) {
	t.Run("conversion waiting for the connection returns on cancellation and writes nothing", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusQualified, nil)

		release := holdOnlyConnection(t, env.db)
		err := cancelWhileWaiting(t, poolWaiter(t, env.db), release, func(ctx context.Context) error {
			_, err := env.leads.svc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, count(t, env.db, &models.Customer{}), "no customer may be created")
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusQualified, stored.Status)
		assert.Nil(t, stored.CustomerID)

		// The pool survived: the same conversion succeeds on a live request.
		customer, err := env.leads.svc.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
		require.NoError(t, err)
		assert.NotZero(t, customer.ID)
	})

	t.Run("conversion on a cancelled request writes nothing", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusQualified, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := env.leads.svc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})

		require.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, count(t, env.db, &models.Customer{}))
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusQualified, stored.Status)
	})

	t.Run("bulk status waiting for the connection returns on cancellation and writes nothing", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusNew, nil)

		release := holdOnlyConnection(t, env.db)
		err := cancelWhileWaiting(t, poolWaiter(t, env.db), release, func(ctx context.Context) error {
			_, err := env.bulk.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		assert.Zero(t, count(t, env.db, &models.BulkOperation{}), "not even the history row is written")
	})

	t.Run("bulk status on a cancelled request writes nothing", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusNew, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := env.bulk.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)

		require.ErrorIs(t, err, context.Canceled)
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		assert.Zero(t, count(t, env.db, &models.BulkOperation{}))
	})

	t.Run("submission waiting for the connection returns on cancellation and writes nothing", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		f := env.forms
		form := f.publish(t, f.newForm())

		release := holdOnlyConnection(t, env.db)
		err := cancelWhileWaiting(t, poolWaiter(t, env.db), release, func(ctx context.Context) error {
			_, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.submissions(t, form.ID))
		assert.Empty(t, f.leads(t))
		assert.Empty(t, f.mailer.messages(), "no mail for a submission that was never stored")
	})

	t.Run("submission on a cancelled request writes nothing", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		f := env.forms
		form := f.publish(t, f.newForm())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.submissions(t, form.ID))
		assert.Empty(t, f.leads(t))
		assert.Empty(t, f.mailer.messages())
	})

	t.Run("confirmation waiting for the connection returns on cancellation and keeps the link usable", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		f := env.forms
		form := f.publish(t, f.optInForm())
		_, err := f.service.SubmitPublic(context.Background(), form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
		messages := f.mailer.messages()
		require.Len(t, messages, 1)
		token := tokenFromLink(t, messages[0].Body)

		release := holdOnlyConnection(t, env.db)
		err = cancelWhileWaiting(t, poolWaiter(t, env.db), release, func(ctx context.Context) error {
			return f.service.ConfirmSubmission(ctx, token)
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.leads(t), "no lead for a confirmation that never ran")
		stored := f.submissions(t, form.ID)
		require.Len(t, stored, 1)
		assert.Equal(t, models.FormSubmissionPending, stored[0].Status)
		assert.Len(t, f.mailer.messages(), 1, "no notification or follow-up went out")

		// The token was not spent: the visitor's next click confirms.
		require.NoError(t, f.service.ConfirmSubmission(context.Background(), token))
		assert.Len(t, f.leads(t), 1)
	})

	// The submission cases below let the published-form read through and take
	// the connection only afterwards, so the call is cancelled while its
	// transaction or its spam write waits, not while the first read does.
	submitCases := []struct {
		name    string
		optIn   bool
		prepare func(req *PublicSubmissionRequest)
	}{
		{name: "a received submission waiting for its transaction"},
		{name: "a pending submission waiting for its transaction", optIn: true},
		{name: "a spam submission waiting for its spam write", prepare: func(req *PublicSubmissionRequest) {
			req.Honeypot = "http://spam.example"
		}},
	}
	for _, tc := range submitCases {
		t.Run(tc.name+" returns on cancellation and writes nothing", func(t *testing.T) {
			env := newCancellationEnv(t, open(t))
			f := env.forms
			form := f.newForm()
			if tc.optIn {
				form = f.optInForm()
			}
			f.publish(t, form)
			holder := &lateHolder{db: env.db}
			f.wire(hookedFormRepo{FormRepository: f.repo, afterRead: holder.take}, f.leadRepo, f.mailer)
			req := validSubmission()
			if tc.prepare != nil {
				tc.prepare(req)
			}

			pooled := poolWaiter(t, env.db)
			err := cancelWhileWaiting(t, func() bool { return holder.held() && pooled() }, holder.release,
				func(ctx context.Context) error {
					_, err := f.service.SubmitPublic(ctx, form.PublicID, req, submissionMeta())
					return err
				})

			require.ErrorIs(t, err, context.Canceled)
			assert.Empty(t, f.submissions(t, form.ID))
			assert.Empty(t, f.leads(t))
			assert.Zero(t, count(t, env.db, &models.FormConfirmationToken{}))
			assert.Empty(t, f.mailer.messages())
		})
	}

	t.Run("a batch cancelled after its history row is written is recorded failed", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusNew, nil)
		svc, holder := env.hookedBulk()
		t.Cleanup(holder.release)
		sqlDB, err := env.db.DB()
		require.NoError(t, err)
		base := sqlDB.Stats().WaitCount

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := svc.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
			done <- err
		}()
		finish := func() { holder.release(); <-done }

		if !assert.Eventually(t, func() bool { return holder.held() && sqlDB.Stats().WaitCount > base },
			cancellationSetupBound, cancellationPollInterval, "the batch never waited for its transaction") {
			cancel()
			finish()
			t.FailNow()
		}
		cancel()
		// The outcome write now queues for the same connection; once the
		// connection is free it records the failure.
		if !assert.Eventually(t, func() bool { return sqlDB.Stats().WaitCount > base+1 },
			cancellationSetupBound, cancellationPollInterval, "the outcome write never queued") {
			finish()
			t.FailNow()
		}
		holder.release()
		select {
		case err = <-done:
		case <-time.After(cancellationPromptBound):
			<-done
			t.Fatalf("the batch did not return within %s once the connection was free", cancellationPromptBound)
		}

		require.ErrorIs(t, err, context.Canceled)
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		var operations []models.BulkOperation
		require.NoError(t, env.db.Find(&operations).Error)
		require.Len(t, operations, 1)
		assert.Equal(t, models.StatusFailed, operations[0].Status)
	})

	t.Run("an outcome write that cannot get a connection gives up and leaves the row pending", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusNew, nil)
		previous := bulkOutcomeTimeout
		bulkOutcomeTimeout = 200 * time.Millisecond
		t.Cleanup(func() { bulkOutcomeTimeout = previous })
		svc, holder := env.hookedBulk()
		pooled := poolWaiter(t, env.db)

		err := cancelWhileWaiting(t, func() bool { return holder.held() && pooled() }, holder.release,
			func(ctx context.Context) error {
				_, err := svc.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
				return err
			})

		require.ErrorIs(t, err, context.Canceled)
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		var operations []models.BulkOperation
		require.NoError(t, env.db.Find(&operations).Error)
		require.Len(t, operations, 1)
		assert.Equal(t, models.StatusPending, operations[0].Status,
			"a timed-out outcome write leaves the documented pending row")
	})
}

// lateHolder takes the pool's only connection from inside the call under test,
// at the point a hook fires, and gives it back on release.
type lateHolder struct {
	db *gorm.DB
	mu sync.Mutex
	tx *gorm.DB
}

func (h *lateHolder) take() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tx != nil {
		return
	}
	tx := h.db.Begin()
	if tx.Error == nil {
		_ = tx.Exec("SELECT 1").Error
	}
	h.tx = tx
}

func (h *lateHolder) held() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tx != nil
}

func (h *lateHolder) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tx != nil {
		_ = h.tx.Rollback().Error
	}
}

// hookedFormRepo runs afterRead once a published-form lookup has returned.
type hookedFormRepo struct {
	repository.FormRepository
	afterRead func()
}

func (r hookedFormRepo) WithTx(tx *gorm.DB) repository.FormRepository {
	return hookedFormRepo{FormRepository: r.FormRepository.WithTx(tx), afterRead: r.afterRead}
}

func (r hookedFormRepo) GetByPublicID(publicID string) (*models.Form, error) {
	form, err := r.FormRepository.GetByPublicID(publicID)
	if err == nil && r.afterRead != nil {
		r.afterRead()
	}
	return form, err
}

// hookedOperationRepo runs afterCreate once a history row has been written.
type hookedOperationRepo struct {
	repository.BulkOperationRepository
	afterCreate func()
}

func (r hookedOperationRepo) Create(ctx context.Context, operation *models.BulkOperation) error {
	err := r.BulkOperationRepository.Create(ctx, operation)
	if err == nil && r.afterCreate != nil {
		r.afterCreate()
	}
	return err
}

// hookedBulk is a bulk service that takes the only connection right after it
// has written the history row, before its transaction begins.
func (e *cancellationEnv) hookedBulk() (BulkStatusService, *lateHolder) {
	quiet := logrus.New()
	quiet.SetLevel(logrus.PanicLevel)
	holder := &lateHolder{db: e.db}
	svc := NewBulkStatusService(
		hookedOperationRepo{BulkOperationRepository: repository.NewBulkOperationRepository(e.db), afterCreate: holder.take},
		repository.NewBulkStatusRepository(e.db), utils.NewTransactionManager(e.db), quiet)
	return svc, holder
}

// TestRequestRootRequired pins that the request-scoped entry points refuse to
// run inside an enclosing transaction: they send mail or record their outcome
// after their own commit, which an enclosing rollback could not undo. No
// production caller nests them; the refusal keeps it that way.
func TestRequestRootRequiredSQLite(t *testing.T) {
	runRequestRootRequired(t, openCancellationSQLite(t))
}

func TestRequestRootRequiredMySQL(t *testing.T) {
	runRequestRootRequired(t, openCancellationMySQL(t, 4))
}

func runRequestRootRequired(t *testing.T, db *gorm.DB) {
	env := newCancellationEnv(t, db)
	f := env.forms
	tm := utils.NewTransactionManager(db)
	lead := env.leads.bind(t).newLead(models.LeadStatusQualified, nil)
	form := f.publish(t, f.newForm())
	optIn := f.publish(t, f.optInForm())
	_, err := f.service.SubmitPublic(context.Background(), optIn.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	require.Len(t, f.mailer.messages(), 1)
	token := tokenFromLink(t, f.mailer.messages()[0].Body)

	nested := func(t *testing.T, call func(ctx context.Context) error) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), cancellationPromptBound)
		defer cancel()
		var inner error
		require.NoError(t, tm.WithTransaction(ctx, func(ctx context.Context) error {
			inner = call(ctx)
			return nil
		}))
		return inner
	}

	t.Run("conversion", func(t *testing.T) {
		err := nested(t, func(ctx context.Context) error {
			_, err := env.leads.svc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
			return err
		})
		require.ErrorIs(t, err, utils.ErrTransactionRootRequired)
		assert.Zero(t, count(t, db, &models.Customer{}))
	})

	t.Run("bulk status", func(t *testing.T) {
		err := nested(t, func(ctx context.Context) error {
			_, err := env.bulk.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
			return err
		})
		require.ErrorIs(t, err, utils.ErrTransactionRootRequired)
		assert.Zero(t, count(t, db, &models.BulkOperation{}))
		var stored models.Lead
		require.NoError(t, db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusQualified, stored.Status)
	})

	t.Run("submission", func(t *testing.T) {
		err := nested(t, func(ctx context.Context) error {
			_, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
			return err
		})
		require.ErrorIs(t, err, utils.ErrTransactionRootRequired)
		assert.Empty(t, f.submissions(t, form.ID))
		assert.Len(t, f.mailer.messages(), 1, "no mail")
	})

	t.Run("confirmation", func(t *testing.T) {
		err := nested(t, func(ctx context.Context) error {
			return f.service.ConfirmSubmission(ctx, token)
		})
		require.ErrorIs(t, err, utils.ErrTransactionRootRequired)
		assert.Len(t, f.mailer.messages(), 1, "no mail")
		require.NoError(t, f.service.ConfirmSubmission(context.Background(), token), "the link stays usable")
	})

	assert.Len(t, f.leads(t), 2, "the seeded lead and the one the unnested confirmation created")
}

// The statements each request-bound call issues against the row the MySQL
// lock-wait test locks. MySQL 8 shows a prepared statement's INFO with its
// parameters filled in; MariaDB 10.11 can keep the `?` placeholders, so a
// pattern never depends on a parameter's value.
var (
	leadLockingRead = regexp.MustCompile("^SELECT \\* FROM `leads` WHERE `leads`\\.`id` = (\\d+|\\?) .*FOR UPDATE$")
	leadStatusWrite = regexp.MustCompile("^UPDATE `leads` SET `status`=")
	leadNotesWrite  = regexp.MustCompile("^UPDATE `leads` SET .*`notes`=")
	tokenClaim      = regexp.MustCompile("^UPDATE `form_confirmation_tokens` SET `used_at`=")
)

// TestRequestCancellationMySQLLockWait covers the wait MySQL deployments
// actually see: a request queued behind another transaction's row lock.
// Cancelling it must abandon the statement long before innodb_lock_wait_timeout
// and leave nothing committed. The probe reads information_schema.PROCESSLIST,
// which MySQL 8 and MariaDB 10.11 both provide.
func TestRequestCancellationMySQLLockWait(t *testing.T) {
	open := func(t *testing.T) *gorm.DB { return openCancellationMySQL(t, 4) }

	// lockWaiter reports when the call is blocked on the held row lock: the
	// statement matching target (the one that touches the locked row) has been
	// seen executing on the same connection on two consecutive polls. See
	// lockWaitProbe for why that identifies a lock wait.
	lockWaiter := func(t *testing.T, db *gorm.DB, target *regexp.Regexp) func() bool {
		probe := &lockWaitProbe{target: target}
		return func() bool {
			var rows []executingStatement
			err := db.Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
				WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
				AND INFO IS NOT NULL`).Scan(&rows).Error
			if err != nil {
				rows = nil
			}
			return probe.observe(rows)
		}
	}
	// lockRow takes an exclusive lock on one row in a separate transaction.
	lockRow := func(t *testing.T, db *gorm.DB, model interface{}, id uint) (release func()) {
		holder := db.Begin()
		require.NoError(t, holder.Error)
		require.NoError(t, holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(model, id).Error)
		released := false
		release = func() {
			if !released {
				released = true
				_ = holder.Rollback().Error
			}
		}
		t.Cleanup(release)
		return release
	}

	t.Run("conversion queued behind the lead's row lock", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusQualified, nil)

		release := lockRow(t, env.db, &models.Lead{}, lead.ID)
		err := cancelWhileWaiting(t, lockWaiter(t, env.db, leadLockingRead), release, func(ctx context.Context) error {
			_, err := env.leads.svc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, count(t, env.db, &models.Customer{}))
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusQualified, stored.Status)
		assert.Nil(t, stored.CustomerID)
	})

	t.Run("bulk status queued behind a lead's row lock", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusNew, nil)

		release := lockRow(t, env.db, &models.Lead{}, lead.ID)
		// The history row is written before the transaction opens, so it
		// exists once the status write is seen waiting.
		err := cancelWhileWaiting(t, lockWaiter(t, env.db, leadStatusWrite), release, func(ctx context.Context) error {
			_, err := env.bulk.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		var operations []models.BulkOperation
		require.NoError(t, env.db.Find(&operations).Error)
		require.Len(t, operations, 1, "exactly one history row for the batch")
		assert.Equal(t, models.StatusFailed, operations[0].Status)
	})

	t.Run("submission queued behind the existing lead's row lock", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		f := env.forms
		form := f.publish(t, f.newForm())
		// The visitor is already a lead, so the submission appends to that
		// lead: the published-form read and the submission insert pass, and
		// the lead update waits for the row lock.
		existing := models.Lead{FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com",
			Status: models.LeadStatusNew, OwnerID: f.owner.ID, Notes: "Met at the fair."}
		require.NoError(t, env.db.Create(&existing).Error)

		release := lockRow(t, env.db, &models.Lead{}, existing.ID)
		err := cancelWhileWaiting(t, lockWaiter(t, env.db, leadNotesWrite), release, func(ctx context.Context) error {
			_, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.submissions(t, form.ID), "the submission insert rolled back with the transaction")
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, existing.ID).Error)
		assert.Equal(t, "Met at the fair.", stored.Notes)
		assert.Len(t, f.leads(t), 1)
		assert.Empty(t, f.mailer.messages())
	})

	t.Run("confirmation queued behind the token's row lock", func(t *testing.T) {
		env := newCancellationEnv(t, open(t))
		f := env.forms
		form := f.publish(t, f.optInForm())
		_, err := f.service.SubmitPublic(context.Background(), form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
		token := tokenFromLink(t, f.mailer.messages()[0].Body)
		var stored models.FormConfirmationToken
		require.NoError(t, env.db.First(&stored).Error)

		release := lockRow(t, env.db, &models.FormConfirmationToken{}, stored.ID)
		err = cancelWhileWaiting(t, lockWaiter(t, env.db, tokenClaim), release, func(ctx context.Context) error {
			return f.service.ConfirmSubmission(ctx, token)
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.leads(t))
		require.NoError(t, f.service.ConfirmSubmission(context.Background(), token), "the link stays usable")
		assert.Len(t, f.leads(t), 1)
	})
}

// executingStatement is one row of information_schema.PROCESSLIST.
type executingStatement struct {
	ID   int64
	Info string
}

// lockWaitProbe decides, from successive PROCESSLIST snapshots, whether the
// call under test is blocked on a row lock. Only a statement matching target
// counts, and it must be the same statement on the same connection on two
// consecutive polls: an uncontended statement finishes in microseconds, so one
// still executing a poll interval later is waiting for the lock. Requiring the
// target statement keeps the call's earlier statements (published-form read,
// history insert, submission insert) from passing for the wait even on a slow
// server, so cancellation always lands inside the transaction at the intended
// write. The lock holder is idle between statements and never matches.
//
// INNODB_TRX is not usable as the signal: MySQL 8 reports a lock wait taken
// during the optimizer's const-table read of a primary key as RUNNING in state
// "statistics", and it serves that view from a cache. Other tests may share
// the server, hence the schema filter in the query.
type lockWaitProbe struct {
	target   *regexp.Regexp
	lastID   int64
	lastInfo string
	seen     int
}

func (p *lockWaitProbe) observe(rows []executingStatement) bool {
	var match *executingStatement
	for i := range rows {
		if !p.target.MatchString(rows[i].Info) {
			continue
		}
		if match == nil || (rows[i].ID == p.lastID && rows[i].Info == p.lastInfo) {
			match = &rows[i]
		}
	}
	if match == nil {
		p.seen, p.lastID, p.lastInfo = 0, 0, ""
		return false
	}
	if p.seen > 0 && match.ID == p.lastID && match.Info == p.lastInfo {
		p.seen++
	} else {
		p.seen, p.lastID, p.lastInfo = 1, match.ID, match.Info
	}
	return p.seen >= 2
}

// TestLockWaitProbe pins the probe's rule without a server: only the target
// statement, seen twice in a row on one connection, reports a wait.
func TestLockWaitProbe(t *testing.T) {
	target := regexp.MustCompile("^UPDATE `leads` SET `status`=")
	write := executingStatement{ID: 7, Info: "UPDATE `leads` SET `status`='contacted' WHERE id IN (1)"}
	insert := executingStatement{ID: 7, Info: "INSERT INTO `bulk_operations` (`status`) VALUES ('pending')"}
	other := executingStatement{ID: 8, Info: "SELECT * FROM `forms` WHERE `public_id` = 'x'"}

	t.Run("a non-target statement never counts, however long it runs", func(t *testing.T) {
		p := &lockWaitProbe{target: target}
		for i := 0; i < 5; i++ {
			assert.False(t, p.observe([]executingStatement{insert, other}))
		}
	})
	t.Run("the target statement on two consecutive polls counts", func(t *testing.T) {
		p := &lockWaitProbe{target: target}
		assert.False(t, p.observe([]executingStatement{other, write}))
		assert.True(t, p.observe([]executingStatement{write, other}))
	})
	t.Run("a different statement in between restarts the count", func(t *testing.T) {
		p := &lockWaitProbe{target: target}
		assert.False(t, p.observe([]executingStatement{write}))
		assert.False(t, p.observe([]executingStatement{insert}))
		assert.False(t, p.observe([]executingStatement{write}))
		assert.True(t, p.observe([]executingStatement{write}))
	})
	t.Run("the same text on another connection or a re-issued statement does not count", func(t *testing.T) {
		p := &lockWaitProbe{target: target}
		assert.False(t, p.observe([]executingStatement{write}))
		assert.False(t, p.observe([]executingStatement{{ID: 9, Info: write.Info}}))
		assert.False(t, p.observe([]executingStatement{{ID: 9, Info: write.Info + " "}}))
	})
	t.Run("the conversion pattern matches filled-in and placeholder parameters", func(t *testing.T) {
		assert.True(t, leadLockingRead.MatchString("SELECT * FROM `leads` WHERE `leads`.`id` = 42 AND `leads`.`deleted_at` IS NULL ORDER BY `leads`.`id` LIMIT 1 FOR UPDATE"), "MySQL 8")
		assert.True(t, leadLockingRead.MatchString("SELECT * FROM `leads` WHERE `leads`.`id` = ? AND `leads`.`deleted_at` IS NULL ORDER BY `leads`.`id` LIMIT ? FOR UPDATE"), "MariaDB 10.11")
		assert.False(t, leadLockingRead.MatchString("SELECT * FROM `leads` WHERE `leads`.`id` = ? AND `leads`.`deleted_at` IS NULL"), "not a locking read")
	})
}

// TestRequestCancellationCaptchaVerifier blocks the verification service and
// cancels the submission while it waits. The call must return the
// cancellation at once — not after the verifier's timeout — and must not file
// the submission as captcha spam, because no verdict was ever reached.
func TestRequestCancellationCaptchaVerifier(t *testing.T) {
	run := func(t *testing.T, db *gorm.DB) {
		f := newFormFixtureOn(t, config.FormsConfig{
			PublicBaseURL:     formTestPublicBase,
			RecaptchaSiteKey:  "site-key",
			RecaptchaSecret:   "secret-key",
			RecaptchaMinScore: 0.5,
		}, db)

		var entered atomic.Int32
		unblock := make(chan struct{})
		stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			entered.Add(1)
			select {
			case <-r.Context().Done():
			case <-unblock:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"score":0.9}`))
			}
		}))
		closeOnce := false
		releaseServer := func() {
			if !closeOnce {
				closeOnce = true
				close(unblock)
			}
		}
		t.Cleanup(stalled.Close)
		t.Cleanup(releaseServer)

		svc, ok := f.service.(*formService)
		require.True(t, ok)
		// No client timeout of its own: only the request context can end the
		// wait, which is exactly what the scenario isolates.
		svc.verifier = forms.NewRecaptchaVerifier("secret-key", 0.5,
			forms.WithRecaptchaEndpoint(stalled.URL), forms.WithRecaptchaHTTPClient(&http.Client{}))

		form := f.newForm()
		form.CaptchaEnabled = true
		f.publish(t, form)
		req := validSubmission()
		req.CaptchaToken = "client-token"

		err := cancelWhileWaiting(t, func() bool { return entered.Load() > 0 }, releaseServer, func(ctx context.Context) error {
			_, err := f.service.SubmitPublic(ctx, form.PublicID, req, submissionMeta())
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.submissions(t, form.ID), "an abandoned check is not a spam verdict")
		assert.Empty(t, f.leads(t))
		assert.Empty(t, f.mailer.messages())
	}

	t.Run("SQLite", func(t *testing.T) { run(t, openCancellationSQLite(t)) })
	t.Run("MySQL", func(t *testing.T) { run(t, openCancellationMySQL(t, 2)) })
}
