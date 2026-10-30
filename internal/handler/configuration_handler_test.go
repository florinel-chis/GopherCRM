package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

// mockConfigurationService is a hand-written mock: the generated mocks package
// does not carry a ConfigurationService double.
type mockConfigurationService struct {
	mock.Mock
}

func (m *mockConfigurationService) GetByKey(key string) (*models.Configuration, error) {
	args := m.Called(key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.Configuration), args.Error(1)
}

func (m *mockConfigurationService) GetByCategory(category models.ConfigurationCategory) ([]models.Configuration, error) {
	args := m.Called(category)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Configuration), args.Error(1)
}

func (m *mockConfigurationService) GetAll() ([]models.Configuration, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Configuration), args.Error(1)
}

func (m *mockConfigurationService) Set(key string, value interface{}) error {
	return m.Called(key, value).Error(0)
}

func (m *mockConfigurationService) Get(key string) (interface{}, error) {
	args := m.Called(key)
	return args.Get(0), args.Error(1)
}

func (m *mockConfigurationService) GetSecret(key string) (string, error) {
	args := m.Called(key)
	return args.String(0), args.Error(1)
}

func (m *mockConfigurationService) GetString(key string) (string, error) {
	args := m.Called(key)
	return args.String(0), args.Error(1)
}

func (m *mockConfigurationService) GetBool(key string) (bool, error) {
	args := m.Called(key)
	return args.Bool(0), args.Error(1)
}

func (m *mockConfigurationService) GetInt(key string) (int, error) {
	args := m.Called(key)
	return args.Int(0), args.Error(1)
}

func (m *mockConfigurationService) GetFloat(key string) (float64, error) {
	args := m.Called(key)
	return args.Get(0).(float64), args.Error(1)
}

func (m *mockConfigurationService) GetArray(key string) ([]interface{}, error) {
	args := m.Called(key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]interface{}), args.Error(1)
}

