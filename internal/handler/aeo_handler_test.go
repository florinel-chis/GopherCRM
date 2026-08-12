package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	openai "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Compile-time proof that the shared double still satisfies the interface it
// stands in for. The assertion lives here rather than in internal/mocks, which
// cannot import internal/service without creating an import cycle.
var _ service.AEOService = (*mocks.AEOService)(nil)

type AEOHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.AEOService
	handler     *AEOHandler
	role        models.UserRole
	router      *gin.Engine
}

func (suite *AEOHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)
}

func (suite *AEOHandlerTestSuite) SetupTest() {
	suite.mockService = new(mocks.AEOService)
	suite.handler = NewAEOHandler(suite.mockService)
	suite.role = models.RoleAdmin

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Set("user_role", string(suite.role))
		c.Next()
	})
	// The same minimal bind-error handler the other handler suites install, so
	// a validation failure comes back as 400 rather than an empty 200.
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	SetupAEORoutes(suite.router.Group(""), suite.handler)
}

func (suite *AEOHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *AEOHandlerTestSuite) do(method, path string, body interface{}) *httptest.ResponseRecorder {
	var reader *bytes.Buffer
	if body != nil {
		encoded, err := json.Marshal(body)
		suite.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	} else {
		reader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func validProfileBody() gin.H {
	return gin.H{
		"brand_name":    "Acme",
		"description":   "CRM vendor",
		"brand_aliases": []string{"Acme Inc"},
		"owned_domains": []string{"acme.com"},
		"competitors": []gin.H{
			{"name": "Globex", "aliases": []string{"Globex Corp"}, "domain": "globex.com"},
		},
	}
}

// ---------------------------------------------------------------- RBAC matrix

// aeoRoute describes one endpoint and the roles allowed to reach its handler.
type aeoRoute struct {
	method  string
	path    string
	body    interface{}
	allowed []models.UserRole
}

func aeoRoutes() []aeoRoute {
	read := []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport}
	write := []models.UserRole{models.RoleAdmin, models.RoleSales}
	adminOnly := []models.UserRole{models.RoleAdmin}

	return []aeoRoute{
		{http.MethodGet, "/aeo/profile", nil, read},
		{http.MethodPut, "/aeo/profile", validProfileBody(), write},
		{http.MethodGet, "/aeo/prompts", nil, read},
		{http.MethodPost, "/aeo/prompts", gin.H{"prompts": []string{"Which CRM?"}}, write},
		{http.MethodPost, "/aeo/prompts/generate", gin.H{"count": 5}, write},
		{http.MethodGet, "/aeo/prompts/1/answers", nil, read},
		{http.MethodPut, "/aeo/prompts/1", gin.H{"is_active": false}, write},
		{http.MethodDelete, "/aeo/prompts/1", nil, adminOnly},
		{http.MethodPost, "/aeo/prompts/1/run", nil, write},
		{http.MethodPost, "/aeo/runs", nil, write},
		{http.MethodGet, "/aeo/runs", nil, read},
		{http.MethodGet, "/aeo/runs/1", nil, read},
		{http.MethodGet, "/aeo/dashboard", nil, read},
		{http.MethodGet, "/aeo/citations", nil, read},
		{http.MethodGet, "/aeo/providers", nil, read},
	}
}

// allowEverything registers a permissive expectation for every service method
// so the RBAC matrix can assert on status codes without a bespoke fixture per
// route. Every expectation is optional: a route the matrix rejects must never
// reach the service at all.
func (suite *AEOHandlerTestSuite) allowEverything() {
	m := suite.mockService
	m.On("GetProfile").Return(&models.AEOProfile{BrandName: "Acme"}, nil).Maybe()
	m.On("SaveProfile", mock.Anything).Return(&models.AEOProfile{BrandName: "Acme"}, nil).Maybe()
	m.On("ListPrompts", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]models.AEOPrompt{}, int64(0), nil).Maybe()
	m.On("CreatePrompts", mock.Anything, mock.Anything).Return([]models.AEOPrompt{}, nil).Maybe()
	m.On("UpdatePrompt", mock.Anything, mock.Anything, mock.Anything).Return(&models.AEOPrompt{}, nil).Maybe()
	m.On("DeletePrompt", mock.Anything).Return(nil).Maybe()
	m.On("GeneratePrompts", mock.Anything, mock.Anything).Return([]string{}, nil).Maybe()
	m.On("GetPromptAnswers", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]models.AEOAnswer{}, int64(0), nil).Maybe()
	m.On("StartRun", mock.Anything, mock.Anything, mock.Anything).Return(&models.AEORun{}, nil).Maybe()
	m.On("StartPromptRun", mock.Anything, mock.Anything, mock.Anything).Return(&models.AEORun{}, nil).Maybe()
	m.On("ListRuns", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]models.AEORun{}, int64(0), nil).Maybe()
	m.On("GetRun", mock.Anything).Return(&models.AEORun{}, nil).Maybe()
	m.On("Dashboard", mock.Anything, mock.Anything).Return(&models.AEODashboard{}, nil).Maybe()
	m.On("Citations", mock.Anything, mock.Anything).Return(&models.AEOCitationsReport{}, nil).Maybe()
	m.On("Providers").Return([]models.AEOProviderStatus{}).Maybe()
}

