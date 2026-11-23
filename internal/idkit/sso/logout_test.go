package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/oidctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPostLogoutURL = "https://crm.example.test/login"

// memEnder is a session ender that records its calls.
type memEnder struct {
	mu    sync.Mutex
	calls [][2]string
	ended int
	err   error
}

func (m *memEnder) EndProviderSession(_ context.Context, issuer, sid string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, [2]string{issuer, sid})
	return m.ended, m.err
}

// assertLogPrivate fails when a recorded line carries any of values, in its
// message or in a field, or a field other than allowed.
func assertLogPrivate(t *testing.T, entries []logEntry, allowed map[string]bool, values ...string) {
	t.Helper()
	for _, entry := range entries {
		for _, value := range values {
			assert.NotContains(t, entry.msg, value, "message %q", entry.msg)
		}
		for _, field := range entry.fields {
			assert.True(t, allowed[field.Key], "field %q in %q", field.Key, entry.msg)
			for _, value := range values {
				assert.NotContains(t, fmt.Sprint(field.Value), value, "field %s of %q", field.Key, entry.msg)
			}
		}
	}
}

// TestCallbackProviderSession: the sid of the ID token reaches the session
// request, an absent, non-string or over-long one is "", and the session
// returned carries the raw ID token.
func TestCallbackProviderSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		sid  interface{}
		want string
	}{
		{"sid", "sid-0634-a", "sid-0634-a"},
		{"no_sid", nil, ""},
		{"not_a_string", 42, ""},
		{"too_long", string(make([]byte, 257)), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			claims := oidctest.Claims{Subject: "subject-sid-0634", Email: "sid-0634@example.test", Groups: []string{testGroupSales}}
			if tc.sid != nil {
				claims.Extra = map[string]interface{}{"sid": tc.sid}
			}
			session, err := f.signIn(t, claims)
			require.NoError(t, err)
			require.Len(t, f.sessions.requests, 1)
			assert.Equal(t, tc.want, f.sessions.requests[0].ProviderSessionID)
			assert.Equal(t, f.issuer.Issuer(), f.sessions.requests[0].Issuer, "the verified iss, the configured issuer")
			assert.NotEmpty(t, session.IDToken)
			assert.Equal(t, f.issuer.LastIDToken(), session.IDToken, "the raw ID token of the exchange")
		})
	}
	t.Run("refusal_returns_no_id_token", func(t *testing.T) {
		f := newFixture(t)
		session, err := f.signIn(t, oidctest.Claims{Subject: "subject-sid-0634", Email: "x@example.test", Groups: []string{"nobody"}})
		require.Error(t, err)
		assert.Empty(t, session.IDToken)
	})
}

// endSessionFixture is a fixture whose account 21 is linked to the issuer's
// subject-end-0634, with PostLogoutRedirectURL set.
func endSessionFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.svc.PostLogoutRedirectURL = testPostLogoutURL
	const subject = "subject-end-0634"
	f.identities.add(idkit.Identity{AccountID: "21", Issuer: f.issuer.Issuer(), Subject: subject, Source: idkit.IdentitySourceOIDC})
	return f, subject
}

// expiredClaims is a hint for subject that expired an hour ago.
func expiredClaims(subject string) oidctest.Claims {
	return oidctest.Claims{Subject: subject, SID: "sid-end-0634", Extra: map[string]interface{}{
		"exp": time.Now().Add(-time.Hour).Unix(), "iat": time.Now().Add(-2 * time.Hour).Unix(),
	}}
}

