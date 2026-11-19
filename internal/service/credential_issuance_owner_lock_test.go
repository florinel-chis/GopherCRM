package service

// Regression roots for issue #342 (C3 of #183): a password-reset request
// (authService.RequestPasswordReset) and an API key (apiKeyService.Generate)
// are issued only inside one WithTransactionAndRetry transaction that first
// locks the owner's users row by primary key and refuses a missing, erased
// (soft-deleted) or inactive owner, writing nothing. Lock order users →
// password_reset_tokens and users → api_keys. The reset mail goes out after
// the commit and the request keeps its uniform nil answer.
//
// The roots run on SQLite with the production one-connection pool (a file
// database with the full erasure schema) and on a disposable MySQL/MariaDB
// schema built by the real migrations when TEST_MYSQL_DSN is set (skipped
// without it, failing when it points at an unreachable server). On SQLite the
// single connection serialises the parties, so a competitor runs through the
// instance-local seams resetBeforeLock and apiKeyService.beforeLock, between
// the plain lookup and the transaction, or after the issuance committed. On
// MySQL the first party is parked inside its transaction, the second is seen
// waiting for the row lock in information_schema.PROCESSLIST on two
// consecutive polls, and no ordering may end in error 1213: in the
// interleaved reset request against a reset confirmation the engine picks a
// victim, either party is replayed (the confirmation since #407), and both
// outcomes are asserted. Every assertion reads committed rows.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
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
	ciSecret          = "credential-issuance-owner-lock-secret"
	ciPassword        = "IssueLock123!"
	ciNewPassword     = "IssueLockNew12!"
	ciOwnerEmail      = "issuance-owner@example.com"
	ciMovedEmail      = "issuance-owner-moved@example.com"
	ciBystanderEmail  = "issuance-bystander@example.com"
	ciCustomerEmail   = "issuance-customer@example.com"
	ciOwnerFirstName  = "Ilinca-Canary"
	ciOwnerLastName   = "Issuer-Canary"
	ciKeyName         = "issuance-key-canary"
	ciEarlierLink     = "issuance-earlier-reset-link"
	ciUserUpdateWait  = "%UPDATE `users` SET%"
	ciForUpdateWait   = "%FOR UPDATE%"
	ciResetUpdateWait = "%UPDATE `password_reset_tokens`%"
)

// ------------------------------------------------------------------- hooks ---

type ciHookSet struct {
	// beforeLock fires before a transaction-bound (locking) owner read.
	beforeLock func(id uint) error
	// beforeRootUpdate fires before an UpdateFields outside any transaction
	// (UserService.Update).
	beforeRootUpdate func(id uint)
	beforeInvalidate func(userID uint) error
	afterInvalidate  func(userID uint) error
	beforeResetSave  func(token *models.PasswordResetToken) error
	afterResetSave   func(token *models.PasswordResetToken) error
	beforeMarkUsed   func(id uint) error
	beforeKeySave    func(key *models.APIKey) error
	afterKeySave     func(key *models.APIKey) error
	afterRevokeAll   func(userID uint) error
}

// ciHooks interposes on the repositories one service writes through, records
// the order of the issuing statements and counts the locking owner reads.
type ciHooks struct {
	mu       sync.Mutex
	set      ciHookSet
	locks    int
	sequence []string
}

func (h *ciHooks) install(fn func(s *ciHookSet)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(&h.set)
}

func (h *ciHooks) current() ciHookSet {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.set
}

func (h *ciHooks) record(step string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if step == "users:lock" {
		h.locks++
	}
	h.sequence = append(h.sequence, step)
}

func (h *ciHooks) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.set = ciHookSet{}
	h.locks = 0
	h.sequence = nil
}

func (h *ciHooks) lockReads() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.locks
}

func (h *ciHooks) steps() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.sequence...)
}

type ciUserRepository struct {
	repository.UserRepository
	hooks *ciHooks
	inTx  bool
}

func (r *ciUserRepository) WithTx(tx *gorm.DB) repository.UserRepository {
	return &ciUserRepository{UserRepository: r.UserRepository.WithTx(tx), hooks: r.hooks, inTx: true}
}

func (r *ciUserRepository) GetByID(id uint) (*models.User, error) {
	if r.inTx {
		r.hooks.record("users:lock")
		if hook := r.hooks.current().beforeLock; hook != nil {
			if err := hook(id); err != nil {
				return nil, err
			}
		}
	}
	return r.UserRepository.GetByID(id)
}

func (r *ciUserRepository) UpdateFields(id uint, fields map[string]interface{}) error {
	if !r.inTx {
		if hook := r.hooks.current().beforeRootUpdate; hook != nil {
			hook(id)
		}
	}
	return r.UserRepository.UpdateFields(id, fields)
}

type ciResetRepository struct {
	repository.PasswordResetTokenRepository
	hooks *ciHooks
}

func (r *ciResetRepository) WithTx(tx *gorm.DB) repository.PasswordResetTokenRepository {
	return &ciResetRepository{PasswordResetTokenRepository: r.PasswordResetTokenRepository.WithTx(tx), hooks: r.hooks}
}

func (r *ciResetRepository) InvalidateAllForUser(userID uint) error {
	r.hooks.record("password_reset_tokens:invalidate")
	if hook := r.hooks.current().beforeInvalidate; hook != nil {
		if err := hook(userID); err != nil {
			return err
		}
	}
	if err := r.PasswordResetTokenRepository.InvalidateAllForUser(userID); err != nil {
		return err
	}
	if hook := r.hooks.current().afterInvalidate; hook != nil {
		return hook(userID)
	}
	return nil
}

func (r *ciResetRepository) Create(token *models.PasswordResetToken) error {
	r.hooks.record("password_reset_tokens:insert")
	if hook := r.hooks.current().beforeResetSave; hook != nil {
		if err := hook(token); err != nil {
			return err
		}
	}
	if err := r.PasswordResetTokenRepository.Create(token); err != nil {
		return err
	}
	if hook := r.hooks.current().afterResetSave; hook != nil {
		return hook(token)
	}
	return nil
}

func (r *ciResetRepository) MarkUsed(id uint) error {
	if hook := r.hooks.current().beforeMarkUsed; hook != nil {
		if err := hook(id); err != nil {
			return err
		}
	}
	return r.PasswordResetTokenRepository.MarkUsed(id)
}

