package service

// Issue #506: the SCIM 2.0 Groups read and members patch. TestSCIMGroups
// {SQLite,MySQL} run one table of subtests through the real Group PatchOp
// parser, repositories, the governed transaction and the sign-in audit log,
// on a SQLite file opened by the production connector and on a schema built
// by the real migrations (TEST_MYSQL_DSN, also pointed at MariaDB 10.11; the
// MySQL root skips without it). The file name starts with auth so that
// scripts/ci/check-auth.py discovers both roots. Every refusal asserts that
// scim_group_members, the users' role columns, role_audit_log and
// auth_audit_log are unchanged. Issue #529: the seeded accounts are linked
// under the test issuer, so SCIM manages them; a local account (no identity
// link) is never listed, refused as a member an add or a replace names, and
// only loses a leftover membership row on a remove, keeping its role. Issue
// #530: an active managed account a patch leaves in no mapped group is
// deactivated and every credential it holds ends; a role that lacks a
// permission the old one had ends its sessions. Every refusal also asserts
// that is_active, token_version, refresh tokens and API keys are unchanged.

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Group values of the test mapping.
const (
	scimGroupAdmins   = "Admins"
	scimGroupSales    = "Sales"
	scimGroupCustomer = "Customers"
	scimGroupRoleOnly = "X"
	// scimGroupsIssuer is the configured single sign-on issuer.
	scimGroupsIssuer = "https://idp.example.test"
)

type scimGroupsFixture struct {
	db      *gorm.DB
	source  *scimWritesSource
	svc     SCIMGroupService
	roleIDs map[models.UserRole]uint
}

func newSCIMGroupsFixture(t *testing.T, db *gorm.DB) *scimGroupsFixture {
	t.Helper()
	f := &scimGroupsFixture{db: db, source: &scimWritesSource{}, roleIDs: map[models.UserRole]uint{}}
	var roles []models.Role
	require.NoError(t, db.Where("system_key IS NOT NULL").Find(&roles).Error)
	for _, role := range roles {
		f.roleIDs[models.UserRole(*role.SystemKey)] = role.ID
	}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		require.NotZero(t, f.roleIDs[role], "the system role %s is seeded", role)
	}
	f.svc = NewSCIMGroupService(repository.NewSCIMGroupMemberRepository(db), f.source,
		WithSCIMGroupWrites(repository.NewUserRepository(db), repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db),
			repository.NewRefreshTokenRepository(db), repository.NewAPIKeyRepository(db),
			utils.NewTransactionManager(db), NewAuthAuditService(repository.NewAuthAuditRepository(db))))
	return f
}

// mapping lists Admins before Sales, so a user in both is admin, and one
// roles entry that is not a group.
func (f *scimGroupsFixture) mapping() []SSORoleMapping {
	return []SSORoleMapping{
		{Claim: SSOClaimGroups, Value: scimGroupAdmins, RoleID: f.roleIDs[models.RoleAdmin]},
		{Claim: SSOClaimGroups, Value: scimGroupSales, RoleID: f.roleIDs[models.RoleSales]},
		{Claim: SSOClaimRoles, Value: scimGroupRoleOnly, RoleID: f.roleIDs[models.RoleSales]},
		{Claim: SSOClaimGroups, Value: scimGroupCustomer, RoleID: f.roleIDs[models.RoleCustomer]},
	}
}

func (f *scimGroupsFixture) setMapping(mapping []SSORoleMapping) {
	f.source.mu.Lock()
	f.source.cfg = EffectiveSSOConfig{IssuerURL: scimGroupsIssuer, RoleMapping: mapping}
	f.source.mu.Unlock()
}

func (f *scimGroupsFixture) reset(t *testing.T) {
	t.Helper()
	for _, table := range []string{"scim_group_members", "role_audit_log", "auth_audit_log", "refresh_tokens", "api_keys", "user_identities", "users"} {
		require.NoError(t, f.db.Exec("DELETE FROM "+table).Error, table)
	}
	f.setMapping(f.mapping())
}

// seed creates a user with role and active, links it under the issuer, and
// puts it in groups.
func (f *scimGroupsFixture) seed(t *testing.T, email string, role models.UserRole, active bool, groups ...string) *models.User {
	t.Helper()
	user := f.seedLocal(t, email, role, active, groups...)
	require.NoError(t, f.db.Create(&models.UserIdentity{UserID: user.ID, Issuer: scimGroupsIssuer, Subject: "sub-" + email,
		Source: models.UserIdentitySourceSCIM}).Error)
	return user
}

// seedLocal creates a local account (a password, no identity link) with role
// and active, and inserts its membership rows in groups directly.
func (f *scimGroupsFixture) seedLocal(t *testing.T, email string, role models.UserRole, active bool, groups ...string) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "x", FirstName: "Seed", LastName: "User", Role: role, IsActive: true}
	require.NoError(t, f.db.Create(user).Error)
	if !active {
		require.NoError(t, f.db.Model(&models.User{}).Where("id = ?", user.ID).Update("is_active", false).Error)
	}
	for _, group := range groups {
		require.NoError(t, f.db.Create(&models.SCIMGroupMember{GroupValue: group, UserID: user.ID}).Error)
	}
	return user
}

