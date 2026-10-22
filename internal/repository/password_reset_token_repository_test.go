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

// The claim is conditional: it stamps a token only while it is unused and
// strictly unexpired, and it succeeds only when exactly one row was stamped. A
// second claim of the same token, a claim of an expired one and a claim of an
// unknown id are all not-found, and none of them writes anything.
func TestPasswordResetTokenRepository_MarkUsed_ClaimsOnlySpendableTokens(t *testing.T) {
	db := setupResetTokenDB(t)
	repo := NewPasswordResetTokenRepository(db)

	// Each read goes into a fresh struct: GORM adds a populated primary key
	// on the destination as a condition, so reusing one across ids would
	// look for a row matching both.
	loadToken := func(id uint) models.PasswordResetToken {
		var stored models.PasswordResetToken
		require.NoError(t, db.First(&stored, id).Error)
		return stored
	}

	live := &models.PasswordResetToken{UserID: 1, TokenHash: "hash-live", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, repo.Create(live))

	require.NoError(t, repo.MarkUsed(live.ID))
	claimed := loadToken(live.ID)
	require.NotNil(t, claimed.UsedAt, "the row is kept, only stamped")
	firstStamp := *claimed.UsedAt

	assert.ErrorIs(t, repo.MarkUsed(live.ID), gorm.ErrRecordNotFound, "a token can be claimed exactly once")
	reclaimed := loadToken(live.ID)
	require.NotNil(t, reclaimed.UsedAt)
	assert.True(t, reclaimed.UsedAt.Equal(firstStamp), "a refused claim does not restamp the row")

	expired := &models.PasswordResetToken{UserID: 1, TokenHash: "hash-expired", ExpiresAt: time.Now().Add(-time.Second)}
	require.NoError(t, repo.Create(expired))
	assert.ErrorIs(t, repo.MarkUsed(expired.ID), gorm.ErrRecordNotFound,
		"an expired token cannot be claimed even though it is unused")
	assert.Nil(t, loadToken(expired.ID).UsedAt, "a refused claim leaves the expired token untouched")

	assert.ErrorIs(t, repo.MarkUsed(424242), gorm.ErrRecordNotFound)
}

// A claim rolled back with its transaction leaves the token spendable: the
// stamp is part of the unit of work, not a separate write.
func TestPasswordResetTokenRepository_MarkUsed_RollsBackWithTheTransaction(t *testing.T) {
	db := setupResetTokenDB(t)
	repo := NewPasswordResetTokenRepository(db)

	token := &models.PasswordResetToken{UserID: 1, TokenHash: "hash-live", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, repo.Create(token))

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := repo.WithTx(tx).MarkUsed(token.ID); err != nil {
			return err
		}
		_, err := repo.WithTx(tx).GetByTokenHash("hash-live")
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "inside the transaction the token is spent")
		return errors.New("something after the claim failed")
	})
	require.Error(t, err)

	found, err := repo.GetByTokenHash("hash-live")
	require.NoError(t, err, "after the rollback the token is spendable again")
	assert.Equal(t, token.ID, found.ID)
	require.NoError(t, repo.MarkUsed(token.ID))
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
