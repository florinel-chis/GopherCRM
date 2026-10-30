package service

// Regression roots for issue #102: a password reset token is claimed inside
// the transaction that changes the password, clears the lockout and revokes
// every refresh session, so a failed or competing confirmation can neither
// burn the link nor commit half of the reset.
//
// Every root runs on a real database — in-memory SQLite with the production
// one-connection pool, and a disposable MySQL schema when TEST_MYSQL_DSN is
// set — and every assertion reads committed rows outside any transaction. The
// hooks below interpose on the transaction-bound repositories so a scenario
// can fail, or alter rows the way a competing request would, at an exact point
// of the unit of work. All waits are bounded.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// resetTimeout bounds every wait and verification read. It is generous for a
// loaded CI host but far below the check timeout, so a hung reset fails the
// scenario instead of the package.
const resetTimeout = 20 * time.Second

// resetCompetitors is how many requests race for one link.
const resetCompetitors = 3

const (
	resetTestSecret  = "password-reset-regression-secret-32-bytes"
	resetTestBaseURL = "http://localhost:5173"
	resetOldPassword = "OldSecret123!"
	resetNewPassword = "BrandNewPass1!"
)

var resetTestDBSeq atomic.Uint64

func resetContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), resetTimeout)
	t.Cleanup(cancel)
	return ctx
}

// migrateResetTestSchema creates the tables the reset flow touches, plus the
// two more that user erasure purges or scrubs. Foreign keys are not created:
// the missing-owner scenario keeps a token whose user row is gone, which
// production erasure never produces (it purges the tokens) but the service
// must still refuse.
func migrateResetTestSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.RefreshToken{}, &models.PasswordResetToken{},
		&models.APIKey{}, &models.TicketComment{},
	))
}

func resetTestGormConfig() *gorm.Config {
	cfg := &gorm.Config{Logger: logger.Discard}
	cfg.DisableForeignKeyConstraintWhenMigrating = true
	return cfg
}

// openResetTestSQLite is an in-memory database with the pool pinned to one
// connection, exactly as a SQLite deployment runs: a second pooled connection
// to ":memory:" would open a second, empty database.
func openResetTestSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), resetTestGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateResetTestSchema(t, db)
	return db
}

// openResetTestMySQL creates a uniquely named disposable schema on the server
// identified by TEST_MYSQL_DSN and drops it afterwards. The named acceptance
// checks require the variable; the ordinary `go test ./...` run on a machine
// without a MySQL server skips, which the acceptance parser rejects. A
// configured but unreachable server fails rather than skips.
func openResetTestMySQL(t *testing.T, maxConns int) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, resetTimeout, resetTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_pwreset_%d_%d", time.Now().UnixNano(), resetTestDBSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), resetTestGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	// Registered after the DROP cleanup, so it runs first and the schema is
	// dropped only once its last connection is closed.
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateResetTestSchema(t, db)
	return db
}

// ------------------------------------------------------------------- hooks ---

// resetHooks interposes on the repositories the service resets through. Every
// hook fires on the transaction-bound repository, and the token-lookup hook
// receives the transaction's handle, so a scenario can alter rows the way a
// competing request would — or fail the way a broken database would — at an
// exact point of the unit of work. Access is mutex-guarded because the
// concurrent scenario installs and fires hooks from different goroutines.
type resetHooks struct {
	mu               sync.Mutex
	afterTokenLookup func(tx *gorm.DB, token *models.PasswordResetToken) error
	beforeClaim      func(id uint) error
	beforeUserUpdate func(user *models.User) error
	beforeRevokeAll  func(userID uint) error
}

func (h *resetHooks) install(fn func(h *resetHooks)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *resetHooks) clear() {
	h.install(func(h *resetHooks) {
		h.afterTokenLookup = nil
		h.beforeClaim = nil
		h.beforeUserUpdate = nil
		h.beforeRevokeAll = nil
	})
}

func (h *resetHooks) tokenLookup() func(*gorm.DB, *models.PasswordResetToken) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.afterTokenLookup
}

func (h *resetHooks) claim() func(uint) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeClaim
}

