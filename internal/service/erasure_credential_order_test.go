package service

// Service roots of #385: the erasure of a user against the credential flows
// of that user (refresh rotation and password-reset confirmation), and the
// governed user delete (UserService.DeleteAs), which nests the erasure in its
// own transaction, against a customer-rooted erasure of the same person.
//
// The erasure locks users before refresh_tokens, password_reset_tokens and
// api_keys, and is replayed when it loses a deadlock. Two inversions remain
// and are exercised here: the reset confirmation claims its token before it
// locks the owner (it is replayed up to resetConfirmMaxRetries times, #407,
// so a deadlock it loses ends in a commit or in the generic refusal, never in
// error 1213), and the governed write holds the target's users row before the
// nested erasure locks leads and customers (either root is replayed).
//
// Interleavings are forced through the repository seams and the erasure
// hooks, and a waiting party is observed in information_schema.PROCESSLIST
// (ciFixture.contend). The MySQL roots use a disposable TEST_MYSQL_DSN
// schema, skip without it at the root only and fail when it is unreachable.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	ecoRefreshToken   = "erasure-order-refresh-token"
	ecoResetToken     = "erasure-order-reset-token"
	ecoErasedPassword = "!erased"
	// What the erasure waits on, as PROCESSLIST shows it.
	ecoUsersLockWait  = "%FROM `users`%FOR UPDATE%"
	ecoResetLockWait  = "%FROM `password_reset_tokens`%FOR UPDATE%"
	ecoLeadsLockWait  = "%FROM `leads`%FOR UPDATE%"
	ecoResetClaimWait = "%UPDATE `password_reset_tokens`%"
	ecoStageLeads     = "leads"
	ecoStageCustomers = "customers"
	ecoStageAPIKeys   = "api_keys"
)

// errECODeadlock is what a hook returns in place of the engine: the text the
// transaction manager recognises as a deadlock.
var errECODeadlock error = &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"}

// ecoSeed gives the fixture's owner a live refresh token, a spendable reset
// token and an API key.
func ecoSeed(t *testing.T, f *ciFixture) ciSeed {
	t.Helper()
	s := f.seed(t)
	ctx := resetContext(t)
	require.NoError(t, f.db.WithContext(ctx).Create(&models.RefreshToken{
		UserID: s.owner.ID, TokenHash: hashOpaqueToken(ecoRefreshToken, ciSecret), ExpiresAt: time.Now().Add(time.Hour),
	}).Error)
	f.earlierToken(t, s.owner.ID, ecoResetToken)
	require.NoError(t, f.db.WithContext(ctx).Create(&models.APIKey{
		Name: "erasure-order", KeyHash: fmt.Sprintf("eco-key-%d", time.Now().UnixNano()), Prefix: "gcrm_eo", UserID: s.owner.ID,
	}).Error)
	return s
}

// ecoErase is the user-rooted erasure of the owner, with hooks.
func ecoErase(f *ciFixture, s ciSeed, hooks repository.ErasureHooks) func() error {
	return func() error {
		return repository.NewUserRepository(f.db, repository.WithUserErasureHooks(hooks)).Delete(s.owner.ID)
	}
}

// ecoParkAfter returns hooks that park the erasure once, after the lock stage
// of table, and count its transactions.
func ecoParkAfter(table string, hold *llHold, transactions *atomic.Int32) repository.ErasureHooks {
	var once sync.Once
	return repository.ErasureHooks{
		BeforeLock: func(stage string) error {
			if stage == ecoStageLeads && transactions != nil {
				transactions.Add(1)
			}
			return nil
		},
		AfterLock: func(stage string) {
			if stage == table {
				once.Do(hold.wait)
			}
		},
	}
}

