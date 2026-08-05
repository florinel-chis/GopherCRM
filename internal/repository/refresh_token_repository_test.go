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

func setupRefreshTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.RefreshToken{}))
	return db
}

func TestRefreshTokenRepository_GetByTokenHash_ReturnsLiveToken(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	token := &models.RefreshToken{
		UserID:    1,
		TokenHash: "hash-live",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.Create(token))

	got, err := repo.GetByTokenHash("hash-live")
	assert.NoError(t, err)
	assert.Equal(t, token.ID, got.ID)
}

func TestRefreshTokenRepository_GetByTokenHash_ExcludesRevoked(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	require.NoError(t, repo.Create(&models.RefreshToken{
		UserID:    1,
		TokenHash: "hash-revoked",
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.RevokeByTokenHash("hash-revoked"))

	got, err := repo.GetByTokenHash("hash-revoked")
	assert.Nil(t, got)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound,
		"a revoked token must be dead: rotation depends on this")
}

func TestRefreshTokenRepository_GetByTokenHash_ExcludesExpired(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	require.NoError(t, repo.Create(&models.RefreshToken{
		UserID:    1,
		TokenHash: "hash-expired",
		ExpiresAt: time.Now().Add(-time.Minute),
	}))

	got, err := repo.GetByTokenHash("hash-expired")
	assert.Nil(t, got)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestRefreshTokenRepository_RevokeAllForUser_LeavesOtherUsersAlone(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	require.NoError(t, repo.Create(&models.RefreshToken{
		UserID: 1, TokenHash: "u1-a", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.Create(&models.RefreshToken{
		UserID: 1, TokenHash: "u1-b", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, repo.Create(&models.RefreshToken{
		UserID: 2, TokenHash: "u2-a", ExpiresAt: time.Now().Add(time.Hour),
	}))

	require.NoError(t, repo.RevokeAllForUser(1))

	_, err := repo.GetByTokenHash("u1-a")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = repo.GetByTokenHash("u1-b")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	got, err := repo.GetByTokenHash("u2-a")
	assert.NoError(t, err)
	assert.Equal(t, uint(2), got.UserID)
}