func (h *resetHooks) userUpdate() func(*models.User) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeUserUpdate
}

func (h *resetHooks) revokeAll() func(uint) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeRevokeAll
}

// hookedResetTokenRepository wraps the real repository; WithTx keeps the hooks
// and remembers the handle the wrapped repository is bound to.
type hookedResetTokenRepository struct {
	repository.PasswordResetTokenRepository
	db    *gorm.DB
	hooks *resetHooks
}

func (r *hookedResetTokenRepository) WithTx(tx *gorm.DB) repository.PasswordResetTokenRepository {
	return &hookedResetTokenRepository{PasswordResetTokenRepository: r.PasswordResetTokenRepository.WithTx(tx), db: tx, hooks: r.hooks}
}

func (r *hookedResetTokenRepository) GetByTokenHash(hash string) (*models.PasswordResetToken, error) {
	token, err := r.PasswordResetTokenRepository.GetByTokenHash(hash)
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

func (r *hookedResetTokenRepository) MarkUsed(id uint) error {
	if hook := r.hooks.claim(); hook != nil {
		if err := hook(id); err != nil {
			return err
		}
	}
	return r.PasswordResetTokenRepository.MarkUsed(id)
}

type hookedUserRepository struct {
	repository.UserRepository
	hooks *resetHooks
}

func (r *hookedUserRepository) WithTx(tx *gorm.DB) repository.UserRepository {
	return &hookedUserRepository{UserRepository: r.UserRepository.WithTx(tx), hooks: r.hooks}
}

func (r *hookedUserRepository) UpdateFields(id uint, fields map[string]interface{}) error {
	if hook := r.hooks.userUpdate(); hook != nil {
		if err := hook(&models.User{BaseModel: models.BaseModel{ID: id}}); err != nil {
			return err
		}
	}
	return r.UserRepository.UpdateFields(id, fields)
}

type hookedRefreshTokenRepository struct {
	repository.RefreshTokenRepository
	hooks *resetHooks
}

func (r *hookedRefreshTokenRepository) WithTx(tx *gorm.DB) repository.RefreshTokenRepository {
	return &hookedRefreshTokenRepository{RefreshTokenRepository: r.RefreshTokenRepository.WithTx(tx), hooks: r.hooks}
}

func (r *hookedRefreshTokenRepository) RevokeAllForUser(userID uint) error {
	if hook := r.hooks.revokeAll(); hook != nil {
		if err := hook(userID); err != nil {
			return err
		}
	}
	return r.RefreshTokenRepository.RevokeAllForUser(userID)
}

// --------------------------------------------------------- committed state ---

// accountState is what the database holds for one account, read outside any
// transaction.
type accountState struct {
	exists          bool
	active          bool
	unspentTokens   int64 // reset tokens with no used_at stamp
	spentTokens     int64 // reset tokens stamped used
	activeSessions  int64 // refresh tokens not revoked
	revokedSessions int64 // refresh tokens revoked
	failedAttempts  int
	locked          bool
}

var (
	// seededOwner is the account a reset link was issued for: locked out,
	// with two live sessions.
	seededOwner = accountState{exists: true, active: true, unspentTokens: 1, activeSessions: 2,
		failedAttempts: MaxFailedLoginAttempts, locked: true}
	// resetOwner is the same account after a committed reset.
	resetOwner = accountState{exists: true, active: true, spentTokens: 1, revokedSessions: 2}
	// bystanderState is an unrelated account that must never change.
	bystanderState = accountState{exists: true, active: true, unspentTokens: 1, activeSessions: 1}
)

// ----------------------------------------------------------------- fixture ---

type resetFixture struct {
	db          *gorm.DB
	userRepo    repository.UserRepository
	refreshRepo repository.RefreshTokenRepository
	resetRepo   repository.PasswordResetTokenRepository
	hooks       *resetHooks
	mailer      *fakeFormMailer
	svc         AuthService
}

func newResetFixture(t *testing.T, db *gorm.DB) *resetFixture {
	t.Helper()
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})

	f := &resetFixture{
		db:          db,
		userRepo:    repository.NewUserRepository(db),
		refreshRepo: repository.NewRefreshTokenRepository(db),
		resetRepo:   repository.NewPasswordResetTokenRepository(db),
		hooks:       &resetHooks{},
		mailer:      &fakeFormMailer{},
	}
	f.svc = NewAuthServiceWithSessions(
		&hookedUserRepository{UserRepository: f.userRepo, hooks: f.hooks},
		repository.NewAPIKeyRepository(db),
		&hookedRefreshTokenRepository{RefreshTokenRepository: f.refreshRepo, hooks: f.hooks},
		&hookedResetTokenRepository{PasswordResetTokenRepository: f.resetRepo, db: db, hooks: f.hooks},
		utils.NewTransactionManager(db),
		f.mailer,
		config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30},
		resetTestBaseURL, "")
	return f
}

