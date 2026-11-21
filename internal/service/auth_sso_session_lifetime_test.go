package service

// Regression roots for the single sign-on session limit: a refresh session
// that started with single sign-on (sign_in_method oidc) can be rotated, and
// renewed by a password change, only until 24 hours after its sign-in, and
// every token it issues expires by then. Password sessions keep
// REFRESH_TOKEN_EXPIRY_DAYS. Every root runs on a real database (in-memory
// SQLite, and a disposable MySQL schema when TEST_MYSQL_DSN is set) and reads
// committed rows outside any transaction.

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	ssoLifetimePresented = "sso-lifetime-presented-token"
	ssoLifetimeSecond    = "sso-lifetime-second-token"
)

func TestSSOSessionLifetimeSQLite(t *testing.T) {
	runSSOSessionLifetimeScenarios(t, openResetTestSQLite(t))
}

func TestSSOSessionLifetimeMySQL(t *testing.T) {
	runSSOSessionLifetimeScenarios(t, openResetTestMySQL(t, 1))
}

// ssoLifetimeRow is a refresh token row as committed, read with a raw SELECT.
type ssoLifetimeRow struct {
	FamilyID         string
	SignInMethod     string
	SessionStartedAt *time.Time
	ExpiresAt        time.Time
	Revoked          bool `gorm:"column:is_revoked"`
}

// ssoLifetimeFamily returns a new random family id.
func ssoLifetimeFamily(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}

// createSessionRow seeds a live, unrevoked token with the given session
// fields and expiry; a nil start is stored as NULL.
func (f *rotationFixture) createSessionRow(t *testing.T, userID uint, raw, family, method string, startedAt *time.Time, expiresAt time.Time) {
	t.Helper()
	require.NoError(t, f.refreshRepo.Create(&models.RefreshToken{
		UserID:           userID,
		TokenHash:        hashOpaqueToken(raw, rotationTestSecret),
		ExpiresAt:        expiresAt,
		FamilyID:         family,
		SignInMethod:     method,
		SessionStartedAt: startedAt,
	}))
}

// row reads the committed row of a raw token.
func (f *rotationFixture) row(t *testing.T, raw string) ssoLifetimeRow {
	t.Helper()
	var r ssoLifetimeRow
	require.NoError(t, f.db.WithContext(resetContext(t)).Raw(
		"SELECT family_id, sign_in_method, session_started_at, expires_at, is_revoked FROM refresh_tokens WHERE token_hash = ?",
		hashOpaqueToken(raw, rotationTestSecret)).Scan(&r).Error)
	return r
}

// ssoLifetimeStart is a session start offset from now, truncated to the
// millisecond the datetime(3) column keeps.
func ssoLifetimeStart(offset time.Duration) *time.Time {
	start := time.Now().Add(offset).Truncate(time.Millisecond)
	return &start
}

