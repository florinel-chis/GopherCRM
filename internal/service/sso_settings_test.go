package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeSSOConfigs is a ConfigurationService holding the six SSO rows in
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

func boolPtr(v bool) *bool    { return &v }
func ssoCtx() context.Context { return context.Background() }
func envSSO() config.SSOConfig {
	return config.SSOConfig{
		Enabled:      true,
		EnabledSet:   true,
		IssuerURL:    "https://env-idp.example.test",
		ClientID:     "env-client",
		ClientSecret: "env-secret-value",
		Scopes:       "openid email",
		RoleMapping:  `[{"claim":"groups","value":"env-group","role_id":3}]`,
	}
}

func TestSSOConfig(t *testing.T) {
	t.Run("nothing configured: disabled, defaults", func(t *testing.T) {
		svc := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), config.SSOConfig{})
		cfg, err := svc.SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.False(t, cfg.Enabled)
		assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Scopes)
		assert.Empty(t, cfg.IssuerURL)
		assert.Empty(t, cfg.ClientID)
		assert.Empty(t, cfg.ClientSecret)
		assert.Empty(t, cfg.RoleMapping)
		assert.Equal(t, SSOConfigSources{
			Enabled: SSOSourceDefault, IssuerURL: SSOSourceDefault, ClientID: SSOSourceDefault,
			ClientSecret: SSOSourceDefault, Scopes: SSOSourceDefault, RoleMapping: SSOSourceDefault,
		}, cfg.Sources)
	})

	t.Run("settings win over env", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "false"
		configs.values[models.ConfigKeySSOIssuerURL] = "https://stored-idp.example.test/v2.0"
		configs.values[models.ConfigKeySSOClientID] = "stored-client"
		configs.values[models.ConfigKeySSOClientSecret] = "stored-secret"
		configs.values[models.ConfigKeySSOScopes] = "openid profile groups"
		configs.values[models.ConfigKeySSORoleMapping] = `[{"claim":"roles","value":"crm.admin","role_id":1}]`
		cfg, err := NewSSOSettingsService(configs, knownRoles(), envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.False(t, cfg.Enabled)
		assert.Equal(t, "https://stored-idp.example.test/v2.0", cfg.IssuerURL)
		assert.Equal(t, "stored-client", cfg.ClientID)
		assert.Equal(t, "stored-secret", cfg.ClientSecret)
		assert.Equal(t, []string{"openid", "profile", "groups"}, cfg.Scopes)
		assert.Equal(t, []SSORoleMapping{{Claim: "roles", Value: "crm.admin", RoleID: 1}}, cfg.RoleMapping)
		assert.Equal(t, SSOConfigSources{
			Enabled: SSOSourceSettings, IssuerURL: SSOSourceSettings, ClientID: SSOSourceSettings,
			ClientSecret: SSOSourceSettings, Scopes: SSOSourceSettings, RoleMapping: SSOSourceSettings,
		}, cfg.Sources)
	})

	t.Run("env used when settings are empty", func(t *testing.T) {
		cfg, err := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.True(t, cfg.Enabled)
		assert.Equal(t, "https://env-idp.example.test", cfg.IssuerURL)
		assert.Equal(t, "env-client", cfg.ClientID)
		assert.Equal(t, "env-secret-value", cfg.ClientSecret)
		assert.Equal(t, []string{"openid", "email"}, cfg.Scopes)
		assert.Equal(t, []SSORoleMapping{{Claim: "groups", Value: "env-group", RoleID: 3}}, cfg.RoleMapping)
		assert.Equal(t, SSOConfigSources{
			Enabled: SSOSourceEnv, IssuerURL: SSOSourceEnv, ClientID: SSOSourceEnv,
			ClientSecret: SSOSourceEnv, Scopes: SSOSourceEnv, RoleMapping: SSOSourceEnv,
		}, cfg.Sources)
	})

	t.Run("enabled fails closed on a read error even with env true", func(t *testing.T) {
		for name, err := range map[string]error{
			"database error": errors.New("connection refused"),
			"missing row":    apperrors.ErrNotFound,
		} {
			configs := newFakeSSOConfigs()
			configs.readErrs[models.ConfigKeySSOEnabled] = err
			cfg, cfgErr := NewSSOSettingsService(configs, knownRoles(), envSSO()).SSOConfig(ssoCtx())
			require.NoError(t, cfgErr, name)
			assert.False(t, cfg.Enabled, name)
			assert.Equal(t, SSOSourceDefault, cfg.Sources.Enabled, name)
		}
	})

	t.Run("mapping fails closed on a read error", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.readErrs[models.ConfigKeySSORoleMapping] = errors.New("connection refused")
		cfg, err := NewSSOSettingsService(configs, knownRoles(), envSSO()).SSOConfig(ssoCtx())
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
			cfg, err := NewSSOSettingsService(configs, knownRoles(), envSSO()).SSOConfig(ssoCtx())
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
		cfg, err := NewSSOSettingsService(configs, knownRoles(), envSSO()).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.IssuerURL)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.ClientID)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.ClientSecret)
		assert.Equal(t, SSOSourceEnv, cfg.Sources.Scopes)
	})

	t.Run("invalid env issuer and invalid env mapping are treated as unset", func(t *testing.T) {
		env := envSSO()
		env.IssuerURL = "http://idp.example.com"
		env.RoleMapping = `[{"claim":"email","value":"x","role_id":1}]`
		cfg, err := NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), env).SSOConfig(ssoCtx())
		require.NoError(t, err)
		assert.Empty(t, cfg.IssuerURL)
		assert.Equal(t, SSOSourceDefault, cfg.Sources.IssuerURL)
		assert.Empty(t, cfg.RoleMapping)
		assert.Equal(t, SSOSourceDefault, cfg.Sources.RoleMapping)

		env.RoleMapping = "not json"
		cfg, err = NewSSOSettingsService(newFakeSSOConfigs(), knownRoles(), env).SSOConfig(ssoCtx())
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
		svc := NewSSOSettingsService(configs, knownRoles(2), env)

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
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate()))
		require.NotEmpty(t, configs.writes)
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[len(configs.writes)-1])
		assert.Equal(t, 5, len(configs.writes), "the secret is not written when absent")
	})

	t.Run("write order: enabling via env (null) writes enabled last", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		update := validSSOUpdate()
		update.Enabled = nil
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), envSSO()).Update(ssoCtx(), update))
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[len(configs.writes)-1])
	})

	t.Run("write order: disabling writes enabled first", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "true"
		update := validSSOUpdate()
		update.Enabled = boolPtr(false)
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), config.SSOConfig{}).Update(ssoCtx(), update))
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[0])

		configs = newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "true"
		update.Enabled = nil
		env := envSSO()
		env.Enabled = false
		require.NoError(t, NewSSOSettingsService(configs, knownRoles(2), env).Update(ssoCtx(), update))
		assert.Equal(t, models.ConfigKeySSOEnabled, configs.writes[0], "stored true to null with env false")
	})

	t.Run("write order: a failing second write", func(t *testing.T) {
		// Enabling: the switch is never reached, so it stays as it was.
		configs := newFakeSSOConfigs()
		configs.failWrite = 2
		err := NewSSOSettingsService(configs, knownRoles(2), config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate())
		require.Error(t, err)
		assert.Equal(t, "", configs.values[models.ConfigKeySSOEnabled], "enabled unchanged")
		assert.NotContains(t, configs.writes, models.ConfigKeySSOEnabled)

		// Disabling: the switch went first and is already off.
		configs = newFakeSSOConfigs()
		configs.values[models.ConfigKeySSOEnabled] = "true"
		configs.failWrite = 2
		update := validSSOUpdate()
		update.Enabled = boolPtr(false)
		err = NewSSOSettingsService(configs, knownRoles(2), config.SSOConfig{}).Update(ssoCtx(), update)
		require.Error(t, err)
		assert.Equal(t, "false", configs.values[models.ConfigKeySSOEnabled])
	})

	t.Run("unknown role id is an invalid reference and stores nothing", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		update := validSSOUpdate()
		update.RoleMapping = []SSORoleMapping{{Claim: "groups", Value: "g-1", RoleID: 2}, {Claim: "roles", Value: "r", RoleID: 999999}}
		err := NewSSOSettingsService(configs, knownRoles(2), config.SSOConfig{}).Update(ssoCtx(), update)
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
		err := NewSSOSettingsService(configs, roles, config.SSOConfig{}).Update(ssoCtx(), validSSOUpdate())
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
			err := NewSSOSettingsService(configs, roles, config.SSOConfig{}).Update(ssoCtx(), update)
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
		svc := NewSSOSettingsService(configs, knownRoles(2), config.SSOConfig{})
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

	t.Run("an empty update clears every stored value", func(t *testing.T) {
		configs := newFakeSSOConfigs()
		svc := NewSSOSettingsService(configs, knownRoles(2), envSSO())
		require.NoError(t, svc.Update(ssoCtx(), validSSOUpdate()))
		require.NoError(t, svc.Update(ssoCtx(), SSOSettingsUpdate{ClientSecret: strPtr("")}))
		for key, value := range configs.values {
			assert.Equal(t, "", value, key)
		}
	})
}