// assertOwnerErased is the end state of every case: no credential row of the
// owner, an unusable password on a soft-deleted row, and a refresh token the
// rotation may have returned no longer accepted.
func assertOwnerErased(t *testing.T, f *ciFixture, s ciSeed, rotated *AuthTokens) {
	t.Helper()
	ctx := resetContext(t)
	for _, model := range []interface{}{&models.RefreshToken{}, &models.PasswordResetToken{}, &models.APIKey{}} {
		var n int64
		require.NoError(t, f.db.WithContext(ctx).Unscoped().Model(model).Where("user_id = ?", s.owner.ID).Count(&n).Error)
		assert.Zero(t, n, "%T of the erased user must be gone", model)
	}
	var owner models.User
	require.NoError(t, f.db.WithContext(ctx).Unscoped().First(&owner, s.owner.ID).Error)
	assert.Equal(t, ecoErasedPassword, owner.Password, "the password is the unusable placeholder")
	assert.True(t, owner.DeletedAt.Valid, "the users row is soft-deleted")
	assert.True(t, strings.HasSuffix(owner.Email, "@anonymized.invalid"), "the address is a placeholder")
	var customer models.Customer
	require.NoError(t, f.db.WithContext(ctx).Unscoped().First(&customer, s.customer.ID).Error)
	assert.True(t, customer.DeletedAt.Valid, "the linked customer is erased with the login")

	for _, raw := range []string{ecoRefreshToken, func() string {
		if rotated == nil {
			return ""
		}
		return rotated.RefreshToken
	}()} {
		if raw == "" {
			continue
		}
		tokens, err := f.confirmer.RefreshAccessToken(raw)
		assert.Nil(t, tokens, "no session for an erased user")
		assert.ErrorIs(t, err, ErrInvalidRefreshToken, "a refresh token of the erased user is rejected")
	}
}

// ecoNoLockError asserts that err carries no deadlock and no lock wait
// timeout of the engine.
func ecoNoLockError(t *testing.T, err error, what string) {
	t.Helper()
	llNoDeadlock(t, err, what)
	if err != nil {
		assert.NotContains(t, err.Error(), "1205", "%s: no lock wait timeout", what)
	}
}

// TestErasureVsCredentialFlowsSQLite runs both sequential orders of the
// erasure and each credential flow on SQLite's single connection.
func TestErasureVsCredentialFlowsSQLite(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))

	t.Run("refresh rotation, then the erasure", func(t *testing.T) {
		s := ecoSeed(t, f)
		rotated, err := f.confirmer.RefreshAccessToken(ecoRefreshToken)
		require.NoError(t, err)
		require.NotNil(t, rotated)
		require.NoError(t, ecoErase(f, s, repository.ErasureHooks{})())
		assertOwnerErased(t, f, s, rotated)
	})

	t.Run("the erasure, then refresh rotation", func(t *testing.T) {
		s := ecoSeed(t, f)
		require.NoError(t, ecoErase(f, s, repository.ErasureHooks{})())
		rotated, err := f.confirmer.RefreshAccessToken(ecoRefreshToken)
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assertOwnerErased(t, f, s, rotated)
	})

	t.Run("reset confirmation, then the erasure", func(t *testing.T) {
		s := ecoSeed(t, f)
		require.NoError(t, f.confirmer.ConfirmPasswordReset(ecoResetToken, ciNewPassword))
		require.NoError(t, ecoErase(f, s, repository.ErasureHooks{})())
		assertOwnerErased(t, f, s, nil)
	})

	t.Run("the erasure, then reset confirmation", func(t *testing.T) {
		s := ecoSeed(t, f)
		require.NoError(t, ecoErase(f, s, repository.ErasureHooks{})())
		require.ErrorIs(t, f.confirmer.ConfirmPasswordReset(ecoResetToken, ciNewPassword), ErrInvalidResetToken)
		assertOwnerErased(t, f, s, nil)
	})
}