func (m *mockConfigurationService) GetJSON(key string) (map[string]interface{}, error) {
	args := m.Called(key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

func (m *mockConfigurationService) Delete(key string) error {
	return m.Called(key).Error(0)
}

func (m *mockConfigurationService) Reset(key string) error {
	return m.Called(key).Error(0)
}

func (m *mockConfigurationService) InitializeDefaults() error {
	return m.Called().Error(0)
}

func (m *mockConfigurationService) GetLeadConversionStatuses() ([]string, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

func (m *mockConfigurationService) IsLeadConversionRequireNotes() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

func (m *mockConfigurationService) IsLeadConversionAutoAssignOwner() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

type ConfigurationHandlerTestSuite struct {
	suite.Suite
	mockService *mockConfigurationService
	handler     *ConfigurationHandler
	router      *gin.Engine
}

func (suite *ConfigurationHandlerTestSuite) SetupSuite() {
	logConfig := config.LoggingConfig{Level: "debug", Format: "json"}
	utils.InitLogger(&logConfig)
}

func (suite *ConfigurationHandlerTestSuite) SetupTest() {
	gin.SetMode(gin.TestMode)
	suite.mockService = new(mockConfigurationService)
	suite.handler = NewConfigurationHandler(suite.mockService)
	suite.router = gin.New()
	suite.router.Use(middleware.ErrorHandler())
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", "admin")
		c.Set("request_id", "test-request-id")
		c.Next()
	})
	suite.router.GET("/configurations", suite.handler.GetAll)
	suite.router.GET("/configurations/ui", suite.handler.GetUIConfigurations)
	suite.router.GET("/configurations/category/:category", suite.handler.GetByCategory)
	suite.router.GET("/configurations/:key", suite.handler.GetByKey)
	suite.router.PUT("/configurations/:key", suite.handler.Set)
	suite.router.POST("/configurations/:key/reset", suite.handler.Reset)
}

func (suite *ConfigurationHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *ConfigurationHandlerTestSuite) put(key string, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("PUT", "/configurations/"+key, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func (suite *ConfigurationHandlerTestSuite) reset(key string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("POST", "/configurations/"+key+"/reset", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func notFoundErr(key string) error {
	return fmt.Errorf("configuration %q not found: %w", key, apperrors.ErrNotFound)
}

// --- Reset: the reported defect ---

func (suite *ConfigurationHandlerTestSuite) TestReset_UnknownKeyReturns404() {
	suite.mockService.On("Reset", "no.such.key").Return(notFoundErr("no.such.key"))

	w := suite.reset("no.such.key")

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// TestReset_RawGormNotFoundReturns404 pins the exact shape of the original
// defect: the repository's own not-found error arriving unwrapped. It answered
// 500 because the handler compared error strings.
func (suite *ConfigurationHandlerTestSuite) TestReset_RawGormNotFoundReturns404() {
	suite.mockService.On("Reset", "no.such.key").Return(gorm.ErrRecordNotFound)

	w := suite.reset("no.such.key")

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestReset_ReadOnlyReturns400() {
	suite.mockService.On("Reset", "security.locked").
		Return(fmt.Errorf("configuration %q is read-only: %w", "security.locked", apperrors.ErrConfigurationReadOnly))

	w := suite.reset("security.locked")

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	var response utils.APIResponse
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
	assert.Contains(suite.T(), response.Error.Message, "read-only")
}

func (suite *ConfigurationHandlerTestSuite) TestReset_DatabaseErrorReturns500() {
	suite.mockService.On("Reset", "general.company_name").Return(errors.New("connection refused"))

	w := suite.reset("general.company_name")

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestReset_SuccessReturns200() {
	config := &models.Configuration{Key: "general.company_name", Value: "GopherCRM"}
	suite.mockService.On("Reset", "general.company_name").Return(nil)
	suite.mockService.On("GetByKey", "general.company_name").Return(config, nil)

	w := suite.reset("general.company_name")

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

// --- Set: classification ---

func (suite *ConfigurationHandlerTestSuite) TestSet_UnknownKeyReturns404() {
	suite.mockService.On("Set", "no.such.key", "x").Return(notFoundErr("no.such.key"))

	w := suite.put("no.such.key", `{"value":"x"}`)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestSet_ReadOnlyReturns400() {
	suite.mockService.On("Set", "security.locked", "x").
		Return(fmt.Errorf("configuration %q is read-only: %w", "security.locked", apperrors.ErrConfigurationReadOnly))

	w := suite.put("security.locked", `{"value":"x"}`)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestSet_InvalidValueReturns400() {
	suite.mockService.On("Set", "security.session_timeout_hours", float64(999)).
		Return(fmt.Errorf("invalid value for configuration %q: %w", "security.session_timeout_hours", apperrors.ErrConfigurationInvalidValue))

	w := suite.put("security.session_timeout_hours", `{"value":999}`)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	var response utils.APIResponse
	suite.NoError(json.Unmarshal(w.Body.Bytes(), &response))
	assert.Contains(suite.T(), response.Error.Message, "Invalid value")
}

func (suite *ConfigurationHandlerTestSuite) TestSet_DatabaseErrorReturns500() {
	suite.mockService.On("Set", "general.company_name", "x").Return(errors.New("connection refused"))

	w := suite.put("general.company_name", `{"value":"x"}`)

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
}

// --- Set: falsy values ---

func (suite *ConfigurationHandlerTestSuite) TestSet_FalseIsForwardedToService() {
	config := &models.Configuration{Key: "tickets.auto_assign_support", Value: "false", Type: models.ConfigTypeBoolean}
	suite.mockService.On("Set", "tickets.auto_assign_support", false).Return(nil)
	suite.mockService.On("GetByKey", "tickets.auto_assign_support").Return(config, nil)

	w := suite.put("tickets.auto_assign_support", `{"value":false}`)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestSet_ZeroIsForwardedToService() {
	config := &models.Configuration{Key: "test.integer.setting", Value: "0", Type: models.ConfigTypeInteger}
	suite.mockService.On("Set", "test.integer.setting", float64(0)).Return(nil)
	suite.mockService.On("GetByKey", "test.integer.setting").Return(config, nil)

	w := suite.put("test.integer.setting", `{"value":0}`)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestSet_EmptyStringIsForwardedToService() {
	config := &models.Configuration{Key: "general.company_name", Value: "", Type: models.ConfigTypeString}
	suite.mockService.On("Set", "general.company_name", "").Return(nil)
	suite.mockService.On("GetByKey", "general.company_name").Return(config, nil)

	w := suite.put("general.company_name", `{"value":""}`)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestSet_AbsentValueReturns400() {
	w := suite.put("general.company_name", `{}`)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestSet_NullValueReturns400() {
	w := suite.put("general.company_name", `{"value":null}`)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// --- GetByKey: classification ---

func (suite *ConfigurationHandlerTestSuite) TestGetByKey_UnknownKeyReturns404() {
	suite.mockService.On("GetByKey", "no.such.key").Return(nil, notFoundErr("no.such.key"))

	req, _ := http.NewRequest("GET", "/configurations/no.such.key", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *ConfigurationHandlerTestSuite) TestGetByKey_DatabaseErrorReturns500() {
	suite.mockService.On("GetByKey", "general.company_name").Return(nil, errors.New("connection refused"))

	req, _ := http.NewRequest("GET", "/configurations/general.company_name", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
}

// --- Sensitive entries: masked on every read path ---

// storedSensitiveConfig is what the repository holds for a configured secret:
// the sealed form, never the plaintext.
func storedSensitiveConfig() models.Configuration {
	return models.Configuration{
		Key:         "integration.aeo.gemini_api_key",
		Value:       "enc:v1:c2VhbGVkLXN0YW5kLWlu",
		Type:        models.ConfigTypeString,
		Category:    models.CategoryIntegration,
		IsSystem:    true,
		IsSensitive: true,
	}
}

func (suite *ConfigurationHandlerTestSuite) get(path string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

// decodeConfigurations pulls the configuration list out of the response
// envelope as raw maps, so the assertions see exactly the JSON that was sent.
func (suite *ConfigurationHandlerTestSuite) decodeConfigurations(w *httptest.ResponseRecorder) []map[string]interface{} {
	var body struct {
		Data struct {
			Configurations []map[string]interface{} `json:"configurations"`
		} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data.Configurations
}

func (suite *ConfigurationHandlerTestSuite) decodeConfiguration(w *httptest.ResponseRecorder) map[string]interface{} {
	var body struct {
		Data map[string]interface{} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}

func (suite *ConfigurationHandlerTestSuite) assertMasked(entry map[string]interface{}, isSet bool) {
	assert.Equal(suite.T(), "", entry["value"], "a sensitive value must never be returned")
	assert.Equal(suite.T(), true, entry["is_sensitive"])
	assert.Equal(suite.T(), isSet, entry["is_set"])
}

func (suite *ConfigurationHandlerTestSuite) TestGetByKey_SensitiveValueIsMasked() {
	config := storedSensitiveConfig()
	suite.mockService.On("GetByKey", config.Key).Return(&config, nil)

	w := suite.get("/configurations/" + config.Key)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "enc:v1:")
	suite.assertMasked(suite.decodeConfiguration(w), true)
}

func (suite *ConfigurationHandlerTestSuite) TestGetByKey_UnsetSensitiveEntryReportsIsSetFalse() {
	config := storedSensitiveConfig()
	config.Value = ""
	suite.mockService.On("GetByKey", config.Key).Return(&config, nil)

	w := suite.get("/configurations/" + config.Key)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	suite.assertMasked(suite.decodeConfiguration(w), false)
}

func (suite *ConfigurationHandlerTestSuite) TestGetAll_SensitiveValuesAreMasked() {
	plain := models.Configuration{Key: "general.company_name", Value: "GopherCRM", Type: models.ConfigTypeString, Category: models.CategoryGeneral}
	suite.mockService.On("GetAll").Return([]models.Configuration{plain, storedSensitiveConfig()}, nil)

	w := suite.get("/configurations")

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "enc:v1:")
	entries := suite.decodeConfigurations(w)
	suite.Require().Len(entries, 2)
	assert.Equal(suite.T(), "GopherCRM", entries[0]["value"], "a plain entry keeps its value")
	assert.Equal(suite.T(), true, entries[0]["is_set"])
	suite.assertMasked(entries[1], true)
}

func (suite *ConfigurationHandlerTestSuite) TestGetByCategory_SensitiveValuesAreMasked() {
	suite.mockService.On("GetByCategory", models.CategoryIntegration).
		Return([]models.Configuration{storedSensitiveConfig()}, nil)

	w := suite.get("/configurations/category/integration")

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "enc:v1:")
	entries := suite.decodeConfigurations(w)
	suite.Require().Len(entries, 1)
	suite.assertMasked(entries[0], true)
}

// The UI endpoint is the one every authenticated user can call, so it is the
// one masking must not miss.
func (suite *ConfigurationHandlerTestSuite) TestGetUIConfigurations_SensitiveValuesAreMasked() {
	suite.mockService.On("GetByCategory", models.CategoryUI).
		Return([]models.Configuration{storedSensitiveConfig()}, nil)
	suite.mockService.On("GetByCategory", models.CategoryGeneral).
		Return([]models.Configuration{}, nil)
	suite.mockService.On("GetByKey", "deals.default_currency").Return(nil, gorm.ErrRecordNotFound)
	suite.mockService.On("GetLeadConversionStatuses").Return([]string{"qualified"}, nil)
	suite.mockService.On("IsLeadConversionRequireNotes").Return(false, nil)
	suite.mockService.On("IsLeadConversionAutoAssignOwner").Return(true, nil)

	w := suite.get("/configurations/ui")

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "enc:v1:")
	entries := suite.decodeConfigurations(w)
	suite.Require().NotEmpty(entries)
	suite.assertMasked(entries[0], true)
}

// The deal form pre-fills its currency from this endpoint, so the deals
// default has to be in it next to the ui and general entries; a missing row
// (a database that was never seeded) degrades to the key's absence, not to a
// failure of the whole endpoint.
func (suite *ConfigurationHandlerTestSuite) TestGetUIConfigurations_CarriesTheDefaultDealCurrency() {
	suite.mockService.On("GetByCategory", models.CategoryUI).Return([]models.Configuration{}, nil)
	suite.mockService.On("GetByCategory", models.CategoryGeneral).Return([]models.Configuration{}, nil)
	suite.mockService.On("GetByKey", "deals.default_currency").Return(&models.Configuration{
		Key: "deals.default_currency", Value: "RON", Type: models.ConfigTypeString, Category: models.CategoryDeals,
	}, nil).Once()
	suite.mockService.On("GetLeadConversionStatuses").Return([]string{"qualified"}, nil)
	suite.mockService.On("IsLeadConversionRequireNotes").Return(false, nil)
	suite.mockService.On("IsLeadConversionAutoAssignOwner").Return(true, nil)

	w := suite.get("/configurations/ui")
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	var found bool
	for _, entry := range suite.decodeConfigurations(w) {
		if entry["key"] == "deals.default_currency" {
			found = true
			assert.Equal(suite.T(), "RON", entry["value"])
			assert.Equal(suite.T(), string(models.CategoryDeals), entry["category"])
		}
	}
	assert.True(suite.T(), found, "deals.default_currency must be in the UI configurations")

	suite.mockService.On("GetByKey", "deals.default_currency").Return(nil, gorm.ErrRecordNotFound).Once()
	w = suite.get("/configurations/ui")
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "deals.default_currency")
}

// Issue #104: the convert dialog reads the lead-conversion contract from the
// UI endpoint — the allowed statuses, whether notes are required and whether
// the owner is assigned — as read-only entries with the enforced values. An
// entry whose setting cannot be read is left out without failing the call.
func (suite *ConfigurationHandlerTestSuite) TestGetUIConfigurations_CarriesTheLeadConversionContract() {
	suite.mockService.On("GetByCategory", models.CategoryUI).Return([]models.Configuration{}, nil)
	suite.mockService.On("GetByCategory", models.CategoryGeneral).Return([]models.Configuration{}, nil)
	suite.mockService.On("GetByKey", "deals.default_currency").Return(nil, gorm.ErrRecordNotFound)
	suite.mockService.On("GetLeadConversionStatuses").Return([]string{"contacted", "unqualified"}, nil).Once()
	suite.mockService.On("IsLeadConversionRequireNotes").Return(true, nil).Once()
	suite.mockService.On("IsLeadConversionAutoAssignOwner").Return(false, nil).Once()

	w := suite.get("/configurations/ui")
	suite.Require().Equal(http.StatusOK, w.Code)
	byKey := map[string]map[string]interface{}{}
	for _, entry := range suite.decodeConfigurations(w) {
		byKey[entry["key"].(string)] = entry
	}
	expected := map[string][2]string{
		"leads.conversion.allowed_statuses":  {`["contacted","unqualified"]`, "array"},
		"leads.conversion.require_notes":     {"true", "boolean"},
		"leads.conversion.auto_assign_owner": {"false", "boolean"},
	}
	for key, want := range expected {
		entry, ok := byKey[key]
		if !suite.Truef(ok, "%s must be in the UI configurations", key) {
			continue
		}
		suite.Equal(want[0], entry["value"], key)
		suite.Equal(want[1], entry["type"], key)
		suite.Equal(string(models.CategoryLeads), entry["category"], key)
		suite.Equal(true, entry["is_read_only"], key)
	}

	suite.mockService.On("GetLeadConversionStatuses").Return(nil, errors.New("connection refused")).Once()
	suite.mockService.On("IsLeadConversionRequireNotes").Return(false, errors.New("connection refused")).Once()
	suite.mockService.On("IsLeadConversionAutoAssignOwner").Return(true, nil).Once()
	w = suite.get("/configurations/ui")
	suite.Require().Equal(http.StatusOK, w.Code)
	body := w.Body.String()
	suite.NotContains(body, "leads.conversion.allowed_statuses")
	suite.NotContains(body, "leads.conversion.require_notes")
	suite.Contains(body, "leads.conversion.auto_assign_owner")
}

// A write answers with the same masked shape, so a saved key is never echoed.
func (suite *ConfigurationHandlerTestSuite) TestSet_SensitiveResponseIsMasked() {
	config := storedSensitiveConfig()
	suite.mockService.On("Set", config.Key, "not-a-real-provider-key-0002").Return(nil)
	suite.mockService.On("GetByKey", config.Key).Return(&config, nil)

	w := suite.put(config.Key, `{"value":"not-a-real-provider-key-0002"}`)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "not-a-real-provider-key-0002")
	assert.NotContains(suite.T(), w.Body.String(), "enc:v1:")
	suite.assertMasked(suite.decodeConfiguration(w), true)
}

func (suite *ConfigurationHandlerTestSuite) TestReset_SensitiveResponseIsMasked() {
	config := storedSensitiveConfig()
	config.Value = ""
	suite.mockService.On("Reset", config.Key).Return(nil)
	suite.mockService.On("GetByKey", config.Key).Return(&config, nil)

	w := suite.reset(config.Key)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	suite.assertMasked(suite.decodeConfiguration(w), false)
}

// A non-sensitive entry is unchanged by masking: it keeps its value and simply
// gains is_set.
func (suite *ConfigurationHandlerTestSuite) TestGetByKey_NonSensitiveEntryKeepsItsValue() {
	config := models.Configuration{Key: "general.company_name", Value: "GopherCRM", Type: models.ConfigTypeString}
	suite.mockService.On("GetByKey", config.Key).Return(&config, nil)

	w := suite.get("/configurations/" + config.Key)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	entry := suite.decodeConfiguration(w)
	assert.Equal(suite.T(), "GopherCRM", entry["value"])
	assert.Equal(suite.T(), false, entry["is_sensitive"])
	assert.Equal(suite.T(), true, entry["is_set"])
}

func TestConfigurationHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(ConfigurationHandlerTestSuite))
}
