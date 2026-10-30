package service

// Regression roots for issue #116: a password change is one transaction that
// locks the owner, claims the caller's current refresh token, writes the new
// hash, revokes every other refresh token and issues the replacement session.
// The current session stays renewable, every other session loses renewal, and
// a failure at any step leaves the password and every session as they were.
//
// The roots run on real databases: in-memory SQLite with the production
// one-connection pool, and disposable MySQL/MariaDB schemas when
// TEST_MYSQL_DSN is set. The concurrency roots on MySQL use a schema built by
// the real migrations (foreign keys and indexes as in production, several
// connections, the server's REPEATABLE READ default) and force both lock
// orderings of a password change against a refresh, so a lock-order inversion
// shows up as a deadlock instead of hiding behind lucky timing. Every
// assertion reads committed rows outside any transaction; every wait is
// bounded.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	cpOldPassword = "OldSecret123!"
	cpNewPassword = "BrandNewPass1!"
	cpPresented   = "change-password-presented"
	cpSibling     = "change-password-sibling"
	cpBystander   = "change-password-bystander"
	cpForeign     = "change-password-foreign"
	// cpGrace is how long a forced-ordering scenario lets the second
	// request run into the lock the first one holds before releasing it.
	// The outcome assertions hold whether or not the second request got that
	// far; the grace only makes the contended path the one usually taken.
	cpGrace = 750 * time.Millisecond
)

// ------------------------------------------------------------------- hooks ---

type cpHooks struct {
	mu sync.Mutex
	// afterRootGetByID fires after the plain owner read the password change
	// makes before its transaction (the verification read).
	afterRootGetByID func(id uint)
	txGetByID        func(id uint) error
	updateFields     func(id uint, fields map[string]interface{}) error
	claim            func(id uint) error
	afterClaim       func(id uint) error
	revokeAll        func(userID uint) error
	create           func(token *models.RefreshToken) error
	order            []string
}

func (h *cpHooks) install(fn func(h *cpHooks)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *cpHooks) clear() {
	h.install(func(h *cpHooks) {
		h.afterRootGetByID, h.txGetByID, h.updateFields = nil, nil, nil
		h.claim, h.afterClaim, h.revokeAll, h.create = nil, nil, nil, nil
		h.order = nil
	})
}

func (h *cpHooks) record(step string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.order = append(h.order, step)
}

func (h *cpHooks) steps() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.order...)
}

func (h *cpHooks) get(fn func(h *cpHooks) interface{}) interface{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fn(h)
}

type cpUserRepository struct {
	repository.UserRepository
	hooks *cpHooks
	inTx  bool
}

func (r *cpUserRepository) WithTx(tx *gorm.DB) repository.UserRepository {
	return &cpUserRepository{UserRepository: r.UserRepository.WithTx(tx), hooks: r.hooks, inTx: true}
}

func (r *cpUserRepository) GetByID(id uint) (*models.User, error) {
	if r.inTx {
		r.hooks.record("owner")
		if hook, _ := r.hooks.get(func(h *cpHooks) interface{} { return h.txGetByID }).(func(uint) error); hook != nil {
			if err := hook(id); err != nil {
				return nil, err
			}
		}
	}
	user, err := r.UserRepository.GetByID(id)
	if !r.inTx && err == nil {
		if hook, _ := r.hooks.get(func(h *cpHooks) interface{} { return h.afterRootGetByID }).(func(uint)); hook != nil {
			hook(id)
		}
	}
	return user, err
}

func (r *cpUserRepository) UpdateFields(id uint, fields map[string]interface{}) error {
	if hook, _ := r.hooks.get(func(h *cpHooks) interface{} { return h.updateFields }).(func(uint, map[string]interface{}) error); hook != nil {
		if err := hook(id, fields); err != nil {
			return err
		}
	}
	return r.UserRepository.UpdateFields(id, fields)
}

type cpRefreshRepository struct {
	repository.RefreshTokenRepository
	hooks *cpHooks
}

func (r *cpRefreshRepository) WithTx(tx *gorm.DB) repository.RefreshTokenRepository {
	return &cpRefreshRepository{RefreshTokenRepository: r.RefreshTokenRepository.WithTx(tx), hooks: r.hooks}
}

