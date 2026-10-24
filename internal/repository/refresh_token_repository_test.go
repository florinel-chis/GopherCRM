package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// The rotation claim is conditional: it revokes a token only while it is
// live and strictly unexpired, and it succeeds only when exactly one row was
// affected. A second claim of the same token, a claim of an expired one, of
// an already revoked one and of an unknown id are all not-found, and none of
// them writes anything.
func TestRefreshTokenRepository_ClaimForRotation_ClaimsOnlyLiveTokens(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	// Each read goes into a fresh struct: GORM adds a populated primary key
	// on the destination as a condition, so reusing one across ids would
	// look for a row matching both.
	loadToken := func(id uint) models.RefreshToken {
		var stored models.RefreshToken
		require.NoError(t, db.First(&stored, id).Error)
		return stored
	}
	countRows := func() int64 {
		var n int64
		require.NoError(t, db.Model(&models.RefreshToken{}).Count(&n).Error)
		return n
	}

	live := &models.RefreshToken{UserID: 1, TokenHash: "hash-live", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, repo.Create(live))

	require.NoError(t, repo.ClaimForRotation(live.ID))
	assert.True(t, loadToken(live.ID).Revoked, "the row is kept, only revoked")
	_, err := repo.GetByTokenHash("hash-live")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "a claimed token is no longer live")

	assert.ErrorIs(t, repo.ClaimForRotation(live.ID), gorm.ErrRecordNotFound, "a token can be claimed exactly once")

	expired := &models.RefreshToken{UserID: 1, TokenHash: "hash-expired", ExpiresAt: time.Now().Add(-time.Second)}
	require.NoError(t, repo.Create(expired))
	assert.ErrorIs(t, repo.ClaimForRotation(expired.ID), gorm.ErrRecordNotFound,
		"an expired token cannot be claimed even though it is not revoked")
	assert.False(t, loadToken(expired.ID).Revoked, "a refused claim leaves the expired token untouched")

	loggedOut := &models.RefreshToken{UserID: 1, TokenHash: "hash-logged-out", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, repo.Create(loggedOut))
	require.NoError(t, repo.RevokeByTokenHash("hash-logged-out"))
	assert.ErrorIs(t, repo.ClaimForRotation(loggedOut.ID), gorm.ErrRecordNotFound,
		"a token revoked by logout cannot be claimed for rotation")

	assert.ErrorIs(t, repo.ClaimForRotation(424242), gorm.ErrRecordNotFound)
	assert.Equal(t, int64(3), countRows(), "claims never add rows")
}

// A claim rolled back with its transaction leaves the token live: the
// revocation is part of the unit of work, not a separate write.
func TestRefreshTokenRepository_ClaimForRotation_RollsBackWithTheTransaction(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	token := &models.RefreshToken{UserID: 1, TokenHash: "hash-live", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, repo.Create(token))

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := repo.WithTx(tx).ClaimForRotation(token.ID); err != nil {
			return err
		}
		_, err := repo.WithTx(tx).GetByTokenHash("hash-live")
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "inside the transaction the token is spent")
		return errors.New("something after the claim failed")
	})
	require.Error(t, err)

	found, err := repo.GetByTokenHash("hash-live")
	require.NoError(t, err, "after the rollback the token is live again")
	assert.Equal(t, token.ID, found.ID)
	require.NoError(t, repo.ClaimForRotation(token.ID))
}

// Logout keeps its unconditional, idempotent revocation: revoking an already
// revoked, an expired or an unknown token is a success, and it never claims
// anything a rotation could later succeed on.
func TestRefreshTokenRepository_RevokeByTokenHash_IsUnconditionalAndIdempotent(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	live := &models.RefreshToken{UserID: 1, TokenHash: "hash-live", ExpiresAt: time.Now().Add(time.Hour)}
	expired := &models.RefreshToken{UserID: 1, TokenHash: "hash-expired", ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, repo.Create(live))
	require.NoError(t, repo.Create(expired))

	require.NoError(t, repo.RevokeByTokenHash("hash-live"))
	require.NoError(t, repo.RevokeByTokenHash("hash-live"), "revoking twice is not an error")
	require.NoError(t, repo.RevokeByTokenHash("hash-expired"), "an expired token can still be revoked")
	require.NoError(t, repo.RevokeByTokenHash("hash-unknown"), "an unknown token is ignored")

	var stored models.RefreshToken
	require.NoError(t, db.First(&stored, live.ID).Error)
	assert.True(t, stored.Revoked)
	assert.ErrorIs(t, repo.ClaimForRotation(live.ID), gorm.ErrRecordNotFound,
		"a logged-out token cannot be rotated afterwards")
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
