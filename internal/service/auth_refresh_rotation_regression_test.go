package service

// Regression roots for issue #103: a presented refresh token is claimed by a
// conditional update inside the transaction that inserts its successor and
// signs the access token, so a failed or competing rotation can neither mint
// two successors for one token nor strand a session by revoking a token whose
// replacement never came.
//
// Every root runs on a real database — in-memory SQLite with the production
// one-connection pool, and a disposable MySQL schema when TEST_MYSQL_DSN is
// set (the openers are shared with the password reset roots) — and every
// assertion reads committed rows outside any transaction. The hooks below
// interpose on the transaction-bound refresh token repository so a scenario
// can fail, or alter rows the way a competing request would, at an exact point
// of the unit of work; the access token signer fault uses the instance-local
// seam of the service under test. All waits are bounded.

import (
	"errors"
	"sync"
	"sync/atomic"
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

// rotationCompetitors is how many requests race for one refresh token.
const rotationCompetitors = 3

const (
	rotationTestSecret = "refresh-rotation-regression-secret-32-bytes"
	// rotationPresented is the raw refresh token every scenario presents;
	// rotationSibling is a second live session of the same owner that no
	// scenario presents and that must survive whatever happens to the first.
	rotationPresented = "owner-session-presented"
	rotationSibling   = "owner-session-sibling"
	rotationBystander = "bystander-session"
)

// ------------------------------------------------------------------- hooks ---

// rotationHooks interposes on the refresh token repository the service
// rotates through. Every hook fires on the transaction-bound repository, and
// the lookup hook receives the handle the repository is bound to, so a
// scenario can alter rows through the transaction itself — or fail the way a
// broken database would — at an exact point of the unit of work. Access is
// mutex-guarded because the concurrent scenario installs and fires hooks from
// different goroutines.
type rotationHooks struct {
	mu               sync.Mutex
	afterTokenLookup func(tx *gorm.DB, token *models.RefreshToken) error
	beforeClaim      func(id uint) error
	beforeCreate     func(token *models.RefreshToken) error
}

func (h *rotationHooks) install(fn func(h *rotationHooks)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *rotationHooks) clear() {
	h.install(func(h *rotationHooks) {
		h.afterTokenLookup = nil
		h.beforeClaim = nil
		h.beforeCreate = nil
	})
}

func (h *rotationHooks) tokenLookup() func(*gorm.DB, *models.RefreshToken) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.afterTokenLookup
}

func (h *rotationHooks) claim() func(uint) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeClaim
}

func (h *rotationHooks) create() func(*models.RefreshToken) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforeCreate
}

// rotationRefreshTokenRepository wraps the real repository; WithTx keeps the
// hooks and remembers the handle the wrapped repository is bound to.
type rotationRefreshTokenRepository struct {
	repository.RefreshTokenRepository
	db    *gorm.DB
	hooks *rotationHooks
}

func (r *rotationRefreshTokenRepository) WithTx(tx *gorm.DB) repository.RefreshTokenRepository {
	return &rotationRefreshTokenRepository{RefreshTokenRepository: r.RefreshTokenRepository.WithTx(tx), db: tx, hooks: r.hooks}
}

