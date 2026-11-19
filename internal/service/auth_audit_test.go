package service

// Issue #454: the sign-in audit log. TestAuthAuditOutcomes{SQLite,MySQL} and
// TestAuthAuditPrivacy{SQLite,MySQL} run one table of checks through the real
// NewAuthServiceWithSessions, the OIDC service and the audit writer on a
// SQLite file opened by the production connector and on a schema built by the
// real migrations (TEST_MYSQL_DSN, also pointed at MariaDB 10.11).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
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
	authAuditTestPassword = "Audit-Passw0rd!-0454"
	authAuditTestIP       = "198.51.100.77"
	authAuditTestNetwork  = "198.51.100.0/24"
	authAuditTestUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	authAuditTestFamily   = "Chrome on Windows"
)

// authAuditCtx is the context the handlers build: request id, client IP and
// user agent.
func authAuditCtx(requestID string) context.Context {
	return utils.WithClientInfo(utils.WithRequestID(context.Background(), requestID), authAuditTestIP, authAuditTestUA)
}

// authAuditEnv is the real auth service and audit writer over one database.
type authAuditEnv struct {
	db    *gorm.DB
	auth  *authService
	audit AuthAuditService
}

func newAuthAuditEnv(t *testing.T, db *gorm.DB) *authAuditEnv {
	t.Helper()
	return &authAuditEnv{
		db:    db,
		auth:  newAuthAuditAuthService(db, repository.NewAPIKeyRepository(db), oidcTestAPIKeySecret),
		audit: NewAuthAuditService(repository.NewAuthAuditRepository(db)),
	}
}

func newAuthAuditAuthService(db *gorm.DB, apiKeys repository.APIKeyRepository, apiKeySecret string) *authService {
	return NewAuthServiceWithSessions(repository.NewUserRepository(db), apiKeys,
		repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db), nil, oidcTestJWTConfig(), oidcTestAppBaseURL, apiKeySecret,
		WithAuthIdentities(repository.NewUserIdentityRepository(db))).(*authService)
}

func (e *authAuditEnv) seedUser(t *testing.T, email string) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(authAuditTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: email, Password: string(hash), FirstName: "Audit", LastName: "User", Role: models.RoleSales, IsActive: true}
	require.NoError(t, e.db.Create(user).Error)
	return user
}

func (e *authAuditEnv) update(t *testing.T, userID uint, column string, value interface{}) {
	t.Helper()
	require.NoError(t, e.db.Model(&models.User{}).Where("id = ?", userID).Update(column, value).Error)
}

func (e *authAuditEnv) erase(t *testing.T, userID uint) {
	t.Helper()
	require.NoError(t, e.db.Delete(&models.User{}, userID).Error)
}

// seedAPIKey stores a key of the user and returns the raw key.
func (e *authAuditEnv) seedAPIKey(t *testing.T, userID uint, tag string, expiresAt *time.Time) (string, *models.APIKey) {
	t.Helper()
	raw := "gcrm_" + tag + "0454abcdef0123456789"
	key := &models.APIKey{Name: "audit " + tag, KeyHash: utils.HashAPIKeyHMAC(raw, oidcTestAPIKeySecret),
		Prefix: "0454abcd", UserID: userID, ExpiresAt: expiresAt, IsActive: true}
	require.NoError(t, e.db.Create(key).Error)
	return raw, key
}

func (e *authAuditEnv) maxAuditID(t *testing.T) uint {
	t.Helper()
	var id uint
	require.NoError(t, e.db.Raw("SELECT COALESCE(MAX(id), 0) FROM auth_audit_log").Scan(&id).Error)
	return id
}

// onlyNewRow asserts that exactly one row was written after afterID and
// returns it.
func (e *authAuditEnv) onlyNewRow(t *testing.T, afterID uint) models.AuthAuditLog {
	t.Helper()
	var rows []models.AuthAuditLog
	require.NoError(t, e.db.Where("id > ?", afterID).Order("id").Find(&rows).Error)
	require.Len(t, rows, 1, "exactly one audit row")
	return rows[0]
}

