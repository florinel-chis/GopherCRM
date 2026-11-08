package service

// Regression roots for issue #236: an access JWT carries the owner's
// users.token_version as the claim token_version, ValidateToken refuses a
// token whose claim is no longer the stored value, and logout, the password
// change and the password-reset confirmation increment that value inside the
// transactions they already run. Refresh rotation and login leave it alone.
//
// The roots run on real databases: in-memory SQLite with the production
// one-connection pool, and disposable MySQL/MariaDB schemas when
// TEST_MYSQL_DSN is set (the MySQL roots skip without it and fail when it
// points at an unreachable server). The lock-order root on MySQL uses a schema
// built by the real migrations and forces both orderings of a logout against
// a refresh rotation of the same user. Every assertion reads committed rows
// outside any transaction; every wait is bounded.

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	atvPassword    = "OldSecret123!"
	atvNewPassword = "BrandNewPass1!"
	atvPresented   = "atv-presented-session"
	atvSibling     = "atv-sibling-session"
	atvBystander   = "atv-bystander-session"
	atvForeign     = "atv-foreign-session"
	atvOwnerEmail  = "atv-owner@example.com"
	atvLockPoll    = 10 * time.Millisecond
)

// ------------------------------------------------------------------- hooks ---

type atvHookSet struct {
	// afterRootRead fires after a plain owner read outside any transaction
	// (the verification read of a password change, or ValidateToken).
	afterRootRead func(id uint)
	// beforeLock fires before the transaction-bound (locking) owner read.
	beforeLock   func(id uint) error
	updateFields func(id uint, fields map[string]interface{}) error
	revokeAll    func(userID uint) error
	revoke       func(hash string) error
	afterClaim   func(id uint) error
}

// atvHooks interposes on the repositories the service writes through and
// records the order of the transaction-bound statements. Access is
// mutex-guarded because the concurrency roots fire hooks from several
// goroutines.
type atvHooks struct {
	mu    sync.Mutex
	steps []string
	set   atvHookSet
}

func (h *atvHooks) install(fn func(s *atvHookSet)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(&h.set)
}

func (h *atvHooks) current() atvHookSet {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.set
}

func (h *atvHooks) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.set = atvHookSet{}
	h.steps = nil
}

func (h *atvHooks) record(step string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steps = append(h.steps, step)
}

func (h *atvHooks) recorded() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.steps...)
}

type atvUserRepository struct {
	repository.UserRepository
	hooks *atvHooks
	inTx  bool
}

func (r *atvUserRepository) WithTx(tx *gorm.DB) repository.UserRepository {
	return &atvUserRepository{UserRepository: r.UserRepository.WithTx(tx), hooks: r.hooks, inTx: true}
}

func (r *atvUserRepository) GetByID(id uint) (*models.User, error) {
	if r.inTx {
		if hook := r.hooks.current().beforeLock; hook != nil {
			if err := hook(id); err != nil {
				return nil, err
			}
		}
		r.hooks.record("users:lock")
		return r.UserRepository.GetByID(id)
	}
	user, err := r.UserRepository.GetByID(id)
	if err == nil {
		if hook := r.hooks.current().afterRootRead; hook != nil {
			hook(id)
		}
	}
	return user, err
}

func (r *atvUserRepository) UpdateFields(id uint, fields map[string]interface{}) error {
	if r.inTx {
		r.hooks.record("users:update")
	}
	if hook := r.hooks.current().updateFields; hook != nil {
		if err := hook(id, fields); err != nil {
			return err
		}
	}
	return r.UserRepository.UpdateFields(id, fields)
}

type atvRefreshRepository struct {
	repository.RefreshTokenRepository
	hooks *atvHooks
}

func (r *atvRefreshRepository) WithTx(tx *gorm.DB) repository.RefreshTokenRepository {
	return &atvRefreshRepository{RefreshTokenRepository: r.RefreshTokenRepository.WithTx(tx), hooks: r.hooks}
}

func (r *atvRefreshRepository) GetByTokenHash(hash string) (*models.RefreshToken, error) {
	r.hooks.record("refresh_tokens:read")
	return r.RefreshTokenRepository.GetByTokenHash(hash)
}

func (r *atvRefreshRepository) RevokeAllForUser(userID uint) error {
	r.hooks.record("refresh_tokens:revoke_all")
	if hook := r.hooks.current().revokeAll; hook != nil {
		if err := hook(userID); err != nil {
			return err
		}
	}
	return r.RefreshTokenRepository.RevokeAllForUser(userID)
}

func (r *atvRefreshRepository) RevokeByTokenHash(hash string) error {
	r.hooks.record("refresh_tokens:revoke")
	if hook := r.hooks.current().revoke; hook != nil {
		if err := hook(hash); err != nil {
			return err
		}
	}
	return r.RefreshTokenRepository.RevokeByTokenHash(hash)
}

func (r *atvRefreshRepository) ClaimForRotation(id uint) error {
	r.hooks.record("refresh_tokens:claim")
	if err := r.RefreshTokenRepository.ClaimForRotation(id); err != nil {
		return err
	}
	if hook := r.hooks.current().afterClaim; hook != nil {
		return hook(id)
	}
	return nil
}

func (r *atvRefreshRepository) Create(token *models.RefreshToken) error {
	r.hooks.record("refresh_tokens:create")
	return r.RefreshTokenRepository.Create(token)
}

// ----------------------------------------------------------------- fixture ---

type atvFixture struct {
	db          *gorm.DB
	userRepo    repository.UserRepository
	refreshRepo repository.RefreshTokenRepository
	hooks       *atvHooks
	mailer      *fakeFormMailer
	svc         *authService
}