// reset empties the tables a scenario writes, the recorded mail and the hooks,
// so every scenario starts from a blank slate on the shared schema.
func (f *resetFixture) reset(t *testing.T) {
	t.Helper()
	f.hooks.clear()
	f.mailer.reset()
	ctx := resetContext(t)
	for _, table := range []string{"password_reset_tokens", "refresh_tokens", "users"} {
		require.NoError(t, f.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
}

func (f *resetFixture) exec(t *testing.T, statement string, args ...interface{}) {
	t.Helper()
	require.NoError(t, f.db.WithContext(resetContext(t)).Exec(statement, args...).Error)
}

func (f *resetFixture) createUser(t *testing.T, email string, locked bool) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(resetOldPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{
		Email:     email,
		Password:  string(hash),
		FirstName: "Reset",
		LastName:  "Owner",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	if locked {
		until := time.Now().Add(AccountLockDuration)
		user.FailedLoginAttempts = MaxFailedLoginAttempts
		user.LockedUntil = &until
	}
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(user).Error)
	return user
}

func (f *resetFixture) createSession(t *testing.T, userID uint, raw string) {
	t.Helper()
	require.NoError(t, f.refreshRepo.Create(&models.RefreshToken{
		UserID:    userID,
		TokenHash: hashOpaqueToken(raw, resetTestSecret),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))
}

// issueLink requests a reset for the account through the service and returns
// the raw token of the mailed link.
func (f *resetFixture) issueLink(t *testing.T, user *models.User) string {
	t.Helper()
	f.mailer.reset()
	require.NoError(t, f.svc.RequestPasswordReset(user.Email))
	messages := f.mailer.messages()
	require.Len(t, messages, 1, "exactly one reset mail goes out")
	require.Equal(t, user.Email, messages[0].To)
	parsed, err := url.Parse(messages[0].Body)
	require.NoError(t, err)
	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)
	f.mailer.reset()
	return token
}

// resetSeed is the starting point of every scenario: a locked-out owner with
// two sessions and a fresh link, and a bystander with a session and a link
// of their own.
type resetSeed struct {
	owner     *models.User
	bystander *models.User
	token     string
}

func (f *resetFixture) seed(t *testing.T) resetSeed {
	t.Helper()
	f.reset(t)
	owner := f.createUser(t, "owner@example.com", true)
	bystander := f.createUser(t, "bystander@example.com", false)
	f.createSession(t, owner.ID, "owner-session-a")
	f.createSession(t, owner.ID, "owner-session-b")
	f.createSession(t, bystander.ID, "bystander-session")
	f.issueLink(t, bystander)
	token := f.issueLink(t, owner)
	require.Equal(t, seededOwner, f.state(t, owner.ID))
	require.Equal(t, bystanderState, f.state(t, bystander.ID))
	return resetSeed{owner: owner, bystander: bystander, token: token}
}

func (f *resetFixture) state(t *testing.T, userID uint) accountState {
	t.Helper()
	ctx := resetContext(t)
	var s accountState
	count := func(dst *int64, model interface{}, where string, args ...interface{}) {
		require.NoError(t, f.db.WithContext(ctx).Model(model).Where(where, args...).Count(dst).Error)
	}
	count(&s.unspentTokens, &models.PasswordResetToken{}, "user_id = ? AND used_at IS NULL", userID)
	count(&s.spentTokens, &models.PasswordResetToken{}, "user_id = ? AND used_at IS NOT NULL", userID)
	count(&s.activeSessions, &models.RefreshToken{}, "user_id = ? AND is_revoked = ?", userID, false)
	count(&s.revokedSessions, &models.RefreshToken{}, "user_id = ? AND is_revoked = ?", userID, true)

	// Unscoped, so an erased (soft-deleted) account is still inspected.
	var user models.User
	err := f.db.WithContext(ctx).Unscoped().First(&user, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s
	}
	require.NoError(t, err)
	s.exists = true
	s.active = user.IsActive
	s.failedAttempts = user.FailedLoginAttempts
	s.locked = user.LockedUntil != nil
	return s
}

// passwordAccepts reports whether the committed hash of the account verifies
// the password.
func (f *resetFixture) passwordAccepts(t *testing.T, userID uint, password string) bool {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&user, userID).Error)
	return bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) == nil
}