type ciKeyRepository struct {
	repository.APIKeyRepository
	hooks *ciHooks
}

func (r *ciKeyRepository) WithTx(tx *gorm.DB) repository.APIKeyRepository {
	return &ciKeyRepository{APIKeyRepository: r.APIKeyRepository.WithTx(tx), hooks: r.hooks}
}

func (r *ciKeyRepository) Create(key *models.APIKey) error {
	r.hooks.record("api_keys:insert")
	if hook := r.hooks.current().beforeKeySave; hook != nil {
		if err := hook(key); err != nil {
			return err
		}
	}
	if err := r.APIKeyRepository.Create(key); err != nil {
		return err
	}
	if hook := r.hooks.current().afterKeySave; hook != nil {
		return hook(key)
	}
	return nil
}

type ciRefreshRepository struct {
	repository.RefreshTokenRepository
	hooks *ciHooks
}

func (r *ciRefreshRepository) WithTx(tx *gorm.DB) repository.RefreshTokenRepository {
	return &ciRefreshRepository{RefreshTokenRepository: r.RefreshTokenRepository.WithTx(tx), hooks: r.hooks}
}

func (r *ciRefreshRepository) RevokeAllForUser(userID uint) error {
	if err := r.RefreshTokenRepository.RevokeAllForUser(userID); err != nil {
		return err
	}
	if hook := r.hooks.current().afterRevokeAll; hook != nil {
		return hook(userID)
	}
	return nil
}

// ------------------------------------------------------------------ mailer ---

type ciMail struct {
	to, url string
	// committed is true when, at send time, the mailed token was a committed
	// unused row readable on a separate handle.
	committed bool
}

// ciMailer records reset mails. At send time it reads the mailed token on a
// separate handle of the same database with a short deadline: inside an open
// transaction (the one-connection SQLite pool) that read could not run, and on
// MySQL an uncommitted insert is invisible to it.
type ciMailer struct {
	mu   sync.Mutex
	db   *gorm.DB
	sent []ciMail
	fail error
}

func (m *ciMailer) SendPasswordReset(to, resetURL, _ string) error {
	committed := m.tokenCommitted(resetURL)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, ciMail{to: to, url: resetURL, committed: committed})
	return m.fail
}

func (m *ciMailer) Send(string, string, string) error { return nil }

func (m *ciMailer) tokenCommitted(resetURL string) bool {
	raw := ciTokenOf(resetURL)
	if raw == "" || m.db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int64
	err := m.db.Session(&gorm.Session{NewDB: true}).WithContext(ctx).Model(&models.PasswordResetToken{}).
		Where("token_hash = ? AND used_at IS NULL", hashOpaqueToken(raw, ciSecret)).Count(&n).Error
	return err == nil && n == 1
}

func (m *ciMailer) mails() []ciMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ciMail(nil), m.sent...)
}

func (m *ciMailer) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = nil
	m.fail = nil
}

func ciTokenOf(resetURL string) string {
	parsed, err := url.Parse(resetURL)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("token")
}

// ----------------------------------------------------------------- fixture ---

type ciFixture struct {
	db *gorm.DB
	// issueHooks interpose on the issuing services (reset request, API key);
	// otherHooks on the competitors (confirmation, UserService).
	issueHooks *ciHooks
	otherHooks *ciHooks
	issuer     *authService
	confirmer  *authService
	keys       *apiKeyService
	users      UserService
	customers  repository.CustomerRepository
	mailer     *ciMailer
	parker     *llParker
}

func newCIFixture(t *testing.T, db *gorm.DB) *ciFixture {
	t.Helper()
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	f := &ciFixture{
		db:         db,
		issueHooks: &ciHooks{},
		otherHooks: &ciHooks{},
		customers:  repository.NewCustomerRepository(db),
		mailer:     &ciMailer{db: db},
		parker:     &llParker{},
	}
	require.NoError(t, db.Callback().Update().
		Before("gorm:commit_or_rollback_transaction").After("gorm:update").
		Register("test:credential_issuance_park", func(tx *gorm.DB) {
			if tx.Error == nil && tx.Statement != nil {
				f.parker.fire(tx.Statement.Table)
			}
		}))
	userRepo := repository.NewUserRepository(db)
	tm := utils.NewTransactionManager(db)
	cfg := config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30}
	build := func(h *ciHooks) *authService {
		return NewAuthServiceWithSessions(
			&ciUserRepository{UserRepository: userRepo, hooks: h},
			repository.NewAPIKeyRepository(db),
			&ciRefreshRepository{RefreshTokenRepository: repository.NewRefreshTokenRepository(db), hooks: h},
			&ciResetRepository{PasswordResetTokenRepository: repository.NewPasswordResetTokenRepository(db), hooks: h},
			tm, f.mailer, cfg, resetTestBaseURL, ciSecret).(*authService)
	}
	f.issuer = build(f.issueHooks)
	f.confirmer = build(f.otherHooks)
	f.keys = NewAPIKeyService(
		&ciKeyRepository{APIKeyRepository: repository.NewAPIKeyRepository(db), hooks: f.issueHooks},
		&ciUserRepository{UserRepository: userRepo, hooks: f.issueHooks},
		tm, ciSecret).(*apiKeyService)
	f.users = NewUserService(&ciUserRepository{UserRepository: userRepo, hooks: f.otherHooks})
	return f
}

type ciSeed struct {
	owner, bystander *models.User
	customer         *models.Customer
}

func (f *ciFixture) exec(t *testing.T, statement string, args ...interface{}) {
	t.Helper()
	require.NoError(t, f.db.WithContext(resetContext(t)).Exec(statement, args...).Error)
}

func (f *ciFixture) createUser(t *testing.T, email string, role models.UserRole) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(ciPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: email, Password: string(hash), FirstName: ciOwnerFirstName, LastName: ciOwnerLastName,
		Role: role, IsActive: true}
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(user).Error)
	return user
}

