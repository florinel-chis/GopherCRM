package repository

import (
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupResetTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.PasswordResetToken{}))
	return db
}

func TestPasswordResetTokenRepository_CreateAndGet(t *testing.T) {
	db := setupResetTokenDB(t)
	repo := NewPasswordResetTokenRepository(db)

	token := &models.PasswordResetToken{
		UserID:    1,
		TokenHash: "hash-1",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.Create(token))

	got, err := repo.GetByTokenHash("hash-1")
	assert.NoError(t, err)
	assert.Equal(t, token.ID, got.ID)
	assert.Nil(t, got.UsedAt)
}

func TestPasswordResetTokenRepository_GetByTokenHash_ExcludesExpired(t *testing.T) {
	db := setupResetTokenDB(t)
	repo := NewPasswordResetTokenRepository(db)

	require.NoError(t, repo.Create(&models.PasswordResetToken{
		UserID:    1,
		TokenHash: "hash-expired",
		ExpiresAt: time.Now().Add(-time.Second),
	}))

	got, err := repo.GetByTokenHash("hash-expired")
	assert.Nil(t, got)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestPasswordResetTokenRepository_MarkUsed_EnforcesSingleUse(t *testing.T) {
	db := setupResetTokenDB(t)
	repo := NewPasswordResetTokenRepository(db)

	token := &models.PasswordResetToken{
		UserID:    1,
		TokenHash: "hash-once",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.Create(token))

	// First spend succeeds.
	got, err := repo.GetByTokenHash("hash-once")
	require.NoError(t, err)
	require.NoError(t, repo.MarkUsed(got.ID))

	// Second lookup finds nothing: the token is single-use.
	again, err := repo.GetByTokenHash("hash-once")
	assert.Nil(t, again)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestPasswordResetTokenRepository_InvalidateAllForUser(t *testing.T) {
	db := setupResetTokenDB(t)
	repo := NewPasswordResetTokenRepository(db)

	require.NoError(t, repo.Create(&models.PasswordResetToken{
		UserID: 1, TokenHash: "u1-old", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.Create(&models.PasswordResetToken{
		UserID: 2, TokenHash: "u2-live", ExpiresAt: time.Now().Add(time.Hour),
	}))

	require.NoError(t, repo.InvalidateAllForUser(1))

	_, err := repo.GetByTokenHash("u1-old")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	got, err := repo.GetByTokenHash("u2-live")
	assert.NoError(t, err)
	assert.Equal(t, uint(2), got.UserID)
}

func TestPasswordResetTokenRepository_DeleteExpired(t *testing.T) {
	db := setupResetTokenDB(t)
	repo := NewPasswordResetTokenRepository(db)

	require.NoError(t, repo.Create(&models.PasswordResetToken{
		UserID: 1, TokenHash: "stale", ExpiresAt: time.Now().Add(-time.Hour),
	}))
	require.NoError(t, repo.Create(&models.PasswordResetToken{
		UserID: 1, TokenHash: "fresh", ExpiresAt: time.Now().Add(time.Hour),
	}))

	require.NoError(t, repo.DeleteExpired())

	var count int64
	require.NoError(t, db.Unscoped().Model(&models.PasswordResetToken{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
