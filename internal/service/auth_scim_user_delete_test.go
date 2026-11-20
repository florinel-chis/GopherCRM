package service

// Issue #504: the SCIM 2.0 user delete. TestSCIMUserDelete{SQLite,MySQL} run
// one table of subtests through the real repositories, the governed
// transaction, the session store and the sign-in audit log, on a SQLite file
// opened by the production connector and on a schema built by the real
// migrations (TEST_MYSQL_DSN, also pointed at MariaDB 10.11). They reuse the
// fixture of TestSCIMUserWrites. A delete deactivates and never erases: the
// row keeps its profile, role, identity link and a NULL deleted_at, while
// token_version moves one up and every refresh token is revoked in the same
// transaction, checked BEFORE any refresh call (a refresh of an inactive
// owner would revoke them by itself). A repeat on the inactive row is a
// success that writes nothing.

import (
	"context"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const scimDeletePassword = "Delete-Passw0rd!-504"

func TestSCIMUserDeleteSQLite(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	runSCIMUserDelete(t, newOIDCFixture(t, db, competitor))
}

func TestSCIMUserDeleteMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runSCIMUserDelete(t, newOIDCFixture(t, db, db))
}

// scimDeleteRows is the row count of every table a SCIM write or a sign-in
// touches.
type scimDeleteRows struct {
	counts        scimCounts
	refreshTokens int64
}

