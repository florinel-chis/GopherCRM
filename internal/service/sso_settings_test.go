package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeSSOConfigs is a ConfigurationService holding the nine SSO rows in
// plaintext. It records the order of SetSSO writes, can fail a read per key,
// and can fail the n-th write.
type fakeSSOConfigs struct {
	ConfigurationService
	values    map[string]string
	readErrs  map[string]error
	writes    []string
	failWrite int // 1-based index of the write that fails; 0 = none
}

func newFakeSSOConfigs() *fakeSSOConfigs {
	values := map[string]string{}
	for _, config := range models.DefaultConfigurations() {
		if models.IsSSOConfigurationKey(config.Key) {
			values[config.Key] = ""
		}
	}
	return &fakeSSOConfigs{values: values, readErrs: map[string]error{}}
}

func (f *fakeSSOConfigs) read(key string) (string, error) {
	if err := f.readErrs[key]; err != nil {
		return "", err
	}
	value, ok := f.values[key]
	if !ok {
		return "", apperrors.ErrNotFound
	}
	return value, nil
}

func (f *fakeSSOConfigs) GetString(key string) (string, error) { return f.read(key) }
func (f *fakeSSOConfigs) GetSecret(key string) (string, error) { return f.read(key) }

func (f *fakeSSOConfigs) SetSSO(key string, value interface{}) error {
	f.writes = append(f.writes, key)
	if f.failWrite == len(f.writes) {
		return errors.New("connection refused")
	}
	f.values[key] = value.(string)
	return nil
}

// fakeRoles knows the role ids in known.
type fakeRoles struct {
	repository.RoleRepository
	known  map[uint]bool
	err    error
	lookup []uint
}

func (r *fakeRoles) GetByID(id uint) (*models.Role, error) {
	r.lookup = append(r.lookup, id)
	if r.err != nil {
		return nil, r.err
	}
	if !r.known[id] {
		return nil, gorm.ErrRecordNotFound
	}
	return &models.Role{ID: id}, nil
}

func knownRoles(ids ...uint) *fakeRoles {
	known := map[uint]bool{}
	for _, id := range ids {
		known[id] = true
	}
	return &fakeRoles{known: known}
}

// fakeIssuerLinks answers CountByIssuer with links (or err) and records the
// issuer of each call.
type fakeIssuerLinks struct {
	links int64
	err   error
	calls []string
}

func (f *fakeIssuerLinks) CountByIssuer(issuer string) (int64, error) {
	f.calls = append(f.calls, issuer)
	if f.err != nil {
		return 0, f.err
	}
	return f.links, nil
}

// noIssuerLinks is a counter that finds no link under any issuer.
func noIssuerLinks() *fakeIssuerLinks { return &fakeIssuerLinks{} }

func boolPtr(v bool) *bool    { return &v }
func ssoCtx() context.Context { return context.Background() }
func envSSO() config.SSOConfig {
	return config.SSOConfig{
		Enabled:          true,
		EnabledSet:       true,
		AllowPassword:    true,
		AllowPasswordSet: true,
		IssuerURL:        "https://env-idp.example.test",
		ClientID:         "env-client",
		ClientSecret:     "env-secret-value",
		Scopes:           "openid email",
		RoleMapping:      `[{"claim":"groups","value":"env-group","role_id":3}]`,
	}
}

