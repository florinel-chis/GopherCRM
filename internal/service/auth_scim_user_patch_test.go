package service

// Issue #471: the SCIM 2.0 user patch. TestSCIMUserPatch{SQLite,MySQL} run
// one table of subtests through the real PatchOp parser, repositories, the
// governed transaction, the session store and the sign-in audit log, on a
// SQLite file opened by the production connector and on a schema built by the
// real migrations (TEST_MYSQL_DSN, also pointed at MariaDB 10.11). They reuse
// the fixture of TestSCIMUserWrites. A deactivation must end the sessions in
// its own transaction: "sessions end" checks every refresh_tokens row is
// revoked BEFORE any refresh call (a refresh of an inactive owner would
// revoke them by itself), and "reactivate" checks the access token issued
// before stays refused once the account is active again, which only the
// token_version increment does.

import (
	"context"
	"errors"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const scimPatchPassword = "Patch-Passw0rd!-471"

// scimPatchState is everything a refused patch must leave as it was: the row
// counts of the four tables a SCIM write touches, the live refresh tokens of
// the account and the columns a patch writes.
type scimPatchState struct {
	counts       scimCounts
	liveSessions int64
	email        string
	firstName    string
	lastName     string
	role         models.UserRole
	roleID       *uint
	active       bool
	tokenVersion uint
}

func (f *scimWritesFixture) patchState(t *testing.T, userID uint) scimPatchState {
	t.Helper()
	user := f.user(t, userID)
	return scimPatchState{
		counts:       f.counts(t),
		liveSessions: f.liveRefreshTokens(t, userID),
		email:        user.Email,
		firstName:    user.FirstName,
		lastName:     user.LastName,
		role:         user.Role,
		roleID:       user.RoleID,
		active:       user.IsActive,
		tokenVersion: user.TokenVersion,
	}
}

// parsePatch parses a PatchOp body the way the handler does.
func parsePatch(t *testing.T, body string) scim.UserPatch {
	t.Helper()
	p, err := scim.ParseUserPatch([]byte(body))
	require.NoError(t, err, body)
	return p
}

// failingRevokeRepo is the real refresh token repository whose
// RevokeAllForUser fails, so a deactivation fails after its users write.
type failingRevokeRepo struct {
	repository.RefreshTokenRepository
}

var errRevokeFailed = errors.New("revoke failed")

func (r failingRevokeRepo) RevokeAllForUser(uint) error { return errRevokeFailed }

func (r failingRevokeRepo) WithTx(tx *gorm.DB) repository.RefreshTokenRepository {
	return failingRevokeRepo{r.RefreshTokenRepository.WithTx(tx)}
}

func TestSCIMUserPatchSQLite(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	runSCIMUserPatch(t, newOIDCFixture(t, db, competitor))
}

func TestSCIMUserPatchMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runSCIMUserPatch(t, newOIDCFixture(t, db, db))
}

