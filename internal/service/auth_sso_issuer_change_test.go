package service

// Issue #556: saving a different single sign-on issuer while accounts are
// linked under the current one needs confirmation. TestSSOIssuerChange{SQLite,
// MySQL} count the links through the real identity repository, on a SQLite
// file opened by the production connector and on a schema built by the real
// migrations (TEST_MYSQL_DSN, also pointed at MariaDB 10.11). The count takes
// every source and the links of an erased account; user_identities.issuer is
// utf8mb4_bin PAD SPACE on MySQL/MariaDB, so a row whose issuer carries a
// trailing space must not be counted.

import (
	"fmt"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const ssoIssuerChangeTestIssuer = "https://idp.example.test/tenant/v2.0"

func TestSSOIssuerChangeSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runSSOIssuerChange(t, db)
}

func TestSSOIssuerChangeMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runSSOIssuerChange(t, db)
}

func runSSOIssuerChange(t *testing.T, db *gorm.DB) {
	newUser := func(i int) *models.User {
		user := &models.User{Email: fmt.Sprintf("issuer-change-%d@example.test", i), Password: "x",
			FirstName: "Pat", LastName: "Linked", Role: models.RoleSales, IsActive: true}
		require.NoError(t, db.Create(user).Error)
		return user
	}
	link := func(user *models.User, issuer, subject, source string) {
		require.NoError(t, db.Create(&models.UserIdentity{UserID: user.ID, Issuer: issuer, Subject: subject, Source: source}).Error)
	}

	oidcUser, scimUser, erasedUser, otherUser := newUser(1), newUser(2), newUser(3), newUser(4)
	link(oidcUser, ssoIssuerChangeTestIssuer, "subject-oidc", models.UserIdentitySourceOIDC)
	link(scimUser, ssoIssuerChangeTestIssuer, "subject-scim", models.UserIdentitySourceSCIM)
	link(erasedUser, ssoIssuerChangeTestIssuer, "subject-erased", models.UserIdentitySourceOIDC)
	link(otherUser, ssoIssuerChangeTestIssuer+" ", "subject-padded", models.UserIdentitySourceOIDC)
	link(otherUser, "https://other-idp.example.test", "subject-other", models.UserIdentitySourceOIDC)
	// Erasure keeps the identity row and soft-deletes the account.
	require.NoError(t, db.Model(erasedUser).Update("email", "erased-0556@example.invalid").Error)
	require.NoError(t, db.Delete(erasedUser).Error)

	repo := repository.NewUserIdentityRepository(db)
	count, err := repo.CountByIssuer(ssoIssuerChangeTestIssuer)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count, "oidc, scim and the erased account's link; not the padded or the other issuer")

	count, err = repo.CountByIssuer("")
	require.NoError(t, err)
	assert.Zero(t, count)

	count, err = repo.CountByIssuer(ssoIssuerChangeTestIssuer + " ")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "the padded issuer counts only its own row")

	configs := newFakeSSOConfigs()
	configs.values[models.ConfigKeySSOIssuerURL] = ssoIssuerChangeTestIssuer
	svc := NewSSOSettingsService(configs, knownRoles(2), repo, config.SSOConfig{})
	update := validSSOUpdate()
	update.IssuerURL = "https://other-idp.example.test"
	err = svc.Update(ssoCtx(), update)
	var changeErr *SSOIssuerChangeError
	require.ErrorAs(t, err, &changeErr)
	assert.Equal(t, int64(3), changeErr.Links)
	assert.Empty(t, configs.writes)

	update.ConfirmIssuerChange = true
	require.NoError(t, svc.Update(ssoCtx(), update))
	assert.Equal(t, "https://other-idp.example.test", configs.values[models.ConfigKeySSOIssuerURL])
}