func TestSSOConfig(t *testing.T) {
	t.Run("nothing configured: disabled, defaults", func(t *testing.T) {
		svc := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), nil, config.SSOConfig{})
		cfg, err := svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.False(t, cfg.Enabled)
		assert.False(t, cfg.AllowPassword)
		assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Scopes)
		assert.Empty(t, cfg.IssuerURL)
		assert.Empty(t, cfg.ClientID)
		assert.Empty(t, cfg.ClientSecret)
		assert.Empty(t, cfg.RoleMapping)
		assert.Equal(t, 10, cfg.GraphMaxPages, "the Graph page bound defaults to 10 and has no source")
		assert.Equal(t, SSOConfigSources{
			Enabled: SSOSourceDefault, AllowPassword: SSOSourceDefault, IssuerURL: SSOSourceDefault, ClientID: SSOSourceDefault,
			ClientSecret: SSOSourceDefault, Scopes: SSOSourceDefault, RoleMapping: SSOSourceDefault,
			ClientCertificate: SSOSourceDefault,
		}, cfg.Sources)
	})

	t.Run("settings win over env", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "false"
		configs.values[models.ConfigKeySSOAllowPassword] = "false"
		configs.values[models.ConfigKeySSOIssuerURL] = "https://stored-idp.example.test/v2.0"
		configs.values[models.ConfigKeySSOClientID] = "stored-client"
		configs.values[models.ConfigKeySSOClientSecret] = "stored-secret"
		configs.values[models.ConfigKeySSOScopes] = "openid profile groups"
		configs.values[models.ConfigKeySSORoleMapping] = `[{"claim":"roles","value":"crm.admin","role_id":1}]`
		cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.False(t, cfg.Enabled)
		assert.False(t, cfg.AllowPassword, "stored false wins over OIDC_ALLOW_PASSWORD=true")
		assert.Equal(t, "https://stored-idp.example.test/v2.0", cfg.IssuerURL)
		assert.Equal(t, "stored-client", cfg.ClientID)
		assert.Equal(t, "stored-secret", cfg.ClientSecret)
		assert.Equal(t, []string{"openid", "profile", "groups"}, cfg.Scopes)
		assert.Equal(t, []SSORoleMapping{{Claim: "roles", Value: "crm.admin", RoleID: 1}}, cfg.RoleMapping)
		assert.Equal(t, SSOConfigSources{
			Enabled: SSOSourceSettings, AllowPassword: SSOSourceSettings, IssuerURL: SSOSourceSettings, ClientID: SSOSourceSettings,
			ClientSecret: SSOSourceSettings, Scopes: SSOSourceSettings, RoleMapping: SSOSourceSettings,
			ClientCertificate: SSOSourceDefault,
		}, cfg.Sources)
	})

	t.Run("env used when settings are empty", func(t *testing.T) {
		cfg, err := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), nil, envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.True(t, cfg.Enabled)
		assert.True(t, cfg.AllowPassword)
		assert.Equal(t, "https://env-idp.example.test", cfg.IssuerURL)
		assert.Equal(t, "env-client", cfg.ClientID)
		assert.Equal(t, "env-secret-value", cfg.ClientSecret)
		assert.Equal(t, []string{"openid", "email"}, cfg.Scopes)
		assert.Equal(t, []SSORoleMapping{{Claim: "groups", Value: "env-group", RoleID: 3}}, cfg.RoleMapping)
		assert.Equal(t, SSOConfigSources{
			Enabled: SSOSourceEnv, AllowPassword: SSOSourceEnv, IssuerURL: SSOSourceEnv, ClientID: SSOSourceEnv,
			ClientSecret: SSOSourceEnv, Scopes: SSOSourceEnv, RoleMapping: SSOSourceEnv,
			ClientCertificate: SSOSourceDefault,
		}, cfg.Sources)
	})

	t.Run("enabled fails closed on a read error even with env true", func(t *testing.T) {
		for name, err := range map[string]error{
			"database error": errors.New("connection refused"),
			"missing row":    apperrors.ErrNotFound,
		} {
			configs := newFakeSSOConfigs()
			configs.readErrs[models.ConfigKeySSOEnabled] = err
			cfg, cfgErr := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), envSSO()).SSOConfig(ssoCtx())
			require.NoError(t, cfgErr, name)
			assert.False(t, cfg.Enabled, name)
			assert.Equal(t, SSOSourceDefault, cfg.Sources.Enabled, name)
		}
	})

	t.Run("allow_password: stored true wins over env false", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOAllowPassword] = "true"
		env := envSSO()
		env.AllowPassword = false
		cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), env).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.True(t, cfg.AllowPassword)
		assert.Equal(t, SSOSourceSettings, cfg.Sources.AllowPassword)
	})

	t.Run("allow_password fails closed on a read error even with env true", func(t *testing.T) {
		for name, err := range map[string]error{
			"database error": errors.New("connection refused"),
			"missing row":    apperrors.ErrNotFound,
		} {
			configs := newFakeSSOConfigs()
			configs.readErrs[models.ConfigKeySSOAllowPassword] = err
			cfg, cfgErr := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), envSSO()).SSOConfig(ssoCtx())
			require.NoError(t, cfgErr, name)
			assert.False(t, cfg.AllowPassword, name)
			assert.Equal(t, SSOSourceDefault, cfg.Sources.AllowPassword, name)
		}
	})

	t.Run("mapping fails closed on a read error", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.readErrs[models.ConfigKeySSORoleMapping] = errors.New("connection refused")
		cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Empty(t, cfg.RoleMapping, "never the env mapping")
		assert.NotNil(t, cfg.RoleMapping)
		assert.Equal(t, SSOSourceDefault, cfg.Sources.RoleMapping)
	})

	t.Run("mapping fails closed on an unparsable stored value", func(t *testing.T) {
		for _, stored := range []string{
			"not json",
			`{"claim":"groups"}`,
			`[{"claim":"email","value":"x","role_id":1}]`,
			`[{"claim":"groups","value":"x","role_id":0}]`,
			`[{"claim":"groups","value":"x","role_id":1,"extra":true}]`,
		} {
			configs := newFakeSSOConfigs()
			configs.values[models.ConfigKeySSORoleMapping] = stored
			cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), envSSO()).SSOConfig(ssoCtx())
			require.NoError(t, err)
			assert.Empty(t, cfg.RoleMapping, stored)
			assert.Equal(t, SSOSourceDefault, cfg.Sources.RoleMapping, stored)
		}
	})

	t.Run("plain fields fall back to env on a read error", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		for _, key := range []string{models.ConfigKeySSOIssuerURL, models.ConfigKeySSOClientID, models.ConfigKeySSOClientSecret, models.ConfigKeySSOScopes} {
			configs.readErrs[key] = errors.New("connection refused")
		}
		cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.IssuerURL)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.ClientID)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.ClientSecret)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.Scopes)
	})

	t.Run("client certificate: a stored pair is in force next to the secret", func(t *testing.T) {
		pair := validSSOTestCertificate(t)
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOClientCertificate] = pair.certPEM
		configs.values[models.ConfigKeySSOClientPrivateKey] = pair.keyPEM
		cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		require.NotNil(t, cfg.ClientCertificate)
		assert.Equal(t, sha1Thumbprint(pair.cert), cfg.ClientCertificate.ThumbprintSHA1)
		assert.Equal(t, SSOSourceSettings, cfg.Sources.ClientCertificate)
		assert.Equal(t, "env-secret-value", cfg.ClientSecret, "the secret is still resolved")
	})

	t.Run("invalid env issuer and invalid env mapping are treated as unset", func(t *testing.T) {
		env := envSSO()
		env.IssuerURL = "http://idp.example.com"
		env.RoleMapping = `[{"claim":"email","value":"x","role_id":1}]`
		cfg, err := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), nil, env).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Empty(t, cfg.IssuerURL)
		assert.Equal(t, SSOSourceDefault, cfg.Sources.IssuerURL)
		assert.Empty(t, cfg.RoleMapping)
		assert.Equal(t, SSOSourceDefault, cfg.Sources.RoleMapping)

		env.RoleMapping = "not json"
		cfg, err = NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), nil, env).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Empty(t, cfg.RoleMapping)
		assert.Equal(t, SSOSourceDefault, cfg.Sources.RoleMapping)
	})
}

// validSSOUpdate is a well-formed update naming role 2.
func validSSOUpdate() SSOSettingsUpdate {
	return SSOSettingsUpdate{
		Enabled:     boolPtr(true),
		IssuerURL:   "https://login.microsoftonline.com/tenant-id/v2.0",
		ClientID:    "crm",
		Scopes:      []string{"openid", "profile"},
		RoleMapping: []SSORoleMapping{{Claim: "groups", Value: "g-1", RoleID: 2}},
	}
}