func runSCIMUserPatch(t *testing.T, base *oidcFixture) {
	ctx := context.Background()
	db := base.db
	custom := &models.Role{Name: "SCIM Patch Custom"}
	require.NoError(t, repository.NewRoleRepository(db).Create(custom))
	f := &scimWritesFixture{oidcFixture: base, source: &scimWritesSource{}, customID: custom.ID}
	build := func(refreshTokens repository.RefreshTokenRepository) SCIMUserService {
		return NewSCIMUserService(repository.NewSCIMUserRepository(db), f.source,
			WithSCIMUserWrites(repository.NewUserRepository(db), repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db),
				repository.NewUserIdentityRepository(db), refreshTokens, repository.NewAPIKeyRepository(db), utils.NewTransactionManager(db), f.audit))
	}
	f.newServiceWith = build
	f.svc = build(repository.NewRefreshTokenRepository(db))

	// seed creates an active account linked under the issuer, with a
	// password and no audit rows.
	seed := func(t *testing.T, email string, role models.UserRole) *models.User {
		t.Helper()
		return f.seedLinked(t, email, role, true, scimPatchPassword)
	}
	// assertDeactivated checks a deactivation: inactive, the generation one
	// up and no live refresh token, read before any refresh call.
	assertDeactivated := func(t *testing.T, userID uint, versionBefore uint) {
		t.Helper()
		stored := f.user(t, userID)
		assert.False(t, stored.IsActive)
		assert.Equal(t, versionBefore+1, stored.TokenVersion, "token_version is incremented")
		assert.Zero(t, f.liveRefreshTokens(t, userID), "every refresh token is revoked")
	}
	scimAudits := func(t *testing.T, userID uint) int64 {
		return f.count(t, "auth_audit_log", "method = ? AND outcome = ? AND user_id = ?", AuthAuditMethodSCIM, AuthAuditOutcomeSuccess, userID)
	}

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"entra deactivate", func(t *testing.T) {
			ann := seed(t, "ann@x.test", models.RoleSales)
			f.login(t, ann.Email, scimPatchPassword)
			version := f.user(t, ann.ID).TokenVersion
			record, err := f.svc.Patch(ctx, uintString(ann.ID),
				parsePatch(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`))
			require.NoError(t, err)
			assert.False(t, record.User.IsActive)
			assertDeactivated(t, ann.ID, version)
			assert.Empty(t, f.roleAudits(t, ann.ID), "an untouched role writes no role audit row")
			f.assertSuccessAudit(t, ann.ID)
			stored := f.user(t, ann.ID)
			assert.Equal(t, "ann@x.test", stored.Email, "untouched attributes stay")
			assert.Equal(t, "Seed", stored.FirstName)
			assert.Equal(t, models.RoleSales, stored.Role)
		}},
		{"okta deactivate", func(t *testing.T) {
			bob := seed(t, "bob@x.test", models.RoleSupport)
			f.login(t, bob.Email, scimPatchPassword)
			version := f.user(t, bob.ID).TokenVersion
			record, err := f.svc.Patch(ctx, uintString(bob.ID),
				parsePatch(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"active":false}}]}`))
			require.NoError(t, err)
			assert.False(t, record.User.IsActive)
			assertDeactivated(t, bob.ID, version)
			f.assertSuccessAudit(t, bob.ID)

			// A second deactivation of the inactive row ends the sessions
			// issued since (none can be, but the generation still moves).
			_, err = f.svc.Patch(ctx, uintString(bob.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"active","value":false}]}`))
			require.NoError(t, err)
			assert.Equal(t, version+2, f.user(t, bob.ID).TokenVersion, "active false always increments")
		}},
		{"sessions end", func(t *testing.T) {
			cy := seed(t, "cy@x.test", models.RoleSales)
			first, second := f.login(t, cy.Email, scimPatchPassword), f.login(t, cy.Email, scimPatchPassword)
			require.Equal(t, int64(2), f.liveRefreshTokens(t, cy.ID))
			_, err := f.auth.ValidateToken(first.AccessToken)
			require.NoError(t, err, "the access token works before")
			version := f.user(t, cy.ID).TokenVersion

			_, err = f.svc.Patch(ctx, uintString(cy.ID), parsePatch(t, `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`))
			require.NoError(t, err)

			// Before any refresh call: a refresh of an inactive owner would
			// revoke the tokens by itself.
			var rows []models.RefreshToken
			require.NoError(t, f.db.Where("user_id = ?", cy.ID).Find(&rows).Error)
			require.Len(t, rows, 2)
			for _, row := range rows {
				assert.True(t, row.Revoked, "refresh token %d is revoked by the patch itself", row.ID)
			}
			assert.Equal(t, version+1, f.user(t, cy.ID).TokenVersion)

			for _, tokens := range []*AuthTokens{first, second} {
				_, err = f.auth.ValidateToken(tokens.AccessToken)
				assert.Error(t, err, "the access token is refused")
				refreshed, err := f.auth.RefreshAccessToken(tokens.RefreshToken)
				assert.Error(t, err, "the refresh token is refused")
				assert.Nil(t, refreshed)
			}
		}},
		{"reactivate", func(t *testing.T) {
			dee := seed(t, "dee@x.test", models.RoleSales)
			first, second := f.login(t, dee.Email, scimPatchPassword), f.login(t, dee.Email, scimPatchPassword)
			version := f.user(t, dee.ID).TokenVersion
			_, err := f.svc.Patch(ctx, uintString(dee.ID), parsePatch(t, `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`))
			require.NoError(t, err)
			record, err := f.svc.Patch(ctx, uintString(dee.ID), parsePatch(t, `{"Operations":[{"op":"replace","value":{"active":true}}]}`))
			require.NoError(t, err)
			assert.True(t, record.User.IsActive)
			stored := f.user(t, dee.ID)
			assert.True(t, stored.IsActive, "active true reactivates")
			assert.Equal(t, version+1, stored.TokenVersion, "active true does not move the generation")
			assert.Zero(t, f.liveRefreshTokens(t, dee.ID), "active true brings no session back")

			// The account is active again, so only the generation refuses
			// the old access token.
			_, err = f.auth.ValidateToken(first.AccessToken)
			assert.ErrorIs(t, err, ErrAccessTokenRevoked, "the access token issued before stays refused")
			refreshed, err := f.auth.RefreshAccessToken(second.RefreshToken)
			assert.Error(t, err, "the unused refresh token issued before stays refused")
			assert.Nil(t, refreshed)

			fresh := f.login(t, dee.Email, scimPatchPassword)
			validated, err := f.auth.ValidateToken(fresh.AccessToken)
			require.NoError(t, err, "a new sign-in works")
			assert.Equal(t, dee.ID, validated.ID)
			_, err = f.auth.RefreshAccessToken(fresh.RefreshToken)
			assert.NoError(t, err, "a new refresh token works")
		}},
		{"last admin", func(t *testing.T) {
			admin := seed(t, "admin@x.test", models.RoleAdmin)
			f.login(t, admin.Email, scimPatchPassword)
			before := f.patchState(t, admin.ID)
			require.Equal(t, int64(1), before.liveSessions)
			for name, body := range map[string]string{
				"path active False":    `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`,
				"path-less active":     `{"Operations":[{"op":"replace","value":{"active":false}}]}`,
				"roles to a non-admin": `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"` + scimRoleSales + `"}]}]}`,
			} {
				record, err := f.svc.Patch(ctx, uintString(admin.ID), parsePatch(t, body))
				assert.Nil(t, record, name)
				assert.ErrorIs(t, err, ErrSCIMLastAdmin, name)
				assert.ErrorIs(t, err, ErrSCIMInvalidValue, name)
				assert.Equal(t, before, f.patchState(t, admin.ID), "%s: nothing is written", name)
			}
			assert.Empty(t, f.roleAudits(t, admin.ID))
			assert.Zero(t, f.count(t, "auth_audit_log", ""))

			// With a second active admin the same deactivation goes through.
			f.seedUser(t, "admin2@x.test", models.RoleAdmin, true, "")
			_, err := f.svc.Patch(ctx, uintString(admin.ID), parsePatch(t, `{"Operations":[{"op":"replace","value":{"active":false}}]}`))
			require.NoError(t, err)
			assertDeactivated(t, admin.ID, before.tokenVersion)
		}},
		{"role change", func(t *testing.T) {
			eve := seed(t, "eve@x.test", models.RoleSales)
			record, err := f.svc.Patch(ctx, uintString(eve.ID),
				parsePatch(t, `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"unrelated"},{"value":"`+oidcAppRole+`"}]}]}`))
			require.NoError(t, err)
			assert.True(t, record.User.IsActive)
			stored := f.user(t, eve.ID)
			assert.Equal(t, models.RoleSupport, stored.Role)
			assert.Nil(t, stored.RoleID)
			audits := f.roleAudits(t, eve.ID)
			require.Len(t, audits, 1)
			assert.Equal(t, models.RoleAuditUserRoleChanged, audits[0].Action)
			assert.Equal(t, AuthMethodSCIM, audits[0].AuthMethod)
			assert.Nil(t, audits[0].ActorUserID, "no user acted")
			require.NotNil(t, audits[0].BeforeState)
			require.NotNil(t, audits[0].AfterState)
			assert.JSONEq(t, `{"role":"sales","role_id":null}`, *audits[0].BeforeState)
			assert.JSONEq(t, `{"role":"support","role_id":null}`, *audits[0].AfterState)
			f.assertSuccessAudit(t, eve.ID)

			// The same role again writes no role audit row; a custom role
			// stores its id.
			_, err = f.svc.Patch(ctx, uintString(eve.ID), parsePatch(t, `{"Operations":[{"op":"add","value":{"roles":[{"value":"`+oidcAppRole+`"}]}}]}`))
			require.NoError(t, err)
			assert.Len(t, f.roleAudits(t, eve.ID), 1, "an unchanged role writes no role audit row")
			_, err = f.svc.Patch(ctx, uintString(eve.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"`+scimRoleCustom+`"}]}]}`))
			require.NoError(t, err)
			stored = f.user(t, eve.ID)
			assert.Equal(t, models.RoleCustom, stored.Role)
			if assert.NotNil(t, stored.RoleID) {
				assert.Equal(t, f.customID, *stored.RoleID)
			}
			audits = f.roleAudits(t, eve.ID)
			require.Len(t, audits, 2)
			assert.Nil(t, audits[1].ActorUserID)
			assert.Equal(t, AuthMethodSCIM, audits[1].AuthMethod)
			assert.Equal(t, int64(3), scimAudits(t, eve.ID), "one sign-in audit row per successful patch")

			// No matching entry, a mapping to customer and a mapping to a
			// missing role row are invalidValue and write nothing.
			before := f.patchState(t, eve.ID)
			for name, role := range map[string]string{"unmapped": "nobody", "case variant": "crm.sales", "customer": scimRoleCustomer, "no such role row": scimRoleGhost} {
				record, err := f.svc.Patch(ctx, uintString(eve.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"`+role+`"}]}]}`))
				assert.Nil(t, record, name)
				assert.ErrorIs(t, err, ErrSCIMInvalidValue, name)
				assert.Equal(t, before, f.patchState(t, eve.ID), name)
			}
		}},
		{"name and emails", func(t *testing.T) {
			fay := seed(t, "fay@x.test", models.RoleSales)
			f.login(t, fay.Email, scimPatchPassword)
			record, err := f.svc.Patch(ctx, uintString(fay.ID), parsePatch(t, `{"Operations":[
				{"op":"Add","value":{"name.givenName":"Faye"}},
				{"op":"Replace","path":"emails[type eq \"work\"].value","value":"faye@x.test"}]}`))
			require.NoError(t, err)
			assert.Equal(t, "faye@x.test", record.User.Email)
			stored := f.user(t, fay.ID)
			assert.Equal(t, "faye@x.test", stored.Email)
			assert.Equal(t, "Faye", stored.FirstName)
			assert.Equal(t, "User", stored.LastName, "an untouched name part stays")
			assert.True(t, stored.IsActive)
			assert.Equal(t, int64(1), f.liveRefreshTokens(t, fay.ID), "no deactivation, no session ends")
			assert.Empty(t, f.roleAudits(t, fay.ID), "an untouched role writes no role audit row")
			f.assertSuccessAudit(t, fay.ID)

			_, err = f.svc.Patch(ctx, uintString(fay.ID), parsePatch(t, `{"Operations":[{"op":"remove","path":"name.familyName"},
				{"op":"replace","path":"emails","value":[{"value":"home@x.test","type":"home"},{"value":"fay@x.test","type":"work"}]}]}`))
			require.NoError(t, err)
			stored = f.user(t, fay.ID)
			assert.Equal(t, "", stored.LastName, "remove clears the part")
			assert.Equal(t, "Faye", stored.FirstName)
			assert.Equal(t, "fay@x.test", stored.Email, "the work address wins")
			assert.Empty(t, f.roleAudits(t, fay.ID))
			assert.Equal(t, int64(2), scimAudits(t, fay.ID))
			assert.Equal(t, int64(2), f.count(t, "auth_audit_log", ""))

			// A taken address is a uniqueness refusal, an invalid one an
			// invalidValue; a too long name part too.
			other := seed(t, "other@x.test", models.RoleSales)
			before := f.patchState(t, fay.ID)
			record, err = f.svc.Patch(ctx, uintString(fay.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"userName","value":"OTHER@x.test"}]}`))
			assert.Nil(t, record)
			assert.ErrorIs(t, err, ErrSCIMUniqueness)
			assert.Equal(t, before, f.patchState(t, fay.ID))
			record, err = f.svc.Patch(ctx, uintString(fay.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"emails","value":[{"value":"not-an-address"}]}]}`))
			assert.Nil(t, record)
			assert.ErrorIs(t, err, ErrSCIMInvalidValue)
			assert.Equal(t, before, f.patchState(t, fay.ID))
			long := make([]rune, 101)
			for i := range long {
				long[i] = 'é'
			}
			record, err = f.svc.Patch(ctx, uintString(fay.ID), scim.UserPatch{GivenName: func() *string { s := string(long); return &s }()})
			assert.Nil(t, record)
			assert.ErrorIs(t, err, ErrSCIMInvalidValue)
			assert.Equal(t, before, f.patchState(t, fay.ID))
			assert.Equal(t, "other@x.test", f.user(t, other.ID).Email)
		}},
		{"userName wins", func(t *testing.T) {
			gus := seed(t, "gus@x.test", models.RoleSales)
			for name, body := range map[string]string{
				"userName first": `{"Operations":[{"op":"replace","path":"userName","value":"gus.u@x.test"},{"op":"replace","path":"emails","value":[{"value":"gus.e@x.test"}]}]}`,
				"emails first":   `{"Operations":[{"op":"replace","path":"emails","value":[{"value":"gus.e@x.test"}]},{"op":"replace","path":"userName","value":"gus.u@x.test"}]}`,
				"one object":     `{"Operations":[{"op":"replace","value":{"emails":[{"value":"gus.e@x.test","primary":true}],"userName":"gus.u@x.test"}}]}`,
			} {
				require.NoError(t, f.db.Model(&models.User{}).Where("id = ?", gus.ID).Update("email", "gus@x.test").Error)
				_, err := f.svc.Patch(ctx, uintString(gus.ID), parsePatch(t, body))
				require.NoError(t, err, name)
				assert.Equal(t, "gus.u@x.test", f.user(t, gus.ID).Email, name)
			}
		}},
		{"not visible", func(t *testing.T) {
			customer := seed(t, "cust@x.test", models.RoleCustomer)
			erased := seed(t, "gone@x.test", models.RoleSales)
			require.NoError(t, repository.NewUserRepository(f.db).Delete(erased.ID), "erasure through the real path")
			before := f.counts(t)
			deactivate := parsePatch(t, `{"Operations":[{"op":"replace","path":"active","value":false}]}`)
			for _, id := range []string{"999999", uintString(erased.ID), uintString(customer.ID), "abc", "0", "-1", ""} {
				record, err := f.svc.Patch(ctx, id, deactivate)
				f.assertRefused(t, apperrors.ErrNotFound, record, err, before)
			}
			stored := f.user(t, customer.ID)
			assert.True(t, stored.IsActive)
			assert.Zero(t, stored.TokenVersion)
		}},
		{"local account is not found", func(t *testing.T) {
			local := f.seedLocalAdmin(t)
			before := f.patchState(t, local.ID)
			for name, body := range map[string]string{
				"userName":     `{"Operations":[{"op":"replace","path":"userName","value":"renamed@x.test"}]}`,
				"active false": `{"Operations":[{"op":"replace","path":"active","value":false}]}`,
			} {
				record, err := f.svc.Patch(ctx, uintString(local.ID), parsePatch(t, body))
				assert.Nil(t, record, name)
				assert.ErrorIs(t, err, apperrors.ErrNotFound, name)
				assert.Equal(t, before, f.patchState(t, local.ID), "%s: the local account is unchanged", name)
			}
		}},
		{"account linked under another issuer is not found", func(t *testing.T) {
			lee := f.seedOtherIssuer(t, "lee@x.test", models.RoleSales, scimPatchPassword)
			f.login(t, lee.Email, scimPatchPassword)
			before := f.patchState(t, lee.ID)
			require.Equal(t, int64(1), before.liveSessions)
			record, err := f.svc.Patch(ctx, uintString(lee.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"active","value":false}]}`))
			assert.Nil(t, record)
			assert.ErrorIs(t, err, apperrors.ErrNotFound)
			assert.Equal(t, before, f.patchState(t, lee.ID), "the account is unchanged")
		}},
		{"refusals write nothing", func(t *testing.T) {
			hal := seed(t, "hal@x.test", models.RoleSales)
			taken := seed(t, "taken@x.test", models.RoleSales)
			f.login(t, hal.Email, scimPatchPassword)
			before := f.patchState(t, hal.ID)
			require.Equal(t, int64(1), before.liveSessions)

			// Refusals found inside the transaction, after the locks.
			for name, body := range map[string]string{
				"email taken with a deactivation": `{"Operations":[{"op":"replace","path":"active","value":false},{"op":"replace","path":"userName","value":"taken@x.test"}]}`,
				"unmapped role with a name":       `{"Operations":[{"op":"replace","value":{"name.givenName":"New","roles":[{"value":"` + scimRoleGhost + `"}]}}]}`,
				"bad address with a deactivation": `{"Operations":[{"op":"replace","path":"active","value":"false"},{"op":"replace","path":"userName","value":"not-an-address"}]}`,
			} {
				record, err := f.svc.Patch(ctx, uintString(hal.ID), parsePatch(t, body))
				assert.Nil(t, record, name)
				assert.Error(t, err, name)
				assert.Equal(t, before, f.patchState(t, hal.ID), "%s: nothing is written", name)
			}

			// A refusal found after the users write: the revocation fails, and
			// the deactivation, the generation, the name and the role change
			// written before it roll back with it.
			failing := f.newServiceWith(failingRevokeRepo{repository.NewRefreshTokenRepository(f.db)})
			record, err := failing.Patch(ctx, uintString(hal.ID), parsePatch(t, `{"Operations":[{"op":"replace","value":{"active":false,"name.givenName":"Rolled","roles":[{"value":"`+oidcAppRole+`"}]}}]}`))
			assert.Nil(t, record)
			assert.ErrorIs(t, err, errRevokeFailed)
			assert.Equal(t, before, f.patchState(t, hal.ID), "the failed revocation rolls everything back")
			assert.Empty(t, f.roleAudits(t, hal.ID))
			assert.Zero(t, f.count(t, "auth_audit_log", ""))
			assert.Equal(t, "taken@x.test", f.user(t, taken.ID).Email)
		}},
		{"not wired", func(t *testing.T) {
			ivy := seed(t, "ivy@x.test", models.RoleSales)
			f.login(t, ivy.Email, scimPatchPassword)
			before := f.patchState(t, ivy.ID)
			unwired := NewSCIMUserService(repository.NewSCIMUserRepository(db), f.source)
			_, err := unwired.Patch(ctx, uintString(ivy.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"active","value":false}]}`))
			assert.ErrorIs(t, err, errSCIMWritesNotWired)

			noSessions := f.newServiceWith(nil)
			_, err = noSessions.Patch(ctx, uintString(ivy.ID), parsePatch(t, `{"Operations":[{"op":"replace","path":"name.givenName","value":"X"}]}`))
			assert.ErrorIs(t, err, errSCIMWritesNotWired, "a patch needs the refresh token repository")
			inactive := false
			_, err = noSessions.Replace(ctx, uintString(ivy.ID), SCIMUserInput{UserName: ivy.Email, Active: &inactive, Roles: []string{scimRoleSales}})
			assert.ErrorIs(t, err, errSCIMWritesNotWired, "a deactivating replace needs the refresh token repository")
			assert.Equal(t, before, f.patchState(t, ivy.ID))

			// A replace that does not deactivate still works without it.
			_, err = noSessions.Replace(ctx, uintString(ivy.ID), SCIMUserInput{UserName: ivy.Email, Roles: []string{scimRoleSales}})
			assert.NoError(t, err)
		}},
	}
	if f.db.Dialector.Name() == "mysql" {
		// #513: a handover X -> Y that commits while the patch active false of Y waits
		// for the admin set. The write's admin set comes back empty; the
		// locking read of Y must still count Y as the admin before it.
		cases = append(cases, struct {
			name string
			run  func(t *testing.T)
		}{"last_admin_race/after_handover", func(t *testing.T) {
			x := f.seedUser(t, "x@x.test", models.RoleAdmin, true, scimPatchPassword)
			y := f.seedLinked(t, "y@x.test", models.RoleSales, true, scimPatchPassword)
			f.login(t, y.Email, scimPatchPassword)
			require.Equal(t, int64(1), f.liveRefreshTokens(t, y.ID))
			versionBefore := f.user(t, y.ID).TokenVersion
			rolesBefore := f.count(t, "role_audit_log", "")

			deactivate := parsePatch(t, `{"Operations":[{"op":"replace","path":"active","value":false}]}`)
			handover := holdAdminHandover(t, f.db, f.roleIDs, x.ID, y.ID)
			done := make(chan error, 1)
			go func() {
				_, err := f.svc.Patch(ctx, uintString(y.ID), deactivate)
				done <- err
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
