package service

// Regression roots for issue #336 (C2): on a service built by
// NewAuthServiceWithSessions a login keeps its plain email lookup and bcrypt
// before any transaction, and then issues its session from the owner row read
// by primary key with a locking read inside one WithTransactionAndRetry
// transaction (users → refresh_tokens). A password change, reset,
// deactivation, erasure or logout committed between the lookup and the lock is
// honoured; one committed after the login ends the session it issued. The
// refresh rotation, which already locks the owner, is raced against the same
// credential changes.
//
// The roots run on in-memory SQLite with the production one-connection pool
// and on a disposable MySQL/MariaDB schema built by the real migrations when
// TEST_MYSQL_DSN is set (skipped without it, failing when it points at an
// unreachable server). On SQLite the single connection serialises the two
// parties, so the competing write runs through the instance-local
// loginBeforeLock seam, between the lookup and the transaction; on MySQL the
// first party is parked inside its transaction, the second is seen waiting for
// the row lock in information_schema.PROCESSLIST on two consecutive polls, and
// no ordering may end in error 1213. Every assertion reads committed rows
// outside any transaction; every wait is bounded.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	llPassword    = "LoginLock123!"
	llNewPassword = "LoginLockNew12!"
	llOwnerEmail  = "login-lock-owner@example.com"
	llPresented   = "login-lock-presented-session"
	llBystander   = "login-lock-bystander-session"
	llLockPoll    = 10 * time.Millisecond

	// llSeedSessions is the number of refresh_tokens rows of the seeded owner.
	llSeedSessions = 1

	llInvalidCredentials = "invalid credentials"
	llAccountLocked      = "account is locked due to too many failed login attempts, please try again later"

	// llLockingReadPattern and llUserUpdatePattern match, in
	// information_schema.PROCESSLIST, a locking read and the UPDATE of a users
	// row that waits for the row lock.
	llLockingReadPattern = "%FOR UPDATE%"
	llUserUpdatePattern  = "%UPDATE `users` SET%"
)

// ------------------------------------------------------------------- hooks ---

type llHookSet struct {
	// beforeLock fires before a transaction-bound (locking) owner read, with
	// the handle the repository is bound to.
	beforeLock func(tx *gorm.DB, id uint) error
	// beforeRootUpdate fires before an UpdateFields outside any transaction
	// (UserService.Update, the failure bookkeeping of a login).
	beforeRootUpdate func(id uint)
	beforeCreate     func(token *models.RefreshToken) error
	afterCreate      func(token *models.RefreshToken) error
	afterRevokeAll   func(userID uint) error
}

// llHooks interposes on the repositories one service writes through. Access is
// mutex-guarded because the concurrency roots fire hooks from several
// goroutines.
type llHooks struct {
	mu    sync.Mutex
	set   llHookSet
	locks int
}

func (h *llHooks) install(fn func(s *llHookSet)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(&h.set)
}

func (h *llHooks) current() llHookSet {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.set
}

func (h *llHooks) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.set = llHookSet{}
	h.locks = 0
}

// lockReads counts the transaction-bound owner reads: one per attempt of an
// issuing transaction.
func (h *llHooks) lockReads() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.locks
}

type llUserRepository struct {
	repository.UserRepository
	hooks *llHooks
	tx    *gorm.DB
}

func (r *llUserRepository) WithTx(tx *gorm.DB) repository.UserRepository {
	return &llUserRepository{UserRepository: r.UserRepository.WithTx(tx), hooks: r.hooks, tx: tx}
}

func (r *llUserRepository) GetByID(id uint) (*models.User, error) {
	if r.tx != nil {
		r.hooks.mu.Lock()
		r.hooks.locks++
		r.hooks.mu.Unlock()
		if hook := r.hooks.current().beforeLock; hook != nil {
			if err := hook(r.tx, id); err != nil {
				return nil, err
			}
		}
	}
	return r.UserRepository.GetByID(id)
}

func (r *llUserRepository) UpdateFields(id uint, fields map[string]interface{}) error {
	if r.tx == nil {
		if hook := r.hooks.current().beforeRootUpdate; hook != nil {
			hook(id)
		}
	}
	return r.UserRepository.UpdateFields(id, fields)
}

type llRefreshRepository struct {
	repository.RefreshTokenRepository
	hooks *llHooks
}

func (r *llRefreshRepository) WithTx(tx *gorm.DB) repository.RefreshTokenRepository {
	return &llRefreshRepository{RefreshTokenRepository: r.RefreshTokenRepository.WithTx(tx), hooks: r.hooks}
}

func (r *llRefreshRepository) Create(token *models.RefreshToken) error {
	if hook := r.hooks.current().beforeCreate; hook != nil {
		if err := hook(token); err != nil {
			return err
		}
	}
	if err := r.RefreshTokenRepository.Create(token); err != nil {
		return err
	}
	if hook := r.hooks.current().afterCreate; hook != nil {
		return hook(token)
	}
	return nil
}

func (r *llRefreshRepository) RevokeAllForUser(userID uint) error {
	if err := r.RefreshTokenRepository.RevokeAllForUser(userID); err != nil {
		return err
	}
	if hook := r.hooks.current().afterRevokeAll; hook != nil {
		return hook(userID)
	}
	return nil
}

// llParker parks the first UPDATE of a table issued while it is armed, after
// the statement ran and before its transaction (GORM's default one included)
// commits, so the row lock it took is held while parked. It parks
// UserService.Update and the user erasure, which have no repository seam.
type llParker struct {
	mu    sync.Mutex
	table string
	park  func()
}

func (p *llParker) arm(table string, park func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.table, p.park = table, park
}

func (p *llParker) disarm() { p.arm("", nil) }

func (p *llParker) fire(table string) {
	p.mu.Lock()
	var park func()
	if p.park != nil && p.table == table {
		park, p.park = p.park, nil
	}
	p.mu.Unlock()
	if park != nil {
		park()
	}
}

// ------------------------------------------------------------------ waits ---

// llHold parks one party until release; entered is closed when it got there.
type llHold struct {
	in, out             chan struct{}
	enterOnce, freeOnce sync.Once
}