func (r *cpRefreshRepository) ClaimForRotation(id uint) error {
	r.hooks.record("claim")
	if hook, _ := r.hooks.get(func(h *cpHooks) interface{} { return h.claim }).(func(uint) error); hook != nil {
		if err := hook(id); err != nil {
			return err
		}
	}
	if err := r.RefreshTokenRepository.ClaimForRotation(id); err != nil {
		return err
	}
	if hook, _ := r.hooks.get(func(h *cpHooks) interface{} { return h.afterClaim }).(func(uint) error); hook != nil {
		return hook(id)
	}
	return nil
}

func (r *cpRefreshRepository) RevokeAllForUser(userID uint) error {
	if hook, _ := r.hooks.get(func(h *cpHooks) interface{} { return h.revokeAll }).(func(uint) error); hook != nil {
		if err := hook(userID); err != nil {
			return err
		}
	}
	return r.RefreshTokenRepository.RevokeAllForUser(userID)
}

func (r *cpRefreshRepository) Create(token *models.RefreshToken) error {
	if hook, _ := r.hooks.get(func(h *cpHooks) interface{} { return h.create }).(func(*models.RefreshToken) error); hook != nil {
		if err := hook(token); err != nil {
			return err
		}
	}
	return r.RefreshTokenRepository.Create(token)
}

// ----------------------------------------------------------------- fixture ---

type cpFixture struct {
	db          *gorm.DB
	refreshRepo repository.RefreshTokenRepository
	userRepo    repository.UserRepository
	hooks       *cpHooks
	svc         *authService
}

func newCPFixture(t *testing.T, db *gorm.DB) *cpFixture {
	t.Helper()
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	f := &cpFixture{
		db:          db,
		refreshRepo: repository.NewRefreshTokenRepository(db),
		userRepo:    repository.NewUserRepository(db),
		hooks:       &cpHooks{},
	}
	f.svc = NewAuthServiceWithSessions(
		&cpUserRepository{UserRepository: f.userRepo, hooks: f.hooks},
		repository.NewAPIKeyRepository(db),
		&cpRefreshRepository{RefreshTokenRepository: f.refreshRepo, hooks: f.hooks},
		repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db),
		&fakeFormMailer{},
		config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30},
		resetTestBaseURL, "").(*authService)
	return f
}

type cpSeed struct {
	owner, bystander, other *models.User
	oldJWT                  string
}

func (f *cpFixture) createUser(t *testing.T, email string) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(cpOldPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: email, Password: string(hash), FirstName: "Change", LastName: "Password",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(user).Error)
	return user
}

func (f *cpFixture) createSession(t *testing.T, userID uint, raw string, expires time.Time, revoked bool) {
	t.Helper()
	require.NoError(t, f.refreshRepo.Create(&models.RefreshToken{
		UserID: userID, TokenHash: hashOpaqueToken(raw, resetTestSecret), ExpiresAt: expires, Revoked: revoked,
	}))
}

// seed creates the owner with two live sessions (the presented one and a
// sibling), a bystander with one, and a third account holding cpForeign.
func (f *cpFixture) seed(t *testing.T) cpSeed {
	t.Helper()
	f.hooks.clear()
	ctx := resetContext(t)
	for _, table := range []string{"refresh_tokens", "api_keys", "users"} {
		require.NoError(t, f.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
	s := cpSeed{
		owner:     f.createUser(t, "owner@example.com"),
		bystander: f.createUser(t, "bystander@example.com"),
		other:     f.createUser(t, "other@example.com"),
	}
	later := time.Now().Add(24 * time.Hour)
	f.createSession(t, s.owner.ID, cpPresented, later, false)
	f.createSession(t, s.owner.ID, cpSibling, later, false)
	f.createSession(t, s.bystander.ID, cpBystander, later, false)
	f.createSession(t, s.other.ID, cpForeign, later, false)
	jwt, err := f.svc.GenerateJWT(s.owner)
	require.NoError(t, err)
	s.oldJWT = jwt
	return s
}

func (f *cpFixture) tokenState(t *testing.T, raw string) (exists, live bool) {
	t.Helper()
	var token models.RefreshToken
	err := f.db.WithContext(resetContext(t)).Where("token_hash = ?", hashOpaqueToken(raw, resetTestSecret)).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false
	}
	require.NoError(t, err)
	return true, !token.Revoked && token.ExpiresAt.After(time.Now())
}

func (f *cpFixture) live(t *testing.T, raw string) bool {
	_, live := f.tokenState(t, raw)
	return live
}

func (f *cpFixture) liveSessions(t *testing.T, userID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.RefreshToken{}).
		Where("user_id = ? AND is_revoked = ?", userID, false).Count(&n).Error)
	return n
}