func TestSSOSettingsUpdate(t *testing.T) {
	t.Run("enabled true then null returns to env", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		env := envSSO()
		env.Enabled = false
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), env)

		require.NoError(t, svc.Update(ssoCtx(), validSSOUpdate()))
		cfg, err := svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.True(t, cfg.Enabled)
		assert.Equal(t, SSOSourceSettings, cfg.Sources.Enabled)

		update := validSSOUpdate()
		update.Enabled = nil
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Equal(t, "", configs.values[models.ConfigKeySSOEnabled], "SetSSO accepts the empty value")
		cfg, err = svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.False(t, cfg.Enabled)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.Enabled)
	})

	t.Run("write order: enabling writes enabled last", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate()))
		require.NotEmpty(t, configs.writes)
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[len(configs.writes)-1])
		assert.Equal(t, 6, len(configs.writes), "the secret is not written when absent")
	})

	t.Run("write order: enabling via env (null) writes enabled last", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		update := validSSOUpdate()
		update.Enabled = nil
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), envSSO()).Update(ssoCtx(), update))
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[len(configs.writes)-1])
	})

	t.Run("write order: disabling writes enabled first", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "true"
		update := validSSOUpdate()
		update.Enabled = boolPtr(false)
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update))
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[0])

		configs = newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "true"
		update.Enabled = nil
		env := envSSO()
		env.Enabled = false
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), env).Update(ssoCtx(), update))
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[0], "stored true to null with env false")
	})

	t.Run("write order: a failing second write", func(t *testing.T) {
		// Enabling: the switch is never reached, so it stays as it was.
		configs := newFakeSSOConfigs()
		configs.failWrite = 2
		err := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate())
		require.Error(t, err)
		assert.Equal(t, "", configs.values[models.ConfigKeySSOEnabled], "enabled unchanged")
		assert.NotContains(t, configs.writes, models.ConfigKeySSOEnabled)

		// Disabling: the switch went first and is already off.
		configs = newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "true"
		configs.failWrite = 2
		update := validSSOUpdate()
		update.Enabled = boolPtr(false)
		err = NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update)
		require.Error(t, err)
		assert.Equal(t, "false", configs.values[models.ConfigKeySSOEnabled])
	})

	t.Run("unknown role id is an invalid reference and stores nothing", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		update := validSSOUpdate()
		update.RoleMapping = []SSORoleMapping{{Claim: "groups", Value: "g-1", RoleID: 2}, {Claim: "roles", Value: "r", RoleID: 999999}}
		err := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update)
		require.ErrorIs(t, err, ErrSSOUnknownRole)
		var settingsErr *SSOSettingsError
		require.ErrorAs(t, err, &settingsErr)
		assert.Contains(t, settingsErr.Fields, "role_mapping[1].role_id")
		assert.Empty(t, configs.writes)
	})

	t.Run("a role lookup failure is returned as is", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		roles := knownRoles(2)
		roles.err = errors.New("connection refused")
		err := NewSSOSettingsService(configs, roles, noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate())
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrSSOUnknownRole)
		assert.NotErrorIs(t, err, apperrors.ErrValidation)
		assert.Empty(t, configs.writes)
	})

	t.Run("malformed input is a validation error, stores nothing and checks no role", func(t *testing.T) {
		for name, mutate := range map[string]func(*SSOSettingsUpdate){
			"issuer_url":              func(u *SSOSettingsUpdate) { u.IssuerURL = "http://idp.example.com" },
			"client_id":               func(u *SSOSettingsUpdate) { u.ClientID = strings.Repeat("a", 256) },
			"client_secret":           func(u *SSOSettingsUpdate) { u.ClientSecret = strPtr(strings.Repeat("s", 1025)) },
			"scopes":                  func(u *SSOSettingsUpdate) { u.Scopes = []string{"profile"} },
			"scopes[1]":               func(u *SSOSettingsUpdate) { u.Scopes = []string{"openid", "a b"} },
			"role_mapping[0].claim":   func(u *SSOSettingsUpdate) { u.RoleMapping[0].Claim = "email" },
			"role_mapping[0].value":   func(u *SSOSettingsUpdate) { u.RoleMapping[0].Value = "  " },
			"role_mapping[0].role_id": func(u *SSOSettingsUpdate) { u.RoleMapping[0].RoleID = 0 },
			"role_mapping[1]": func(u *SSOSettingsUpdate) {
				u.RoleMapping = append(u.RoleMapping, SSORoleMapping{Claim: "groups", Value: " g-1 ", RoleID: 3})
			},
			"role_mapping": func(u *SSOSettingsUpdate) {
				u.RoleMapping = make([]SSORoleMapping, 201)
			},
		} {
			configs := newFakeSSOConfigs()
			roles := knownRoles(2)
			update := validSSOUpdate()
			mutate(&update)
			err := NewSSOSettingsService(configs, roles, noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update)
			require.ErrorIs(t, err, apperrors.ErrValidation, name)
			var settingsErr *SSOSettingsError
			require.ErrorAs(t, err, &settingsErr, name)
			assert.Contains(t, settingsErr.Fields, name, name)
			assert.Empty(t, configs.writes, name)
			assert.Empty(t, roles.lookup, name)
		}
	})

	t.Run("absent client_secret keeps the stored secret, empty clears it", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{})
		update := validSSOUpdate()
		update.ClientSecret = strPtr("first-secret")
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Equal(t, "first-secret", configs.values[models.ConfigKeySSOClientSecret])

		update.ClientSecret = nil
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Equal(t, "first-secret", configs.values[models.ConfigKeySSOClientSecret])

		update.ClientSecret = strPtr("")
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Equal(t, "", configs.values[models.ConfigKeySSOClientSecret])
	})

	t.Run("the certificate pair is written only when sent", func(t *testing.T) {
		pair := validSSOTestCertificate(t)
		configs := newFakeSSOConfigs()
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{})
		require.NoError(t, svc.Update(ssoCtx(), validSSOUpdate()))
		assert.NotContains(t, configs.writes, models.ConfigKeySSOClientCertificate)
		assert.NotContains(t, configs.writes, models.ConfigKeySSOClientPrivateKey)

		update := validSSOUpdate()
		update.ClientCertificate, update.ClientPrivateKey = strPtr(pair.certPEM), strPtr(pair.keyPEM)
		configs.writes = nil
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Contains(t, configs.writes, models.ConfigKeySSOClientCertificate)
		assert.Contains(t, configs.writes, models.ConfigKeySSOClientPrivateKey)
		assert.Equal(t, strings.TrimSpace(pair.certPEM), configs.values[models.ConfigKeySSOClientCertificate])
	})

	t.Run("an empty update clears every stored value", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), envSSO())
		require.NoError(t, svc.Update(ssoCtx(), validSSOUpdate()))
		require.NoError(t, svc.Update(ssoCtx(), SSOSettingsUpdate{ClientSecret: strPtr("")}))
		for key, value := range configs.values {
			assert.Equal(t, "", value, key)
		}
	})
}