// seedCredentials gives user one live refresh token and one live API key.
func (f *scimGroupsFixture) seedCredentials(t *testing.T, user *models.User) {
	t.Helper()
	tag := uintString(user.ID)
	require.NoError(t, f.db.Create(&models.RefreshToken{UserID: user.ID, TokenHash: "scim-groups-rt-" + tag,
		ExpiresAt: time.Now().Add(time.Hour)}).Error)
	require.NoError(t, f.db.Create(&models.APIKey{Name: "scim groups", KeyHash: "scim-groups-kh-" + tag, Prefix: "gcrm_sg",
		UserID: user.ID, IsActive: true}).Error)
}

// assertSessionsEnded checks that every refresh token of user is revoked.
func (f *scimGroupsFixture) assertSessionsEnded(t *testing.T, userID uint) {
	t.Helper()
	var tokens []models.RefreshToken
	require.NoError(t, f.db.Where("user_id = ?", userID).Find(&tokens).Error)
	require.NotEmpty(t, tokens)
	for _, token := range tokens {
		assert.True(t, token.Revoked, "refresh token %d is revoked", token.ID)
	}
}

// assertKeys checks that every API key of user is live (revoked false) or
// revoked with revoked_at set.
func (f *scimGroupsFixture) assertKeys(t *testing.T, userID uint, revoked bool) {
	t.Helper()
	var keys []models.APIKey
	require.NoError(t, f.db.Where("user_id = ?", userID).Find(&keys).Error)
	require.NotEmpty(t, keys)
	for _, key := range keys {
		assert.Equal(t, !revoked, key.IsActive, "API key %d is_active", key.ID)
		if revoked {
			assert.NotNil(t, key.RevokedAt, "API key %d carries revoked_at", key.ID)
		} else {
			assert.Nil(t, key.RevokedAt, "API key %d has no revoked_at", key.ID)
		}
	}
}

func (f *scimGroupsFixture) user(t *testing.T, id uint) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.Unscoped().First(&user, id).Error)
	return user
}

func (f *scimGroupsFixture) count(t *testing.T, table, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	query := "SELECT COUNT(*) FROM " + table
	if where != "" {
		query += " WHERE " + where
	}
	require.NoError(t, f.db.Raw(query, args...).Scan(&n).Error)
	return n
}

// members returns every scim_group_members pair as "value/user", sorted.
func (f *scimGroupsFixture) members(t *testing.T) []string {
	t.Helper()
	var rows []models.SCIMGroupMember
	require.NoError(t, f.db.Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.GroupValue+"/"+uintString(row.UserID))
	}
	sort.Strings(out)
	return out
}

func (f *scimGroupsFixture) roleAudits(t *testing.T, target uint) []models.RoleAuditEntry {
	t.Helper()
	var rows []models.RoleAuditEntry
	require.NoError(t, f.db.Where("target_user_id = ?", target).Order("id").Find(&rows).Error)
	return rows
}

func (f *scimGroupsFixture) scimAudits(t *testing.T, userID uint) int64 {
	t.Helper()
	return f.count(t, "auth_audit_log", "method = ? AND outcome = ? AND user_id = ?", AuthAuditMethodSCIM, AuthAuditOutcomeSuccess, userID)
}

func (f *scimGroupsFixture) activeAdmins(t *testing.T) int64 {
	t.Helper()
	return f.count(t, "users", "role = ? AND is_active = ? AND deleted_at IS NULL", models.RoleAdmin, true)
}

// scimGroupsState is everything a refused patch must leave as it was.
type scimGroupsState struct {
	members    []string
	roles      map[uint]string
	accounts   map[uint]string
	roleAudits int64
	authAudits int64
	// revokedRefresh and revokedKeys count the revoked refresh tokens and
	// API keys.
	revokedRefresh int64
	revokedKeys    int64
}

func (f *scimGroupsFixture) state(t *testing.T) scimGroupsState {
	t.Helper()
	var users []models.User
	require.NoError(t, f.db.Unscoped().Find(&users).Error)
	roles := map[uint]string{}
	accounts := map[uint]string{}
	for _, u := range users {
		roleID := "nil"
		if u.RoleID != nil {
			roleID = uintString(*u.RoleID)
		}
		roles[u.ID] = string(u.Role) + "/" + roleID
		active := "inactive"
		if u.IsActive {
			active = "active"
		}
		accounts[u.ID] = active + "/" + uintString(u.TokenVersion)
	}
	return scimGroupsState{members: f.members(t), roles: roles, accounts: accounts,
		roleAudits: f.count(t, "role_audit_log", ""), authAudits: f.count(t, "auth_audit_log", ""),
		revokedRefresh: f.count(t, "refresh_tokens", "is_revoked = ?", true),
		revokedKeys:    f.count(t, "api_keys", "is_active = ? OR revoked_at IS NOT NULL", false)}
}

// assertRefused checks the refusal kind and that nothing was written.
func (f *scimGroupsFixture) assertRefused(t *testing.T, want error, record *SCIMGroupRecord, err error, before scimGroupsState) {
	t.Helper()
	assert.Nil(t, record)
	assert.ErrorIs(t, err, want)
	assert.Equal(t, before, f.state(t), "a refusal writes nothing")
}

func parseGroupPatch(t *testing.T, body string) scim.GroupPatch {
	t.Helper()
	p, err := scim.ParseGroupPatch([]byte(body))
	require.NoError(t, err, body)
	return p
}

func groupAdd(ids ...uint) string {
	return `{"Operations":[{"op":"add","path":"members","value":[` + memberValues(ids) + `]}]}`
}

func groupReplace(ids ...uint) string {
	return `{"Operations":[{"op":"replace","path":"members","value":[` + memberValues(ids) + `]}]}`
}

