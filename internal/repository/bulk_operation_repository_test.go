package repository

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func setupBulkOperationRepoDB(t *testing.T) (*gorm.DB, BulkOperationRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.BulkOperation{},
		&models.BulkOperationItem{},
	))

	repo := NewBulkOperationRepository(db)
	return db, repo
}

func TestBulkOperationRepository_CreateAndGet(t *testing.T) {
	db, repo := setupBulkOperationRepoDB(t)

	user := &models.User{
		Email:     "bulk-admin@example.com",
		Password:  "hashed",
		FirstName: "Admin",
		LastName:  "User",
		Role:      models.RoleAdmin,
		IsActive:  true,
	}
	require.NoError(t, db.Create(user).Error)

	op := &models.BulkOperation{
		UserID:       user.ID,
		ResourceType: "leads",
		Type:         models.BulkAction,
		Status:       models.StatusPending,
		TotalItems:   5,
	}
	require.NoError(t, repo.Create(op))
	assert.NotZero(t, op.ID)

	// GetByID
	fetched, err := repo.GetByID(op.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, fetched.UserID)
	assert.Equal(t, "leads", fetched.ResourceType)
	assert.Equal(t, models.BulkAction, fetched.Type)
	assert.Equal(t, models.StatusPending, fetched.Status)

	// UpdateStatus
	require.NoError(t, repo.UpdateStatus(op.ID, models.StatusCompleted))
	fetched, err = repo.GetByID(op.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, fetched.Status)

	// Count & CountByUserID
	total, err := repo.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)

	userCount, err := repo.CountByUserID(user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), userCount)

	// List & GetByUserID
	list, err := repo.List(0, 10)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	userList, err := repo.GetByUserID(user.ID, 0, 10)
	require.NoError(t, err)
	assert.Len(t, userList, 1)

	// Delete
	require.NoError(t, repo.Delete(op.ID))
	_, err = repo.GetByID(op.ID)
	assert.Error(t, err)
}

func TestBulkOperationRepository_ItemsOperations(t *testing.T) {
	db, repo := setupBulkOperationRepoDB(t)

	user := &models.User{
		Email:     "items-user@example.com",
		Password:  "hashed",
		FirstName: "Items",
		LastName:  "User",
		Role:      models.RoleAdmin,
		IsActive:  true,
	}
	require.NoError(t, db.Create(user).Error)

	op := &models.BulkOperation{
		UserID:       user.ID,
		ResourceType: "tasks",
		Type:         models.BulkUpdate,
		Status:       models.StatusPending,
		TotalItems:   2,
	}
	require.NoError(t, repo.Create(op))

	item1 := &models.BulkOperationItem{
		OperationID: op.ID,
		ResourceID:  101,
		Status:      models.StatusCompleted,
		Data:        `{"title":"Task 1"}`,
	}
	item2 := &models.BulkOperationItem{
		OperationID: op.ID,
		ResourceID:  102,
		Status:      models.StatusFailed,
		Error:       "validation failed",
	}

	require.NoError(t, repo.CreateItem(item1))
	require.NoError(t, repo.CreateItem(item2))

	// GetItemsByOperationID specifically verifies the operation_id column query
	items, err := repo.GetItemsByOperationID(op.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, uint(101), items[0].ResourceID)
	assert.Equal(t, uint(102), items[1].ResourceID)

	// GetByIDWithItems preloads items
	withItems, err := repo.GetByIDWithItems(op.ID)
	require.NoError(t, err)
	assert.Len(t, withItems.Items, 2)

	// UpdateItem
	item2.Status = models.StatusCompleted
	item2.Error = ""
	require.NoError(t, repo.UpdateItem(item2))

	reloaded, err := repo.GetItemsByOperationID(op.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, reloaded[1].Status)
	assert.Empty(t, reloaded[1].Error)
}