// assertRow checks method, outcome, reason and user id (0 = NULL), and the
// request facts of authAuditCtx.
func assertAuditRow(t *testing.T, row models.AuthAuditLog, method, outcome, reason string, userID uint, requestID string) {
	t.Helper()
	assert.Equal(t, method, row.Method)
	assert.Equal(t, outcome, row.Outcome)
	assert.Equal(t, reason, row.Reason)
	if userID == 0 {
		assert.Nil(t, row.UserID, "user_id is NULL")
	} else if assert.NotNil(t, row.UserID, "user_id is set") {
		assert.Equal(t, userID, *row.UserID)
	}
	assert.Equal(t, requestID, row.RequestID)
	assert.Equal(t, authAuditTestNetwork, row.ClientNetwork)
	assert.Equal(t, authAuditTestFamily, row.UserAgentFamily)
	assert.Nil(t, row.SubjectRef)
	assert.Nil(t, row.AttemptID)
	assert.WithinDuration(t, time.Now().UTC(), row.CreatedAt.UTC(), time.Minute, "created_at is now, in UTC")
}

// failingUserLookup fails the plain email lookup with a database error.
type failingUserLookup struct {
	repository.UserRepository
}

func (failingUserLookup) GetByEmail(string) (*models.User, error) {
	return nil, errors.New("connection refused")
}

// unfilteredAPIKeys looks a key up without the is_active filter of the real
// repository, so ValidateAPIKey's own revocation check is reached.
type unfilteredAPIKeys struct {
	repository.APIKeyRepository
	db *gorm.DB
}