func (f *cpFixture) sessions(t *testing.T, userID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.RefreshToken{}).
		Where("user_id = ?", userID).Count(&n).Error)
	return n
}

func (f *cpFixture) passwordIs(t *testing.T, userID uint, password string) bool {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&user, userID).Error)
	return bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) == nil
}

func (f *cpFixture) assertBystanderIntact(t *testing.T, s cpSeed) {
	t.Helper()
	assert.True(t, f.live(t, cpBystander), "the bystander keeps their session")
	assert.True(t, f.live(t, cpForeign), "the third account keeps its session")
	assert.True(t, f.passwordIs(t, s.bystander.ID, cpOldPassword))
}

// assertUntouched checks a rejected or failed change: old password, both
// owner sessions live, nothing inserted.
func (f *cpFixture) assertUntouched(t *testing.T, s cpSeed) {
	t.Helper()
	assert.True(t, f.passwordIs(t, s.owner.ID, cpOldPassword), "the old password must still be the committed one")
	assert.True(t, f.live(t, cpPresented), "the presented session must be untouched")
	assert.True(t, f.live(t, cpSibling), "the sibling session must be untouched")
	assert.Equal(t, int64(2), f.sessions(t, s.owner.ID), "no session may have been inserted")
	f.assertBystanderIntact(t, s)
}

// assertRenewed checks a committed change that renewed the presented session.
func (f *cpFixture) assertRenewed(t *testing.T, s cpSeed, tokens *AuthTokens) {
	t.Helper()
	require.NotNil(t, tokens)
	assert.True(t, f.passwordIs(t, s.owner.ID, cpNewPassword), "the new password must be the committed one")
	assert.False(t, f.live(t, cpPresented), "the presented token is spent")
	assert.False(t, f.live(t, cpSibling), "every other session loses renewal")
	assert.True(t, f.live(t, tokens.RefreshToken), "the replacement is the one live session")
	assert.Equal(t, int64(1), f.liveSessions(t, s.owner.ID))
	authenticated, err := f.svc.ValidateToken(tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, s.owner.ID, authenticated.ID)
	f.assertBystanderIntact(t, s)
}

// ------------------------------------------------------------------- roots ---

func TestChangePasswordValidationSQLite(t *testing.T) {
	runChangePasswordValidationScenarios(t, newCPFixture(t, openResetTestSQLite(t)))
}

func TestChangePasswordValidationMySQL(t *testing.T) {
	runChangePasswordValidationScenarios(t, newCPFixture(t, openResetTestMySQL(t, 2)))
}

func TestChangePasswordRenewalSQLite(t *testing.T) {
	runChangePasswordRenewalScenarios(t, newCPFixture(t, openResetTestSQLite(t)))
}

func TestChangePasswordRenewalMySQL(t *testing.T) {
	runChangePasswordRenewalScenarios(t, newCPFixture(t, openResetTestMySQL(t, 2)))
}

func TestChangePasswordAtomicitySQLite(t *testing.T) {
	runChangePasswordAtomicityScenarios(t, newCPFixture(t, openResetTestSQLite(t)))
}

func TestChangePasswordAtomicityMySQL(t *testing.T) {
	runChangePasswordAtomicityScenarios(t, newCPFixture(t, openResetTestMySQL(t, 2)))
}

func TestChangePasswordConcurrentRefreshSQLite(t *testing.T) {
	runChangePasswordConcurrentScenarios(t, newCPFixture(t, openResetTestSQLite(t)), false)
}

func TestChangePasswordConcurrentRefreshMySQL(t *testing.T) {
	runChangePasswordConcurrentScenarios(t, newCPFixture(t, openMigratedTestMySQL(t, 4)), true)
}

func TestRefreshRotationLocksOwnerBeforeClaimSQLite(t *testing.T) {
	f := newCPFixture(t, openResetTestSQLite(t))
	f.seed(t)
	f.hooks.clear()
	tokens, err := f.svc.RefreshAccessToken(cpPresented)
	require.NoError(t, err)
	require.NotNil(t, tokens)
	assert.Equal(t, []string{"owner", "claim"}, f.hooks.steps(),
		"the rotation must lock the owner row before claiming the token (users before refresh_tokens)")
}