func TestSSOSettingsUpdateIssuerChange(t *testing.T) {
	const stored = "https://stored-idp.example.test/v2.0"
	withStored := func() *fakeSSOConfigs {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOIssuerURL] = stored
		return configs
	}

	t.Run("issuer limit equals the user_identities.issuer width", func(t *testing.T) {
		assert.Equal(t, models.UserIdentityIssuerMaxLength, config.OIDCIssuerMaxBytes)
	})

	t.Run("a changed issuer with links is refused, counted and stores nothing", func(t *testing.T) {
		configs := withStored()
		links := &fakeIssuerLinks{links: 4}
		err := NewSSOSettingsService(configs, knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate())
		require.ErrorIs(t, err, ErrSSOIssuerChangeUnconfirmed)
		var changeErr *SSOIssuerChangeError
		require.ErrorAs(t, err, &changeErr)
		assert.Equal(t, int64(4), changeErr.Links)
		assert.Equal(t, []string{stored}, links.calls)
		assert.Empty(t, configs.writes, "no security.sso.* row written")
		assert.Equal(t, stored, configs.values[models.ConfigKeySSOIssuerURL])
	})

	t.Run("the same request with ConfirmIssuerChange is stored without a count", func(t *testing.T) {
		configs := withStored()
		links := &fakeIssuerLinks{links: 4}
		update := validSSOUpdate()
		update.ConfirmIssuerChange = true
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), update))
		assert.Equal(t, update.IssuerURL, configs.values[models.ConfigKeySSOIssuerURL])
		assert.Empty(t, links.calls)
	})

	t.Run("a changed issuer with no link is stored without the flag", func(t *testing.T) {
		configs := withStored()
		links := noIssuerLinks()
		update := validSSOUpdate()
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), update))
		assert.Equal(t, update.IssuerURL, configs.values[models.ConfigKeySSOIssuerURL])
		assert.Equal(t, []string{stored}, links.calls)
	})

	t.Run("an unchanged issuer and a first issuer call no counter", func(t *testing.T) {
		configs := withStored()
		links := &fakeIssuerLinks{links: 4}
		update := validSSOUpdate()
		update.IssuerURL = "  " + stored + " "
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), update))
		assert.Empty(t, links.calls, "same issuer after trimming")

		// Stored issuer cleared while OIDC_ISSUER_URL is the same value.
		env := config.SSOConfig{IssuerURL: stored}
		update.IssuerURL = ""
		require.NoError(t, NewSSOSettingsService(withStored(), knownRoles(2), links, env).Update(ssoCtx(), update))
		assert.Empty(t, links.calls, "the effective issuer stays the same")

		// No effective issuer before the save.
		configs = newFakeSSOConfigs()
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate()))
		assert.Empty(t, links.calls, "a first issuer")
		assert.Equal(t, validSSOUpdate().IssuerURL, configs.values[models.ConfigKeySSOIssuerURL])

		// An invalid OIDC_ISSUER_URL is unset, so nothing is in force either.
		configs = newFakeSSOConfigs()
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), nil, config.SSOConfig{IssuerURL: "http://env-idp.example.test"}).
			Update(ssoCtx(), validSSOUpdate()), "a nil counter is never asked")
	})

	t.Run("clearing a stored issuer under a different env issuer counts the stored one", func(t *testing.T) {
		configs := withStored()
		links := &fakeIssuerLinks{links: 1}
		update := validSSOUpdate()
		update.IssuerURL = ""
		err := NewSSOSettingsService(configs, knownRoles(2), links, envSSO()).Update(ssoCtx(), update)
		var changeErr *SSOIssuerChangeError
		require.ErrorAs(t, err, &changeErr)
		assert.Equal(t, int64(1), changeErr.Links)
		assert.Equal(t, []string{stored}, links.calls)
		assert.Empty(t, configs.writes)

		// Clearing it with no env issuer at all is a change too.
		links = &fakeIssuerLinks{links: 1}
		err = NewSSOSettingsService(withStored(), knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), update)
		require.ErrorAs(t, err, &changeErr)
		assert.Equal(t, []string{stored}, links.calls)
	})

	t.Run("an env issuer in force is the current one", func(t *testing.T) {
		links := &fakeIssuerLinks{links: 2}
		configs := newFakeSSOConfigs()
		err := NewSSOSettingsService(configs, knownRoles(2), links, envSSO()).Update(ssoCtx(), validSSOUpdate())
		require.ErrorIs(t, err, ErrSSOIssuerChangeUnconfirmed)
		assert.Equal(t, []string{envSSO().IssuerURL}, links.calls)
		assert.Empty(t, configs.writes)
	})

	t.Run("a count error is not a refusal and stores nothing", func(t *testing.T) {
		configs := withStored()
		links := &fakeIssuerLinks{err: errors.New("connection refused")}
		err := NewSSOSettingsService(configs, knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate())
		require.Error(t, err)
		var changeErr *SSOIssuerChangeError
		assert.False(t, errors.As(err, &changeErr))
		assert.NotErrorIs(t, err, ErrSSOIssuerChangeUnconfirmed)
		assert.NotErrorIs(t, err, apperrors.ErrValidation)
		assert.Empty(t, configs.writes)

		// A needed count without a counter fails the same way.
		configs = withStored()
		err = NewSSOSettingsService(configs, knownRoles(2), nil, config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate())
		require.Error(t, err)
		assert.False(t, errors.As(err, &changeErr))
		assert.Empty(t, configs.writes)
	})

	t.Run("a stored issuer that cannot be read is an error, not the env issuer", func(t *testing.T) {
		configs := withStored()
		configs.readErrs[models.ConfigKeySSOIssuerURL] = errors.New("connection refused")
		links := noIssuerLinks()
		update := validSSOUpdate()
		update.IssuerURL = envSSO().IssuerURL
		err := NewSSOSettingsService(configs, knownRoles(2), links, envSSO()).Update(ssoCtx(), update)
		require.Error(t, err)
		var changeErr *SSOIssuerChangeError
		assert.False(t, errors.As(err, &changeErr))
		assert.Empty(t, links.calls)
		assert.Empty(t, configs.writes)
	})

	t.Run("a 513-byte issuer is refused on issuer_url", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		links := noIssuerLinks()
		update := validSSOUpdate()
		update.IssuerURL = "https://idp.example.test/" + strings.Repeat("a", 513-len("https://idp.example.test/"))
		require.Len(t, update.IssuerURL, 513)
		err := NewSSOSettingsService(configs, knownRoles(2), links, config.SSOConfig{}).Update(ssoCtx(), update)
		var settingsErr *SSOSettingsError
		require.ErrorAs(t, err, &settingsErr)
		assert.Contains(t, settingsErr.Fields["issuer_url"], "at most 512 bytes")
		assert.Empty(t, configs.writes)
		assert.Empty(t, links.calls)
	})
}

