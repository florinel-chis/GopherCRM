package service

import (
	"errors"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gorm.io/gorm"
)

func TestAPIKeyService_Generate(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

	userID := uint(1)
	name := "Test API Key"

	mockAPIKeyRepo.On("Create", mock.Anything).Return(nil)

	key, apiKey, err := apiKeyService.Generate(userID, name, nil)

	assert.NoError(t, err)
	assert.NotEmpty(t, key)
	assert.NotNil(t, apiKey)
	assert.Equal(t, name, apiKey.Name)
	assert.Equal(t, userID, apiKey.UserID)
	assert.NotEmpty(t, apiKey.KeyHash)
	assert.NotEmpty(t, apiKey.Prefix)
	assert.True(t, len(apiKey.Prefix) == 8)
	assert.Contains(t, key, "gcrm_")
	
	mockAPIKeyRepo.AssertExpectations(t)
}

func TestAPIKeyService_Generate_CreateError(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

	userID := uint(1)
	name := "Test API Key"

	mockAPIKeyRepo.On("Create", mock.Anything).Return(errors.New("database error"))

	key, apiKey, err := apiKeyService.Generate(userID, name, nil)

	assert.Error(t, err)
	assert.Empty(t, key)
	assert.Nil(t, apiKey)
	
	mockAPIKeyRepo.AssertExpectations(t)
}

func TestAPIKeyService_GetByUser(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

	userID := uint(1)
	expectedKeys := []models.APIKey{
		{
			BaseModel: models.BaseModel{ID: 1},
			Name:      "Key 1",
			UserID:    userID,
			IsActive:  true,
		},
		{
			BaseModel: models.BaseModel{ID: 2},
			Name:      "Key 2",
			UserID:    userID,
			IsActive:  false,
		},
	}

	mockAPIKeyRepo.On("GetByUserID", userID).Return(expectedKeys, nil)

	keys, err := apiKeyService.GetByUser(userID)

	assert.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Equal(t, expectedKeys, keys)
	
	mockAPIKeyRepo.AssertExpectations(t)
}

func TestAPIKeyService_Revoke(t *testing.T) {
	t.Run("successful revocation", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(1)
		
		apiKey := &models.APIKey{
			BaseModel: models.BaseModel{ID: apiKeyID},
			UserID:    userID,
			IsActive:  true,
		}

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(apiKey, nil)
		mockAPIKeyRepo.On("Update", mock.MatchedBy(func(k *models.APIKey) bool {
			return k.ID == apiKeyID && k.IsActive == false
		})).Return(nil)

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.NoError(t, err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("unauthorized revocation", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(2) // Different user
		
		apiKey := &models.APIKey{
			BaseModel: models.BaseModel{ID: apiKeyID},
			UserID:    1, // Different from requesting user
			IsActive:  true,
		}

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(apiKey, nil)

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrForbidden),
			"ownership violation must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	// The repository hands gorm's sentinel back unwrapped; the service is the
	// layer that must translate it into something a handler can classify.
	t.Run("api key not found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(1)

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(nil, gorm.ErrRecordNotFound)

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound),
			"missing key must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	// A lookup that failed for any other reason is an infrastructure failure and
	// must not be laundered into a not-found.
	t.Run("lookup failure is not a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(1)

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(nil, errors.New("connection refused"))

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.Error(t, err)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		assert.False(t, errors.Is(err, apperrors.ErrForbidden))
		mockAPIKeyRepo.AssertExpectations(t)
	})
}

func TestAPIKeyService_GetByID(t *testing.T) {
	t.Run("owner gets their own key", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			Name:      "Production Key",
			UserID:    7,
			IsActive:  true,
			// The repository preloads the owner; the service must not leak it.
			User: models.User{BaseModel: models.BaseModel{ID: 7}, Email: "owner@example.com"},
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)

		key, err := apiKeyService.GetByID(3, 7)

		assert.NoError(t, err)
		assert.NotNil(t, key)
		assert.Equal(t, uint(3), key.ID)
		assert.Equal(t, "Production Key", key.Name)
		assert.Empty(t, key.User.Email, "the owner record must not ride along on the key payload")
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("another user's key is forbidden", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			UserID:    99,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)

		key, err := apiKeyService.GetByID(3, 7)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrForbidden),
			"ownership violation must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("missing key is a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)

		key, err := apiKeyService.GetByID(404, 7)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound),
			"missing key must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("lookup failure is not a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(3)).Return(nil, errors.New("connection refused"))

		key, err := apiKeyService.GetByID(3, 7)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		assert.False(t, errors.Is(err, apperrors.ErrForbidden))
		mockAPIKeyRepo.AssertExpectations(t)
	})
}

