package handler

import (
	"bytes"
	"encoding/json"
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
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TaskLabelHandlerTestSuite covers the label-aware parts of the task handler:
// the label_ids field on create/update and the ?label_id= list filter. It
// re-uses MockTaskService from task_handler_test.go.
type TaskLabelHandlerTestSuite struct {
	suite.Suite
	mockService *MockTaskService
	role        models.UserRole
	userID      uint
	router      *gin.Engine
}

func (suite *TaskLabelHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)
}

func (suite *TaskLabelHandlerTestSuite) SetupTest() {
	suite.mockService = new(MockTaskService)
	suite.role = models.RoleAdmin
	suite.userID = 1

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", suite.userID)
		c.Set("user_role", string(suite.role))
		c.Next()
	})
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	SetupTaskRoutes(suite.router.Group(""), NewTaskHandler(suite.mockService))
}

func (suite *TaskLabelHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *TaskLabelHandlerTestSuite) do(method, path string, body interface{}) *httptest.ResponseRecorder {
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		suite.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func (suite *TaskLabelHandlerTestSuite) TestCreate_PassesLabelIDsThrough() {
	suite.mockService.On("CreateWithLabels", mock.AnythingOfType("*models.Task"), []uint{4, 9}).Return(nil)

	w := suite.do(http.MethodPost, "/tasks", gin.H{
		"title":          "Labelled",
		"assigned_to_id": 1,
		"label_ids":      []uint{4, 9},
	})
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

// An unknown label id is a bad reference in the payload, not a missing
// resource at the requested path, so it must be a 400 carrying the
// INVALID_REFERENCE code rather than a 404.
func (suite *TaskLabelHandlerTestSuite) TestCreate_UnknownLabelIs400InvalidReference() {
	suite.mockService.On("CreateWithLabels", mock.Anything, []uint{99}).
		Return(fmt.Errorf("unknown label ids [99]: %w", apperrors.ErrLabelNotFound))

	w := suite.do(http.MethodPost, "/tasks", gin.H{
		"title":          "Bad ref",
		"assigned_to_id": 1,
		"label_ids":      []uint{99},
	})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	var response utils.APIResponse
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	suite.Require().NotNil(response.Error)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, response.Error.Code)
	assert.Contains(suite.T(), response.Error.Message, "99")
}

// The pointer in the DTO is what distinguishes the three cases; if it collapsed
// to a plain slice, an edit that never mentioned labels would clear them.
func (suite *TaskLabelHandlerTestSuite) TestUpdate_OmittedFieldLeavesLabelsAlone() {
	suite.mockService.On("GetByID", uint(1)).Return(&models.Task{
		BaseModel: models.BaseModel{ID: 1}, Title: "Existing", AssignedToID: 1,
	}, nil)
	suite.mockService.On("UpdateWithLabels", mock.AnythingOfType("*models.Task"), (*[]uint)(nil)).Return(nil)

	w := suite.do(http.MethodPut, "/tasks/1", gin.H{"title": "Renamed"})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskLabelHandlerTestSuite) TestUpdate_EmptyArrayClearsLabels() {
	suite.mockService.On("GetByID", uint(1)).Return(&models.Task{
		BaseModel: models.BaseModel{ID: 1}, Title: "Existing", AssignedToID: 1,
	}, nil)
	suite.mockService.On("UpdateWithLabels", mock.AnythingOfType("*models.Task"),
		mock.MatchedBy(func(ids *[]uint) bool {
			return ids != nil && len(*ids) == 0
		})).Return(nil)

	w := suite.do(http.MethodPut, "/tasks/1", gin.H{"label_ids": []uint{}})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskLabelHandlerTestSuite) TestUpdate_PresentArrayReplacesLabels() {
	suite.mockService.On("GetByID", uint(1)).Return(&models.Task{
		BaseModel: models.BaseModel{ID: 1}, Title: "Existing", AssignedToID: 1,
	}, nil)
	suite.mockService.On("UpdateWithLabels", mock.AnythingOfType("*models.Task"),
		mock.MatchedBy(func(ids *[]uint) bool {
			return ids != nil && len(*ids) == 2 && (*ids)[0] == 3 && (*ids)[1] == 5
		})).Return(nil)

	w := suite.do(http.MethodPut, "/tasks/1", gin.H{"label_ids": []uint{3, 5}})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskLabelHandlerTestSuite) TestUpdate_UnknownLabelIs400InvalidReference() {
	suite.mockService.On("GetByID", uint(1)).Return(&models.Task{
		BaseModel: models.BaseModel{ID: 1}, Title: "Existing", AssignedToID: 1,
	}, nil)
	suite.mockService.On("UpdateWithLabels", mock.Anything, mock.Anything).
		Return(fmt.Errorf("unknown label ids [99]: %w", apperrors.ErrLabelNotFound))

	w := suite.do(http.MethodPut, "/tasks/1", gin.H{"label_ids": []uint{99}})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// label_ids is capped at the same 100 ids the bulk endpoints allow, so a single
// request cannot expand into an unbounded IN clause. The cap is enforced by the
// binding tag, which means the service is never reached.
func (suite *TaskLabelHandlerTestSuite) TestCreate_RejectsMoreThanOneHundredLabelIDs() {
	ids := make([]uint, 101)
	for i := range ids {
		ids[i] = uint(i + 1)
	}

	w := suite.do(http.MethodPost, "/tasks", gin.H{
		"title":          "Too many labels",
		"assigned_to_id": 1,
		"label_ids":      ids,
	})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "CreateWithLabels", mock.Anything, mock.Anything)
}

func (suite *TaskLabelHandlerTestSuite) TestCreate_AcceptsExactlyOneHundredLabelIDs() {
	ids := make([]uint, 100)
	for i := range ids {
		ids[i] = uint(i + 1)
	}
	suite.mockService.On("CreateWithLabels", mock.AnythingOfType("*models.Task"), ids).Return(nil)

	w := suite.do(http.MethodPost, "/tasks", gin.H{
		"title":          "At the cap",
		"assigned_to_id": 1,
		"label_ids":      ids,
	})
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

// The same cap applies through the pointer field on update.
func (suite *TaskLabelHandlerTestSuite) TestUpdate_RejectsMoreThanOneHundredLabelIDs() {
	ids := make([]uint, 101)
	for i := range ids {
		ids[i] = uint(i + 1)
	}

	w := suite.do(http.MethodPut, "/tasks/1", gin.H{"label_ids": ids})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "UpdateWithLabels", mock.Anything, mock.Anything)
}

// Zero is not a usable label id; rejecting it at the binding keeps the id list
// consistent with every other multi-id payload in the API.
func (suite *TaskLabelHandlerTestSuite) TestCreate_RejectsZeroLabelID() {
	w := suite.do(http.MethodPost, "/tasks", gin.H{
		"title":          "Zero id",
		"assigned_to_id": 1,
		"label_ids":      []uint{0},
	})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "CreateWithLabels", mock.Anything, mock.Anything)
}