// The customer role is rejected by the group guard on every route, and the
// stricter per-route guards keep support out of the mutations and everyone but
// admin out of the delete.
func (suite *AEOHandlerTestSuite) TestRBACMatrix() {
	suite.allowEverything()

	for _, route := range aeoRoutes() {
		for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
			suite.role = role

			permitted := false
			for _, allowed := range route.allowed {
				if allowed == role {
					permitted = true
				}
			}

			w := suite.do(route.method, route.path, route.body)
			if permitted {
				assert.NotEqualf(suite.T(), http.StatusForbidden, w.Code,
					"%s %s must be reachable by %s", route.method, route.path, role)
			} else {
				assert.Equalf(suite.T(), http.StatusForbidden, w.Code,
					"%s %s must be forbidden for %s", route.method, route.path, role)
			}
		}
	}
}

// A request that arrives without a role in the context — an unauthenticated
// path reaching the group by mistake — is refused rather than served.
func (suite *AEOHandlerTestSuite) TestRolelessRequestIsForbidden() {
	router := gin.New()
	SetupAEORoutes(router.Group(""), suite.handler)

	req := httptest.NewRequest(http.MethodGet, "/aeo/providers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Providers")
}

// -------------------------------------------------------------------- profile

func (suite *AEOHandlerTestSuite) TestGetProfile_Success() {
	suite.role = models.RoleSupport
	suite.mockService.On("GetProfile").Return(&models.AEOProfile{
		BaseModel:    models.BaseModel{ID: 1},
		BrandName:    "Acme",
		BrandAliases: []string{"Acme Inc"},
	}, nil)

	w := suite.do(http.MethodGet, "/aeo/profile", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	response := decodeResponse(suite.T(), w)
	assert.True(suite.T(), response.Success)
	assert.Contains(suite.T(), w.Body.String(), `"brand_name":"Acme"`)
}

// An unconfigured profile is a plain absence on this route only; every other
// route treats it as a precondition failure and answers 409.
func (suite *AEOHandlerTestSuite) TestGetProfile_UnconfiguredIs404() {
	suite.mockService.On("GetProfile").Return(nil, apperrors.ErrProfileNotConfigured)

	w := suite.do(http.MethodGet, "/aeo/profile", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeNotFound, decodeResponse(suite.T(), w).Error.Code)
}

func (suite *AEOHandlerTestSuite) TestGetProfile_ServiceFailureIsAServerError() {
	suite.mockService.On("GetProfile").Return(nil, errors.New("db down"))

	w := suite.do(http.MethodGet, "/aeo/profile", nil)
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "db down", "internal errors must not be echoed back")
}

func (suite *AEOHandlerTestSuite) TestSaveProfile_MapsTheBodyOntoTheModel() {
	suite.mockService.On("SaveProfile", mock.MatchedBy(func(p *models.AEOProfile) bool {
		return p.BrandName == "Acme" &&
			p.Description == "CRM vendor" &&
			len(p.BrandAliases) == 1 && p.BrandAliases[0] == "Acme Inc" &&
			len(p.OwnedDomains) == 1 && p.OwnedDomains[0] == "acme.com" &&
			len(p.Competitors) == 1 &&
			p.Competitors[0].Name == "Globex" &&
			p.Competitors[0].Domain == "globex.com" &&
			len(p.Competitors[0].Aliases) == 1
	})).Return(&models.AEOProfile{BaseModel: models.BaseModel{ID: 1}, BrandName: "Acme"}, nil)

	w := suite.do(http.MethodPut, "/aeo/profile", validProfileBody())
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.True(suite.T(), decodeResponse(suite.T(), w).Success)
}

