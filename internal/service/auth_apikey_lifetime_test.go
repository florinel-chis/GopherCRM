package service

// Roots for #532: access tokens from a sign-in carry auth_time and tokens
// from a refresh carry none (TestAccessTokenSignInTime,
// TestOIDCSessionSignInTime), and ValidateAPIKey applies the one-year
// lifetime to a key stored without an expiry and refuses a key of a customer
// account (TestValidateAPIKeyLifetime).

import (
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawAccessClaims decodes the claims of an access token signed with the test
// secret.
func rawAccessClaims(t *testing.T, token string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (interface{}, error) {
		return []byte(oidcTestJWTConfig().Secret), nil
	})
	require.NoError(t, err)
	return claims
}

// assertSignedInNow checks that the token carries auth_time within a few
// seconds of now, and that SignedInAt reports it.
func assertSignedInNow(t *testing.T, auth *authService, token string) {
	t.Helper()
	claims := rawAccessClaims(t, token)
	raw, ok := claims["auth_time"].(float64)
	require.True(t, ok, "auth_time is a number: %v", claims["auth_time"])
	assert.InDelta(t, float64(time.Now().Unix()), raw, 5)
	signedInAt, ok := auth.SignedInAt(token)
	require.True(t, ok)
	assert.Equal(t, int64(raw), signedInAt.Unix())
}

func TestAccessTokenSignInTime(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	e := newAuthAuditEnv(t, db)
	user := e.seedUser(t, "sign-in-time-0532@crm.example.test")

	tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
	require.NoError(t, err)
	assertSignedInNow(t, e.auth, tokens.AccessToken)

	t.Run("a refreshed token carries none", func(t *testing.T) {
		rotated, err := e.auth.RefreshAccessToken(tokens.RefreshToken)
		require.NoError(t, err)
		_, present := rawAccessClaims(t, rotated.AccessToken)["auth_time"]
		assert.False(t, present, "no auth_time after a refresh")
		_, ok := e.auth.SignedInAt(rotated.AccessToken)
		assert.False(t, ok)
		_, err = e.auth.ValidateToken(rotated.AccessToken)
		assert.NoError(t, err, "the refreshed token still authenticates")
	})

	t.Run("the renewed token of a password change carries it", func(t *testing.T) {
		session, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		changed, err := e.auth.ChangePassword(user.ID, authAuditTestPassword, "Changed-Passw0rd!-0532", session.RefreshToken)
		require.NoError(t, err)
		require.NotNil(t, changed, "the renewed session")
		assertSignedInNow(t, e.auth, changed.AccessToken)
	})

	t.Run("SignedInAt refuses what does not verify or has no number", func(t *testing.T) {
		sign := func(claims jwt.MapClaims, secret string) string {
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
			require.NoError(t, err)
			return token
		}
		exp := time.Now().Add(time.Hour).Unix()
		secret := oidcTestJWTConfig().Secret

		_, ok := e.auth.SignedInAt(sign(jwt.MapClaims{"user_id": user.ID, "exp": exp}, secret))
		assert.False(t, ok, "no claim")
		_, ok = e.auth.SignedInAt(sign(jwt.MapClaims{"user_id": user.ID, "exp": exp, "auth_time": "1790000000"}, secret))
		assert.False(t, ok, "a string claim")
		_, ok = e.auth.SignedInAt(sign(jwt.MapClaims{"user_id": user.ID, "exp": exp, "auth_time": time.Now().Unix()},
			secret+"-wrong"))
		assert.False(t, ok, "a wrong signature")
		_, ok = e.auth.SignedInAt(sign(jwt.MapClaims{"user_id": user.ID, "exp": time.Now().Add(-time.Minute).Unix(),
			"auth_time": time.Now().Unix()}, secret))
		assert.False(t, ok, "an expired token")
		at, ok := e.auth.SignedInAt(sign(jwt.MapClaims{"user_id": user.ID, "exp": exp, "auth_time": 1_790_000_000}, secret))
		assert.True(t, ok)
		assert.Equal(t, int64(1_790_000_000), at.Unix())
	})
}

func TestOIDCSessionSignInTime(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)

	tokens, err := f.signIn(t, oidcValid("subject-0532", "sso-0532@idp.example.test"))
	require.NoError(t, err)
	assertSignedInNow(t, f.auth, tokens.AccessToken)
}

func TestValidateAPIKeyLifetime(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	e := newAuthAuditEnv(t, db)
	user := e.seedUser(t, "key-lifetime-0532@crm.example.test")

	// seedLegacy stores a key without an expiry, created at createdAt.
	seedLegacy := func(t *testing.T, ownerID uint, tag string, createdAt time.Time) (string, *models.APIKey) {
		t.Helper()
		raw, key := e.seedAPIKey(t, ownerID, tag, nil)
		require.NoError(t, db.Exec("UPDATE api_keys SET created_at = ?, expires_at = NULL WHERE id = ?", createdAt, key.ID).Error)
		return raw, key
	}
	refusalOf := func(t *testing.T, err error) *authRefusalError {
		t.Helper()
		require.Error(t, err)
		var refusal *authRefusalError
		require.True(t, errors.As(err, &refusal), "a refusal with a reason, got %v", err)
		return refusal
	}

	t.Run("a key without an expiry created more than a year ago is expired", func(t *testing.T) {
		raw, key := seedLegacy(t, user.ID, "legacyold", time.Now().AddDate(-1, 0, -1))
		_, err := e.auth.ValidateAPIKey(raw)
		refusal := refusalOf(t, err)
		assert.Equal(t, AuthAuditReasonKeyExpired, refusal.reason)
		assert.Equal(t, "API key expired", err.Error())
		var stored models.APIKey
		require.NoError(t, db.First(&stored, key.ID).Error)
		assert.Nil(t, stored.LastUsedAt, "a refused key is not stamped as used")
	})

	t.Run("a key without an expiry created a day ago authenticates", func(t *testing.T) {
		raw, _ := seedLegacy(t, user.ID, "legacynew", time.Now().AddDate(0, 0, -1))
		got, err := e.auth.ValidateAPIKey(raw)
		require.NoError(t, err)
		assert.Equal(t, user.ID, got.ID)
	})

	t.Run("a key of a customer account is refused", func(t *testing.T) {
		customer := e.seedUser(t, "key-customer-0532@crm.example.test")
		e.update(t, customer.ID, "role", models.RoleCustomer)
		expires := time.Now().Add(24 * time.Hour)
		raw, key := e.seedAPIKey(t, customer.ID, "customer", &expires)
		_, err := e.auth.ValidateAPIKey(raw)
		refusal := refusalOf(t, err)
		assert.Equal(t, "customer_login_role", refusal.reason)
		assert.Equal(t, customer.ID, refusal.userID)
		var stored models.APIKey
		require.NoError(t, db.First(&stored, key.ID).Error)
		assert.Nil(t, stored.LastUsedAt, "a refused key is not stamped as used")
	})
}