func (r unfilteredAPIKeys) GetByKeyHash(hash string) (*models.APIKey, error) {
	var key models.APIKey
	if err := r.db.Where("key_hash = ?", hash).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

func runAuthAuditOutcomes(t *testing.T, db *gorm.DB) {
	e := newAuthAuditEnv(t, db)
	var lastID uint
	login := func(t *testing.T, email, password, requestID string) (*AuthTokens, error) {
		t.Helper()
		tokens, err := e.auth.LoginWithTokens(email, password)
		lastID = e.audit.RecordPassword(authAuditCtx(requestID), tokens, err)
		return tokens, err
	}
	refresh := func(t *testing.T, token, requestID string) (*AuthTokens, error) {
		t.Helper()
		tokens, err := e.auth.RefreshAccessToken(token)
		lastID = e.audit.RecordRefresh(authAuditCtx(requestID), tokens, err)
		return tokens, err
	}
	apiKey := func(t *testing.T, svc *authService, key, requestID string) error {
		t.Helper()
		_, err := svc.ValidateAPIKey(key)
		require.Error(t, err)
		e.audit.RecordAPIKeyFailure(authAuditCtx(requestID), err)
		return err
	}

	t.Run("password_success", func(t *testing.T) {
		user := e.seedUser(t, "pw-ok-0454@crm.example.test")
		before := e.maxAuditID(t)
		tokens, err := login(t, user.Email, authAuditTestPassword, "req-pw-ok")
		require.NoError(t, err)
		require.NotEmpty(t, tokens.AccessToken)
		row := e.onlyNewRow(t, before)
		assert.Equal(t, row.ID, lastID, "Record returns the row id")
		assertAuditRow(t, row, AuthAuditMethodPassword, AuthAuditOutcomeSuccess, "", user.ID, "req-pw-ok")
	})
	t.Run("password_invalid_credentials_unknown_email", func(t *testing.T) {
		before := e.maxAuditID(t)
		_, err := login(t, "nobody-0454@crm.example.test", authAuditTestPassword, "req-pw-unknown")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "invalid_credentials", 0, "req-pw-unknown")
	})
	t.Run("password_invalid_credentials_wrong_password", func(t *testing.T) {
		user := e.seedUser(t, "pw-wrong-0454@crm.example.test")
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, "Wrong-Passw0rd!", "req-pw-wrong")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assert.Equal(t, "invalid credentials", err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "invalid_credentials", user.ID, "req-pw-wrong")
	})
	t.Run("password_account_locked", func(t *testing.T) {
		user := e.seedUser(t, "pw-locked-0454@crm.example.test")
		e.update(t, user.ID, "locked_until", time.Now().Add(time.Hour))
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-locked")
		require.ErrorIs(t, err, errLoginAccountLocked)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "account_locked", user.ID, "req-pw-locked")
	})
	t.Run("password_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "pw-inactive-0454@crm.example.test")
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-inactive")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-pw-inactive")
	})
	t.Run("password_sso_account", func(t *testing.T) {
		user := e.seedUser(t, "pw-sso-0454@crm.example.test")
		require.NoError(t, e.db.Create(&models.UserIdentity{UserID: user.ID, Issuer: "https://idp.example.test", Subject: "pw-sso-0454"}).Error)
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-sso")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "sso_account", user.ID, "req-pw-sso")
	})
	t.Run("password_password_changed", func(t *testing.T) {
		user := e.seedUser(t, "pw-changed-0454@crm.example.test")
		e.auth.loginBeforeLock = func(id uint) { e.update(t, id, "password", "changed-meanwhile") }
		t.Cleanup(func() { e.auth.loginBeforeLock = nil })
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-changed")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "password_changed", user.ID, "req-pw-changed")
	})
	t.Run("password_account_erased", func(t *testing.T) {
		user := e.seedUser(t, "pw-erased-0454@crm.example.test")
		e.auth.loginBeforeLock = func(id uint) { e.erase(t, id) }
		t.Cleanup(func() { e.auth.loginBeforeLock = nil })
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-erased")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "account_erased", user.ID, "req-pw-erased")
	})
	t.Run("password_internal_error", func(t *testing.T) {
		broken := newAuthAuditAuthService(db, repository.NewAPIKeyRepository(db), oidcTestAPIKeySecret)
		broken.userRepo = failingUserLookup{broken.userRepo}
		before := e.maxAuditID(t)
		tokens, err := broken.LoginWithTokens("pw-internal-0454@crm.example.test", authAuditTestPassword)
		require.ErrorIs(t, err, errLoginInvalidCredentials, "the answer is still the generic refusal")
		e.audit.RecordPassword(authAuditCtx("req-pw-internal"), tokens, err)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "internal_error", 0, "req-pw-internal")
	})

	t.Run("refresh_success", func(t *testing.T) {
		user := e.seedUser(t, "rf-ok-0454@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		before := e.maxAuditID(t)
		rotated, err := refresh(t, tokens.RefreshToken, "req-rf-ok")
		require.NoError(t, err)
		require.NotEmpty(t, rotated.RefreshToken)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeSuccess, "", user.ID, "req-rf-ok")
	})
	t.Run("refresh_token_invalid", func(t *testing.T) {
		before := e.maxAuditID(t)
		_, err := refresh(t, "unknown-refresh-token-0454", "req-rf-invalid")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, "token_invalid", 0, "req-rf-invalid")
	})
	t.Run("refresh_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "rf-inactive-0454@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		_, err = refresh(t, tokens.RefreshToken, "req-rf-inactive")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assert.Equal(t, "refresh token rejected: invalid refresh token", err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-rf-inactive")
	})
	t.Run("refresh_account_erased", func(t *testing.T) {
		user := e.seedUser(t, "rf-erased-0454@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		e.erase(t, user.ID)
		before := e.maxAuditID(t)
		_, err = refresh(t, tokens.RefreshToken, "req-rf-erased")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, "account_erased", user.ID, "req-rf-erased")
	})

	t.Run("api_key_key_invalid", func(t *testing.T) {
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, "gcrm_bogus0454", "req-ak-invalid")
		assert.Equal(t, "invalid API key", err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "key_invalid", 0, "req-ak-invalid")
	})
	t.Run("api_key_key_revoked", func(t *testing.T) {
		user := e.seedUser(t, "ak-revoked-0454@crm.example.test")
		raw, key := e.seedAPIKey(t, user.ID, "revoked", nil)
		require.NoError(t, e.db.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("is_active", false).Error)
		// The shipped repository filters revoked keys out of the lookup, so
		// the real service records them as key_invalid.
		before := e.maxAuditID(t)
		apiKey(t, e.auth, raw, "req-ak-revoked")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "key_invalid", 0, "req-ak-revoked")
		// A lookup that returns it reaches the revocation check.
		unfiltered := newAuthAuditAuthService(db, unfilteredAPIKeys{repository.NewAPIKeyRepository(db), db}, oidcTestAPIKeySecret)
		before = e.maxAuditID(t)
		err := apiKey(t, unfiltered, raw, "req-ak-revoked")
		assert.Equal(t, "API key has been revoked", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "key_revoked", user.ID, "req-ak-revoked")
	})
	t.Run("api_key_key_expired", func(t *testing.T) {
		user := e.seedUser(t, "ak-expired-0454@crm.example.test")
		expired := time.Now().Add(-time.Hour)
		raw, _ := e.seedAPIKey(t, user.ID, "expired", &expired)
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-expired")
		assert.Equal(t, "API key expired", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "key_expired", user.ID, "req-ak-expired")
	})
	t.Run("api_key_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "ak-inactive-0454@crm.example.test")
		raw, _ := e.seedAPIKey(t, user.ID, "inactive", nil)
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-inactive")
		assert.Equal(t, "API key owner is not active", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-ak-inactive")
	})
	t.Run("api_key_account_erased", func(t *testing.T) {
		user := e.seedUser(t, "ak-erased-0454@crm.example.test")
		raw, _ := e.seedAPIKey(t, user.ID, "erased", nil)
		// A key that escaped the erasure purge: the owner row alone is gone.
		e.erase(t, user.ID)
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-erased")
		assert.Equal(t, "invalid API key", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "account_erased", user.ID, "req-ak-erased")
	})
	t.Run("request_id_filtered", func(t *testing.T) {
		for _, bad := range []string{strings.Repeat("r", 100), "has space", "mail@example.test"} {
			before := e.maxAuditID(t)
			apiKey(t, e.auth, "gcrm_bogus0454", bad)
			row := e.onlyNewRow(t, before)
			assert.Equal(t, "", row.RequestID, "a request id outside the pattern is not stored")
		}
	})
}

func TestAuthAuditOutcomesSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runAuthAuditOutcomes(t, db)
}

func TestAuthAuditOutcomesMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runAuthAuditOutcomes(t, db)
}

// TestAuthAuditUnavailableRefresh: an operational refresh failure (the 503
// path) is recorded as unavailable with the owner it named.
func TestAuthAuditUnavailableRefresh(t *testing.T) {
	entry := outcomeEntry(AuthAuditMethodRefresh, nil, withAuthAuditUser(fmt.Errorf("commit: %w", utils.ErrCommitFailed), 7), AuthAuditReasonUnavailable)
	assert.Equal(t, AuthAuditReasonUnavailable, entry.Reason)
	assert.Equal(t, uint(7), entry.UserID)
	assert.Equal(t, AuthAuditOutcomeRefused, entry.Outcome)
	reason, _ := AuthAuditReason(nil)
	assert.Equal(t, "", reason)
}

// authAuditLogLines returns the JSON log lines written after from.
func authAuditLogLines(output *bytes.Buffer, from int) []map[string]interface{} {
	var lines []map[string]interface{}
	for _, raw := range strings.Split(strings.TrimSpace(output.String()[from:]), "\n") {
		var line map[string]interface{}
		if raw != "" && json.Unmarshal([]byte(raw), &line) == nil {
			lines = append(lines, line)
		}
	}
	return lines
}

func captureAuthAuditLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })
	return &output
}

// stubSSO answers SSOConfig with a fixed configuration.
type stubSSO struct {
	SSOSettingsService
	cfg EffectiveSSOConfig
}

func (s stubSSO) SSOConfig(context.Context) (EffectiveSSOConfig, error) { return s.cfg, nil }

// failingClaim fails every claim with a database error.
type failingClaim struct {
	repository.OIDCLoginAttemptRepository
}

func (failingClaim) Claim(string, time.Time) (*models.OIDCLoginAttempt, error) {
	return nil, errors.New("connection refused")
}