func (f *resetFixture) assertBystanderIntact(t *testing.T, s resetSeed) {
	t.Helper()
	assert.Equal(t, bystanderState, f.state(t, s.bystander.ID), "an unrelated account must not change")
	assert.True(t, f.passwordAccepts(t, s.bystander.ID, resetOldPassword), "the bystander keeps their password")
}

// assertUntouched checks that a rejected or failed reset left the owner
// exactly as seeded: link unspent, old password, lockout in place, both
// sessions alive.
func (f *resetFixture) assertUntouched(t *testing.T, s resetSeed) {
	t.Helper()
	assert.Equal(t, seededOwner, f.state(t, s.owner.ID))
	assert.True(t, f.passwordAccepts(t, s.owner.ID, resetOldPassword), "the old password must still be the committed one")
	f.assertBystanderIntact(t, s)
}

// assertReset checks the committed state of a successful reset: token spent,
// lockout cleared, both sessions revoked, and only the new password signs in.
func (f *resetFixture) assertReset(t *testing.T, s resetSeed, newPassword string) {
	t.Helper()
	assert.Equal(t, resetOwner, f.state(t, s.owner.ID))
	assert.True(t, f.passwordAccepts(t, s.owner.ID, newPassword), "the new password must be the committed one")
	assert.False(t, f.passwordAccepts(t, s.owner.ID, resetOldPassword), "the old password must be gone")
	f.assertBystanderIntact(t, s)

	// Login proves the lockout is cleared and only the new password works.
	_, err := f.svc.Login(s.owner.Email, resetOldPassword)
	assert.Error(t, err, "the old password must not sign in")
	jwt, err := f.svc.Login(s.owner.Email, newPassword)
	assert.NoError(t, err, "the new password must sign in once the lockout is cleared")
	assert.NotEmpty(t, jwt)
}

// ------------------------------------------------------------------- roots ---

func TestPasswordResetValidationSQLite(t *testing.T) {
	runPasswordResetValidationScenarios(t, openResetTestSQLite(t))
}

func TestPasswordResetValidationMySQL(t *testing.T) {
	runPasswordResetValidationScenarios(t, openResetTestMySQL(t, 1))
}

func TestPasswordResetAtomicitySQLite(t *testing.T) {
	runPasswordResetAtomicityScenarios(t, openResetTestSQLite(t))
}

func TestPasswordResetAtomicityMySQL(t *testing.T) {
	runPasswordResetAtomicityScenarios(t, openResetTestMySQL(t, 1))
}

func TestPasswordResetConcurrentSQLite(t *testing.T) {
	runPasswordResetConcurrentScenarios(t, openResetTestSQLite(t), 1)
}

func TestPasswordResetConcurrentMySQL(t *testing.T) {
	runPasswordResetConcurrentScenarios(t, openResetTestMySQL(t, resetCompetitors+1), resetCompetitors+1)
}

// -------------------------------------------------------------- validation ---