func TestAPIKeyService_Update(t *testing.T) {
	t.Run("rename only leaves the active flag alone", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			Name:      "Old Name",
			UserID:    7,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)
		mockAPIKeyRepo.On("Update", mock.MatchedBy(func(k *models.APIKey) bool {
			return k.ID == 3 && k.Name == "New Name" && k.IsActive
		})).Return(nil)

		name := "New Name"
		key, err := apiKeyService.Update(3, 7, &name, nil)

		assert.NoError(t, err)
		assert.Equal(t, "New Name", key.Name)
		assert.True(t, key.IsActive)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("deactivate only leaves the name alone", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			Name:      "Keep Me",
			UserID:    7,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)
		mockAPIKeyRepo.On("Update", mock.MatchedBy(func(k *models.APIKey) bool {
			return k.ID == 3 && k.Name == "Keep Me" && !k.IsActive
		})).Return(nil)

		active := false
		key, err := apiKeyService.Update(3, 7, nil, &active)

		assert.NoError(t, err)
		assert.Equal(t, "Keep Me", key.Name)
		assert.False(t, key.IsActive)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	// Reactivation is deliberate: revocation flips is_active, and the owner may
	// flip it back. Expiry is the guard that cannot be undone this way.
	t.Run("a revoked key can be reactivated by its owner", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			Name:      "Revoked Key",
			UserID:    7,
			IsActive:  false,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)
		mockAPIKeyRepo.On("Update", mock.MatchedBy(func(k *models.APIKey) bool {
			return k.ID == 3 && k.IsActive
		})).Return(nil)

		active := true
		key, err := apiKeyService.Update(3, 7, nil, &active)

		assert.NoError(t, err)
		assert.True(t, key.IsActive)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("another user's key is forbidden", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			UserID:    99,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)

		name := "Hijacked"
		key, err := apiKeyService.Update(3, 7, &name, nil)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrForbidden))
		mockAPIKeyRepo.AssertNotCalled(t, "Update", mock.Anything)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("missing key is a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)

		name := "New Name"
		key, err := apiKeyService.Update(404, 7, &name, nil)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound))
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("persist failure is surfaced", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			UserID:    7,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)
		mockAPIKeyRepo.On("Update", mock.Anything).Return(errors.New("connection refused"))

		name := "New Name"
		key, err := apiKeyService.Update(3, 7, &name, nil)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		mockAPIKeyRepo.AssertExpectations(t)
	})
}

func TestAPIKeyService_Generate_WithExpiry(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

	expires := time.Now().Add(48 * time.Hour)
	mockAPIKeyRepo.On("Create", mock.MatchedBy(func(k *models.APIKey) bool {
		return k.ExpiresAt != nil && k.ExpiresAt.Equal(expires)
	})).Return(nil)

	key, apiKey, err := apiKeyService.Generate(1, "Expiring Key", &expires)

	assert.NoError(t, err)
	assert.NotEmpty(t, key)
	assert.NotNil(t, apiKey.ExpiresAt)
	assert.True(t, apiKey.ExpiresAt.Equal(expires))
	mockAPIKeyRepo.AssertExpectations(t)
}

func TestAPIKeyService_List(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo, "test-api-key-secret")

	userID := uint(1)
	now := time.Now()
	expectedKeys := []models.APIKey{
		{
			BaseModel:  models.BaseModel{ID: 1},
			Name:       "Production Key",
			UserID:     userID,
			IsActive:   true,
			LastUsedAt: &now,
		},
		{
			BaseModel: models.BaseModel{ID: 2},
			Name:      "Development Key",
			UserID:    userID,
			IsActive:  false,
		},
	}

	mockAPIKeyRepo.On("GetByUserID", userID).Return(expectedKeys, nil)

	keys, err := apiKeyService.List(userID)

	assert.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Equal(t, expectedKeys, keys)
	
	mockAPIKeyRepo.AssertExpectations(t)
}