// TestEndSessionURL: a hint of the linked account gives the provider's
// end-session URL with the hint, the post-logout redirect and the client id,
// the endpoint's own query kept; every refusal is "" and no error.
func TestEndSessionURL(t *testing.T) {
	t.Run("expired_hint_of_the_linked_account", func(t *testing.T) {
		f, subject := endSessionFixture(t)
		f.issuer.SetEndSessionEndpoint(f.issuer.URL() + "/logout?tenant=t-0634")
		hint := f.issuer.IDToken(t, expiredClaims(subject))
		got, err := f.svc.EndSessionURL(context.Background(), "21", hint)
		require.NoError(t, err)
		parsed, err := url.Parse(got)
		require.NoError(t, err)
		assert.Equal(t, f.issuer.URL()+"/logout", parsed.Scheme+"://"+parsed.Host+parsed.Path)
		assert.Equal(t, url.Values{
			"tenant":                   {"t-0634"},
			"id_token_hint":            {hint},
			"post_logout_redirect_uri": {testPostLogoutURL},
			"client_id":                {f.issuer.ClientID()},
		}, parsed.Query())
	})

	t.Run("no_post_logout_redirect_configured", func(t *testing.T) {
		f, subject := endSessionFixture(t)
		f.svc.PostLogoutRedirectURL = ""
		got, err := f.svc.EndSessionURL(context.Background(), "21", f.issuer.IDToken(t, expiredClaims(subject)))
		require.NoError(t, err)
		parsed, err := url.Parse(got)
		require.NoError(t, err)
		assert.NotContains(t, parsed.Query(), "post_logout_redirect_uri")
		assert.Equal(t, f.issuer.ClientID(), parsed.Query().Get("client_id"))
	})

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	refusals := []struct {
		name  string
		setup func(t *testing.T, f *fixture, subject string) (accountID, hint string)
		code  string
	}{
		{"empty_hint", func(t *testing.T, f *fixture, subject string) (string, string) {
			return "21", ""
		}, ""},
		{"wrong_key", func(t *testing.T, f *fixture, subject string) (string, string) {
			claims := expiredClaims(subject)
			claims.SigningKey, claims.KeyIDOverride = otherKey, oidctest.KeyID
			return "21", f.issuer.IDToken(t, claims)
		}, CodeHintInvalid},
		{"wrong_aud", func(t *testing.T, f *fixture, subject string) (string, string) {
			claims := expiredClaims(subject)
			claims.Audience = "another-client"
			return "21", f.issuer.IDToken(t, claims)
		}, CodeHintInvalid},
		{"wrong_iss", func(t *testing.T, f *fixture, subject string) (string, string) {
			claims := expiredClaims(subject)
			claims.Extra["iss"] = "https://other-issuer.example.test"
			return "21", f.issuer.IDToken(t, claims)
		}, CodeHintInvalid},
		{"sub_linked_to_another_account", func(t *testing.T, f *fixture, subject string) (string, string) {
			return "22", f.issuer.IDToken(t, expiredClaims(subject))
		}, CodeHintOtherAccount},
		{"unlinked_sub", func(t *testing.T, f *fixture, subject string) (string, string) {
			return "21", f.issuer.IDToken(t, expiredClaims("subject-unlinked-0634"))
		}, CodeHintUnlinked},
		{"near_match_link", func(t *testing.T, f *fixture, subject string) (string, string) {
			f.identities.near[[2]string{f.issuer.Issuer(), "subject-near-0634"}] = idkit.Identity{AccountID: "21",
				Issuer: f.issuer.Issuer(), Subject: "subject-near-0634 ", Source: idkit.IdentitySourceOIDC}
			return "21", f.issuer.IDToken(t, expiredClaims("subject-near-0634"))
		}, CodeHintUnlinked},
		{"no_endpoint", func(t *testing.T, f *fixture, subject string) (string, string) {
			f.issuer.OmitEndSessionEndpoint(true)
			return "21", f.issuer.IDToken(t, expiredClaims(subject))
		}, CodeEndSessionUnsupported},
		{"plain_http_endpoint", func(t *testing.T, f *fixture, subject string) (string, string) {
			f.issuer.SetEndSessionEndpoint("http://idp.example.test/logout")
			return "21", f.issuer.IDToken(t, expiredClaims(subject))
		}, CodeEndSessionUnsupported},
		{"disabled", func(t *testing.T, f *fixture, subject string) (string, string) {
			f.config.cfg.Enabled = false
			return "21", f.issuer.IDToken(t, expiredClaims(subject))
		}, ""},
		{"not_configured", func(t *testing.T, f *fixture, subject string) (string, string) {
			f.config.cfg.ClientID = ""
			return "21", f.issuer.IDToken(t, expiredClaims(subject))
		}, ""},
		{"discovery_failed", func(t *testing.T, f *fixture, subject string) (string, string) {
			f.issuer.SetDiscoveryRedirect(true)
			return "21", f.issuer.IDToken(t, expiredClaims(subject))
		}, CodeDiscoveryFailed},
		{"slow_discovery_cut_off", func(t *testing.T, f *fixture, subject string) (string, string) {
			f.svc.EndSessionTimeout = 200 * time.Millisecond
			f.issuer.SetDiscoveryDelay(3 * time.Second)
			return "21", f.issuer.IDToken(t, expiredClaims(subject))
		}, CodeEndSessionTimeout},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			f, subject := endSessionFixture(t)
			accountID, hint := tc.setup(t, f, subject)
			started := time.Now()
			got, err := f.svc.EndSessionURL(context.Background(), accountID, hint)
			require.NoError(t, err)
			assert.Equal(t, "", got)
			assert.Less(t, time.Since(started), 2*time.Second, "never waits past its deadline")
			if tc.code == "" {
				assert.Empty(t, f.logger.entries)
				return
			}
			require.Len(t, f.logger.entries, 1)
			entry := f.logger.entries[0]
			assert.Equal(t, "Single sign-on sign-out without the provider's end-session URL", entry.msg)
			uid := uint64(0)
			fmt.Sscan(accountID, &uid)
			assert.Equal(t, []idkit.Field{{Key: "user_id", Value: uid}, {Key: "error_code", Value: tc.code}}, entry.fields)
			if tc.code == CodeDiscoveryFailed || tc.code == CodeEndSessionTimeout {
				assert.Equal(t, "warn", entry.level)
			}
		})
	}

	t.Run("default_deadline", func(t *testing.T) {
		assert.Equal(t, 5*time.Second, New(Options{}).EndSessionTimeout)
	})

	t.Run("configuration_error", func(t *testing.T) {
		f, _ := endSessionFixture(t)
		f.svc.Config = failingConfig{}
		_, err := f.svc.EndSessionURL(context.Background(), "21", "hint")
		require.Error(t, err)
	})

	t.Run("log_privacy", func(t *testing.T) {
		f, subject := endSessionFixture(t)
		hint := f.issuer.IDToken(t, expiredClaims(subject))
		got, err := f.svc.EndSessionURL(context.Background(), "21", hint)
		require.NoError(t, err)
		require.NotEmpty(t, got)
		other := f.issuer.IDToken(t, expiredClaims("subject-unlinked-0634"))
		_, err = f.svc.EndSessionURL(context.Background(), "21", other)
		require.NoError(t, err)
		require.Len(t, f.logger.entries, 2)
		assertLogPrivate(t, f.logger.entries, map[string]bool{"user_id": true, "error_code": true},
			hint, other, got, f.issuer.URL(), "sid-end-0634", subject, testPostLogoutURL)
	})
}