func runPasswordResetValidationScenarios(t *testing.T, db *gorm.DB) {
	f := newResetFixture(t, db)

	t.Run("a weak password is refused and does not spend the token", func(t *testing.T) {
		s := f.seed(t)
		for _, weak := range []string{"Short1!", "alllowercase1!", "ALLUPPERCASE1!", "NoDigitsHere!", "NoSpecial123"} {
			err := f.svc.ConfirmPasswordReset(s.token, weak)
			require.Error(t, err, "password %q must be refused", weak)
			assert.NotErrorIs(t, err, ErrInvalidResetToken, "a policy failure is not a token failure")
		}
		f.assertUntouched(t, s)

		// The same link then completes the reset.
		require.NoError(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword))
		f.assertReset(t, s, resetNewPassword)
	})

	t.Run("a password bcrypt cannot hash is refused and does not spend the token", func(t *testing.T) {
		s := f.seed(t)
		// Complex by the policy, but over bcrypt's 72-byte limit.
		tooLong := "Aa1!" + strings.Repeat("x", 80)
		require.NoError(t, utils.ValidatePasswordComplexity(tooLong))

		err := f.svc.ConfirmPasswordReset(s.token, tooLong)
		require.ErrorIs(t, err, bcrypt.ErrPasswordTooLong)
		assert.NotErrorIs(t, err, ErrInvalidResetToken)
		f.assertUntouched(t, s)

		require.NoError(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword))
		f.assertReset(t, s, resetNewPassword)
	})

	t.Run("unknown and empty tokens are rejected", func(t *testing.T) {
		s := f.seed(t)
		for _, token := range []string{"", "never-issued"} {
			assert.ErrorIs(t, f.svc.ConfirmPasswordReset(token, resetNewPassword), ErrInvalidResetToken)
		}
		f.assertUntouched(t, s)
	})

	t.Run("a spent token is rejected without touching the account", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "UPDATE password_reset_tokens SET used_at = ? WHERE user_id = ?", time.Now().Add(-time.Minute), s.owner.ID)

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)

		expected := seededOwner
		expected.unspentTokens, expected.spentTokens = 0, 1
		assert.Equal(t, expected, f.state(t, s.owner.ID))
		assert.True(t, f.passwordAccepts(t, s.owner.ID, resetOldPassword))
		f.assertBystanderIntact(t, s)
	})

	t.Run("an expired token is rejected and stays unspent", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "UPDATE password_reset_tokens SET expires_at = ? WHERE user_id = ?", time.Now().Add(-time.Second), s.owner.ID)

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)
		f.assertUntouched(t, s)
	})

	t.Run("a token that expires between the lookup and the claim is rejected", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *resetHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.PasswordResetToken) error {
				// The lookup has just returned the token as spendable; the clock
				// crosses its expiry before the claim runs. Written through the
				// transaction's own handle: on the one-connection pool a second
				// handle would wait for the connection the transaction holds.
				return tx.Model(&models.PasswordResetToken{}).Where("id = ?", stored.ID).
					Update("expires_at", time.Now().Add(-time.Second)).Error
			}
		})

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)
		f.assertUntouched(t, s)

		// The rollback discarded the simulated expiry with everything else, so
		// the same link completes once the clock no longer crosses it.
		f.hooks.clear()
		require.NoError(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword))
		f.assertReset(t, s, resetNewPassword)
	})

	t.Run("a token spent between the lookup and the claim is rejected", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *resetHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.PasswordResetToken) error {
				// A competing request stamped the row after this one read it.
				return tx.Model(&models.PasswordResetToken{}).Where("id = ?", stored.ID).
					Update("used_at", time.Now()).Error
			}
		})

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)
		f.assertUntouched(t, s)
	})

	t.Run("an inactive owner is rejected and the token stays unspent", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, s.owner.ID)

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)

		expected := seededOwner
		expected.active = false
		assert.Equal(t, expected, f.state(t, s.owner.ID))
		assert.True(t, f.passwordAccepts(t, s.owner.ID, resetOldPassword))
		f.assertBystanderIntact(t, s)
	})

	t.Run("an owner deactivated between the lookup and the owner check is rejected and not reactivated", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *resetHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.PasswordResetToken) error {
				// The account is disabled after this request read its token as
				// spendable. The owner check must see the current row, not a
				// user record read earlier or a stale snapshot, and the
				// full-row password update must never write is_active back.
				return tx.Model(&models.User{}).Where("id = ?", stored.UserID).Update("is_active", false).Error
			}
		})

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)
		// The rollback discarded the simulated deactivation with everything
		// else, so the account is exactly as seeded.
		f.assertUntouched(t, s)

		f.hooks.clear()
		require.NoError(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword))
		f.assertReset(t, s, resetNewPassword)
	})

	t.Run("an owner soft-deleted between the lookup and the owner check is rejected", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *resetHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.PasswordResetToken) error {
				return tx.Model(&models.User{}).Where("id = ?", stored.UserID).Update("deleted_at", time.Now()).Error
			}
		})

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)
		f.assertUntouched(t, s)

		f.hooks.clear()
		require.NoError(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword))
		f.assertReset(t, s, resetNewPassword)
	})

	t.Run("an erased owner is rejected", func(t *testing.T) {
		s := f.seed(t)
		// Erasure scrubs the row, purges its credentials and soft-deletes it.
		require.NoError(t, f.userRepo.Delete(s.owner.ID))

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)

		assert.Equal(t, accountState{exists: true}, f.state(t, s.owner.ID), "erasure leaves a scrubbed, inactive row with no credentials")
		assert.False(t, f.passwordAccepts(t, s.owner.ID, resetNewPassword))
		f.assertBystanderIntact(t, s)
	})

	t.Run("a soft-deleted owner whose token survived is rejected and the token stays unspent", func(t *testing.T) {
		s := f.seed(t)
		// Defence in depth: a token that escaped the purge must not reach the
		// scoped-out row.
		f.exec(t, "UPDATE users SET deleted_at = ? WHERE id = ?", time.Now(), s.owner.ID)

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)
		f.assertUntouched(t, s)
	})

	t.Run("a missing owner is rejected and the token stays unspent", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "DELETE FROM users WHERE id = ?", s.owner.ID)

		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword), ErrInvalidResetToken)

		expected := seededOwner
		expected.exists, expected.active, expected.failedAttempts, expected.locked = false, false, 0, false
		assert.Equal(t, expected, f.state(t, s.owner.ID), "the orphaned token and sessions are left as they were")
		f.assertBystanderIntact(t, s)
	})
}