// seed empties the tables and resets every hook and seam. The owner is a
// customer-role login linked to a customer row, so the customer-rooted
// erasure reaches it.
func (f *ciFixture) seed(t *testing.T) ciSeed {
	t.Helper()
	f.issueHooks.reset()
	f.otherHooks.reset()
	f.parker.disarm()
	f.mailer.reset()
	f.issuer.resetBeforeLock = nil
	f.keys.beforeLock = nil
	ctx := resetContext(t)
	for _, table := range []string{"api_keys", "password_reset_tokens", "refresh_tokens", "leads", "customers", "users"} {
		require.NoError(t, f.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
	s := ciSeed{
		owner:     f.createUser(t, ciOwnerEmail, models.RoleCustomer),
		bystander: f.createUser(t, ciBystanderEmail, models.RoleSales),
	}
	ownerID := s.owner.ID
	s.customer = &models.Customer{FirstName: "Cora", LastName: "Customer", Email: ciCustomerEmail, UserID: &ownerID}
	require.NoError(t, f.db.WithContext(ctx).Create(s.customer).Error)
	return s
}

// earlierToken stores a spendable reset token of the user directly, so its
// insert sends no mail and never waits for a lock the scenario holds.
func (f *ciFixture) earlierToken(t *testing.T, userID uint, raw string) {
	t.Helper()
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(&models.PasswordResetToken{
		UserID: userID, TokenHash: hashOpaqueToken(raw, ciSecret), ExpiresAt: time.Now().Add(time.Hour),
	}).Error)
}

// resetTokens counts the user's password_reset_tokens rows and the unused ones.
func (f *ciFixture) resetTokens(t *testing.T, userID uint) (total, unused int64) {
	t.Helper()
	ctx := resetContext(t)
	require.NoError(t, f.db.WithContext(ctx).Unscoped().Model(&models.PasswordResetToken{}).
		Where("user_id = ?", userID).Count(&total).Error)
	require.NoError(t, f.db.WithContext(ctx).Unscoped().Model(&models.PasswordResetToken{}).
		Where("user_id = ? AND used_at IS NULL", userID).Count(&unused).Error)
	return total, unused
}

// tokenUnused reports whether the token row exists and is unused.
func (f *ciFixture) tokenUnused(t *testing.T, raw string) (exists, unused bool) {
	t.Helper()
	var row models.PasswordResetToken
	err := f.db.WithContext(resetContext(t)).Unscoped().
		Where("token_hash = ?", hashOpaqueToken(raw, ciSecret)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false
	}
	require.NoError(t, err)
	return true, row.UsedAt == nil
}

func (f *ciFixture) apiKeys(t *testing.T, userID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().Model(&models.APIKey{}).
		Where("user_id = ?", userID).Count(&n).Error)
	return n
}

func (f *ciFixture) passwordIs(t *testing.T, userID uint, password string) bool {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&user, userID).Error)
	return bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) == nil
}

// contend waits until the second party is blocked on a row lock the first
// holds: it reached its statement (when reached is given) and the server
// reports a statement matching pattern still executing on two consecutive
// polls of information_schema.PROCESSLIST (MySQL 8 and MariaDB 10.11 alike).
func (f *ciFixture) contend(t *testing.T, reached <-chan struct{}, done <-chan error, pattern, what string) {
	t.Helper()
	if reached != nil {
		select {
		case <-reached:
		case err := <-done:
			t.Fatalf("%s finished before reaching its locking statement: %v", what, err)
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
		case err := <-done:
			t.Fatalf("%s finished without waiting for the row lock: %v", what, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never reported waiting for the row lock", what)
		}
		time.Sleep(llLockPoll)
	}
}

func ciStart(fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

func ciAwait(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(resetTimeout):
		t.Fatalf("%s did not finish within %s", what, resetTimeout)
		return nil
	}
}

// --------------------------------------------------------- the competitors ---

type ciCompetitor struct {
	name    string
	erasure bool
	run     func(f *ciFixture, s ciSeed) error
	// park arms the hold inside the competitor's transaction, after it took
	// the owner's row lock.
	park func(f *ciFixture, h *llHold)
	// waitPattern is the statement the competitor waits in on MySQL while the
	// issuance holds the owner row; progress, when set, signals that the
	// competitor reached it.
	waitPattern string
	progress    func(f *ciFixture) <-chan struct{}
}

func ciParkOnUsersUpdate(f *ciFixture, h *llHold) { f.parker.arm("users", h.wait) }

func ciRootUpdateProgress(f *ciFixture) <-chan struct{} {
	fire, ch := llSignal()
	f.otherHooks.install(func(s *ciHookSet) { s.beforeRootUpdate = func(uint) { fire() } })
	return ch
}

var ciCompetitors = []ciCompetitor{
	{
		name:    "a user-rooted erasure",
		erasure: true,
		// DELETE /users/{id}: UserService.Delete.
		run:         func(f *ciFixture, s ciSeed) error { return f.users.Delete(s.owner.ID) },
		park:        ciParkOnUsersUpdate,
		waitPattern: ciForUpdateWait,
	},
	{
		name:    "a customer-rooted erasure",
		erasure: true,
		// DELETE /customers/{id}: the customer erasure reaching its
		// customer-role login.
		run:         func(f *ciFixture, s ciSeed) error { return f.customers.Delete(s.customer.ID) },
		park:        ciParkOnUsersUpdate,
		waitPattern: ciForUpdateWait,
	},
	{
		name: "a deactivation",
		run: func(f *ciFixture, s ciSeed) error {
			_, err := f.users.Update(s.owner.ID, map[string]interface{}{"is_active": false})
			return err
		},
		park:        ciParkOnUsersUpdate,
		waitPattern: ciUserUpdateWait,
		progress:    ciRootUpdateProgress,
	},
}

// ------------------------------------------------- AC-1, AC-2 reset decisions ---

func TestPasswordResetRequestOwnerLockDecisionsSQLite(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))

	refusals := []struct {
		name  string
		apply func(t *testing.T, s ciSeed)
	}{
		{"an owner deactivated after the lookup", func(t *testing.T, s ciSeed) {
			f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, s.owner.ID)
		}},
		{"an owner erased after the lookup", func(t *testing.T, s ciSeed) {
			require.NoError(t, f.users.Delete(s.owner.ID))
		}},
		{"an owner soft-deleted after the lookup", func(t *testing.T, s ciSeed) {
			f.exec(t, "UPDATE users SET deleted_at = ? WHERE id = ?", time.Now(), s.owner.ID)
		}},
		{"an owner row removed after the lookup", func(t *testing.T, s ciSeed) {
			f.exec(t, "DELETE FROM users WHERE id = ?", s.owner.ID)
		}},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name+" writes nothing, sends nothing and answers nil", func(t *testing.T) {
			s := f.seed(t)
			f.earlierToken(t, s.owner.ID, ciEarlierLink)
			var beforeTotal, beforeUnused int64
			applied := false
			f.issuer.resetBeforeLock = func(id uint) {
				require.Equal(t, s.owner.ID, id)
				applied = true
				refusal.apply(t, s)
				beforeTotal, beforeUnused = f.resetTokens(t, id)
			}
			require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
			require.True(t, applied, "the change lands between the lookup and the lock")
			assert.Equal(t, []string{"users:lock"}, f.issueHooks.steps(),
				"no InvalidateAllForUser and no Create after the refusal")
			total, unused := f.resetTokens(t, s.owner.ID)
			assert.Equal(t, beforeTotal, total, "no token is inserted")
			assert.Equal(t, beforeUnused, unused, "no token is spent")
			assert.Empty(t, f.mailer.mails(), "no mail")
		})
	}

	t.Run("a live, active owner is issued one token after the lock and mailed once after the commit", func(t *testing.T) {
		s := f.seed(t)
		f.earlierToken(t, s.owner.ID, ciEarlierLink)
		// The address changes after the plain lookup: the mail goes to the
		// address of the locked row.
		f.issuer.resetBeforeLock = func(id uint) {
			f.exec(t, "UPDATE users SET email = ? WHERE id = ?", ciMovedEmail, id)
		}
		require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
		assert.Equal(t, []string{"users:lock", "password_reset_tokens:invalidate", "password_reset_tokens:insert"},
			f.issueHooks.steps(), "owner lock, then the transaction-bound invalidation and insert")
		assert.Equal(t, 1, f.issueHooks.lockReads())

		mails := f.mailer.mails()
		require.Len(t, mails, 1, "SendPasswordReset exactly once")
		assert.Equal(t, ciMovedEmail, mails[0].to, "the address read from the locked row")
		assert.True(t, mails[0].committed, "the token is committed and readable on a separate handle at send time")
		total, unused := f.resetTokens(t, s.owner.ID)
		assert.EqualValues(t, 2, total)
		assert.EqualValues(t, 1, unused, "exactly one unused token of the user")
		_, live := f.tokenUnused(t, ciTokenOf(mails[0].url))
		assert.True(t, live, "the unused token is the mailed one")
		_, earlierLive := f.tokenUnused(t, ciEarlierLink)
		assert.False(t, earlierLive, "the earlier link is spent")
	})

	t.Run("unknown and inactive addresses answer nil before any transaction", func(t *testing.T) {
		s := f.seed(t)
		require.NoError(t, f.issuer.RequestPasswordReset("nobody@example.com"))
		f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, s.owner.ID)
		require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
		assert.Empty(t, f.issueHooks.steps())
		assert.Empty(t, f.mailer.mails())
	})
}

