package service

// Issue #470: the SCIM 2.0 user create and replace. TestSCIMUserWrites{SQLite,
// MySQL} run one table of subtests through the real repositories, the
// governed transaction and the sign-in audit log, on a SQLite file opened by
// the production connector and on a schema built by the real migrations
// (TEST_MYSQL_DSN, also pointed at MariaDB 10.11). The issuer is the local
// test issuer of the OIDC suite and the roles are the seeded system roles
// plus one custom role, so the last subtest can finish a real OIDC sign-in on
// the account SCIM created. Every refusal asserts that the four tables a
// write touches are unchanged.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// Role values of the SCIM mapping.
const (
	scimRoleSales     = "CRM.Sales"
	scimRoleAdmin     = "CRM.Admin"
	scimRoleCustomer  = "CRM.Customer"
	scimRoleCustom    = "CRM.Custom"
	scimRoleGhost     = "CRM.Ghost"
	scimRoleGroupOnly = "CRM.GroupOnly"
	scimGhostRoleID   = 999999
)

// scimWritesSource is the effective single sign-on configuration the SCIM
// service reads: the test issuer and a mapping a subtest may change.
type scimWritesSource struct {
	mu  sync.Mutex
	cfg EffectiveSSOConfig
}

func (s *scimWritesSource) SSOConfig(context.Context) (EffectiveSSOConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, nil
}

type scimWritesFixture struct {
	*oidcFixture
	source   *scimWritesSource
	svc      SCIMUserService
	customID uint
	// newService builds the service over another configuration source.
	newService func(source SCIMIssuerSource) SCIMUserService
	// newServiceWith builds the service over the fixture's source with
	// another refresh token repository (nil leaves it out).
	newServiceWith func(refreshTokens repository.RefreshTokenRepository) SCIMUserService
}

// login signs the account in with its password and returns the access and
// refresh tokens.
func (f *scimWritesFixture) login(t *testing.T, email, password string) *AuthTokens {
	t.Helper()
	tokens, err := f.auth.LoginWithTokens(email, password)
	require.NoError(t, err)
	require.NotEmpty(t, tokens.AccessToken)
	require.NotEmpty(t, tokens.RefreshToken)
	return tokens
}

// liveRefreshTokens counts the refresh_tokens rows of userID that are not
// revoked.
func (f *scimWritesFixture) liveRefreshTokens(t *testing.T, userID uint) int64 {
	t.Helper()
	return f.count(t, "refresh_tokens", "user_id = ? AND is_revoked = ?", userID, false)
}

// scimCounts is the row count of users (erased rows included),
// user_identities, role_audit_log and auth_audit_log.
type scimCounts struct{ users, identities, roleAudits, authAudits int64 }

func (f *scimWritesFixture) counts(t *testing.T) scimCounts {
	t.Helper()
	return scimCounts{
		users:      f.count(t, "users", ""),
		identities: f.count(t, "user_identities", ""),
		roleAudits: f.count(t, "role_audit_log", ""),
		authAudits: f.count(t, "auth_audit_log", ""),
	}
}

func (f *scimWritesFixture) mapping() []SSORoleMapping {
	return []SSORoleMapping{
		{Claim: SSOClaimGroups, Value: scimRoleGroupOnly, RoleID: f.roleIDs[models.RoleSales]},
		{Claim: SSOClaimRoles, Value: scimRoleSales, RoleID: f.roleIDs[models.RoleSales]},
		{Claim: SSOClaimRoles, Value: oidcAppRole, RoleID: f.roleIDs[models.RoleSupport]},
		{Claim: SSOClaimRoles, Value: scimRoleAdmin, RoleID: f.roleIDs[models.RoleAdmin]},
		{Claim: SSOClaimRoles, Value: scimRoleCustomer, RoleID: f.roleIDs[models.RoleCustomer]},
		{Claim: SSOClaimRoles, Value: scimRoleCustom, RoleID: f.customID},
		{Claim: SSOClaimRoles, Value: scimRoleGhost, RoleID: scimGhostRoleID},
	}
}

func (f *scimWritesFixture) setIssuer(issuer string) {
	f.source.mu.Lock()
	f.source.cfg.IssuerURL = issuer
	f.source.mu.Unlock()
}

