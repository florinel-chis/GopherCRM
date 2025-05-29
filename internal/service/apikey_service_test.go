package service

import (
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAPIKeyService_Generate(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo)

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
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo)

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
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo)

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
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo)

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
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo)

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
		assert.Equal(t, "unauthorized", err.Error())
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("api key not found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo)

		apiKeyID := uint(1)
		userID := uint(1)

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(nil, errors.New("not found"))

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.Error(t, err)
		mockAPIKeyRepo.AssertExpectations(t)
	})
}

func TestAPIKeyService_List(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo)

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