func (r *rotationRefreshTokenRepository) GetByTokenHash(hash string) (*models.RefreshToken, error) {
	token, err := r.RefreshTokenRepository.GetByTokenHash(hash)
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

func (r *rotationRefreshTokenRepository) ClaimForRotation(id uint) error {
	if hook := r.hooks.claim(); hook != nil {
		if err := hook(id); err != nil {
			return err
		}
	}
	return r.RefreshTokenRepository.ClaimForRotation(id)
}

func (r *rotationRefreshTokenRepository) Create(token *models.RefreshToken) error {
	if hook := r.hooks.create(); hook != nil {
		if err := hook(token); err != nil {
			return err
		}
	}
	return r.RefreshTokenRepository.Create(token)
}

// --------------------------------------------------------- committed state ---

// rotationState is what the database holds for one account, read outside any
// transaction.
type rotationState struct {
	exists       bool
	active       bool
	sessions     int64 // every refresh token row of the account
	liveSessions int64 // rows not revoked (all seeded rows are unexpired)
}

var (
	// rotationSeededOwner has the presented token and its sibling, both live.
	rotationSeededOwner = rotationState{exists: true, active: true, sessions: 2, liveSessions: 2}
	// rotationRotatedOwner is the same account after one committed rotation:
	// the presented token revoked, the sibling and exactly one successor live.
	rotationRotatedOwner = rotationState{exists: true, active: true, sessions: 3, liveSessions: 2}
	// rotationContainedOwner is a disabled account whose presented token was
	// revoked as containment, with no successor; the sibling is untouched.
	rotationContainedOwner = rotationState{exists: true, active: false, sessions: 2, liveSessions: 1}
	// rotationBystanderState is an unrelated account that must never change.
	rotationBystanderState = rotationState{exists: true, active: true, sessions: 1, liveSessions: 1}
)

// ----------------------------------------------------------------- fixture ---

type rotationFixture struct {
	db          *gorm.DB
	userRepo    repository.UserRepository
	refreshRepo repository.RefreshTokenRepository
	hooks       *rotationHooks
	svc         AuthService
	// auth is the same instance as svc, for the instance-local signer seam.
	auth *authService
}

func newRotationFixture(t *testing.T, db *gorm.DB) *rotationFixture {
	t.Helper()
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})

	f := &rotationFixture{
		db:          db,
		userRepo:    repository.NewUserRepository(db),
		refreshRepo: repository.NewRefreshTokenRepository(db),
		hooks:       &rotationHooks{},
	}
	f.svc = f.newService(utils.NewTransactionManager(db))
	f.auth = f.svc.(*authService)
	return f
}

// newService wires the service under test on the fixture's repositories with
// the given transaction manager (nil reproduces a stateless wiring).
func (f *rotationFixture) newService(txManager repository.TransactionManager) AuthService {
	return NewAuthServiceWithSessions(
		f.userRepo,
		repository.NewAPIKeyRepository(f.db),
		&rotationRefreshTokenRepository{RefreshTokenRepository: f.refreshRepo, db: f.db, hooks: f.hooks},
		repository.NewPasswordResetTokenRepository(f.db),
		txManager,
		&fakeFormMailer{},
		config.JWTConfig{Secret: rotationTestSecret, ExpiryHours: 1, RefreshTokenDays: 30},
		resetTestBaseURL, "")
}

// reset empties the tables a scenario writes and the hooks and seams, so
// every scenario starts from a blank slate on the shared schema.
func (f *rotationFixture) reset(t *testing.T) {
	t.Helper()
	f.hooks.clear()
	f.auth.signAccessToken = nil
	ctx := resetContext(t)
	for _, table := range []string{"password_reset_tokens", "refresh_tokens", "users"} {
		require.NoError(t, f.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
}

func (f *rotationFixture) exec(t *testing.T, statement string, args ...interface{}) {
	t.Helper()
	require.NoError(t, f.db.WithContext(resetContext(t)).Exec(statement, args...).Error)
}

func (f *rotationFixture) createUser(t *testing.T, email string) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(resetOldPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{
		Email:     email,
		Password:  string(hash),
		FirstName: "Rotation",
		LastName:  "Owner",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(user).Error)
	return user
}

func (f *rotationFixture) createSession(t *testing.T, userID uint, raw string) {
	t.Helper()
	require.NoError(t, f.refreshRepo.Create(&models.RefreshToken{
		UserID:    userID,
		TokenHash: hashOpaqueToken(raw, rotationTestSecret),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))
}

// rotationSeed is the starting point of every scenario: an owner with the
// presented token and a sibling session, and a bystander with a session of
// their own.
type rotationSeed struct {
	owner     *models.User
	bystander *models.User
	token     string
}

func (f *rotationFixture) seed(t *testing.T) rotationSeed {
	t.Helper()
	f.reset(t)
	owner := f.createUser(t, "owner@example.com")
	bystander := f.createUser(t, "bystander@example.com")
	f.createSession(t, owner.ID, rotationPresented)
	f.createSession(t, owner.ID, rotationSibling)
	f.createSession(t, bystander.ID, rotationBystander)
	require.Equal(t, rotationSeededOwner, f.state(t, owner.ID))
	require.Equal(t, rotationBystanderState, f.state(t, bystander.ID))
	return rotationSeed{owner: owner, bystander: bystander, token: rotationPresented}
}

func (f *rotationFixture) state(t *testing.T, userID uint) rotationState {
	t.Helper()
	ctx := resetContext(t)
	var s rotationState
	require.NoError(t, f.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("user_id = ?", userID).Count(&s.sessions).Error)
	require.NoError(t, f.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("user_id = ? AND is_revoked = ?", userID, false).Count(&s.liveSessions).Error)

	// Unscoped, so an erased (soft-deleted) account is still inspected.
	var user models.User
	err := f.db.WithContext(ctx).Unscoped().First(&user, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s
	}
	require.NoError(t, err)
	s.exists = true
	s.active = user.IsActive
	return s
}

// live reports whether a raw refresh token is committed as live: present,
// not revoked and unexpired.
func (f *rotationFixture) live(t *testing.T, raw string) bool {
	t.Helper()
	var n int64
	require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.RefreshToken{}).
		Where("token_hash = ? AND is_revoked = ? AND expires_at > ?", hashOpaqueToken(raw, rotationTestSecret), false, time.Now()).
		Count(&n).Error)
	return n == 1
}

// revoked reports whether a raw refresh token is committed as a revoked row.
func (f *rotationFixture) revoked(t *testing.T, raw string) bool {
	t.Helper()
	var n int64
	require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.RefreshToken{}).
		Where("token_hash = ? AND is_revoked = ?", hashOpaqueToken(raw, rotationTestSecret), true).
		Count(&n).Error)
	return n == 1
}