func newATVFixture(t *testing.T, db *gorm.DB) *atvFixture {
	t.Helper()
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	f := &atvFixture{
		db:          db,
		userRepo:    repository.NewUserRepository(db),
		refreshRepo: repository.NewRefreshTokenRepository(db),
		hooks:       &atvHooks{},
		mailer:      &fakeFormMailer{},
	}
	f.svc = NewAuthServiceWithSessions(
		&atvUserRepository{UserRepository: f.userRepo, hooks: f.hooks},
		repository.NewAPIKeyRepository(db),
		&atvRefreshRepository{RefreshTokenRepository: f.refreshRepo, hooks: f.hooks},
		repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db),
		f.mailer,
		config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30},
		resetTestBaseURL, "").(*authService)
	return f
}

type atvSeed struct {
	owner, bystander, other *models.User
}

func (f *atvFixture) createUser(t *testing.T, email string) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(atvPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: email, Password: string(hash), FirstName: "Token", LastName: "Version",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(user).Error)
	return user
}

func (f *atvFixture) createSession(t *testing.T, userID uint, raw string) {
	t.Helper()
	require.NoError(t, f.refreshRepo.Create(&models.RefreshToken{
		UserID: userID, TokenHash: hashOpaqueToken(raw, resetTestSecret), ExpiresAt: time.Now().Add(24 * time.Hour),
	}))
}

// seed empties the tables and creates the owner with two live sessions, a
// bystander with one, and a third account holding atvForeign.
func (f *atvFixture) seed(t *testing.T) atvSeed {
	t.Helper()
	f.hooks.reset()
	f.mailer.reset()
	f.svc.signAccessToken = nil
	ctx := resetContext(t)
	for _, table := range []string{"password_reset_tokens", "refresh_tokens", "api_keys", "users"} {
		require.NoError(t, f.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
	s := atvSeed{
		owner:     f.createUser(t, atvOwnerEmail),
		bystander: f.createUser(t, "atv-bystander@example.com"),
		other:     f.createUser(t, "atv-other@example.com"),
	}
	f.createSession(t, s.owner.ID, atvPresented)
	f.createSession(t, s.owner.ID, atvSibling)
	f.createSession(t, s.bystander.ID, atvBystander)
	f.createSession(t, s.other.ID, atvForeign)
	return s
}

// version reads the committed users.token_version, erased rows included.
func (f *atvFixture) version(t *testing.T, userID uint) uint {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&user, userID).Error)
	return user.TokenVersion
}

func (f *atvFixture) setVersion(t *testing.T, userID uint, value uint) {
	t.Helper()
	require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.User{}).
		Where("id = ?", userID).Update("token_version", value).Error)
}

// jwt signs an access token for the account as it is currently committed.
func (f *atvFixture) jwt(t *testing.T, userID uint) string {
	t.Helper()
	user, err := f.userRepo.GetByID(userID)
	require.NoError(t, err)
	token, err := f.svc.GenerateJWT(user)
	require.NoError(t, err)
	return token
}

func (f *atvFixture) live(t *testing.T, raw string) bool {
	t.Helper()
	var token models.RefreshToken
	err := f.db.WithContext(resetContext(t)).Where("token_hash = ?", hashOpaqueToken(raw, resetTestSecret)).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	require.NoError(t, err)
	return !token.Revoked && token.ExpiresAt.After(time.Now())
}

func (f *atvFixture) assertAccepted(t *testing.T, token string, userID uint, msgAndArgs ...interface{}) {
	t.Helper()
	user, err := f.svc.ValidateToken(token)
	if assert.NoError(t, err, msgAndArgs...) {
		assert.Equal(t, userID, user.ID, msgAndArgs...)
	}
}

func (f *atvFixture) assertRevoked(t *testing.T, token string, msgAndArgs ...interface{}) {
	t.Helper()
	user, err := f.svc.ValidateToken(token)
	assert.Nil(t, user, msgAndArgs...)
	assert.ErrorIs(t, err, ErrAccessTokenRevoked, msgAndArgs...)
}

// issueLink requests a reset through the service and returns the raw token.
func (f *atvFixture) issueLink(t *testing.T, user *models.User) string {
	t.Helper()
	f.mailer.reset()
	require.NoError(t, f.svc.RequestPasswordReset(user.Email))
	messages := f.mailer.messages()
	require.Len(t, messages, 1)
	idx := strings.Index(messages[0].Body, "token=")
	require.Positive(t, idx)
	f.mailer.reset()
	return messages[0].Body[idx+len("token="):]
}

// signClaims signs arbitrary claims with the fixture's secret, for the shapes
// GenerateJWT never produces.
func signClaims(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(resetTestSecret))
	require.NoError(t, err)
	return token
}

// claimOf reads the token_version claim of a token the fixture signed.
func claimOf(t *testing.T, token string) interface{} {
	t.Helper()
	parsed, err := jwt.Parse(token, func(*jwt.Token) (interface{}, error) { return []byte(resetTestSecret), nil })
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	value, present := claims["token_version"]
	require.True(t, present, "every access token carries token_version")
	return value
}

// ------------------------------------------------------------------- AC-1 ---

