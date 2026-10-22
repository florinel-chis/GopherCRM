package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestPasswordResetOwnerDeactivatedAfterTokenReadMySQL reproduces the stale
// snapshot that only a second connection can create: under REPEATABLE READ
// the transaction's snapshot is taken at the token lookup, a separate
// connection then commits the deactivation, and a plain owner read would
// still see the account as active and the password update would write that
// stale row back. The owner read must be a locking (current) read. There is
// no SQLite twin: its one-connection pool cannot interleave a committed
// write; the transaction-handle scenarios in the validation roots cover the
// owner check there.
func TestPasswordResetOwnerDeactivatedAfterTokenReadMySQL(t *testing.T) {
	db := openFormTestMySQL(t, 2)
	f := newFormFixtureOn(t, config.FormsConfig{PublicBaseURL: formTestPublicBase}, db)
	require.NoError(t, db.AutoMigrate(&models.PasswordResetToken{}, &models.RefreshToken{}))
	require.NoError(t, db.Exec("SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ").Error)
	raw := "reset-owner-race-regression"
	token := models.PasswordResetToken{UserID: f.owner.ID, TokenHash: hashOpaqueToken(raw, formTestSecret), ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, db.Create(&token).Error)
	svc := NewAuthServiceWithSessions(f.userRepo, repository.NewAPIKeyRepository(db), repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db), utils.NewTransactionManager(db), f.mailer, config.JWTConfig{Secret: formTestSecret}, "https://example.test", formTestSecret)
	var changed atomic.Bool
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("probe:disable-after-token-read", func(tx *gorm.DB) {
		if tx.Statement.Table == "password_reset_tokens" && changed.CompareAndSwap(false, true) {
			// A separate connection commits the account change after the token
			// lookup, before reset reads its owner. A stale transaction snapshot
			// must never reactivate this account by saving the old user record.
			if err := db.Model(&models.User{}).Where("id = ?", f.owner.ID).Update("is_active", false).Error; err != nil {
				tx.AddError(err)
			}
		}
	}))
	err := svc.ConfirmPasswordReset(raw, "BrandNewPassword1!")
	require.NoError(t, db.Callback().Query().Remove("probe:disable-after-token-read"))
	require.True(t, changed.Load(), "probe must interleave deactivation after token lookup")
	assert.ErrorIs(t, err, ErrInvalidResetToken)
	var owner models.User
	require.NoError(t, db.First(&owner, f.owner.ID).Error)
	assert.False(t, owner.IsActive, "reset must not reactivate an account disabled after its token lookup")
	assert.Equal(t, f.owner.Password, owner.Password, "a disabled owner's password must remain unchanged")
	var after models.PasswordResetToken
	require.NoError(t, db.First(&after, token.ID).Error)
	assert.Nil(t, after.UsedAt, "rejection must leave the token unspent")
}
