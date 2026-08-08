package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
var _ service.LabelService = (*mocks.LabelService)(nil)

type LabelHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.LabelService
	handler     *LabelHandler
	role        models.UserRole
	router      *gin.Engine
}

func (suite *LabelHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)
}

func (suite *LabelHandlerTestSuite) SetupTest() {
	suite.mockService = new(mocks.LabelService)
	suite.handler = NewLabelHandler(suite.mockService)
	suite.role = models.RoleAdmin

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
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
	SetupLabelRoutes(suite.router.Group(""), suite.handler)
}

func (suite *LabelHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *LabelHandlerTestSuite) do(method, path string, body interface{}) *httptest.ResponseRecorder {
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

func decodeResponse(t assert.TestingT, w *httptest.ResponseRecorder) utils.APIResponse {
	var response utils.APIResponse
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	return response
}

func (suite *LabelHandlerTestSuite) TestList_ReturnsLabelsWithTaskCounts() {
	suite.role = models.RoleCustomer // reading labels is open to every role
	suite.mockService.On("List").Return([]models.Label{
		{BaseModel: models.BaseModel{ID: 1}, Name: "alpha", Color: "#111111", TaskCount: 4},
	}, nil)

	w := suite.do(http.MethodGet, "/labels", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"task_count":4`)
}

func (suite *LabelHandlerTestSuite) TestList_ServiceFailureIsAServerError() {
	suite.mockService.On("List").Return(nil, errors.New("db down"))

	w := suite.do(http.MethodGet, "/labels", nil)
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "db down", "internal errors must not be echoed back")
}

func (suite *LabelHandlerTestSuite) TestCreate_Success() {
	suite.mockService.On("Create", mock.MatchedBy(func(l *models.Label) bool {
		return l.Name == "Urgent" && l.Color == "#FF0000"
	})).Return(nil)

	w := suite.do(http.MethodPost, "/labels", gin.H{"name": "Urgent", "color": "#FF0000"})
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
	assert.True(suite.T(), decodeResponse(suite.T(), w).Success)
}

func (suite *LabelHandlerTestSuite) TestCreate_DuplicateNameIs409() {
	suite.mockService.On("Create", mock.Anything).
		Return(fmt.Errorf("label %q already exists: %w", "Urgent", apperrors.ErrDuplicateLabelName))

	w := suite.do(http.MethodPost, "/labels", gin.H{"name": "Urgent", "color": "#FF0000"})
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeConflict, decodeResponse(suite.T(), w).Error.Code)
}

// The binding tags reject every malformed colour this API can receive, so the
// service-level sentinels are the second line of defence. The handler still has
// to map them, and it must map them to 400 rather than to a server error.
func (suite *LabelHandlerTestSuite) TestValidationSentinelsAre400() {
	for _, sentinel := range []error{apperrors.ErrInvalidLabelColor, apperrors.ErrInvalidLabelName} {
		existing := &models.Label{BaseModel: models.BaseModel{ID: 3}, Name: "Urgent", Color: "#FF0000"}
		suite.mockService.On("GetByID", uint(3)).Return(existing, nil).Once()
		suite.mockService.On("Update", mock.Anything).Return(sentinel).Once()

		w := suite.do(http.MethodPut, "/labels/3", gin.H{"color": "#00FF00"})
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%v must be answered with 400", sentinel)
	}
}

// #RGB shorthand and a missing hash are rejected by the binding tags, before
// the service is ever called.
func (suite *LabelHandlerTestSuite) TestCreate_MalformedBodyIsRejectedAtBinding() {
	for _, body := range []gin.H{
		{"name": "Urgent", "color": "#FFF"},
		{"name": "Urgent", "color": "FF0000"},
		{"name": "Urgent"},
		{"color": "#FF0000"},
		{"name": "0123456789012345678901234567890123456789012345678901", "color": "#FF0000"},
	} {
		w := suite.do(http.MethodPost, "/labels", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "body %v must be rejected", body)
	}
	suite.mockService.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

func (suite *LabelHandlerTestSuite) TestCreate_CustomerRoleIsForbidden() {
	suite.role = models.RoleCustomer

	w := suite.do(http.MethodPost, "/labels", gin.H{"name": "Urgent", "color": "#FF0000"})
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

func (suite *LabelHandlerTestSuite) TestCreate_SalesAndSupportAreAllowed() {
	for _, role := range []models.UserRole{models.RoleSales, models.RoleSupport} {
		suite.role = role
		suite.mockService.On("Create", mock.Anything).Return(nil).Once()

		w := suite.do(http.MethodPost, "/labels", gin.H{"name": "Urgent", "color": "#FF0000"})
		assert.Equalf(suite.T(), http.StatusCreated, w.Code, "role %s must be allowed to create labels", role)
	}
}

func (suite *LabelHandlerTestSuite) TestUpdate_AppliesOnlyThePresentFields() {
	existing := &models.Label{BaseModel: models.BaseModel{ID: 3}, Name: "Urgent", Color: "#FF0000"}
	suite.mockService.On("GetByID", uint(3)).Return(existing, nil)
	suite.mockService.On("Update", mock.MatchedBy(func(l *models.Label) bool {
		// The name was not sent, so it must survive the recolour.
		return l.ID == 3 && l.Name == "Urgent" && l.Color == "#00FF00"
	})).Return(nil)

	w := suite.do(http.MethodPut, "/labels/3", gin.H{"color": "#00FF00"})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *LabelHandlerTestSuite) TestUpdate_MissingLabelIs404() {
	suite.mockService.On("GetByID", uint(3)).
		Return(nil, fmt.Errorf("label 3 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodPut, "/labels/3", gin.H{"name": "Renamed"})
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Update", mock.Anything)
}

func (suite *LabelHandlerTestSuite) TestUpdate_DuplicateNameIs409() {
	existing := &models.Label{BaseModel: models.BaseModel{ID: 3}, Name: "Urgent", Color: "#FF0000"}
	suite.mockService.On("GetByID", uint(3)).Return(existing, nil)
	suite.mockService.On("Update", mock.Anything).Return(apperrors.ErrDuplicateLabelName)

	w := suite.do(http.MethodPut, "/labels/3", gin.H{"name": "Taken"})
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
}

func (suite *LabelHandlerTestSuite) TestUpdate_InvalidIDIs400() {
	w := suite.do(http.MethodPut, "/labels/abc", gin.H{"name": "Renamed"})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "GetByID", mock.Anything)
}

func (suite *LabelHandlerTestSuite) TestDelete_Success() {
	suite.mockService.On("Delete", uint(3)).Return(nil)

	w := suite.do(http.MethodDelete, "/labels/3", nil)
	assert.Equal(suite.T(), http.StatusNoContent, w.Code)
	assert.Empty(suite.T(), w.Body.String())
}

func (suite *LabelHandlerTestSuite) TestDelete_MissingLabelIs404() {
	suite.mockService.On("Delete", uint(3)).
		Return(fmt.Errorf("label 3 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodDelete, "/labels/3", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// Deleting a label detaches it from every task at once, so it is admin-only —
// including for the two roles that may create and edit labels.
func (suite *LabelHandlerTestSuite) TestDelete_NonAdminIsForbidden() {
	for _, role := range []models.UserRole{models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		suite.role = role

		w := suite.do(http.MethodDelete, "/labels/3", nil)
		assert.Equalf(suite.T(), http.StatusForbidden, w.Code, "role %s must not delete labels", role)
	}
	suite.mockService.AssertNotCalled(suite.T(), "Delete", mock.Anything)
}

func TestLabelHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(LabelHandlerTestSuite))
}