// TestErasureVsCredentialFlowsMySQL interleaves the erasure of a user with a
// refresh rotation and with a reset confirmation of that user.
func TestErasureVsCredentialFlowsMySQL(t *testing.T) {
	f := newCIFixture(t, openMigratedTestMySQL(t, 6))

	type refreshResult struct {
		tokens *AuthTokens
		err    error
	}
	startRefresh := func() <-chan refreshResult {
		ch := make(chan refreshResult, 1)
		go func() {
			tokens, err := f.confirmer.RefreshAccessToken(ecoRefreshToken)
			ch <- refreshResult{tokens, err}
		}()
		return ch
	}
	awaitRefresh := func(t *testing.T, ch <-chan refreshResult) refreshResult {
		t.Helper()
		select {
		case r := <-ch:
			return r
		case <-time.After(resetTimeout):
			t.Fatal("the refresh rotation did not finish")
			return refreshResult{}
		}
	}
	confirm := func() error { return f.confirmer.ConfirmPasswordReset(ecoResetToken, ciNewPassword) }

	t.Run("refresh (a) the rotation commits first and the erasure waits on the users row", func(t *testing.T) {
		s := ecoSeed(t, f)
		hold := newLLHold()
		defer hold.release()
		// The rotation parks after its claim, holding the owner and the token.
		f.parker.arm("refresh_tokens", hold.wait)
		refresh := startRefresh()
		hold.entered(t, "the refresh rotation")
		erase := ciStart(ecoErase(f, s, repository.ErasureHooks{}))
		f.contend(t, nil, erase, ecoUsersLockWait, "the erasure")
		hold.release()
		r := awaitRefresh(t, refresh)
		eraseErr := ciAwait(t, erase, "the erasure")
		require.NoError(t, r.err, "the rotation commits")
		require.NotNil(t, r.tokens)
		require.NoError(t, eraseErr)
		assertOwnerErased(t, f, s, r.tokens)
	})

	t.Run("refresh (b) the erasure holds the users row first and the rotation is refused", func(t *testing.T) {
		s := ecoSeed(t, f)
		hold := newLLHold()
		defer hold.release()
		erase := ciStart(ecoErase(f, s, ecoParkAfter(ecoStageAPIKeys, hold, nil)))
		hold.entered(t, "the erasure")
		fire, reached := llSignal()
		f.otherHooks.install(func(h *ciHookSet) { h.beforeLock = func(uint) error { fire(); return nil } })
		refresh := startRefresh()
		select {
		case <-reached:
		case <-time.After(resetTimeout):
			t.Fatal("the refresh rotation never reached its owner lock")
		}
		f.contend(t, nil, nil, ecoUsersLockWait, "the refresh rotation")
		hold.release()
		eraseErr := ciAwait(t, erase, "the erasure")
		r := awaitRefresh(t, refresh)
		require.NoError(t, eraseErr)
		llNoDeadlock(t, r.err, "the refresh rotation")
		require.ErrorIs(t, r.err, ErrInvalidRefreshToken, "the rotation is refused")
		assertOwnerErased(t, f, s, r.tokens)
	})

	t.Run("confirm (a) the confirmation commits first and the erasure waits on the users row", func(t *testing.T) {
		s := ecoSeed(t, f)
		hold := newLLHold()
		defer hold.release()
		f.otherHooks.install(func(h *ciHookSet) { h.afterRevokeAll = func(uint) error { hold.wait(); return nil } })
		conf := ciStart(confirm)
		hold.entered(t, "the confirmation")
		erase := ciStart(ecoErase(f, s, repository.ErasureHooks{}))
		f.contend(t, nil, erase, ecoUsersLockWait, "the erasure")
		hold.release()
		confErr := ciAwait(t, conf, "the confirmation")
		eraseErr := ciAwait(t, erase, "the erasure")
		require.NoError(t, confErr, "the confirmation commits")
		require.NoError(t, eraseErr)
		assertOwnerErased(t, f, s, nil)
	})

	// On MariaDB 10.11 the confirmation's claim (UPDATE password_reset_tokens
	// ... WHERE id = ? AND used_at IS NULL AND expires_at > ? AND deleted_at IS
	// NULL) is served through idx_password_reset_tokens_deleted_at: it locks
	// that index entry and then waits for the row the erasure holds, and the
	// erasure's hard delete of the row then needs the index entry. The engine
	// rolls the confirmation back (1213) and the confirmation is replayed; its
	// replay finds the token gone. MySQL 8 claims by primary key and the
	// first attempt is refused. The caller sees the same refusal on both.
	t.Run("confirm (b) the erasure holds the users row first and the confirmation is refused", func(t *testing.T) {
		s := ecoSeed(t, f)
		hold := newLLHold()
		defer hold.release()
		erase := ciStart(ecoErase(f, s, ecoParkAfter(ecoStageAPIKeys, hold, nil)))
		hold.entered(t, "the erasure")
		fire, reached := llSignal()
		f.otherHooks.install(func(h *ciHookSet) { h.beforeMarkUsed = func(uint) error { fire(); return nil } })
		conf := ciStart(confirm)
		f.contend(t, reached, conf, ecoResetClaimWait, "the confirmation")
		hold.release()
		eraseErr := ciAwait(t, erase, "the erasure")
		confErr := ciAwait(t, conf, "the confirmation")
		require.NoError(t, eraseErr, "the erasure returns nil")
		ecoNoLockError(t, confErr, "the confirmation")
		require.ErrorIs(t, confErr, ErrInvalidResetToken, "the claim is refused")
		assertOwnerErased(t, f, s, nil)
	})

	t.Run("confirm (c) each holds its first lock: the erasure returns nil and the confirmation commits or is refused", func(t *testing.T) {
		s := ecoSeed(t, f)
		claimed := newLLHold()
		defer claimed.release()
		// The confirmation claims its token and parks before the owner lock.
		// The hold parks once: a replayed attempt passes the released hold.
		f.otherHooks.install(func(h *ciHookSet) { h.beforeLock = func(uint) error { claimed.wait(); return nil } })
		conf := ciStart(confirm)
		claimed.entered(t, "the confirmation")
		// The erasure locks the users row and then asks for the token row.
		var transactions atomic.Int32
		erase := ciStart(ecoErase(f, s, repository.ErasureHooks{BeforeLock: func(stage string) error {
			if stage == ecoStageLeads {
				transactions.Add(1)
			}
			return nil
		}}))
		f.contend(t, nil, erase, ecoResetLockWait, "the erasure")
		// The confirmation now asks for the users row the erasure holds.
		claimed.release()
		confErr := ciAwait(t, conf, "the confirmation")
		eraseErr := ciAwait(t, erase, "the erasure")
		require.NoError(t, eraseErr, "the erasure returns nil in every case")
		t.Logf("erasure transactions: %d, confirmation locking reads: %d, confirmation refused: %v",
			transactions.Load(), f.otherHooks.lockReads(), confErr != nil)

		ecoNoLockError(t, confErr, "the confirmation")
		if confErr != nil {
			// The confirmation was a victim: replayed, it found the token
			// gone or the owner erased. On MariaDB a replayed claim can meet
			// the erasure once more on idx_password_reset_tokens_deleted_at,
			// so neither side's transaction count is exact here.
			require.ErrorIs(t, confErr, ErrInvalidResetToken, "a replayed confirmation is refused")
			assert.GreaterOrEqual(t, transactions.Load(), int32(1))
		} else {
			// The confirmation committed, so the erasure, which held the
			// users row the confirmation needed, lost and was replayed.
			assert.GreaterOrEqual(t, transactions.Load(), int32(2), "the erasure was replayed")
		}
		assertOwnerErased(t, f, s, nil)
	})
}

