package service

import (
	"strings"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// collationConfigRepo is a ConfigurationRepository whose GetByKey resolves
// keys the way the configurations table does on MySQL and MariaDB
// (utf8mb4_unicode_ci: case-insensitive, trailing spaces ignored) and returns
// the canonical row. It records every write.
type collationConfigRepo struct {
	rows    map[string]*models.Configuration
	updates []string
	deletes []string
}

func newCollationConfigRepo(rows ...models.Configuration) *collationConfigRepo {
	repo := &collationConfigRepo{rows: map[string]*models.Configuration{}}
	for i := range rows {
		row := rows[i]
		repo.rows[row.Key] = &row
	}
	return repo
}

func (r *collationConfigRepo) GetByKey(key string) (*models.Configuration, error) {
	folded := strings.ToLower(strings.TrimRight(key, " "))
	for canonical, row := range r.rows {
		if strings.ToLower(canonical) == folded {
			copied := *row
			return &copied, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *collationConfigRepo) GetByCategory(models.ConfigurationCategory) ([]models.Configuration, error) {
	return nil, nil
}
func (r *collationConfigRepo) GetAll() ([]models.Configuration, error) { return nil, nil }
func (r *collationConfigRepo) Create(*models.Configuration) error      { return nil }
func (r *collationConfigRepo) Update(config *models.Configuration) error {
	r.updates = append(r.updates, config.Key)
	copied := *config
	r.rows[config.Key] = &copied
	return nil
}
func (r *collationConfigRepo) Delete(key string) error {
	r.deletes = append(r.deletes, key)
	return nil
}
func (r *collationConfigRepo) BulkUpsert([]models.Configuration) error { return nil }
func (r *collationConfigRepo) InitializeDefaults() error               { return nil }

// TestConfigurationServiceReservesSSOKeys pins that the generic writes refuse
// the reserved single sign-on rows by the RESOLVED key, whatever spelling the
// collation matched, and that SetSSO writes only those rows.
func TestConfigurationServiceReservesSSOKeys(t *testing.T) {
	seed := func() *collationConfigRepo {
		var rows []models.Configuration
		for _, config := range models.DefaultConfigurations() {
			if models.IsSSOConfigurationKey(config.Key) || config.Key == "general.company_name" {
				// Not system, so a generic Delete would otherwise be allowed
				// to reach the repository.
				config.IsSystem = false
				rows = append(rows, config)
			}
		}
		return newCollationConfigRepo(rows...)
	}

	for _, key := range []string{
		"SECURITY.SSO.ROLE_MAPPING",
		"Security.Sso.Client_Secret",
		"security.sso.role_mapping ",
		models.ConfigKeySSOEnabled,
	} {
		t.Run(key, func(t *testing.T) {
			repo := seed()
			svc := NewConfigurationService(repo, testConfigurationSecretBox())

			_, err := repo.GetByKey(key)
			require.NoError(t, err, "the fake resolves the spelling, as the collation does")

			err = svc.Set(key, "true")
			assert.True(t, apperrors.IsNotFound(err), "Set: %v", err)
			err = svc.Reset(key)
			assert.True(t, apperrors.IsNotFound(err), "Reset: %v", err)
			err = svc.Delete(key)
			assert.True(t, apperrors.IsNotFound(err), "Delete: %v", err)

			assert.Empty(t, repo.updates, "no Update reached the repository")
			assert.Empty(t, repo.deletes, "no Delete reached the repository")
		})
	}

	t.Run("SetSSO refuses a non-SSO row", func(t *testing.T) {
		repo := seed()
		svc := NewConfigurationService(repo, testConfigurationSecretBox())
		err := svc.SetSSO("general.company_name", "x")
		assert.True(t, apperrors.IsNotFound(err), "%v", err)
		assert.Empty(t, repo.updates)
	})

	t.Run("SetSSO writes an SSO row", func(t *testing.T) {
		repo := seed()
		svc := NewConfigurationService(repo, testConfigurationSecretBox())
		require.NoError(t, svc.SetSSO(models.ConfigKeySSOClientID, "x"))
		assert.Equal(t, []string{models.ConfigKeySSOClientID}, repo.updates)
		assert.Equal(t, "x", repo.rows[models.ConfigKeySSOClientID].Value)
	})

	t.Run("generic Set still writes a plain row", func(t *testing.T) {
		repo := seed()
		svc := NewConfigurationService(repo, testConfigurationSecretBox())
		require.NoError(t, svc.Set("general.company_name", "Acme"))
		assert.Equal(t, []string{"general.company_name"}, repo.updates)
	})
}