func (suite *TaskLabelHandlerTestSuite) TestUpdate_RejectsZeroLabelID() {
	w := suite.do(http.MethodPut, "/tasks/1", gin.H{"label_ids": []uint{0}})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "UpdateWithLabels", mock.Anything, mock.Anything)
}

func (suite *TaskLabelHandlerTestSuite) TestList_LabelFilterForAdmin() {
	suite.mockService.On("ListByLabel", uint(7), 0, 20, "title", "asc").
		Return([]models.Task{{BaseModel: models.BaseModel{ID: 1}}}, int64(1), nil)

	w := suite.do(http.MethodGet, "/tasks?label_id=7&sort_by=title&sort_order=asc", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

// The label filter narrows the same list a non-admin already sees; it must not
// widen it to other people's tasks.
func (suite *TaskLabelHandlerTestSuite) TestList_LabelFilterStaysScopedForNonAdmins() {
	// The context middleware reads these at request time.
	suite.role = models.RoleSupport
	suite.userID = 12

	suite.mockService.On("ListByLabelForAssignee", uint(12), uint(7), 0, 20, "", "asc").
		Return([]models.Task{}, int64(0), nil)

	w := suite.do(http.MethodGet, "/tasks?label_id=7", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "ListByLabel", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// Documented behaviour: label_id wins over search rather than the two being
// combined.
func (suite *TaskLabelHandlerTestSuite) TestList_LabelFilterTakesPrecedenceOverSearch() {
	suite.mockService.On("ListByLabel", uint(7), 0, 20, "", "asc").
		Return([]models.Task{}, int64(0), nil)

	w := suite.do(http.MethodGet, "/tasks?label_id=7&search=anything", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Search", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// A malformed or non-positive label_id is ignored, exactly like a sort_by that
// is not on the allowlist — the list still renders instead of erroring.
func (suite *TaskLabelHandlerTestSuite) TestList_MalformedLabelIDIsIgnored() {
	suite.mockService.On("List", 0, 20).Return([]models.Task{}, int64(0), nil).Twice()

	for _, query := range []string{"/tasks?label_id=abc", "/tasks?label_id=0"} {
		w := suite.do(http.MethodGet, query, nil)
		assert.Equalf(suite.T(), http.StatusOK, w.Code, "%s must still list", query)
	}
	suite.mockService.AssertNotCalled(suite.T(), "ListByLabel", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestTaskLabelHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(TaskLabelHandlerTestSuite))
}
