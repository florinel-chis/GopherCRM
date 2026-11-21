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

// countRefreshTokenStatements counts every query, row, raw and update
// statement the handle runs from now on.
func countRefreshTokenStatements(t *testing.T, db *gorm.DB) *int {
	t.Helper()
	count := 0
	inc := func(*gorm.DB) { count++ }
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:count_query", inc))
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:count_row", inc))
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:count_raw", inc))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:count_update", inc))
	return &count
}

func seedFamilyToken(t *testing.T, repo RefreshTokenRepository, userID uint, hash, family string, expiresIn time.Duration) *models.RefreshToken {
	t.Helper()
	token := &models.RefreshToken{UserID: userID, TokenHash: hash, FamilyID: family, ExpiresAt: time.Now().Add(expiresIn)}
	require.NoError(t, repo.Create(token))
	return token
}

func isRevoked(t *testing.T, db *gorm.DB, hash string) bool {
	t.Helper()
	var token models.RefreshToken
	require.NoError(t, db.Where("token_hash = ?", hash).First(&token).Error)
	return token.Revoked
}

// The revoked-row lookup finds a revoked token whatever its expiry and never
// a live or unknown one.
func TestRefreshTokenRepository_GetRevokedByTokenHash_FindsOnlyRevokedRows(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	revoked := seedFamilyToken(t, repo, 1, "hash-revoked", "fam", time.Hour)
	require.NoError(t, repo.RevokeByTokenHash("hash-revoked"))
	expired := seedFamilyToken(t, repo, 1, "hash-revoked-expired", "fam", -time.Hour)
	require.NoError(t, repo.RevokeByTokenHash("hash-revoked-expired"))
	seedFamilyToken(t, repo, 1, "hash-live", "fam", time.Hour)
	seedFamilyToken(t, repo, 1, "hash-live-expired", "fam", -time.Hour)

	got, err := repo.GetRevokedByTokenHash("hash-revoked")
	require.NoError(t, err)
	assert.Equal(t, revoked.ID, got.ID)
	assert.True(t, got.Revoked)
	assert.Equal(t, "fam", got.FamilyID)

	got, err = repo.GetRevokedByTokenHash("hash-revoked-expired")
	require.NoError(t, err, "a revoked token is found after its expiry")
	assert.Equal(t, expired.ID, got.ID)

	for _, hash := range []string{"hash-live", "hash-live-expired", "hash-unknown"} {
		got, err := repo.GetRevokedByTokenHash(hash)
		assert.Nil(t, got, hash)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, hash)
	}
}

// A later row exists only for the same user and family and a greater id; an
// empty family is never a family and is answered without a query.
func TestRefreshTokenRepository_HasLaterInFamily_MatchesUserFamilyAndGreaterID(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	first := seedFamilyToken(t, repo, 1, "hash-first", "fam-a", time.Hour)
	seedFamilyToken(t, repo, 2, "hash-other-user", "fam-a", time.Hour)
	seedFamilyToken(t, repo, 1, "hash-other-family", "fam-b", time.Hour)

	later, err := repo.HasLaterInFamily(first)
	require.NoError(t, err)
	assert.False(t, later, "rows of another user or another family do not count")

	second := seedFamilyToken(t, repo, 1, "hash-second", "fam-a", time.Hour)
	later, err = repo.HasLaterInFamily(first)
	require.NoError(t, err)
	assert.True(t, later, "a greater id of the same user and family counts")

	later, err = repo.HasLaterInFamily(second)
	require.NoError(t, err)
	assert.False(t, later, "the newest row of a family has no later row")

	// An expired or revoked later row still marks the earlier one rotated.
	require.NoError(t, repo.RevokeByTokenHash("hash-second"))
	later, err = repo.HasLaterInFamily(first)
	require.NoError(t, err)
	assert.True(t, later)

	unfamilied := seedFamilyToken(t, repo, 1, "hash-empty", "", time.Hour)
	seedFamilyToken(t, repo, 1, "hash-empty-later", "", time.Hour)
	statements := countRefreshTokenStatements(t, db)
	later, err = repo.HasLaterInFamily(unfamilied)
	require.NoError(t, err)
	assert.False(t, later, "a row without a family is never part of one")
	assert.Zero(t, *statements, "no query runs for an empty family")
	_, err = repo.HasLaterInFamily(first)
	require.NoError(t, err)
	assert.Positive(t, *statements, "the counter sees the query of a real family")
}

// The family revocation touches only the user's live rows of that family; an
// empty family id is refused without a statement.
func TestRefreshTokenRepository_RevokeFamily_RevokesOnlyThatUsersFamily(t *testing.T) {
	db := setupRefreshTokenDB(t)
	repo := NewRefreshTokenRepository(db)

	seedFamilyToken(t, repo, 1, "hash-a1", "fam-a", time.Hour)
	seedFamilyToken(t, repo, 1, "hash-a2", "fam-a", time.Hour)
	seedFamilyToken(t, repo, 1, "hash-a-expired", "fam-a", -time.Hour)
	seedFamilyToken(t, repo, 1, "hash-b", "fam-b", time.Hour)
	seedFamilyToken(t, repo, 2, "hash-other-user", "fam-a", time.Hour)
	seedFamilyToken(t, repo, 1, "hash-empty", "", time.Hour)

	statements := countRefreshTokenStatements(t, db)
	err := repo.RevokeFamily(1, "")
	require.Error(t, err, "an empty family id is refused")
	assert.Zero(t, *statements, "no statement runs for an empty family id")
	for _, hash := range []string{"hash-a1", "hash-a2", "hash-a-expired", "hash-b", "hash-other-user", "hash-empty"} {
		assert.False(t, isRevoked(t, db, hash), "%s is unchanged", hash)
	}

	require.NoError(t, repo.RevokeFamily(1, "fam-a"))
	assert.Positive(t, *statements, "the counter sees the revocation of a real family")
	for _, hash := range []string{"hash-a1", "hash-a2", "hash-a-expired"} {
		assert.True(t, isRevoked(t, db, hash), "%s belongs to the family", hash)
	}
	for _, hash := range []string{"hash-b", "hash-other-user", "hash-empty"} {
		assert.False(t, isRevoked(t, db, hash), "%s is outside the family", hash)
	}
	require.NoError(t, repo.RevokeFamily(1, "fam-a"), "revoking an ended family again is not an error")
}