// ------------------------------------------------------------- scenarios ---

func runChangePasswordValidationScenarios(t *testing.T, f *cpFixture) {
	t.Run("a wrong current password changes nothing", func(t *testing.T) {
		s := f.seed(t)
		tokens, err := f.svc.ChangePassword(s.owner.ID, "WrongSecret123!", cpNewPassword, cpPresented)
		assert.ErrorIs(t, err, ErrInvalidCurrentPassword)
		assert.Nil(t, tokens)
		f.assertUntouched(t, s)
	})

	t.Run("a weak new password changes nothing", func(t *testing.T) {
		s := f.seed(t)
		tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, "weakpassword", cpPresented)
		assert.Error(t, err)
		assert.Nil(t, tokens)
		f.assertUntouched(t, s)
	})

	t.Run("without a refresh token every session is revoked and nothing is issued", func(t *testing.T) {
		s := f.seed(t)
		tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, "")
		require.NoError(t, err)
		assert.Nil(t, tokens)
		assert.True(t, f.passwordIs(t, s.owner.ID, cpNewPassword))
		assert.Zero(t, f.liveSessions(t, s.owner.ID))
		assert.Equal(t, int64(2), f.sessions(t, s.owner.ID), "nothing is inserted")
		f.assertBystanderIntact(t, s)
	})

	stale := map[string]func(t *testing.T, s cpSeed) string{
		"unknown": func(*testing.T, cpSeed) string { return "never-issued" },
		"foreign": func(*testing.T, cpSeed) string { return cpForeign },
		"revoked": func(t *testing.T, s cpSeed) string {
			f.createSession(t, s.owner.ID, "owner-revoked", time.Now().Add(time.Hour), true)
			return "owner-revoked"
		},
		"expired": func(t *testing.T, s cpSeed) string {
			f.createSession(t, s.owner.ID, "owner-expired", time.Now().Add(-time.Minute), false)
			return "owner-expired"
		},
	}
	for name, presented := range stale {
		t.Run("a "+name+" refresh token is stale and changes nothing", func(t *testing.T) {
			s := f.seed(t)
			raw := presented(t, s)
			before := f.sessions(t, s.owner.ID)

			tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, raw)
			assert.ErrorIs(t, err, ErrSessionStale)
			assert.Nil(t, tokens)
			assert.True(t, f.passwordIs(t, s.owner.ID, cpOldPassword))
			assert.True(t, f.live(t, cpPresented))
			assert.True(t, f.live(t, cpSibling))
			assert.Equal(t, before, f.sessions(t, s.owner.ID))
			f.assertBystanderIntact(t, s)
		})
	}

	t.Run("an owner deactivated after the verification read is refused", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *cpHooks) {
			h.afterRootGetByID = func(id uint) {
				require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.User{}).
					Where("id = ?", id).Update("is_active", false).Error)
			}
		})
		tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		assert.ErrorIs(t, err, ErrAccountUnavailable)
		assert.Nil(t, tokens)
		assert.True(t, f.passwordIs(t, s.owner.ID, cpOldPassword))
		assert.True(t, f.live(t, cpPresented))
		assert.True(t, f.live(t, cpSibling))
	})

	t.Run("an owner erased after the verification read is refused and stays erased", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *cpHooks) {
			h.afterRootGetByID = func(id uint) { require.NoError(t, f.userRepo.Delete(id)) }
		})
		tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, "")
		assert.ErrorIs(t, err, ErrAccountUnavailable)
		assert.Nil(t, tokens)
		var row models.User
		require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&row, s.owner.ID).Error)
		assert.True(t, row.DeletedAt.Valid, "the erased row must stay soft-deleted")
		assert.False(t, f.passwordIs(t, s.owner.ID, cpNewPassword), "no password may be written to an erased row")
	})
}