// ---------------------------------------------------- AC-3 reset atomicity ---

func TestPasswordResetRequestOwnerLockAtomicitySQLite(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))
	injected := errors.New("injected failure")

	failures := map[string]func(h *ciHookSet){
		"InvalidateAllForUser failing after its update": func(h *ciHookSet) {
			h.afterInvalidate = func(uint) error { return injected }
		},
		"Create failing before its insert": func(h *ciHookSet) {
			h.beforeResetSave = func(*models.PasswordResetToken) error { return injected }
		},
		"Create failing after its insert": func(h *ciHookSet) {
			h.afterResetSave = func(*models.PasswordResetToken) error { return injected }
		},
	}
	for name, install := range failures {
		t.Run(name+" rolls both writes back, sends no mail and answers nil", func(t *testing.T) {
			s := f.seed(t)
			f.earlierToken(t, s.owner.ID, ciEarlierLink)
			f.issueHooks.install(install)
			require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
			total, unused := f.resetTokens(t, s.owner.ID)
			assert.EqualValues(t, 1, total, "no new token")
			assert.EqualValues(t, 1, unused, "the previous token stays as it was")
			_, live := f.tokenUnused(t, ciEarlierLink)
			assert.True(t, live)
			assert.Empty(t, f.mailer.mails())
		})
	}

	t.Run("a service without a transaction manager writes nothing and sends nothing", func(t *testing.T) {
		s := f.seed(t)
		f.earlierToken(t, s.owner.ID, ciEarlierLink)
		unwired := NewAuthServiceWithSessions(
			&ciUserRepository{UserRepository: repository.NewUserRepository(f.db), hooks: f.issueHooks},
			repository.NewAPIKeyRepository(f.db), repository.NewRefreshTokenRepository(f.db),
			&ciResetRepository{PasswordResetTokenRepository: repository.NewPasswordResetTokenRepository(f.db), hooks: f.issueHooks},
			nil, f.mailer, config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1}, resetTestBaseURL, ciSecret)
		require.NoError(t, unwired.RequestPasswordReset(ciOwnerEmail))
		assert.Empty(t, f.issueHooks.steps(), "no lock, no invalidation, no insert")
		total, unused := f.resetTokens(t, s.owner.ID)
		assert.EqualValues(t, 1, total)
		assert.EqualValues(t, 1, unused)
		assert.Empty(t, f.mailer.mails())
	})
}

// ------------------------------------------ AC-14, AC-4 reset vs competitors ---

func TestPasswordResetRequestOwnerLockConcurrentSQLite(t *testing.T) {
	runResetRequestConcurrent(t, newCIFixture(t, openAutomationSQLite(t)), false)
}

func TestPasswordResetRequestOwnerLockConcurrentMySQL(t *testing.T) {
	runResetRequestConcurrent(t, newCIFixture(t, openMigratedTestMySQL(t, 5)), true)
}

