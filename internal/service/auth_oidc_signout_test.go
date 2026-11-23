package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// Sign-out of single sign-on sessions (#634): the provider session reference
// a sign-in records on its refresh token, its copy at a rotation and a
// password change, and the front-channel sign-out that ends the sessions by
// it.

// signOutSpec is a sales sign-in of sub carrying sid ("" leaves it out).
func signOutSpec(sub, sid string) oidcTokenSpec {
	spec := oidcTokenSpec{sub: sub, email: sub + "@example.test", groups: []string{oidcGroupSales}}
	if sid != "" {
		spec.extra = map[string]interface{}{"sid": sid}
	}
	return spec
}

// refreshRow is the stored row of a raw refresh token.
func (f *oidcFixture) refreshRow(t *testing.T, raw string) models.RefreshToken {
	t.Helper()
	var row models.RefreshToken
	require.NoError(t, f.db.Where("token_hash = ?", hashOpaqueToken(raw, f.auth.sessionTokenSecret())).First(&row).Error)
	return row
}

// wantProviderHash is the reference a sign-in under the fixture's issuer
// with sid records.
func (f *oidcFixture) wantProviderHash(sid string) string {
	return hashOpaqueToken(f.issuer.Issuer()+"\n"+sid, f.auth.sessionTokenSecret())
}

// TestRefreshProviderSession: the refresh token of a sign-in with a sid
// records the keyed hash of the issuer and the sid; one without a sid and a
// password login record an empty value; a rotation and the single sign-on renewal of a
// password change copy the value to the successor.
func TestRefreshProviderSession(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)

	t.Run("sign_in_with_sid", func(t *testing.T) {
		f.reset(t)
		tokens, err := f.signIn(t, signOutSpec("sub-ps-1", "sid-ps-1"))
		require.NoError(t, err)
		assert.Equal(t, f.issuer.lastIDToken(), tokens.IDToken, "the callback returns the raw ID token")
		row := f.refreshRow(t, tokens.RefreshToken)
		assert.Equal(t, f.wantProviderHash("sid-ps-1"), row.ProviderSessionHash)
		assert.Len(t, row.ProviderSessionHash, 64)
		assert.Zero(t, f.count(t, "refresh_tokens", "provider_session_hash = ?", "sid-ps-1"), "the raw sid is never stored")
	})

	t.Run("sign_in_without_sid", func(t *testing.T) {
		f.reset(t)
		tokens, err := f.signIn(t, signOutSpec("sub-ps-2", ""))
		require.NoError(t, err)
		assert.Equal(t, "", f.refreshRow(t, tokens.RefreshToken).ProviderSessionHash)
	})

	t.Run("password_login", func(t *testing.T) {
		f.reset(t)
		f.seedUser(t, "password-ps@example.test", models.RoleSales, true, "Str0ng!Passw0rd")
		tokens, err := f.auth.LoginWithTokens("password-ps@example.test", "Str0ng!Passw0rd")
		require.NoError(t, err)
		assert.Empty(t, tokens.IDToken)
		row := f.refreshRow(t, tokens.RefreshToken)
		assert.Equal(t, models.RefreshTokenSignInPassword, row.SignInMethod)
		assert.Equal(t, "", row.ProviderSessionHash)
	})

	t.Run("rotation_copies", func(t *testing.T) {
		f.reset(t)
		tokens, err := f.signIn(t, signOutSpec("sub-ps-3", "sid-ps-3"))
		require.NoError(t, err)
		rotated, err := f.auth.RefreshAccessToken(tokens.RefreshToken)
		require.NoError(t, err)
		successor := f.refreshRow(t, rotated.RefreshToken)
		assert.Equal(t, f.wantProviderHash("sid-ps-3"), successor.ProviderSessionHash, "the successor keeps the reference")
		rotatedAgain, err := f.auth.RefreshAccessToken(rotated.RefreshToken)
		require.NoError(t, err)
		assert.Equal(t, f.wantProviderHash("sid-ps-3"), f.refreshRow(t, rotatedAgain.RefreshToken).ProviderSessionHash)
	})

	t.Run("password_change_renewal_copies", func(t *testing.T) {
		f.reset(t)
		tokens, err := f.signIn(t, signOutSpec("sub-ps-4", "sid-ps-4"))
		require.NoError(t, err)
		hash, err := bcrypt.GenerateFromPassword([]byte("Str0ng!Passw0rd"), bcrypt.MinCost)
		require.NoError(t, err)
		require.NoError(t, f.db.Model(&models.User{}).Where("id = ?", tokens.User.ID).Update("password", string(hash)).Error)
		renewed, err := f.auth.ChangePassword(tokens.User.ID, "Str0ng!Passw0rd", "N3w!Passw0rd-x", tokens.RefreshToken)
		require.NoError(t, err)
		require.NotNil(t, renewed)
		row := f.refreshRow(t, renewed.RefreshToken)
		assert.Equal(t, models.RefreshTokenSignInOIDC, row.SignInMethod)
		assert.Equal(t, f.wantProviderHash("sid-ps-4"), row.ProviderSessionHash, "the renewed session keeps the reference")
	})

	t.Run("password_change_of_a_password_session_keeps_none", func(t *testing.T) {
		f.reset(t)
		user := f.seedUser(t, "password-ps-5@example.test", models.RoleSales, true, "Str0ng!Passw0rd")
		tokens, err := f.auth.LoginWithTokens("password-ps-5@example.test", "Str0ng!Passw0rd")
		require.NoError(t, err)
		renewed, err := f.auth.ChangePassword(user.ID, "Str0ng!Passw0rd", "N3w!Passw0rd-x", tokens.RefreshToken)
		require.NoError(t, err)
		assert.Equal(t, "", f.refreshRow(t, renewed.RefreshToken).ProviderSessionHash)
	})
}

