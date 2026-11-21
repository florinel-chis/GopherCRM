package repository

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// #236: users.token_version is written through UpdateFields, the only user
// write path, with the same `WHERE id = ? AND deleted_at IS NULL` guard as
// every other column, and read back by GetByID. These roots run on a
// file-backed SQLite database with one connection and, when TEST_MYSQL_DSN is
// set, on a disposable MySQL schema; the MySQL root skips without the variable
// and fails when it is unreachable.

func TestUserRepositoryTokenVersionSQLite(t *testing.T) {
	runUserRepositoryTokenVersion(t, testsupport.Open(t, testsupport.SQLite, "user_token_version", userTokenVersionDB))
}

func TestUserRepositoryTokenVersionMySQL(t *testing.T) {
	runUserRepositoryTokenVersion(t, testsupport.Open(t, testsupport.MySQL, "user_token_version", userTokenVersionDB))
}

// userTokenVersionDB is a file-backed SQLite database with one connection, or a uniquely
// named disposable schema on the TEST_MYSQL_DSN server.
var userTokenVersionDB = testsupport.Group(
	testsupport.OnSQLite(testsupport.File("user_token_version.db", testsupport.BusyTimeout)),
	testsupport.Models(&models.User{}),
)

// statementCapture records the SQL of every UPDATE the handle runs.
type statementCapture struct {
	mu      sync.Mutex
	updates []string
}

func (c *statementCapture) install(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:capture_token_version_update", func(tx *gorm.DB) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.updates = append(c.updates, tx.Statement.SQL.String())
	}))
}

func (c *statementCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = nil
}

func (c *statementCapture) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.updates...)
}

func runUserRepositoryTokenVersion(t *testing.T, db *gorm.DB) {
	capture := &statementCapture{}
	capture.install(t, db)
	repo := NewUserRepository(db)

	create := func(t *testing.T, email string) *models.User {
		t.Helper()
		user := &models.User{Email: email, Password: "hash", FirstName: "Token", LastName: "Version",
			Role: models.RoleSales, IsActive: true}
		require.NoError(t, repo.Create(user))
		return user
	}
	stored := func(t *testing.T, id uint) uint {
		t.Helper()
		var row models.User
		require.NoError(t, db.Unscoped().First(&row, id).Error)
		return row.TokenVersion
	}

	t.Run("a new account starts at 0 and GetByID returns the stored value", func(t *testing.T) {
		user := create(t, "fresh@example.com")
		got, err := repo.GetByID(user.ID)
		require.NoError(t, err)
		assert.Equal(t, uint(0), got.TokenVersion)

		require.NoError(t, db.Model(&models.User{}).Where("id = ?", user.ID).Update("token_version", 41).Error)
		got, err = repo.GetByID(user.ID)
		require.NoError(t, err)
		assert.Equal(t, uint(41), got.TokenVersion)
	})

	t.Run("UpdateFields writes token_version on a live row with the live-row guard", func(t *testing.T) {
		user := create(t, "live@example.com")
		bystander := create(t, "bystander@example.com")
		capture.reset()

		require.NoError(t, repo.UpdateFields(user.ID, map[string]interface{}{"token_version": uint(1)}))
		assert.Equal(t, uint(1), stored(t, user.ID))
		assert.Equal(t, uint(0), stored(t, bystander.ID), "only the named row is written")

		updates := capture.all()
		require.NotEmpty(t, updates)
		statement := strings.ToLower(updates[0])
		assert.Contains(t, statement, "token_version")
		assert.Contains(t, statement, "deleted_at is null", "the write is limited to a live row")
		assert.Regexp(t, "id = \\?", statement)

		// Together with the password, as the password change and reset write it.
		require.NoError(t, repo.UpdateFields(user.ID, map[string]interface{}{
			"password": "new-hash", "token_version": uint(2),
		}))
		got, err := repo.GetByID(user.ID)
		require.NoError(t, err)
		assert.Equal(t, uint(2), got.TokenVersion)
		assert.Equal(t, "new-hash", got.Password)

		// Writing the value the row already holds is not a missing row, even
		// where MySQL reports zero changed rows.
		require.NoError(t, repo.UpdateFields(user.ID, map[string]interface{}{"token_version": uint(2)}))
		assert.Equal(t, uint(2), stored(t, user.ID))
	})

	t.Run("the transaction-bound locking repository writes and reads it too", func(t *testing.T) {
		user := create(t, "locked@example.com")
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			locked := repo.WithTx(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{}))
			owner, err := locked.GetByID(user.ID)
			if err != nil {
				return err
			}
			return locked.UpdateFields(user.ID, map[string]interface{}{"token_version": owner.TokenVersion + 1})
		}))
		assert.Equal(t, uint(1), stored(t, user.ID))

		injected := errors.New("rolled back")
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := repo.WithTx(tx).UpdateFields(user.ID, map[string]interface{}{"token_version": uint(9)}); err != nil {
				return err
			}
			return injected
		})
		require.ErrorIs(t, err, injected)
		assert.Equal(t, uint(1), stored(t, user.ID), "a rolled-back write leaves the stored value")
	})

	t.Run("an erased row is not found and keeps its value", func(t *testing.T) {
		user := create(t, "erased@example.com")
		require.NoError(t, repo.UpdateFields(user.ID, map[string]interface{}{"token_version": uint(5)}))
		require.NoError(t, db.Delete(&models.User{}, user.ID).Error)

		err := repo.UpdateFields(user.ID, map[string]interface{}{"token_version": uint(6)})
		require.Error(t, err)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "got %v", err)
		assert.Equal(t, uint(5), stored(t, user.ID), "nothing is written to an erased row")

		var row models.User
		require.NoError(t, db.Unscoped().First(&row, user.ID).Error)
		assert.True(t, row.DeletedAt.Valid, "the row stays erased")

		_, err = repo.GetByID(user.ID)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "GetByID is scoped")
	})

	t.Run("a missing row is not found", func(t *testing.T) {
		err := repo.UpdateFields(999999, map[string]interface{}{"token_version": uint(1)})
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("columns outside the allowlist are still refused", func(t *testing.T) {
		user := create(t, "allowlist@example.com")
		err := repo.UpdateFields(user.ID, map[string]interface{}{"token_version": uint(1), "deleted_at": time.Now()})
		require.Error(t, err)
		assert.Equal(t, uint(0), stored(t, user.ID))
	})
}