func (f *rotationFixture) assertBystanderIntact(t *testing.T, s rotationSeed) {
	t.Helper()
	assert.Equal(t, rotationBystanderState, f.state(t, s.bystander.ID), "an unrelated account must not change")
	assert.True(t, f.live(t, rotationBystander), "the bystander keeps their session")
}

// assertUntouched checks that a rejected or failed rotation left the owner
// exactly as seeded: the presented token live, the sibling live, no new row.
func (f *rotationFixture) assertUntouched(t *testing.T, s rotationSeed) {
	t.Helper()
	assert.Equal(t, rotationSeededOwner, f.state(t, s.owner.ID))
	assert.True(t, f.live(t, s.token), "the presented token must still be usable")
	assert.True(t, f.live(t, rotationSibling), "the sibling session must be untouched")
	f.assertBystanderIntact(t, s)
}

// assertCredentials checks that a returned pair is usable: the access token
// authenticates the owner and the refresh token is the one live successor.
func (f *rotationFixture) assertCredentials(t *testing.T, s rotationSeed, tokens *AuthTokens) {
	t.Helper()
	require.NotNil(t, tokens)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.NotEmpty(t, tokens.RefreshToken)
	assert.NotEqual(t, s.token, tokens.RefreshToken, "rotation must mint a new opaque token")
	require.NotNil(t, tokens.User)
	assert.Equal(t, s.owner.ID, tokens.User.ID)

	authenticated, err := f.svc.ValidateToken(tokens.AccessToken)
	require.NoError(t, err, "the new access token must authenticate")
	assert.Equal(t, s.owner.ID, authenticated.ID)
	assert.True(t, f.live(t, tokens.RefreshToken), "the returned refresh token must be the committed live successor")
}

// assertRotated checks the committed state of a successful rotation: the
// presented token revoked, the sibling untouched, exactly one successor live
// and the returned credentials usable.
func (f *rotationFixture) assertRotated(t *testing.T, s rotationSeed, tokens *AuthTokens) {
	t.Helper()
	assert.Equal(t, rotationRotatedOwner, f.state(t, s.owner.ID))
	assert.True(t, f.revoked(t, s.token), "the presented token must be revoked")
	assert.True(t, f.live(t, rotationSibling), "the sibling session must be untouched")
	f.assertCredentials(t, s, tokens)
	f.assertBystanderIntact(t, s)
}

// assertContained checks the committed state of a rotation refused because
// of its owner: the presented token revoked, no successor, the sibling alone.
func (f *rotationFixture) assertContained(t *testing.T, s rotationSeed, expected rotationState) {
	t.Helper()
	assert.Equal(t, expected, f.state(t, s.owner.ID))
	assert.True(t, f.revoked(t, s.token), "the presented token of a rejected owner must be revoked")
	assert.True(t, f.live(t, rotationSibling), "the sibling session is not the business of this rotation")
	f.assertBystanderIntact(t, s)
}