// TestAuthAuditOIDCReasonCoverage: every oidcRefuse/oidcRefuseWith code in
// the sign-in sources, recorded through the step Callback finishes with,
// yields a row with exactly that reason and a refusal log line naming the
// row; the 503s, sso_disabled and internal_error are driven through Callback
// itself.
func TestAuthAuditOIDCReasonCoverage(t *testing.T) {
	pattern := regexp.MustCompile(`oidcRefuse(?:With)?\("([a-z_]+)"`)
	codes := map[string]bool{}
	for _, file := range []string{"auth_oidc.go", "user_governed_writes.go"} {
		source, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			codes[match[1]] = true
		}
	}
	require.NotEmpty(t, codes, "the refusal codes were parsed")
	parsed := make([]string, 0, len(codes))
	for code := range codes {
		parsed = append(parsed, code)
	}
	sort.Strings(parsed)
	t.Logf("parsed %d refusal codes: %s", len(parsed), strings.Join(parsed, " "))

	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	output := captureAuthAuditLogs(t)

	lineFor := func(t *testing.T, from int, msg string) map[string]interface{} {
		t.Helper()
		var found map[string]interface{}
		for _, line := range authAuditLogLines(output, from) {
			if line["msg"] == msg {
				require.Nil(t, found, "one %q line", msg)
				found = line
			}
		}
		require.NotNil(t, found, "a %q line is logged", msg)
		return found
	}

	for i, code := range parsed {
		t.Run("refusal_"+code, func(t *testing.T) {
			f.reset(t)
			from := output.Len()
			attemptID := uint(4540 + i)
			_, err := f.svc.finishCallback(context.Background(), nil, oidcCallbackFacts{attemptID: attemptID}, oidcRefuse(code))
			require.ErrorIs(t, err, ErrSSOSignInFailed)
			row := f.lastAudit(t)
			assert.Equal(t, AuthAuditMethodOIDC, row.Method)
			assert.Equal(t, AuthAuditOutcomeRefused, row.Outcome)
			assert.Equal(t, code, row.Reason)
			require.NotNil(t, row.AttemptID)
			assert.Equal(t, attemptID, *row.AttemptID)
			line := lineFor(t, from, "Single sign-on refused")
			assert.Equal(t, code, line["error_code"])
			assert.Equal(t, float64(row.ID), line["audit_id"])
		})
	}

	// assertUnavailable checks the row and the line of a 503 callback.
	assertOutcome := func(t *testing.T, from int, reason, msg string) {
		t.Helper()
		require.Equal(t, int64(1), f.count(t, "auth_audit_log", ""), "one row per callback")
		row := f.lastAudit(t)
		assert.Equal(t, AuthAuditOutcomeRefused, row.Outcome)
		assert.Equal(t, reason, row.Reason)
		if msg != "" {
			line := lineFor(t, from, msg)
			assert.Equal(t, reason, line["error_code"])
			assert.Equal(t, float64(row.ID), line["audit_id"], "the line names the audit row")
		}
	}
	t.Run("not_configured", func(t *testing.T) {
		f.reset(t)
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-cov-nc", "cov-nc@idp.example.test"))
		cfg, err := f.sso.SSOConfig(context.Background())
		require.NoError(t, err)
		cfg.ClientID = ""
		f.useSSO(stubSSO{f.defaultSSO, cfg})
		from := output.Len()
		_, err = f.svc.Callback(context.Background(), code, state)
		require.ErrorIs(t, err, ErrSSOUnavailable)
		assert.Equal(t, ErrSSOUnavailable.Error(), err.Error())
		assertOutcome(t, from, "not_configured", "Single sign-on unavailable")
	})
	t.Run("discovery_failed", func(t *testing.T) {
		f.reset(t)
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-cov-df", "cov-df@idp.example.test"))
		f.issuer.setDiscoveryRedirect(true)
		from := output.Len()
		_, err := f.svc.Callback(context.Background(), code, state)
		require.ErrorIs(t, err, ErrSSOUnavailable)
		assertOutcome(t, from, "discovery_failed", "Single sign-on unavailable")
	})
	t.Run("token_exchange_failed", func(t *testing.T) {
		f.reset(t)
		f.issuer.setTokenStatus(500)
		from := output.Len()
		_, err := f.signIn(t, oidcValid("subject-cov-te", "cov-te@idp.example.test"))
		require.ErrorIs(t, err, ErrSSOUnavailable)
		assertOutcome(t, from, "token_exchange_failed", "Single sign-on unavailable")
	})
	t.Run("sso_disabled", func(t *testing.T) {
		f.reset(t)
		f.useSSO(stubSSO{f.defaultSSO, EffectiveSSOConfig{Enabled: false}})
		_, err := f.svc.Callback(context.Background(), "code-cov-disabled", "state-cov-disabled")
		require.ErrorIs(t, err, ErrSSODisabled)
		assertOutcome(t, 0, "sso_disabled", "")
	})
	t.Run("internal_error", func(t *testing.T) {
		f.reset(t)
		f.svc.attempts = failingClaim{f.svc.attempts}
		_, err := f.svc.Callback(context.Background(), "code-cov-internal", "state-cov-internal")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrSSOSignInFailed)
		assertOutcome(t, 0, "internal_error", "")
	})
}