// --------------------------------------------------------------- atomicity ---

func runPasswordResetAtomicityScenarios(t *testing.T, db *gorm.DB) {
	f := newResetFixture(t, db)
	injected := errors.New("injected database failure")

	cases := []struct {
		name    string
		install func(h *resetHooks)
	}{
		{name: "the token claim fails", install: func(h *resetHooks) {
			h.beforeClaim = func(uint) error { return injected }
		}},
		{name: "the password update fails after the claim", install: func(h *resetHooks) {
			h.beforeUserUpdate = func(*models.User) error { return injected }
		}},
		{name: "the session revocation fails after the password update", install: func(h *resetHooks) {
			h.beforeRevokeAll = func(uint) error { return injected }
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := f.seed(t)
			f.hooks.install(tc.install)

			err := f.svc.ConfirmPasswordReset(s.token, resetNewPassword)
			require.ErrorIs(t, err, injected, "the failure reaches the caller as it is")
			assert.NotErrorIs(t, err, ErrInvalidResetToken)
			f.assertUntouched(t, s)

			// The same link, retried once the database behaves, completes the
			// reset: the token was handed back by the rollback.
			f.hooks.clear()
			require.NoError(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword))
			f.assertReset(t, s, resetNewPassword)
		})
	}

	t.Run("a successful reset clears the lockout, revokes every session and leaves other users alone", func(t *testing.T) {
		s := f.seed(t)

		require.NoError(t, f.svc.ConfirmPasswordReset(s.token, resetNewPassword))
		f.assertReset(t, s, resetNewPassword)

		// A replay of the spent link changes nothing.
		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, "ReplayedPass9!"), ErrInvalidResetToken)
		assert.Equal(t, resetOwner, f.state(t, s.owner.ID))
		assert.True(t, f.passwordAccepts(t, s.owner.ID, resetNewPassword))
		assert.False(t, f.passwordAccepts(t, s.owner.ID, "ReplayedPass9!"))
		f.assertBystanderIntact(t, s)
	})
}