// TestPasswordResetConfirmRetrySQLite drives the retry of the confirming
// transaction (#407) through the claim seam: beforeMarkUsed fires once per
// attempt, before the conditional claim of that attempt.
func TestPasswordResetConfirmRetrySQLite(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))

	// failing installs a claim hook that fails with err on the first `times`
	// attempts (every one when times is negative) and returns the counter of
	// the attempts.
	failing := func(err error, times int32) *atomic.Int32 {
		var attempts atomic.Int32
		f.otherHooks.install(func(h *ciHookSet) {
			h.beforeMarkUsed = func(uint) error {
				if n := attempts.Add(1); times < 0 || n <= times {
					return err
				}
				return nil
			}
		})
		return &attempts
	}
	liveSessions := func(t *testing.T, userID uint) int64 {
		t.Helper()
		var n int64
		require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.RefreshToken{}).
			Where("user_id = ? AND is_revoked = ?", userID, false).Count(&n).Error)
		return n
	}
	tokenVersion := func(t *testing.T, userID uint) uint {
		t.Helper()
		var owner models.User
		require.NoError(t, f.db.WithContext(resetContext(t)).First(&owner, userID).Error)
		return owner.TokenVersion
	}
	// requireUntouched asserts that a confirmation that returned an error
	// committed nothing: the link is still spendable.
	requireUntouched := func(t *testing.T, s ciSeed, versionBefore uint) {
		t.Helper()
		assert.True(t, f.passwordIs(t, s.owner.ID, ciPassword), "the password is unchanged")
		exists, unused := f.tokenUnused(t, ecoResetToken)
		assert.True(t, exists && unused, "the token is still unused")
		assert.EqualValues(t, 1, liveSessions(t, s.owner.ID), "the session is kept")
		assert.Equal(t, versionBefore, tokenVersion(t, s.owner.ID), "the session generation is unchanged")
	}

	t.Run("(a) a deadlock on the first attempt only: the replay commits the whole reset", func(t *testing.T) {
		s := ecoSeed(t, f)
		versionBefore := tokenVersion(t, s.owner.ID)
		attempts := failing(errECODeadlock, 1)
		require.NoError(t, f.confirmer.ConfirmPasswordReset(ecoResetToken, ciNewPassword))
		assert.EqualValues(t, 2, attempts.Load(), "the failed attempt and its replay")
		assert.Equal(t, 1, f.otherHooks.lockReads(), "only the replay reached the owner lock")
		assert.True(t, f.passwordIs(t, s.owner.ID, ciNewPassword), "the new password is committed")
		exists, unused := f.tokenUnused(t, ecoResetToken)
		assert.True(t, exists, "the token row is kept")
		assert.False(t, unused, "the token is used")
		assert.Zero(t, liveSessions(t, s.owner.ID), "every refresh token is revoked")
		assert.Equal(t, versionBefore+1, tokenVersion(t, s.owner.ID), "the session generation moved once")
	})

	t.Run("(b) a deadlock on every attempt: the error reaches the caller after the bound", func(t *testing.T) {
		s := ecoSeed(t, f)
		versionBefore := tokenVersion(t, s.owner.ID)
		attempts := failing(errECODeadlock, -1)
		err := f.confirmer.ConfirmPasswordReset(ecoResetToken, ciNewPassword)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrInvalidResetToken, "an exhausted bound is not a refusal: the link is still valid")
		assert.ErrorIs(t, err, errECODeadlock, "the last error is returned as it is")
		assert.EqualValues(t, resetConfirmMaxRetries+1, attempts.Load(), "one attempt and the bounded replays")
		requireUntouched(t, s, versionBefore)
	})

	t.Run("(c) an error that is not retryable: one attempt", func(t *testing.T) {
		s := ecoSeed(t, f)
		versionBefore := tokenVersion(t, s.owner.ID)
		errClaim := errors.New("claim failed for another reason")
		attempts := failing(errClaim, -1)
		err := f.confirmer.ConfirmPasswordReset(ecoResetToken, ciNewPassword)
		require.ErrorIs(t, err, errClaim)
		assert.NotErrorIs(t, err, ErrInvalidResetToken)
		assert.EqualValues(t, 1, attempts.Load(), "no replay")
		requireUntouched(t, s, versionBefore)
	})
}