func runAuthAuditPrivacy(t *testing.T, db, competitor *gorm.DB) {
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	output := captureAuthAuditLogs(t)
	e := &authAuditEnv{db: db, auth: f.auth, audit: f.audit}

	const (
		ssoSub      = "subject-privacy-ok-0454"
		ssoEmail    = "privacy-ok-0454@idp.example.test"
		refusedSub  = "subject-privacy-refused-0454"
		refusedMail = "privacy-refused-0454@idp.example.test"
		pwEmail     = "privacy-pw-0454@crm.example.test"
		wrongPass   = "Wr0ng-Privacy-Passw0rd!-0454"
	)
	var secrets []string
	callback := func(spec oidcTokenSpec, requestID string) (*AuthTokens, error) {
		authURL, err := f.svc.Start(authAuditCtx(requestID))
		require.NoError(t, err)
		code, state := f.issuer.authorize(t, authURL, spec)
		var attempt models.OIDCLoginAttempt
		require.NoError(t, f.db.Where("state_hash = ?", hashOpaqueToken(state, f.auth.sessionTokenSecret())).First(&attempt).Error)
		secrets = append(secrets, code, state, attempt.Nonce, attempt.CodeVerifier)
		tokens, err := f.svc.Callback(authAuditCtx(requestID), code, state)
		secrets = append(secrets, f.issuer.lastIDToken(), f.issuer.lastAccessToken())
		return tokens, err
	}

	// An SSO success, and a refusal after the ID token verified.
	ssoTokens, err := callback(oidcValid(ssoSub, ssoEmail), "req-privacy-sso-ok")
	require.NoError(t, err)
	secrets = append(secrets, ssoTokens.AccessToken, ssoTokens.RefreshToken, ssoSub, ssoEmail)
	_, err = callback(oidcTokenSpec{sub: refusedSub, email: refusedMail, groups: []string{"nobody-privacy-0454"}}, "req-privacy-sso-no")
	require.ErrorIs(t, err, ErrSSOSignInFailed)
	secrets = append(secrets, refusedSub, refusedMail)

	// A password failure and a refresh.
	user := e.seedUser(t, pwEmail)
	tokens, err := e.auth.LoginWithTokens(pwEmail, wrongPass)
	require.Error(t, err)
	e.audit.RecordPassword(authAuditCtx("req-privacy-pw"), tokens, err)
	tokens, err = e.auth.LoginWithTokens(pwEmail, authAuditTestPassword)
	require.NoError(t, err)
	rotated, err := e.auth.RefreshAccessToken(tokens.RefreshToken)
	require.NoError(t, err)
	e.audit.RecordRefresh(authAuditCtx("req-privacy-rf"), rotated, err)
	secrets = append(secrets, pwEmail, wrongPass, authAuditTestPassword, tokens.AccessToken, tokens.RefreshToken,
		rotated.AccessToken, rotated.RefreshToken, user.FirstName+" "+user.LastName)

	// An API-key failure.
	const badKey = "gcrm_privacy0454deadbeefcafe"
	_, err = e.auth.ValidateAPIKey(badKey)
	require.Error(t, err)
	e.audit.RecordAPIKeyFailure(authAuditCtx("req-privacy-ak"), err)
	secrets = append(secrets, badKey, authAuditTestIP, authAuditTestUA)

	var rows []map[string]interface{}
	require.NoError(t, db.Table("auth_audit_log").Find(&rows).Error)
	require.Len(t, rows, 5, "one row per outcome")
	var stored strings.Builder
	for _, row := range rows {
		for column, value := range row {
			if raw, ok := value.([]byte); ok {
				value = string(raw)
			}
			fmt.Fprintf(&stored, "%s=%v\n", column, value)
		}
	}
	logs := output.String()
	for _, secret := range secrets {
		require.NotEmpty(t, secret)
		assert.NotContains(t, stored.String(), secret, "the audit rows hold no %q", secret)
		assert.NotContains(t, logs, secret, "the logs hold no %q", secret)
	}
	assert.Contains(t, stored.String(), authAuditTestNetwork)
	assert.Contains(t, stored.String(), authAuditTestFamily)
}