func runResetRequestConcurrent(t *testing.T, f *ciFixture, contended bool) {
	request := func() error { return f.issuer.RequestPasswordReset(ciOwnerEmail) }

	for _, c := range ciCompetitors {
		c := c
		t.Run(c.name+" committed first refuses the request", func(t *testing.T) {
			s := f.seed(t)
			var err error
			if !contended {
				ran := false
				f.issuer.resetBeforeLock = func(id uint) {
					ran = true
					require.NoError(t, c.run(f, s))
				}
				err = request()
				require.True(t, ran)
			} else {
				hold := newLLHold()
				defer hold.release()
				c.park(f, hold)
				competitor := ciStart(func() error { return c.run(f, s) })
				hold.entered(t, c.name)
				fire, reached := llSignal()
				f.issueHooks.install(func(h *ciHookSet) { h.beforeLock = func(uint) error { fire(); return nil } })
				req := ciStart(request)
				f.contend(t, reached, req, ciForUpdateWait, "the reset request")
				hold.release()
				done := ciAwait(t, competitor, c.name)
				err = ciAwait(t, req, "the reset request")
				llNoDeadlock(t, done, c.name)
				require.NoError(t, done, "%s commits first", c.name)
			}
			llNoDeadlock(t, err, "the reset request")
			require.NoError(t, err, "the uniform answer")
			total, _ := f.resetTokens(t, s.owner.ID)
			assert.Zero(t, total, "the request writes no token")
			assert.Empty(t, f.mailer.mails(), "and sends no mail")
			assert.NotContains(t, f.issueHooks.steps(), "password_reset_tokens:insert")
		})

		t.Run("request committed before "+c.name, func(t *testing.T) {
			s := f.seed(t)
			if !contended {
				require.NoError(t, request())
				require.NoError(t, c.run(f, s))
			} else {
				hold := newLLHold()
				defer hold.release()
				f.issueHooks.install(func(h *ciHookSet) {
					h.afterResetSave = func(*models.PasswordResetToken) error { hold.wait(); return nil }
				})
				req := ciStart(request)
				hold.entered(t, "the reset request")
				var reached <-chan struct{}
				if c.progress != nil {
					reached = c.progress(f)
				}
				competitor := ciStart(func() error { return c.run(f, s) })
				f.contend(t, reached, competitor, c.waitPattern, c.name)
				hold.release()
				reqErr := ciAwait(t, req, "the reset request")
				done := ciAwait(t, competitor, c.name)
				llNoDeadlock(t, reqErr, "the reset request")
				llNoDeadlock(t, done, c.name)
				require.NoError(t, reqErr)
				require.NoError(t, done, "%s waits for the owner lock and commits", c.name)
			}
			mails := f.mailer.mails()
			require.Len(t, mails, 1, "the request committed and mailed once")
			assert.True(t, mails[0].committed)
			raw := ciTokenOf(mails[0].url)
			if c.erasure {
				total, _ := f.resetTokens(t, s.owner.ID)
				assert.Zero(t, total, "the erasure hard-deletes the token issued before it")
				return
			}
			exists, unused := f.tokenUnused(t, raw)
			require.True(t, exists)
			assert.True(t, unused, "a deactivation spends no token")
			err := f.confirmer.ConfirmPasswordReset(raw, ciNewPassword)
			assert.ErrorIs(t, err, ErrInvalidResetToken, "the confirmation refuses it with the generic rejection")
			assert.True(t, f.passwordIs(t, s.owner.ID, ciPassword), "the password is unchanged")
		})
	}
}

// ---------------------------------------- AC-17, AC-16 request vs confirmation ---

func TestPasswordResetRequestVsConfirmSQLite(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))

	t.Run("(a) the confirmation commits first", func(t *testing.T) {
		s := f.seed(t)
		f.earlierToken(t, s.owner.ID, ciEarlierLink)
		require.NoError(t, f.confirmer.ConfirmPasswordReset(ciEarlierLink, ciNewPassword))
		require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
		f.assertConfirmFirst(t, s)
	})

	t.Run("(b) the request commits first", func(t *testing.T) {
		s := f.seed(t)
		f.earlierToken(t, s.owner.ID, ciEarlierLink)
		require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
		err := f.confirmer.ConfirmPasswordReset(ciEarlierLink, ciNewPassword)
		assert.ErrorIs(t, err, ErrInvalidResetToken)
		f.assertRequestFirst(t, s)
	})
}

// assertConfirmFirst is the end state (a): the password changed, the earlier
// link spent, exactly one unused token (the mailed one) and one mail.
func (f *ciFixture) assertConfirmFirst(t *testing.T, s ciSeed) {
	t.Helper()
	assert.True(t, f.passwordIs(t, s.owner.ID, ciNewPassword), "the password changed")
	mails := f.mailer.mails()
	require.Len(t, mails, 1, "exactly one mail")
	assert.True(t, mails[0].committed)
	_, unused := f.resetTokens(t, s.owner.ID)
	assert.EqualValues(t, 1, unused, "exactly one unused token")
	_, live := f.tokenUnused(t, ciTokenOf(mails[0].url))
	assert.True(t, live, "the new token is unused")
	_, earlierLive := f.tokenUnused(t, ciEarlierLink)
	assert.False(t, earlierLive)
}

// assertRequestFirst is the end state (b): the password unchanged, the earlier
// link spent by the request, exactly one unused token (the mailed one) and one
// mail.
func (f *ciFixture) assertRequestFirst(t *testing.T, s ciSeed) {
	t.Helper()
	assert.True(t, f.passwordIs(t, s.owner.ID, ciPassword), "the password is unchanged")
	mails := f.mailer.mails()
	require.Len(t, mails, 1, "exactly one mail")
	_, unused := f.resetTokens(t, s.owner.ID)
	assert.EqualValues(t, 1, unused, "exactly one unused token")
	_, live := f.tokenUnused(t, ciTokenOf(mails[0].url))
	assert.True(t, live)
	_, earlierLive := f.tokenUnused(t, ciEarlierLink)
	assert.False(t, earlierLive)
}

