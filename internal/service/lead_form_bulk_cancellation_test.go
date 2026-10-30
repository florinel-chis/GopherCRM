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

// cancellationPollInterval spaces the probes that detect the wait. It is
// above InnoDB's 100 ms idle threshold on purpose: INFORMATION_SCHEMA.INNODB_TRX
// is served from a cache that is refreshed only after it has gone unread for
// that long, so a tighter loop would keep reading the snapshot taken before
// the wait began.
const cancellationPollInterval = 150 * time.Millisecond

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
}

// TestRequestCancellationMySQLLockWait covers the wait MySQL deployments
// actually see: a request queued behind another transaction's row lock.
// Cancelling it must abandon the statement long before innodb_lock_wait_timeout
// and leave nothing committed. The probe uses information_schema only, which
// MySQL 8 and MariaDB 10.11 both provide.
func TestRequestCancellationMySQLLockWait(t *testing.T) {
	open := func(t *testing.T) *gorm.DB { return openCancellationMySQL(t, 4) }

	// lockWaiter reports when a transaction of this schema is waiting for a
	// row lock. Other tests may share the server, hence the schema filter.
	lockWaiter := func(t *testing.T, db *gorm.DB) func() bool {
		return func() bool {
			var threads []uint64
			if err := db.Raw("SELECT id FROM information_schema.processlist WHERE db = DATABASE()").Scan(&threads).Error; err != nil || len(threads) == 0 {
				return false
			}
			var n int64
			err := db.Raw("SELECT COUNT(*) FROM information_schema.innodb_trx WHERE trx_state = 'LOCK WAIT' AND trx_mysql_thread_id IN ?", threads).Scan(&n).Error
			return err == nil && n > 0
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
		err := cancelWhileWaiting(t, lockWaiter(t, env.db), release, func(ctx context.Context) error {
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
		err := cancelWhileWaiting(t, lockWaiter(t, env.db), release, func(ctx context.Context) error {
			_, err := env.bulk.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
			return err
		})

		require.ErrorIs(t, err, context.Canceled)
		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		var operations []models.BulkOperation
		require.NoError(t, env.db.Find(&operations).Error)
		for _, op := range operations {
			assert.NotEqual(t, models.StatusCompleted, op.Status, "a cancelled batch is never recorded as completed")
		}
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
		err = cancelWhileWaiting(t, lockWaiter(t, env.db), release, func(ctx context.Context) error {
			return f.service.ConfirmSubmission(ctx, token)
		})

		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, f.leads(t))
		require.NoError(t, f.service.ConfirmSubmission(context.Background(), token), "the link stays usable")
		assert.Len(t, f.leads(t), 1)
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