// assertReplayRejected presents the spent token again and checks that the
// committed state did not move.
func (f *rotationFixture) assertReplayRejected(t *testing.T, s rotationSeed, expected rotationState) {
	t.Helper()
	replayed, err := f.svc.RefreshAccessToken(s.token)
	assert.Nil(t, replayed)
	assert.ErrorIs(t, err, ErrInvalidRefreshToken)
	assert.Equal(t, expected, f.state(t, s.owner.ID))
}

// ------------------------------------------------------------------- roots ---

func TestRefreshRotationValidationSQLite(t *testing.T) {
	runRefreshRotationValidationScenarios(t, openResetTestSQLite(t), 1)
}

func TestRefreshRotationValidationMySQL(t *testing.T) {
	runRefreshRotationValidationScenarios(t, openResetTestMySQL(t, 2), 2)
}

func TestRefreshRotationAtomicitySQLite(t *testing.T) {
	runRefreshRotationAtomicityScenarios(t, openResetTestSQLite(t))
}

func TestRefreshRotationAtomicityMySQL(t *testing.T) {
	runRefreshRotationAtomicityScenarios(t, openResetTestMySQL(t, 1))
}

func TestRefreshRotationConcurrentSQLite(t *testing.T) {
	runRefreshRotationConcurrentScenarios(t, openResetTestSQLite(t), 1)
}

func TestRefreshRotationConcurrentMySQL(t *testing.T) {
	runRefreshRotationConcurrentScenarios(t, openResetTestMySQL(t, rotationCompetitors+1), rotationCompetitors+1)
}

// -------------------------------------------------------------- validation ---