func TestPasswordResetRequestVsConfirmMySQL(t *testing.T) {
	f := newCIFixture(t, openMigratedTestMySQL(t, 5))
	request := func() error { return f.issuer.RequestPasswordReset(ciOwnerEmail) }
	confirm := func() error { return f.confirmer.ConfirmPasswordReset(ciEarlierLink, ciNewPassword) }

	t.Run("(a) the confirmation commits first and the request waits on the users row", func(t *testing.T) {
		s := f.seed(t)
		f.earlierToken(t, s.owner.ID, ciEarlierLink)
		hold := newLLHold()
		defer hold.release()
		f.otherHooks.install(func(h *ciHookSet) { h.afterRevokeAll = func(uint) error { hold.wait(); return nil } })
		conf := ciStart(confirm)
		hold.entered(t, "the confirmation")
		fire, reached := llSignal()
		f.issueHooks.install(func(h *ciHookSet) { h.beforeLock = func(uint) error { fire(); return nil } })
		req := ciStart(request)
		f.contend(t, reached, req, ciForUpdateWait, "the reset request")
		hold.release()
		confErr := ciAwait(t, conf, "the confirmation")
		reqErr := ciAwait(t, req, "the reset request")
		llNoDeadlock(t, confErr, "the confirmation")
		llNoDeadlock(t, reqErr, "the reset request")
		require.NoError(t, confErr)
		require.NoError(t, reqErr)
		f.assertConfirmFirst(t, s)
	})

	t.Run("(b) the request commits first and the confirmation waits on the token row", func(t *testing.T) {
		s := f.seed(t)
		f.earlierToken(t, s.owner.ID, ciEarlierLink)
		hold := newLLHold()
		defer hold.release()
		f.issueHooks.install(func(h *ciHookSet) {
			h.afterResetSave = func(*models.PasswordResetToken) error { hold.wait(); return nil }
		})
		req := ciStart(request)
		hold.entered(t, "the reset request")
		fire, reached := llSignal()
		f.otherHooks.install(func(h *ciHookSet) { h.beforeMarkUsed = func(uint) error { fire(); return nil } })
		conf := ciStart(confirm)
		f.contend(t, reached, conf, ciResetUpdateWait, "the confirmation")
		hold.release()
		reqErr := ciAwait(t, req, "the reset request")
		confErr := ciAwait(t, conf, "the confirmation")
		llNoDeadlock(t, reqErr, "the reset request")
		llNoDeadlock(t, confErr, "the confirmation")
		require.NoError(t, reqErr)
		assert.ErrorIs(t, confErr, ErrInvalidResetToken, "the claim is refused")
		f.assertRequestFirst(t, s)
	})

	t.Run("(c) each holds its first lock: the deadlock victim leaves no partial commit", func(t *testing.T) {
		s := f.seed(t)
		f.earlierToken(t, s.owner.ID, ciEarlierLink)
		claimed := newLLHold()
		locked := newLLHold()
		defer claimed.release()
		defer locked.release()
		// The confirmation claims the earlier token and parks; the request then
		// locks the owner and parks before it touches the tokens. Each hold
		// parks once: a replayed attempt passes the released hold.
		f.otherHooks.install(func(h *ciHookSet) {
			h.beforeLock = func(uint) error { claimed.wait(); return nil }
		})
		conf := ciStart(confirm)
		claimed.entered(t, "the confirmation")
		f.issueHooks.install(func(h *ciHookSet) {
			h.beforeInvalidate = func(uint) error { locked.wait(); return nil }
		})
		req := ciStart(request)
		locked.entered(t, "the reset request")
		// The confirmation now asks for the users row the request holds.
		claimed.release()
		f.contend(t, nil, conf, ciForUpdateWait, "the confirmation")
		// The request asks for the token row the confirmation holds.
		locked.release()
		reqErr := ciAwait(t, req, "the reset request")
		confErr := ciAwait(t, conf, "the confirmation")
		require.NoError(t, reqErr, "the request returns nil in every case")

		t.Logf("request locking reads: %d, confirmation locking reads: %d, confirmation refused: %v",
			f.issueHooks.lockReads(), f.otherHooks.lockReads(), confErr != nil)
		llNoDeadlock(t, confErr, "the confirmation")
		if confErr != nil {
			// The confirmation was the victim: replayed (#407), it finds the
			// earlier token spent by the request and is refused. It wrote
			// nothing, and the end state equals (b).
			assert.NotContains(t, confErr.Error(), "1205", "the confirmation: no lock wait timeout")
			require.ErrorIs(t, confErr, ErrInvalidResetToken, "a replayed confirmation is refused")
			// The request's attempts are not counted here: a replayed claim
			// can meet the request once more on MariaDB.
			f.assertRequestFirst(t, s)
			return
		}
		// The request was the victim: replayed by WithTransactionAndRetry, the
		// end state equals (a) with exactly one mail.
		assert.Equal(t, 2, f.issueHooks.lockReads(), "the request was replayed once")
		f.assertConfirmFirst(t, s)
	})
}

// ------------------------------------------------- AC-5 API-key decisions ---

func TestAPIKeyCreateOwnerLockDecisionsSQLite(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))

	refusals := []struct {
		name  string
		apply func(t *testing.T, s ciSeed)
	}{
		{"an inactive owner", func(t *testing.T, s ciSeed) {
			f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, s.owner.ID)
		}},
		{"an erased owner", func(t *testing.T, s ciSeed) {
			require.NoError(t, f.users.Delete(s.owner.ID))
		}},
		{"a soft-deleted owner", func(t *testing.T, s ciSeed) {
			f.exec(t, "UPDATE users SET deleted_at = ? WHERE id = ?", time.Now(), s.owner.ID)
		}},
		{"a missing owner", func(t *testing.T, s ciSeed) {
			f.exec(t, "DELETE FROM users WHERE id = ?", s.owner.ID)
		}},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name+" is refused with ErrAccountUnavailable and no row", func(t *testing.T) {
			s := f.seed(t)
			applied := false
			f.keys.beforeLock = func(id uint) {
				require.Equal(t, s.owner.ID, id)
				applied = true
				refusal.apply(t, s)
			}
			key, apiKey, err := f.keys.Generate(s.owner.ID, ciKeyName, nil)
			require.True(t, applied)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrAccountUnavailable)
			assert.Empty(t, key, "no plaintext key")
			assert.Nil(t, apiKey)
			assert.Zero(t, f.apiKeys(t, s.owner.ID), "no api_keys row")
			assert.Equal(t, []string{"users:lock"}, f.issueHooks.steps(), "nothing is inserted after the refusal")
		})
	}

	t.Run("a live, active owner gets exactly one row inserted after the lock", func(t *testing.T) {
		s := f.seed(t)
		key, apiKey, err := f.keys.Generate(s.owner.ID, ciKeyName, nil)
		require.NoError(t, err)
		require.NotNil(t, apiKey)
		assert.True(t, strings.HasPrefix(key, "gcrm_"))
		assert.Equal(t, []string{"users:lock", "api_keys:insert"}, f.issueHooks.steps())
		assert.EqualValues(t, 1, f.apiKeys(t, s.owner.ID))
		user, err := f.issuer.ValidateAPIKey(strings.TrimPrefix(key, "gcrm_"))
		require.NoError(t, err, "the committed key authenticates")
		assert.Equal(t, s.owner.ID, user.ID)
	})
}