func TestAuthAuditPrivacySQLite(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	runAuthAuditPrivacy(t, db, competitor)
}

func TestAuthAuditPrivacyMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runAuthAuditPrivacy(t, db, db)
}

// TestAuthAuditSubjectReference: the subject_ref of a refused identity is a
// stable keyed hash of (issuer, subject).
func TestAuthAuditSubjectReference(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	refuse := func(t *testing.T, sub string) string {
		t.Helper()
		_, err := f.signIn(t, oidcTokenSpec{sub: sub, email: sub + "@idp.example.test", groups: []string{"nobody-ref-0454"}})
		require.ErrorIs(t, err, ErrSSOSignInFailed)
		row := f.lastAudit(t)
		require.NotNil(t, row.SubjectRef)
		require.Regexp(t, `^[0-9a-f]{64}$`, *row.SubjectRef)
		return *row.SubjectRef
	}
	first := refuse(t, "subject-ref-a-0454")
	assert.Equal(t, first, refuse(t, "subject-ref-a-0454"), "the same identity gives the same reference")
	assert.NotEqual(t, first, refuse(t, "subject-ref-b-0454"), "another subject gives another reference")
	assert.Equal(t, AuthAuditSubjectRef(oidcTestAPIKeySecret, f.issuer.URL(), "subject-ref-a-0454"), first)
	assert.NotEqual(t, first, AuthAuditSubjectRef(oidcTestAPIKeySecret, f.issuer.URL()+"/other", "subject-ref-a-0454"),
		"another issuer gives another reference")
	assert.NotEqual(t, AuthAuditSubjectRef("k", "ab", "c"), AuthAuditSubjectRef("k", "a", "bc"),
		"the issuer length keeps concatenations apart")

	// Another issuer through the service: a second fixture on its own issuer.
	db2, competitor2 := openOIDCTestSQLite(t)
	other := newOIDCFixture(t, db2, competitor2)
	other.reset(t)
	require.NotEqual(t, f.issuer.URL(), other.issuer.URL())
	_, err := other.signIn(t, oidcTokenSpec{sub: "subject-ref-a-0454", email: "ref-other@idp.example.test", groups: []string{"nobody-ref-0454"}})
	require.ErrorIs(t, err, ErrSSOSignInFailed)
	otherRow := other.lastAudit(t)
	require.NotNil(t, otherRow.SubjectRef)
	assert.NotEqual(t, first, *otherRow.SubjectRef, "the same subject at another issuer gives another reference")

	// Another API_KEY_SECRET.
	f.svc.auth = newAuthAuditAuthService(db, repository.NewAPIKeyRepository(db), "another-api-key-secret-0454-0123456789abcdef")
	t.Cleanup(func() { f.svc.auth = f.auth })
	assert.NotEqual(t, first, refuse(t, "subject-ref-a-0454"), "another key gives another reference")
}

// failingAuditRepo fails every insert.
type failingAuditRepo struct {
	repository.AuthAuditRepository
	creates int
}

func (r *failingAuditRepo) Create(*models.AuthAuditLog) error {
	r.creates++
	return errors.New("audit table unavailable")
}

