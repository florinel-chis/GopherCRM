package service

// Regression roots for #196: every bulk_operations row a bulk status batch
// writes reaches completed or failed without operator action. completed is
// written inside the batch transaction; a row left pending (its batch never
// committed and its failed write timed out) is failed by the stale-pending
// sweep. On SQLite (one pooled connection) and on a disposable MySQL schema
// when TEST_MYSQL_DSN is set. Every assertion reads committed rows.

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBulkOutcomeRecoverySQLite(t *testing.T) {
	runBulkOutcomeRecovery(t, bulkRecoveryEngine{
		automation:   func(t *testing.T) *gorm.DB { return openAutomationSQLite(t) },
		cancellation: openCancellationSQLite,
		concurrent:   false,
	})
}

func TestBulkOutcomeRecoveryMySQL(t *testing.T) {
	runBulkOutcomeRecovery(t, bulkRecoveryEngine{
		automation:   func(t *testing.T) *gorm.DB { return openAutomationMySQL(t, 4) },
		cancellation: func(t *testing.T) *gorm.DB { return openCancellationMySQL(t, 1) },
		concurrent:   true,
	})
}

// bulkRecoveryEngine opens the databases one engine's scenarios need.
// concurrent says whether a second connection can commit while a batch
// transaction is open: on MySQL a sweep can land between a batch's status
// writes and its completion; SQLite's single connection serialises them, so
// there the sweep lands between the history row and the transaction.
type bulkRecoveryEngine struct {
	automation   func(t *testing.T) *gorm.DB
	cancellation func(t *testing.T) *gorm.DB
	concurrent   bool
}

// lockedBuffer collects log output written from any goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// capturingLogger writes JSON at warn level and above into a buffer.
func capturingLogger() (*logrus.Logger, *lockedBuffer) {
	out := &lockedBuffer{}
	logger := logrus.New()
	logger.SetOutput(out)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.WarnLevel)
	return logger, out
}

// completeFaultOperationRepo fails CompletePending on the transaction-bound
// repository, after the batch's status writes and events.
type completeFaultOperationRepo struct {
	repository.BulkOperationRepository
	bound bool
}

var errCompleteFault = errors.New("injected completion fault")

func (r completeFaultOperationRepo) WithTx(tx *gorm.DB) repository.BulkOperationRepository {
	return completeFaultOperationRepo{BulkOperationRepository: r.BulkOperationRepository.WithTx(tx), bound: true}
}

func (r completeFaultOperationRepo) CompletePending(ctx context.Context, id uint) error {
	if r.bound {
		return errCompleteFault
	}
	return r.BulkOperationRepository.CompletePending(ctx, id)
}

// sweepingStatusRepo runs sweep right after the batch's status write, inside
// the batch transaction, before the completion.
type sweepingStatusRepo struct {
	repository.BulkStatusRepository
	sweep func()
}

func (r sweepingStatusRepo) WithTx(tx *gorm.DB) repository.BulkStatusRepository {
	return sweepingStatusRepo{BulkStatusRepository: r.BulkStatusRepository.WithTx(tx), sweep: r.sweep}
}

func (r sweepingStatusRepo) SetLeadStatus(ids []uint, status models.LeadStatus) error {
	if err := r.BulkStatusRepository.SetLeadStatus(ids, status); err != nil {
		return err
	}
	r.sweep()
	return nil
}

// sweepAfterCreateRepo runs sweep once the history row is written.
type sweepAfterCreateRepo struct {
	repository.BulkOperationRepository
	sweep func()
}

func (r sweepAfterCreateRepo) Create(ctx context.Context, operation *models.BulkOperation) error {
	if err := r.BulkOperationRepository.Create(ctx, operation); err != nil {
		return err
	}
	r.sweep()
	return nil
}

