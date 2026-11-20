package service

// Issue #469: the read of the SCIM 2.0 Users resource.
// TestSCIMUsers{SQLite,MySQL} run one table of checks through the real
// repository and service, on a SQLite file opened by the production connector
// and on a schema built by the real migrations (TEST_MYSQL_DSN, also pointed
// at MariaDB 10.11). users.email is utf8mb4_unicode_ci on MySQL/MariaDB, so
// LOWER(email) = LOWER(?) also returns padded, accented and Unicode-fold
// variants, and user_identities.issuer is utf8mb4_bin PAD SPACE, so an issuer
// with a trailing space comes back too: the MySQL root proves that the Go
// judges drop them.

import (
	"context"
	"fmt"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const scimUsersTestIssuer = "https://idp.example.test"

// scimIssuerStub is the configured single sign-on issuer.
type scimIssuerStub string

func (s scimIssuerStub) SSOConfig(context.Context) (EffectiveSSOConfig, error) {
	return EffectiveSSOConfig{IssuerURL: string(s)}, nil
}

func TestSCIMUsersSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runSCIMUsers(t, db, false)
}

func TestSCIMUsersMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runSCIMUsers(t, db, true)
}

func runSCIMUsers(t *testing.T, db *gorm.DB, mysql bool) {
	ctx := context.Background()
	repo := repository.NewSCIMUserRepository(db)
	svc := NewSCIMUserService(repo, scimIssuerStub(scimUsersTestIssuer))

	var existing int64
	require.NoError(t, db.Model(&models.User{}).Unscoped().Count(&existing).Error)
	require.Zero(t, existing, "the schema starts without users")

	seed := func(email string, role models.UserRole, active bool) *models.User {
		t.Helper()
		u := &models.User{Email: email, Password: "x", FirstName: "First " + email[:4], LastName: "Last " + email[:4], Role: role, IsActive: true}
		require.NoError(t, db.Create(u).Error)
		if !active {
			// The gorm default:true overwrites an explicit false on create.
			require.NoError(t, db.Model(u).Update("is_active", false).Error)
			u.IsActive = false
		}
		return u
	}
	identity := func(userID uint, issuer, subject string) {
		t.Helper()
		require.NoError(t, db.Create(&models.UserIdentity{UserID: userID, Issuer: issuer, Subject: subject}).Error)
	}

	jose := seed("jose@scim.example.test", models.RoleSales, true)
	kate := seed("kate@scim.example.test", models.RoleSupport, true)
	deactivated := seed("idle@scim.example.test", models.RoleSupport, false)
	other := seed("othr@scim.example.test", models.RoleAdmin, true)
	padded := seed("padi@scim.example.test", models.RoleSales, true)
	twice := seed("twic@scim.example.test", models.RoleSales, true)
	customer := seed("cust@scim.example.test", models.RoleCustomer, true)
	erased := seed("gone@scim.example.test", models.RoleSales, true)
	erasedEmail := erased.Email

	identity(jose.ID, scimUsersTestIssuer, "sub-jose")
	identity(other.ID, "https://other.example.test", "sub-other")
	identity(padded.ID, scimUsersTestIssuer+" ", "sub-padded")
	// The lower id carries the subject that sorts last, so only the id order
	// picks it.
	identity(twice.ID, scimUsersTestIssuer, "sub-z-first")
	identity(twice.ID, scimUsersTestIssuer, "sub-a-second")
	identity(customer.ID, scimUsersTestIssuer, "sub-customer")
	identity(erased.ID, scimUsersTestIssuer, "sub-erased")

	require.NoError(t, repository.NewUserRepository(db).Delete(erased.ID), "erasure through the real path")
	var erasedRow models.User
	require.NoError(t, db.Unscoped().First(&erasedRow, erased.ID).Error)
	require.True(t, erasedRow.DeletedAt.Valid, "erasure soft-deletes")

	visibleIDs := []uint{jose.ID, kate.ID, deactivated.ID, other.ID, padded.ID, twice.ID}

	list := func(t *testing.T, userName *string, startIndex, count int) *SCIMUserPage {
		t.Helper()
		page, err := svc.List(ctx, userName, startIndex, count)
		require.NoError(t, err)
		require.NotNil(t, page)
		require.NotNil(t, page.Users, "Users is never nil")
		return page
	}
	filter := func(t *testing.T, userName string) *SCIMUserPage {
		t.Helper()
		return list(t, &userName, 1, 100)
	}
	ids := func(page *SCIMUserPage) []uint {
		out := []uint{}
		for _, r := range page.Users {
			out = append(out, r.User.ID)
		}
		return out
	}
	// candidates asserts on MySQL/MariaDB that the collation returns the
	// stored row, so the totalResults 0 that follows is the Go judge's doing.
	// An engine that does not fold the pair is logged; the totalResults
	// assertion stays.
	candidates := func(t *testing.T, value string, want uint) {
		t.Helper()
		if !mysql {
			return
		}
		rows, err := repo.FindVisibleByEmailFolded(value)
		require.NoError(t, err)
		if len(rows) == 0 {
			t.Logf("this engine does not return the stored row for this variant; candidate assertion skipped")
			return
		}
		require.Len(t, rows, 1)
		assert.Equal(t, want, rows[0].ID, "the collation returns the stored row")
	}

	t.Run("filter exact returns the full record", func(t *testing.T) {
		page := filter(t, "jose@scim.example.test")
		assert.Equal(t, 1, page.Total)
		assert.Equal(t, 1, page.StartIndex)
		require.Len(t, page.Users, 1)
		got := page.Users[0]
		assert.Equal(t, jose.ID, got.User.ID)
		assert.Equal(t, "jose@scim.example.test", got.User.Email)
		assert.Equal(t, jose.FirstName, got.User.FirstName)
		assert.Equal(t, jose.LastName, got.User.LastName)
		assert.True(t, got.User.IsActive)
		assert.False(t, got.User.CreatedAt.IsZero())
		assert.False(t, got.User.UpdatedAt.IsZero())
		assert.Equal(t, "sub-jose", got.ExternalID)
	})

	t.Run("ASCII case variant matches", func(t *testing.T) {
		page := filter(t, "JOSE@Scim.Example.TEST")
		assert.Equal(t, 1, page.Total)
		assert.Equal(t, []uint{jose.ID}, ids(page))
	})

	t.Run("unknown userName", func(t *testing.T) {
		page := filter(t, "nobody@scim.example.test")
		assert.Equal(t, 0, page.Total)
		assert.Empty(t, page.Users)
	})

	for _, tc := range []struct {
		name, value string
		stored      *models.User
	}{
		{"pad variant", "jose@scim.example.test ", jose},
		{"accent variant", "josé@scim.example.test", jose},
		{"long s variant (U+017F)", "joſe@scim.example.test", jose},
		{"Kelvin variant (U+212A)", "Kate@scim.example.test", kate},
	} {
		t.Run(tc.name+" is judged out", func(t *testing.T) {
			candidates(t, tc.value, tc.stored.ID)
			page := filter(t, tc.value)
			assert.Equal(t, 0, page.Total)
			assert.Empty(t, page.Users)
		})
	}

	t.Run("customer is never returned", func(t *testing.T) {
		page := filter(t, customer.Email)
		assert.Equal(t, 0, page.Total)
		_, err := svc.Get(ctx, fmt.Sprint(customer.ID))
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.NotContains(t, ids(list(t, nil, 1, 100)), customer.ID)
	})

	t.Run("erased user is never returned", func(t *testing.T) {
		// (a) by id
		_, err := svc.Get(ctx, fmt.Sprint(erased.ID))
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		// (b) in the unfiltered list
		page := list(t, nil, 1, 100)
		assert.Equal(t, len(visibleIDs), page.Total)
		assert.NotContains(t, ids(page), erased.ID)
		// (c) by its former email
		assert.Equal(t, 0, filter(t, erasedEmail).Total)
	})

	t.Run("deactivated user is returned inactive", func(t *testing.T) {
		got, err := svc.Get(ctx, fmt.Sprint(deactivated.ID))
		require.NoError(t, err)
		assert.False(t, got.User.IsActive)
		page := filter(t, deactivated.Email)
		require.Len(t, page.Users, 1)
		assert.False(t, page.Users[0].User.IsActive)
	})

	t.Run("get", func(t *testing.T) {
		got, err := svc.Get(ctx, fmt.Sprint(jose.ID))
		require.NoError(t, err)
		assert.Equal(t, jose.Email, got.User.Email)
		assert.Equal(t, "sub-jose", got.ExternalID)
		for _, raw := range []string{"abc", "0", "-1", "+1", "1.0", "", "18446744073709551616", "999999"} {
			_, err := svc.Get(ctx, raw)
			assert.ErrorIs(t, err, apperrors.ErrNotFound, raw)
		}
	})

	t.Run("unfiltered list pages the visible users", func(t *testing.T) {
		page := list(t, nil, 1, 100)
		assert.Equal(t, len(visibleIDs), page.Total)
		assert.Equal(t, visibleIDs, ids(page), "id ascending")

		page = list(t, nil, 2, 2)
		assert.Equal(t, len(visibleIDs), page.Total)
		assert.Equal(t, 2, page.StartIndex)
		assert.Equal(t, visibleIDs[1:3], ids(page))

		page = list(t, nil, 6, 5)
		assert.Equal(t, visibleIDs[5:], ids(page))

		page = list(t, nil, 10, 5)
		assert.Equal(t, len(visibleIDs), page.Total)
		assert.Empty(t, page.Users)

		page = list(t, nil, 0, 2)
		assert.Equal(t, 1, page.StartIndex, "startIndex below 1 is 1")
		assert.Equal(t, visibleIDs[:2], ids(page))

		page = list(t, nil, 1, 0)
		assert.Equal(t, len(visibleIDs), page.Total)
		assert.Empty(t, page.Users)

		page = list(t, nil, 1, -5)
		assert.Equal(t, len(visibleIDs), page.Total)
		assert.Empty(t, page.Users)
	})

	t.Run("filtered list pages in Go", func(t *testing.T) {
		value := "jose@scim.example.test"
		page := list(t, &value, 2, 10)
		assert.Equal(t, 1, page.Total)
		assert.Empty(t, page.Users)
		page = list(t, &value, 1, 0)
		assert.Equal(t, 1, page.Total)
		assert.Empty(t, page.Users)
	})

	t.Run("externalId", func(t *testing.T) {
		if mysql {
			rows, err := repo.IdentitiesForUsers(scimUsersTestIssuer, []uint{padded.ID})
			require.NoError(t, err)
			if len(rows) == 0 {
				t.Logf("this engine does not return the padded issuer; candidate assertion skipped")
			} else {
				assert.Equal(t, scimUsersTestIssuer+" ", rows[0].Issuer, "PAD SPACE returns the padded issuer")
			}
		}
		page := list(t, nil, 1, 100)
		external := map[uint]string{}
		for _, r := range page.Users {
			external[r.User.ID] = r.ExternalID
		}
		assert.Equal(t, "sub-jose", external[jose.ID], "the identity under the configured issuer")
		assert.Equal(t, "", external[other.ID], "an identity under another issuer is ignored")
		assert.Equal(t, "", external[padded.ID], "an issuer with a trailing space is another issuer")
		assert.Equal(t, "sub-z-first", external[twice.ID], "the lowest id wins")
		assert.Equal(t, "", external[kate.ID], "no identity")

		got, err := svc.Get(ctx, fmt.Sprint(padded.ID))
		require.NoError(t, err)
		assert.Equal(t, "", got.ExternalID)

		noIssuer := NewSCIMUserService(repo, scimIssuerStub(""))
		page, err = noIssuer.List(ctx, nil, 1, 100)
		require.NoError(t, err)
		require.Len(t, page.Users, len(visibleIDs))
		for _, r := range page.Users {
			assert.Equal(t, "", r.ExternalID, "no issuer configured: user %d", r.User.ID)
		}
		got, err = noIssuer.Get(ctx, fmt.Sprint(jose.ID))
		require.NoError(t, err)
		assert.Equal(t, "", got.ExternalID)
	})

	t.Run("count is capped at 100", func(t *testing.T) {
		extra := make([]models.User, 100)
		for i := range extra {
			extra[i] = models.User{Email: fmt.Sprintf("bulk-%03d@scim.example.test", i), Password: "x", FirstName: "Bulk", LastName: "User", Role: models.RoleSupport, IsActive: true}
		}
		require.NoError(t, db.CreateInBatches(extra, 50).Error)
		page := list(t, nil, 1, 500)
		assert.Equal(t, len(visibleIDs)+100, page.Total)
		assert.Len(t, page.Users, 100)
		page = list(t, nil, 101, 100)
		assert.Len(t, page.Users, len(visibleIDs))
	})
}