func groupRemove(id uint) string {
	return `{"Operations":[{"op":"remove","path":"members[value eq \"` + uintString(id) + `\"]"}]}`
}

func memberValues(ids []uint) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += `{"value":"` + uintString(id) + `"}`
	}
	return out
}

func TestSCIMGroupsSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runSCIMGroups(t, newSCIMGroupsFixture(t, db))
}

func TestSCIMGroupsMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 4)
	runSCIMGroups(t, newSCIMGroupsFixture(t, db))
}

func runSCIMGroups(t *testing.T, f *scimGroupsFixture) {
	ctx := context.Background()
	adminsID, salesID := scim.GroupID(scimGroupAdmins), scim.GroupID(scimGroupSales)

	// lastAdminPair seeds X and Y, the only two active admins, both in
	// Admins and Sales.
	lastAdminPair := func(t *testing.T) (*models.User, *models.User) {
		x := f.seed(t, "x@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
		y := f.seed(t, "y@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
		return x, y
	}
	// assertRaceOutcome checks that exactly the winner's patch committed.
	assertRaceOutcome := func(t *testing.T, winner, loser *models.User) {
		t.Helper()
		assert.Equal(t, models.RoleSales, f.user(t, winner.ID).Role, "the winner's removed admin is recomputed")
		assert.Equal(t, models.RoleAdmin, f.user(t, loser.ID).Role, "the loser's admin is unchanged")
		assert.Equal(t, int64(1), f.activeAdmins(t), "exactly one active admin remains")
		assert.Len(t, f.roleAudits(t, winner.ID), 1, "the winner's audit row")
		assert.Empty(t, f.roleAudits(t, loser.ID), "no audit row of the loser")
		assert.Equal(t, int64(1), f.count(t, "role_audit_log", ""))
		assert.Equal(t, int64(1), f.scimAudits(t, winner.ID))
		assert.Zero(t, f.scimAudits(t, loser.ID))
		want := []string{"Admins/" + uintString(loser.ID), "Sales/" + uintString(loser.ID), "Sales/" + uintString(winner.ID)}
		sort.Strings(want)
		assert.Equal(t, want, f.members(t))
	}

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"list", func(t *testing.T) {
			// The mapping order of AC-1: listed by bytes, roles entries left out.
			f.setMapping([]SSORoleMapping{
				{Claim: SSOClaimGroups, Value: scimGroupSales, RoleID: f.roleIDs[models.RoleSales]},
				{Claim: SSOClaimGroups, Value: scimGroupAdmins, RoleID: f.roleIDs[models.RoleAdmin]},
				{Claim: SSOClaimRoles, Value: scimGroupRoleOnly, RoleID: f.roleIDs[models.RoleSales]},
				{Claim: SSOClaimGroups, Value: scimGroupSales, RoleID: f.roleIDs[models.RoleSupport]},
			})
			ann := f.seed(t, "ann@x.test", models.RoleSales, true, scimGroupSales)
			bob := f.seed(t, "bob@x.test", models.RoleSupport, false, scimGroupSales)
			customer := f.seed(t, "customer@x.test", models.RoleCustomer, true, scimGroupSales)
			erased := f.seed(t, "erased@x.test", models.RoleSales, true, scimGroupSales, scimGroupAdmins)
			require.NoError(t, f.db.Delete(&models.User{}, erased.ID).Error)

			page, err := f.svc.List(ctx, nil, 1, scim.MaxResults)
			require.NoError(t, err)
			assert.Equal(t, 2, page.Total)
			assert.Equal(t, []SCIMGroupRecord{
				{Value: scimGroupAdmins, MemberIDs: []uint{}},
				{Value: scimGroupSales, MemberIDs: []uint{ann.ID, bob.ID}},
			}, page.Groups, "only visible members, id ascending; never the customer or the erased user")
			_ = customer

			page, err = f.svc.List(ctx, nil, 2, 1)
			require.NoError(t, err)
			assert.Equal(t, 2, page.Total)
			assert.Equal(t, 2, page.StartIndex)
			require.Len(t, page.Groups, 1)
			assert.Equal(t, scimGroupSales, page.Groups[0].Value)
			page, err = f.svc.List(ctx, nil, 3, 10)
			require.NoError(t, err)
			assert.Empty(t, page.Groups)
			assert.NotNil(t, page.Groups)

			for value, want := range map[string][]string{"Sales": {scimGroupSales}, "sales": {}, "Sales ": {}, "X": {}} {
				value := value
				page, err = f.svc.List(ctx, &value, 1, scim.MaxResults)
				require.NoError(t, err)
				got := []string{}
				for _, g := range page.Groups {
					got = append(got, g.Value)
				}
				assert.Equal(t, want, got, "filter %q matches byte for byte", value)
			}

			record, err := f.svc.Get(ctx, salesID)
			require.NoError(t, err)
			assert.Equal(t, []uint{ann.ID, bob.ID}, record.MemberIDs)
			for _, id := range []string{"", "nope!", scim.GroupID("sales"), scim.GroupID(scimGroupRoleOnly), adminsID + "="} {
				_, err := f.svc.Get(ctx, id)
				assert.ErrorIs(t, err, apperrors.ErrNotFound, id)
			}

			// The mapping drops Sales: it is no longer served or patched.
			f.setMapping([]SSORoleMapping{{Claim: SSOClaimGroups, Value: scimGroupAdmins, RoleID: f.roleIDs[models.RoleAdmin]}})
			page, err = f.svc.List(ctx, nil, 1, scim.MaxResults)
			require.NoError(t, err)
			require.Len(t, page.Groups, 1)
			assert.Equal(t, scimGroupAdmins, page.Groups[0].Value)
			_, err = f.svc.Get(ctx, salesID)
			assert.ErrorIs(t, err, apperrors.ErrNotFound)
			before := f.state(t)
			record, err = f.svc.Patch(ctx, salesID, parseGroupPatch(t, groupRemove(ann.ID)))
			f.assertRefused(t, apperrors.ErrNotFound, record, err, before)
			assert.NotContains(t, err.Error(), scimGroupSales, "no error names the group")
		}},
		{"add makes admin", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			u := f.seed(t, "u@x.test", models.RoleSales, true)
			f.seedCredentials(t, u)
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupAdd(u.ID)))
			require.NoError(t, err)
			assert.Equal(t, scimGroupAdmins, record.Value)
			assert.Equal(t, u.TokenVersion, f.user(t, u.ID).TokenVersion, "a raised role ends no session")
			assert.Zero(t, f.count(t, "refresh_tokens", "user_id = ? AND is_revoked = ?", u.ID, true), "the refresh token stays live")
			f.assertKeys(t, u.ID, false)
			assert.Equal(t, []uint{u.ID}, record.MemberIDs)
			stored := f.user(t, u.ID)
			assert.Equal(t, models.RoleAdmin, stored.Role)
			assert.Nil(t, stored.RoleID)
			audits := f.roleAudits(t, u.ID)
			require.Len(t, audits, 1)
			assert.Equal(t, AuthMethodSCIM, audits[0].AuthMethod)
			assert.Nil(t, audits[0].ActorUserID, "a SCIM write has no actor")
			assert.Equal(t, models.RoleAuditUserRoleChanged, audits[0].Action)
			require.NotNil(t, audits[0].BeforeState)
			assert.JSONEq(t, `{"role":"sales","role_id":null}`, *audits[0].BeforeState)
			require.NotNil(t, audits[0].AfterState)
			assert.JSONEq(t, `{"role":"admin","role_id":null}`, *audits[0].AfterState)
			assert.Equal(t, int64(1), f.scimAudits(t, u.ID))
			assert.Equal(t, int64(1), f.count(t, "auth_audit_log", ""))

			// A repeat changes nothing and writes nothing.
			before := f.state(t)
			record, err = f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupAdd(u.ID)))
			require.NoError(t, err)
			assert.Equal(t, []uint{u.ID}, record.MemberIDs)
			assert.Equal(t, before, f.state(t), "a patch that changes nothing writes nothing")
		}},
		{"remove recomputes from the remaining groups", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			u := f.seed(t, "u@x.test", models.RoleSales, true, scimGroupSales)
			_, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupAdd(u.ID)))
			require.NoError(t, err)
			assert.Equal(t, models.RoleAdmin, f.user(t, u.ID).Role, "Admins comes first in the mapping")
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(u.ID)))
			require.NoError(t, err)
			assert.Empty(t, record.MemberIDs)
			assert.Equal(t, models.RoleSales, f.user(t, u.ID).Role)
			assert.Len(t, f.roleAudits(t, u.ID), 2, "one more audit row")
			assert.Equal(t, int64(2), f.scimAudits(t, u.ID))
		}},
		{"removing the only group deactivates", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			u := f.seed(t, "u@x.test", models.RoleSupport, true)
			f.seedCredentials(t, u)
			_, err := f.svc.Patch(ctx, salesID, parseGroupPatch(t, groupAdd(u.ID)))
			require.NoError(t, err)
			added := f.user(t, u.ID)
			assert.Equal(t, models.RoleSales, added.Role)
			require.Len(t, f.roleAudits(t, u.ID), 1)
			roleAuditsBefore := f.count(t, "role_audit_log", "")
			scimAuditsBefore := f.scimAudits(t, u.ID)

			record, err := f.svc.Patch(ctx, salesID, parseGroupPatch(t, groupRemove(u.ID)))
			require.NoError(t, err)
			assert.Empty(t, record.MemberIDs)
			stored := f.user(t, u.ID)
			assert.False(t, stored.IsActive, "the account is deactivated")
			assert.Equal(t, added.Role, stored.Role, "the role is kept")
			assert.Equal(t, added.RoleID, stored.RoleID, "the role_id is kept")
			assert.Equal(t, added.TokenVersion+1, stored.TokenVersion, "every access token ends")
			f.assertSessionsEnded(t, u.ID)
			f.assertKeys(t, u.ID, true)
			assert.Equal(t, roleAuditsBefore, f.count(t, "role_audit_log", ""), "no role audit row")
			assert.Equal(t, scimAuditsBefore+1, f.scimAudits(t, u.ID), "one sign-in audit row for the remove")
			assert.Equal(t, int64(2), f.scimAudits(t, u.ID), "the membership changed twice")
			assert.Empty(t, f.members(t))
		}},
		{"removing an inactive account's only group writes no users change", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			w := f.seed(t, "w@x.test", models.RoleSales, false, scimGroupSales)
			f.seedCredentials(t, w)
			before := f.user(t, w.ID)
			record, err := f.svc.Patch(ctx, salesID, parseGroupPatch(t, groupRemove(w.ID)))
			require.NoError(t, err)
			assert.Empty(t, record.MemberIDs)
			stored := f.user(t, w.ID)
			assert.False(t, stored.IsActive)
			assert.Equal(t, before.Role, stored.Role)
			assert.Equal(t, before.RoleID, stored.RoleID)
			assert.Equal(t, before.TokenVersion, stored.TokenVersion, "token_version unchanged")
			assert.Zero(t, f.count(t, "refresh_tokens", "is_revoked = ?", true), "refresh tokens unchanged")
			f.assertKeys(t, w.ID, false)
			assert.Empty(t, f.roleAudits(t, w.ID))
			assert.Equal(t, int64(1), f.scimAudits(t, w.ID), "the membership changed")
			assert.Empty(t, f.members(t))
		}},
		{"lowered role ends sessions", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			u := f.seed(t, "u@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
			f.seedCredentials(t, u)
			before := f.user(t, u.ID)
			_, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(u.ID)))
			require.NoError(t, err)
			stored := f.user(t, u.ID)
			assert.Equal(t, models.RoleSales, stored.Role)
			assert.True(t, stored.IsActive, "a lowered role does not deactivate")
			assert.Equal(t, before.TokenVersion+1, stored.TokenVersion, "every access token ends")
			f.assertSessionsEnded(t, u.ID)
			f.assertKeys(t, u.ID, false)
			assert.Len(t, f.roleAudits(t, u.ID), 1)
			assert.Equal(t, int64(1), f.scimAudits(t, u.ID))
		}},
		{"last_admin", func(t *testing.T) {
			u := f.seed(t, "u@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
			for _, body := range []string{groupRemove(u.ID), groupReplace(), `{"Operations":[{"op":"remove","path":"members"}]}`} {
				before := f.state(t)
				record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, body))
				f.assertRefused(t, ErrSCIMGroupLastAdmin, record, err, before)
				assert.False(t, errors.Is(err, ErrSCIMInvalidValue), "the group refusal is not invalidValue")
				assert.NotContains(t, err.Error(), scimGroupAdmins)
				stored := f.user(t, u.ID)
				assert.Equal(t, models.RoleAdmin, stored.Role)
				assert.Nil(t, stored.RoleID)
			}
			assert.Empty(t, f.roleAudits(t, u.ID))
			assert.Zero(t, f.count(t, "auth_audit_log", ""))
		}},
		{"handover_replace", func(t *testing.T) {
			x := f.seed(t, "x@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
			y := f.seed(t, "y@x.test", models.RoleSales, true)
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupReplace(y.ID)))
			require.NoError(t, err)
			assert.Equal(t, []uint{y.ID}, record.MemberIDs)
			assert.Equal(t, models.RoleAdmin, f.user(t, y.ID).Role)
			assert.Equal(t, models.RoleSales, f.user(t, x.ID).Role)
			assert.Len(t, f.roleAudits(t, x.ID), 1)
			assert.Len(t, f.roleAudits(t, y.ID), 1)
			assert.Equal(t, int64(1), f.scimAudits(t, x.ID))
			assert.Equal(t, int64(1), f.scimAudits(t, y.ID))
			assert.Equal(t, int64(1), f.activeAdmins(t))
		}},
		{"last_admin_deactivation", func(t *testing.T) {
			u := f.seed(t, "u@x.test", models.RoleAdmin, true, scimGroupAdmins)
			f.seedCredentials(t, u)
			before := f.state(t)
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(u.ID)))
			f.assertRefused(t, ErrSCIMGroupLastAdmin, record, err, before)
			stored := f.user(t, u.ID)
			assert.True(t, stored.IsActive)
			assert.Equal(t, models.RoleAdmin, stored.Role)
			f.assertKeys(t, u.ID, false)
		}},
		{"handover_deactivates_old_admin", func(t *testing.T) {
			x := f.seed(t, "x@x.test", models.RoleAdmin, true, scimGroupAdmins)
			y := f.seed(t, "y@x.test", models.RoleSales, true)
			f.seedCredentials(t, x)
			before := f.user(t, x.ID)
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupReplace(y.ID)))
			require.NoError(t, err)
			assert.Equal(t, []uint{y.ID}, record.MemberIDs)
			assert.Equal(t, models.RoleAdmin, f.user(t, y.ID).Role)
			stored := f.user(t, x.ID)
			assert.False(t, stored.IsActive, "the old admin is deactivated")
			assert.Equal(t, models.RoleAdmin, stored.Role, "its role is kept")
			assert.Equal(t, before.TokenVersion+1, stored.TokenVersion)
			f.assertSessionsEnded(t, x.ID)
			f.assertKeys(t, x.ID, true)
			assert.Empty(t, f.roleAudits(t, x.ID))
			assert.Len(t, f.roleAudits(t, y.ID), 1)
			assert.Equal(t, int64(1), f.scimAudits(t, x.ID))
			assert.Equal(t, int64(1), f.activeAdmins(t))
		}},
		{"remove all deactivates every member left in no mapped group", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			a := f.seed(t, "a@x.test", models.RoleSales, true, scimGroupSales)
			b := f.seed(t, "b@x.test", models.RoleSales, true, scimGroupSales)
			c := f.seed(t, "c@x.test", models.RoleAdmin, true, scimGroupSales, scimGroupAdmins)
			for _, u := range []*models.User{a, b, c} {
				f.seedCredentials(t, u)
			}
			record, err := f.svc.Patch(ctx, salesID, parseGroupPatch(t, `{"Operations":[{"op":"remove","path":"members"}]}`))
			require.NoError(t, err)
			assert.Empty(t, record.MemberIDs)
			for _, u := range []*models.User{a, b} {
				stored := f.user(t, u.ID)
				assert.False(t, stored.IsActive, "user %d is deactivated", u.ID)
				assert.Equal(t, models.RoleSales, stored.Role)
				assert.Equal(t, u.TokenVersion+1, stored.TokenVersion)
				f.assertSessionsEnded(t, u.ID)
				f.assertKeys(t, u.ID, true)
				assert.Equal(t, int64(1), f.scimAudits(t, u.ID))
			}
			stored := f.user(t, c.ID)
			assert.True(t, stored.IsActive, "a member still in a mapped group stays active")
			assert.Equal(t, models.RoleAdmin, stored.Role)
			assert.Equal(t, c.TokenVersion, stored.TokenVersion)
			assert.Zero(t, f.count(t, "refresh_tokens", "user_id = ? AND is_revoked = ?", c.ID, true))
			f.assertKeys(t, c.ID, false)
			assert.Zero(t, f.count(t, "role_audit_log", ""))
			assert.Equal(t, []string{"Admins/" + uintString(c.ID)}, f.members(t))
		}},
		{"handover_add_remove", func(t *testing.T) {
			x := f.seed(t, "x@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
			y := f.seed(t, "y@x.test", models.RoleSales, true)
			body := `{"Operations":[{"op":"add","path":"members","value":[{"value":"` + uintString(y.ID) + `"}]},` +
				`{"op":"remove","path":"members[value eq \"` + uintString(x.ID) + `\"]"}]}`
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, body))
			require.NoError(t, err)
			assert.Equal(t, []uint{y.ID}, record.MemberIDs)
			assert.Equal(t, models.RoleAdmin, f.user(t, y.ID).Role)
			assert.Equal(t, models.RoleSales, f.user(t, x.ID).Role)
			assert.Len(t, f.roleAudits(t, x.ID), 1)
			assert.Len(t, f.roleAudits(t, y.ID), 1)
			assert.Equal(t, int64(1), f.activeAdmins(t))
		}},
		{"replace_to_inactive", func(t *testing.T) {
			x := f.seed(t, "x@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
			w := f.seed(t, "w@x.test", models.RoleSales, false)
			before := f.state(t)
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupReplace(w.ID)))
			f.assertRefused(t, ErrSCIMGroupLastAdmin, record, err, before)
			assert.Equal(t, models.RoleAdmin, f.user(t, x.ID).Role)
			assert.Equal(t, models.RoleSales, f.user(t, w.ID).Role)
		}},
		{"deactivated member may be added", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			w := f.seed(t, "w@x.test", models.RoleSupport, false)
			_, err := f.svc.Patch(ctx, salesID, parseGroupPatch(t, groupAdd(w.ID)))
			require.NoError(t, err)
			assert.Equal(t, models.RoleSales, f.user(t, w.ID).Role)
			assert.False(t, f.user(t, w.ID).IsActive, "a group patch never activates")
		}},
		{"local account is not listed", func(t *testing.T) {
			keeper := f.seed(t, "keeper@x.test", models.RoleAdmin, true, scimGroupAdmins)
			local := f.seedLocal(t, "local.admin@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
			record, err := f.svc.Get(ctx, adminsID)
			require.NoError(t, err)
			assert.Equal(t, []uint{keeper.ID}, record.MemberIDs, "the local account is not listed")
			page, err := f.svc.List(ctx, nil, 1, scim.MaxResults)
			require.NoError(t, err)
			assert.Equal(t, []SCIMGroupRecord{
				{Value: scimGroupAdmins, MemberIDs: []uint{keeper.ID}},
				{Value: scimGroupCustomer, MemberIDs: []uint{}},
				{Value: scimGroupSales, MemberIDs: []uint{}},
			}, page.Groups)
			assert.Len(t, f.members(t), 3, "the rows stay stored")
			_ = local
		}},
		{"add or replace naming a local account is refused", func(t *testing.T) {
			keeper := f.seed(t, "keeper@x.test", models.RoleAdmin, true, scimGroupAdmins)
			local := f.seedLocal(t, "local.admin@x.test", models.RoleAdmin, true, scimGroupSales)
			sales := f.seed(t, "sales@x.test", models.RoleSales, true)
			for name, body := range map[string]string{
				"add":                  groupAdd(local.ID),
				"add next to a linked": groupAdd(sales.ID, local.ID),
				"replace":              groupReplace(keeper.ID, local.ID),
			} {
				before := f.state(t)
				record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, body))
				f.assertRefused(t, ErrSCIMInvalidValue, record, err, before)
				assert.False(t, errors.Is(err, ErrSCIMGroupLastAdmin), name)
			}
			assert.Equal(t, models.RoleAdmin, f.user(t, local.ID).Role)
			assert.Empty(t, f.roleAudits(t, local.ID))
			assert.Zero(t, f.count(t, "auth_audit_log", "user_id = ?", local.ID))
		}},
		{"remove of a local account's leftover row keeps its role", func(t *testing.T) {
			// The local admin is the only active admin and holds a row in
			// Admins from before; removing that row changes nothing else.
			local := f.seedLocal(t, "local.admin@x.test", models.RoleAdmin, true, scimGroupAdmins)
			u := f.seed(t, "u@x.test", models.RoleSales, true, scimGroupAdmins)
			require.Equal(t, int64(1), f.activeAdmins(t))
			_ = u
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(local.ID)))
			require.NoError(t, err)
			assert.Equal(t, []uint{u.ID}, record.MemberIDs)
			assert.Equal(t, []string{"Admins/" + uintString(u.ID)}, f.members(t), "the leftover row is deleted")
			stored := f.user(t, local.ID)
			assert.Equal(t, models.RoleAdmin, stored.Role, "the role stays admin")
			assert.Nil(t, stored.RoleID)
			assert.True(t, stored.IsActive)
			assert.Empty(t, f.roleAudits(t, local.ID), "no role_audit_log row names it")
			assert.Zero(t, f.count(t, "auth_audit_log", "user_id = ?", local.ID), "no auth_audit_log row names it")
			assert.Zero(t, f.count(t, "role_audit_log", ""))
			assert.Zero(t, f.count(t, "auth_audit_log", ""))
		}},
		{"invalid members", func(t *testing.T) {
			keeper := f.seed(t, "keeper@x.test", models.RoleAdmin, true, scimGroupAdmins)
			valid := f.seed(t, "valid@x.test", models.RoleSales, true)
			customer := f.seed(t, "customer@x.test", models.RoleCustomer, true)
			erased := f.seed(t, "erased@x.test", models.RoleSales, true)
			require.NoError(t, f.db.Delete(&models.User{}, erased.ID).Error)
			for name, body := range map[string]string{
				"non-numeric":          `{"Operations":[{"op":"add","path":"members","value":[{"value":"abc"}]}]}`,
				"zero":                 `{"Operations":[{"op":"add","path":"members","value":[{"value":"0"}]}]}`,
				"negative":             `{"Operations":[{"op":"add","path":"members","value":[{"value":"-1"}]}]}`,
				"unknown":              groupAdd(987654),
				"customer":             groupAdd(customer.ID),
				"erased":               groupAdd(erased.ID),
				"unknown next to good": groupAdd(valid.ID, 987654),
				"customer replace":     groupReplace(keeper.ID, customer.ID),
				"malformed remove":     `{"Operations":[{"op":"remove","path":"members[value eq \"x1\"]"}]}`,
			} {
				before := f.state(t)
				record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, body))
				f.assertRefused(t, ErrSCIMInvalidValue, record, err, before)
				assert.False(t, errors.Is(err, ErrSCIMGroupLastAdmin), name)
			}
			// A remove of a non-member is a no-op.
			before := f.state(t)
			_, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(987654)))
			require.NoError(t, err)
			assert.Equal(t, before, f.state(t))
		}},
		{"removing a member that is no longer visible", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			gone := f.seed(t, "gone@x.test", models.RoleSales, true, scimGroupSales)
			require.NoError(t, f.db.Delete(&models.User{}, gone.ID).Error)
			before := f.state(t)
			record, err := f.svc.Patch(ctx, salesID, parseGroupPatch(t, groupRemove(gone.ID)))
			require.NoError(t, err)
			assert.Empty(t, record.MemberIDs)
			assert.Empty(t, f.members(t), "the row goes")
			assert.Equal(t, before.roles, f.state(t).roles, "no role is recomputed for an erased user")
			assert.Empty(t, f.roleAudits(t, gone.ID))
		}},
		{"mapping to the customer role", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			u := f.seed(t, "u@x.test", models.RoleSales, true)
			before := f.state(t)
			record, err := f.svc.Patch(ctx, scim.GroupID(scimGroupCustomer), parseGroupPatch(t, groupAdd(u.ID)))
			f.assertRefused(t, ErrSCIMInvalidValue, record, err, before)
		}},
		{"no active admin before refuses nothing on that ground", func(t *testing.T) {
			u := f.seed(t, "u@x.test", models.RoleSales, true, scimGroupSales)
			_, err := f.svc.Patch(ctx, salesID, parseGroupPatch(t, groupRemove(u.ID)))
			require.NoError(t, err)
		}},
		{"last_admin_race/a_first", func(t *testing.T) {
			x, y := lastAdminPair(t)
			_, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(x.ID)))
			require.NoError(t, err)
			before := f.state(t)
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(y.ID)))
			f.assertRefused(t, ErrSCIMGroupLastAdmin, record, err, before)
			assertRaceOutcome(t, x, y)
		}},
		{"last_admin_race/b_first", func(t *testing.T) {
			x, y := lastAdminPair(t)
			_, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(y.ID)))
			require.NoError(t, err)
			before := f.state(t)
			record, err := f.svc.Patch(ctx, adminsID, parseGroupPatch(t, groupRemove(x.ID)))
			f.assertRefused(t, ErrSCIMGroupLastAdmin, record, err, before)
			assertRaceOutcome(t, y, x)
		}},
		{"last_admin_race/concurrent", func(t *testing.T) {
			x, y := lastAdminPair(t)
			start := make(chan struct{})
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i, target := range []*models.User{x, y} {
				i, target := i, target
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, errs[i] = f.svc.Patch(ctx, adminsID, scim.GroupPatch{Ops: []scim.GroupPatchOp{{Op: scim.GroupPatchRemove, Values: []string{uintString(target.ID)}}}})
				}()
			}
			close(start)
			wg.Wait()
			switch {
			case errs[0] == nil:
				require.ErrorIs(t, errs[1], ErrSCIMGroupLastAdmin)
				assertRaceOutcome(t, x, y)
			case errs[1] == nil:
				require.ErrorIs(t, errs[0], ErrSCIMGroupLastAdmin)
				assertRaceOutcome(t, y, x)
			default:
				t.Fatalf("exactly one patch must commit: %v / %v", errs[0], errs[1])
			}
		}},
		{"unwired writes", func(t *testing.T) {
			f.seed(t, "keeper@x.test", models.RoleAdmin, true)
			u := f.seed(t, "u@x.test", models.RoleSales, true, scimGroupSales)
			f.seedCredentials(t, u)
			unwired := NewSCIMGroupService(repository.NewSCIMGroupMemberRepository(f.db), f.source)
			_, err := unwired.Patch(ctx, adminsID, parseGroupPatch(t, groupAdd(1)))
			assert.ErrorIs(t, err, errSCIMGroupWritesNotWired)

			// The credential stores are part of the wiring.
			writes := func(refreshTokens repository.RefreshTokenRepository, apiKeys repository.APIKeyRepository) SCIMGroupService {
				return NewSCIMGroupService(repository.NewSCIMGroupMemberRepository(f.db), f.source,
					WithSCIMGroupWrites(repository.NewUserRepository(f.db), repository.NewRoleRepository(f.db), repository.NewRoleAuditRepository(f.db),
						refreshTokens, apiKeys, utils.NewTransactionManager(f.db), NewAuthAuditService(repository.NewAuthAuditRepository(f.db))))
			}
			for name, svc := range map[string]SCIMGroupService{
				"no refresh tokens": writes(nil, repository.NewAPIKeyRepository(f.db)),
				"no API keys":       writes(repository.NewRefreshTokenRepository(f.db), nil),
			} {
				before := f.state(t)
				record, err := svc.Patch(ctx, salesID, parseGroupPatch(t, groupRemove(u.ID)))
				assert.Nil(t, record, name)
				assert.ErrorIs(t, err, errSCIMGroupWritesNotWired, name)
				assert.Equal(t, before, f.state(t), "%s: nothing is written", name)
			}
		}},
	}
	if f.db.Dialector.Name() == "mysql" {
		// A handover Admins [X] -> [Y] that commits while patch B (remove Y
		// from Admins) waits for the admin set. B discovered the admins
		// before the commit, so its admin set no longer holds anyone once X
		// is re-checked; the locking read of Y must still count Y as the
		// admin before the patch. Needs a second connection and InnoDB lock
		// waits, so it runs only under the MySQL root.
		cases = append(cases, struct {
			name string
			run  func(t *testing.T)
		}{"last_admin_race/after_handover", func(t *testing.T) {
			x := f.seed(t, "x@x.test", models.RoleAdmin, true, scimGroupAdmins, scimGroupSales)
			y := f.seed(t, "y@x.test", models.RoleSales, true, scimGroupSales)

			handover := f.db.Begin()
			require.NoError(t, handover.Error)
			defer handover.Rollback()
			require.NoError(t, handover.Model(&models.User{}).Where("id = ?", x.ID).
				Updates(map[string]interface{}{"role": models.RoleSales, "role_id": f.roleIDs[models.RoleSales]}).Error)
			require.NoError(t, handover.Model(&models.User{}).Where("id = ?", y.ID).
				Updates(map[string]interface{}{"role": models.RoleAdmin, "role_id": f.roleIDs[models.RoleAdmin]}).Error)
			require.NoError(t, handover.Where("group_value = ? AND user_id = ?", scimGroupAdmins, x.ID).
				Delete(&models.SCIMGroupMember{}).Error)
			require.NoError(t, handover.Create(&models.SCIMGroupMember{GroupValue: scimGroupAdmins, UserID: y.ID}).Error)

			patch := parseGroupPatch(t, groupRemove(y.ID))
			done := make(chan error, 1)
			go func() {
				_, err := f.svc.Patch(ctx, adminsID, patch)
				done <- err
			}()

			// Wait, bounded, until B is in a lock wait on X. B's first locking
			// statement is the admin-set lock on X, which the handover holds,
			// so once it shows in the process list (this schema, another
			// connection) it is waiting. information_schema.innodb_trx is no
			// signal here: MySQL 8 does not list B's transaction there while
			// it waits, because B has only read until then.
			deadline := time.Now().Add(30 * time.Second)
			for {
				var waiting int64
				require.NoError(t, f.db.Raw("SELECT COUNT(*) FROM information_schema.processlist "+
					"WHERE id <> CONNECTION_ID() AND db = DATABASE() AND info LIKE ?", "%FOR UPDATE%").
					Scan(&waiting).Error)
				if waiting > 0 {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("the patch finished before the handover committed: %v", err)
				default:
				}
				require.True(t, time.Now().Before(deadline), "the patch never waited for the handover's lock")
				time.Sleep(10 * time.Millisecond)
			}
			require.NoError(t, handover.Commit().Error)

			var err error
			select {
			case err = <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("the patch did not finish after the handover committed")
			}
			require.ErrorIs(t, err, ErrSCIMGroupLastAdmin)
			assert.Equal(t, int64(1), f.activeAdmins(t), "exactly one active admin remains")
			assert.Equal(t, models.RoleAdmin, f.user(t, y.ID).Role, "Y stays admin")
			assert.Equal(t, models.RoleSales, f.user(t, x.ID).Role)
			assert.Zero(t, f.count(t, "role_audit_log", ""), "the refused patch wrote no role audit row")
			assert.Zero(t, f.count(t, "auth_audit_log", ""), "the refused patch wrote no sign-in audit row")
			want := []string{"Admins/" + uintString(y.ID), "Sales/" + uintString(x.ID), "Sales/" + uintString(y.ID)}
			sort.Strings(want)
			assert.Equal(t, want, f.members(t))
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.reset(t)
			tc.run(t)
		})
	}
}