// ecoRootCounter counts the root transactions of a governed write: the
// governance binds its user repository to each one through WithTx, once.
type ecoRootCounter struct {
	repository.UserRepository
	roots *atomic.Int32
}

func (r *ecoRootCounter) WithTx(tx *gorm.DB) repository.UserRepository {
	r.roots.Add(1)
	return r.UserRepository.WithTx(tx)
}

// ecoGoverned is a governed user service whose user repository carries
// erasure hooks, with the rows of one person.
type ecoGoverned struct {
	t  *testing.T
	db *gorm.DB
	// roots counts the root transactions of the governed writes.
	roots    atomic.Int32
	users    UserService
	actor    Actor
	login    *models.User
	customer *models.Customer
	lead     *models.Lead
}

func newECOGoverned(t *testing.T, db *gorm.DB, hooks repository.ErasureHooks) *ecoGoverned {
	t.Helper()
	g := &ecoGoverned{t: t, db: db}
	g.users = NewUserService(
		&ecoRootCounter{UserRepository: repository.NewUserRepository(db, repository.WithUserErasureHooks(hooks)), roots: &g.roots},
		WithUserRoleGovernance(repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db), utils.NewTransactionManager(db)))

	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	admin := &models.User{Email: "eco-admin-" + tag + "@governed.test", Password: "not-a-login-hash",
		FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(admin).Error)
	g.actor = Actor{UserID: admin.ID, AuthMethod: AuthMethodBearer}

	g.login = &models.User{Email: "eco-login-" + tag + "@governed.test", Password: "not-a-login-hash",
		FirstName: "Portal", LastName: "Login", Role: models.RoleCustomer, IsActive: true}
	require.NoError(t, db.Create(g.login).Error)
	require.NoError(t, db.Create(&models.RefreshToken{UserID: g.login.ID, TokenHash: "eco-rt-" + tag, ExpiresAt: time.Now().Add(time.Hour)}).Error)
	require.NoError(t, db.Create(&models.APIKey{Name: "eco", KeyHash: "eco-kh-" + tag, Prefix: "gcrm_eg", UserID: g.login.ID}).Error)

	g.customer = &models.Customer{FirstName: "Linked", LastName: "Customer", Email: "eco-customer-" + tag + "@governed.test", UserID: &g.login.ID}
	require.NoError(t, db.Create(g.customer).Error)
	g.lead = &models.Lead{FirstName: "Linked", LastName: "Lead", Email: "eco-lead-" + tag + "@governed.test",
		Status: models.LeadStatusConverted, OwnerID: admin.ID, CustomerID: &g.customer.ID}
	require.NoError(t, db.Create(g.lead).Error)
	return g
}