func runChangePasswordRenewalScenarios(t *testing.T, f *cpFixture) {
	t.Run("the current session is renewed and every other session loses renewal", func(t *testing.T) {
		s := f.seed(t)
		tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		require.NoError(t, err)
		f.assertRenewed(t, s, tokens)

		// The replacement keeps renewing.
		rotated, err := f.svc.RefreshAccessToken(tokens.RefreshToken)
		require.NoError(t, err, "the renewed session must refresh after the change")
		assert.True(t, f.live(t, rotated.RefreshToken))

		// The pre-change current token and the sibling are dead.
		for _, raw := range []string{cpPresented, cpSibling} {
			replayed, err := f.svc.RefreshAccessToken(raw)
			assert.Nil(t, replayed)
			assert.ErrorIs(t, err, ErrInvalidRefreshToken, "refresh with %s must be rejected", raw)
		}

		// Only the new password signs in.
		_, err = f.svc.LoginWithTokens("owner@example.com", cpOldPassword)
		assert.Error(t, err)
		login, err := f.svc.LoginWithTokens("owner@example.com", cpNewPassword)
		require.NoError(t, err)
		assert.NotEmpty(t, login.AccessToken)
	})

	t.Run("an access token issued before the change keeps authorising until it expires", func(t *testing.T) {
		// Documented boundary: revocation ends renewal, not access. There is
		// no blocklist; only owner state (deactivation, erasure) rejects an
		// unexpired JWT early.
		s := f.seed(t)
		_, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		require.NoError(t, err)

		user, err := f.svc.ValidateToken(s.oldJWT)
		require.NoError(t, err, "the old access token still authorises")
		assert.Equal(t, s.owner.ID, user.ID)
		_, err = f.svc.RefreshAccessToken(cpSibling)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken, "but its session can no longer renew")

		require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.User{}).
			Where("id = ?", s.owner.ID).Update("is_active", false).Error)
		_, err = f.svc.ValidateToken(s.oldJWT)
		assert.Error(t, err, "deactivation still rejects the old access token before it expires")
	})
}

func runChangePasswordAtomicityScenarios(t *testing.T, f *cpFixture) {
	injected := errors.New("injected failure")
	steps := map[string]func(h *cpHooks){
		"locking owner read": func(h *cpHooks) { h.txGetByID = func(uint) error { return injected } },
		"token claim":        func(h *cpHooks) { h.claim = func(uint) error { return injected } },
		"password write": func(h *cpHooks) {
			h.updateFields = func(uint, map[string]interface{}) error { return injected }
		},
		"revocation of the other sessions": func(h *cpHooks) { h.revokeAll = func(uint) error { return injected } },
		"successor insert":                 func(h *cpHooks) { h.create = func(*models.RefreshToken) error { return injected } },
	}
	for name, install := range steps {
		t.Run("a failure at the "+name+" rolls everything back", func(t *testing.T) {
			s := f.seed(t)
			f.hooks.install(install)

			tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
			assert.ErrorIs(t, err, injected)
			assert.Nil(t, tokens)
			f.assertUntouched(t, s)

			f.hooks.clear()
			tokens, err = f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
			require.NoError(t, err, "the same request succeeds once the failure is gone")
			f.assertRenewed(t, s, tokens)
		})
	}

	t.Run("a failure signing the access token rolls everything back", func(t *testing.T) {
		s := f.seed(t)
		f.svc.signAccessToken = func(*models.User) (string, error) { return "", injected }
		tokens, err := f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		f.svc.signAccessToken = nil
		assert.ErrorIs(t, err, injected)
		assert.Nil(t, tokens)
		f.assertUntouched(t, s)

		tokens, err = f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		require.NoError(t, err)
		f.assertRenewed(t, s, tokens)
	})
}