// TestSSOSettingsSecretEncryptedAtRest runs the shipped configuration service
// over the SQLite configuration repository: the client secret and the role
// mapping are stored as ciphertext.
func TestSSOSettingsSecretEncryptedAtRest(t *testing.T) {
	db, configs := newSQLiteSSOConfigs(t)
	svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{})

	const secret = "plaintext-client-secret-0434"
	const group = "plaintext-group-object-id-0434"
	pair := validSSOTestCertificate(t)
	update := validSSOUpdate()
	update.ClientSecret = strPtr(secret)
	update.RoleMapping = []SSORoleMapping{{Claim: "groups", Value: group, RoleID: 2}}
	update.ClientCertificate = strPtr(pair.certPEM)
	update.ClientPrivateKey = strPtr(pair.keyPEM)
	require.NoError(t, svc.Update(ssoCtx(), update))

	for _, key := range []string{models.ConfigKeySSOClientSecret, models.ConfigKeySSORoleMapping,
		models.ConfigKeySSOClientCertificate, models.ConfigKeySSOClientPrivateKey} {
		var row models.Configuration
		require.NoError(t, db.Where("config_key = ?", key).First(&row).Error)
		assert.True(t, strings.HasPrefix(row.Value, "enc:v1:"), "%s is sealed", key)
		assert.NotContains(t, row.Value, secret, key)
		assert.NotContains(t, row.Value, group, key)
		assert.NotContains(t, row.Value, "BEGIN", key)
	}

	cfg, err := svc.SSOConfig(ssoCtx())
	require.NoError(t, err)
	assert.Equal(t, secret, cfg.ClientSecret)
	assert.Equal(t, []SSORoleMapping{{Claim: "groups", Value: group, RoleID: 2}}, cfg.RoleMapping)
	assert.True(t, cfg.Enabled)
	require.NotNil(t, cfg.ClientCertificate)
	assert.Equal(t, sha1Thumbprint(pair.cert), cfg.ClientCertificate.ThumbprintSHA1)
}

// newSQLiteSSOConfigs is the shipped configuration service over the SQLite
// configuration repository, with the defaults seeded.
func newSQLiteSSOConfigs(t *testing.T) (*gorm.DB, ConfigurationService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&models.Configuration{}))
	repo := repository.NewConfigurationRepository(db)
	require.NoError(t, repo.InitializeDefaults())
	return db, NewConfigurationService(repo, testConfigurationSecretBox())
}

// countingSSOConfigs records every key read, to prove the password policy
// reads only its own key.
type countingSSOConfigs struct {
	*fakeSSOConfigs
	reads []string
}

func (c *countingSSOConfigs) GetString(key string) (string, error) {
	c.reads = append(c.reads, key)
	return c.fakeSSOConfigs.GetString(key)
}

func (c *countingSSOConfigs) GetSecret(key string) (string, error) {
	c.reads = append(c.reads, key)
	return c.fakeSSOConfigs.GetSecret(key)
}

func TestAllowPasswordForSSOAccounts(t *testing.T) {
	unset := config.SSOConfig{}
	envTrue := config.SSOConfig{AllowPassword: true, AllowPasswordSet: true}
	envFalse := config.SSOConfig{AllowPassword: false, AllowPasswordSet: true}
	cases := []struct {
		name    string
		stored  string
		readErr error
		env     config.SSOConfig
		want    bool
	}{
		{name: "stored_true", stored: "true", env: unset, want: true},
		{name: "stored_true_over_env_false", stored: "true", env: envFalse, want: true},
		{name: "stored_false", stored: "false", env: unset, want: false},
		{name: "stored_false_over_env_true", stored: "false", env: envTrue, want: false},
		{name: "empty_env_true", stored: "", env: envTrue, want: true},
		{name: "empty_env_false", stored: "", env: envFalse, want: false},
		{name: "empty_env_unset", stored: "", env: unset, want: false},
		{name: "read_error_env_true_fails_closed", readErr: errors.New("connection refused"), env: envTrue, want: false},
		{name: "missing_row_env_true_fails_closed", readErr: apperrors.ErrNotFound, env: envTrue, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configs := &countingSSOConfigs{fakeSSOConfigs: newFakeSSOConfigs()}
			configs.values[models.ConfigKeySSOAllowPassword] = tc.stored
			if tc.readErr != nil {
				configs.readErrs[models.ConfigKeySSOAllowPassword] = tc.readErr
			}
			svc := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), tc.env)
			assert.Equal(t, tc.want, svc.AllowPasswordForSSOAccounts(ssoCtx()))
			assert.Equal(t, []string{models.ConfigKeySSOAllowPassword}, configs.reads,
				"only the password setting is read; the secret and the mapping are not")
		})
	}

	t.Run("cancelled_context_fails_closed", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOAllowPassword] = "true"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		assert.False(t, NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), unset).AllowPasswordForSSOAccounts(ctx))
	})
}

