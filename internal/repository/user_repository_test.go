package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

func setupUserRepoDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.User{}))
	return db
}

func createUserRepoTestUser(t *testing.T, db *gorm.DB, email string) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "hash-v1", FirstName: "First", LastName: "Last",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(user).Error)
	return user
}

// UpdateFields writes the named columns of a live row and nothing else, and
// never inserts: GORM's Save upserts when its UPDATE matches no row, which is
// how a stale struct used to resurrect an erased account.
func TestUserRepository_UpdateFields(t *testing.T) {
	t.Run("writes only the named columns", func(t *testing.T) {
		db := setupUserRepoDB(t)
		repo := NewUserRepository(db)
		user := createUserRepoTestUser(t, db, "fields@example.com")
		// A concurrent writer changed the password after our read.
		require.NoError(t, db.Model(&models.User{}).Where("id = ?", user.ID).Update("password", "hash-v2").Error)

		until := time.Now().Add(time.Hour)
		require.NoError(t, repo.UpdateFields(user.ID, map[string]interface{}{
			"failed_login_attempts": 3, "locked_until": &until,
		}))

		var stored models.User
		require.NoError(t, db.First(&stored, user.ID).Error)
		assert.Equal(t, 3, stored.FailedLoginAttempts)
		assert.NotNil(t, stored.LockedUntil)
		assert.Equal(t, "hash-v2", stored.Password, "an unnamed column is never written back")
	})

	t.Run("a soft-deleted row is not found and not resurrected", func(t *testing.T) {
		db := setupUserRepoDB(t)
		repo := NewUserRepository(db)
		user := createUserRepoTestUser(t, db, "erased@example.com")
		require.NoError(t, db.Delete(&models.User{}, user.ID).Error)

		err := repo.UpdateFields(user.ID, map[string]interface{}{"first_name": "Back"})
		assert.True(t, apperrors.IsNotFound(err), "got %v", err)

		var stored models.User
		require.NoError(t, db.Unscoped().First(&stored, user.ID).Error)
		assert.True(t, stored.DeletedAt.Valid, "the row must stay soft-deleted")
		assert.Equal(t, "First", stored.FirstName)
		var rows int64
		require.NoError(t, db.Unscoped().Model(&models.User{}).Count(&rows).Error)
		assert.Equal(t, int64(1), rows, "nothing may be inserted")
	})

	t.Run("a missing row is not found and nothing is inserted", func(t *testing.T) {
		db := setupUserRepoDB(t)
		repo := NewUserRepository(db)

		err := repo.UpdateFields(4242, map[string]interface{}{"first_name": "Ghost"})
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
		var rows int64
		require.NoError(t, db.Unscoped().Model(&models.User{}).Count(&rows).Error)
		assert.Zero(t, rows)
	})

	t.Run("an unlisted column is refused", func(t *testing.T) {
		db := setupUserRepoDB(t)
		repo := NewUserRepository(db)
		user := createUserRepoTestUser(t, db, "allowlist@example.com")

		assert.Error(t, repo.UpdateFields(user.ID, map[string]interface{}{"deleted_at": nil}))
		assert.Error(t, repo.UpdateFields(user.ID, map[string]interface{}{"id": 99}))
	})

	t.Run("a duplicate email maps to the sentinel", func(t *testing.T) {
		db := setupUserRepoDB(t)
		repo := NewUserRepository(db)
		createUserRepoTestUser(t, db, "taken@example.com")
		user := createUserRepoTestUser(t, db, "mine@example.com")

		err := repo.UpdateFields(user.ID, map[string]interface{}{"email": "taken@example.com"})
		assert.ErrorIs(t, err, apperrors.ErrDuplicateEmail)
	})

	t.Run("UpdateLastLogin refuses an erased row", func(t *testing.T) {
		db := setupUserRepoDB(t)
		repo := NewUserRepository(db)
		user := createUserRepoTestUser(t, db, "lastlogin@example.com")
		require.NoError(t, repo.UpdateLastLogin(user.ID))
		require.NoError(t, db.Delete(&models.User{}, user.ID).Error)

		assert.True(t, apperrors.IsNotFound(repo.UpdateLastLogin(user.ID)))
	})
}

// A login email change through UpdateFields spends the user's unused password
// reset tokens in the same write; nothing else does.
func TestUpdateFieldsEmailSpendsResetTokens(t *testing.T) {
	setup := func(t *testing.T) (*gorm.DB, *models.User, *models.User) {
		t.Helper()
		db := setupUserRepoDB(t)
		require.NoError(t, db.AutoMigrate(&models.PasswordResetToken{}))
		owner := createUserRepoTestUser(t, db, "reset-owner@example.com")
		other := createUserRepoTestUser(t, db, "reset-other@example.com")
		for i, u := range []*models.User{owner, other} {
			require.NoError(t, db.Create(&models.PasswordResetToken{UserID: u.ID,
				TokenHash: []string{"hash-owner", "hash-other"}[i], ExpiresAt: time.Now().Add(time.Hour)}).Error)
		}
		return db, owner, other
	}
	unused := func(t *testing.T, db *gorm.DB, userID uint) bool {
		t.Helper()
		var token models.PasswordResetToken
		require.NoError(t, db.Where("user_id = ?", userID).First(&token).Error)
		return token.UsedAt == nil
	}

	t.Run("email_change_spends_own_tokens", func(t *testing.T) {
		db, owner, other := setup(t)
		require.NoError(t, NewUserRepository(db).UpdateFields(owner.ID, map[string]interface{}{"email": "reset-owner-new@example.com"}))
		assert.False(t, unused(t, db, owner.ID), "the owner's link is spent")
		assert.True(t, unused(t, db, other.ID), "another user's link is untouched")
	})

	t.Run("other_fields_keep_tokens", func(t *testing.T) {
		db, owner, _ := setup(t)
		require.NoError(t, NewUserRepository(db).UpdateFields(owner.ID, map[string]interface{}{"first_name": "Renamed"}))
		assert.True(t, unused(t, db, owner.ID))
	})

	t.Run("duplicate_email_keeps_tokens", func(t *testing.T) {
		db, owner, other := setup(t)
		err := NewUserRepository(db).UpdateFields(owner.ID, map[string]interface{}{"email": other.Email})
		require.ErrorIs(t, err, apperrors.ErrDuplicateEmail)
		assert.True(t, unused(t, db, owner.ID))
	})

	t.Run("inside_transaction_rolls_back_with_caller", func(t *testing.T) {
		db, owner, _ := setup(t)
		callerFailed := errors.New("caller failed after the email write")
		err := db.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, NewUserRepository(db).WithTx(tx).UpdateFields(owner.ID, map[string]interface{}{"email": "reset-owner-tx@example.com"}))
			return callerFailed
		})
		require.ErrorIs(t, err, callerFailed)
		assert.True(t, unused(t, db, owner.ID), "the caller's rollback keeps the link")
		var reread models.User
		require.NoError(t, db.First(&reread, owner.ID).Error)
		assert.Equal(t, "reset-owner@example.com", reread.Email)
	})
}