func runSCIMUserDelete(t *testing.T, base *oidcFixture) {
	ctx := context.Background()
	db := base.db
	custom := &models.Role{Name: "SCIM Delete Custom"}
	require.NoError(t, repository.NewRoleRepository(db).Create(custom))
	f := &scimWritesFixture{oidcFixture: base, source: &scimWritesSource{}, customID: custom.ID}
	build := func(refreshTokens repository.RefreshTokenRepository) SCIMUserService {
		return NewSCIMUserService(repository.NewSCIMUserRepository(db), f.source,
			WithSCIMUserWrites(repository.NewUserRepository(db), repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db),
				repository.NewUserIdentityRepository(db), refreshTokens, repository.NewAPIKeyRepository(db), utils.NewTransactionManager(db), f.audit))
	}
	f.newServiceWith = build
	f.svc = build(repository.NewRefreshTokenRepository(db))

	rows := func(t *testing.T) scimDeleteRows {
		t.Helper()
		return scimDeleteRows{counts: f.counts(t), refreshTokens: f.count(t, "refresh_tokens", "")}
	}
	scimAudits := func(t *testing.T, userID uint) int64 {
		t.Helper()
		return f.count(t, "auth_audit_log", "method = ? AND outcome = ? AND user_id = ?", AuthAuditMethodSCIM, AuthAuditOutcomeSuccess, userID)
	}
	// provision creates the account through the SCIM create and gives it a
	// password, so it can sign in and hold refresh tokens.
	provision := func(t *testing.T, email, externalID, role string) *SCIMUserRecord {
		t.Helper()
		record, err := f.svc.Create(ctx, scimInput(email, externalID, role))
		require.NoError(t, err)
		hash, err := bcrypt.GenerateFromPassword([]byte(scimDeletePassword), bcrypt.MinCost)
		require.NoError(t, err)
		require.NoError(t, f.db.Model(&models.User{}).Where("id = ?", record.User.ID).Update("password", string(hash)).Error)
		return record
	}
	identityOf := func(t *testing.T, userID uint) []models.UserIdentity {
		t.Helper()
		var identities []models.UserIdentity
		require.NoError(t, f.db.Where("user_id = ?", userID).Order("id").Find(&identities).Error)
		return identities
	}

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"deactivates", func(t *testing.T) {
			record := provision(t, "ann@x.test", "sub-ann", scimRoleSales)
			id := record.User.ID
			first, second := f.login(t, record.User.Email, scimDeletePassword), f.login(t, record.User.Email, scimDeletePassword)
			require.Equal(t, int64(2), f.liveRefreshTokens(t, id))
			before := f.user(t, id)
			require.True(t, before.IsActive)
			identitiesBefore := identityOf(t, id)
			require.Len(t, identitiesBefore, 1)
			rowsBefore := rows(t)
			auditsBefore := scimAudits(t, id)

			require.NoError(t, f.svc.Delete(ctx, uintString(id)))

			stored := f.user(t, id)
			assert.False(t, stored.IsActive)
			assert.Equal(t, before.TokenVersion+1, stored.TokenVersion, "token_version is incremented")
			assert.Equal(t, before.Email, stored.Email)
			assert.Equal(t, before.FirstName, stored.FirstName)
			assert.Equal(t, before.LastName, stored.LastName)
			assert.Equal(t, before.Role, stored.Role)
			assert.Equal(t, before.RoleID, stored.RoleID)
			assert.False(t, stored.DeletedAt.Valid, "deleted_at stays NULL: a delete never erases")
			assert.Equal(t, int64(1), f.count(t, "users", "id = ? AND deleted_at IS NULL", id))
			assert.Equal(t, identitiesBefore, identityOf(t, id), "the identity link stays")

			// Before any refresh call: a refresh of an inactive owner would
			// revoke the tokens by itself.
			assert.Zero(t, f.liveRefreshTokens(t, id), "every refresh token is revoked by the delete itself")

			for _, tokens := range []*AuthTokens{first, second} {
				_, err := f.auth.ValidateToken(tokens.AccessToken)
				assert.Error(t, err, "the access token is refused")
				refreshed, err := f.auth.RefreshAccessToken(tokens.RefreshToken)
				assert.Error(t, err, "the refresh token is refused")
				assert.Nil(t, refreshed)
			}

			assert.Equal(t, auditsBefore+1, scimAudits(t, id), "one successful scim audit row")
			after := rows(t)
			assert.Equal(t, rowsBefore.counts.authAudits+1, after.counts.authAudits, "exactly one new auth_audit_log row")
			assert.Equal(t, rowsBefore.counts.roleAudits, after.counts.roleAudits, "no role_audit_log row")
			assert.Equal(t, rowsBefore.counts.users, after.counts.users)
			assert.Equal(t, rowsBefore.counts.identities, after.counts.identities)
		}},
		{"idempotent", func(t *testing.T) {
			record := provision(t, "bob@x.test", "sub-bob", scimRoleSales)
			id := record.User.ID
			f.login(t, record.User.Email, scimDeletePassword)
			require.NoError(t, f.svc.Delete(ctx, uintString(id)))
			stored := f.user(t, id)
			rowsBefore := rows(t)
			auditsBefore := scimAudits(t, id)

			require.NoError(t, f.svc.Delete(ctx, uintString(id)), "a repeat is a success")

			again := f.user(t, id)
			assert.False(t, again.IsActive)
			assert.Equal(t, stored.TokenVersion, again.TokenVersion, "a repeat does not move token_version")
			assert.Equal(t, stored.UpdatedAt, again.UpdatedAt, "a repeat writes nothing")
			assert.Equal(t, auditsBefore, scimAudits(t, id), "a repeat writes no audit row")
			assert.Equal(t, rowsBefore, rows(t), "a repeat adds no row anywhere")
		}},
		{"last admin", func(t *testing.T) {
			admin := f.seedLinked(t, "admin@x.test", models.RoleAdmin, true, scimDeletePassword)
			f.login(t, admin.Email, scimDeletePassword)
			before := f.patchState(t, admin.ID)
			require.Equal(t, int64(1), before.liveSessions)
			rowsBefore := rows(t)

			err := f.svc.Delete(ctx, uintString(admin.ID))
			assert.ErrorIs(t, err, ErrSCIMLastAdmin)
			assert.ErrorIs(t, err, ErrSCIMInvalidValue)
			assert.Equal(t, before, f.patchState(t, admin.ID), "nothing is written")
			assert.Equal(t, rowsBefore, rows(t))
			assert.Zero(t, f.count(t, "auth_audit_log", ""), "a refusal writes no audit row")
		}},
		{"second admin", func(t *testing.T) {
			admin := f.seedLinked(t, "admin@x.test", models.RoleAdmin, true, scimDeletePassword)
			f.seedUser(t, "admin2@x.test", models.RoleAdmin, true, "")
			f.login(t, admin.Email, scimDeletePassword)
			version := f.user(t, admin.ID).TokenVersion

			require.NoError(t, f.svc.Delete(ctx, uintString(admin.ID)))
			stored := f.user(t, admin.ID)
			assert.False(t, stored.IsActive)
			assert.Equal(t, models.RoleAdmin, stored.Role, "the role stays")
			assert.Equal(t, version+1, stored.TokenVersion)
			assert.Zero(t, f.liveRefreshTokens(t, admin.ID))
			f.assertSuccessAudit(t, admin.ID)
		}},
		{"not found", func(t *testing.T) {
			customer := f.seedLinked(t, "cust@x.test", models.RoleCustomer, true, scimDeletePassword)
			f.login(t, customer.Email, scimDeletePassword)
			erased := f.seedLinked(t, "gone@x.test", models.RoleSales, true, "")
			require.NoError(t, repository.NewUserRepository(f.db).Delete(erased.ID), "erasure through the real path")
			rowsBefore := rows(t)
			customerBefore := f.patchState(t, customer.ID)
			erasedBefore := f.user(t, erased.ID)
			for _, id := range []string{"abc", "0", "999999", uintString(customer.ID), uintString(erased.ID)} {
				err := f.svc.Delete(ctx, id)
				assert.True(t, apperrors.IsNotFound(err), "%q: %v", id, err)
			}
			assert.Equal(t, rowsBefore, rows(t), "a not-found writes nothing")
			assert.Equal(t, customerBefore, f.patchState(t, customer.ID))
			assert.Equal(t, erasedBefore, f.user(t, erased.ID))
		}},
		{"local account is not found", func(t *testing.T) {
			local := f.seedLocalAdmin(t)
			before := f.patchState(t, local.ID)
			rowsBefore := rows(t)
			err := f.svc.Delete(ctx, uintString(local.ID))
			assert.ErrorIs(t, err, apperrors.ErrNotFound)
			assert.Equal(t, before, f.patchState(t, local.ID), "the local account is unchanged")
			assert.Equal(t, rowsBefore, rows(t), "a not-found writes nothing")
		}},
		{"not wired", func(t *testing.T) {
			ivy := f.seedLinked(t, "ivy@x.test", models.RoleSales, true, scimDeletePassword)
			f.login(t, ivy.Email, scimDeletePassword)
			before := f.patchState(t, ivy.ID)
			assert.ErrorIs(t, f.newServiceWith(nil).Delete(ctx, uintString(ivy.ID)), errSCIMWritesNotWired,
				"a delete needs the refresh token repository")
			unwired := NewSCIMUserService(repository.NewSCIMUserRepository(db), f.source)
			assert.ErrorIs(t, unwired.Delete(ctx, uintString(ivy.ID)), errSCIMWritesNotWired)
			assert.Equal(t, before, f.patchState(t, ivy.ID))
		}},
	}
	if f.db.Dialector.Name() == "mysql" {
		// #513: a handover X -> Y that commits while the delete of Y waits
		// for the admin set. The write's admin set comes back empty; the
		// locking read of Y must still count Y as the admin before it.
		cases = append(cases, struct {
			name string
			run  func(t *testing.T)
		}{"last_admin_race/after_handover", func(t *testing.T) {
			x := f.seedUser(t, "x@x.test", models.RoleAdmin, true, scimDeletePassword)
			y := f.seedLinked(t, "y@x.test", models.RoleSales, true, scimDeletePassword)
			f.login(t, y.Email, scimDeletePassword)
			require.Equal(t, int64(1), f.liveRefreshTokens(t, y.ID))
			versionBefore := f.user(t, y.ID).TokenVersion
			rolesBefore := f.count(t, "role_audit_log", "")

			handover := holdAdminHandover(t, f.db, f.roleIDs, x.ID, y.ID)
			done := make(chan error, 1)
			go func() {
				done <- f.svc.Delete(ctx, uintString(y.ID))
			}()
			err := awaitLockWaitThenCommit(t, f.db, handover, done)

			require.ErrorIs(t, err, ErrSCIMLastAdmin)
			assert.Equal(t, int64(1), countActiveAdmins(t, f.db), "exactly one active admin remains")
			stored := f.user(t, y.ID)
			assert.Equal(t, models.RoleAdmin, stored.Role, "Y stays admin")
			assert.True(t, stored.IsActive, "Y stays active")
			assert.Equal(t, versionBefore, stored.TokenVersion, "token_version is unchanged")
			assert.Equal(t, int64(1), f.liveRefreshTokens(t, y.ID), "Y's refresh tokens stay unrevoked")
			assert.Zero(t, f.count(t, "auth_audit_log", ""), "the refused write wrote no audit row")
			assert.Equal(t, rolesBefore, f.count(t, "role_audit_log", ""), "no role audit row")
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.reset(t)
			tc.run(t)
		})
	}
}
