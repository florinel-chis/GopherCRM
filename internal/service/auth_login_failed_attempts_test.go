package service

// Regression roots for issue #545: on a service built by
// NewAuthServiceWithSessions a wrong password is counted from the owner row
// read by primary key with a locking read inside one WithTransactionAndRetry
// transaction, so parallel failed attempts each count and the lock after
// MaxFailedLoginAttempts applies whatever the timing of the requests.
//
// The losing order is forced: every parallel login has finished its plain
// email lookup before any of them records its failure. The roots run on
// in-memory SQLite with the production one-connection pool and on a
// disposable MySQL/MariaDB schema when TEST_MYSQL_DSN is set (skipped without
// it). Every assertion reads committed rows outside any transaction; every
// wait is bounded.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	lfaEmail         = "failed-attempts-owner@example.com"
	lfaPassword      = "FailedAttempts12!"
	lfaWrongPassword = "WrongPassw0rd!x"
	lfaBarrierWait   = 10 * time.Second
)

// lfaBarrier holds every caller after its read until `parties` callers have
// read. A caller that waits longer than lfaBarrierWait reports an error.
type lfaBarrier struct {
	mu      sync.Mutex
	armed   bool
	parties int
	arrived int
	open    chan struct{}
	// afterAll, when set, runs once, after the last party has read and before
	// any of them proceeds.
	afterAll func()
}

func (b *lfaBarrier) arm(parties int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.armed, b.parties, b.arrived, b.open = true, parties, 0, make(chan struct{})
}

func (b *lfaBarrier) disarm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.armed = false
}

func (b *lfaBarrier) wait() error {
	b.mu.Lock()
	if !b.armed {
		b.mu.Unlock()
		return nil
	}
	b.arrived++
	open := b.open
	if b.arrived == b.parties {
		if b.afterAll != nil {
			b.afterAll()
		}
		close(open)
	}
	b.mu.Unlock()
	select {
	case <-open:
		return nil
	case <-time.After(lfaBarrierWait):
		return errors.New("barrier: not every parallel login read the user in time")
	}
}

// lfaUserRepository is the real repository whose GetByEmail waits on the
// barrier after the real read. WithTx is the real one, so reads inside the
// transaction go straight to the database.
type lfaUserRepository struct {
	repository.UserRepository
	barrier *lfaBarrier
	errs    chan error
}

func (r *lfaUserRepository) GetByEmail(email string) (*models.User, error) {
	user, err := r.UserRepository.GetByEmail(email)
	if waitErr := r.barrier.wait(); waitErr != nil {
		r.errs <- waitErr
	}
	return user, err
}

func lfaService(db *gorm.DB, userRepo repository.UserRepository, now time.Time) *authService {
	s := NewAuthServiceWithSessions(
		userRepo,
		repository.NewAPIKeyRepository(db),
		repository.NewRefreshTokenRepository(db),
		repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db),
		&fakeFormMailer{},
		config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30},
		resetTestBaseURL, "").(*authService)
	s.loginTime = func() time.Time { return now }
	return s
}

func lfaSeed(t *testing.T, db *gorm.DB, attempts int, lockedUntil *time.Time) *models.User {
	t.Helper()
	ctx := resetContext(t)
	for _, table := range []string{"refresh_tokens", "api_keys", "users"} {
		require.NoError(t, db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(lfaPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: lfaEmail, Password: string(hash), FirstName: "Failed", LastName: "Attempts",
		Role: models.RoleSales, IsActive: true, FailedLoginAttempts: attempts, LockedUntil: lockedUntil}
	require.NoError(t, db.WithContext(ctx).Create(user).Error)
	return user
}

type lfaRow struct {
	FailedLoginAttempts int
	LockedUntil         *time.Time
}

func lfaReadRow(t *testing.T, db *gorm.DB, id uint) lfaRow {
	t.Helper()
	var row lfaRow
	require.NoError(t, db.WithContext(resetContext(t)).Unscoped().Model(&models.User{}).
		Select("failed_login_attempts", "locked_until").Where("id = ?", id).Take(&row).Error)
	return row
}

func lfaCountRefreshTokens(t *testing.T, db *gorm.DB, userID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.WithContext(resetContext(t)).Model(&models.RefreshToken{}).
		Where("user_id = ?", userID).Count(&n).Error)
	return n
}

// lfaParallelWrongPasswords runs n parallel wrong-password logins through the
// barrier and returns their errors.
func lfaParallelWrongPasswords(t *testing.T, s *authService, repo *lfaUserRepository, n int) []error {
	t.Helper()
	repo.barrier.arm(n)
	results := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = s.LoginWithTokens(lfaEmail, lfaWrongPassword)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * resetTimeout):
		t.Fatal("the parallel logins did not finish in time")
	}
	repo.barrier.disarm()
	select {
	case err := <-repo.errs:
		t.Fatal(err)
	default:
	}
	return results
}

