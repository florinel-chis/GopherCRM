package service

// Regression roots for issue #444: an account provisioned by single sign-on
// (one with a user_identities row) can neither obtain nor use a password
// reset token, with the public answers unchanged. A password account without
// an identity row is the control in each half. Every assertion reads
// committed rows outside any transaction.

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestPasswordResetSSOAccountSQLite(t *testing.T) {
	runPasswordResetSSOAccount(t, openResetTestSQLite(t))
}

func TestPasswordResetSSOAccountMySQL(t *testing.T) {
	runPasswordResetSSOAccount(t, openResetTestMySQL(t, 4))
}

type ssoResetFixture struct {
	db     *gorm.DB
	mailer *fakeFormMailer
	svc    AuthService
}

func newSSOResetFixture(t *testing.T, db *gorm.DB) *ssoResetFixture {
	t.Helper()
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	require.NoError(t, db.AutoMigrate(&models.UserIdentity{}))
	f := &ssoResetFixture{db: db, mailer: &fakeFormMailer{}}
	f.svc = NewAuthServiceWithSessions(
		repository.NewUserRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewRefreshTokenRepository(db),
		repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db),
		f.mailer,
		config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30},
		resetTestBaseURL, "",
		WithAuthIdentities(repository.NewUserIdentityRepository(db)))
	return f
}

// seed empties every table the scenario reads and creates a provisioned
// account (with an identity row and a live refresh token) and a password
// account (no identity row).
func (f *ssoResetFixture) seed(t *testing.T) (provisioned, password *models.User) {
	t.Helper()
	f.mailer.reset()
	ctx := resetContext(t)
	for _, table := range []string{"user_identities", "password_reset_tokens", "refresh_tokens", "users"} {
		require.NoError(t, f.db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
	}
	create := func(email string) *models.User {
		hash, err := bcrypt.GenerateFromPassword([]byte(resetOldPassword), bcrypt.MinCost)
		require.NoError(t, err)
		user := &models.User{
			Email: email, Password: string(hash), FirstName: "Reset", LastName: "Owner",
			Role: models.RoleSales, IsActive: true,
		}
		require.NoError(t, f.db.WithContext(ctx).Create(user).Error)
		return user
	}
	provisioned = create("provisioned@example.com")
	password = create("password@example.com")
	require.NoError(t, f.db.WithContext(ctx).Create(&models.UserIdentity{
		UserID: provisioned.ID, Issuer: "https://idp.example.com", Subject: "subject-444",
	}).Error)
	require.NoError(t, f.db.WithContext(ctx).Create(&models.RefreshToken{
		UserID: provisioned.ID, TokenHash: hashOpaqueToken("provisioned-session", resetTestSecret),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)
	return provisioned, password
}

func (f *ssoResetFixture) count(t *testing.T, model interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.WithContext(resetContext(t)).Model(model).Where(where, args...).Count(&n).Error)
	return n
}

func (f *ssoResetFixture) user(t *testing.T, id uint) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, f.db.WithContext(resetContext(t)).Unscoped().First(&user, id).Error)
	return user
}

// preseedToken inserts an unexpired, unused reset token for the user directly
// and returns its raw value.
func (f *ssoResetFixture) preseedToken(t *testing.T, userID uint, raw string) {
	t.Helper()
	require.NoError(t, f.db.WithContext(resetContext(t)).Create(&models.PasswordResetToken{
		UserID: userID, TokenHash: hashOpaqueToken(raw, resetTestSecret), ExpiresAt: time.Now().Add(time.Hour),
	}).Error)
}

func runPasswordResetSSOAccount(t *testing.T, db *gorm.DB) {
	f := newSSOResetFixture(t, db)

	t.Run("request_refused", func(t *testing.T) {
		provisioned, _ := f.seed(t)
		require.NoError(t, f.svc.RequestPasswordReset(provisioned.Email))
		assert.Zero(t, f.count(t, &models.PasswordResetToken{}, "user_id = ?", provisioned.ID),
			"no reset token for an account provisioned by single sign-on")
		assert.Empty(t, f.mailer.messages(), "no mail for an account provisioned by single sign-on")
	})

	t.Run("request_control", func(t *testing.T) {
		_, password := f.seed(t)
		require.NoError(t, f.svc.RequestPasswordReset(password.Email))
		assert.Equal(t, int64(1), f.count(t, &models.PasswordResetToken{}, "user_id = ? AND used_at IS NULL", password.ID))
		messages := f.mailer.messages()
		require.Len(t, messages, 1)
		assert.Equal(t, password.Email, messages[0].To)
		parsed, err := url.Parse(messages[0].Body)
		require.NoError(t, err)
		assert.NotEmpty(t, parsed.Query().Get("token"))
	})

	t.Run("confirm_refused", func(t *testing.T) {
		provisioned, _ := f.seed(t)
		f.preseedToken(t, provisioned.ID, "provisioned-raw-token")
		before := f.user(t, provisioned.ID)

		err := f.svc.ConfirmPasswordReset("provisioned-raw-token", resetNewPassword)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidResetToken), "got %v", err)

		after := f.user(t, provisioned.ID)
		assert.Equal(t, int64(1), f.count(t, &models.PasswordResetToken{}, "user_id = ? AND used_at IS NULL", provisioned.ID),
			"the token stays unspent")
		assert.Equal(t, before.Password, after.Password, "the stored password hash is unchanged")
		assert.Equal(t, before.TokenVersion, after.TokenVersion, "token_version is unchanged")
		assert.Error(t, bcrypt.CompareHashAndPassword([]byte(after.Password), []byte(resetNewPassword)),
			"the new password must not verify")
		assert.Equal(t, int64(1), f.count(t, &models.RefreshToken{}, "user_id = ? AND is_revoked = ?", provisioned.ID, false),
			"the refresh token stays unrevoked")
	})

	t.Run("confirm_control", func(t *testing.T) {
		_, password := f.seed(t)
		f.preseedToken(t, password.ID, "password-raw-token")
		before := f.user(t, password.ID)

		require.NoError(t, f.svc.ConfirmPasswordReset("password-raw-token", resetNewPassword))

		after := f.user(t, password.ID)
		assert.Zero(t, f.count(t, &models.PasswordResetToken{}, "user_id = ? AND used_at IS NULL", password.ID),
			"the token is spent")
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(after.Password), []byte(resetNewPassword)))
		assert.Equal(t, before.TokenVersion+1, after.TokenVersion)
	})
}