func TestAccessTokenVersionClaim(t *testing.T) {
	f := newATVFixture(t, openResetTestSQLite(t))

	t.Run("GenerateJWT signs the stored value and ValidateToken accepts it", func(t *testing.T) {
		s := f.seed(t)
		for _, stored := range []uint{0, 1, 7, maxTokenVersionClaim} {
			f.setVersion(t, s.owner.ID, stored)
			token := f.jwt(t, s.owner.ID)
			assert.Equal(t, float64(stored), claimOf(t, token), "the claim is the integer %d", stored)
			f.assertAccepted(t, token, s.owner.ID, "stored %d", stored)
		}
	})

	t.Run("a claim that differs from the stored value is revoked", func(t *testing.T) {
		s := f.seed(t)
		f.setVersion(t, s.owner.ID, 3)
		exp := time.Now().Add(time.Hour).Unix()
		for _, claimed := range []interface{}{0, 2, 4, uint64(maxTokenVersionClaim)} {
			token := signClaims(t, jwt.MapClaims{"user_id": s.owner.ID, "exp": exp, "token_version": claimed})
			f.assertRevoked(t, token, "claim %v against stored 3", claimed)
		}
	})

	t.Run("an absent claim reads as 0", func(t *testing.T) {
		s := f.seed(t)
		legacy := signClaims(t, jwt.MapClaims{"user_id": s.owner.ID, "email": s.owner.Email,
			"role": s.owner.Role, "exp": time.Now().Add(time.Hour).Unix()})
		f.assertAccepted(t, legacy, s.owner.ID, "a token signed before the claim existed is version 0")
		f.setVersion(t, s.owner.ID, 1)
		f.assertRevoked(t, legacy, "and is revoked by the first increment")
	})

	t.Run("a malformed claim is rejected before the user is returned", func(t *testing.T) {
		s := f.seed(t)
		exp := time.Now().Add(time.Hour).Unix()
		malformed := []struct {
			name    string
			claimed interface{}
		}{
			{"a string", "0"},
			{"a boolean", false},
			{"null", nil},
			{"an object", map[string]interface{}{"v": 0}},
			{"an array", []int{0}},
			{"a fraction", 0.5},
			{"a fraction above 1", 1.25},
			{"negative", -1},
			{"a negative fraction", -0.5},
			{"2^53", uint64(1 << 53)},
			{"2^53+1", uint64(1<<53 + 1)},
			{"2^64-1", uint64(math.MaxUint64)},
			{"1e300", 1e300},
		}
		for _, tc := range malformed {
			token := signClaims(t, jwt.MapClaims{"user_id": s.owner.ID, "exp": exp, "token_version": tc.claimed})
			user, err := f.svc.ValidateToken(token)
			assert.Nil(t, user, tc.name)
			assert.Error(t, err, tc.name)
			assert.NotErrorIs(t, err, ErrAccessTokenRevoked, "%s is malformed, not merely stale", tc.name)
		}
	})

	t.Run("the claim helper checks the shape before converting", func(t *testing.T) {
		valid := []struct {
			raw  interface{}
			want uint
		}{
			{float64(0), 0},
			{float64(12), 12},
			{float64(maxTokenVersionClaim), maxTokenVersionClaim},
			{json.Number("5"), 5},
			{json.Number("9007199254740991"), maxTokenVersionClaim},
			{json.Number("2.0"), 2},
		}
		for _, tc := range valid {
			got, err := accessTokenVersion(jwt.MapClaims{"token_version": tc.raw})
			if assert.NoError(t, err, "%v", tc.raw) {
				assert.Equal(t, tc.want, got, "%v", tc.raw)
			}
		}
		got, err := accessTokenVersion(jwt.MapClaims{})
		require.NoError(t, err)
		assert.Zero(t, got, "absent reads as 0")

		for _, raw := range []interface{}{
			"1", true, nil, float64(-1), 1.5, float64(maxTokenVersionClaim + 1), math.Inf(1), math.NaN(),
			json.Number("-1"), json.Number("9007199254740992"), json.Number("18446744073709551615"),
			json.Number("1.5"), json.Number("abc"), int64(3), uint(3),
		} {
			_, err := accessTokenVersion(jwt.MapClaims{"token_version": raw})
			assert.ErrorIs(t, err, errInvalidTokenVersionClaim, "%#v", raw)
		}
	})

	t.Run("the existing rejections are unchanged", func(t *testing.T) {
		s := f.seed(t)
		exp := time.Now().Add(time.Hour).Unix()

		forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": s.owner.ID, "exp": exp, "token_version": 0}).
			SignedString([]byte("another-secret-that-is-long-enough-1234"))
		require.NoError(t, err)
		user, err := f.svc.ValidateToken(forged)
		assert.Nil(t, user)
		assert.Error(t, err, "a bad signature")
		assert.NotErrorIs(t, err, ErrAccessTokenRevoked)

		expired := signClaims(t, jwt.MapClaims{"user_id": s.owner.ID, "exp": time.Now().Add(-time.Minute).Unix(), "token_version": 0})
		user, err = f.svc.ValidateToken(expired)
		assert.Nil(t, user)
		assert.ErrorIs(t, err, jwt.ErrTokenExpired)

		missing := signClaims(t, jwt.MapClaims{"user_id": 999999, "exp": exp, "token_version": 0})
		user, err = f.svc.ValidateToken(missing)
		assert.Nil(t, user)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "a missing user")

		token := f.jwt(t, s.owner.ID)
		require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.User{}).
			Where("id = ?", s.owner.ID).Update("is_active", false).Error)
		user, err = f.svc.ValidateToken(token)
		assert.Nil(t, user)
		assert.Error(t, err, "a deactivated user")
		assert.NotErrorIs(t, err, ErrAccessTokenRevoked)

		erasedToken := f.jwt(t, s.bystander.ID)
		require.NoError(t, f.db.WithContext(resetContext(t)).Delete(&models.User{}, s.bystander.ID).Error)
		user, err = f.svc.ValidateToken(erasedToken)
		assert.Nil(t, user)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "an erased user")
	})
}

// ------------------------------------------------------------------- AC-2 ---

func TestAccessTokenRevokedByLogoutSQLite(t *testing.T) {
	runAccessTokenLogoutScenarios(t, newATVFixture(t, openResetTestSQLite(t)))
}

func TestAccessTokenRevokedByLogoutMySQL(t *testing.T) {
	runAccessTokenLogoutScenarios(t, newATVFixture(t, openResetTestMySQL(t, 2)))
}