func runSSOSessionLifetimeScenarios(t *testing.T, db *gorm.DB) {
	f := newRotationFixture(t, db)
	monthAhead := func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }

	// seedOwner empties the tables the scenarios write (API keys included)
	// and creates the owner.
	seedOwner := func(t *testing.T) *models.User {
		t.Helper()
		f.reset(t)
		f.exec(t, "DELETE FROM api_keys")
		return f.createUser(t, "sso-owner@example.com")
	}

	// assertCappedSuccessor checks the committed successor of a single
	// sign-on session: the expected family rule, oidc, the carried start and
	// an expiry 24 hours after it. MySQL datetime(3) rounds rather than
	// truncates, hence the one-second tolerance.
	assertCappedSuccessor := func(t *testing.T, raw string, start time.Time) ssoLifetimeRow {
		t.Helper()
		successor := f.row(t, raw)
		assert.Equal(t, models.RefreshTokenSignInOIDC, successor.SignInMethod)
		if assert.NotNil(t, successor.SessionStartedAt) {
			assert.WithinDuration(t, start, *successor.SessionStartedAt, time.Second, "the session start is carried")
		}
		assert.WithinDuration(t, start.Add(24*time.Hour), successor.ExpiresAt, time.Second)
		assert.False(t, successor.Revoked)
		return successor
	}

	// assertRotationRefused checks a refused single sign-on rotation: the
	// generic refusal for the owner, the presented row still live and no row
	// inserted.
	assertRotationRefused := func(t *testing.T, owner *models.User) {
		t.Helper()
		before := f.state(t, owner.ID)
		tokens, err := f.svc.RefreshAccessToken(ssoLifetimePresented)
		assertRefused(t, tokens, err, AuthAuditReasonTokenInvalid, owner.ID)
		assert.False(t, f.row(t, ssoLifetimePresented).Revoked, "the presented row is not revoked")
		assert.Equal(t, before, f.state(t, owner.ID), "nothing is written")
	}

	t.Run("oidc_within_24h_rotates", func(t *testing.T) {
		owner := seedOwner(t)
		family := ssoLifetimeFamily(t)
		start := ssoLifetimeStart(-23 * time.Hour)
		f.createSessionRow(t, owner.ID, ssoLifetimePresented, family, models.RefreshTokenSignInOIDC, start, monthAhead())

		tokens, err := f.svc.RefreshAccessToken(ssoLifetimePresented)
		require.NoError(t, err)
		require.NotNil(t, tokens)

		successor := assertCappedSuccessor(t, tokens.RefreshToken, *start)
		assert.Equal(t, family, successor.FamilyID, "the successor continues the family")
		assert.True(t, f.revoked(t, ssoLifetimePresented), "the presented token is spent")
	})

	t.Run("oidc_past_24h_refused", func(t *testing.T) {
		owner := seedOwner(t)
		start := ssoLifetimeStart(-24*time.Hour - time.Minute)
		f.createSessionRow(t, owner.ID, ssoLifetimePresented, ssoLifetimeFamily(t), models.RefreshTokenSignInOIDC, start, monthAhead())
		assertRotationRefused(t, owner)
	})

	t.Run("oidc_without_start_refused", func(t *testing.T) {
		owner := seedOwner(t)
		f.createSessionRow(t, owner.ID, ssoLifetimePresented, ssoLifetimeFamily(t), models.RefreshTokenSignInOIDC, nil, monthAhead())
		assertRotationRefused(t, owner)
	})

	t.Run("password_keeps_lifetime", func(t *testing.T) {
		owner := seedOwner(t)
		start := ssoLifetimeStart(-25 * 24 * time.Hour)
		f.createSessionRow(t, owner.ID, ssoLifetimePresented, ssoLifetimeFamily(t), models.RefreshTokenSignInPassword, start, time.Now().Add(5*24*time.Hour))
		f.createSessionRow(t, owner.ID, ssoLifetimeSecond, ssoLifetimeFamily(t), "", start, time.Now().Add(5*24*time.Hour))

		for raw, method := range map[string]string{
			ssoLifetimePresented: models.RefreshTokenSignInPassword,
			ssoLifetimeSecond:    "",
		} {
			tokens, err := f.svc.RefreshAccessToken(raw)
			require.NoError(t, err, "method %q rotates", method)
			successor := f.row(t, tokens.RefreshToken)
			assert.Equal(t, method, successor.SignInMethod)
			if assert.NotNil(t, successor.SessionStartedAt) {
				assert.WithinDuration(t, *start, *successor.SessionStartedAt, time.Second, "the session start is carried")
			}
			assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), successor.ExpiresAt, time.Minute,
				"method %q keeps REFRESH_TOKEN_EXPIRY_DAYS", method)
		}
	})

	t.Run("capped_successor_expires", func(t *testing.T) {
		owner := seedOwner(t)
		start := ssoLifetimeStart(-24*time.Hour + 2*time.Minute)
		f.createSessionRow(t, owner.ID, ssoLifetimePresented, ssoLifetimeFamily(t), models.RefreshTokenSignInOIDC, start, monthAhead())

		tokens, err := f.svc.RefreshAccessToken(ssoLifetimePresented)
		require.NoError(t, err)
		successor := assertCappedSuccessor(t, tokens.RefreshToken, *start)
		assert.True(t, successor.ExpiresAt.Before(time.Now().Add(2*time.Minute)),
			"the successor expires with the session, %v", successor.ExpiresAt)
	})

	t.Run("password_change_keeps_sso_limit", func(t *testing.T) {
		owner := seedOwner(t)
		family := ssoLifetimeFamily(t)
		start := ssoLifetimeStart(-23 * time.Hour)
		f.createSessionRow(t, owner.ID, ssoLifetimePresented, family, models.RefreshTokenSignInOIDC, start, monthAhead())

		tokens, err := f.svc.ChangePassword(owner.ID, resetOldPassword, resetNewPassword, ssoLifetimePresented)
		require.NoError(t, err)
		require.NotNil(t, tokens)

		successor := assertCappedSuccessor(t, tokens.RefreshToken, *start)
		assert.NotEqual(t, family, successor.FamilyID, "the renewal starts a new family")
		assert.Regexp(t, `^[0-9a-f]{32}$`, successor.FamilyID)
	})

	t.Run("password_change_past_sso_limit_stale", func(t *testing.T) {
		owner := seedOwner(t)
		require.NoError(t, f.db.WithContext(resetContext(t)).Create(&models.APIKey{
			Name: "sso owner key", KeyHash: "sso-lifetime-key-hash", Prefix: "0a1b2c3d", UserID: owner.ID, IsActive: true,
		}).Error)
		start := ssoLifetimeStart(-24*time.Hour - time.Minute)
		f.createSessionRow(t, owner.ID, ssoLifetimePresented, ssoLifetimeFamily(t), models.RefreshTokenSignInOIDC, start, monthAhead())

		var before models.User
		require.NoError(t, f.db.WithContext(resetContext(t)).First(&before, owner.ID).Error)
		sessionsBefore := f.state(t, owner.ID)

		tokens, err := f.svc.ChangePassword(owner.ID, resetOldPassword, resetNewPassword, ssoLifetimePresented)
		assert.Nil(t, tokens)
		require.ErrorIs(t, err, ErrSessionStale)

		var after models.User
		require.NoError(t, f.db.WithContext(resetContext(t)).First(&after, owner.ID).Error)
		assert.Equal(t, before.Password, after.Password, "the password is unchanged")
		assert.Equal(t, before.TokenVersion, after.TokenVersion, "token_version is unchanged")

		var liveKeys int64
		require.NoError(t, f.db.WithContext(resetContext(t)).Model(&models.APIKey{}).
			Where("user_id = ? AND is_active = ? AND revoked_at IS NULL", owner.ID, true).Count(&liveKeys).Error)
		assert.Equal(t, int64(1), liveKeys, "the API key is not revoked")

		assert.Equal(t, sessionsBefore, f.state(t, owner.ID), "no refresh token revoked or inserted")
		assert.False(t, f.row(t, ssoLifetimePresented).Revoked)
	})
}
