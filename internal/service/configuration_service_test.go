package service

import (
	"errors"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type ConfigurationServiceTestSuite struct {
	suite.Suite
	mockRepo *mocks.ConfigurationRepository
	service  ConfigurationService
}

func (suite *ConfigurationServiceTestSuite) SetupTest() {
	suite.mockRepo = new(mocks.ConfigurationRepository)
	suite.service = NewConfigurationService(suite.mockRepo)
}

func (suite *ConfigurationServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
}

// errDatabase stands in for an infrastructure failure that is not a missing row.
var errDatabase = errors.New("connection refused")

func writableStringConfig() *models.Configuration {
	return &models.Configuration{
		Key:          "general.company_name",
		Value:        "GopherCRM",
		Type:         models.ConfigTypeString,
		Category:     models.CategoryGeneral,
		DefaultValue: "GopherCRM",
	}
}

func readOnlyConfig() *models.Configuration {
	return &models.Configuration{
		Key:          "security.locked",
		Value:        "locked",
		Type:         models.ConfigTypeString,
		Category:     models.CategorySecurity,
		DefaultValue: "locked",
		IsReadOnly:   true,
	}
}

// --- GetByKey classification ---

func (suite *ConfigurationServiceTestSuite) TestGetByKey_UnknownKeyIsNotFoundSentinel() {
	suite.mockRepo.On("GetByKey", "no.such.key").Return(nil, gorm.ErrRecordNotFound)

	config, err := suite.service.GetByKey("no.such.key")

	suite.Nil(config)
	suite.Error(err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound), "expected ErrNotFound, got %v", err)
	assert.True(suite.T(), apperrors.IsNotFound(err))
	assert.Contains(suite.T(), err.Error(), "no.such.key")
}

func (suite *ConfigurationServiceTestSuite) TestGetByKey_DatabaseErrorIsNotNotFound() {
	suite.mockRepo.On("GetByKey", "general.company_name").Return(nil, errDatabase)

	_, err := suite.service.GetByKey("general.company_name")

	suite.Error(err)
	assert.False(suite.T(), apperrors.IsNotFound(err), "a driver failure must not be classified as not-found")
	assert.True(suite.T(), errors.Is(err, errDatabase))
}

// --- Set classification ---

func (suite *ConfigurationServiceTestSuite) TestSet_UnknownKeyIsNotFoundSentinel() {
	suite.mockRepo.On("GetByKey", "no.such.key").Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Set("no.such.key", "x")

	suite.Error(err)
	assert.True(suite.T(), apperrors.IsNotFound(err), "expected ErrNotFound, got %v", err)
}

func (suite *ConfigurationServiceTestSuite) TestSet_DatabaseErrorIsNotNotFound() {
	suite.mockRepo.On("GetByKey", "general.company_name").Return(nil, errDatabase)

	err := suite.service.Set("general.company_name", "x")

	suite.Error(err)
	assert.False(suite.T(), apperrors.IsNotFound(err), "a driver failure must not masquerade as a missing key")
	assert.True(suite.T(), errors.Is(err, errDatabase))
}

func (suite *ConfigurationServiceTestSuite) TestSet_ReadOnlyIsReadOnlySentinel() {
	suite.mockRepo.On("GetByKey", "security.locked").Return(readOnlyConfig(), nil)

	err := suite.service.Set("security.locked", "x")

	suite.Error(err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrConfigurationReadOnly), "expected ErrConfigurationReadOnly, got %v", err)
	assert.False(suite.T(), apperrors.IsNotFound(err))
}

func (suite *ConfigurationServiceTestSuite) TestSet_InvalidValueIsInvalidValueSentinel() {
	config := &models.Configuration{
		Key:         "security.session_timeout_hours",
		Value:       "24",
		Type:        models.ConfigTypeInteger,
		Category:    models.CategorySecurity,
		ValidValues: `[1, 8, 24, 48, 72, 168]`,
	}
	suite.mockRepo.On("GetByKey", "security.session_timeout_hours").Return(config, nil)

	err := suite.service.Set("security.session_timeout_hours", float64(999))

	suite.Error(err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrConfigurationInvalidValue), "expected ErrConfigurationInvalidValue, got %v", err)
}

// TestSet_FalseOnBooleanConfig proves the service layer never had a problem with
// falsy values: the rejection was purely the handler's binding tag.
func (suite *ConfigurationServiceTestSuite) TestSet_FalseOnBooleanConfig() {
	config := &models.Configuration{
		Key:          "tickets.auto_assign_support",
		Value:        "true",
		Type:         models.ConfigTypeBoolean,
		Category:     models.CategoryTickets,
		DefaultValue: "false",
	}
	suite.mockRepo.On("GetByKey", "tickets.auto_assign_support").Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		return c.Value == "false"
	})).Return(nil)

	suite.NoError(suite.service.Set("tickets.auto_assign_support", false))
}