func runAccessTokenLogoutScenarios(t *testing.T, f *atvFixture) {
	t.Run("a logout without a refresh token ends every earlier access token and every session", func(t *testing.T) {
		s := f.seed(t)
		first, second := f.jwt(t, s.owner.ID), f.jwt(t, s.owner.ID)
		bystander := f.jwt(t, s.bystander.ID)
		f.assertAccepted(t, first, s.owner.ID)

		require.NoError(t, f.svc.Logout(s.owner.ID, ""))

		assert.Equal(t, uint(1), f.version(t, s.owner.ID), "incremented by exactly 1")
		f.assertRevoked(t, first)
		f.assertRevoked(t, second)
		assert.False(t, f.live(t, atvPresented))
		assert.False(t, f.live(t, atvSibling))
		after := f.jwt(t, s.owner.ID)
		f.assertAccepted(t, after, s.owner.ID, "a token issued after the logout passes")
		assert.Equal(t, float64(1), claimOf(t, after))

		assert.Equal(t, uint(0), f.version(t, s.bystander.ID), "another account is untouched")
		f.assertAccepted(t, bystander, s.bystander.ID)
		assert.True(t, f.live(t, atvBystander))
		assert.True(t, f.live(t, atvForeign))
	})

	t.Run("a logout with the caller's own refresh token revokes only that session and ends earlier access tokens", func(t *testing.T) {
		s := f.seed(t)
		before := f.jwt(t, s.owner.ID)
		require.NoError(t, f.svc.Logout(s.owner.ID, atvPresented))
		assert.Equal(t, uint(1), f.version(t, s.owner.ID))
		f.assertRevoked(t, before)
		assert.False(t, f.live(t, atvPresented))
		assert.True(t, f.live(t, atvSibling), "only the presented session is revoked")
		f.assertAccepted(t, f.jwt(t, s.owner.ID), s.owner.ID)
	})

	others := []struct {
		name      string
		presented string
	}{
		{"another user's", atvForeign},
		{"an unknown", "never-issued"},
		{"an already-revoked", ""},
	}
	for _, tc := range others {
		t.Run("a logout presenting "+tc.name+" refresh token still increments once and revokes nothing else", func(t *testing.T) {
			s := f.seed(t)
			raw := tc.presented
			if raw == "" {
				raw = atvPresented
				require.NoError(t, f.refreshRepo.RevokeByTokenHash(hashOpaqueToken(raw, resetTestSecret)))
			}
			before := f.jwt(t, s.owner.ID)
			require.NoError(t, f.svc.Logout(s.owner.ID, raw))
			assert.Equal(t, uint(1), f.version(t, s.owner.ID))
			f.assertRevoked(t, before)
			assert.True(t, f.live(t, atvSibling))
			assert.True(t, f.live(t, atvForeign), "a foreign session is never revoked")
			assert.Equal(t, uint(0), f.version(t, s.other.ID))
		})
	}

	t.Run("API-key and Bearer callers reach the same increment", func(t *testing.T) {
		// The service takes no authentication method: POST /auth/logout passes
		// the caller's id whichever credential authenticated it (the HTTP root
		// exercises the API-key route). Two logouts are two increments.
		s := f.seed(t)
		require.NoError(t, f.svc.Logout(s.owner.ID, ""))
		require.NoError(t, f.svc.Logout(s.owner.ID, ""))
		assert.Equal(t, uint(2), f.version(t, s.owner.ID))
	})

	injected := errors.New("injected failure")
	failInc := func(s *atvHookSet) {
		s.updateFields = func(uint, map[string]interface{}) error { return injected }
	}
	failures := []struct {
		name      string
		presented string
		install   func(s *atvHookSet)
	}{
		{"revoke-all", "", func(s *atvHookSet) { s.revokeAll = func(uint) error { return injected } }},
		{"single revoke", atvPresented, func(s *atvHookSet) { s.revoke = func(string) error { return injected } }},
		{"increment", "", failInc},
		{"increment after a single revoke", atvPresented, failInc},
		{"locking read", "", func(s *atvHookSet) { s.beforeLock = func(uint) error { return injected } }},
	}
	for _, failure := range failures {
		t.Run("a failure of the "+failure.name+" rolls the whole logout back", func(t *testing.T) {
			s := f.seed(t)
			before := f.jwt(t, s.owner.ID)
			f.hooks.install(failure.install)

			err := f.svc.Logout(s.owner.ID, failure.presented)
			assert.ErrorIs(t, err, injected)
			assert.Equal(t, uint(0), f.version(t, s.owner.ID), "the increment is rolled back")
			assert.True(t, f.live(t, atvPresented), "the revocation is rolled back")
			assert.True(t, f.live(t, atvSibling))
			f.assertAccepted(t, before, s.owner.ID, "the earlier token keeps passing")

			f.hooks.reset()
			require.NoError(t, f.svc.Logout(s.owner.ID, failure.presented), "the same logout succeeds once the fault is gone")
			assert.Equal(t, uint(1), f.version(t, s.owner.ID))
			f.assertRevoked(t, before)
		})
	}

	t.Run("an erased user's logout returns nil and writes nothing", func(t *testing.T) {
		s := f.seed(t)
		f.setVersion(t, s.owner.ID, 4)
		require.NoError(t, f.db.WithContext(resetContext(t)).Delete(&models.User{}, s.owner.ID).Error)

		assert.NoError(t, f.svc.Logout(s.owner.ID, ""))
		assert.NoError(t, f.svc.Logout(s.owner.ID, atvPresented))
		assert.Equal(t, uint(4), f.version(t, s.owner.ID), "nothing is written")
		assert.True(t, f.live(t, atvPresented), "no session is revoked")
		assert.True(t, f.live(t, atvSibling))
	})
}