func runLoginFailedAttemptsConcurrent(t *testing.T, db *gorm.DB) {
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	newFixture := func() (*authService, *lfaUserRepository) {
		repo := &lfaUserRepository{UserRepository: repository.NewUserRepository(db),
			barrier: &lfaBarrier{}, errs: make(chan error, 64)}
		return lfaService(db, repo, now), repo
	}
	assertAllInvalid := func(t *testing.T, results []error) {
		t.Helper()
		for i, err := range results {
			require.Error(t, err, "login %d", i)
			assert.EqualError(t, err, errLoginInvalidCredentials.Error(), "login %d", i)
		}
	}

	t.Run("below the threshold", func(t *testing.T) {
		user := lfaSeed(t, db, 0, nil)
		s, repo := newFixture()
		n := MaxFailedLoginAttempts - 1
		assertAllInvalid(t, lfaParallelWrongPasswords(t, s, repo, n))
		row := lfaReadRow(t, db, user.ID)
		assert.Equal(t, n, row.FailedLoginAttempts)
		assert.Nil(t, row.LockedUntil)
	})

	t.Run("past the threshold", func(t *testing.T) {
		user := lfaSeed(t, db, 0, nil)
		s, repo := newFixture()
		assertAllInvalid(t, lfaParallelWrongPasswords(t, s, repo, 20))
		row := lfaReadRow(t, db, user.ID)
		assert.Equal(t, MaxFailedLoginAttempts, row.FailedLoginAttempts)
		require.NotNil(t, row.LockedUntil)
		assert.WithinDuration(t, now.Add(AccountLockDuration), *row.LockedUntil, time.Second)

		_, err := s.LoginWithTokens(lfaEmail, lfaPassword)
		require.ErrorIs(t, err, errLoginAccountLocked)
		assert.Zero(t, lfaCountRefreshTokens(t, db, user.ID))
	})

	t.Run("expired lock starts a fresh window", func(t *testing.T) {
		expired := now.Add(-time.Minute)
		user := lfaSeed(t, db, MaxFailedLoginAttempts, &expired)
		s, repo := newFixture()
		assertAllInvalid(t, lfaParallelWrongPasswords(t, s, repo, 3))
		row := lfaReadRow(t, db, user.ID)
		assert.Equal(t, 3, row.FailedLoginAttempts)
		assert.Nil(t, row.LockedUntil)
	})
}

func TestLoginFailedAttemptsConcurrentSQLite(t *testing.T) {
	runLoginFailedAttemptsConcurrent(t, openResetTestSQLite(t))
}

func TestLoginFailedAttemptsConcurrentMySQL(t *testing.T) {
	runLoginFailedAttemptsConcurrent(t, openResetTestMySQL(t, 5))
}

// lfaFailingUserRepository's transaction-bound repository fails GetByID with
// an error whose text carries the user's email.
type lfaFailingUserRepository struct {
	repository.UserRepository
}

func (r *lfaFailingUserRepository) WithTx(tx *gorm.DB) repository.UserRepository {
	return &lfaFailingTxUserRepository{UserRepository: r.UserRepository.WithTx(tx)}
}

type lfaFailingTxUserRepository struct {
	repository.UserRepository
}

const lfaInjectedError = "injected read failure"

func (r *lfaFailingTxUserRepository) GetByID(uint) (*models.User, error) {
	return nil, fmt.Errorf("%s for %s", lfaInjectedError, lfaEmail)
}

// lfaCaptureLog captures utils.Logger as JSON lines and restores it afterwards.
func lfaCaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })
	return &output
}

func lfaRecordFailedLines(t *testing.T, output *bytes.Buffer) []map[string]interface{} {
	t.Helper()
	var lines []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["error_code"] == "login_failure_record_failed" {
			lines = append(lines, entry)
		}
	}
	return lines
}

func TestLoginFailedAttemptsRecordFailureLogSQLite(t *testing.T) {
	db := openResetTestSQLite(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	user := lfaSeed(t, db, 0, nil)
	s := lfaService(db, &lfaFailingUserRepository{UserRepository: repository.NewUserRepository(db)}, now)
	output := lfaCaptureLog(t)

	_, err := s.LoginWithTokens(lfaEmail, lfaWrongPassword)
	require.EqualError(t, err, errLoginInvalidCredentials.Error())

	lines := lfaRecordFailedLines(t, output)
	require.Len(t, lines, 1, output.String())
	assert.EqualValues(t, user.ID, lines[0]["user_id"])
	logged := output.String()
	for _, secret := range []string{lfaEmail, lfaWrongPassword, lfaInjectedError} {
		assert.NotContains(t, logged, secret)
	}
	row := lfaReadRow(t, db, user.ID)
	assert.Zero(t, row.FailedLoginAttempts)
	assert.Nil(t, row.LockedUntil)
}

// An account erased between the plain lookup and the record keeps its row
// untouched, and the miss is not logged as a failed write.
func TestLoginWrongPasswordErasedDuringRecordSQLite(t *testing.T) {
	db := openResetTestSQLite(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	user := lfaSeed(t, db, 0, nil)
	base := repository.NewUserRepository(db)
	barrier := &lfaBarrier{afterAll: func() { _ = base.Delete(user.ID) }}
	repo := &lfaUserRepository{UserRepository: base, barrier: barrier, errs: make(chan error, 1)}
	s := lfaService(db, repo, now)
	output := lfaCaptureLog(t)

	results := lfaParallelWrongPasswords(t, s, repo, 1)
	require.EqualError(t, results[0], errLoginInvalidCredentials.Error())
	assert.Empty(t, lfaRecordFailedLines(t, output))
	row := lfaReadRow(t, db, user.ID)
	assert.Zero(t, row.FailedLoginAttempts)
	assert.Nil(t, row.LockedUntil)
}