func newLLHold() *llHold { return &llHold{in: make(chan struct{}), out: make(chan struct{})} }

func (h *llHold) wait() {
	h.enterOnce.Do(func() { close(h.in) })
	select {
	case <-h.out:
	case <-time.After(resetTimeout):
	}
}

func (h *llHold) release() { h.freeOnce.Do(func() { close(h.out) }) }

func (h *llHold) entered(t *testing.T, what string) {
	t.Helper()
	select {
	case <-h.in:
	case <-time.After(resetTimeout):
		t.Fatalf("%s never reached its hold point", what)
	}
}

// llSignal returns a fire-once trigger and the channel it closes.
func llSignal() (func(), <-chan struct{}) {
	ch := make(chan struct{})
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }, ch
}

type llResult struct {
	tokens *AuthTokens
	err    error
}

func llStart(fn func() (*AuthTokens, error)) <-chan llResult {
	ch := make(chan llResult, 1)
	go func() {
		tokens, err := fn()
		ch <- llResult{tokens: tokens, err: err}
	}()
	return ch
}

func llAwait(t *testing.T, ch <-chan llResult, what string) llResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(resetTimeout):
		t.Fatalf("%s did not finish within %s", what, resetTimeout)
		return llResult{}
	}
}

func llNoDeadlock(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		assert.NotContains(t, err.Error(), "1213", "%s: no deadlock", what)
		assert.NotContains(t, strings.ToLower(err.Error()), "deadlock", what)
	}
}

// ----------------------------------------------------------------- fixture ---

type llFixture struct {
	db          *gorm.DB
	userRepo    repository.UserRepository
	refreshRepo repository.RefreshTokenRepository
	// loginHooks interpose on login (the service under test, also used for the
	// rotations), otherHooks on the competitor services.
	loginHooks *llHooks
	otherHooks *llHooks
	login      *authService
	other      *authService
	users      UserService
	mailer     *fakeFormMailer
	parker     *llParker
	// otherHash is a bcrypt hash of another password, written as a change.
	otherHash string
}

func newLLFixture(t *testing.T, db *gorm.DB) *llFixture {
	t.Helper()
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	other, err := bcrypt.GenerateFromPassword([]byte("SomeoneElse12!"), bcrypt.MinCost)
	require.NoError(t, err)
	f := &llFixture{
		db:          db,
		userRepo:    repository.NewUserRepository(db),
		refreshRepo: repository.NewRefreshTokenRepository(db),
		loginHooks:  &llHooks{},
		otherHooks:  &llHooks{},
		mailer:      &fakeFormMailer{},
		parker:      &llParker{},
		otherHash:   string(other),
	}
	require.NoError(t, db.Callback().Update().
		Before("gorm:commit_or_rollback_transaction").After("gorm:update").
		Register("test:login_owner_lock_park", func(tx *gorm.DB) {
			if tx.Error == nil && tx.Statement != nil {
				f.parker.fire(tx.Statement.Table)
			}
		}))
	build := func(h *llHooks) *authService {
		return NewAuthServiceWithSessions(
			&llUserRepository{UserRepository: f.userRepo, hooks: h},
			repository.NewAPIKeyRepository(db),
			&llRefreshRepository{RefreshTokenRepository: f.refreshRepo, hooks: h},
			repository.NewPasswordResetTokenRepository(db),
			utils.NewTransactionManager(db),
			f.mailer,
			config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30},
			resetTestBaseURL, "").(*authService)
	}
	f.login = build(f.loginHooks)
	f.other = build(f.otherHooks)
	f.users = NewUserService(&llUserRepository{UserRepository: f.userRepo, hooks: f.otherHooks})
	return f
}

type llSeed struct {
	owner, bystander *models.User
	// link is a spendable reset token of the owner, issued before the scenario
	// so that its insert never waits for a lock the scenario holds.
	link string
}

func (f *llFixture) exec(t *testing.T, statement string, args ...interface{}) {
	t.Helper()
	require.NoError(t, f.db.WithContext(resetContext(t)).Exec(statement, args...).Error)
}

func (f *llFixture) createUser(t *testing.T, email string, failed int) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(llPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: email, Password: string(hash), FirstName: "Login", LastName: "Lock",
		Role: models.RoleSales, IsActive: true, FailedLoginAttempts: failed}
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(user).Error)
	return user
}

func (f *llFixture) createSession(t *testing.T, userID uint, raw string) {
	t.Helper()
	require.NoError(t, f.refreshRepo.Create(&models.RefreshToken{
		UserID: userID, TokenHash: hashOpaqueToken(raw, resetTestSecret), ExpiresAt: time.Now().Add(24 * time.Hour),
	}))
}

// seed empties the tables and resets every hook and seam. The owner carries
// two earlier failed attempts, so a successful login visibly clears them and a
// refused one visibly leaves them.
func (f *llFixture) seed(t *testing.T) llSeed {
	t.Helper()
	f.loginHooks.reset()
	f.otherHooks.reset()
	f.parker.disarm()
	f.mailer.reset()
	for _, svc := range []*authService{f.login, f.other} {
		svc.signAccessToken = nil
		svc.loginBeforeLock = nil
		svc.loginTime = nil
	}
	ctx := resetContext(t)
	for _, table := range []string{"refresh_tokens", "password_reset_tokens", "api_keys", "users"} {
		require.NoError(t, f.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
	s := llSeed{
		owner:     f.createUser(t, llOwnerEmail, 2),
		bystander: f.createUser(t, "login-lock-bystander@example.com", 0),
	}
	f.createSession(t, s.owner.ID, llPresented)
	f.createSession(t, s.bystander.ID, llBystander)

	require.NoError(t, f.other.RequestPasswordReset(llOwnerEmail))
	messages := f.mailer.messages()
	require.Len(t, messages, 1)
	idx := strings.Index(messages[0].Body, "token=")
	require.Positive(t, idx)
	s.link = messages[0].Body[idx+len("token="):]
	f.mailer.reset()
	return s
}

// llAccount is the committed login state of an account.
type llAccount struct {
	LastLoginAt         *time.Time
	FailedLoginAttempts int
	LockedUntil         *time.Time
	Sessions            int64
}

// llSessionsAfter is the owner's refresh_tokens row count after a competitor
// committed and a login or rotation that followed it issued nothing.
func llSessionsAfter(c llCompetitor) int64 {
	if c.ends == llEndsByErasure {
		return 0
	}
	return llSeedSessions
}

func (f *llFixture) account(t *testing.T, userID uint) llAccount {
	t.Helper()
	ctx := resetContext(t)
	var user models.User
	require.NoError(t, f.db.WithContext(ctx).Unscoped().First(&user, userID).Error)
	a := llAccount{LastLoginAt: user.LastLoginAt, FailedLoginAttempts: user.FailedLoginAttempts, LockedUntil: user.LockedUntil}
	require.NoError(t, f.db.WithContext(ctx).Model(&models.RefreshToken{}).Where("user_id = ?", userID).Count(&a.Sessions).Error)
	return a
}

func (f *llFixture) version(t *testing.T, userID uint) uint {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&user, userID).Error)
	return user.TokenVersion
}