// TestSSOSettingsSecretEncryptedAtRest runs the shipped configuration service
// over the SQLite configuration repository: the client secret and the role
// mapping are stored as ciphertext.
func TestSSOSettingsSecretEncryptedAtRest(t *testing.T) {
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
	configs := NewConfigurationService(repo, testConfigurationSecretBox())
	svc := NewSSOSettingsService(configs, knownRoles(2), config.SSOConfig{})

	const secret = "plaintext-client-secret-0434"
	const group = "plaintext-group-object-id-0434"
	update := validSSOUpdate()
	update.ClientSecret = strPtr(secret)
	update.RoleMapping = []SSORoleMapping{{Claim: "groups", Value: group, RoleID: 2}}
	require.NoError(t, svc.Update(ssoCtx(), update))

	for _, key := range []string{models.ConfigKeySSOClientSecret, models.ConfigKeySSORoleMapping} {
		var row models.Configuration
		require.NoError(t, db.Where("config_key = ?", key).First(&row).Error)
		assert.True(t, strings.HasPrefix(row.Value, "enc:v1:"), "%s is sealed", key)
		assert.NotContains(t, row.Value, secret, key)
		assert.NotContains(t, row.Value, group, key)
	}

	cfg, err := svc.SSOConfig(ssoCtx())
	require.NoError(t, err)
	assert.Equal(t, secret, cfg.ClientSecret)
	assert.Equal(t, []SSORoleMapping{{Claim: "groups", Value: group, RoleID: 2}}, cfg.RoleMapping)
	assert.True(t, cfg.Enabled)
}