func runRefreshRotationValidationScenarios(t *testing.T, db *gorm.DB, connections int) {
	f := newRotationFixture(t, db)

	t.Run("unknown and empty tokens are rejected without touching anything", func(t *testing.T) {
		s := f.seed(t)
		for _, token := range []string{"", "never-issued"} {
			tokens, err := f.svc.RefreshAccessToken(token)
			assert.Nil(t, tokens)
			assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		}
		f.assertUntouched(t, s)
	})

	t.Run("a revoked token is rejected and no successor is created", func(t *testing.T) {
		s := f.seed(t)
		require.NoError(t, f.refreshRepo.RevokeByTokenHash(hashOpaqueToken(s.token, rotationTestSecret)))

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)

		expected := rotationSeededOwner
		expected.liveSessions = 1
		assert.Equal(t, expected, f.state(t, s.owner.ID))
		assert.True(t, f.live(t, rotationSibling))
		f.assertBystanderIntact(t, s)
	})

	t.Run("an expired token is rejected and left as it is", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "UPDATE refresh_tokens SET expires_at = ? WHERE token_hash = ?",
			time.Now().Add(-time.Second), hashOpaqueToken(s.token, rotationTestSecret))

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)

		assert.Equal(t, rotationSeededOwner, f.state(t, s.owner.ID), "an expired token is neither revoked nor replaced")
		assert.False(t, f.live(t, s.token))
		assert.False(t, f.revoked(t, s.token))
		f.assertBystanderIntact(t, s)
	})

	t.Run("a token that expires between the lookup and the claim is rejected", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *rotationHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.RefreshToken) error {
				// The lookup has just returned the token as live; the clock
				// crosses its expiry before the claim runs. Written through
				// the transaction's own handle: on the one-connection pool a
				// second handle would wait for the connection it holds.
				return tx.Model(&models.RefreshToken{}).Where("id = ?", stored.ID).
					Update("expires_at", time.Now().Add(-time.Second)).Error
			}
		})

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		// The rollback discarded the simulated expiry with everything else.
		f.assertUntouched(t, s)

		f.hooks.clear()
		rotated, err := f.svc.RefreshAccessToken(s.token)
		require.NoError(t, err)
		f.assertRotated(t, s, rotated)
	})

	t.Run("a token revoked between the lookup and the claim is rejected", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *rotationHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.RefreshToken) error {
				// A competing rotation or a logout revoked the row after this
				// request read it as live.
				return tx.Model(&models.RefreshToken{}).Where("id = ?", stored.ID).
					Update("is_revoked", true).Error
			}
		})

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		f.assertUntouched(t, s)
	})

	t.Run("an inactive owner is rejected and the presented token is revoked", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, s.owner.ID)

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		f.assertContained(t, s, rotationContainedOwner)

		// Reactivating the account does not resurrect the contained token:
		// the revocation was committed with the rejection. The sibling
		// session of the reactivated owner rotates normally.
		f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", true, s.owner.ID)
		reactivated := rotationContainedOwner
		reactivated.active = true
		f.assertReplayRejected(t, s, reactivated)
		sibling, err := f.svc.RefreshAccessToken(rotationSibling)
		require.NoError(t, err)
		assert.True(t, f.live(t, sibling.RefreshToken))
		assert.True(t, f.revoked(t, rotationSibling))
	})

	t.Run("an owner deactivated between the lookup and the owner check is rejected and contained", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *rotationHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.RefreshToken) error {
				// The account is disabled after this request read its token
				// as live. The owner check must see the current row, not a
				// user record read earlier, and no successor may be issued.
				return tx.Model(&models.User{}).Where("id = ?", stored.UserID).Update("is_active", false).Error
			}
		})

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		// The containment commits, so the deactivation written through the
		// same transaction is committed with it.
		f.assertContained(t, s, rotationContainedOwner)
		f.hooks.clear()
		f.assertReplayRejected(t, s, rotationContainedOwner)
	})

	t.Run("an owner soft-deleted between the lookup and the owner check is rejected and contained", func(t *testing.T) {
		s := f.seed(t)
		f.hooks.install(func(h *rotationHooks) {
			h.afterTokenLookup = func(tx *gorm.DB, stored *models.RefreshToken) error {
				return tx.Model(&models.User{}).Where("id = ?", stored.UserID).Update("deleted_at", time.Now()).Error
			}
		})

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		expected := rotationContainedOwner
		expected.active = true // soft-deleted, not deactivated
		f.assertContained(t, s, expected)
		f.hooks.clear()
		f.assertReplayRejected(t, s, expected)
	})

	if connections > 1 {
		t.Run("an owner deactivated by another connection after the token lookup is rejected, not served from the snapshot", func(t *testing.T) {
			s := f.seed(t)
			var deactivated atomic.Bool
			f.hooks.install(func(h *rotationHooks) {
				h.afterTokenLookup = func(_ *gorm.DB, stored *models.RefreshToken) error {
					// A separate connection commits the deactivation after the
					// transaction's snapshot was taken at the token lookup. A
					// plain owner read under REPEATABLE READ would still see
					// the account as active; the locking read must not.
					deactivated.Store(true)
					return f.db.WithContext(resetContext(t)).Model(&models.User{}).
						Where("id = ?", stored.UserID).Update("is_active", false).Error
				}
			})

			tokens, err := f.svc.RefreshAccessToken(s.token)
			require.True(t, deactivated.Load(), "the deactivation must interleave after the token lookup")
			assert.Nil(t, tokens)
			assert.ErrorIs(t, err, ErrInvalidRefreshToken)
			f.assertContained(t, s, rotationContainedOwner)
			f.hooks.clear()
			f.assertReplayRejected(t, s, rotationContainedOwner)
		})
	}

	t.Run("an erased owner is rejected", func(t *testing.T) {
		s := f.seed(t)
		// Erasure scrubs the row, purges its credentials and soft-deletes it.
		require.NoError(t, f.userRepo.Delete(s.owner.ID))

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		assert.Equal(t, rotationState{exists: true}, f.state(t, s.owner.ID), "erasure leaves a scrubbed, inactive row with no sessions")
		f.assertBystanderIntact(t, s)
	})

	t.Run("a soft-deleted owner whose token survived is rejected and contained", func(t *testing.T) {
		s := f.seed(t)
		// Defence in depth: a token that escaped the purge must not reach the
		// scoped-out row.
		f.exec(t, "UPDATE users SET deleted_at = ? WHERE id = ?", time.Now(), s.owner.ID)

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		expected := rotationContainedOwner
		expected.active = true
		f.assertContained(t, s, expected)
	})

	t.Run("a missing owner is rejected and the orphaned token is revoked", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "DELETE FROM users WHERE id = ?", s.owner.ID)

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		f.assertContained(t, s, rotationState{sessions: 2, liveSessions: 1})
		f.assertReplayRejected(t, s, rotationState{sessions: 2, liveSessions: 1})
	})

	t.Run("without a transaction manager the rotation is refused rather than run as separate writes", func(t *testing.T) {
		s := f.seed(t)
		stateless := f.newService(nil)

		tokens, err := stateless.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		f.assertUntouched(t, s)
	})

	t.Run("a valid rotation commits once and logout stays idempotent around it", func(t *testing.T) {
		s := f.seed(t)

		rotated, err := f.svc.RefreshAccessToken(s.token)
		require.NoError(t, err)
		f.assertRotated(t, s, rotated)
		f.assertReplayRejected(t, s, rotationRotatedOwner)

		// Logout with the spent token is a no-op success; with a token of
		// another user it is ignored; with the successor it revokes exactly
		// that session, and again is still a success.
		require.NoError(t, f.svc.Logout(s.owner.ID, s.token))
		assert.Equal(t, rotationRotatedOwner, f.state(t, s.owner.ID))
		require.NoError(t, f.svc.Logout(s.bystander.ID, rotationSibling))
		assert.True(t, f.live(t, rotationSibling), "a foreign token is not revoked by someone else's logout")
		require.NoError(t, f.svc.InvalidateRefreshToken("never-issued"))

		require.NoError(t, f.svc.Logout(s.owner.ID, rotated.RefreshToken))
		afterLogout := rotationRotatedOwner
		afterLogout.liveSessions = 1
		assert.Equal(t, afterLogout, f.state(t, s.owner.ID))
		assert.True(t, f.revoked(t, rotated.RefreshToken))
		require.NoError(t, f.svc.Logout(s.owner.ID, rotated.RefreshToken), "logout is idempotent")
		assert.Equal(t, afterLogout, f.state(t, s.owner.ID))

		loggedOut, err := f.svc.RefreshAccessToken(rotated.RefreshToken)
		assert.Nil(t, loggedOut)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken, "a logged-out successor cannot be rotated")

		// Logout without a token ends every session of the owner only.
		require.NoError(t, f.svc.Logout(s.owner.ID, ""))
		afterLogout.liveSessions = 0
		assert.Equal(t, afterLogout, f.state(t, s.owner.ID))
		f.assertBystanderIntact(t, s)
	})
}

