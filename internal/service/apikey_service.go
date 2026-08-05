package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type apiKeyService struct {
	apiKeyRepo   repository.APIKeyRepository
	apiKeySecret string
}

func NewAPIKeyService(apiKeyRepo repository.APIKeyRepository, apiKeySecret string) APIKeyService {
	return &apiKeyService{apiKeyRepo: apiKeyRepo, apiKeySecret: apiKeySecret}
}

func (s *apiKeyService) Generate(userID uint, name string, expiresAt *time.Time) (string, *models.APIKey, error) {
	// Generate random key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", nil, err
	}

	key := hex.EncodeToString(keyBytes)
	prefix := key[:8]

	apiKey := &models.APIKey{
		Name:      name,
		KeyHash:   utils.HashAPIKeyHMAC(key, s.apiKeySecret),
		Prefix:    prefix,
		UserID:    userID,
		ExpiresAt: expiresAt,
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

// ownedKey loads a key and asserts that userID owns it. It is the single place
// the ownership rule for a single key is expressed, so the fetch, update and
// revoke paths cannot drift apart. The repository returns gorm's own sentinel
// unwrapped, so the translation to apperrors.ErrNotFound happens here and
// callers can classify with errors.Is instead of matching on a message.
func (s *apiKeyService) ownedKey(id uint, userID uint) (*models.APIKey, error) {
	apiKey, err := s.apiKeyRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("api key %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}

	if apiKey.UserID != userID {
		return nil, fmt.Errorf("api key %d belongs to another user: %w", id, apperrors.ErrForbidden)
	}

	return apiKey, nil
}

func (s *apiKeyService) GetByID(id uint, userID uint) (*models.APIKey, error) {
	apiKey, err := s.ownedKey(id, userID)
	if err != nil {
		return nil, err
	}

	// The repository preloads the owner. Drop it: the caller is the owner by
	// definition here, so it carries no information, and shipping a whole user
	// record inside every key payload would make this endpoint's shape differ
	// from the list endpoint's for no reason.
	apiKey.User = models.User{}
	return apiKey, nil
}

// Update applies only the fields the caller actually sent. Setting isActive to
// true on a revoked key reactivates it — revocation here is a flag the owner
// controls, not a tombstone. An expired key is unaffected by this: expiry is
// checked independently at authentication time and cannot be cleared through
// this path.
func (s *apiKeyService) Update(id uint, userID uint, name *string, isActive *bool) (*models.APIKey, error) {
	apiKey, err := s.ownedKey(id, userID)
	if err != nil {
		return nil, err
	}

	if name != nil {
		apiKey.Name = *name
	}
	if isActive != nil {
		apiKey.IsActive = *isActive
	}

	if err := s.apiKeyRepo.Update(apiKey); err != nil {
		return nil, err
	}

	apiKey.User = models.User{}
	return apiKey, nil
}

func (s *apiKeyService) Revoke(id uint, userID uint) error {
	apiKey, err := s.ownedKey(id, userID)
	if err != nil {
		return err
	}

	apiKey.IsActive = false
	return s.apiKeyRepo.Update(apiKey)
}

func (s *apiKeyService) List(userID uint) ([]models.APIKey, error) {
	return s.apiKeyRepo.GetByUserID(userID)
}