func TestLogoutRequiresSessionStore(t *testing.T) {
	f := newATVFixture(t, openResetTestSQLite(t))
	s := f.seed(t)
	before := f.jwt(t, s.owner.ID)
	cfg := config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30}

	for name, svc := range map[string]AuthService{
		"no transaction manager": NewAuthServiceWithSessions(f.userRepo, repository.NewAPIKeyRepository(f.db),
			f.refreshRepo, repository.NewPasswordResetTokenRepository(f.db), nil, f.mailer, cfg, resetTestBaseURL, ""),
		"no refresh-token repository": NewAuthServiceWithSessions(f.userRepo, repository.NewAPIKeyRepository(f.db),
			nil, repository.NewPasswordResetTokenRepository(f.db), utils.NewTransactionManager(f.db), f.mailer, cfg, resetTestBaseURL, ""),
		"stateless JWT service": NewAuthService(f.userRepo, repository.NewAPIKeyRepository(f.db), cfg),
	} {
		t.Run(name, func(t *testing.T) {
			for _, presented := range []string{"", atvPresented} {
				err := svc.Logout(s.owner.ID, presented)
				assert.Error(t, err, "logout is refused, not run half-way (presented %q)", presented)
			}
			assert.Equal(t, uint(0), f.version(t, s.owner.ID), "nothing is written")
			assert.True(t, f.live(t, atvPresented))
			assert.True(t, f.live(t, atvSibling))
			f.assertAccepted(t, before, s.owner.ID)
		})
	}
}

// ------------------------------------------------------------------- AC-3 ---

func TestAccessTokenRevokedByPasswordChangeSQLite(t *testing.T) {
	runAccessTokenPasswordChangeScenarios(t, newATVFixture(t, openResetTestSQLite(t)))
}

func TestAccessTokenRevokedByPasswordChangeMySQL(t *testing.T) {
	runAccessTokenPasswordChangeScenarios(t, newATVFixture(t, openResetTestMySQL(t, 2)))
}

func runAccessTokenPasswordChangeScenarios(t *testing.T, f *atvFixture) {
	t.Run("a renewing change ends every earlier access token and the renewed one passes", func(t *testing.T) {
		s := f.seed(t)
		f.setVersion(t, s.owner.ID, 5)
		caller, otherDevice := f.jwt(t, s.owner.ID), f.jwt(t, s.owner.ID)
		var written map[string]interface{}
		f.hooks.install(func(h *atvHookSet) {
			h.updateFields = func(_ uint, fields map[string]interface{}) error {
				written = fields
				return nil
			}
		})

		tokens, err := f.svc.ChangePassword(s.owner.ID, atvPassword, atvNewPassword, atvPresented)
		require.NoError(t, err)
		require.NotNil(t, tokens)

		require.NotNil(t, written)
		assert.Contains(t, written, "password", "one UpdateFields write carries the password")
		assert.EqualValues(t, 6, written["token_version"], "and the incremented version")
		assert.Equal(t, uint(6), f.version(t, s.owner.ID), "incremented by exactly 1")
		f.assertRevoked(t, caller, "the caller's own token")
		f.assertRevoked(t, otherDevice)
		f.assertAccepted(t, tokens.AccessToken, s.owner.ID, "the renewed token passes")
		assert.Equal(t, float64(6), claimOf(t, tokens.AccessToken))
		assert.Equal(t, uint(0), f.version(t, s.bystander.ID))
	})

	t.Run("a change without a refresh token ends every earlier access token", func(t *testing.T) {
		s := f.seed(t)
		before := f.jwt(t, s.owner.ID)
		tokens, err := f.svc.ChangePassword(s.owner.ID, atvPassword, atvNewPassword, "")
		require.NoError(t, err)
		assert.Nil(t, tokens)
		assert.Equal(t, uint(1), f.version(t, s.owner.ID))
		f.assertRevoked(t, before)
		login, err := f.svc.LoginWithTokens(atvOwnerEmail, atvNewPassword)
		require.NoError(t, err)
		f.assertAccepted(t, login.AccessToken, s.owner.ID, "a fresh login passes")
	})

	injected := errors.New("injected failure")
	failures := []struct {
		name      string
		presented string
		install   func(f *atvFixture, s atvSeed)
		want      error
	}{
		{name: "a stale session", presented: "never-issued", want: ErrSessionStale},
		{name: "a foreign session", presented: atvForeign, want: ErrSessionStale},
		{
			name:      "a concurrent change",
			presented: atvPresented,
			want:      ErrInvalidCurrentPassword,
			install: func(f *atvFixture, s atvSeed) {
				f.hooks.install(func(h *atvHookSet) {
					h.afterRootRead = func(id uint) {
						if id != s.owner.ID {
							return
						}
						hash, err := bcrypt.GenerateFromPassword([]byte("SomeoneElse12!"), bcrypt.MinCost)
						if err == nil {
							_ = f.db.Model(&models.User{}).Where("id = ?", id).Update("password", string(hash)).Error
						}
					}
				})
			},
		},
		{
			name:      "a revocation fault",
			presented: atvPresented,
			want:      injected,
			install: func(f *atvFixture, _ atvSeed) {
				f.hooks.install(func(h *atvHookSet) { h.revokeAll = func(uint) error { return injected } })
			},
		},
		{
			name:      "a signing fault",
			presented: atvPresented,
			want:      injected,
			install: func(f *atvFixture, _ atvSeed) {
				f.svc.signAccessToken = func(*models.User) (string, error) { return "", injected }
			},
		},
	}
	for _, failure := range failures {
		t.Run("a change that fails before its commit through "+failure.name+" leaves the version alone", func(t *testing.T) {
			s := f.seed(t)
			before := f.jwt(t, s.owner.ID)
			if failure.install != nil {
				failure.install(f, s)
			}
			tokens, err := f.svc.ChangePassword(s.owner.ID, atvPassword, atvNewPassword, failure.presented)
			f.svc.signAccessToken = nil
			f.hooks.reset()
			assert.Nil(t, tokens)
			assert.ErrorIs(t, err, failure.want)
			assert.Equal(t, uint(0), f.version(t, s.owner.ID), "token_version is unchanged")
			f.assertAccepted(t, before, s.owner.ID, "the earlier token keeps passing")
		})
	}
}

