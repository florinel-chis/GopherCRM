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
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// fakeFormService is a hand-written double: the form service has a wide
// surface, and every one of these tests cares about at most one method, so
// overridable function fields read better here than a full mock with
// expectations. Unset hooks answer with the zero-value success a route needs to
// get past its authorization check.
type fakeFormService struct {
	createFn              func(form *models.Form, actorID uint) error
	getByIDFn             func(id uint) (*models.Form, error)
	listFn                func(offset, limit int, status, sortBy, sortOrder string) ([]models.Form, map[uint]int64, int64, error)
	updateFn              func(id uint, form *models.Form) error
	deleteFn              func(id uint) error
	listSubmissionsFn     func(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error)
	getSubmissionFn       func(id uint) (*models.FormSubmission, error)
	createdActorID        uint
	createdForm           *models.Form
	updatedForm           *models.Form
	deletedID             uint
	listedStatus          string
	listedSortBy          string
	submissionsFormID     uint
	submissionsStatus     string
	requestedSubmissionID uint
}

var _ service.FormService = (*fakeFormService)(nil)

func (f *fakeFormService) Create(form *models.Form, actorID uint) error {
	f.createdForm = form
	f.createdActorID = actorID
	if f.createFn != nil {
		return f.createFn(form, actorID)
	}
	form.ID = 1
	form.PublicID = "public-key"
	return nil
}

func (f *fakeFormService) GetByID(id uint) (*models.Form, error) {
	if f.getByIDFn != nil {
		return f.getByIDFn(id)
	}
	return &models.Form{BaseModel: models.BaseModel{ID: id}, Name: "Contact"}, nil
}

func (f *fakeFormService) List(offset, limit int, status, sortBy, sortOrder string) ([]models.Form, map[uint]int64, int64, error) {
	f.listedStatus = status
	f.listedSortBy = sortBy
	if f.listFn != nil {
		return f.listFn(offset, limit, status, sortBy, sortOrder)
	}
	return []models.Form{}, map[uint]int64{}, 0, nil
}

func (f *fakeFormService) Update(id uint, form *models.Form) error {
	f.updatedForm = form
	if f.updateFn != nil {
		return f.updateFn(id, form)
	}
	form.ID = id
	return nil
}

func (f *fakeFormService) Delete(id uint) error {
	f.deletedID = id
	if f.deleteFn != nil {
		return f.deleteFn(id)
	}
	return nil
}

func (f *fakeFormService) ListSubmissions(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error) {
	f.submissionsFormID = formID
	f.submissionsStatus = status
	if f.listSubmissionsFn != nil {
		return f.listSubmissionsFn(formID, offset, limit, status)
	}
	return nil, 0, nil
}

func (f *fakeFormService) GetSubmission(id uint) (*models.FormSubmission, error) {
	f.requestedSubmissionID = id
	if f.getSubmissionFn != nil {
		return f.getSubmissionFn(id)
	}
	return &models.FormSubmission{BaseModel: models.BaseModel{ID: id}, FormID: 3}, nil
}

func (f *fakeFormService) PublicDefinition(publicID, origin string) (*service.PublicFormDefinition, error) {
	return nil, apperrors.ErrNotFound
}

func (f *fakeFormService) SubmitPublic(publicID string, req *service.PublicSubmissionRequest, meta service.SubmissionMeta) (*service.SubmitOutcome, error) {
	return nil, apperrors.ErrNotFound
}

func (f *fakeFormService) ConfirmSubmission(rawToken string) error {
	return service.ErrInvalidConfirmationToken
}

type FormHandlerTestSuite struct {
	suite.Suite
	fakeService *fakeFormService
	handler     *FormHandler
	role        models.UserRole
	router      *gin.Engine
}

func (suite *FormHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)
}

func (suite *FormHandlerTestSuite) SetupTest() {
	suite.fakeService = &fakeFormService{}
	suite.handler = NewFormHandler(suite.fakeService)
	suite.role = models.RoleAdmin

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(11))
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
	SetupFormRoutes(suite.router.Group(""), suite.handler)
}

