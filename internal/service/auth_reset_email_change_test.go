package service

// A change of an account's login email spends the account's outstanding
// password reset tokens in the same write (userRepository.UpdateFields), so a
// link mailed to the old address stops working: the own profile and the admin
// edit (UpdateAs), the SCIM replace and the SCIM patch. Other profile edits
// keep the link.
//
// TestResetLinkEmailChangeSQLite runs on a SQLite file opened by the
// production connector; TestResetLinkEmailChangeMySQL on a schema built by the
// real migrations when TEST_MYSQL_DSN is set (skipped without it, failing when
// it points at an unreachable server; also pointed at MariaDB 10.11).

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	recPassword    = "Current-Passw0rd!554"
	recNewPassword = "Changed-Passw0rd!554"
	recIssuer      = "https://issuer.reset-email-change.test"
	recSCIMRole    = "CRM.Sales"
)

var recSeq atomic.Uint64

func TestResetLinkEmailChangeSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runResetLinkEmailChange(t, db)
}

func TestResetLinkEmailChangeMySQL(t *testing.T) {
	runResetLinkEmailChange(t, openMigratedTestMySQL(t, 3))
}

type recEnv struct {
	db      *gorm.DB
	mail    *ciMailer
	auth    *authService
	users   UserService
	scim    SCIMUserService
	salesID uint
	admin   Actor
}

func (e *recEnv) createUser(t *testing.T, prefix string, role models.UserRole, roleID uint, managed bool) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(recPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: fmt.Sprintf("%s-%d@reset-email.test", prefix, recSeq.Add(1)), Password: string(hash),
		FirstName: "Reset", LastName: "Owner", Role: role, RoleID: &roleID, IsActive: true}
	require.NoError(t, e.db.Create(user).Error)
	if managed {
		require.NoError(t, e.db.Create(&models.UserIdentity{UserID: user.ID, Issuer: recIssuer,
			Subject: fmt.Sprintf("sub-%d", user.ID), Source: models.UserIdentitySourceSCIM}).Error)
	}
	return user
}

// issue requests a reset for email and returns the raw token of the mailed
// link, checking that the mail went to email.
func (e *recEnv) issue(t *testing.T, email string) string {
	t.Helper()
	before := len(e.mail.mails())
	_, err := e.auth.IssuePasswordReset(email)
	require.NoError(t, err)
	mails := e.mail.mails()
	require.Len(t, mails, before+1, "one reset mail was sent")
	assert.Equal(t, email, mails[len(mails)-1].to)
	return lastResetToken(t, e.mail)
}

func (e *recEnv) passwordHash(t *testing.T, id uint) string {
	t.Helper()
	var user models.User
	require.NoError(t, e.db.First(&user, id).Error)
	return user.Password
}

// assertLinkEnded checks that the link is refused and the password kept.
func (e *recEnv) assertLinkEnded(t *testing.T, raw string, user *models.User, hashBefore string) {
	t.Helper()
	_, err := e.auth.CompletePasswordReset(raw, recNewPassword)
	require.ErrorIs(t, err, ErrInvalidResetToken)
	assert.Equal(t, hashBefore, e.passwordHash(t, user.ID), "the password is unchanged")
}

// assertLinkWorks checks that the link sets the new password.
func (e *recEnv) assertLinkWorks(t *testing.T, raw string, user *models.User) {
	t.Helper()
	_, err := e.auth.CompletePasswordReset(raw, recNewPassword)
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(e.passwordHash(t, user.ID)), []byte(recNewPassword)))
}