// ------------------------------------------------------------------- AC-4 ---

func TestAccessTokenRevokedByPasswordResetSQLite(t *testing.T) {
	runAccessTokenPasswordResetScenarios(t, newATVFixture(t, openResetTestSQLite(t)))
}

func TestAccessTokenRevokedByPasswordResetMySQL(t *testing.T) {
	runAccessTokenPasswordResetScenarios(t, newATVFixture(t, openResetTestMySQL(t, 2)))
}

func runAccessTokenPasswordResetScenarios(t *testing.T, f *atvFixture) {
	t.Run("a confirmed reset ends every earlier access token in the password write", func(t *testing.T) {
		s := f.seed(t)
		f.setVersion(t, s.owner.ID, 2)
		before := f.jwt(t, s.owner.ID)
		bystander := f.jwt(t, s.bystander.ID)
		link := f.issueLink(t, s.owner)
		var written map[string]interface{}
		f.hooks.install(func(h *atvHookSet) {
			h.updateFields = func(_ uint, fields map[string]interface{}) error {
				written = fields
				return nil
			}
		})

		require.NoError(t, f.svc.ConfirmPasswordReset(link, atvNewPassword))

		require.NotNil(t, written)
		for _, column := range []string{"password", "failed_login_attempts", "locked_until"} {
			assert.Contains(t, written, column, "one UpdateFields write carries %s", column)
		}
		assert.EqualValues(t, 3, written["token_version"], "and the incremented version")
		assert.Equal(t, uint(3), f.version(t, s.owner.ID), "incremented by exactly 1")
		f.assertRevoked(t, before)
		f.assertAccepted(t, bystander, s.bystander.ID)
		login, err := f.svc.LoginWithTokens(atvOwnerEmail, atvNewPassword)
		require.NoError(t, err)
		f.assertAccepted(t, login.AccessToken, s.owner.ID)
	})

	injected := errors.New("refresh token table unavailable")
	cases := map[string]func(t *testing.T, s atvSeed, link string) error{
		"a weak password": func(t *testing.T, _ atvSeed, link string) error {
			return f.svc.ConfirmPasswordReset(link, "weak")
		},
		"an unhashable password": func(t *testing.T, _ atvSeed, link string) error {
			return f.svc.ConfirmPasswordReset(link, "Aa1!"+strings.Repeat("x", 80))
		},
		"a rejected token": func(t *testing.T, _ atvSeed, _ string) error {
			return f.svc.ConfirmPasswordReset("never-issued", atvNewPassword)
		},
		"an inactive owner": func(t *testing.T, s atvSeed, link string) error {
			require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.User{}).
				Where("id = ?", s.owner.ID).Update("is_active", false).Error)
			return f.svc.ConfirmPasswordReset(link, atvNewPassword)
		},
		"an erased owner": func(t *testing.T, s atvSeed, link string) error {
			require.NoError(t, f.db.WithContext(resetContext(t)).Delete(&models.User{}, s.owner.ID).Error)
			return f.svc.ConfirmPasswordReset(link, atvNewPassword)
		},
		"a failed refresh-token revocation": func(t *testing.T, _ atvSeed, link string) error {
			f.hooks.install(func(h *atvHookSet) { h.revokeAll = func(uint) error { return injected } })
			defer f.hooks.reset()
			err := f.svc.ConfirmPasswordReset(link, atvNewPassword)
			assert.ErrorIs(t, err, injected)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name+" leaves token_version unchanged", func(t *testing.T) {
			s := f.seed(t)
			f.setVersion(t, s.owner.ID, 2)
			before := f.jwt(t, s.owner.ID)
			link := f.issueLink(t, s.owner)

			assert.Error(t, run(t, s, link))
			assert.Equal(t, uint(2), f.version(t, s.owner.ID), "token_version is unchanged")
			var row models.User
			require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&row, s.owner.ID).Error)
			if row.IsActive && !row.DeletedAt.Valid {
				f.assertAccepted(t, before, s.owner.ID, "the earlier token keeps passing")
			}
		})
	}
}

// ------------------------------------------------------------------- AC-5 ---

func TestAccessTokenSurvivesRefreshRotationSQLite(t *testing.T) {
	runAccessTokenRotationScenarios(t, newATVFixture(t, openResetTestSQLite(t)))
}

func TestAccessTokenSurvivesRefreshRotationMySQL(t *testing.T) {
	runAccessTokenRotationScenarios(t, newATVFixture(t, openResetTestMySQL(t, 2)))
}

func runAccessTokenRotationScenarios(t *testing.T, f *atvFixture) {
	t.Run("a rotation keeps the version, signs the current value and leaves the earlier token valid", func(t *testing.T) {
		s := f.seed(t)
		f.setVersion(t, s.owner.ID, 4)
		before := f.jwt(t, s.owner.ID)

		rotated, err := f.svc.RefreshAccessToken(atvPresented)
		require.NoError(t, err)
		assert.Equal(t, uint(4), f.version(t, s.owner.ID), "rotation does not change token_version")
		assert.Equal(t, float64(4), claimOf(t, rotated.AccessToken), "the new token carries the current value")
		f.assertAccepted(t, rotated.AccessToken, s.owner.ID)
		f.assertAccepted(t, before, s.owner.ID, "the token issued before the rotation keeps passing")

		again, err := f.svc.RefreshAccessToken(rotated.RefreshToken)
		require.NoError(t, err)
		assert.Equal(t, uint(4), f.version(t, s.owner.ID))
		f.assertAccepted(t, again.AccessToken, s.owner.ID)
		f.assertAccepted(t, rotated.AccessToken, s.owner.ID)
	})

	t.Run("a login does not change the version", func(t *testing.T) {
		s := f.seed(t)
		f.setVersion(t, s.owner.ID, 2)
		before := f.jwt(t, s.owner.ID)

		tokens, err := f.svc.LoginWithTokens(atvOwnerEmail, atvPassword)
		require.NoError(t, err)
		plain, err := f.svc.Login(atvOwnerEmail, atvPassword)
		require.NoError(t, err)
		_, err = f.svc.Login(atvOwnerEmail, "WrongSecret123!")
		require.Error(t, err)

		assert.Equal(t, uint(2), f.version(t, s.owner.ID), "login does not change token_version")
		assert.Equal(t, float64(2), claimOf(t, tokens.AccessToken))
		f.assertAccepted(t, tokens.AccessToken, s.owner.ID)
		f.assertAccepted(t, plain, s.owner.ID)
		f.assertAccepted(t, before, s.owner.ID)
	})
}