// token reports whether a refresh token row exists and whether it is live.
func (f *llFixture) token(t *testing.T, raw string) (exists, live bool) {
	t.Helper()
	var row models.RefreshToken
	err := f.db.WithContext(resetContext(t)).Where("token_hash = ?", hashOpaqueToken(raw, resetTestSecret)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false
	}
	require.NoError(t, err)
	return true, !row.Revoked && row.ExpiresAt.After(time.Now())
}

// contend waits until the second party is blocked on the row lock the first
// holds: it reached its statement (when reached is given) and the server
// reports a statement matching pattern still executing on two consecutive
// polls of information_schema.PROCESSLIST (MySQL 8 and MariaDB 10.11 alike).
func (f *llFixture) contend(t *testing.T, reached <-chan struct{}, ch <-chan llResult, pattern, what string) {
	t.Helper()
	if reached != nil {
		select {
		case <-reached:
		case r := <-ch:
			t.Fatalf("%s finished before reaching its locking statement: %v", what, r.err)
		case <-time.After(resetTimeout):
			t.Fatalf("%s never reached its locking statement", what)
		}
	}
	deadline := time.Now().Add(resetTimeout)
	seen := 0
	for seen < 2 {
		var waiting int64
		require.NoError(t, f.db.WithContext(resetContext(t)).Raw(`SELECT COUNT(*)
			FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
			AND INFO LIKE ?`, pattern).Scan(&waiting).Error)
		if waiting > 0 {
			seen++
		} else {
			seen = 0
		}
		select {
		case r := <-ch:
			t.Fatalf("%s finished without waiting for the row lock: %v", what, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never reported waiting for the row lock", what)
		}
		time.Sleep(llLockPoll)
	}
}

// --------------------------------------------------------- the competitors ---

// The ways a session issued before the competing write ends.
const (
	llEndsByRevocation   = "revocation"   // refresh token revoked, access token version mismatch
	llEndsByErasure      = "erasure"      // refresh token row deleted, owner not found
	llEndsByDeactivation = "deactivation" // nothing revoked, owner inactive
)

type llCompetitor struct {
	name string
	// ends is how a session issued before the competitor ends.
	ends string
	// allowsLogin is true when a login after the competitor still succeeds.
	allowsLogin bool
	run         func(f *llFixture, s llSeed) error
	// park arms the hold inside the competitor's transaction, after it took
	// the owner's row lock.
	park func(f *llFixture, h *llHold)
	// waitPattern is the statement the competitor waits in on MySQL while the
	// login or rotation holds the owner row; progress, when set, signals that
	// the competitor reached it.
	waitPattern string
	progress    func(f *llFixture) <-chan struct{}
}

func llParkAfterRevokeAll(f *llFixture, h *llHold) {
	f.otherHooks.install(func(s *llHookSet) {
		s.afterRevokeAll = func(uint) error { h.wait(); return nil }
	})
}

func llParkOnUsersUpdate(f *llFixture, h *llHold) { f.parker.arm("users", h.wait) }

func llOtherLockProgress(f *llFixture) <-chan struct{} {
	fire, ch := llSignal()
	f.otherHooks.install(func(s *llHookSet) {
		s.beforeLock = func(*gorm.DB, uint) error { fire(); return nil }
	})
	return ch
}

func llOtherUpdateProgress(f *llFixture) <-chan struct{} {
	fire, ch := llSignal()
	f.otherHooks.install(func(s *llHookSet) {
		s.beforeRootUpdate = func(uint) { fire() }
	})
	return ch
}

// The competing writes, through the services production routes call.

func llRunChange(f *llFixture, s llSeed) error {
	_, err := f.other.ChangePassword(s.owner.ID, llPassword, llNewPassword, "")
	return err
}

func llRunReset(f *llFixture, s llSeed) error {
	return f.other.ConfirmPasswordReset(s.link, llNewPassword)
}

func llRunDeactivate(f *llFixture, s llSeed) error {
	_, err := f.users.Update(s.owner.ID, map[string]interface{}{"is_active": false})
	return err
}

// llRunErase is the DELETE /users/{id} path: UserService.Delete.
func llRunErase(f *llFixture, s llSeed) error {
	return f.users.Delete(context.Background(), s.owner.ID)
}

func llRunLogout(f *llFixture, s llSeed) error {
	return f.other.Logout(s.owner.ID, "")
}

var (
	llChange = llCompetitor{
		name:        "a password change",
		ends:        llEndsByRevocation,
		run:         llRunChange,
		park:        llParkAfterRevokeAll,
		waitPattern: llLockingReadPattern,
		progress:    llOtherLockProgress,
	}
	llReset = llCompetitor{
		name:        "a reset confirmation",
		ends:        llEndsByRevocation,
		run:         llRunReset,
		park:        llParkAfterRevokeAll,
		waitPattern: llLockingReadPattern,
		progress:    llOtherLockProgress,
	}
	llDeactivate = llCompetitor{
		name:        "a deactivation",
		ends:        llEndsByDeactivation,
		run:         llRunDeactivate,
		park:        llParkOnUsersUpdate,
		waitPattern: llUserUpdatePattern,
		progress:    llOtherUpdateProgress,
	}
	llErase = llCompetitor{
		name:        "a user-rooted erasure",
		ends:        llEndsByErasure,
		run:         llRunErase,
		park:        llParkOnUsersUpdate,
		waitPattern: llLockingReadPattern,
	}
	llLogout = llCompetitor{
		name:        "a logout without a body",
		ends:        llEndsByRevocation,
		allowsLogin: true,
		run:         llRunLogout,
		park:        llParkAfterRevokeAll,
		waitPattern: llLockingReadPattern,
		progress:    llOtherLockProgress,
	}
)

// assertEnded checks that a session issued before the competitor committed is
// over, in the way that competitor ends sessions.
func (f *llFixture) assertEnded(t *testing.T, c llCompetitor, issued *AuthTokens) {
	t.Helper()
	require.NotNil(t, issued)
	require.NotEmpty(t, issued.RefreshToken)
	exists, live := f.token(t, issued.RefreshToken)
	user, err := f.login.ValidateToken(issued.AccessToken)
	assert.Nil(t, user, "the access token issued before %s is refused", c.name)
	switch c.ends {
	case llEndsByRevocation:
		assert.True(t, exists)
		assert.False(t, live, "%s revokes the refresh token issued before it", c.name)
		assert.ErrorIs(t, err, ErrAccessTokenRevoked, "token_version mismatch")
	case llEndsByErasure:
		assert.False(t, exists, "the erasure deletes the refresh token row issued before it")
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "the owner is gone")
	case llEndsByDeactivation:
		assert.True(t, live, "a deactivation revokes no refresh token")
		assert.EqualError(t, err, "user account is deactivated")
		owner := issued.User.ID
		before := f.account(t, owner).Sessions
		rotated, err := f.login.RefreshAccessToken(issued.RefreshToken)
		assert.Nil(t, rotated)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken, "the refresh token of a deactivated owner is refused")
		assert.Equal(t, before, f.account(t, owner).Sessions, "and no successor is inserted")
	default:
		t.Fatalf("unknown ending %q", c.ends)
	}
}