// TestAuthAuditWriteFailure: a failed audit write is logged once and changes
// no sign-in answer.
func TestAuthAuditWriteFailure(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	output := captureAuthAuditLogs(t)
	failing := &failingAuditRepo{}
	audit := NewAuthAuditService(failing)
	f.svc.audit = audit
	e := &authAuditEnv{db: db, auth: f.auth, audit: audit}
	failedLines := func(from int) int {
		n := 0
		for _, line := range authAuditLogLines(output, from) {
			if line["error_code"] == "audit_write_failed" {
				n++
				assert.Equal(t, "auth_audit", line["component"])
			}
		}
		return n
	}

	user := e.seedUser(t, "wf-0454@crm.example.test")
	from := output.Len()
	tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
	require.NoError(t, err)
	assert.Zero(t, audit.RecordPassword(authAuditCtx("req-wf-ok"), tokens, err))
	assert.NotEmpty(t, tokens.AccessToken, "the login still returns its tokens")
	assert.Equal(t, 1, failedLines(from))

	from = output.Len()
	tokens, err = e.auth.LoginWithTokens(user.Email, "Wrong-Passw0rd!")
	assert.Zero(t, audit.RecordPassword(authAuditCtx("req-wf-no"), tokens, err))
	assert.ErrorIs(t, err, errLoginInvalidCredentials, "the refusal is unchanged")
	assert.Equal(t, 1, failedLines(from))

	from = output.Len()
	ssoTokens, err := f.signIn(t, oidcValid("subject-wf-0454", "wf-sso-0454@idp.example.test"))
	require.NoError(t, err)
	assert.NotEmpty(t, ssoTokens.AccessToken, "the single sign-on still returns its tokens")
	assert.Equal(t, 1, failedLines(from))

	from = output.Len()
	_, err = f.signIn(t, oidcTokenSpec{sub: "subject-wf-no-0454", email: "wf-no-0454@idp.example.test", groups: []string{"nobody"}})
	assert.ErrorIs(t, err, ErrSSOSignInFailed, "the refusal is unchanged")
	assert.Equal(t, 1, failedLines(from), "one audit_write_failed line")
	var refused map[string]interface{}
	for _, line := range authAuditLogLines(output, from) {
		if line["msg"] == "Single sign-on refused" {
			refused = line
		}
	}
	require.NotNil(t, refused)
	require.Contains(t, refused, "audit_id")
	assert.Equal(t, float64(0), refused["audit_id"], "no row, audit_id 0")
	assert.Equal(t, 4, failing.creates)
	assert.Zero(t, f.count(t, "auth_audit_log", ""))
}

func runAuthAuditPrune(t *testing.T, db *gorm.DB) {
	repo := repository.NewAuthAuditRepository(db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	const days = 30
	retention := days * 24 * time.Hour
	insert := func(createdAt time.Time) uint {
		row := &models.AuthAuditLog{CreatedAt: createdAt, Method: AuthAuditMethodRefresh, Outcome: AuthAuditOutcomeSuccess}
		require.NoError(t, db.Create(row).Error)
		return row.ID
	}
	exists := func(id uint) bool {
		var n int64
		require.NoError(t, db.Model(&models.AuthAuditLog{}).Where("id = ?", id).Count(&n).Error)
		return n == 1
	}
	old := insert(now.Add(-retention - time.Minute))
	young := insert(now.Add(-retention + time.Minute))
	fresh := insert(now)
	pruned, err := PruneAuthAuditLog(context.Background(), repo, now, days)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pruned)
	assert.False(t, exists(old), "a row older than the retention is deleted")
	assert.True(t, exists(young), "a row younger than the retention stays")
	assert.True(t, exists(fresh), "a new row stays")

	// Chunks: more rows than one statement deletes.
	for i := 0; i < authAuditPruneChunk+3; i++ {
		insert(now.Add(-retention - time.Hour))
	}
	pruned, err = PruneAuthAuditLog(context.Background(), repo, now, days)
	require.NoError(t, err)
	assert.Equal(t, int64(authAuditPruneChunk+3), pruned)
	assert.True(t, exists(young))
}

func TestAuthAuditPruneSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runAuthAuditPrune(t, db)
}

func TestAuthAuditPruneMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runAuthAuditPrune(t, db)
}

// TestAuthAuditClaimValues: the stored claim arrays are bounded.
func TestAuthAuditClaimValues(t *testing.T) {
	assert.Nil(t, authAuditClaimJSON(nil))
	assert.Equal(t, "[]", *authAuditClaimJSON([]string{}))
	many := make([]string, 150)
	for i := range many {
		many[i] = strings.Repeat("é", 200)
	}
	var decoded []string
	require.NoError(t, json.Unmarshal([]byte(*authAuditClaimJSON(many)), &decoded))
	assert.Len(t, decoded, authAuditMaxClaimValues)
	for _, value := range decoded {
		assert.LessOrEqual(t, len(value), authAuditClaimValueMaxBytes)
	}
	control := make([]string, 100)
	for i := range control {
		control[i] = strings.Repeat("\x01", 256)
	}
	assert.LessOrEqual(t, len(*authAuditClaimJSON(control)), authAuditClaimColumnMaxBytes, "the array fits a TEXT column")
}