func TestSSOSettingsUpdateAllowPassword(t *testing.T) {
	t.Run("true and false are stored, nil clears so the env applies", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		env := config.SSOConfig{AllowPassword: true, AllowPasswordSet: true}
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), env)

		update := validSSOUpdate()
		update.AllowPassword = boolPtr(true)
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Equal(t, "true", configs.values[models.ConfigKeySSOAllowPassword])

		update.AllowPassword = boolPtr(false)
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Equal(t, "false", configs.values[models.ConfigKeySSOAllowPassword])
		cfg, err := svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.False(t, cfg.AllowPassword)
		assert.Equal(t, SSOSourceSettings, cfg.Sources.AllowPassword)

		update.AllowPassword = nil
		require.NoError(t, svc.Update(ssoCtx(), update))
		assert.Equal(t, "", configs.values[models.ConfigKeySSOAllowPassword])
		cfg, err = svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.True(t, cfg.AllowPassword, "OIDC_ALLOW_PASSWORD applies again")
		assert.Equal(t, SSOSourceEnv, cfg.Sources.AllowPassword)
	})

	t.Run("write position: before the other settings, after a leading disable switch", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		update := validSSOUpdate()
		update.AllowPassword = boolPtr(false)
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update))
		assert.Equal(t, []string{
			models.ConfigKeySSOAllowPassword, models.ConfigKeySSOIssuerURL, models.ConfigKeySSOClientID,
			models.ConfigKeySSOScopes, models.ConfigKeySSORoleMapping, models.ConfigKeySSOEnabled,
		}, configs.writes, "enabling: the switch stays last")

		configs = newFakeSSOConfigs()
		update.Enabled = boolPtr(false)
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update))
		assert.Equal(t, []string{
			models.ConfigKeySSOEnabled, models.ConfigKeySSOAllowPassword, models.ConfigKeySSOIssuerURL,
			models.ConfigKeySSOClientID, models.ConfigKeySSOScopes, models.ConfigKeySSORoleMapping,
		}, configs.writes, "disabling: the switch stays first")
	})

	t.Run("a refused update stores nothing, allow_password included", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		update := validSSOUpdate()
		update.AllowPassword = boolPtr(true)
		update.IssuerURL = "http://not-https.example.test"
		err := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update)
		require.ErrorIs(t, err, ErrSSOSettingsInvalid)
		assert.Empty(t, configs.writes)
	})
}

// ssoTestCertificate is a generated client certificate and its key, in PEM.
type ssoTestCertificate struct {
	cert     *x509.Certificate
	key      *rsa.PrivateKey
	certPEM  string
	keyPEM   string // PKCS#8
	pkcs1PEM string
}

// newSSOTestCertificate generates a self-signed certificate over a new RSA
// key of bits, valid from notBefore to notAfter.
func newSSOTestCertificate(t *testing.T, bits int, notBefore, notAfter time.Time) ssoTestCertificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	return newSSOTestCertificateForKey(t, key, &key.PublicKey, notBefore, notAfter)
}

func newSSOTestCertificateForKey(t *testing.T, key *rsa.PrivateKey, public any, notBefore, notAfter time.Time) ssoTestCertificate {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "gophercrm-test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return ssoTestCertificate{
		cert:     cert,
		key:      key,
		certPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		keyPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		pkcs1PEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
	}
}

func validSSOTestCertificate(t *testing.T) ssoTestCertificate {
	t.Helper()
	now := time.Now()
	return newSSOTestCertificate(t, 2048, now.Add(-time.Hour), now.Add(365*24*time.Hour))
}

// sha1Thumbprint is computed here from the DER, independently of the code.
func sha1Thumbprint(cert *x509.Certificate) string {
	sum := sha1.Sum(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// pemFragments are substrings of a PEM value that no message may contain:
// each body line of 16 or more characters.
func pemFragments(values ...string) []string {
	var out []string
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if len(line) >= 16 && !strings.HasPrefix(line, "-----") {
				out = append(out, line[:16])
			}
		}
	}
	return out
}