// ---------------------------------------------------------- AC-1 decisions ---

func TestLoginOwnerLockDecisionsSQLite(t *testing.T) {
	runLoginOwnerLockDecisions(t, newLLFixture(t, openResetTestSQLite(t)), 1)
}

func TestLoginOwnerLockDecisionsMySQL(t *testing.T) {
	runLoginOwnerLockDecisions(t, newLLFixture(t, openMigratedTestMySQL(t, 3)), 3)
}

type llEntry struct {
	name    string
	refresh bool
	call    func(svc *authService) (*AuthTokens, error)
}

var llEntries = []llEntry{
	{name: "LoginWithTokens", refresh: true, call: func(svc *authService) (*AuthTokens, error) {
		return svc.LoginWithTokens(llOwnerEmail, llPassword)
	}},
	{name: "Login", call: func(svc *authService) (*AuthTokens, error) {
		token, err := svc.Login(llOwnerEmail, llPassword)
		if err != nil {
			return nil, err
		}
		return &AuthTokens{AccessToken: token}, nil
	}},
}

func runLoginOwnerLockDecisions(t *testing.T, f *llFixture, connections int) {
	refusals := []struct {
		name  string
		apply func(t *testing.T, s llSeed)
		want  string
	}{
		{name: "a changed password hash", want: llInvalidCredentials, apply: func(t *testing.T, s llSeed) {
			f.exec(t, "UPDATE users SET password = ? WHERE id = ?", f.otherHash, s.owner.ID)
		}},
		{name: "a deactivation", want: llInvalidCredentials, apply: func(t *testing.T, s llSeed) {
			_, err := f.users.Update(s.owner.ID, map[string]interface{}{"is_active": false})
			require.NoError(t, err)
		}},
		{name: "an erasure", want: llInvalidCredentials, apply: func(t *testing.T, s llSeed) {
			require.NoError(t, f.users.Delete(context.Background(), s.owner.ID))
		}},
		{name: "a lock later than the login clock", want: llAccountLocked, apply: func(t *testing.T, s llSeed) {
			f.exec(t, "UPDATE users SET failed_login_attempts = ?, locked_until = ? WHERE id = ?",
				MaxFailedLoginAttempts, time.Now().Add(10*time.Minute), s.owner.ID)
		}},
	}

	for _, entry := range llEntries {
		t.Run(entry.name, func(t *testing.T) {
			for _, refusal := range refusals {
				t.Run(refusal.name+" committed after the lookup refuses the login and writes nothing", func(t *testing.T) {
					s := f.seed(t)
					var before llAccount
					applied := false
					f.login.loginBeforeLock = func(id uint) {
						if id != s.owner.ID || applied {
							return
						}
						applied = true
						refusal.apply(t, s)
						before = f.account(t, id)
					}
					tokens, err := entry.call(f.login)
					f.login.loginBeforeLock = nil
					require.True(t, applied, "the change must land between the lookup and the lock")
					assert.Nil(t, tokens)
					require.EqualError(t, err, refusal.want)
					assert.Equal(t, before, f.account(t, s.owner.ID),
						"no refresh token, and last_login_at and the lockout columns as stored")
					assert.Equal(t, 1, f.loginHooks.lockReads(), "the decision was taken on the locked row, once")
				})
			}

			t.Run("a token_version incremented after the lookup is the value signed", func(t *testing.T) {
				s := f.seed(t)
				f.login.loginBeforeLock = func(id uint) {
					f.exec(t, "UPDATE users SET token_version = ? WHERE id = ?", 7, id)
				}
				tokens, err := entry.call(f.login)
				f.login.loginBeforeLock = nil
				require.NoError(t, err)
				assert.Equal(t, float64(7), claimOf(t, tokens.AccessToken))
				user, err := f.login.ValidateToken(tokens.AccessToken)
				require.NoError(t, err, "ValidateToken accepts the token signed from the locked row")
				assert.Equal(t, s.owner.ID, user.ID)
				assert.Equal(t, uint(7), f.version(t, s.owner.ID), "login never changes token_version")

				after := f.account(t, s.owner.ID)
				assert.NotNil(t, after.LastLoginAt)
				assert.Zero(t, after.FailedLoginAttempts, "the failure counter is cleared")
				assert.Nil(t, after.LockedUntil)
				if entry.refresh {
					assert.EqualValues(t, llSeedSessions+1, after.Sessions)
					_, live := f.token(t, tokens.RefreshToken)
					assert.True(t, live)
				} else {
					assert.EqualValues(t, llSeedSessions, after.Sessions, "Login never issues a refresh token")
					assert.Empty(t, tokens.RefreshToken)
				}
			})

			t.Run("an expired lock is cleared with the last-login write", func(t *testing.T) {
				s := f.seed(t)
				f.exec(t, "UPDATE users SET failed_login_attempts = ?, locked_until = ? WHERE id = ?",
					MaxFailedLoginAttempts, time.Now().Add(-time.Minute), s.owner.ID)
				tokens, err := entry.call(f.login)
				require.NoError(t, err)
				require.NotEmpty(t, tokens.AccessToken)
				after := f.account(t, s.owner.ID)
				assert.NotNil(t, after.LastLoginAt)
				assert.Zero(t, after.FailedLoginAttempts)
				assert.Nil(t, after.LockedUntil)
			})
		})
	}

	if connections > 1 {
		t.Run("a change committed by another connection after the transaction's snapshot is still honoured", func(t *testing.T) {
			s := f.seed(t)
			var stale, fired atomic.Bool
			f.loginHooks.install(func(h *llHookSet) {
				h.beforeLock = func(tx *gorm.DB, id uint) error {
					if id != s.owner.ID || fired.Swap(true) {
						return nil
					}
					// A plain read establishes the REPEATABLE READ snapshot,
					// another connection then commits a password change, and
					// a second plain read still answers from the snapshot.
					plain := tx.Session(&gorm.Session{NewDB: true})
					var first, second models.User
					if err := plain.Select("id", "password").First(&first, id).Error; err != nil {
						return err
					}
					if err := f.db.WithContext(resetContext(t)).Model(&models.User{}).
						Where("id = ?", id).Update("password", f.otherHash).Error; err != nil {
						return err
					}
					if err := plain.Select("id", "password").First(&second, id).Error; err != nil {
						return err
					}
					stale.Store(second.Password == first.Password && second.Password != f.otherHash)
					return nil
				}
			})
			before := f.account(t, s.owner.ID)
			tokens, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
			require.True(t, fired.Load())
			assert.True(t, stale.Load(), "a plain read would have returned the stale hash")
			assert.Nil(t, tokens)
			require.EqualError(t, err, llInvalidCredentials, "the locking read returns the committed hash")
			assert.Equal(t, before, f.account(t, s.owner.ID))
		})
	}
}