type failingConfig struct{}

func (failingConfig) Config(context.Context) (idkit.Config, error) {
	return idkit.Config{}, errors.New("settings unavailable")
}

// TestFrontChannelLogout: the sid is handed to the session ender under the
// configured issuer; a mismatched iss, an empty or over-long sid end
// nothing; disabled and not configured are their errors.
func TestFrontChannelLogout(t *testing.T) {
	newEnder := func(f *fixture) *memEnder {
		ender := &memEnder{ended: 1}
		f.svc.SessionEnder = ender
		return ender
	}

	t.Run("sid_without_iss", func(t *testing.T) {
		f := newFixture(t)
		ender := newEnder(f)
		ended, err := f.svc.FrontChannelLogout(context.Background(), "", "sid-fc-0634")
		require.NoError(t, err)
		assert.Equal(t, 1, ended)
		assert.Equal(t, [][2]string{{f.issuer.Issuer(), "sid-fc-0634"}}, ender.calls)
	})

	t.Run("sid_with_the_configured_iss", func(t *testing.T) {
		f := newFixture(t)
		ender := newEnder(f)
		_, err := f.svc.FrontChannelLogout(context.Background(), f.issuer.Issuer(), "sid-fc-0634")
		require.NoError(t, err)
		assert.Equal(t, [][2]string{{f.issuer.Issuer(), "sid-fc-0634"}}, ender.calls)
	})

	for name, iss := range map[string]string{
		"another_iss":        "https://other-issuer.example.test",
		"iss_trailing_slash": "", // set below: the configured issuer plus "/"
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ender := newEnder(f)
			if iss == "" {
				iss = f.issuer.Issuer() + "/"
			}
			ended, err := f.svc.FrontChannelLogout(context.Background(), iss, "sid-fc-0634")
			require.NoError(t, err)
			assert.Equal(t, 0, ended)
			assert.Empty(t, ender.calls, "nothing ends")
		})
	}

	for name, sid := range map[string]string{"empty_sid": "", "over_long_sid": string(make([]byte, 257))} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ender := newEnder(f)
			ended, err := f.svc.FrontChannelLogout(context.Background(), "", sid)
			require.NoError(t, err)
			assert.Equal(t, 0, ended)
			assert.Empty(t, ender.calls)
		})
	}

	t.Run("disabled", func(t *testing.T) {
		f := newFixture(t)
		ender := newEnder(f)
		f.config.cfg.Enabled = false
		_, err := f.svc.FrontChannelLogout(context.Background(), "", "sid-fc-0634")
		require.ErrorIs(t, err, idkitoidc.ErrDisabled)
		assert.Empty(t, ender.calls)
	})

	t.Run("not_configured", func(t *testing.T) {
		f := newFixture(t)
		ender := newEnder(f)
		f.config.cfg.IssuerURL = ""
		_, err := f.svc.FrontChannelLogout(context.Background(), "", "sid-fc-0634")
		require.ErrorIs(t, err, idkitoidc.ErrUnavailable)
		assert.Equal(t, CodeNotConfigured, idkitoidc.UnavailableCode(err))
		assert.Empty(t, ender.calls)
	})

	t.Run("ender_failure", func(t *testing.T) {
		f := newFixture(t)
		ender := newEnder(f)
		ender.err = errors.New("database down")
		_, err := f.svc.FrontChannelLogout(context.Background(), "", "sid-fc-0634")
		require.Error(t, err)
	})

	t.Run("no_ender", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.FrontChannelLogout(context.Background(), "", "sid-fc-0634")
		require.Error(t, err)
	})

	t.Run("log_privacy", func(t *testing.T) {
		f := newFixture(t)
		newEnder(f)
		const sid = "sid-privacy-0634"
		_, err := f.svc.FrontChannelLogout(context.Background(), "", sid)
		require.NoError(t, err)
		_, err = f.svc.FrontChannelLogout(context.Background(), "https://other-issuer-privacy.example.test", sid)
		require.NoError(t, err)
		f.config.cfg.ClientID = ""
		_, err = f.svc.FrontChannelLogout(context.Background(), "", sid)
		require.Error(t, err)
		require.Len(t, f.logger.entries, 3)
		assert.Equal(t, []idkit.Field{{Key: "ended", Value: 1}}, f.logger.entries[0].fields)
		assertLogPrivate(t, f.logger.entries, map[string]bool{"ended": true, "error_code": true},
			sid, f.issuer.URL(), "other-issuer-privacy")
	})
}