// ------------------------------------------------- AC-6 API-key atomicity ---

func TestAPIKeyCreateOwnerLockAtomicitySQLite(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))
	injected := errors.New("injected failure")
	keyRepo := &ciKeyRepository{APIKeyRepository: repository.NewAPIKeyRepository(f.db), hooks: f.issueHooks}
	userRepo := &ciUserRepository{UserRepository: repository.NewUserRepository(f.db), hooks: f.issueHooks}
	tm := utils.NewTransactionManager(f.db)

	for name, svc := range map[string]APIKeyService{
		"without a user repository":     NewAPIKeyService(keyRepo, nil, tm, ciSecret),
		"without a transaction manager": NewAPIKeyService(keyRepo, userRepo, nil, ciSecret),
	} {
		t.Run(name+" Generate returns ErrAPIKeyIssuanceUnavailable and writes nothing", func(t *testing.T) {
			s := f.seed(t)
			key, apiKey, err := svc.Generate(s.owner.ID, ciKeyName, nil)
			assert.ErrorIs(t, err, ErrAPIKeyIssuanceUnavailable)
			assert.False(t, errors.Is(err, ErrAccountUnavailable))
			assert.Empty(t, key)
			assert.Nil(t, apiKey)
			assert.Zero(t, f.apiKeys(t, s.owner.ID))
			assert.Empty(t, f.issueHooks.steps())
		})
	}

	for name, install := range map[string]func(h *ciHookSet){
		"before its insert": func(h *ciHookSet) { h.beforeKeySave = func(*models.APIKey) error { return injected } },
		"after its insert":  func(h *ciHookSet) { h.afterKeySave = func(*models.APIKey) error { return injected } },
	} {
		t.Run("an insert failing "+name+" returns an error, no row and no key", func(t *testing.T) {
			s := f.seed(t)
			f.issueHooks.install(install)
			key, apiKey, err := f.keys.Generate(s.owner.ID, ciKeyName, nil)
			assert.ErrorIs(t, err, injected)
			assert.False(t, errors.Is(err, ErrAccountUnavailable))
			assert.Empty(t, key)
			assert.Nil(t, apiKey)
			assert.Zero(t, f.apiKeys(t, s.owner.ID), "the insert is rolled back")
		})
	}
}

// ------------------------------------------ AC-15, AC-7 API key vs competitors ---

func TestAPIKeyCreateOwnerLockConcurrentSQLite(t *testing.T) {
	runAPIKeyCreateConcurrent(t, newCIFixture(t, openAutomationSQLite(t)), false)
}

func TestAPIKeyCreateOwnerLockConcurrentMySQL(t *testing.T) {
	runAPIKeyCreateConcurrent(t, newCIFixture(t, openMigratedTestMySQL(t, 5)), true)
}

func runAPIKeyCreateConcurrent(t *testing.T, f *ciFixture, contended bool) {
	type generated struct {
		key    string
		apiKey *models.APIKey
	}
	generate := func(s ciSeed, out *generated) func() error {
		return func() error {
			key, apiKey, err := f.keys.Generate(s.owner.ID, ciKeyName, nil)
			out.key, out.apiKey = key, apiKey
			return err
		}
	}

	for _, c := range ciCompetitors {
		c := c
		t.Run(c.name+" committed first refuses the key", func(t *testing.T) {
			s := f.seed(t)
			var out generated
			var err error
			if !contended {
				ran := false
				f.keys.beforeLock = func(uint) {
					ran = true
					require.NoError(t, c.run(f, s))
				}
				err = generate(s, &out)()
				require.True(t, ran)
			} else {
				hold := newLLHold()
				defer hold.release()
				c.park(f, hold)
				competitor := ciStart(func() error { return c.run(f, s) })
				hold.entered(t, c.name)
				fire, reached := llSignal()
				f.issueHooks.install(func(h *ciHookSet) { h.beforeLock = func(uint) error { fire(); return nil } })
				gen := ciStart(generate(s, &out))
				f.contend(t, reached, gen, ciForUpdateWait, "the key create")
				hold.release()
				done := ciAwait(t, competitor, c.name)
				err = ciAwait(t, gen, "the key create")
				llNoDeadlock(t, done, c.name)
				require.NoError(t, done, "%s commits first", c.name)
			}
			llNoDeadlock(t, err, "the key create")
			assert.ErrorIs(t, err, ErrAccountUnavailable)
			assert.Empty(t, out.key)
			assert.Nil(t, out.apiKey)
			assert.Zero(t, f.apiKeys(t, s.owner.ID), "no api_keys row")
		})

		t.Run("key created before "+c.name, func(t *testing.T) {
			s := f.seed(t)
			var out generated
			if !contended {
				require.NoError(t, generate(s, &out)())
				require.NoError(t, c.run(f, s))
			} else {
				hold := newLLHold()
				defer hold.release()
				f.issueHooks.install(func(h *ciHookSet) {
					h.afterKeySave = func(*models.APIKey) error { hold.wait(); return nil }
				})
				gen := ciStart(generate(s, &out))
				hold.entered(t, "the key create")
				var reached <-chan struct{}
				if c.progress != nil {
					reached = c.progress(f)
				}
				competitor := ciStart(func() error { return c.run(f, s) })
				f.contend(t, reached, competitor, c.waitPattern, c.name)
				hold.release()
				genErr := ciAwait(t, gen, "the key create")
				done := ciAwait(t, competitor, c.name)
				llNoDeadlock(t, genErr, "the key create")
				llNoDeadlock(t, done, c.name)
				require.NoError(t, genErr, "the key create commits first")
				require.NoError(t, done, "%s waits for the owner lock and commits", c.name)
			}
			require.NotEmpty(t, out.key)
			if c.erasure {
				assert.Zero(t, f.apiKeys(t, s.owner.ID), "the erasure hard-deletes the key issued before it")
			} else {
				assert.EqualValues(t, 1, f.apiKeys(t, s.owner.ID), "a deactivation keeps the row")
			}
			user, err := f.issuer.ValidateAPIKey(strings.TrimPrefix(out.key, "gcrm_"))
			assert.Nil(t, user)
			assert.Error(t, err, "ValidateAPIKey refuses the key")
		})
	}
}