// ------------------------------------------------------------------- AC-6 ---

func TestLogoutLocksOwnerBeforeRefreshTokensSQLite(t *testing.T) {
	runLogoutLockOrderScenarios(t, newATVFixture(t, openResetTestSQLite(t)), false)
}

func TestLogoutLocksOwnerBeforeRefreshTokensMySQL(t *testing.T) {
	runLogoutLockOrderScenarios(t, newATVFixture(t, openMigratedTestMySQL(t, 4)), true)
}

func runLogoutLockOrderScenarios(t *testing.T, f *atvFixture, contended bool) {
	t.Run("the users row is locked before any refresh_tokens row", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.reset()
		require.NoError(t, f.svc.Logout(s.owner.ID, atvSibling))
		assert.Equal(t, []string{"users:lock", "refresh_tokens:read", "refresh_tokens:revoke", "users:update"}, f.hooks.recorded())

		f.hooks.reset()
		require.NoError(t, f.svc.Logout(s.owner.ID, ""))
		assert.Equal(t, []string{"users:lock", "refresh_tokens:revoke_all", "users:update"}, f.hooks.recorded())
		assert.Equal(t, uint(2), f.version(t, s.owner.ID))
	})

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
	done := func(r result) <-chan result {
		ch := make(chan result, 1)
		ch <- r
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
	// hold parks the first request at a hook until release; entered is closed
	// once it got there. Only one request ever reaches the parking hook.
	hold := func() (wait func(), entered <-chan struct{}, release func()) {
		in := make(chan struct{})
		out := make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		return func() {
				enterOnce.Do(func() { close(in) })
				select {
				case <-out:
				case <-time.After(resetTimeout):
				}
			}, in, func() {
				releaseOnce.Do(func() { close(out) })
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
	// progress reports when the second request is about to run its locking
	// owner read; it is installed after the first request is parked past its
	// own, so only the second request fires it.
	progress := func() <-chan struct{} {
		reached := make(chan struct{})
		var once sync.Once
		f.hooks.install(func(h *atvHookSet) {
			h.beforeLock = func(uint) error {
				once.Do(func() { close(reached) })
				return nil
			}
		})
		return reached
	}
	// contend waits until the second request is blocked on the row lock the
	// first holds: it reached its `... FOR UPDATE`, and the server reports that
	// statement still executing on two consecutive polls of
	// information_schema.PROCESSLIST (MySQL 8 and MariaDB 10.11 alike). On
	// SQLite the single pooled connection serialises the two instead.
	contend := func(t *testing.T, reached <-chan struct{}, ch <-chan result) {
		t.Helper()
		if !contended {
			return
		}
		select {
		case <-reached:
		case r := <-ch:
			t.Fatalf("the second request finished before reaching its locking read: %v", r.err)
		case <-time.After(resetTimeout):
			t.Fatalf("the second request never reached its locking read")
		}
		deadline := time.Now().Add(resetTimeout)
		seen := 0
		for seen < 2 {
			var waiting int64
			require.NoError(t, f.db.WithContext(resetContext(t)).Raw(`SELECT COUNT(*)
				FROM information_schema.PROCESSLIST
				WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
				AND INFO LIKE '%FOR UPDATE%'`).Scan(&waiting).Error)
			if waiting > 0 {
				seen++
			} else {
				seen = 0
			}
			select {
			case r := <-ch:
				t.Fatalf("the second request finished without waiting for the lock: %v", r.err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("the second request was never reported waiting for the row lock")
			}
			time.Sleep(atvLockPoll)
		}
	}
	noDeadlock := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			assert.NotContains(t, err.Error(), "1213", "no deadlock")
			assert.NotContains(t, strings.ToLower(err.Error()), "deadlock")
		}
	}
	tokenID := func(t *testing.T, raw string) uint {
		t.Helper()
		var token models.RefreshToken
		require.NoError(t, f.db.WithContext(resetContext(t)).
			Where("token_hash = ?", hashOpaqueToken(raw, resetTestSecret)).First(&token).Error)
		return token.ID
	}

	t.Run("a rotation that commits before the logout loses its access token to it", func(t *testing.T) {
		s := f.seed(t)
		siblingID := tokenID(t, atvSibling)
		wait, entered, release := hold()
		defer release()
		f.hooks.install(func(h *atvHookSet) {
			h.afterClaim = func(id uint) error {
				if id == siblingID {
					wait()
				}
				return nil
			}
		})
		rotation := start(func() (*AuthTokens, error) { return f.svc.RefreshAccessToken(atvSibling) })
		if contended {
			enter(t, entered, "the rotation")
		} else {
			// One pooled connection: the rotation runs to completion first,
			// then the logout runs in sequence.
			release()
			rotation = done(await(t, rotation, "the rotation"))
		}
		reached := progress()
		logout := start(func() (*AuthTokens, error) { return nil, f.svc.Logout(s.owner.ID, "") })
		contend(t, reached, logout)
		release()

		r := await(t, rotation, "the rotation")
		l := await(t, logout, "the logout")
		noDeadlock(t, r.err)
		noDeadlock(t, l.err)
		require.NoError(t, r.err, "the rotation committed first")
		require.NoError(t, l.err, "the logout waits for the owner lock and commits")
		assert.Equal(t, uint(1), f.version(t, s.owner.ID), "incremented once")
		f.assertRevoked(t, r.tokens.AccessToken, "a token issued by a rotation committed before the logout fails")
		assert.False(t, f.live(t, r.tokens.RefreshToken), "the logout revokes the successor too")
	})

	t.Run("a rotation that arrives during the logout waits and then signs the new version", func(t *testing.T) {
		s := f.seed(t)
		before := f.jwt(t, s.owner.ID)
		wait, entered, release := hold()
		defer release()
		f.hooks.install(func(h *atvHookSet) {
			h.revoke = func(string) error { wait(); return nil }
		})
		logout := start(func() (*AuthTokens, error) { return nil, f.svc.Logout(s.owner.ID, atvPresented) })
		if contended {
			enter(t, entered, "the logout")
		} else {
			release()
			logout = done(await(t, logout, "the logout"))
		}
		reached := progress()
		rotation := start(func() (*AuthTokens, error) { return f.svc.RefreshAccessToken(atvSibling) })
		contend(t, reached, rotation)
		release()

		l := await(t, logout, "the logout")
		r := await(t, rotation, "the rotation")
		noDeadlock(t, l.err)
		noDeadlock(t, r.err)
		require.NoError(t, l.err)
		require.NoError(t, r.err, "the sibling session is still live after a single-session logout")
		assert.Equal(t, uint(1), f.version(t, s.owner.ID), "incremented once")
		f.assertRevoked(t, before)
		assert.Equal(t, float64(1), claimOf(t, r.tokens.AccessToken), "the rotation read the committed version")
		f.assertAccepted(t, r.tokens.AccessToken, s.owner.ID)
		assert.False(t, f.live(t, atvPresented))
	})
}

// ------------------------------------------------------------------- AC-8 ---

func TestAccessTokenRevocationLogsOmitPersonalData(t *testing.T) {
	f := newATVFixture(t, openResetTestSQLite(t))
	s := f.seed(t)
	beforeLogout := f.jwt(t, s.owner.ID)

	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })

	// A baseline line tells which keys every entry carries.
	utils.Logger.Info("atv-baseline")

	require.NoError(t, f.svc.Logout(s.owner.ID, atvPresented))
	_, err := f.svc.ValidateToken(beforeLogout)
	require.ErrorIs(t, err, ErrAccessTokenRevoked)

	beforeChange := f.jwt(t, s.owner.ID)
	tokens, err := f.svc.ChangePassword(s.owner.ID, atvPassword, atvNewPassword, atvSibling)
	require.NoError(t, err)
	_, err = f.svc.ValidateToken(beforeChange)
	require.ErrorIs(t, err, ErrAccessTokenRevoked)

	beforeReset := f.jwt(t, s.owner.ID)
	link := f.issueLink(t, s.owner)
	require.NoError(t, f.svc.ConfirmPasswordReset(link, "AnotherPass12!"))
	_, err = f.svc.ValidateToken(beforeReset)
	require.ErrorIs(t, err, ErrAccessTokenRevoked)
	_, err = f.svc.ValidateToken(tokens.AccessToken)
	require.ErrorIs(t, err, ErrAccessTokenRevoked)

	var baseline map[string]interface{}
	targets := map[string][]map[string]interface{}{}
	lines := []struct{ msg, operation string }{
		{"Rejected access token of an ended session", "ValidateToken"},
		{"Logout ended the access tokens issued before it", "Logout"},
		{"Password changed; current session renewed, other sessions and earlier access tokens ended", "ChangePassword"},
		{"Password reset completed; all sessions and earlier access tokens ended", "ConfirmPasswordReset"},
	}
	wantOperation := map[string]string{}
	for _, line := range lines {
		wantOperation[line.msg] = line.operation
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		msg, _ := entry["msg"].(string)
		if msg == "atv-baseline" {
			baseline = entry
		}
		if _, ok := wantOperation[msg]; ok {
			targets[msg] = append(targets[msg], entry)
		}
	}
	require.NotNil(t, baseline)
	allowed := map[string]bool{"user_id": true, "operation": true}
	for key := range baseline {
		allowed[key] = true
	}

	secrets := []string{
		atvOwnerEmail, beforeLogout, beforeChange, beforeReset, tokens.AccessToken, tokens.RefreshToken,
		atvPresented, atvSibling, link, atvPassword, atvNewPassword, "AnotherPass12!",
	}
	for msg, operation := range wantOperation {
		entries := targets[msg]
		require.NotEmpty(t, entries, "the %q line is written", msg)
		for _, entry := range entries {
			assert.Equal(t, operation, entry["operation"], msg)
			assert.EqualValues(t, s.owner.ID, entry["user_id"], msg)
			for key := range entry {
				assert.True(t, allowed[key], "%q carries only user_id and the operation, not %q", msg, key)
			}
			encoded, err := json.Marshal(entry)
			require.NoError(t, err)
			for _, secret := range secrets {
				assert.NotContains(t, string(encoded), secret, msg)
			}
		}
	}
	assert.Len(t, targets["Rejected access token of an ended session"], 4, "one line per rejected token")

	// No line of these flows carries a token or the claim.
	text := output.String()
	for _, secret := range []string{beforeLogout, beforeChange, beforeReset, tokens.AccessToken, tokens.RefreshToken, atvPresented, atvSibling, link} {
		assert.NotContains(t, text, secret)
	}
	assert.NotContains(t, text, "token_version")
}