func TestSSOSettingsClientCertificate(t *testing.T) {
	valid := validSSOTestCertificate(t)

	withPair := func(certPEM, keyPEM string) SSOSettingsUpdate {
		update := validSSOUpdate()
		update.ClientCertificate = strPtr(certPEM)
		update.ClientPrivateKey = strPtr(keyPEM)
		return update
	}

	for name, keyPEM := range map[string]string{"PKCS#8": valid.keyPEM, "PKCS#1": valid.pkcs1PEM} {
		t.Run("accepts a matching RSA-2048 pair, "+name, func(t *testing.T) {
			configs := newFakeSSOConfigs()
			svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{})
			require.NoError(t, svc.Update(ssoCtx(), withPair(valid.certPEM, keyPEM)))
			cfg, err := svc.SSOConfig(ssoCtx())
			require.NoError(t, err)
			require.NotNil(t, cfg.ClientCertificate)
			assert.Equal(t, sha1Thumbprint(valid.cert), cfg.ClientCertificate.ThumbprintSHA1)
			assert.True(t, valid.cert.NotAfter.Equal(cfg.ClientCertificate.NotAfter))
			assert.True(t, valid.key.Equal(cfg.ClientCertificate.PrivateKey))
			assert.Equal(t, SSOSourceSettings, cfg.Sources.ClientCertificate)
		})
	}

	t.Run("accepts CRLF line endings and a trailing newline", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{})
		crlf := func(value string) string { return strings.ReplaceAll(value, "\n", "\r\n") + "\r\n" }
		require.NoError(t, svc.Update(ssoCtx(), withPair(crlf(valid.certPEM), crlf(valid.keyPEM))))
		cfg, err := svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		require.NotNil(t, cfg.ClientCertificate)
		assert.Equal(t, sha1Thumbprint(valid.cert), cfg.ClientCertificate.ThumbprintSHA1)
	})

	t.Run("write order: certificate, then key, then the enabled switch", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{}).
			Update(ssoCtx(), withPair(valid.certPEM, valid.keyPEM)))
		n := len(configs.writes)
		require.GreaterOrEqual(t, n, 3)
		assert.Equal(t, []string{models.ConfigKeySSOClientCertificate, models.ConfigKeySSOClientPrivateKey, models.ConfigKeySSOEnabled},
			configs.writes[n-3:])
	})

	t.Run("absent keeps the pair, empty clears it, a new pair replaces it", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{})
		require.NoError(t, svc.Update(ssoCtx(), withPair(valid.certPEM, valid.keyPEM)))

		require.NoError(t, svc.Update(ssoCtx(), validSSOUpdate()))
		assert.NotContains(t, configs.writes[len(configs.writes)-6:], models.ConfigKeySSOClientCertificate)
		cfg, err := svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		require.NotNil(t, cfg.ClientCertificate, "kept")

		other := validSSOTestCertificate(t)
		require.NoError(t, svc.Update(ssoCtx(), withPair(other.certPEM, other.pkcs1PEM)))
		cfg, err = svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		require.NotNil(t, cfg.ClientCertificate)
		assert.Equal(t, sha1Thumbprint(other.cert), cfg.ClientCertificate.ThumbprintSHA1, "replaced")

		require.NoError(t, svc.Update(ssoCtx(), withPair("", "")))
		assert.Equal(t, "", configs.values[models.ConfigKeySSOClientCertificate])
		assert.Equal(t, "", configs.values[models.ConfigKeySSOClientPrivateKey])
		cfg, err = svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Nil(t, cfg.ClientCertificate, "cleared")
		assert.Equal(t, SSOSourceDefault, cfg.Sources.ClientCertificate)
	})

	now := time.Now()
	small := newSSOTestCertificate(t, 1024, now.Add(-time.Hour), now.Add(time.Hour))
	expired := newSSOTestCertificate(t, 2048, now.Add(-48*time.Hour), now.Add(-time.Hour))
	mismatched := validSSOTestCertificate(t)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecPKCS8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	require.NoError(t, err)
	ecSEC1, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	//nolint:staticcheck // an encrypted legacy PEM block is what is being refused
	encryptedBlock, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(valid.key), []byte("pass"), x509.PEMCipherAES256)
	require.NoError(t, err)
	oversized := valid.certPEM + strings.Repeat(" ", SSOClientCertificateMaxBytes)

	type refusal struct {
		cert, key *string
		field     string
	}
	cases := map[string]refusal{
		"certificate is not PEM":            {strPtr("not a certificate"), strPtr(valid.keyPEM), "client_certificate"},
		"certificate is a key block":        {strPtr(valid.keyPEM), strPtr(valid.keyPEM), "client_certificate"},
		"certificate is a chain":            {strPtr(valid.certPEM + mismatched.certPEM), strPtr(valid.keyPEM), "client_certificate"},
		"certificate is not parseable":      {strPtr("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), strPtr(valid.keyPEM), "client_certificate"},
		"certificate has expired":           {strPtr(expired.certPEM), strPtr(expired.keyPEM), "client_certificate"},
		"key is not PEM":                    {strPtr(valid.certPEM), strPtr("not a key"), "client_private_key"},
		"key is encrypted (PKCS#8)":         {strPtr(valid.certPEM), strPtr(string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1, 2, 3}}))), "client_private_key"},
		"key is encrypted (legacy PEM)":     {strPtr(valid.certPEM), strPtr(string(pem.EncodeToMemory(encryptedBlock))), "client_private_key"},
		"key is ECDSA (PKCS#8)":             {strPtr(valid.certPEM), strPtr(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecPKCS8}))), "client_private_key"},
		"key is ECDSA (SEC 1)":              {strPtr(valid.certPEM), strPtr(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecSEC1}))), "client_private_key"},
		"key is RSA under 2048 bits":        {strPtr(small.certPEM), strPtr(small.keyPEM), "client_private_key"},
		"key does not match":                {strPtr(valid.certPEM), strPtr(mismatched.keyPEM), "client_private_key"},
		"only the certificate":              {strPtr(valid.certPEM), nil, "client_private_key"},
		"only the key":                      {nil, strPtr(valid.keyPEM), "client_certificate"},
		"certificate with an empty key":     {strPtr(valid.certPEM), strPtr(""), "client_private_key"},
		"key with an empty certificate":     {strPtr(""), strPtr(valid.keyPEM), "client_certificate"},
		"certificate over 16384 bytes":      {strPtr(oversized), strPtr(valid.keyPEM), "client_certificate"},
		"key over 16384 bytes":              {strPtr(valid.certPEM), strPtr(valid.keyPEM + strings.Repeat(" ", SSOClientPrivateKeyMaxBytes)), "client_private_key"},
		"certificate and key both mismatch": {strPtr(expired.certPEM), strPtr(mismatched.keyPEM), "client_certificate"},
	}
	for name, tc := range cases {
		t.Run("refuses: "+name, func(t *testing.T) {
			configs := newFakeSSOConfigs()
			configs.values[models.ConfigKeySSOClientCertificate] = valid.certPEM
			configs.values[models.ConfigKeySSOClientPrivateKey] = valid.keyPEM
			roles := knownRoles(2)
			update := validSSOUpdate()
			update.ClientCertificate, update.ClientPrivateKey = tc.cert, tc.key
			err := NewSSOSettingsService(configs, roles, noIssuerLinks(), config.SSOConfig{}).Update(ssoCtx(), update)
			require.ErrorIs(t, err, ErrSSOSettingsInvalid)
			var settingsErr *SSOSettingsError
			require.ErrorAs(t, err, &settingsErr)
			assert.Contains(t, settingsErr.Fields, tc.field)
			assert.Empty(t, configs.writes, "nothing is stored")
			assert.Empty(t, roles.lookup)
			assert.Equal(t, valid.certPEM, configs.values[models.ConfigKeySSOClientCertificate])
			assert.Equal(t, valid.keyPEM, configs.values[models.ConfigKeySSOClientPrivateKey])
			var submitted []string
			for _, value := range []*string{tc.cert, tc.key} {
				if value != nil {
					submitted = append(submitted, *value)
				}
			}
			for _, message := range settingsErr.Fields {
				assert.NotContains(t, message, "BEGIN")
				for _, fragment := range pemFragments(submitted...) {
					assert.NotContains(t, message, fragment)
				}
			}
		})
	}

	t.Run("an expired stored certificate stays in force", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOClientCertificate] = expired.certPEM
		configs.values[models.ConfigKeySSOClientPrivateKey] = expired.keyPEM
		cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), config.SSOConfig{}).SSOConfig(ssoCtx())
		require.NoError(t, err)
		require.NotNil(t, cfg.ClientCertificate)
		assert.True(t, cfg.ClientCertificate.NotAfter.Before(time.Now()))
	})

	t.Run("an unusable stored pair is unset and logged once without its content", func(t *testing.T) {
		for name, tc := range map[string]struct {
			cert, key string
			readErr   string // key whose read fails
			wantKey   string
		}{
			"unparseable certificate": {cert: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", key: valid.keyPEM, wantKey: models.ConfigKeySSOClientCertificate},
			"mismatched key":          {cert: valid.certPEM, key: mismatched.keyPEM, wantKey: models.ConfigKeySSOClientPrivateKey},
			"incomplete pair":         {cert: valid.certPEM, key: "", wantKey: models.ConfigKeySSOClientPrivateKey},
			"unreadable key":          {cert: valid.certPEM, key: valid.keyPEM, readErr: models.ConfigKeySSOClientPrivateKey, wantKey: models.ConfigKeySSOClientPrivateKey},
		} {
			t.Run(name, func(t *testing.T) {
				logs := captureServiceLogs(t)
				configs := newFakeSSOConfigs()
				configs.values[models.ConfigKeySSOClientSecret] = "stored-secret"
				configs.values[models.ConfigKeySSOClientCertificate] = tc.cert
				configs.values[models.ConfigKeySSOClientPrivateKey] = tc.key
				if tc.readErr != "" {
					configs.readErrs[tc.readErr] = errors.New("driver: connection refused at 10.0.0.9")
				}
				cfg, err := NewSSOSettingsService(configs, knownRoles(), noIssuerLinks(), config.SSOConfig{}).SSOConfig(ssoCtx())
				require.NoError(t, err)
				assert.Nil(t, cfg.ClientCertificate)
				assert.Equal(t, SSOSourceDefault, cfg.Sources.ClientCertificate)
				assert.Equal(t, "stored-secret", cfg.ClientSecret, "the secret flow applies")
				assertOneUnusableCertificateWarning(t, logs.String(), tc.wantKey, tc.cert, tc.key)
				assert.NotContains(t, logs.String(), "connection refused")
			})
		}
	})

	t.Run("an undecryptable stored certificate is unset and logged once", func(t *testing.T) {
		db, configs := newSQLiteSSOConfigs(t)
		svc := NewSSOSettingsService(configs, knownRoles(2), noIssuerLinks(), config.SSOConfig{})
		update := withPair(valid.certPEM, valid.keyPEM)
		update.ClientSecret = strPtr("stored-secret")
		require.NoError(t, svc.Update(ssoCtx(), update))
		const garbage = "enc:v1:bm90LWEtcmVhbC1jaXBoZXJ0ZXh0LXZhbHVl"
		require.NoError(t, db.Model(&models.Configuration{}).Where("config_key = ?", models.ConfigKeySSOClientCertificate).
			Update("value", garbage).Error)

		logs := captureServiceLogs(t)
		cfg, err := svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Nil(t, cfg.ClientCertificate)
		assert.Equal(t, "stored-secret", cfg.ClientSecret)
		assertOneUnusableCertificateWarning(t, logs.String(), models.ConfigKeySSOClientCertificate, valid.certPEM, valid.keyPEM, garbage[len("enc:v1:"):])
	})

	t.Run("nothing stored logs nothing", func(t *testing.T) {
		logs := captureServiceLogs(t)
		cfg, err := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), noIssuerLinks(), config.SSOConfig{}).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Nil(t, cfg.ClientCertificate)
		assert.NotContains(t, logs.String(), "client certificate")
	})
}

// assertOneUnusableCertificateWarning checks the captured JSON log: exactly one
// entry with the unusable-certificate message, whose fields are only
// component and config_key, and no PEM content anywhere.
func assertOneUnusableCertificateWarning(t *testing.T, output, wantKey string, stored ...string) {
	t.Helper()
	const message = "Stored single sign-on client certificate is not usable and is treated as unset"
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["msg"] == message {
			found = append(found, entry)
		}
	}
	require.Len(t, found, 1, output)
	entry := found[0]
	assert.Equal(t, "warning", entry["level"])
	assert.Equal(t, "sso_settings", entry["component"])
	assert.Equal(t, wantKey, entry["config_key"])
	for field := range entry {
		assert.Contains(t, []string{"level", "msg", "time", "component", "config_key"}, field)
	}
	assert.NotContains(t, output, "BEGIN")
	for _, fragment := range pemFragments(stored...) {
		assert.NotContains(t, output, fragment)
	}
}