// runChangePasswordConcurrentScenarios forces each ordering of a password
// change against a refresh: the first request is held inside its
// transaction at a hook, the second is started and given time to run into
// the first one's locks, then the first is released. On SQLite the single
// pooled connection serialises the two; on MySQL/MariaDB they contend on row
// locks, and a lock-order inversion between them is a deadlock.
func runChangePasswordConcurrentScenarios(t *testing.T, f *cpFixture, contended bool) {
	type result struct {
		tokens *AuthTokens
		err    error
	}
	start := func(fn func() (*AuthTokens, error)) <-chan result {
		ch := make(chan result, 1)
		go func() {
			tokens, err := fn()
			ch <- result{tokens, err}
		}()
		return ch
	}
	await := func(t *testing.T, ch <-chan result, what string) result {
		t.Helper()
		select {
		case r := <-ch:
			return r
		case <-time.After(resetTimeout):
			t.Fatalf("%s did not finish within %s", what, resetTimeout)
			return result{}
		}
	}
	// hold returns a hook body that parks the first request and a release
	// function; entered is closed once the request reached the hook.
	hold := func() (wait func(), entered <-chan struct{}, release func()) {
		in := make(chan struct{})
		out := make(chan struct{})
		var once sync.Once
		return func() {
				once.Do(func() { close(in) })
				select {
				case <-out:
				case <-time.After(resetTimeout):
				}
			}, in, func() {
				select {
				case <-out:
				default:
					close(out)
				}
			}
	}
	enter := func(t *testing.T, entered <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(resetTimeout):
			t.Fatalf("%s never reached its hold point", what)
		}
	}
	grace := func(ch <-chan result) {
		if !contended {
			return
		}
		// Bounded: the second request either finishes (it must not, when it
		// needs a lock the first one holds) or runs into that lock.
		select {
		case <-ch:
		case <-time.After(cpGrace):
		}
	}
	tokenID := func(t *testing.T, raw string) uint {
		var token models.RefreshToken
		require.NoError(t, f.db.WithContext(resetContext(t)).Where("token_hash = ?", hashOpaqueToken(raw, resetTestSecret)).First(&token).Error)
		return token.ID
	}

	t.Run("a sibling refresh that commits first loses its successor to the change", func(t *testing.T) {
		s := f.seed(t)
		siblingID := tokenID(t, cpSibling)
		wait, entered, release := hold()
		defer release()
		f.hooks.install(func(h *cpHooks) {
			h.afterClaim = func(id uint) error {
				if id == siblingID {
					wait()
				}
				return nil
			}
		})
		refresh := start(func() (*AuthTokens, error) { return f.svc.RefreshAccessToken(cpSibling) })
		enter(t, entered, "the sibling refresh")
		change := start(func() (*AuthTokens, error) {
			return f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		})
		grace(change)
		release()

		r := await(t, refresh, "the sibling refresh")
		c := await(t, change, "the password change")
		require.NoError(t, r.err, "the refresh committed first and must succeed")
		require.NoError(t, c.err, "a sibling refresh must never make the password change fail")
		assert.False(t, f.live(t, r.tokens.RefreshToken), "the change revokes the successor the refresh committed")
		f.assertRenewed(t, s, c.tokens)
	})

	t.Run("a sibling refresh that arrives during the change is rejected", func(t *testing.T) {
		s := f.seed(t)
		wait, entered, release := hold()
		defer release()
		f.hooks.install(func(h *cpHooks) {
			h.revokeAll = func(uint) error { wait(); return nil }
		})
		change := start(func() (*AuthTokens, error) {
			return f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		})
		enter(t, entered, "the password change")
		refresh := start(func() (*AuthTokens, error) { return f.svc.RefreshAccessToken(cpSibling) })
		grace(refresh)
		release()

		c := await(t, change, "the password change")
		r := await(t, refresh, "the sibling refresh")
		require.NoError(t, c.err, "a sibling refresh must never make the password change fail")
		assert.Nil(t, r.tokens)
		assert.ErrorIs(t, r.err, ErrInvalidRefreshToken, "the change committed first; the sibling cannot renew")
		f.assertRenewed(t, s, c.tokens)
	})

	t.Run("a refresh of the presented token that commits first makes the change stale", func(t *testing.T) {
		s := f.seed(t)
		presentedID := tokenID(t, cpPresented)
		wait, entered, release := hold()
		defer release()
		f.hooks.install(func(h *cpHooks) {
			h.afterClaim = func(id uint) error {
				if id == presentedID {
					wait()
				}
				return nil
			}
		})
		refresh := start(func() (*AuthTokens, error) { return f.svc.RefreshAccessToken(cpPresented) })
		enter(t, entered, "the refresh")
		f.hooks.install(func(h *cpHooks) { h.afterClaim = nil })
		change := start(func() (*AuthTokens, error) {
			return f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		})
		grace(change)
		release()

		r := await(t, refresh, "the refresh")
		c := await(t, change, "the password change")
		require.NoError(t, r.err)
		assert.Nil(t, c.tokens)
		assert.ErrorIs(t, c.err, ErrSessionStale, "the token was spent by the refresh")
		assert.True(t, f.passwordIs(t, s.owner.ID, cpOldPassword), "a stale change writes nothing")
		assert.True(t, f.live(t, r.tokens.RefreshToken), "the refreshed session stays renewable")
		assert.True(t, f.live(t, cpSibling))
	})

	t.Run("a refresh of the presented token during the change is rejected", func(t *testing.T) {
		s := f.seed(t)
		wait, entered, release := hold()
		defer release()
		f.hooks.install(func(h *cpHooks) {
			h.revokeAll = func(uint) error { wait(); return nil }
		})
		change := start(func() (*AuthTokens, error) {
			return f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		})
		enter(t, entered, "the password change")
		refresh := start(func() (*AuthTokens, error) { return f.svc.RefreshAccessToken(cpPresented) })
		grace(refresh)
		release()

		c := await(t, change, "the password change")
		r := await(t, refresh, "the refresh")
		require.NoError(t, c.err)
		assert.Nil(t, r.tokens)
		assert.ErrorIs(t, r.err, ErrInvalidRefreshToken)
		f.assertRenewed(t, s, c.tokens)
	})

	t.Run("of two concurrent changes with the same current password exactly one wins", func(t *testing.T) {
		s := f.seed(t)
		wait, entered, release := hold()
		defer release()
		f.hooks.install(func(h *cpHooks) {
			h.revokeAll = func(uint) error { wait(); return nil }
		})
		first := start(func() (*AuthTokens, error) {
			return f.svc.ChangePassword(s.owner.ID, cpOldPassword, cpNewPassword, cpPresented)
		})
		enter(t, entered, "the first change")
		f.hooks.install(func(h *cpHooks) { h.revokeAll = nil })
		second := start(func() (*AuthTokens, error) {
			return f.svc.ChangePassword(s.owner.ID, cpOldPassword, "AnotherPass12!", cpSibling)
		})
		grace(second)
		release()

		a := await(t, first, "the first change")
		b := await(t, second, "the second change")
		require.NoError(t, a.err)
		assert.Nil(t, b.tokens)
		assert.True(t, errors.Is(b.err, ErrInvalidCurrentPassword) || errors.Is(b.err, ErrSessionStale),
			"the second change must be refused, got %v", b.err)
		f.assertRenewed(t, s, a.tokens)
		assert.False(t, f.passwordIs(t, s.owner.ID, "AnotherPass12!"))
	})
}

