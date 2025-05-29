package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/repository"
	"github.com/florinel-chis/gocrm/internal/utils"
)

type apiKeyService struct {
	apiKeyRepo repository.APIKeyRepository
}

func NewAPIKeyService(apiKeyRepo repository.APIKeyRepository) APIKeyService {
	return &apiKeyService{apiKeyRepo: apiKeyRepo}
}

func (s *apiKeyService) Generate(userID uint, name string) (string, *models.APIKey, error) {
	// Generate random key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", nil, err
	}
	
	key := hex.EncodeToString(keyBytes)
	prefix := key[:8]
	
	apiKey := &models.APIKey{
		Name:    name,
		KeyHash: utils.HashAPIKey(key),
		Prefix:  prefix,
		UserID:  userID,
	}
	
	if err := s.apiKeyRepo.Create(apiKey); err != nil {
		return "", nil, err
	}
	
	// Return the full key only once
	fullKey := fmt.Sprintf("gcrm_%s", key)
	return fullKey, apiKey, nil
}

func (s *apiKeyService) GetByUser(userID uint) ([]models.APIKey, error) {
	return s.apiKeyRepo.GetByUserID(userID)
}

func (s *apiKeyService) Revoke(id uint, userID uint) error {
	apiKey, err := s.apiKeyRepo.GetByID(id)
	if err != nil {
		return err
	}
	
	if apiKey.UserID != userID {
		return fmt.Errorf("unauthorized")
	}
	
	apiKey.IsActive = false
	return s.apiKeyRepo.Update(apiKey)
}

func (s *apiKeyService) List(userID uint) ([]models.APIKey, error) {
	return s.apiKeyRepo.GetByUserID(userID)
}