func (suite *AEOHandlerTestSuite) TestSaveProfile_MalformedBodyIsRejectedAtBinding() {
	tooManyCompetitors := make([]gin.H, 0, 21)
	for i := 0; i < 21; i++ {
		tooManyCompetitors = append(tooManyCompetitors, gin.H{"name": fmt.Sprintf("Rival %d", i)})
	}
	tooManyAliases := make([]string, 21)
	for i := range tooManyAliases {
		tooManyAliases[i] = fmt.Sprintf("alias-%d", i)
	}

	for name, body := range map[string]gin.H{
		"missing brand name":     {"description": "no name"},
		"brand name too long":    {"brand_name": strings.Repeat("x", 121)},
		"description too long":   {"brand_name": "Acme", "description": strings.Repeat("x", 2001)},
		"too many competitors":   {"brand_name": "Acme", "competitors": tooManyCompetitors},
		"nameless competitor":    {"brand_name": "Acme", "competitors": []gin.H{{"domain": "globex.com"}}},
		"too many brand aliases": {"brand_name": "Acme", "brand_aliases": tooManyAliases},
	} {
		w := suite.do(http.MethodPut, "/aeo/profile", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s must be rejected", name)
	}
	suite.mockService.AssertNotCalled(suite.T(), "SaveProfile", mock.Anything)
}

// --------------------------------------------------------------------- prompts

func (suite *AEOHandlerTestSuite) TestListPrompts_DefaultsToA30DayWindow() {
	expectedTo := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)

	suite.mockService.On("ListPrompts",
		mock.MatchedBy(func(from time.Time) bool { return from.Equal(expectedTo.AddDate(0, 0, -30)) }),
		mock.MatchedBy(func(to time.Time) bool { return to.Equal(expectedTo) }),
		false, 0, 20, "", "desc").
		Return([]models.AEOPrompt{{
			BaseModel: models.BaseModel{ID: 4}, Text: "Which CRM?", IsActive: true, Visibility: 42.5,
		}}, int64(1), nil)

	w := suite.do(http.MethodGet, "/aeo/prompts", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"visibility":42.5`)

	response := decodeResponse(suite.T(), w)
	suite.Require().NotNil(response.Meta)
	assert.Equal(suite.T(), int64(1), response.Meta.Total)
	assert.Equal(suite.T(), 1, response.Meta.Page)
	assert.Equal(suite.T(), 20, response.Meta.PerPage)
}

func (suite *AEOHandlerTestSuite) TestListPrompts_HonoursTheSupportedWindows() {
	for _, days := range []int{7, 30, 90} {
		expectedTo := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
		expectedFrom := expectedTo.AddDate(0, 0, -days)

		suite.mockService.On("ListPrompts",
			mock.MatchedBy(func(from time.Time) bool { return from.Equal(expectedFrom) }),
			mock.Anything, true, 0, 20, "", "desc").
			Return([]models.AEOPrompt{}, int64(0), nil).Once()

		w := suite.do(http.MethodGet, fmt.Sprintf("/aeo/prompts?days=%d&active_only=true", days), nil)
		assert.Equalf(suite.T(), http.StatusOK, w.Code, "days=%d must be accepted", days)
	}
}

// An unsupported window is clamped to the default rather than rejected, the
// same way the other range parameters in this API behave.
func (suite *AEOHandlerTestSuite) TestListPrompts_UnsupportedWindowFallsBackTo30Days() {
	expectedTo := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)

	suite.mockService.On("ListPrompts",
		mock.MatchedBy(func(from time.Time) bool { return from.Equal(expectedTo.AddDate(0, 0, -30)) }),
		mock.Anything, false, 0, 20, "", "desc").
		Return([]models.AEOPrompt{}, int64(0), nil)

	w := suite.do(http.MethodGet, "/aeo/prompts?days=365", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *AEOHandlerTestSuite) TestListPrompts_UnknownSortColumnIs400() {
	w := suite.do(http.MethodGet, "/aeo/prompts?sort_by=deleted_at", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "ListPrompts",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (suite *AEOHandlerTestSuite) TestListPrompts_EmptyResultIsAnArrayNotNull() {
	suite.mockService.On("ListPrompts",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, int64(0), nil)

	w := suite.do(http.MethodGet, "/aeo/prompts", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"data":[]`)
}