func (g *ecoGoverned) credentials() (refresh, keys int64) {
	g.t.Helper()
	require.NoError(g.t, g.db.Unscoped().Model(&models.RefreshToken{}).Where("user_id = ?", g.login.ID).Count(&refresh).Error)
	require.NoError(g.t, g.db.Unscoped().Model(&models.APIKey{}).Where("user_id = ?", g.login.ID).Count(&keys).Error)
	return refresh, keys
}

func (g *ecoGoverned) auditRows() int64 {
	g.t.Helper()
	var n int64
	require.NoError(g.t, g.db.Model(&models.RoleAuditEntry{}).Count(&n).Error)
	return n
}

// requireErased asserts the committed end state of the whole person.
func (g *ecoGoverned) requireErased() {
	g.t.Helper()
	var login models.User
	require.NoError(g.t, g.db.Unscoped().First(&login, g.login.ID).Error)
	assert.True(g.t, login.DeletedAt.Valid, "the login is soft-deleted")
	assert.True(g.t, strings.HasSuffix(login.Email, "@anonymized.invalid"), "the login has a placeholder address")
	assert.Empty(g.t, login.FirstName)
	assert.Empty(g.t, login.LastName)
	assert.Equal(g.t, ecoErasedPassword, login.Password)
	var customer models.Customer
	require.NoError(g.t, g.db.Unscoped().First(&customer, g.customer.ID).Error)
	assert.True(g.t, customer.DeletedAt.Valid, "the customer is soft-deleted")
	assert.True(g.t, strings.HasSuffix(customer.Email, "@anonymized.invalid"), "the customer has a placeholder address")
	assert.Empty(g.t, customer.FirstName)
	var lead models.Lead
	require.NoError(g.t, g.db.Unscoped().First(&lead, g.lead.ID).Error)
	assert.True(g.t, lead.DeletedAt.Valid, "the lead is soft-deleted")
	assert.True(g.t, strings.HasSuffix(lead.Email, "@anonymized.invalid"), "the lead has a placeholder address")
	assert.Empty(g.t, lead.FirstName)
	refresh, keys := g.credentials()
	assert.Zero(g.t, refresh, "no refresh token of the login")
	assert.Zero(g.t, keys, "no API key of the login")
	var resets int64
	require.NoError(g.t, g.db.Unscoped().Model(&models.PasswordResetToken{}).Where("user_id = ?", g.login.ID).Count(&resets).Error)
	assert.Zero(g.t, resets, "no reset token of the login")
}