// --------------------------------------------------------- AC-2 atomicity ---

func TestLoginOwnerLockAtomicitySQLite(t *testing.T) {
	runLoginOwnerLockAtomicity(t, newLLFixture(t, openResetTestSQLite(t)),
		&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"})
}

func TestLoginOwnerLockAtomicityMySQL(t *testing.T) {
	runLoginOwnerLockAtomicity(t, newLLFixture(t, openMigratedTestMySQL(t, 2)),
		&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"})
}

func runLoginOwnerLockAtomicity(t *testing.T, f *llFixture, retryable error) {
	injected := errors.New("injected failure")

	fallbacks := map[string]func(h *llHookSet){
		"before its insert": func(h *llHookSet) {
			h.beforeCreate = func(*models.RefreshToken) error { return injected }
		},
		"after its insert": func(h *llHookSet) {
			h.afterCreate = func(*models.RefreshToken) error { return injected }
		},
	}
	for name, install := range fallbacks {
		t.Run("a refresh insert failing "+name+" rolls back to its savepoint and answers an access token only", func(t *testing.T) {
			s := f.seed(t)
			f.loginHooks.install(install)
			tokens, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
			require.NoError(t, err)
			require.NotNil(t, tokens)
			assert.Empty(t, tokens.RefreshToken, "the documented JWT-only fallback")
			user, err := f.login.ValidateToken(tokens.AccessToken)
			require.NoError(t, err)
			assert.Equal(t, s.owner.ID, user.ID)

			after := f.account(t, s.owner.ID)
			assert.EqualValues(t, llSeedSessions, after.Sessions, "no refresh_tokens row survives the savepoint")
			assert.NotNil(t, after.LastLoginAt, "last_login_at commits")
			assert.Zero(t, after.FailedLoginAttempts)
		})
	}

	t.Run("a signing failure returns an error and writes nothing", func(t *testing.T) {
		s := f.seed(t)
		f.exec(t, "UPDATE users SET locked_until = ? WHERE id = ?", time.Now().Add(-time.Minute), s.owner.ID)
		before := f.account(t, s.owner.ID)
		f.login.signAccessToken = func(*models.User) (string, error) { return "", injected }
		for _, entry := range llEntries {
			tokens, err := entry.call(f.login)
			assert.Nil(t, tokens, entry.name)
			assert.ErrorIs(t, err, injected, entry.name)
			assert.Equal(t, before, f.account(t, s.owner.ID),
				"%s: last_login_at, the lockout columns and refresh_tokens unchanged", entry.name)
		}
	})

	t.Run("a retryable failure on the first attempt is replayed in a fresh root transaction", func(t *testing.T) {
		s := f.seed(t)
		var calls atomic.Int32
		f.loginHooks.install(func(h *llHookSet) {
			h.beforeCreate = func(*models.RefreshToken) error {
				if calls.Add(1) == 1 {
					return retryable
				}
				return nil
			}
		})
		tokens, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
		require.NoError(t, err)
		require.NotEmpty(t, tokens.RefreshToken, "the replay issues the refresh token")
		assert.Equal(t, 2, f.loginHooks.lockReads(), "two attempts, each locking the owner")
		assert.EqualValues(t, 2, calls.Load())

		after := f.account(t, s.owner.ID)
		assert.EqualValues(t, llSeedSessions+1, after.Sessions, "exactly one refresh_tokens row")
		_, live := f.token(t, tokens.RefreshToken)
		assert.True(t, live)
		assert.NotNil(t, after.LastLoginAt, "the replay's last_login_at write is the one committed")
		assert.Zero(t, after.FailedLoginAttempts)
	})

	t.Run("a retryable failure on every attempt stops after the retry bound and writes nothing", func(t *testing.T) {
		s := f.seed(t)
		before := f.account(t, s.owner.ID)
		f.loginHooks.install(func(h *llHookSet) {
			h.beforeCreate = func(*models.RefreshToken) error { return retryable }
		})
		tokens, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
		assert.Nil(t, tokens)
		require.Error(t, err)
		assert.ErrorIs(t, err, utils.ErrNestedRetryDeferred)
		assert.Equal(t, loginMaxRetries+1, f.loginHooks.lockReads())
		assert.Equal(t, before, f.account(t, s.owner.ID))
	})
}

// ---------------------------------------------- AC-3 login vs credentials ---

func TestLoginConcurrentCredentialChangesSQLite(t *testing.T) {
	runLoginConcurrentCredentialChanges(t, newLLFixture(t, openResetTestSQLite(t)), false)
}

func TestLoginConcurrentCredentialChangesMySQL(t *testing.T) {
	runLoginConcurrentCredentialChanges(t, newLLFixture(t, openMigratedTestMySQL(t, 4)), true)
}

func runLoginConcurrentCredentialChanges(t *testing.T, f *llFixture, contended bool) {
	login := func() (*AuthTokens, error) { return f.login.LoginWithTokens(llOwnerEmail, llPassword) }

	for _, c := range []llCompetitor{llChange, llReset, llDeactivate, llErase, llLogout} {
		c := c
		t.Run(c.name+" committed first", func(t *testing.T) {
			s := f.seed(t)
			var r llResult
			if !contended {
				// One pooled connection: the competitor commits between the
				// login's lookup and its transaction.
				ran := false
				f.login.loginBeforeLock = func(id uint) {
					if id == s.owner.ID && !ran {
						ran = true
						require.NoError(t, c.run(f, s))
					}
				}
				r.tokens, r.err = login()
				f.login.loginBeforeLock = nil
				require.True(t, ran)
			} else {
				hold := newLLHold()
				defer hold.release()
				c.park(f, hold)
				competitor := llStart(func() (*AuthTokens, error) { return nil, c.run(f, s) })
				hold.entered(t, c.name)
				fire, reached := llSignal()
				f.loginHooks.install(func(h *llHookSet) {
					h.beforeLock = func(*gorm.DB, uint) error { fire(); return nil }
				})
				lg := llStart(login)
				f.contend(t, reached, lg, llLockingReadPattern, "the login")
				hold.release()
				done := llAwait(t, competitor, c.name)
				r = llAwait(t, lg, "the login")
				llNoDeadlock(t, done.err, c.name)
				require.NoError(t, done.err, "%s commits first", c.name)
			}
			llNoDeadlock(t, r.err, "the login")

			if !c.allowsLogin {
				assert.Nil(t, r.tokens)
				require.EqualError(t, r.err, llInvalidCredentials, "the login decides on the committed row")
				after := f.account(t, s.owner.ID)
				assert.EqualValues(t, llSessionsAfter(c), after.Sessions, "the login issues nothing")
				assert.Nil(t, after.LastLoginAt)
				return
			}
			require.NoError(t, r.err)
			assert.Equal(t, uint(1), f.version(t, s.owner.ID))
			assert.Equal(t, float64(1), claimOf(t, r.tokens.AccessToken), "signed from the incremented token_version")
			user, err := f.login.ValidateToken(r.tokens.AccessToken)
			require.NoError(t, err)
			assert.Equal(t, s.owner.ID, user.ID)
			_, live := f.token(t, r.tokens.RefreshToken)
			assert.True(t, live)
		})

		t.Run("login committed before "+c.name, func(t *testing.T) {
			s := f.seed(t)
			var r llResult
			if !contended {
				r.tokens, r.err = login()
				require.NoError(t, r.err)
				require.NoError(t, c.run(f, s))
			} else {
				hold := newLLHold()
				defer hold.release()
				f.loginHooks.install(func(h *llHookSet) {
					h.afterCreate = func(*models.RefreshToken) error { hold.wait(); return nil }
				})
				lg := llStart(login)
				hold.entered(t, "the login")
				var reached <-chan struct{}
				if c.progress != nil {
					reached = c.progress(f)
				}
				competitor := llStart(func() (*AuthTokens, error) { return nil, c.run(f, s) })
				f.contend(t, reached, competitor, c.waitPattern, c.name)
				hold.release()
				r = llAwait(t, lg, "the login")
				done := llAwait(t, competitor, c.name)
				llNoDeadlock(t, r.err, "the login")
				llNoDeadlock(t, done.err, c.name)
				require.NoError(t, r.err, "the login commits first")
				require.NoError(t, done.err, "%s waits for the owner lock and commits", c.name)
			}
			f.assertEnded(t, c, r.tokens)
		})
	}
}

// ---------------------------------------------- AC-4 rotation vs credentials ---

func TestRefreshRotationConcurrentCredentialChangesSQLite(t *testing.T) {
	runRotationConcurrentCredentialChanges(t, newLLFixture(t, openResetTestSQLite(t)), false)
}

func TestRefreshRotationConcurrentCredentialChangesMySQL(t *testing.T) {
	runRotationConcurrentCredentialChanges(t, newLLFixture(t, openMigratedTestMySQL(t, 4)), true)
}

func runRotationConcurrentCredentialChanges(t *testing.T, f *llFixture, contended bool) {
	rotate := func() (*AuthTokens, error) { return f.login.RefreshAccessToken(llPresented) }

	for _, c := range []llCompetitor{llReset, llDeactivate, llErase} {
		c := c
		t.Run(c.name+" committed first", func(t *testing.T) {
			s := f.seed(t)
			var r llResult
			if !contended {
				require.NoError(t, c.run(f, s))
				r.tokens, r.err = rotate()
			} else {
				hold := newLLHold()
				defer hold.release()
				c.park(f, hold)
				competitor := llStart(func() (*AuthTokens, error) { return nil, c.run(f, s) })
				hold.entered(t, c.name)
				fire, reached := llSignal()
				f.loginHooks.install(func(h *llHookSet) {
					h.beforeLock = func(*gorm.DB, uint) error { fire(); return nil }
				})
				rot := llStart(rotate)
				f.contend(t, reached, rot, llLockingReadPattern, "the rotation")
				hold.release()
				done := llAwait(t, competitor, c.name)
				r = llAwait(t, rot, "the rotation")
				llNoDeadlock(t, done.err, c.name)
				require.NoError(t, done.err, "%s commits first", c.name)
			}
			llNoDeadlock(t, r.err, "the rotation")
			assert.Nil(t, r.tokens)
			assert.ErrorIs(t, r.err, ErrInvalidRefreshToken)
			assert.EqualValues(t, llSessionsAfter(c), f.account(t, s.owner.ID).Sessions, "no successor is inserted")
		})

		t.Run("rotation committed before "+c.name, func(t *testing.T) {
			s := f.seed(t)
			var r llResult
			if !contended {
				r.tokens, r.err = rotate()
				require.NoError(t, r.err)
				require.NoError(t, c.run(f, s))
			} else {
				hold := newLLHold()
				defer hold.release()
				f.loginHooks.install(func(h *llHookSet) {
					h.afterCreate = func(*models.RefreshToken) error { hold.wait(); return nil }
				})
				rot := llStart(rotate)
				hold.entered(t, "the rotation")
				var reached <-chan struct{}
				if c.progress != nil {
					reached = c.progress(f)
				}
				competitor := llStart(func() (*AuthTokens, error) { return nil, c.run(f, s) })
				f.contend(t, reached, competitor, c.waitPattern, c.name)
				hold.release()
				r = llAwait(t, rot, "the rotation")
				done := llAwait(t, competitor, c.name)
				llNoDeadlock(t, r.err, "the rotation")
				llNoDeadlock(t, done.err, c.name)
				require.NoError(t, r.err, "the rotation commits first")
				require.NoError(t, done.err, "%s waits for the owner lock and commits", c.name)
			}
			f.assertEnded(t, c, r.tokens)
		})
	}
}

// --------------------------------------------------------------- AC-5 logs ---

func TestLoginOwnerLockLogsOmitPersonalData(t *testing.T) {
	f := newLLFixture(t, openResetTestSQLite(t))
	s := f.seed(t)
	var owner models.User
	require.NoError(t, f.db.WithContext(resetContext(t)).First(&owner, s.owner.ID).Error)
	originalHash := owner.Password

	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })
	utils.Logger.Info("ll-baseline")

	// Successful logins under the lock.
	first, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
	require.NoError(t, err)
	plain, err := f.login.Login(llOwnerEmail, llPassword)
	require.NoError(t, err)

	// The refresh fallback.
	f.loginHooks.install(func(h *llHookSet) {
		h.beforeCreate = func(*models.RefreshToken) error { return errors.New("injected failure") }
	})
	fallback, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
	require.NoError(t, err)
	require.Empty(t, fallback.RefreshToken)

	// The replay.
	var calls atomic.Int32
	f.loginHooks.install(func(h *llHookSet) {
		h.beforeCreate = func(*models.RefreshToken) error {
			if calls.Add(1) == 1 {
				return &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"}
			}
			return nil
		}
	})
	replayed, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
	require.NoError(t, err)
	require.NotEmpty(t, replayed.RefreshToken)
	f.loginHooks.reset()

	// Refusals under the lock, each followed by the restoration of the row.
	refuse := func(change, restore func(id uint), want string) {
		t.Helper()
		f.login.loginBeforeLock = change
		_, err := f.login.LoginWithTokens(llOwnerEmail, llPassword)
		f.login.loginBeforeLock = nil
		require.EqualError(t, err, want)
		if restore != nil {
			restore(s.owner.ID)
		}
	}
	refuse(func(id uint) { f.exec(t, "UPDATE users SET password = ? WHERE id = ?", f.otherHash, id) },
		func(id uint) { f.exec(t, "UPDATE users SET password = ? WHERE id = ?", originalHash, id) }, llInvalidCredentials)
	refuse(func(id uint) { f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, id) },
		func(id uint) { f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", true, id) }, llInvalidCredentials)
	refuse(func(id uint) {
		f.exec(t, "UPDATE users SET locked_until = ? WHERE id = ?", time.Now().Add(10*time.Minute), id)
	}, func(id uint) { f.exec(t, "UPDATE users SET locked_until = NULL WHERE id = ?", id) }, llAccountLocked)
	refuse(func(id uint) { require.NoError(t, f.users.Delete(context.Background(), id)) }, nil, llInvalidCredentials)

	var baseline map[string]interface{}
	var loginLines []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["msg"] == "ll-baseline" {
			baseline = entry
		}
		if entry["operation"] == "Login" || entry["method"] == "Login" {
			loginLines = append(loginLines, entry)
		}
	}
	require.NotNil(t, baseline)
	allowed := map[string]bool{"user_id": true, "operation": true, "error_code": true}
	for key := range baseline {
		allowed[key] = true
	}

	secrets := []string{
		llOwnerEmail, llPassword, originalHash, f.otherHash, llPresented, s.link,
		first.AccessToken, first.RefreshToken, plain, fallback.AccessToken,
		replayed.AccessToken, replayed.RefreshToken,
	}
	codes := map[string]int{}
	successes := 0
	for _, entry := range loginLines {
		encoded, err := json.Marshal(entry)
		require.NoError(t, err)
		for _, secret := range secrets {
			assert.NotContains(t, string(encoded), secret)
		}
		if entry["operation"] != "Login" {
			continue
		}
		for key := range entry {
			assert.True(t, allowed[key], "%q carries only user_id, operation and error_code, not %q", entry["msg"], key)
		}
		assert.EqualValues(t, s.owner.ID, entry["user_id"])
		if code, ok := entry["error_code"].(string); ok {
			codes[code]++
		}
		if entry["msg"] == "User logged in successfully" {
			successes++
		}
	}
	assert.Equal(t, 4, successes, "one line per issued login")
	for _, code := range []string{"password_changed", "account_inactive", "account_locked", "account_erased", "refresh_token_insert_failed"} {
		assert.Equal(t, 1, codes[code], "one %s line", code)
	}

	// No line anywhere carries a password, a hash or a token.
	text := output.String()
	for _, secret := range []string{llPassword, originalHash, f.otherHash, first.AccessToken, first.RefreshToken,
		plain, fallback.AccessToken, replayed.AccessToken, replayed.RefreshToken} {
		assert.NotContains(t, text, secret)
	}
}