func (suite *AEOHandlerTestSuite) TestCreatePrompts_Success() {
	suite.role = models.RoleSales
	suite.mockService.On("CreatePrompts", []string{"Which CRM?", "Best CRM for SMBs?"}, uint(7)).
		Return([]models.AEOPrompt{
			{BaseModel: models.BaseModel{ID: 1}, Text: "Which CRM?", IsActive: true},
			{BaseModel: models.BaseModel{ID: 2}, Text: "Best CRM for SMBs?", IsActive: true},
		}, nil)

	w := suite.do(http.MethodPost, "/aeo/prompts", gin.H{"prompts": []string{"Which CRM?", "Best CRM for SMBs?"}})
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
	assert.True(suite.T(), decodeResponse(suite.T(), w).Success)
}

func (suite *AEOHandlerTestSuite) TestCreatePrompts_DuplicateIs409() {
	suite.mockService.On("CreatePrompts", mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("prompt %q: %w", "Which CRM?", apperrors.ErrDuplicatePrompt))

	w := suite.do(http.MethodPost, "/aeo/prompts", gin.H{"prompts": []string{"Which CRM?"}})
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeConflict, decodeResponse(suite.T(), w).Error.Code)
}

// The cap on active prompts is a service-level limit rather than one of the
// shared sentinels, and it is a client mistake, so it must land on 400 with the
// validation code instead of a server error.
func (suite *AEOHandlerTestSuite) TestCreatePrompts_ActivePromptLimitIs400() {
	suite.mockService.On("CreatePrompts", mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("cannot add 1 prompt: %w", service.ErrAEOPromptLimit))

	w := suite.do(http.MethodPost, "/aeo/prompts", gin.H{"prompts": []string{"Which CRM?"}})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeValidation, decodeResponse(suite.T(), w).Error.Code)
}

// Whitespace-only text satisfies gin's `required` (the string is non-empty) but
// is empty once the service trims it. The resulting ErrAEOInvalidPrompt is a
// client mistake and must not surface as a 500.
func (suite *AEOHandlerTestSuite) TestCreatePrompts_BlankTextIs400() {
	suite.mockService.On("CreatePrompts", mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("prompt 1: %w", service.ErrAEOInvalidPrompt))

	w := suite.do(http.MethodPost, "/aeo/prompts", gin.H{"prompts": []string{"   "}})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeValidation, decodeResponse(suite.T(), w).Error.Code)
}

// Same trap on the profile: a blank brand name passes binding and fails the
// service's own validation.
func (suite *AEOHandlerTestSuite) TestSaveProfile_BlankBrandNameIs400() {
	suite.mockService.On("SaveProfile", mock.Anything).
		Return(nil, service.ErrAEOInvalidProfile)

	w := suite.do(http.MethodPut, "/aeo/profile", gin.H{"brand_name": "   "})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeValidation, decodeResponse(suite.T(), w).Error.Code)
}

func (suite *AEOHandlerTestSuite) TestCreatePrompts_MalformedBodyIsRejectedAtBinding() {
	tooMany := make([]string, 26)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("prompt %d", i)
	}

	for name, body := range map[string]gin.H{
		"no prompts key":  {},
		"empty list":      {"prompts": []string{}},
		"blank prompt":    {"prompts": []string{""}},
		"prompt too long": {"prompts": []string{strings.Repeat("x", 501)}},
		"too many":        {"prompts": tooMany},
	} {
		w := suite.do(http.MethodPost, "/aeo/prompts", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s must be rejected", name)
	}
	suite.mockService.AssertNotCalled(suite.T(), "CreatePrompts", mock.Anything, mock.Anything)
}

