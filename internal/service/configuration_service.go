package service

import (
	"errors"
	"fmt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

type ConfigurationService interface {
	GetByKey(key string) (*models.Configuration, error)
	GetByCategory(category models.ConfigurationCategory) ([]models.Configuration, error)
	GetAll() ([]models.Configuration, error)
	Set(key string, value interface{}) error
	Get(key string) (interface{}, error)
	GetString(key string) (string, error)
	GetBool(key string) (bool, error)
	GetInt(key string) (int, error)
	GetFloat(key string) (float64, error)
	GetArray(key string) ([]interface{}, error)
	GetJSON(key string) (map[string]interface{}, error)
	Delete(key string) error
	Reset(key string) error
	InitializeDefaults() error

	// Specific configuration getters for common settings
	GetLeadConversionStatuses() ([]string, error)
	IsLeadConversionRequireNotes() (bool, error)
	IsLeadConversionAutoAssignOwner() (bool, error)
}

type configurationService struct {
	configRepo repository.ConfigurationRepository
}

func NewConfigurationService(configRepo repository.ConfigurationRepository) ConfigurationService {
	return &configurationService{configRepo: configRepo}
}

func (s *configurationService) GetByKey(key string) (*models.Configuration, error) {
	return s.configRepo.GetByKey(key)
}

func (s *configurationService) GetByCategory(category models.ConfigurationCategory) ([]models.Configuration, error) {
	return s.configRepo.GetByCategory(category)
}

func (s *configurationService) GetAll() ([]models.Configuration, error) {
	return s.configRepo.GetAll()
}

func (s *configurationService) Set(key string, value interface{}) error {
	config, err := s.configRepo.GetByKey(key)
	if err != nil {
		return fmt.Errorf("configuration not found: %s", key)
	}

	if config.IsReadOnly {
		return errors.New("configuration is read-only")
	}

	if !config.IsValidValue(value) {
		return errors.New("invalid value for configuration")
	}

	if err := config.SetValue(value); err != nil {
		return err
	}

	return s.configRepo.Update(config)
}

func (s *configurationService) Get(key string) (interface{}, error) {
	config, err := s.configRepo.GetByKey(key)
	if err != nil {
		return nil, err
	}
	return config.GetValueAs(), nil
}

func (s *configurationService) GetString(key string) (string, error) {
	value, err := s.Get(key)
	if err != nil {
		return "", err
	}
	if str, ok := value.(string); ok {
		return str, nil
	}
	return "", fmt.Errorf("configuration %s is not a string", key)
}

func (s *configurationService) GetBool(key string) (bool, error) {
	value, err := s.Get(key)
	if err != nil {
		return false, err
	}
	if b, ok := value.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("configuration %s is not a boolean", key)
}

func (s *configurationService) GetInt(key string) (int, error) {
	value, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	if i, ok := value.(int); ok {
		return i, nil
	}
	return 0, fmt.Errorf("configuration %s is not an integer", key)
}

func (s *configurationService) GetFloat(key string) (float64, error) {
	value, err := s.Get(key)
	if err != nil {
		return 0, err
	}
	if f, ok := value.(float64); ok {
		return f, nil
	}
	return 0, fmt.Errorf("configuration %s is not a float", key)
}

func (s *configurationService) GetArray(key string) ([]interface{}, error) {
	value, err := s.Get(key)
	if err != nil {
		return nil, err
	}
	if arr, ok := value.([]interface{}); ok {
		return arr, nil
	}
	return nil, fmt.Errorf("configuration %s is not an array", key)
}

func (s *configurationService) GetJSON(key string) (map[string]interface{}, error) {
	value, err := s.Get(key)
	if err != nil {
		return nil, err
	}
	if obj, ok := value.(map[string]interface{}); ok {
		return obj, nil
	}
	return nil, fmt.Errorf("configuration %s is not a JSON object", key)
}

func (s *configurationService) Delete(key string) error {
	config, err := s.configRepo.GetByKey(key)
	if err != nil {
		return err
	}

	if config.IsSystem {
		return errors.New("cannot delete system configuration")
	}

	return s.configRepo.Delete(key)
}

func (s *configurationService) Reset(key string) error {
	config, err := s.configRepo.GetByKey(key)
	if err != nil {
		return err
	}

	if config.IsReadOnly {
		return errors.New("configuration is read-only")
	}

	config.Value = config.DefaultValue
	return s.configRepo.Update(config)
}

func (s *configurationService) InitializeDefaults() error {
	return s.configRepo.InitializeDefaults()
}

// Specific configuration getters

func (s *configurationService) GetLeadConversionStatuses() ([]string, error) {
	arr, err := s.GetArray("leads.conversion.allowed_statuses")
	if err != nil {
		return []string{"qualified"}, nil // Default fallback
	}

	statuses := make([]string, len(arr))
	for i, status := range arr {
		if str, ok := status.(string); ok {
			statuses[i] = str
		}
	}
	return statuses, nil
}

func (s *configurationService) IsLeadConversionRequireNotes() (bool, error) {
	return s.GetBool("leads.conversion.require_notes")
}

func (s *configurationService) IsLeadConversionAutoAssignOwner() (bool, error) {
	return s.GetBool("leads.conversion.auto_assign_owner")
}