// -------------------------------------------------------------- concurrent ---

// runPasswordResetConcurrentScenarios races several requests for one link,
// each with its own valid password. With several connections every competitor
// is held after its lookup until all of them have read the token as
// spendable, so the claim alone decides the winner. On the one-connection
// SQLite pool that barrier cannot be used — the second request cannot even
// begin until the first has ended — so the competitors start together and the
// pool serialises them; the outcome is the same and just as deterministic:
// one commit, the winner's password, every other request rejected.
func runPasswordResetConcurrentScenarios(t *testing.T, db *gorm.DB, connections int) {
	f := newResetFixture(t, db)

	t.Run("competing resets on one link: exactly one commits and its password wins", func(t *testing.T) {
		s := f.seed(t)

		var barrierFailed atomic.Bool
		if connections > 1 {
			arrived := make(chan struct{}, resetCompetitors)
			release := make(chan struct{})
			f.hooks.install(func(h *resetHooks) {
				h.afterTokenLookup = func(*gorm.DB, *models.PasswordResetToken) error {
					arrived <- struct{}{}
					select {
					case <-release:
						return nil
					case <-time.After(resetTimeout):
						barrierFailed.Store(true)
						return errors.New("barrier: the other competitors never reached the lookup")
					}
				}
			})
			go func() {
				defer close(release)
				deadline := time.After(resetTimeout)
				for i := 0; i < resetCompetitors; i++ {
					select {
					case <-arrived:
					case <-deadline:
						barrierFailed.Store(true)
						return
					}
				}
			}()
		}

		passwords := make([]string, resetCompetitors)
		for i := range passwords {
			passwords[i] = fmt.Sprintf("Competitor%dPass!", i)
			require.NoError(t, utils.ValidatePasswordComplexity(passwords[i]))
		}

		type outcome struct {
			password string
			err      error
		}
		start := make(chan struct{})
		results := make(chan outcome, resetCompetitors)
		for _, password := range passwords {
			go func(password string) {
				<-start
				results <- outcome{password: password, err: f.svc.ConfirmPasswordReset(s.token, password)}
			}(password)
		}
		close(start)

		var winners, losers []string
		deadline := time.After(resetTimeout)
		for i := 0; i < resetCompetitors; i++ {
			select {
			case r := <-results:
				switch {
				case r.err == nil:
					winners = append(winners, r.password)
				case errors.Is(r.err, ErrInvalidResetToken):
					losers = append(losers, r.password)
				default:
					t.Errorf("a competitor failed with something other than a rejection: %v", r.err)
				}
			case <-deadline:
				t.Fatal("the competing resets did not finish within the deadline")
			}
		}
		f.hooks.clear()

		assert.False(t, barrierFailed.Load(), "every competitor must pass the lookup before any claim runs")
		require.Len(t, winners, 1, "exactly one competitor resets the password")
		assert.Len(t, losers, resetCompetitors-1, "every other competitor is told the link is no longer valid")
		f.assertReset(t, s, winners[0])
		for _, lost := range losers {
			assert.False(t, f.passwordAccepts(t, s.owner.ID, lost), "a losing request must not have changed the password")
		}

		// A later replay of the same link is rejected and cannot alter the
		// committed outcome.
		assert.ErrorIs(t, f.svc.ConfirmPasswordReset(s.token, "ReplayedPass9!"), ErrInvalidResetToken)
		assert.Equal(t, resetOwner, f.state(t, s.owner.ID))
		assert.True(t, f.passwordAccepts(t, s.owner.ID, winners[0]))
		assert.False(t, f.passwordAccepts(t, s.owner.ID, "ReplayedPass9!"))
		f.assertBystanderIntact(t, s)
	})
}