// --------------------------------------------------------------- AC-8 logs ---

func TestCredentialIssuanceOwnerLockLogsOmitPersonalData(t *testing.T) {
	f := newCIFixture(t, openAutomationSQLite(t))

	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })
	utils.Logger.Info("ci-baseline")
	injected := errors.New("injected failure")

	var secrets []string
	var mailed []string

	// Password-reset request: success, mail failure, refusals, rollback, unwired.
	s := f.seed(t)
	require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
	f.mailer.fail = errors.New("smtp down")
	require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
	for _, m := range f.mailer.mails() {
		raw := ciTokenOf(m.url)
		mailed = append(mailed, raw, hashOpaqueToken(raw, ciSecret))
	}
	f.mailer.reset()
	f.issueHooks.install(func(h *ciHookSet) {
		h.beforeResetSave = func(*models.PasswordResetToken) error { return injected }
	})
	require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
	f.issueHooks.reset()
	f.issuer.resetBeforeLock = func(id uint) { f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, id) }
	require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
	f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", true, s.owner.ID)
	f.issuer.resetBeforeLock = nil
	unwired := NewAuthServiceWithSessions(repository.NewUserRepository(f.db), repository.NewAPIKeyRepository(f.db),
		repository.NewRefreshTokenRepository(f.db), repository.NewPasswordResetTokenRepository(f.db),
		nil, f.mailer, config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1}, resetTestBaseURL, ciSecret)
	require.NoError(t, unwired.RequestPasswordReset(ciOwnerEmail))

	// API key: success, rollback, refusal, unwired.
	key, apiKey, err := f.keys.Generate(s.owner.ID, ciKeyName, nil)
	require.NoError(t, err)
	secrets = append(secrets, key, strings.TrimPrefix(key, "gcrm_"), apiKey.KeyHash)
	f.issueHooks.install(func(h *ciHookSet) { h.afterKeySave = func(*models.APIKey) error { return injected } })
	_, _, err = f.keys.Generate(s.owner.ID, ciKeyName, nil)
	require.ErrorIs(t, err, injected)
	f.issueHooks.reset()
	f.keys.beforeLock = func(id uint) { f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", false, id) }
	_, _, err = f.keys.Generate(s.owner.ID, ciKeyName, nil)
	require.ErrorIs(t, err, ErrAccountUnavailable)
	f.keys.beforeLock = nil
	_, _, err = NewAPIKeyService(repository.NewAPIKeyRepository(f.db), nil, nil, ciSecret).Generate(s.owner.ID, ciKeyName, nil)
	require.ErrorIs(t, err, ErrAPIKeyIssuanceUnavailable)

	// Erased owners, both paths.
	f.exec(t, "UPDATE users SET is_active = ? WHERE id = ?", true, s.owner.ID)
	f.issuer.resetBeforeLock = func(id uint) { require.NoError(t, f.users.Delete(id)) }
	require.NoError(t, f.issuer.RequestPasswordReset(ciOwnerEmail))
	f.issuer.resetBeforeLock = nil
	s = f.seed(t)
	f.keys.beforeLock = func(id uint) { require.NoError(t, f.users.Delete(id)) }
	_, _, err = f.keys.Generate(s.owner.ID, ciKeyName, nil)
	require.ErrorIs(t, err, ErrAccountUnavailable)
	f.keys.beforeLock = nil

	var baseline map[string]interface{}
	var issuance []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["msg"] == "ci-baseline" {
			baseline = entry
		}
		if entry["operation"] == "RequestPasswordReset" || entry["operation"] == "GenerateAPIKey" {
			issuance = append(issuance, entry)
		}
	}
	require.NotNil(t, baseline)
	allowed := map[string]bool{"user_id": true, "operation": true, "error_code": true, "error": true}
	for k := range baseline {
		allowed[k] = true
	}

	personal := append([]string{ciOwnerEmail, ciOwnerFirstName, ciOwnerLastName, ciKeyName, ciCustomerEmail,
		ciPassword}, secrets...)
	personal = append(personal, mailed...)
	if apiKey != nil {
		personal = append(personal, apiKey.Prefix)
	}
	codes := map[string]map[string]int{"RequestPasswordReset": {}, "GenerateAPIKey": {}}
	for _, entry := range issuance {
		for k := range entry {
			assert.True(t, allowed[k], "%q carries only user_id, operation and error_code, not %q", entry["msg"], k)
		}
		encoded, err := json.Marshal(entry)
		require.NoError(t, err)
		for _, secret := range personal {
			if secret != "" {
				assert.NotContains(t, string(encoded), secret)
			}
		}
		if code, ok := entry["error_code"].(string); ok {
			codes[entry["operation"].(string)][code]++
		}
	}
	for _, code := range []string{"account_inactive", "account_erased", "reset_token_rolled_back", "reset_unwired", "mail_failed"} {
		assert.Positive(t, codes["RequestPasswordReset"][code], "a RequestPasswordReset %s line", code)
	}
	for _, code := range []string{"account_inactive", "account_erased", "api_key_insert_failed", "issuance_unwired"} {
		assert.Positive(t, codes["GenerateAPIKey"][code], "a GenerateAPIKey %s line", code)
	}

	// No line anywhere carries a token, its hash or the key.
	text := output.String()
	for _, secret := range append(append([]string{}, secrets...), mailed...) {
		if secret != "" {
			assert.NotContains(t, text, secret)
		}
	}
}