func runBulkOutcomeRecovery(t *testing.T, engine bulkRecoveryEngine) {
	operations := func(t *testing.T, db *gorm.DB) []models.BulkOperation {
		t.Helper()
		var rows []models.BulkOperation
		require.NoError(t, db.Unscoped().Order("id ASC").Find(&rows).Error)
		return rows
	}
	pastHorizon := func() time.Time { return time.Now().Add(BulkOperationStaleAfter + time.Minute) }

	t.Run("a successful batch completes its row inside the transaction; a failed completion rolls the batch back", func(t *testing.T) {
		env := newAutomationEnv(t, engine.automation(t))
		logger, logs := capturingLogger()
		lead := env.createLead(env.sales, models.LeadStatusNew)

		ok := NewBulkStatusService(repository.NewBulkOperationRepository(env.db), repository.NewBulkStatusRepository(env.db),
			env.txManager, logger, WithBulkAutomationEvents(env.repo))
		result, err := ok.BulkSetLeadStatus(context.Background(), env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Updated)
		rows := operations(t, env.db)
		require.Len(t, rows, 1)
		assert.Equal(t, models.StatusCompleted, rows[0].Status)
		eventsBefore := len(env.events())
		require.Equal(t, 2, eventsBefore, "lead_updated and lead_status_changed")

		faulty := NewBulkStatusService(completeFaultOperationRepo{BulkOperationRepository: repository.NewBulkOperationRepository(env.db)},
			repository.NewBulkStatusRepository(env.db), env.txManager, logger, WithBulkAutomationEvents(env.repo))
		_, err = faulty.BulkSetLeadStatus(context.Background(), env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusQualified)
		require.ErrorIs(t, err, errCompleteFault, "the caller gets the completion's error")

		assert.Equal(t, models.LeadStatusContacted, env.lead(lead.ID).Status, "no record status is committed")
		assert.Len(t, env.events(), eventsBefore, "no automation event is committed")
		rows = operations(t, env.db)
		require.Len(t, rows, 2)
		assert.Equal(t, models.StatusCompleted, rows[0].Status)
		assert.Equal(t, models.StatusFailed, rows[1].Status, "the row of the rolled-back batch ends failed")
		assert.Empty(t, logs.String(), "nothing worth a warning happened")
	})

	t.Run("a rolled-back batch whose failed write times out is pending, then failed by one sweep", func(t *testing.T) {
		env := newCancellationEnv(t, engine.cancellation(t))
		lead := env.leads.bind(t).newLead(models.LeadStatusNew, nil)
		previous := bulkOutcomeTimeout
		bulkOutcomeTimeout = 200 * time.Millisecond
		t.Cleanup(func() { bulkOutcomeTimeout = previous })
		svc, holder := env.hookedBulk()
		pooled := poolWaiter(t, env.db)

		// The batch takes the history row, then waits for the pool's only
		// connection, which the holder keeps; it is cancelled, rolls back, and
		// its bounded failed write times out behind the same holder.
		err := cancelWhileWaiting(t, func() bool { return holder.held() && pooled() }, holder.release,
			func(ctx context.Context) error {
				_, err := svc.BulkSetLeadStatus(ctx, env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusContacted)
				return err
			})
		require.ErrorIs(t, err, context.Canceled)

		var stored models.Lead
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status, "the records are unchanged")
		rows := operations(t, env.db)
		require.Len(t, rows, 1)
		require.Equal(t, models.StatusPending, rows[0].Status, "the timed-out outcome write leaves the row pending at first")

		repo := repository.NewBulkOperationRepository(env.db)
		quiet, _ := capturingLogger()
		// The horizon has not passed: a sweep on the real clock leaves it.
		live := NewBulkOperationRecoveryRuntime(repo, quiet)
		swept, err := live.Sweep(context.Background())
		require.NoError(t, err)
		assert.Zero(t, swept)
		assert.Equal(t, models.StatusPending, operations(t, env.db)[0].Status, "a row younger than the horizon is not swept")

		// One sweep with the clock past BulkOperationStaleAfter moves it to
		// failed, without any request.
		later := NewBulkOperationRecoveryRuntime(repo, quiet, WithBulkRecoveryClock(pastHorizon))
		swept, err = later.Sweep(context.Background())
		require.NoError(t, err)
		assert.EqualValues(t, 1, swept)
		assert.Equal(t, models.StatusFailed, operations(t, env.db)[0].Status)
		require.NoError(t, env.db.First(&stored, lead.ID).Error)
		assert.Equal(t, models.LeadStatusNew, stored.Status, "failed means none of the batch's records changed")
	})

	t.Run("a batch whose row was swept before it completed answers ErrBulkOperationNotPending and changes nothing", func(t *testing.T) {
		env := newAutomationEnv(t, engine.automation(t))
		logger, logs := capturingLogger()
		lead := env.createLead(env.sales, models.LeadStatusNew)
		sweeper := NewBulkOperationRecoveryRuntime(repository.NewBulkOperationRepository(env.db), logger,
			WithBulkRecoveryClock(pastHorizon))
		var sweptCount int64
		var sweepErr error
		sweep := func() { sweptCount, sweepErr = sweeper.Sweep(context.Background()) }

		operationRepo := repository.BulkOperationRepository(repository.NewBulkOperationRepository(env.db))
		statusRepo := repository.BulkStatusRepository(repository.NewBulkStatusRepository(env.db))
		if engine.concurrent {
			// The sweep commits on another connection between the batch's
			// status write and its completion.
			statusRepo = sweepingStatusRepo{BulkStatusRepository: statusRepo, sweep: sweep}
		} else {
			// One connection: the sweep commits between the history row and the
			// batch transaction, the only gap SQLite leaves.
			operationRepo = sweepAfterCreateRepo{BulkOperationRepository: operationRepo, sweep: sweep}
		}
		svc := NewBulkStatusService(operationRepo, statusRepo, env.txManager, logger, WithBulkAutomationEvents(env.repo))

		_, err := svc.BulkSetLeadStatus(context.Background(), env.admin.ID, models.RoleAdmin, []uint{lead.ID}, models.LeadStatusQualified)
		require.NoError(t, sweepErr)
		require.EqualValues(t, 1, sweptCount, "the sweep failed the batch's row")

		require.ErrorIs(t, err, apperrors.ErrBulkOperationNotPending)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		assert.False(t, errors.Is(err, apperrors.ErrForbidden))
		assert.False(t, errors.Is(err, gorm.ErrRecordNotFound))
		_, isAppErr := apperrors.AsAppError(err)
		assert.False(t, isAppErr, "not an AppError (so not a 400 one): respondBulkStatusError answers 500")

		assert.Equal(t, models.LeadStatusNew, env.lead(lead.ID).Status, "the status write rolled back")
		assert.Empty(t, env.events(), "the events rolled back")
		rows := operations(t, env.db)
		require.Len(t, rows, 1)
		assert.Equal(t, models.StatusFailed, rows[0].Status, "the row stays failed")
		assert.NotContains(t, logs.String(), "Failed to record bulk status operation outcome",
			"the later conditional failed write changes nothing and logs no warning")
		assert.Empty(t, logs.String())
	})

	t.Run("a late outcome write never overwrites a terminal row", func(t *testing.T) {
		env := newAutomationEnv(t, engine.automation(t))
		repo := repository.NewBulkOperationRepository(env.db)
		completed := &models.BulkOperation{UserID: env.admin.ID, ResourceType: "leads", Type: models.BulkAction,
			Status: models.StatusPending, TotalItems: 1}
		require.NoError(t, repo.Create(context.Background(), completed))
		require.NoError(t, repo.CompletePending(context.Background(), completed.ID))
		require.ErrorIs(t, repo.UpdateStatus(context.Background(), completed.ID, models.StatusFailed), apperrors.ErrBulkOperationNotPending)

		failed := &models.BulkOperation{UserID: env.admin.ID, ResourceType: "leads", Type: models.BulkAction,
			Status: models.StatusPending, TotalItems: 1}
		require.NoError(t, repo.Create(context.Background(), failed))
		require.NoError(t, repo.UpdateStatus(context.Background(), failed.ID, models.StatusFailed))
		require.ErrorIs(t, repo.CompletePending(context.Background(), failed.ID), apperrors.ErrBulkOperationNotPending)

		swept, err := SweepStaleBulkOperations(context.Background(), repo, pastHorizon())
		require.NoError(t, err)
		assert.Zero(t, swept, "terminal rows are never swept")

		rows := operations(t, env.db)
		require.Len(t, rows, 2)
		assert.Equal(t, models.StatusCompleted, rows[0].Status)
		assert.Equal(t, models.StatusFailed, rows[1].Status)
	})
}