// requireUnchanged asserts that nothing of the person was written.
func (g *ecoGoverned) requireUnchanged(auditBefore int64) {
	g.t.Helper()
	var login models.User
	require.NoError(g.t, g.db.Unscoped().First(&login, g.login.ID).Error)
	assert.False(g.t, login.DeletedAt.Valid, "the users row is live")
	assert.Equal(g.t, g.login.Email, login.Email)
	assert.Equal(g.t, g.login.FirstName, login.FirstName)
	assert.Equal(g.t, g.login.Password, login.Password)
	assert.True(g.t, login.IsActive)
	var customer models.Customer
	require.NoError(g.t, g.db.Unscoped().First(&customer, g.customer.ID).Error)
	assert.False(g.t, customer.DeletedAt.Valid, "the customers row is live")
	assert.Equal(g.t, g.customer.Email, customer.Email)
	refresh, keys := g.credentials()
	assert.EqualValues(g.t, 1, refresh, "the refresh token is kept")
	assert.EqualValues(g.t, 1, keys, "the API key is kept")
	assert.Equal(g.t, auditBefore, g.auditRows(), "the role audit is unchanged")
}

// TestGovernedWriteReplaysErasureGraphChange drives the nested erasure of a
// governed user delete through its before-lock hook.
func TestGovernedWriteReplaysErasureGraphChange(t *testing.T) {
	ctx := context.Background()

	// failing returns hooks whose leads stage fails with err on the first
	// `times` transactions (every one when times is negative), and the counter
	// of the nested erasure's transactions.
	failing := func(err error, times int32) (repository.ErasureHooks, *atomic.Int32) {
		var attempts atomic.Int32
		return repository.ErasureHooks{BeforeLock: func(stage string) error {
			if stage != ecoStageLeads {
				return nil
			}
			if n := attempts.Add(1); times < 0 || n <= times {
				return err
			}
			return nil
		}}, &attempts
	}

	t.Run("(a) the graph changed once: the root is restarted and the user erased", func(t *testing.T) {
		hooks, attempts := failing(repository.ErrErasureGraphChanged, 1)
		g := newECOGoverned(t, openExternalIDSQLite(t), hooks)
		require.NoError(t, g.users.DeleteAs(ctx, g.actor, g.login.ID))
		assert.EqualValues(t, 2, attempts.Load(), "the failed root and its restart")
		g.requireErased()
	})

	t.Run("(b) the graph changes on every attempt: the error reaches the caller after the bound", func(t *testing.T) {
		hooks, attempts := failing(repository.ErrErasureGraphChanged, -1)
		g := newECOGoverned(t, openExternalIDSQLite(t), hooks)
		auditBefore := g.auditRows()
		err := g.users.DeleteAs(ctx, g.actor, g.login.ID)
		require.Error(t, err)
		require.ErrorIs(t, err, repository.ErrErasureGraphChanged)
		assert.EqualValues(t, roleWriteGraphMaxRestarts+1, attempts.Load(), "one root and the bounded restarts")
		g.requireUnchanged(auditBefore)
	})

	t.Run("(c) a deadlock once: the governed write's own replay still works", func(t *testing.T) {
		hooks, attempts := failing(errECODeadlock, 1)
		g := newECOGoverned(t, openExternalIDSQLite(t), hooks)
		require.NoError(t, g.users.DeleteAs(ctx, g.actor, g.login.ID))
		assert.EqualValues(t, 2, attempts.Load(), "the failed transaction and its replay")
		g.requireErased()
	})
}