// TestSet_ZeroOnIntegerConfig covers the json.Unmarshal representation: a JSON
// number arrives as float64, and float64(0) must persist as "0".
func (suite *ConfigurationServiceTestSuite) TestSet_ZeroOnIntegerConfig() {
	config := &models.Configuration{
		Key:      "test.integer.setting",
		Value:    "42",
		Type:     models.ConfigTypeInteger,
		Category: models.CategoryGeneral,
	}
	suite.mockRepo.On("GetByKey", "test.integer.setting").Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		return c.Value == "0"
	})).Return(nil)

	suite.NoError(suite.service.Set("test.integer.setting", float64(0)))
}

func (suite *ConfigurationServiceTestSuite) TestSet_EmptyStringOnStringConfig() {
	config := writableStringConfig()
	suite.mockRepo.On("GetByKey", "general.company_name").Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		return c.Value == ""
	})).Return(nil)

	suite.NoError(suite.service.Set("general.company_name", ""))
}

// --- Set type strictness ---

// TestSet_TypeMismatchIsInvalidValueSentinel covers the entries that carry no
// valid_values constraint, so the rejection can only come from SetValue: a
// mismatched type used to be coerced (to "false", to "") and saved.
func (suite *ConfigurationServiceTestSuite) TestSet_TypeMismatchIsInvalidValueSentinel() {
	cases := []struct {
		name   string
		config *models.Configuration
		value  interface{}
	}{
		{
			name: "string on boolean entry",
			config: &models.Configuration{
				Key:      "tickets.auto_assign_support",
				Value:    "true",
				Type:     models.ConfigTypeBoolean,
				Category: models.CategoryTickets,
			},
			value: "yes",
		},
		{
			name: "string on integer entry",
			config: &models.Configuration{
				Key:      "test.integer.setting",
				Value:    "42",
				Type:     models.ConfigTypeInteger,
				Category: models.CategoryGeneral,
			},
			value: "10",
		},
		{
			name: "fractional number on integer entry",
			config: &models.Configuration{
				Key:      "test.integer.setting",
				Value:    "42",
				Type:     models.ConfigTypeInteger,
				Category: models.CategoryGeneral,
			},
			value: float64(3.5),
		},
		{
			name:   "number on string entry",
			config: writableStringConfig(),
			value:  float64(5),
		},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			repo := new(mocks.ConfigurationRepository)
			repo.On("GetByKey", tc.config.Key).Return(tc.config, nil)
			svc := NewConfigurationService(repo)

			err := svc.Set(tc.config.Key, tc.value)

			suite.Error(err)
			assert.True(suite.T(), errors.Is(err, apperrors.ErrConfigurationInvalidValue), "expected ErrConfigurationInvalidValue, got %v", err)
			assert.Contains(suite.T(), err.Error(), tc.config.Key)
			repo.AssertNotCalled(suite.T(), "Update", mock.Anything)
			repo.AssertExpectations(suite.T())
		})
	}
}

// --- Reset classification (the reported defect) ---

func (suite *ConfigurationServiceTestSuite) TestReset_UnknownKeyIsNotFoundSentinel() {
	suite.mockRepo.On("GetByKey", "no.such.key").Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Reset("no.such.key")

	suite.Error(err)
	assert.True(suite.T(), apperrors.IsNotFound(err), "expected ErrNotFound, got %v", err)
	assert.Contains(suite.T(), err.Error(), "no.such.key")
}

func (suite *ConfigurationServiceTestSuite) TestReset_ReadOnlyIsReadOnlySentinel() {
	suite.mockRepo.On("GetByKey", "security.locked").Return(readOnlyConfig(), nil)

	err := suite.service.Reset("security.locked")

	suite.Error(err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrConfigurationReadOnly), "expected ErrConfigurationReadOnly, got %v", err)
}

func (suite *ConfigurationServiceTestSuite) TestReset_DatabaseErrorIsNotNotFound() {
	suite.mockRepo.On("GetByKey", "general.company_name").Return(nil, errDatabase)

	err := suite.service.Reset("general.company_name")

	suite.Error(err)
	assert.False(suite.T(), apperrors.IsNotFound(err))
	assert.False(suite.T(), errors.Is(err, apperrors.ErrConfigurationReadOnly))
	assert.True(suite.T(), errors.Is(err, errDatabase))
}

func (suite *ConfigurationServiceTestSuite) TestReset_RestoresDefaultValue() {
	config := writableStringConfig()
	config.Value = "Custom Company"
	suite.mockRepo.On("GetByKey", "general.company_name").Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		return c.Value == "GopherCRM"
	})).Return(nil)

	suite.NoError(suite.service.Reset("general.company_name"))
}

func TestConfigurationServiceTestSuite(t *testing.T) {
	suite.Run(t, new(ConfigurationServiceTestSuite))
}