// runFrontChannelLogout signs account A in under sid s1 (twice: two
// devices) and account B under s2 through the real callback, then sends the
// front-channel sign-out of s1: every session of A ends (its refresh tokens
// revoked, its access tokens refused) with one logout audit row, and B keeps
// its session. A mismatched iss and a sid differing only in letter case end
// nothing.
func runFrontChannelLogout(t *testing.T, f *oidcFixture) {
	ctx := context.Background()
	f.reset(t)
	a1, err := f.signIn(t, signOutSpec("sub-fc-a", "Sid-FC-1"))
	require.NoError(t, err)
	a2, err := f.signIn(t, signOutSpec("sub-fc-a", "Sid-FC-1"))
	require.NoError(t, err)
	b, err := f.signIn(t, signOutSpec("sub-fc-b", "Sid-FC-2"))
	require.NoError(t, err)
	require.NotEqual(t, a1.User.ID, b.User.ID)
	liveOf := func(userID uint) int64 {
		return f.count(t, "refresh_tokens", "user_id = ? AND is_revoked = ?", userID, false)
	}
	require.EqualValues(t, 2, liveOf(a1.User.ID))
	logouts := func() int64 { return f.count(t, "auth_audit_log", "method = ?", AuthAuditMethodLogout) }

	t.Run("mismatched_iss_ends_nothing", func(t *testing.T) {
		ended, err := f.svc.FrontChannelLogout(ctx, "https://other-issuer.example.test", "Sid-FC-1")
		require.NoError(t, err)
		assert.Zero(t, ended)
		assert.EqualValues(t, 2, liveOf(a1.User.ID))
		assert.Zero(t, logouts())
	})

	t.Run("letter_case_differs", func(t *testing.T) {
		ended, err := f.svc.FrontChannelLogout(ctx, "", "sid-fc-1")
		require.NoError(t, err)
		assert.Zero(t, ended, "the hash compares bytes")
		assert.EqualValues(t, 2, liveOf(a1.User.ID))
	})

	t.Run("ends_every_session_of_the_account", func(t *testing.T) {
		ended, err := f.svc.FrontChannelLogout(ctx, f.issuer.Issuer(), "Sid-FC-1")
		require.NoError(t, err)
		assert.Equal(t, 1, ended, "one account")
		assert.Zero(t, liveOf(a1.User.ID), "every refresh token of A is revoked")
		for _, tokens := range []*AuthTokens{a1, a2} {
			_, err := f.auth.ValidateToken(tokens.AccessToken)
			assert.Error(t, err, "A's access tokens are refused")
		}
		assert.EqualValues(t, 1, liveOf(b.User.ID), "B keeps its session")
		_, err = f.auth.ValidateToken(b.AccessToken)
		assert.NoError(t, err)
		assert.EqualValues(t, 1, logouts(), "one logout audit row")
		assert.EqualValues(t, 1, f.count(t, "auth_audit_log", "method = ? AND user_id = ? AND outcome = ?",
			AuthAuditMethodLogout, a1.User.ID, AuthAuditOutcomeSuccess))

		again, err := f.svc.FrontChannelLogout(ctx, "", "Sid-FC-1")
		require.NoError(t, err)
		assert.Zero(t, again, "an ended session is not live any more")
		assert.EqualValues(t, 1, logouts())
	})

	t.Run("disabled", func(t *testing.T) {
		enabled := false
		require.NoError(t, f.sso.Update(ctx, SSOSettingsUpdate{Enabled: &enabled, IssuerURL: f.issuer.Issuer(),
			ClientID: oidcTestClientID, RoleMapping: f.mapping()}))
		_, err := f.svc.FrontChannelLogout(ctx, "", "Sid-FC-2")
		require.ErrorIs(t, err, ErrSSODisabled)
		assert.EqualValues(t, 1, liveOf(b.User.ID))
	})
}

func TestFrontChannelLogout(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	runFrontChannelLogout(t, newOIDCFixture(t, db, competitor))
}

func TestFrontChannelLogoutMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runFrontChannelLogout(t, newOIDCFixture(t, db, db))
}

// TestEndSessionURLHost: the host asks the sign-in for the end-session URL
// of the caller's own linked identity, and answers "" for another account.
func TestEndSessionURLHost(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	tokens, err := f.signIn(t, signOutSpec("sub-esu", "sid-esu"))
	require.NoError(t, err)
	got, err := f.svc.EndSessionURL(context.Background(), tokens.User.ID, tokens.IDToken)
	require.NoError(t, err)
	assert.Contains(t, got, "post_logout_redirect_uri=https%3A%2F%2Fcrm.example.test%2Flogin", "APP_BASE_URL/login, trailing slash trimmed")
	other, err := f.svc.EndSessionURL(context.Background(), tokens.User.ID+1000, tokens.IDToken)
	require.NoError(t, err)
	assert.Empty(t, other)
	empty, err := f.svc.EndSessionURL(context.Background(), tokens.User.ID, "")
	require.NoError(t, err)
	assert.Empty(t, empty)
}