// TestGovernedUserErasureVsCustomerErasureMySQL is the inversion that stays:
// the governed delete holds the target's users row before its nested erasure
// locks leads and customers, while a customer-rooted erasure of the same
// person holds leads and customers and asks for users. The engine picks the
// victim, and whichever root it is, that root is replayed and the person is
// erased; no deadlock reaches a caller.
//
// The customer-rooted erasure returns nil in both directions (its replay
// erases the soft-deleted rows again). The governed delete returns nil when
// the other side was the victim. When it is the victim itself, its replay
// starts after the customer-rooted erasure committed and its scoped read of
// the target finds the account already erased: it answers not found, as a
// delete of an erased user always does.
func TestGovernedUserErasureVsCustomerErasureMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 6)
	f := newCIFixture(t, db)

	g := newECOGoverned(t, db, repository.ErasureHooks{})

	// The customer-rooted erasure parks holding leads and customers.
	hold := newLLHold()
	defer hold.release()
	var customerTx atomic.Int32
	customerSide := ciStart(func() error {
		return repository.NewCustomerRepository(db,
			repository.WithCustomerErasureHooks(ecoParkAfter(ecoStageCustomers, hold, &customerTx))).Delete(g.customer.ID)
	})
	hold.entered(t, "the customer-rooted erasure")

	// The governed delete locks the target's users row, then waits for the
	// lead its nested erasure asks for.
	governedSide := ciStart(func() error { return g.users.DeleteAs(context.Background(), g.actor, g.login.ID) })
	f.contend(t, nil, governedSide, ecoLeadsLockWait, "the governed delete")

	// The customer-rooted erasure now asks for the users row: a deadlock.
	hold.release()
	customerErr := ciAwait(t, customerSide, "the customer-rooted erasure")
	governedErr := ciAwait(t, governedSide, "the governed delete")
	t.Logf("transactions: customer-rooted erasure %d, governed delete %d (governed error: %v)",
		customerTx.Load(), g.roots.Load(), governedErr)

	require.NoError(t, customerErr, "the customer-rooted erasure")
	llNoDeadlock(t, governedErr, "the governed delete")
	assert.GreaterOrEqual(t, customerTx.Load()+g.roots.Load(), int32(3), "one side lost the deadlock and was replayed")
	switch {
	case g.roots.Load() > 1:
		// The governed delete was the victim: replayed after the other
		// erasure committed, it finds the account erased.
		require.Error(t, governedErr)
		assert.True(t, apperrors.IsNotFound(governedErr), "the replayed delete finds the user erased, got %v", governedErr)
		assert.EqualValues(t, 1, customerTx.Load(), "the customer-rooted erasure ran once")
	default:
		require.NoError(t, governedErr, "the governed delete")
		assert.EqualValues(t, 2, customerTx.Load(), "the customer-rooted erasure was replayed once")
	}
	g.requireErased()
}
