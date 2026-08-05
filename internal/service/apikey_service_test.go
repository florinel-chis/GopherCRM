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

	key, apiKey, err := apiKeyService.Generate(userID, name)

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

	key, apiKey, err := apiKeyService.Generate(userID, name)

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