// ----------------------------------------------------- AC-6 unwired service ---

// llUnwiredUserRepository is the only repository of a NewAuthService login.
// It records every call; the embedded interface is nil, so a call the legacy
// path never made panics.
type llUnwiredUserRepository struct {
	repository.UserRepository
	stored models.User
	found  bool
	calls  []string
}

func (r *llUnwiredUserRepository) GetByEmail(string) (*models.User, error) {
	r.calls = append(r.calls, "GetByEmail")
	if !r.found {
		return nil, gorm.ErrRecordNotFound
	}
	copy := r.stored
	return &copy, nil
}

func (r *llUnwiredUserRepository) GetByID(uint) (*models.User, error) {
	r.calls = append(r.calls, "GetByID")
	copy := r.stored
	return &copy, nil
}

func (r *llUnwiredUserRepository) WithTx(*gorm.DB) repository.UserRepository {
	r.calls = append(r.calls, "WithTx")
	return r
}

func (r *llUnwiredUserRepository) UpdateFields(_ uint, fields map[string]interface{}) error {
	r.calls = append(r.calls, "UpdateFields")
	for column, value := range fields {
		switch column {
		case "failed_login_attempts":
			r.stored.FailedLoginAttempts = value.(int)
		case "locked_until":
			if value == nil {
				r.stored.LockedUntil = nil
			} else {
				until := *value.(*time.Time)
				r.stored.LockedUntil = &until
			}
		default:
			return errors.New("unexpected column " + column)
		}
	}
	return nil
}