// --------------------------------------------------------------- atomicity ---

func runRefreshRotationAtomicityScenarios(t *testing.T, db *gorm.DB) {
	f := newRotationFixture(t, db)
	injected := errors.New("injected failure")

	cases := []struct {
		name    string
		install func()
	}{
		{name: "the successor insert fails after the claim", install: func() {
			f.hooks.install(func(h *rotationHooks) {
				h.beforeCreate = func(*models.RefreshToken) error { return injected }
			})
		}},
		{name: "the access token cannot be signed after the successor insert", install: func() {
			f.auth.signAccessToken = func(*models.User) (string, error) { return "", injected }
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := f.seed(t)
			tc.install()

			tokens, err := f.svc.RefreshAccessToken(s.token)
			require.ErrorIs(t, err, injected, "the failure reaches the caller as it is")
			assert.NotErrorIs(t, err, ErrInvalidRefreshToken)
			assert.Nil(t, tokens, "no credentials are returned for a rotation that did not commit")
			// The claim and the successor were rolled back together: the
			// presented token is live, there is no extra row.
			f.assertUntouched(t, s)

			// The same token, retried once the fault is gone, rotates.
			f.hooks.clear()
			f.auth.signAccessToken = nil
			rotated, err := f.svc.RefreshAccessToken(s.token)
			require.NoError(t, err)
			f.assertRotated(t, s, rotated)
			f.assertReplayRejected(t, s, rotationRotatedOwner)
		})
	}

	t.Run("a committed rotation yields usable credentials and leaves the sibling session and other users alone", func(t *testing.T) {
		s := f.seed(t)

		first, err := f.svc.RefreshAccessToken(s.token)
		require.NoError(t, err)
		f.assertRotated(t, s, first)
		f.assertReplayRejected(t, s, rotationRotatedOwner)

		// The successor rotates in turn; the chain keeps exactly one live
		// successor and the sibling is never touched.
		second, err := f.svc.RefreshAccessToken(first.RefreshToken)
		require.NoError(t, err)
		chained := rotationRotatedOwner
		chained.sessions = 4
		assert.Equal(t, chained, f.state(t, s.owner.ID))
		assert.True(t, f.revoked(t, first.RefreshToken))
		f.assertCredentials(t, s, second)
		assert.True(t, f.live(t, rotationSibling))

		// The sibling session rotates independently.
		sibling, err := f.svc.RefreshAccessToken(rotationSibling)
		require.NoError(t, err)
		chained.sessions = 5
		assert.Equal(t, chained, f.state(t, s.owner.ID))
		assert.True(t, f.revoked(t, rotationSibling))
		assert.True(t, f.live(t, sibling.RefreshToken))
		assert.True(t, f.live(t, second.RefreshToken))
		f.assertBystanderIntact(t, s)
	})

	t.Run("an inactive owner's token is revoked without a successor and stays revoked", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, s.owner.ID)

		tokens, err := f.svc.RefreshAccessToken(s.token)
		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		f.assertContained(t, s, rotationContainedOwner)

		f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", true, s.owner.ID)
		reactivated := rotationContainedOwner
		reactivated.active = true
		f.assertReplayRejected(t, s, reactivated)
	})
}