func (suite *FormHandlerTestSuite) do(method, path string, body interface{}) *httptest.ResponseRecorder {
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

// validFormBody is a definition that satisfies the binding tags. The service is
// faked in these tests, so the domain rules it enforces are irrelevant here;
// what matters is that the body binds and maps.
func validFormBody() gin.H {
	return gin.H{
		"name":   "Contact us",
		"status": "published",
		"fields": []gin.H{
			{"name": "email", "label": "Email", "type": "email", "required": true},
			{"name": "message", "label": "Message", "type": "textarea"},
		},
		"submit_action":    "message",
		"default_owner_id": 4,
	}
}

// ---------------------------------------------------------------------------
// Authorization
// ---------------------------------------------------------------------------

func (suite *FormHandlerTestSuite) TestRoleMatrix() {
	cases := []struct {
		name     string
		role     models.UserRole
		method   string
		path     string
		body     interface{}
		expected int
	}{
		{"admin lists", models.RoleAdmin, http.MethodGet, "/forms", nil, http.StatusOK},
		{"sales lists", models.RoleSales, http.MethodGet, "/forms", nil, http.StatusOK},
		{"support lists", models.RoleSupport, http.MethodGet, "/forms", nil, http.StatusOK},
		{"customer cannot list", models.RoleCustomer, http.MethodGet, "/forms", nil, http.StatusForbidden},

		{"admin reads one", models.RoleAdmin, http.MethodGet, "/forms/7", nil, http.StatusOK},
		{"support reads one", models.RoleSupport, http.MethodGet, "/forms/7", nil, http.StatusOK},
		{"customer cannot read one", models.RoleCustomer, http.MethodGet, "/forms/7", nil, http.StatusForbidden},

		{"admin creates", models.RoleAdmin, http.MethodPost, "/forms", validFormBody(), http.StatusCreated},
		{"sales creates", models.RoleSales, http.MethodPost, "/forms", validFormBody(), http.StatusCreated},
		{"support cannot create", models.RoleSupport, http.MethodPost, "/forms", validFormBody(), http.StatusForbidden},
		{"customer cannot create", models.RoleCustomer, http.MethodPost, "/forms", validFormBody(), http.StatusForbidden},

		{"sales updates", models.RoleSales, http.MethodPut, "/forms/7", validFormBody(), http.StatusOK},
		{"support cannot update", models.RoleSupport, http.MethodPut, "/forms/7", validFormBody(), http.StatusForbidden},
		{"customer cannot update", models.RoleCustomer, http.MethodPut, "/forms/7", validFormBody(), http.StatusForbidden},

		{"admin deletes", models.RoleAdmin, http.MethodDelete, "/forms/7", nil, http.StatusNoContent},
		{"sales cannot delete", models.RoleSales, http.MethodDelete, "/forms/7", nil, http.StatusForbidden},
		{"support cannot delete", models.RoleSupport, http.MethodDelete, "/forms/7", nil, http.StatusForbidden},
		{"customer cannot delete", models.RoleCustomer, http.MethodDelete, "/forms/7", nil, http.StatusForbidden},

		{"support lists submissions", models.RoleSupport, http.MethodGet, "/forms/7/submissions", nil, http.StatusOK},
		{"customer cannot list submissions", models.RoleCustomer, http.MethodGet, "/forms/7/submissions", nil, http.StatusForbidden},
		{"support reads a submission", models.RoleSupport, http.MethodGet, "/forms/submissions/5", nil, http.StatusOK},
		{"customer cannot read a submission", models.RoleCustomer, http.MethodGet, "/forms/submissions/5", nil, http.StatusForbidden},
	}

	for _, tc := range cases {
		suite.Run(tc.name, func() {
			suite.SetupTest()
			suite.role = tc.role

			w := suite.do(tc.method, tc.path, tc.body)
			assert.Equal(suite.T(), tc.expected, w.Code, "%s %s as %s", tc.method, tc.path, tc.role)
		})
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func (suite *FormHandlerTestSuite) TestCreate_Success() {
	w := suite.do(http.MethodPost, "/forms", validFormBody())

	assert.Equal(suite.T(), http.StatusCreated, w.Code)
	response := decodeResponse(suite.T(), w)
	assert.True(suite.T(), response.Success)
	assert.Contains(suite.T(), w.Body.String(), `"public_id":"public-key"`)

	suite.Require().NotNil(suite.fakeService.createdForm)
	assert.Equal(suite.T(), "Contact us", suite.fakeService.createdForm.Name)
	assert.Equal(suite.T(), models.FormStatusPublished, suite.fakeService.createdForm.Status)
	assert.Len(suite.T(), suite.fakeService.createdForm.Fields, 2)
	assert.Equal(suite.T(), uint(4), suite.fakeService.createdForm.DefaultOwnerID)
	assert.Equal(suite.T(), uint(11), suite.fakeService.createdActorID, "the author is taken from the authenticated context")
}

func (suite *FormHandlerTestSuite) TestCreate_LeadCaptureDefaultsToOn() {
	suite.do(http.MethodPost, "/forms", validFormBody())

	suite.Require().NotNil(suite.fakeService.createdForm)
	assert.True(suite.T(), suite.fakeService.createdForm.CreateLead, "an absent create_lead means the form captures leads")
}

func (suite *FormHandlerTestSuite) TestCreate_LeadCaptureCanBeTurnedOff() {
	body := validFormBody()
	body["create_lead"] = false

	suite.do(http.MethodPost, "/forms", body)

	suite.Require().NotNil(suite.fakeService.createdForm)
	assert.False(suite.T(), suite.fakeService.createdForm.CreateLead, "an explicit false must survive the default")
}

func (suite *FormHandlerTestSuite) TestCreate_InvalidBodyIsRejected() {
	body := validFormBody()
	delete(body, "name")

	w := suite.do(http.MethodPost, "/forms", body)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Nil(suite.T(), suite.fakeService.createdForm, "the service is not called for a body that fails binding")
}

func (suite *FormHandlerTestSuite) TestCreate_UnknownStatusIsRejected() {
	body := validFormBody()
	body["status"] = "live"

	w := suite.do(http.MethodPost, "/forms", body)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Nil(suite.T(), suite.fakeService.createdForm)
}

func (suite *FormHandlerTestSuite) TestCreate_FieldErrorsSurfaceAsDetails() {
	suite.fakeService.createFn = func(form *models.Form, actorID uint) error {
		return service.FieldErrors{"default_owner_id": "user 4 does not exist"}
	}

	w := suite.do(http.MethodPost, "/forms", validFormBody())

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	response := decodeResponse(suite.T(), w)
	suite.Require().NotNil(response.Error)
	assert.Equal(suite.T(), utils.ErrCodeValidation, response.Error.Code)

	details, ok := response.Error.Details.(map[string]interface{})
	suite.Require().True(ok, "field errors are reported as a field to message map")
	assert.Equal(suite.T(), "user 4 does not exist", details["default_owner_id"])
}

func (suite *FormHandlerTestSuite) TestCreate_DefinitionErrorIsABadRequest() {
	suite.fakeService.createFn = func(form *models.Form, actorID uint) error {
		return fmt.Errorf("%w: exactly one field of type email is required: %w",
			models.ErrInvalidFormDefinition, apperrors.ErrValidation)
	}

	w := suite.do(http.MethodPost, "/forms", validFormBody())

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	response := decodeResponse(suite.T(), w)
	suite.Require().NotNil(response.Error)
	assert.Equal(suite.T(), utils.ErrCodeValidation, response.Error.Code)
	assert.Contains(suite.T(), response.Error.Message, "exactly one field of type email")
}

func (suite *FormHandlerTestSuite) TestCreate_ServiceFailureIsAServerError() {
	suite.fakeService.createFn = func(form *models.Form, actorID uint) error {
		return errors.New("db down")
	}

	w := suite.do(http.MethodPost, "/forms", validFormBody())

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "db down", "internal errors must not be echoed back")
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

func (suite *FormHandlerTestSuite) TestList_RowsCarryTheirSubmissionCount() {
	suite.fakeService.listFn = func(offset, limit int, status, sortBy, sortOrder string) ([]models.Form, map[uint]int64, int64, error) {
		return []models.Form{
				{BaseModel: models.BaseModel{ID: 1}, Name: "Contact", PublicID: "aaa"},
				{BaseModel: models.BaseModel{ID: 2}, Name: "Newsletter", PublicID: "bbb"},
			},
			map[uint]int64{1: 12},
			2,
			nil
	}

	w := suite.do(http.MethodGet, "/forms?limit=1&offset=1&status=published&sort_by=name", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response struct {
		Success bool           `json:"success"`
		Data    []FormListItem `json:"data"`
		Meta    *utils.APIMeta `json:"meta"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))

	assert.True(suite.T(), response.Success)
	suite.Require().Len(response.Data, 2)
	assert.Equal(suite.T(), "Contact", response.Data[0].Name)
	assert.Equal(suite.T(), int64(12), response.Data[0].SubmissionCount)
	assert.Equal(suite.T(), int64(0), response.Data[1].SubmissionCount, "a form without submissions reports zero")
	assert.Contains(suite.T(), w.Body.String(), `"public_id":"aaa"`, "a row carries the whole form")

	suite.Require().NotNil(response.Meta)
	assert.Equal(suite.T(), int64(2), response.Meta.Total)
	assert.Equal(suite.T(), 1, response.Meta.PerPage)
	assert.Equal(suite.T(), 2, response.Meta.Page)
	assert.Equal(suite.T(), int64(2), response.Meta.TotalPages)

	assert.Equal(suite.T(), "published", suite.fakeService.listedStatus)
	assert.Equal(suite.T(), "name", suite.fakeService.listedSortBy)
}

func (suite *FormHandlerTestSuite) TestList_EmptyPageIsAnArrayNotNull() {
	w := suite.do(http.MethodGet, "/forms", nil)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"data":[]`)
}

func (suite *FormHandlerTestSuite) TestList_UnknownSortColumnIsABadRequest() {
	suite.fakeService.listFn = func(offset, limit int, status, sortBy, sortOrder string) ([]models.Form, map[uint]int64, int64, error) {
		return nil, nil, 0, service.FieldErrors{"sort_by": `cannot sort forms by "evil"`}
	}

	w := suite.do(http.MethodGet, "/forms?sort_by=evil", nil)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "sort_by")
}

func (suite *FormHandlerTestSuite) TestGet_MissingFormIsNotFound() {
	suite.fakeService.getByIDFn = func(id uint) (*models.Form, error) {
		return nil, fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
	}

	w := suite.do(http.MethodGet, "/forms/404", nil)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
	response := decodeResponse(suite.T(), w)
	suite.Require().NotNil(response.Error)
	assert.Equal(suite.T(), "Form not found", response.Error.Message)
}

func (suite *FormHandlerTestSuite) TestGet_NonNumericIDIsABadRequest() {
	w := suite.do(http.MethodGet, "/forms/abc", nil)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------------------
// Update and delete
// ---------------------------------------------------------------------------

func (suite *FormHandlerTestSuite) TestUpdate_ReplacesTheWholeDefinition() {
	body := validFormBody()
	body["status"] = "archived"
	body["notify_emails"] = []string{"ops@example.com"}

	w := suite.do(http.MethodPut, "/forms/7", body)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	suite.Require().NotNil(suite.fakeService.updatedForm)
	assert.Equal(suite.T(), models.FormStatusArchived, suite.fakeService.updatedForm.Status)
	assert.Equal(suite.T(), []string{"ops@example.com"}, suite.fakeService.updatedForm.NotifyEmails)
	assert.Empty(suite.T(), suite.fakeService.updatedForm.PublicID, "the client cannot set the public identifier")
	assert.Zero(suite.T(), suite.fakeService.updatedForm.CreatedByID, "the client cannot set the author")
}

func (suite *FormHandlerTestSuite) TestUpdate_MissingFormIsNotFound() {
	suite.fakeService.updateFn = func(id uint, form *models.Form) error {
		return fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
	}

	w := suite.do(http.MethodPut, "/forms/404", validFormBody())

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *FormHandlerTestSuite) TestDelete_Success() {
	w := suite.do(http.MethodDelete, "/forms/7", nil)

	assert.Equal(suite.T(), http.StatusNoContent, w.Code)
	assert.Empty(suite.T(), w.Body.String())
	assert.Equal(suite.T(), uint(7), suite.fakeService.deletedID)
}

func (suite *FormHandlerTestSuite) TestDelete_MissingFormIsNotFound() {
	suite.fakeService.deleteFn = func(id uint) error {
		return fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
	}

	w := suite.do(http.MethodDelete, "/forms/404", nil)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// Submissions
// ---------------------------------------------------------------------------

func (suite *FormHandlerTestSuite) TestListSubmissions_Envelope() {
	suite.fakeService.listSubmissionsFn = func(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error) {
		return []models.FormSubmission{
			{
				BaseModel:  models.BaseModel{ID: 9},
				FormID:     formID,
				Email:      "visitor@example.com",
				Status:     models.FormSubmissionSpam,
				SpamReason: models.FormSpamReasonHoneypot,
				Data:       map[string]string{"email": "visitor@example.com"},
			},
		}, 1, nil
	}

	w := suite.do(http.MethodGet, "/forms/7/submissions?status=spam", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response struct {
		Success bool                    `json:"success"`
		Data    []models.FormSubmission `json:"data"`
		Meta    *utils.APIMeta          `json:"meta"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))

	assert.True(suite.T(), response.Success)
	suite.Require().Len(response.Data, 1)
	assert.Equal(suite.T(), models.FormSubmissionSpam, response.Data[0].Status)
	assert.Equal(suite.T(), models.FormSpamReasonHoneypot, response.Data[0].SpamReason)
	suite.Require().NotNil(response.Meta)
	assert.Equal(suite.T(), int64(1), response.Meta.Total)

	assert.Equal(suite.T(), uint(7), suite.fakeService.submissionsFormID)
	assert.Equal(suite.T(), "spam", suite.fakeService.submissionsStatus)
}

func (suite *FormHandlerTestSuite) TestListSubmissions_EmptyPageIsAnArrayNotNull() {
	w := suite.do(http.MethodGet, "/forms/7/submissions", nil)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"data":[]`)
}

func (suite *FormHandlerTestSuite) TestListSubmissions_MissingFormIsNotFound() {
	suite.fakeService.listSubmissionsFn = func(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error) {
		return nil, 0, fmt.Errorf("form %d not found: %w", formID, apperrors.ErrNotFound)
	}

	w := suite.do(http.MethodGet, "/forms/404/submissions", nil)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func (suite *FormHandlerTestSuite) TestGetSubmission_Success() {
	w := suite.do(http.MethodGet, "/forms/submissions/5", nil)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Equal(suite.T(), uint(5), suite.fakeService.requestedSubmissionID,
		"the submission route must win over the form-detail wildcard")
	assert.Contains(suite.T(), w.Body.String(), `"form_id":3`)
}

func (suite *FormHandlerTestSuite) TestGetSubmission_MissingSubmissionIsNotFound() {
	suite.fakeService.getSubmissionFn = func(id uint) (*models.FormSubmission, error) {
		return nil, fmt.Errorf("form submission %d not found: %w", id, apperrors.ErrNotFound)
	}

	w := suite.do(http.MethodGet, "/forms/submissions/404", nil)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
	response := decodeResponse(suite.T(), w)
	suite.Require().NotNil(response.Error)
	assert.Equal(suite.T(), "Submission not found", response.Error.Message)
}

func TestFormHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(FormHandlerTestSuite))
}