// openMigratedTestMySQL builds a disposable schema with the real migrations,
// so the refresh_tokens foreign key to users and the production indexes take
// part in the locking exactly as they do in production. Every connection uses
// the server's default isolation, which the root checks is REPEATABLE READ.
func openMigratedTestMySQL(t *testing.T, maxConns int) *gorm.DB {
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
	name := fmt.Sprintf("gocrm_pwchange_%d_%d", time.Now().UnixNano(), resetTestDBSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")

	cfg.DBName = name
	migrateCfg := cfg.Clone()
	migrateCfg.MultiStatements = true
	migrationDB, err := sql.Open("mysql", migrateCfg.FormatDSN())
	require.NoError(t, err)
	driver, err := migratemysql.WithInstance(migrationDB, &migratemysql.Config{})
	require.NoError(t, err)
	path, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://"+path, "mysql", driver)
	require.NoError(t, err)
	require.NoError(t, m.Up())
	srcErr, dbErr := m.Close()
	require.NoError(t, srcErr)
	require.NoError(t, dbErr)

	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), resetTestGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// MySQL 8 names the variable transaction_isolation; MariaDB 10.11 only
	// knows tx_isolation.
	var isolation string
	if err := db.Raw("SELECT @@transaction_isolation").Scan(&isolation).Error; err != nil || isolation == "" {
		require.NoError(t, db.Raw("SELECT @@tx_isolation").Scan(&isolation).Error)
	}
	require.Equal(t, "REPEATABLE-READ", isolation, "the lock-order roots assume the production isolation level")

	var fks int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA = ? AND TABLE_NAME = 'refresh_tokens' AND REFERENCED_TABLE_NAME = 'users'`, name).
		Scan(&fks).Error)
	require.Equal(t, int64(1), fks, "the migrated schema must carry the refresh_tokens -> users foreign key")
	return db
}