// Absent fields must stay absent: deactivating a prompt cannot be allowed to
// blank its text, which is why both fields travel as pointers.
func (suite *AEOHandlerTestSuite) TestUpdatePrompt_OnlySendsThePresentFields() {
	suite.mockService.On("UpdatePrompt", uint(3),
		mock.MatchedBy(func(text *string) bool { return text == nil }),
		mock.MatchedBy(func(active *bool) bool { return active != nil && !*active }),
	).Return(&models.AEOPrompt{BaseModel: models.BaseModel{ID: 3}, Text: "Which CRM?"}, nil)

	w := suite.do(http.MethodPut, "/aeo/prompts/3", gin.H{"is_active": false})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *AEOHandlerTestSuite) TestUpdatePrompt_TextOnly() {
	suite.mockService.On("UpdatePrompt", uint(3),
		mock.MatchedBy(func(text *string) bool { return text != nil && *text == "Renamed" }),
		mock.MatchedBy(func(active *bool) bool { return active == nil }),
	).Return(&models.AEOPrompt{BaseModel: models.BaseModel{ID: 3}, Text: "Renamed"}, nil)

	w := suite.do(http.MethodPut, "/aeo/prompts/3", gin.H{"text": "Renamed"})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *AEOHandlerTestSuite) TestUpdatePrompt_BlankTextIs400() {
	w := suite.do(http.MethodPut, "/aeo/prompts/3", gin.H{"text": "   "})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "UpdatePrompt", mock.Anything, mock.Anything, mock.Anything)
}

func (suite *AEOHandlerTestSuite) TestUpdatePrompt_InvalidIDIs400() {
	w := suite.do(http.MethodPut, "/aeo/prompts/abc", gin.H{"is_active": true})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "UpdatePrompt", mock.Anything, mock.Anything, mock.Anything)
}

