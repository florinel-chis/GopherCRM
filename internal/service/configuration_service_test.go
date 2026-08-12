package service

import (
	"errors"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
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
	suite.service = NewConfigurationService(suite.mockRepo, testConfigurationSecretBox())
}

// testConfigurationSecretBox builds the box the service seals sensitive values
// with. The master secret is obviously fake and exists only for these tests.
func testConfigurationSecretBox() *utils.SecretBox {
	return utils.NewSecretBox("configuration-service-unit-test-secret", "configuration-secret")
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
			svc := NewConfigurationService(repo, testConfigurationSecretBox())

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

// --- Sensitive configurations ---

// testSecretValue is an obviously fake credential: no test in this package ever
// carries a real one.
const testSecretValue = "not-a-real-provider-key-0001"

func sensitiveConfig() *models.Configuration {
	return &models.Configuration{
		Key:         "integration.aeo.gemini_api_key",
		Type:        models.ConfigTypeString,
		Category:    models.CategoryIntegration,
		Description: "Gemini API key for the answer-engine module",
		IsSystem:    true,
		IsSensitive: true,
	}
}

// The row handed to the repository must never hold the plaintext: what lands in
// the database is the sealed form, and nothing else.
func (suite *ConfigurationServiceTestSuite) TestSet_SensitiveValueIsEncryptedAtRest() {
	config := sensitiveConfig()
	var stored string
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		stored = c.Value
		return true
	})).Return(nil)

	suite.NoError(suite.service.Set(config.Key, testSecretValue))

	assert.True(suite.T(), utils.IsSealed(stored), "stored value is not sealed: %q", stored)
	assert.NotContains(suite.T(), stored, testSecretValue)
}

func (suite *ConfigurationServiceTestSuite) TestGetSecret_RoundTripsTheStoredValue() {
	config := sensitiveConfig()
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)
	suite.mockRepo.On("Update", mock.Anything).Return(nil)

	suite.NoError(suite.service.Set(config.Key, testSecretValue))

	// The mock hands back the same row the service just sealed, which is
	// exactly what a re-read from the database would return.
	secret, err := suite.service.GetSecret(config.Key)
	suite.NoError(err)
	assert.Equal(suite.T(), testSecretValue, secret)
}

func (suite *ConfigurationServiceTestSuite) TestGetSecret_ClearedKeyIsEmpty() {
	config := sensitiveConfig()
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		return c.Value == ""
	})).Return(nil)

	suite.NoError(suite.service.Set(config.Key, ""))

	secret, err := suite.service.GetSecret(config.Key)
	suite.NoError(err)
	assert.Equal(suite.T(), "", secret)
}

// A value sealed under a different master secret — the rotation case — is
// treated as unset rather than as a failure, so the caller falls back instead
// of the module breaking.
func (suite *ConfigurationServiceTestSuite) TestGetSecret_UndecryptableValueIsUnset() {
	sealed, err := utils.NewSecretBox("a-different-master-secret", "configuration-secret").Seal(testSecretValue)
	suite.Require().NoError(err)

	config := sensitiveConfig()
	config.Value = sealed
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)

	secret, err := suite.service.GetSecret(config.Key)
	suite.NoError(err)
	assert.Equal(suite.T(), "", secret)
}

// A plaintext value that predates encryption is not handed back either: only
// this box's ciphertext is ever decrypted.
func (suite *ConfigurationServiceTestSuite) TestGetSecret_PlaintextValueIsUnset() {
	config := sensitiveConfig()
	config.Value = testSecretValue
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)

	secret, err := suite.service.GetSecret(config.Key)
	suite.NoError(err)
	assert.Equal(suite.T(), "", secret)
}

func (suite *ConfigurationServiceTestSuite) TestGetSecret_UnknownKeyIsNotFoundSentinel() {
	suite.mockRepo.On("GetByKey", "no.such.key").Return(nil, gorm.ErrRecordNotFound)

	secret, err := suite.service.GetSecret("no.such.key")

	suite.Error(err)
	assert.True(suite.T(), apperrors.IsNotFound(err), "expected ErrNotFound, got %v", err)
	assert.Equal(suite.T(), "", secret)
}

func (suite *ConfigurationServiceTestSuite) TestGetSecret_NonSensitiveKeyIsRefused() {
	suite.mockRepo.On("GetByKey", "general.company_name").Return(writableStringConfig(), nil)

	secret, err := suite.service.GetSecret("general.company_name")

	suite.Error(err)
	assert.Equal(suite.T(), "", secret)
}

// The typed getters must refuse a sensitive key: they are the paths that hand a
// value to callers which serialise it.
func (suite *ConfigurationServiceTestSuite) TestTypedGetters_RefuseSensitiveKeys() {
	config := sensitiveConfig()
	sealed, err := testConfigurationSecretBox().Seal(testSecretValue)
	suite.Require().NoError(err)
	config.Value = sealed
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)

	value, err := suite.service.Get(config.Key)
	suite.Error(err)
	assert.Nil(suite.T(), value)
	assert.Contains(suite.T(), err.Error(), "sensitive")

	str, err := suite.service.GetString(config.Key)
	suite.Error(err)
	assert.Equal(suite.T(), "", str)

	_, err = suite.service.GetBool(config.Key)
	suite.Error(err)
	_, err = suite.service.GetInt(config.Key)
	suite.Error(err)
	_, err = suite.service.GetFloat(config.Key)
	suite.Error(err)
	_, err = suite.service.GetArray(config.Key)
	suite.Error(err)
	_, err = suite.service.GetJSON(config.Key)
	suite.Error(err)
}

// Resetting a sensitive entry clears it: the seeded default is empty, and an
// empty value is stored as an empty column rather than as ciphertext.
func (suite *ConfigurationServiceTestSuite) TestReset_SensitiveKeyClearsTheValue() {
	config := sensitiveConfig()
	config.Value = "enc:v1:whatever-was-there-before"
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		return c.Value == ""
	})).Return(nil)

	suite.NoError(suite.service.Reset(config.Key))
}

// Without key material a sensitive write fails rather than storing plaintext.
func (suite *ConfigurationServiceTestSuite) TestSet_SensitiveValueWithoutABoxIsRefused() {
	repo := new(mocks.ConfigurationRepository)
	config := sensitiveConfig()
	repo.On("GetByKey", config.Key).Return(config, nil)
	svc := NewConfigurationService(repo, nil)

	err := svc.Set(config.Key, testSecretValue)

	suite.Error(err)
	assert.NotContains(suite.T(), err.Error(), testSecretValue)
	repo.AssertNotCalled(suite.T(), "Update", mock.Anything)
	repo.AssertExpectations(suite.T())
}

// A non-sensitive entry is untouched by any of this: it is stored verbatim and
// read back through the typed getters as before.
func (suite *ConfigurationServiceTestSuite) TestSet_NonSensitiveValueIsStoredVerbatim() {
	config := writableStringConfig()
	suite.mockRepo.On("GetByKey", config.Key).Return(config, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Configuration) bool {
		return c.Value == "Acme Industries"
	})).Return(nil)

	suite.NoError(suite.service.Set(config.Key, "Acme Industries"))

	value, err := suite.service.GetString(config.Key)
	suite.NoError(err)
	assert.Equal(suite.T(), "Acme Industries", value)
}

func TestConfigurationServiceTestSuite(t *testing.T) {
	suite.Run(t, new(ConfigurationServiceTestSuite))
}