func (f *scimWritesFixture) reset(t *testing.T) {
	t.Helper()
	f.oidcFixture.reset(t)
	f.source.mu.Lock()
	f.source.cfg = EffectiveSSOConfig{IssuerURL: f.issuer.URL(), RoleMapping: f.mapping()}
	f.source.mu.Unlock()
}

// seedLinked seeds an account and links it under the fixture's issuer with
// source scim, so SCIM serves and changes it (#529).
func (f *scimWritesFixture) seedLinked(t *testing.T, email string, role models.UserRole, active bool, password string) *models.User {
	t.Helper()
	user := f.seedUser(t, email, role, active, password)
	f.linkSource(t, user.ID, "sub-"+email, models.UserIdentitySourceSCIM)
	return user
}

const scimLocalPassword = "Local-Passw0rd!-529"

// seedLocalAdmin seeds an active local admin (a password, no identity link)
// holding one live refresh token, next to a second active admin so the
// last-admin rule stays out of play (#529).
func (f *scimWritesFixture) seedLocalAdmin(t *testing.T) *models.User {
	t.Helper()
	f.seedUser(t, "keeper@x.test", models.RoleAdmin, true, "")
	local := f.seedUser(t, "local.admin@x.test", models.RoleAdmin, true, scimLocalPassword)
	f.login(t, local.Email, scimLocalPassword)
	require.Equal(t, int64(1), f.liveRefreshTokens(t, local.ID))
	return local
}

// seedOtherIssuer seeds an active account linked only under another issuer.
func (f *scimWritesFixture) seedOtherIssuer(t *testing.T, email string, role models.UserRole, password string) *models.User {
	t.Helper()
	user := f.seedUser(t, email, role, true, password)
	require.NoError(t, f.db.Create(&models.UserIdentity{UserID: user.ID, Issuer: "https://other.example.test",
		Subject: "sub-" + email, Source: models.UserIdentitySourceSCIM}).Error)
	return user
}

// assertRefused checks the refusal kind and that nothing was written.
func (f *scimWritesFixture) assertRefused(t *testing.T, want error, record *SCIMUserRecord, err error, before scimCounts) {
	t.Helper()
	assert.Nil(t, record)
	assert.ErrorIs(t, err, want)
	assert.Equal(t, before, f.counts(t), "a refusal writes nothing")
}

// roleAudits returns the role_audit_log rows of target, id ascending.
func (f *scimWritesFixture) roleAudits(t *testing.T, target uint) []models.RoleAuditEntry {
	t.Helper()
	var rows []models.RoleAuditEntry
	require.NoError(t, f.db.Where("target_user_id = ?", target).Order("id").Find(&rows).Error)
	return rows
}

// assertSuccessAudit checks the one auth_audit_log row of a successful write.
func (f *scimWritesFixture) assertSuccessAudit(t *testing.T, userID uint) {
	t.Helper()
	var rows []models.AuthAuditLog
	require.NoError(t, f.db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 1, "one sign-in audit row")
	assert.Equal(t, AuthAuditMethodSCIM, rows[0].Method)
	assert.Equal(t, AuthAuditOutcomeSuccess, rows[0].Outcome)
	assert.Equal(t, "", rows[0].Reason)
	if assert.NotNil(t, rows[0].UserID) {
		assert.Equal(t, userID, *rows[0].UserID)
	}
}

func scimInput(userName, externalID string, roles ...string) SCIMUserInput {
	return SCIMUserInput{UserName: userName, ExternalID: externalID, GivenName: "Given", FamilyName: "Family", Roles: roles}
}

func TestSCIMUserWritesSQLite(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	runSCIMUserWrites(t, newOIDCFixture(t, db, competitor), false)
}

func TestSCIMUserWritesMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runSCIMUserWrites(t, newOIDCFixture(t, db, db), true)
}