func (suite *AEOHandlerTestSuite) TestUpdatePrompt_MissingPromptIs404() {
	suite.mockService.On("UpdatePrompt", uint(3), mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("aeo prompt 3 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodPut, "/aeo/prompts/3", gin.H{"is_active": true})
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *AEOHandlerTestSuite) TestUpdatePrompt_DuplicateTextIs409() {
	suite.mockService.On("UpdatePrompt", uint(3), mock.Anything, mock.Anything).
		Return(nil, apperrors.ErrDuplicatePrompt)

	w := suite.do(http.MethodPut, "/aeo/prompts/3", gin.H{"text": "Taken"})
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
}

func (suite *AEOHandlerTestSuite) TestDeletePrompt_Success() {
	suite.mockService.On("DeletePrompt", uint(3)).Return(nil)

	w := suite.do(http.MethodDelete, "/aeo/prompts/3", nil)
	assert.Equal(suite.T(), http.StatusNoContent, w.Code)
	assert.Empty(suite.T(), w.Body.String())
}

func (suite *AEOHandlerTestSuite) TestDeletePrompt_MissingPromptIs404() {
	suite.mockService.On("DeletePrompt", uint(3)).
		Return(fmt.Errorf("aeo prompt 3 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodDelete, "/aeo/prompts/3", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// ------------------------------------------------------------------ generation

func (suite *AEOHandlerTestSuite) TestGeneratePrompts_EmptyBodyUsesTheDefaultCount() {
	suite.mockService.On("GeneratePrompts", mock.Anything, 10).
		Return([]string{"Which CRM would you recommend?"}, nil)

	w := suite.do(http.MethodPost, "/aeo/prompts/generate", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"prompts":["Which CRM would you recommend?"]`)
}

func (suite *AEOHandlerTestSuite) TestGeneratePrompts_ExplicitCount() {
	suite.mockService.On("GeneratePrompts", mock.Anything, 5).Return([]string{}, nil)

	w := suite.do(http.MethodPost, "/aeo/prompts/generate", gin.H{"count": 5})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"prompts":[]`)
}

func (suite *AEOHandlerTestSuite) TestGeneratePrompts_OutOfRangeCountIs400() {
	for _, count := range []int{-1, 26} {
		w := suite.do(http.MethodPost, "/aeo/prompts/generate", gin.H{"count": count})
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "count=%d must be rejected", count)
	}
	suite.mockService.AssertNotCalled(suite.T(), "GeneratePrompts", mock.Anything, mock.Anything)
}

// Generation runs on one specific provider, so a missing key here is reported
// with its own code rather than the module-wide PROVIDERS_UNAVAILABLE. The
// service names the selected engine in its wrap; the handler must pass that
// message through verbatim.
func (suite *AEOHandlerTestSuite) TestGeneratePrompts_MissingProviderIs503() {
	suite.mockService.On("GeneratePrompts", mock.Anything, 10).
		Return(nil, fmt.Errorf(
			"prompt generation runs on the gemini engine and no gemini API key is configured: %w",
			apperrors.ErrGenerationProviderNotConfigured))

	w := suite.do(http.MethodPost, "/aeo/prompts/generate", nil)
	assert.Equal(suite.T(), http.StatusServiceUnavailable, w.Code)
	resp := decodeResponse(suite.T(), w)
	assert.Equal(suite.T(), "PROVIDER_NOT_CONFIGURED", resp.Error.Code)
	assert.Contains(suite.T(), resp.Error.Message, "gemini")
}

// A wrong or out-of-quota key on a configured engine answers 503 with a
// message pointing at the key, not a bare 500. 503 rather than 502 because
// fronting proxies replace origin 502 bodies with their own error page.
func (suite *AEOHandlerTestSuite) TestGeneratePrompts_ProviderRejectionIs503() {
	suite.mockService.On("GeneratePrompts", mock.Anything, 10).
		Return(nil, fmt.Errorf("gemini: %w", &openai.Error{StatusCode: 400}))

	w := suite.do(http.MethodPost, "/aeo/prompts/generate", nil)
	assert.Equal(suite.T(), http.StatusServiceUnavailable, w.Code)
	resp := decodeResponse(suite.T(), w)
	assert.Equal(suite.T(), "PROVIDER_REJECTED", resp.Error.Code)
	assert.Contains(suite.T(), resp.Error.Message, "API key")
	assert.Contains(suite.T(), resp.Error.Message, "400")
}

func (suite *AEOHandlerTestSuite) TestRunPrompt_Accepted() {
	run := &models.AEORun{Trigger: "manual", Status: "running", TotalQueries: 1}
	run.ID = 3
	suite.mockService.On("StartPromptRun", mock.Anything, uint(5), mock.Anything).Return(run, nil)

	w := suite.do(http.MethodPost, "/aeo/prompts/5/run", nil)
	assert.Equal(suite.T(), http.StatusAccepted, w.Code)
}

func (suite *AEOHandlerTestSuite) TestRunPrompt_UnknownPromptIs404() {
	suite.mockService.On("StartPromptRun", mock.Anything, uint(99), mock.Anything).
		Return(nil, fmt.Errorf("prompt 99 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodPost, "/aeo/prompts/99/run", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *AEOHandlerTestSuite) TestRunPrompt_BadIDIs400() {
	w := suite.do(http.MethodPost, "/aeo/prompts/zero/run", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "StartPromptRun")
}

func (suite *AEOHandlerTestSuite) TestGeneratePrompts_MissingProfileIs409() {
	suite.mockService.On("GeneratePrompts", mock.Anything, 10).
		Return(nil, apperrors.ErrProfileNotConfigured)

	w := suite.do(http.MethodPost, "/aeo/prompts/generate", nil)
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeConflict, decodeResponse(suite.T(), w).Error.Code)
}

// --------------------------------------------------------------------- answers

func (suite *AEOHandlerTestSuite) TestListPromptAnswers_AllRuns() {
	suite.role = models.RoleSupport
	suite.mockService.On("GetPromptAnswers", uint(4),
		mock.MatchedBy(func(runID *uint) bool { return runID == nil }), 0, 20).
		Return([]models.AEOAnswer{{
			BaseModel: models.BaseModel{ID: 9}, RunID: 2, PromptID: 4, Provider: "openai",
			BrandMentioned: true, FirstMentionPos: 12,
			Citations: []models.AEOCitation{{BaseModel: models.BaseModel{ID: 1}, AnswerID: 9, Domain: "acme.com"}},
		}}, int64(1), nil)

	w := suite.do(http.MethodGet, "/aeo/prompts/4/answers", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"citations":[`)
	assert.Equal(suite.T(), int64(1), decodeResponse(suite.T(), w).Meta.Total)
}

func (suite *AEOHandlerTestSuite) TestListPromptAnswers_FilteredByRun() {
	suite.mockService.On("GetPromptAnswers", uint(4),
		mock.MatchedBy(func(runID *uint) bool { return runID != nil && *runID == 2 }), 0, 50).
		Return([]models.AEOAnswer{}, int64(0), nil)

	w := suite.do(http.MethodGet, "/aeo/prompts/4/answers?run_id=2&limit=50", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *AEOHandlerTestSuite) TestListPromptAnswers_InvalidIDsAre400() {
	for _, path := range []string{"/aeo/prompts/abc/answers", "/aeo/prompts/4/answers?run_id=abc"} {
		w := suite.do(http.MethodGet, path, nil)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s must be rejected", path)
	}
	suite.mockService.AssertNotCalled(suite.T(), "GetPromptAnswers",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (suite *AEOHandlerTestSuite) TestListPromptAnswers_MissingPromptIs404() {
	suite.mockService.On("GetPromptAnswers", uint(4), mock.Anything, 0, 20).
		Return(nil, int64(0), fmt.Errorf("aeo prompt 4 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodGet, "/aeo/prompts/4/answers", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// ------------------------------------------------------------------------ runs

func (suite *AEOHandlerTestSuite) TestCreateRun_Accepted() {
	suite.mockService.On("StartRun", mock.Anything, "manual",
		mock.MatchedBy(func(userID *uint) bool { return userID != nil && *userID == 7 })).
		Return(&models.AEORun{BaseModel: models.BaseModel{ID: 5}, Trigger: "manual", Status: "running"}, nil)

	w := suite.do(http.MethodPost, "/aeo/runs", nil)
	assert.Equal(suite.T(), http.StatusAccepted, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"status":"running"`)
}

func (suite *AEOHandlerTestSuite) TestCreateRun_SentinelMappings() {
	cases := []struct {
		name         string
		err          error
		expectedCode int
		errorCode    string
	}{
		{"run in progress", apperrors.ErrRunInProgress, http.StatusConflict, utils.ErrCodeConflict},
		{"profile missing", apperrors.ErrProfileNotConfigured, http.StatusConflict, utils.ErrCodeConflict},
		{"no providers", apperrors.ErrNoProvidersConfigured, http.StatusServiceUnavailable, "PROVIDERS_UNAVAILABLE"},
		{"no active prompts", fmt.Errorf("no active AEO prompts: %w", apperrors.ErrNotFound), http.StatusNotFound, utils.ErrCodeNotFound},
		{"unclassified", errors.New("db down"), http.StatusInternalServerError, utils.ErrCodeInternal},
	}

	for _, tc := range cases {
		suite.mockService.On("StartRun", mock.Anything, "manual", mock.Anything).Return(nil, tc.err).Once()

		w := suite.do(http.MethodPost, "/aeo/runs", nil)
		assert.Equalf(suite.T(), tc.expectedCode, w.Code, "%s must map to %d", tc.name, tc.expectedCode)
		assert.Equalf(suite.T(), tc.errorCode, decodeResponse(suite.T(), w).Error.Code, "%s error code", tc.name)
	}
}

func (suite *AEOHandlerTestSuite) TestListRuns_Success() {
	suite.mockService.On("ListRuns", 0, 20, "", "desc").
		Return([]models.AEORun{{BaseModel: models.BaseModel{ID: 5}, Status: "completed", Trigger: "scheduled"}}, int64(1), nil)

	w := suite.do(http.MethodGet, "/aeo/runs", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Equal(suite.T(), int64(1), decodeResponse(suite.T(), w).Meta.Total)
}

func (suite *AEOHandlerTestSuite) TestListRuns_SortIsValidated() {
	w := suite.do(http.MethodGet, "/aeo/runs?sort_by=answer_text", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	suite.mockService.On("ListRuns", 0, 20, "started_at", "asc").
		Return([]models.AEORun{}, int64(0), nil)
	w = suite.do(http.MethodGet, "/aeo/runs?sort_by=started_at&sort_order=asc", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *AEOHandlerTestSuite) TestGetRun_Success() {
	suite.mockService.On("GetRun", uint(5)).
		Return(&models.AEORun{BaseModel: models.BaseModel{ID: 5}, Status: "partial", FailedQueries: 2}, nil)

	w := suite.do(http.MethodGet, "/aeo/runs/5", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"failed_queries":2`)
}

func (suite *AEOHandlerTestSuite) TestGetRun_InvalidIDIs400() {
	w := suite.do(http.MethodGet, "/aeo/runs/abc", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "GetRun", mock.Anything)
}

func (suite *AEOHandlerTestSuite) TestGetRun_MissingRunIs404() {
	suite.mockService.On("GetRun", uint(5)).
		Return(nil, fmt.Errorf("aeo run 5 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodGet, "/aeo/runs/5", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// ------------------------------------------------------------------- reporting

func (suite *AEOHandlerTestSuite) TestGetDashboard_PassesTheRequestedWindow() {
	expectedTo := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)

	suite.mockService.On("Dashboard",
		mock.MatchedBy(func(from time.Time) bool { return from.Equal(expectedTo.AddDate(0, 0, -7)) }),
		mock.MatchedBy(func(to time.Time) bool { return to.Equal(expectedTo) })).
		Return(&models.AEODashboard{Days: 7, Visibility: 33.3, TotalAnswers: 9}, nil)

	w := suite.do(http.MethodGet, "/aeo/dashboard?days=7", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"visibility":33.3`)
}

func (suite *AEOHandlerTestSuite) TestGetDashboard_MissingProfileIs409() {
	suite.mockService.On("Dashboard", mock.Anything, mock.Anything).
		Return(nil, apperrors.ErrProfileNotConfigured)

	w := suite.do(http.MethodGet, "/aeo/dashboard", nil)
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
}

func (suite *AEOHandlerTestSuite) TestGetCitations_Success() {
	suite.role = models.RoleSupport
	expectedTo := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)

	suite.mockService.On("Citations",
		mock.MatchedBy(func(from time.Time) bool { return from.Equal(expectedTo.AddDate(0, 0, -90)) }),
		mock.Anything).
		Return(&models.AEOCitationsReport{TotalCitations: 12, OwnedCitationRate: 25.0}, nil)

	w := suite.do(http.MethodGet, "/aeo/citations?days=90", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"total_citations":12`)
}

func (suite *AEOHandlerTestSuite) TestGetProviders_Success() {
	suite.role = models.RoleSupport
	suite.mockService.On("Providers").Return([]models.AEOProviderStatus{
		{Name: "anthropic", Model: "claude-opus-5", Configured: true},
		{Name: "kimi", Model: "moonshot-v1-8k", Configured: false},
	})

	w := suite.do(http.MethodGet, "/aeo/providers", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"configured":false`)
	assert.NotContains(suite.T(), w.Body.String(), "api_key")
}

func TestAEOHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(AEOHandlerTestSuite))
}

// The AEO group registers a static /prompts/generate alongside the parameter
// route /prompts/:id, and gin builds one tree per method, so the conflict this
// guards against would only appear once every Setup* runs together the way
// cmd/main.go mounts them. routes_test.go owns the shared smoke test; this one
// covers the AEO additions specifically.
func TestAEORoutesCoexistWithExistingRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1")

	SetupUserRoutes(group, &UserHandler{})
	SetupLeadRoutes(group, &LeadHandler{})
	SetupCustomerRoutes(group, &CustomerHandler{})
	SetupTicketRoutes(group, &TicketHandler{})
	SetupTaskRoutes(group, &TaskHandler{})
	SetupLabelRoutes(group, &LabelHandler{})
	SetupAPIKeyRoutes(group, &APIKeyHandler{})
	SetupConfigurationRoutes(group, &ConfigurationHandler{})
	SetupDashboardRoutes(group, &DashboardHandler{})
	SetupBulkStatusRoutes(group, &BulkHandler{})
	SetupAEORoutes(group, &AEOHandler{})

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/aeo/profile"},
		{http.MethodPut, "/api/v1/aeo/profile"},
		{http.MethodGet, "/api/v1/aeo/prompts"},
		{http.MethodPost, "/api/v1/aeo/prompts"},
		{http.MethodPost, "/api/v1/aeo/prompts/generate"},
		{http.MethodGet, "/api/v1/aeo/prompts/1/answers"},
		{http.MethodPut, "/api/v1/aeo/prompts/1"},
		{http.MethodDelete, "/api/v1/aeo/prompts/1"},
		{http.MethodPost, "/api/v1/aeo/runs"},
		{http.MethodGet, "/api/v1/aeo/runs"},
		{http.MethodGet, "/api/v1/aeo/runs/1"},
		{http.MethodGet, "/api/v1/aeo/dashboard"},
		{http.MethodGet, "/api/v1/aeo/citations"},
		{http.MethodGet, "/api/v1/aeo/providers"},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }()
			router.ServeHTTP(w, req)
		}()
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s did not match any route", route.method, route.path)
		}
	}
}