// -------------------------------------------------------------- concurrent ---

// runRefreshRotationConcurrentScenarios races several requests presenting one
// refresh token. With several connections every competitor is held after its
// lookup until all of them have read the token as live, so the claim alone
// decides the winner. On the one-connection SQLite pool that barrier cannot be
// used — the second request cannot even begin until the first has ended — so
// the competitors start together and the pool serialises them; the outcome is
// the same and just as deterministic: one commit, one successor, every other
// request rejected without credentials.
func runRefreshRotationConcurrentScenarios(t *testing.T, db *gorm.DB, connections int) {
	f := newRotationFixture(t, db)

	t.Run("competing rotations of one token: exactly one succeeds and mints the only successor", func(t *testing.T) {
		s := f.seed(t)

		var barrierFailed atomic.Bool
		if connections > 1 {
			arrived := make(chan struct{}, rotationCompetitors)
			release := make(chan struct{})
			f.hooks.install(func(h *rotationHooks) {
				h.afterTokenLookup = func(*gorm.DB, *models.RefreshToken) error {
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
				for i := 0; i < rotationCompetitors; i++ {
					select {
					case <-arrived:
					case <-deadline:
						barrierFailed.Store(true)
						return
					}
				}
			}()
		}

		type outcome struct {
			tokens *AuthTokens
			err    error
		}
		start := make(chan struct{})
		results := make(chan outcome, rotationCompetitors)
		for i := 0; i < rotationCompetitors; i++ {
			go func() {
				<-start
				tokens, err := f.svc.RefreshAccessToken(s.token)
				results <- outcome{tokens: tokens, err: err}
			}()
		}
		close(start)

		var winners []*AuthTokens
		losers := 0
		deadline := time.After(resetTimeout)
		for i := 0; i < rotationCompetitors; i++ {
			select {
			case r := <-results:
				switch {
				case r.err == nil:
					winners = append(winners, r.tokens)
				case errors.Is(r.err, ErrInvalidRefreshToken):
					losers++
					assert.Nil(t, r.tokens, "a losing request must not return credentials")
				default:
					t.Errorf("a competitor failed with something other than a rejection: %v", r.err)
				}
			case <-deadline:
				t.Fatal("the competing rotations did not finish within the deadline")
			}
		}
		f.hooks.clear()

		assert.False(t, barrierFailed.Load(), "every competitor must pass the lookup before any claim runs")
		require.Len(t, winners, 1, "exactly one competitor rotates the token")
		assert.Equal(t, rotationCompetitors-1, losers, "every other competitor is told the token is no longer valid")
		winner := winners[0]
		f.assertRotated(t, s, winner)

		// A later replay of the presented token is rejected and cannot alter
		// the committed outcome: the winner's successor stays live and usable.
		f.assertReplayRejected(t, s, rotationRotatedOwner)
		assert.True(t, f.live(t, winner.RefreshToken), "the losers and the replay must not invalidate the winner")
		authenticated, err := f.svc.ValidateToken(winner.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, s.owner.ID, authenticated.ID)

		next, err := f.svc.RefreshAccessToken(winner.RefreshToken)
		require.NoError(t, err, "the winner's successor must rotate normally")
		chained := rotationRotatedOwner
		chained.sessions = 4
		assert.Equal(t, chained, f.state(t, s.owner.ID))
		assert.True(t, f.revoked(t, winner.RefreshToken))
		f.assertCredentials(t, s, next)
		assert.True(t, f.live(t, rotationSibling), "the sibling session is untouched by the race")
		f.assertBystanderIntact(t, s)
	})
}