func runResetLinkEmailChange(t *testing.T, db *gorm.DB) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	ctx := context.Background()
	e := &recEnv{db: db, mail: &ciMailer{}}
	// No identity repository: the SCIM-managed accounts below can request a
	// reset like any other (the single sign-on policy has its own tests).
	e.auth = NewAuthServiceWithSessions(repository.NewUserRepository(db), repository.NewAPIKeyRepository(db),
		repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db), e.mail, oidcTestJWTConfig(), oidcTestAppBaseURL, oidcTestAPIKeySecret).(*authService)

	var roles []models.Role
	require.NoError(t, db.Where("system_key IS NOT NULL").Find(&roles).Error)
	var adminID uint
	for _, role := range roles {
		switch models.UserRole(*role.SystemKey) {
		case models.RoleSales:
			e.salesID = role.ID
		case models.RoleAdmin:
			adminID = role.ID
		}
	}
	require.NotZero(t, e.salesID)
	require.NotZero(t, adminID)
	e.admin = Actor{UserID: e.createUser(t, "admin", models.RoleAdmin, adminID, false).ID}

	txManager := utils.NewTransactionManager(db)
	e.users = NewUserService(repository.NewUserRepository(db),
		WithUserRoleGovernance(repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db), txManager))
	source := &scimWritesSource{cfg: EffectiveSSOConfig{IssuerURL: recIssuer,
		RoleMapping: []SSORoleMapping{{Claim: SSOClaimRoles, Value: recSCIMRole, RoleID: e.salesID}}}}
	e.scim = NewSCIMUserService(repository.NewSCIMUserRepository(db), source,
		WithSCIMUserWrites(repository.NewUserRepository(db), repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db),
			repository.NewUserIdentityRepository(db), repository.NewRefreshTokenRepository(db), repository.NewAPIKeyRepository(db),
			txManager, nil))

	newEmail := func() string { return fmt.Sprintf("changed-%d@reset-email.test", recSeq.Add(1)) }

	t.Run("own_email_change", func(t *testing.T) {
		user := e.createUser(t, "own", models.RoleSales, e.salesID, false)
		raw := e.issue(t, user.Email)
		hash := e.passwordHash(t, user.ID)
		_, err := e.users.UpdateAs(ctx, Actor{UserID: user.ID}, user.ID, map[string]interface{}{"email": newEmail()})
		require.NoError(t, err)
		e.assertLinkEnded(t, raw, user, hash)
	})

	t.Run("admin_changes_other_email", func(t *testing.T) {
		user := e.createUser(t, "admin-edit", models.RoleSales, e.salesID, false)
		raw := e.issue(t, user.Email)
		hash := e.passwordHash(t, user.ID)
		_, err := e.users.UpdateAs(ctx, e.admin, user.ID, map[string]interface{}{"email": newEmail()})
		require.NoError(t, err)
		e.assertLinkEnded(t, raw, user, hash)
	})

	t.Run("scim_replace_email", func(t *testing.T) {
		user := e.createUser(t, "scim-replace", models.RoleSales, e.salesID, true)
		raw := e.issue(t, user.Email)
		hash := e.passwordHash(t, user.ID)
		in := SCIMUserInput{UserName: newEmail(), GivenName: "Reset", FamilyName: "Owner", Roles: []string{recSCIMRole}}
		_, err := e.scim.Replace(ctx, uintString(user.ID), in)
		require.NoError(t, err)
		e.assertLinkEnded(t, raw, user, hash)
	})

	t.Run("scim_patch_email", func(t *testing.T) {
		user := e.createUser(t, "scim-patch", models.RoleSales, e.salesID, true)
		raw := e.issue(t, user.Email)
		hash := e.passwordHash(t, user.ID)
		changed := newEmail()
		_, err := e.scim.Patch(ctx, uintString(user.ID), SCIMUserPatch{UserName: &changed})
		require.NoError(t, err)
		e.assertLinkEnded(t, raw, user, hash)
	})

	t.Run("name_change_keeps_link", func(t *testing.T) {
		user := e.createUser(t, "rename", models.RoleSales, e.salesID, false)
		raw := e.issue(t, user.Email)
		_, err := e.users.UpdateAs(ctx, Actor{UserID: user.ID}, user.ID, map[string]interface{}{"first_name": "Renamed"})
		require.NoError(t, err)
		e.assertLinkWorks(t, raw, user)
	})

	t.Run("same_email_keeps_link", func(t *testing.T) {
		user := e.createUser(t, "same", models.RoleSales, e.salesID, false)
		raw := e.issue(t, user.Email)
		_, err := e.users.UpdateAs(ctx, Actor{UserID: user.ID}, user.ID, map[string]interface{}{"email": user.Email})
		require.NoError(t, err)
		e.assertLinkWorks(t, raw, user)
	})

	t.Run("confirm_before_email_change", func(t *testing.T) {
		user := e.createUser(t, "confirm-first", models.RoleSales, e.salesID, false)
		raw := e.issue(t, user.Email)
		e.assertLinkWorks(t, raw, user)
		changed := newEmail()
		_, err := e.users.UpdateAs(ctx, Actor{UserID: user.ID}, user.ID, map[string]interface{}{"email": changed})
		require.NoError(t, err)
		var stored models.User
		require.NoError(t, e.db.First(&stored, user.ID).Error)
		assert.Equal(t, changed, stored.Email)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte(recNewPassword)), "the reset stays in effect")
	})

	t.Run("email_change_then_new_request", func(t *testing.T) {
		user := e.createUser(t, "new-request", models.RoleSales, e.salesID, false)
		old := e.issue(t, user.Email)
		changed := newEmail()
		_, err := e.users.UpdateAs(ctx, Actor{UserID: user.ID}, user.ID, map[string]interface{}{"email": changed})
		require.NoError(t, err)
		raw := e.issue(t, changed)
		assert.NotEqual(t, old, raw)
		e.assertLinkWorks(t, raw, user)
	})
}
