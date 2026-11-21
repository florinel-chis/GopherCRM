package repository

import (
	"context"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bulkOperationRepoDB is a private in-memory database, foreign keys off, with
// the driver's default pool.
var bulkOperationRepoDB = testsupport.Group(
	testsupport.WithoutForeignKeys(),
	testsupport.DriverDefaultMaxOpenConns(),
	testsupport.Models(
		&models.User{},
		&models.BulkOperation{},
		&models.BulkOperationItem{},
	),
)

func TestBulkOperationRepository_CreateAndUpdateStatus(t *testing.T) {
	db := testsupport.OpenSQLite(t, bulkOperationRepoDB)
	repo := NewBulkOperationRepository(db)

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
	require.NoError(t, repo.Create(context.Background(), op))
	assert.NotZero(t, op.ID)

	var fetched models.BulkOperation
	require.NoError(t, db.First(&fetched, op.ID).Error)
	assert.Equal(t, user.ID, fetched.UserID)
	assert.Equal(t, "leads", fetched.ResourceType)
	assert.Equal(t, models.BulkAction, fetched.Type)
	assert.Equal(t, models.StatusPending, fetched.Status)
	assert.Equal(t, 5, fetched.TotalItems)

	require.NoError(t, repo.UpdateStatus(context.Background(), op.ID, models.StatusCompleted))
	var updated models.BulkOperation
	require.NoError(t, db.First(&updated, op.ID).Error)
	assert.Equal(t, models.StatusCompleted, updated.Status)

	// UpdateStatus touches only the named row.
	other := &models.BulkOperation{
		UserID: user.ID, ResourceType: "tasks", Type: models.BulkAction,
		Status: models.StatusPending, TotalItems: 1,
	}
	require.NoError(t, repo.Create(context.Background(), other))
	require.NoError(t, repo.UpdateStatus(context.Background(), other.ID, models.StatusFailed))
	var first, second models.BulkOperation
	require.NoError(t, db.First(&first, op.ID).Error)
	assert.Equal(t, models.StatusCompleted, first.Status)
	require.NoError(t, db.First(&second, other.ID).Error)
	assert.Equal(t, models.StatusFailed, second.Status)
}