func runSCIMUserWrites(t *testing.T, base *oidcFixture, mysql bool) {
	ctx := context.Background()
	db := base.db
	custom := &models.Role{Name: "SCIM Custom"}
	require.NoError(t, repository.NewRoleRepository(db).Create(custom))
	f := &scimWritesFixture{oidcFixture: base, source: &scimWritesSource{}, customID: custom.ID}
	build := func(source SCIMIssuerSource, refreshTokens repository.RefreshTokenRepository) SCIMUserService {
		return NewSCIMUserService(repository.NewSCIMUserRepository(db), source,
			WithSCIMUserWrites(repository.NewUserRepository(db), repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db),
				repository.NewUserIdentityRepository(db), refreshTokens, repository.NewAPIKeyRepository(db), utils.NewTransactionManager(db), f.audit))
	}
	f.newService = func(source SCIMIssuerSource) SCIMUserService {
		return build(source, repository.NewRefreshTokenRepository(db))
	}
	f.newServiceWith = func(refreshTokens repository.RefreshTokenRepository) SCIMUserService {
		return build(f.source, refreshTokens)
	}
	f.svc = f.newService(f.source)

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"create", func(t *testing.T) {
			const sentPassword = "Sent-Passw0rd!-470"
			record, err := f.svc.Create(ctx, scimInput("new.hire@x.test", "sub-create", "unrelated", oidcAppRole, scimRoleSales))
			require.NoError(t, err)
			require.NotNil(t, record)
			assert.Equal(t, "sub-create", record.ExternalID)
			assert.Equal(t, "new.hire@x.test", record.User.Email)
			assert.True(t, record.User.IsActive)

			stored := f.user(t, record.User.ID)
			assert.Equal(t, models.RoleSales, stored.Role, "the first matching roles entry in stored order")
			assert.Nil(t, stored.RoleID, "a system role stores role_id NULL")
			assert.Equal(t, "Given", stored.FirstName)
			assert.Equal(t, "Family", stored.LastName)
			assert.True(t, stored.IsActive)
			assert.Error(t, bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte(sentPassword)), "no usable password")
			assert.Error(t, bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte("")), "no usable password")

			var identities []models.UserIdentity
			require.NoError(t, f.db.Find(&identities).Error)
			require.Len(t, identities, 1)
			assert.Equal(t, record.User.ID, identities[0].UserID)
			assert.Equal(t, f.issuer.URL(), identities[0].Issuer)
			assert.Equal(t, "sub-create", identities[0].Subject)
			var source string
			require.NoError(t, f.db.Raw("SELECT source FROM user_identities WHERE id = ?", identities[0].ID).Scan(&source).Error)
			assert.Equal(t, models.UserIdentitySourceSCIM, source, "SCIM create writes source scim (#505)")

			audits := f.roleAudits(t, record.User.ID)
			require.Len(t, audits, 1)
			assert.Equal(t, models.RoleAuditUserRoleChanged, audits[0].Action)
			assert.Equal(t, AuthMethodSCIM, audits[0].AuthMethod)
			assert.Nil(t, audits[0].ActorUserID, "no user acted")
			assert.Nil(t, audits[0].BeforeState)
			require.NotNil(t, audits[0].AfterState)
			assert.JSONEq(t, `{"role":"sales","role_id":null}`, *audits[0].AfterState)
			assert.Equal(t, int64(1), f.count(t, "role_audit_log", ""))
			f.assertSuccessAudit(t, record.User.ID)
			assert.Equal(t, scimCounts{1, 1, 1, 1}, f.counts(t))

			// A custom role: role custom with its id, and inactive on request.
			inactive := false
			in := scimInput("custom.hire@x.test", "sub-custom", scimRoleCustom)
			in.Active = &inactive
			record, err = f.svc.Create(ctx, in)
			require.NoError(t, err)
			assert.False(t, record.User.IsActive)
			stored = f.user(t, record.User.ID)
			assert.Equal(t, models.RoleCustom, stored.Role)
			if assert.NotNil(t, stored.RoleID) {
				assert.Equal(t, f.customID, *stored.RoleID)
			}
			assert.False(t, stored.IsActive, "active false is stored")
			audits = f.roleAudits(t, record.User.ID)
			require.Len(t, audits, 1)
			assert.Nil(t, audits[0].ActorUserID)
			if assert.NotNil(t, audits[0].RoleID) {
				assert.Equal(t, f.customID, *audits[0].RoleID)
			}
			require.NotNil(t, audits[0].AfterState)
			assert.JSONEq(t, `{"role":"custom","role_id":`+uintString(f.customID)+`}`, *audits[0].AfterState)
		}},
		{"duplicate email", func(t *testing.T) {
			f.seedUser(t, "jose@x.test", models.RoleSales, true, "")
			before := f.counts(t)
			record, err := f.svc.Create(ctx, scimInput("JOSE@X.TEST", "sub-jose", scimRoleSales))
			f.assertRefused(t, ErrSCIMUniqueness, record, err, before)

			// An erased account still holds its address.
			erased := f.seedUser(t, "erased@x.test", models.RoleSales, true, "")
			require.NoError(t, f.db.Delete(&models.User{}, erased.ID).Error)
			before = f.counts(t)
			record, err = f.svc.Create(ctx, scimInput("erased@x.test", "sub-erased", scimRoleSales))
			f.assertRefused(t, ErrSCIMUniqueness, record, err, before)
		}},
		{"identity taken", func(t *testing.T) {
			owner := f.seedUser(t, "owner@x.test", models.RoleSales, true, "")
			f.link(t, owner.ID, "sub-taken")
			before := f.counts(t)
			record, err := f.svc.Create(ctx, scimInput("other@x.test", "sub-taken", scimRoleSales))
			f.assertRefused(t, ErrSCIMUniqueness, record, err, before)
		}},
		{"missing externalId", func(t *testing.T) {
			before := f.counts(t)
			for _, externalID := range []string{"", "sub ", " sub", "sub\x01"} {
				record, err := f.svc.Create(ctx, scimInput("valid@x.test", externalID, scimRoleSales))
				f.assertRefused(t, ErrSCIMInvalidValue, record, err, before)
			}
		}},
		{"invalid profile", func(t *testing.T) {
			before := f.counts(t)
			long := make([]rune, 101)
			for i := range long {
				long[i] = 'é'
			}
			for name, in := range map[string]SCIMUserInput{
				"empty userName":    scimInput("", "sub-p", scimRoleSales),
				"userName too long": scimInput(string(make([]byte, 256)), "sub-p", scimRoleSales),
				"not an address":    scimInput("not-an-address", "sub-p", scimRoleSales),
				"given name long":   {UserName: "p@x.test", ExternalID: "sub-p", GivenName: string(long), Roles: []string{scimRoleSales}},
				"family name long":  {UserName: "p@x.test", ExternalID: "sub-p", FamilyName: string(long), Roles: []string{scimRoleSales}},
			} {
				record, err := f.svc.Create(ctx, in)
				assert.ErrorIs(t, err, ErrSCIMInvalidValue, name)
				assert.Nil(t, record, name)
			}
			f.setIssuer("")
			record, err := f.svc.Create(ctx, scimInput("p@x.test", "sub-p", scimRoleSales))
			assert.ErrorIs(t, err, ErrSCIMInvalidValue, "no issuer")
			assert.Nil(t, record)
			f.setIssuer("https://idp.example.test/" + string(make([]byte, 600)))
			_, err = f.svc.Create(ctx, scimInput("p@x.test", "sub-p", scimRoleSales))
			assert.ErrorIs(t, err, ErrSCIMInvalidValue, "issuer too long")
			assert.Equal(t, before, f.counts(t))
		}},
		{"unmapped role", func(t *testing.T) {
			before := f.counts(t)
			for name, roles := range map[string][]string{
				"absent":           nil,
				"empty":            {},
				"groups only":      {scimRoleGroupOnly},
				"case variant":     {"crm.sales"},
				"customer":         {scimRoleCustomer},
				"no such role row": {scimRoleGhost},
			} {
				record, err := f.svc.Create(ctx, scimInput("unmapped@x.test", "sub-unmapped", roles...))
				assert.ErrorIs(t, err, ErrSCIMInvalidValue, name)
				assert.Nil(t, record, name)
				assert.Equal(t, before, f.counts(t), name)
			}
		}},
		{"replace changes role", func(t *testing.T) {
			sam := f.seedUser(t, "sam@x.test", models.RoleSales, true, "")
			f.link(t, sam.ID, "sub-sam")
			record, err := f.svc.Replace(ctx, uintString(sam.ID), SCIMUserInput{UserName: "samantha@x.test", ExternalID: "ignored",
				GivenName: "Samantha", FamilyName: "Smith", Roles: []string{oidcAppRole}})
			require.NoError(t, err)
			assert.Equal(t, "sub-sam", record.ExternalID, "the link set at creation")
			assert.Equal(t, "samantha@x.test", record.User.Email)
			stored := f.user(t, sam.ID)
			assert.Equal(t, "samantha@x.test", stored.Email)
			assert.Equal(t, "Samantha", stored.FirstName)
			assert.Equal(t, "Smith", stored.LastName)
			assert.Equal(t, models.RoleSupport, stored.Role)
			assert.True(t, stored.IsActive, "absent active leaves is_active")

			audits := f.roleAudits(t, sam.ID)
			require.Len(t, audits, 1)
			assert.Equal(t, models.RoleAuditUserRoleChanged, audits[0].Action)
			assert.Equal(t, AuthMethodSCIM, audits[0].AuthMethod)
			assert.Nil(t, audits[0].ActorUserID)
			require.NotNil(t, audits[0].BeforeState)
			require.NotNil(t, audits[0].AfterState)
			assert.JSONEq(t, `{"role":"sales","role_id":null}`, *audits[0].BeforeState)
			assert.JSONEq(t, `{"role":"support","role_id":null}`, *audits[0].AfterState)
			f.assertSuccessAudit(t, sam.ID)
			assert.Equal(t, int64(1), f.identities(t), "no identity is written")
		}},
		{"replace keeps role", func(t *testing.T) {
			kim := f.seedLinked(t, "kim@x.test", models.RoleSupport, true, "")
			record, err := f.svc.Replace(ctx, uintString(kim.ID), SCIMUserInput{UserName: "kim@x.test", Roles: []string{oidcAppRole}})
			require.NoError(t, err)
			assert.Equal(t, "sub-kim@x.test", record.ExternalID, "the link under the issuer")
			stored := f.user(t, kim.ID)
			assert.Equal(t, models.RoleSupport, stored.Role)
			assert.Equal(t, "", stored.FirstName, "PUT replaces: an absent name is cleared")
			assert.Equal(t, "", stored.LastName)
			assert.True(t, stored.IsActive)
			assert.Empty(t, f.roleAudits(t, kim.ID), "an unchanged role writes no role audit row")
			f.assertSuccessAudit(t, kim.ID)
		}},
		{"replace deactivates", func(t *testing.T) {
			const password = "Dan-Passw0rd!-471"
			dan := f.seedLinked(t, "dan@x.test", models.RoleSales, true, password)
			first, second := f.login(t, "dan@x.test", password), f.login(t, "dan@x.test", password)
			require.Equal(t, int64(2), f.liveRefreshTokens(t, dan.ID))
			version := f.user(t, dan.ID).TokenVersion
			require.Zero(t, f.count(t, "auth_audit_log", ""), "the fixture's password sign-in writes no audit row")

			inactive := false
			record, err := f.svc.Replace(ctx, uintString(dan.ID), SCIMUserInput{UserName: "dan@x.test", Active: &inactive, Roles: []string{scimRoleSales}})
			require.NoError(t, err)
			assert.False(t, record.User.IsActive)
			stored := f.user(t, dan.ID)
			assert.False(t, stored.IsActive)
			assert.Equal(t, version+1, stored.TokenVersion, "the deactivation ends every access token (#471)")
			assert.Zero(t, f.liveRefreshTokens(t, dan.ID), "the deactivation revokes every refresh token (#471)")
			assert.Equal(t, int64(2), f.refreshTokens(t, dan.ID), "revoked, not deleted")
			assert.Empty(t, f.roleAudits(t, dan.ID))
			f.assertSuccessAudit(t, dan.ID)

			active := true
			_, err = f.svc.Replace(ctx, uintString(dan.ID), SCIMUserInput{UserName: "dan@x.test", Active: &active, Roles: []string{scimRoleSales}})
			require.NoError(t, err)
			stored = f.user(t, dan.ID)
			assert.True(t, stored.IsActive, "active true reactivates")
			assert.Equal(t, version+1, stored.TokenVersion, "a reactivation does not change the generation")
			_, err = f.auth.ValidateToken(first.AccessToken)
			assert.Error(t, err, "the old access token stays refused")
			_, err = f.auth.RefreshAccessToken(second.RefreshToken)
			assert.Error(t, err, "the old refresh token stays refused")
		}},
		{"replace not visible", func(t *testing.T) {
			customer := f.seedLinked(t, "cust@x.test", models.RoleCustomer, true, "")
			erased := f.seedLinked(t, "gone@x.test", models.RoleSales, true, "")
			require.NoError(t, repository.NewUserRepository(f.db).Delete(erased.ID), "erasure through the real path")
			before := f.counts(t)
			for _, id := range []string{"999999", uintString(erased.ID), uintString(customer.ID), "abc", "0", "-1"} {
				record, err := f.svc.Replace(ctx, id, SCIMUserInput{UserName: "x@x.test", Roles: []string{scimRoleSales}})
				f.assertRefused(t, apperrors.ErrNotFound, record, err, before)
			}
			assert.Equal(t, models.RoleCustomer, f.user(t, customer.ID).Role)
		}},
		{"replace of a local account is not found", func(t *testing.T) {
			local := f.seedLocalAdmin(t)
			before := f.patchState(t, local.ID)
			inactive := false
			for name, in := range map[string]SCIMUserInput{
				"mapped role and new userName": {UserName: "renamed@x.test", GivenName: "New", FamilyName: "Name", Roles: []string{scimRoleSales}},
				"active false":                 {UserName: local.Email, Active: &inactive, Roles: []string{scimRoleAdmin}},
			} {
				record, err := f.svc.Replace(ctx, uintString(local.ID), in)
				assert.Nil(t, record, name)
				assert.ErrorIs(t, err, apperrors.ErrNotFound, name)
				assert.Equal(t, before, f.patchState(t, local.ID), "%s: the local account is unchanged", name)
			}
		}},
		{"create with a local account's address", func(t *testing.T) {
			local := f.seedLocalAdmin(t)
			before := f.patchState(t, local.ID)
			record, err := f.svc.Create(ctx, scimInput(local.Email, "sub-local", scimRoleSales))
			assert.Nil(t, record)
			assert.ErrorIs(t, err, ErrSCIMUniqueness)
			assert.Equal(t, before, f.patchState(t, local.ID), "the local account is unchanged")
		}},
		{"replace email taken", func(t *testing.T) {
			ann := f.seedUser(t, "ann@x.test", models.RoleSales, true, "")
			bob := f.seedLinked(t, "bob@x.test", models.RoleSales, true, "")
			erased := f.seedUser(t, "old@x.test", models.RoleSales, true, "")
			require.NoError(t, f.db.Delete(&models.User{}, erased.ID).Error)
			before := f.counts(t)
			for _, email := range []string{"ann@x.test", "ANN@x.test", "old@x.test"} {
				record, err := f.svc.Replace(ctx, uintString(bob.ID), SCIMUserInput{UserName: email, Roles: []string{scimRoleSales}})
				f.assertRefused(t, ErrSCIMUniqueness, record, err, before)
			}
			record, err := f.svc.Replace(ctx, uintString(bob.ID), SCIMUserInput{UserName: "not-an-address", Roles: []string{scimRoleSales}})
			f.assertRefused(t, ErrSCIMInvalidValue, record, err, before)
			assert.Equal(t, "bob@x.test", f.user(t, bob.ID).Email)
			assert.Equal(t, "ann@x.test", f.user(t, ann.ID).Email)

			// A change of letter case of the account's own address is no
			// conflict.
			_, err = f.svc.Replace(ctx, uintString(bob.ID), SCIMUserInput{UserName: "Bob@x.test", Roles: []string{scimRoleSales}})
			require.NoError(t, err)
			assert.Equal(t, "Bob@x.test", f.user(t, bob.ID).Email)
		}},
		{"last admin", func(t *testing.T) {
			admin := f.seedLinked(t, "admin@x.test", models.RoleAdmin, true, "")
			before := f.counts(t)
			record, err := f.svc.Replace(ctx, uintString(admin.ID), SCIMUserInput{UserName: "admin@x.test", Roles: []string{scimRoleSales}})
			f.assertRefused(t, ErrSCIMLastAdmin, record, err, before)
			assert.ErrorIs(t, err, ErrSCIMInvalidValue)
			inactive := false
			record, err = f.svc.Replace(ctx, uintString(admin.ID), SCIMUserInput{UserName: "admin@x.test", Active: &inactive, Roles: []string{scimRoleAdmin}})
			f.assertRefused(t, ErrSCIMLastAdmin, record, err, before)
			assert.ErrorIs(t, err, ErrSCIMInvalidValue)
			stored := f.user(t, admin.ID)
			assert.Equal(t, models.RoleAdmin, stored.Role)
			assert.True(t, stored.IsActive)

			// With a second active admin the same change goes through.
			f.seedUser(t, "admin2@x.test", models.RoleAdmin, true, "")
			_, err = f.svc.Replace(ctx, uintString(admin.ID), SCIMUserInput{UserName: "admin@x.test", Roles: []string{scimRoleSales}})
			require.NoError(t, err)
			assert.Equal(t, models.RoleSales, f.user(t, admin.ID).Role)
		}},
		{"sign-in reuses account", func(t *testing.T) {
			// The SCIM service reads the same settings the sign-in does.
			svc := f.newService(f.sso)
			record, err := svc.Create(ctx, scimInput("reuse@x.test", "sub-reuse", oidcAppRole))
			require.NoError(t, err)
			assert.Equal(t, models.RoleSupport, f.user(t, record.User.ID).Role)
			users, identities := f.users(t), f.identities(t)

			tokens, err := f.signIn(t, oidcTokenSpec{sub: "sub-reuse", email: "reuse@x.test", roles: []string{oidcAppRole}})
			require.NoError(t, err)
			require.NotNil(t, tokens)
			require.NotNil(t, tokens.User)
			assert.Equal(t, record.User.ID, tokens.User.ID, "the sign-in uses the account SCIM created")
			assert.Equal(t, users, f.users(t), "no user is created")
			assert.Equal(t, identities, f.identities(t), "no identity is created")
		}},
	}
	if mysql {
		// #513: a handover X -> Y that commits while a replace of Y with
		// roles mapped to sales waits for the admin set. The write's admin
		// set comes back empty; the locking read of Y must still count Y as
		// the admin before it.
		cases = append(cases, struct {
			name string
			run  func(t *testing.T)
		}{"last_admin_race/after_handover", func(t *testing.T) {
			x := f.seedUser(t, "x@x.test", models.RoleAdmin, true, "")
			y := f.seedLinked(t, "y@x.test", models.RoleSales, true, "")

			handover := holdAdminHandover(t, f.db, f.roleIDs, x.ID, y.ID)
			done := make(chan error, 1)
			go func() {
				_, err := f.svc.Replace(ctx, uintString(y.ID), SCIMUserInput{UserName: "y@x.test", Roles: []string{scimRoleSales}})
				done <- err
			}()
			err := awaitLockWaitThenCommit(t, f.db, handover, done)

			require.ErrorIs(t, err, ErrSCIMLastAdmin)
			assert.Equal(t, int64(1), countActiveAdmins(t, f.db), "exactly one active admin remains")
			stored := f.user(t, y.ID)
			assert.Equal(t, models.RoleAdmin, stored.Role, "Y keeps role admin")
			assert.True(t, stored.IsActive)
			assert.Zero(t, f.count(t, "role_audit_log", ""), "no role audit row")
			assert.Zero(t, f.count(t, "auth_audit_log", ""), "no sign-in audit row")
		}})
	}
	if mysql {
		// The utf8mb4_unicode_ci collation equates padded and accented
		// variants with the stored address.
		cases = append(cases, struct {
			name string
			run  func(t *testing.T)
		}{"duplicate email variants", func(t *testing.T) {
			f.seedUser(t, "jose@x.test", models.RoleSales, true, "")
			before := f.counts(t)
			for _, email := range []string{"JOSE@X.TEST", "jose@x.test ", "josé@x.test"} {
				record, err := f.svc.Create(ctx, scimInput(email, "sub-variant", scimRoleSales))
				f.assertRefused(t, ErrSCIMUniqueness, record, err, before)
			}
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.reset(t)
			tc.run(t)
		})
	}

	t.Run("not wired", func(t *testing.T) {
		svc := NewSCIMUserService(repository.NewSCIMUserRepository(db), f.source)
		_, err := svc.Create(ctx, scimInput("w@x.test", "sub-w", scimRoleSales))
		assert.True(t, errors.Is(err, errSCIMWritesNotWired))
		_, err = svc.Replace(ctx, "1", scimInput("w@x.test", "sub-w", scimRoleSales))
		assert.True(t, errors.Is(err, errSCIMWritesNotWired))
	})
}

func uintString(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