func (r *llUnwiredUserRepository) UpdateLastLogin(uint) error {
	r.calls = append(r.calls, "UpdateLastLogin")
	return nil
}

func TestLoginUnwiredServiceUnchanged(t *testing.T) {
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	hash, err := bcrypt.GenerateFromPassword([]byte(llPassword), bcrypt.MinCost)
	require.NoError(t, err)
	newService := func(mutate func(u *models.User), found bool) (*authService, *llUnwiredUserRepository) {
		repo := &llUnwiredUserRepository{found: found, stored: models.User{
			BaseModel: models.BaseModel{ID: 9}, Email: llOwnerEmail, Password: string(hash),
			IsActive: true, Role: models.RoleSales, FailedLoginAttempts: 2,
		}}
		if mutate != nil {
			mutate(&repo.stored)
		}
		svc := NewAuthService(repo, nil, config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1}).(*authService)
		svc.loginBeforeLock = func(uint) { t.Error("an unwired login has no issuing transaction") }
		return svc, repo
	}

	t.Run("a successful login keeps the separate statements and issues no refresh token", func(t *testing.T) {
		for _, entry := range llEntries {
			svc, repo := newService(nil, true)
			tokens, err := entry.call(svc)
			require.NoError(t, err, entry.name)
			assert.NotEmpty(t, tokens.AccessToken)
			assert.Empty(t, tokens.RefreshToken, "%s: no refresh token without a session store", entry.name)
			assert.Equal(t, []string{"GetByEmail", "UpdateFields", "UpdateLastLogin"}, repo.calls,
				"%s: the failure counters are reset and last_login_at stamped, no locking read", entry.name)
			assert.Zero(t, repo.stored.FailedLoginAttempts)
			assert.Nil(t, repo.stored.LockedUntil)
		}
	})

	t.Run("a clean record skips the reset write", func(t *testing.T) {
		svc, repo := newService(func(u *models.User) { u.FailedLoginAttempts = 0 }, true)
		_, err := svc.Login(llOwnerEmail, llPassword)
		require.NoError(t, err)
		assert.Equal(t, []string{"GetByEmail", "UpdateLastLogin"}, repo.calls)
	})

	t.Run("the errors and the lockout window are unchanged", func(t *testing.T) {
		svc, repo := newService(nil, false)
		_, err := svc.Login("ghost@example.com", llPassword)
		assert.EqualError(t, err, llInvalidCredentials)
		assert.Equal(t, []string{"GetByEmail"}, repo.calls)

		svc, repo = newService(nil, true)
		_, err = svc.Login(llOwnerEmail, "WrongSecret123!")
		assert.EqualError(t, err, llInvalidCredentials)
		assert.Equal(t, []string{"GetByEmail", "UpdateFields"}, repo.calls)
		assert.Equal(t, 3, repo.stored.FailedLoginAttempts)

		svc, repo = newService(func(u *models.User) { u.IsActive = false }, true)
		_, err = svc.LoginWithTokens(llOwnerEmail, llPassword)
		assert.EqualError(t, err, llInvalidCredentials)
		assert.Equal(t, []string{"GetByEmail"}, repo.calls)

		now := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
		until := now.Add(time.Minute)
		svc, repo = newService(func(u *models.User) { u.LockedUntil = &until }, true)
		svc.loginTime = func() time.Time { return now }
		_, err = svc.Login(llOwnerEmail, llPassword)
		assert.EqualError(t, err, llAccountLocked)
		assert.Equal(t, []string{"GetByEmail"}, repo.calls)

		svc.loginTime = func() time.Time { return until }
		_, err = svc.Login(llOwnerEmail, llPassword)
		require.NoError(t, err, "the exact expiry is eligible")
		assert.Nil(t, repo.stored.LockedUntil)
	})
}
