package service

// Regression roots for the login bookkeeping writes: a login reads the account,
// verifies the password and then records the outcome (failed attempts, lockout
// reset, last login). Those writes used to save the whole user struct read at
// the start, and GORM's Save falls back to an upsert when the scoped update
// matches no row — so a login paused across an erasure wrote the erased
// person's name, email and password hash back and cleared deleted_at. The
// writes are now conditional, column-specific updates of a live row: they
// never insert, never touch a soft-deleted row, and a login whose account
// vanished underneath it is refused.
//
// Every root runs on a real database (in-memory SQLite with the production
// one-connection pool, and a disposable MySQL schema when TEST_MYSQL_DSN is
// set) and asserts committed rows read outside any transaction.

import (
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
	erasureLoginEmail    = "erased-during-login@example.com"
	erasureLoginPassword = "LoginSecret123!"
)

// erasingUserRepository erases the account through the real repository right
// after the login has read it, which is exactly the window an erasure request
// served by another worker can hit.
type erasingUserRepository struct {
	repository.UserRepository
	erase func(id uint)
}

func (r *erasingUserRepository) GetByEmail(email string) (*models.User, error) {
	user, err := r.UserRepository.GetByEmail(email)
	if err == nil && r.erase != nil {
		r.erase(user.ID)
	}
	return user, err
}

func TestLoginErasedDuringAuthenticationSQLite(t *testing.T) {
	runLoginErasedDuringAuthentication(t, openResetTestSQLite(t))
}

func TestLoginErasedDuringAuthenticationMySQL(t *testing.T) {
	runLoginErasedDuringAuthentication(t, openResetTestMySQL(t, 2))
}

func runLoginErasedDuringAuthentication(t *testing.T, db *gorm.DB) {
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	cases := []struct {
		name           string
		password       string
		failedAttempts int
	}{
		{name: "wrong password records a failure", password: "WrongSecret123!"},
		{name: "wrong password at the lockout threshold", password: "WrongSecret123!", failedAttempts: MaxFailedLoginAttempts - 1},
		{name: "right password resets earlier failures", password: erasureLoginPassword, failedAttempts: 2},
		{name: "right password with a clean record", password: erasureLoginPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := resetContext(t)
			for _, table := range []string{"refresh_tokens", "api_keys", "users"} {
				require.NoError(t, db.WithContext(ctx).Exec("DELETE FROM "+table).Error)
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(erasureLoginPassword), bcrypt.MinCost)
			require.NoError(t, err)
			user := &models.User{
				Email: erasureLoginEmail, Password: string(hash), FirstName: "Erased", LastName: "Person",
				Role: models.RoleSales, IsActive: true, FailedLoginAttempts: tc.failedAttempts,
			}
			require.NoError(t, db.WithContext(ctx).Create(user).Error)

			realRepo := repository.NewUserRepository(db)
			erased := false
			repo := &erasingUserRepository{UserRepository: realRepo, erase: func(id uint) {
				if !erased {
					erased = true
					require.NoError(t, realRepo.Delete(id))
				}
			}}
			svc := NewAuthServiceWithSessions(repo, repository.NewAPIKeyRepository(db),
				repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db),
				utils.NewTransactionManager(db), &fakeFormMailer{},
				config.JWTConfig{Secret: resetTestSecret, ExpiryHours: 1, RefreshTokenDays: 30}, resetTestBaseURL, "")

			tokens, err := svc.LoginWithTokens(erasureLoginEmail, tc.password)
			require.True(t, erased, "the erasure must have run inside the login")
			assert.Error(t, err, "a login whose account was erased underneath it must be refused")
			assert.Nil(t, tokens)

			var row models.User
			require.NoError(t, db.WithContext(ctx).Unscoped().First(&row, user.ID).Error)
			assert.True(t, row.DeletedAt.Valid, "the erased row must stay soft-deleted")
			assert.NotEqual(t, erasureLoginEmail, row.Email, "the original address must not come back")
			assert.Empty(t, row.FirstName, "erased names must stay blank")
			assert.Empty(t, row.LastName)
			assert.False(t, row.IsActive)
			assert.Error(t, bcrypt.CompareHashAndPassword([]byte(row.Password), []byte(erasureLoginPassword)),
				"the old password hash must not be written back")
			assert.Zero(t, row.FailedLoginAttempts, "erased login bookkeeping stays erased")
			assert.Nil(t, row.LockedUntil)
			assert.Nil(t, row.LastLoginAt)

			var live int64
			require.NoError(t, db.WithContext(ctx).Model(&models.User{}).Where("email = ?", erasureLoginEmail).Count(&live).Error)
			assert.Zero(t, live, "no live row may carry the erased address")
			var sessions int64
			require.NoError(t, db.WithContext(ctx).Model(&models.RefreshToken{}).Where("user_id = ?", user.ID).Count(&sessions).Error)
			assert.Zero(t, sessions, "no session may be issued for an erased account")
		})
	}
}
