package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// stubBulkStatusService implements service.BulkOperationService by embedding the
// interface: only the three status-update methods are exercised here, so any
// other call is a test bug and panics on the nil embedded value rather than
// quietly returning a zero value.
type stubBulkStatusService struct {
	service.BulkOperationService

	called     bool
	gotActorID uint
	gotRole    models.UserRole
	gotIDs     []uint
	gotStatus  string
	result     *models.BulkStatusUpdateResult
	err        error
}

func (s *stubBulkStatusService) record(actorID uint, role models.UserRole, ids []uint, status string) {
	s.called = true
	s.gotActorID = actorID
	s.gotRole = role
	s.gotIDs = ids
	s.gotStatus = status
}

func (s *stubBulkStatusService) BulkSetLeadStatus(actorID uint, role models.UserRole, ids []uint, status models.LeadStatus) (*models.BulkStatusUpdateResult, error) {
	s.record(actorID, role, ids, string(status))
	return s.result, s.err
}

func (s *stubBulkStatusService) BulkSetTicketStatus(actorID uint, role models.UserRole, ids []uint, status models.TicketStatus) (*models.BulkStatusUpdateResult, error) {
	s.record(actorID, role, ids, string(status))
	return s.result, s.err
}

func (s *stubBulkStatusService) BulkSetTaskStatus(actorID uint, role models.UserRole, ids []uint, status models.TaskStatus) (*models.BulkStatusUpdateResult, error) {
	s.record(actorID, role, ids, string(status))
	return s.result, s.err
}

type BulkStatusHandlerTestSuite struct {
	suite.Suite
	handler *BulkHandler
	stub    *stubBulkStatusService
	router  *gin.Engine
	role    models.UserRole
	userID  uint
}

func TestBulkStatusHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(BulkStatusHandlerTestSuite))
}

func (suite *BulkStatusHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)
}

func (suite *BulkStatusHandlerTestSuite) SetupTest() {
	suite.stub = &stubBulkStatusService{result: &models.BulkStatusUpdateResult{Updated: 2}}
	suite.handler = NewBulkHandler(suite.stub)
	suite.userID = 7
	suite.role = models.RoleAdmin

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", suite.userID)
		c.Set("user_role", string(suite.role))
		c.Next()
	})
	// Mirrors middleware.ErrorHandler: binding failures become 400s.
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})

	// Routes are registered by the orchestrator inside the entity groups; the
	// tests mount the same paths directly.
	suite.router.POST("/leads/bulk/status", suite.handler.BulkUpdateLeadStatus)
	suite.router.POST("/tickets/bulk/status", suite.handler.BulkUpdateTicketStatus)
	suite.router.POST("/tasks/bulk/status", suite.handler.BulkUpdateTaskStatus)
}

// actAs switches the role the request middleware reports for the calls that
// follow. The middleware reads the field per request, so this takes effect
// without rebuilding the router — rebuilding it would reset the role to admin.
func (suite *BulkStatusHandlerTestSuite) actAs(role models.UserRole) {
	suite.role = role
}

func (suite *BulkStatusHandlerTestSuite) post(path string, body interface{}) *httptest.ResponseRecorder {
	payload, err := json.Marshal(body)
	suite.Require().NoError(err)
	req, err := http.NewRequest(http.MethodPost, path, bytes.NewBuffer(payload))
	suite.Require().NoError(err)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func (suite *BulkStatusHandlerTestSuite) decode(w *httptest.ResponseRecorder) utils.APIResponse {
	var resp utils.APIResponse
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// detailIDs pulls a list of IDs out of the error details, which travel through
// JSON as float64.
func (suite *BulkStatusHandlerTestSuite) detailIDs(resp utils.APIResponse, key string) []uint {
	suite.Require().NotNil(resp.Error)
	details, ok := resp.Error.Details.(map[string]interface{})
	suite.Require().True(ok, "error details must be an object, got %#v", resp.Error.Details)
	raw, ok := details[key]
	suite.Require().True(ok, "error details must name %q, got %#v", key, details)
	list, ok := raw.([]interface{})
	suite.Require().True(ok, "%s must be a list, got %#v", key, raw)
	ids := make([]uint, 0, len(list))
	for _, v := range list {
		f, ok := v.(float64)
		suite.Require().True(ok, "%s entries must be numbers, got %#v", key, v)
		ids = append(ids, uint(f))
	}
	return ids
}

func tooManyIDs() []uint {
	ids := make([]uint, 101)
	for i := range ids {
		ids[i] = uint(i + 1)
	}
	return ids
}

// --- Leads -------------------------------------------------------------------

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateLeadStatus_Success() {
	w := suite.post("/leads/bulk/status", gin.H{"lead_ids": []uint{1, 2}, "status": "qualified"})

	suite.Equal(http.StatusOK, w.Code)
	resp := suite.decode(w)
	suite.True(resp.Success)
	data, ok := resp.Data.(map[string]interface{})
	suite.Require().True(ok)
	suite.Equal(float64(2), data["updated"])

	suite.True(suite.stub.called)
	suite.Equal(uint(7), suite.stub.gotActorID)
	suite.Equal(models.RoleAdmin, suite.stub.gotRole)
	suite.Equal([]uint{1, 2}, suite.stub.gotIDs)
	suite.Equal("qualified", suite.stub.gotStatus)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateLeadStatus_NotOwnedBySales_ForbiddenNamesIDs() {
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrForbidden, apperrors.CodeInsufficientPermissions,
		"You can only update your own leads").WithDetail("forbidden_ids", []uint{2, 3})

	w := suite.post("/leads/bulk/status", gin.H{"lead_ids": []uint{1, 2, 3}, "status": "contacted"})

	suite.Equal(http.StatusForbidden, w.Code)
	resp := suite.decode(w)
	suite.False(resp.Success)
	suite.Equal(utils.ErrCodeForbidden, resp.Error.Code)
	suite.Equal([]uint{2, 3}, suite.detailIDs(resp, "forbidden_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateLeadStatus_MissingID_NotFoundNamesIDs() {
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrNotFound, apperrors.CodeNotFound,
		"One or more leads were not found").WithDetail("missing_ids", []uint{9})

	w := suite.post("/leads/bulk/status", gin.H{"lead_ids": []uint{1, 9}, "status": "contacted"})

	suite.Equal(http.StatusNotFound, w.Code)
	resp := suite.decode(w)
	suite.Equal(utils.ErrCodeNotFound, resp.Error.Code)
	suite.Equal([]uint{9}, suite.detailIDs(resp, "missing_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateLeadStatus_InvalidStatus_BadRequest() {
	w := suite.post("/leads/bulk/status", gin.H{"lead_ids": []uint{1}, "status": "archived"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called, "an invalid status must never reach the service")
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateLeadStatus_EmptyList_BadRequest() {
	w := suite.post("/leads/bulk/status", gin.H{"lead_ids": []uint{}, "status": "contacted"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateLeadStatus_TooManyIDs_BadRequest() {
	w := suite.post("/leads/bulk/status", gin.H{"lead_ids": tooManyIDs(), "status": "contacted"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

// --- Tickets -----------------------------------------------------------------

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_Success() {
	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{4, 5}, "status": "resolved"})

	suite.Equal(http.StatusOK, w.Code)
	suite.True(suite.stub.called)
	suite.Equal([]uint{4, 5}, suite.stub.gotIDs)
	suite.Equal("resolved", suite.stub.gotStatus)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_SalesForbidden() {
	suite.actAs(models.RoleSales)

	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{1}, "status": "resolved"})

	suite.Equal(http.StatusForbidden, w.Code)
	suite.False(suite.stub.called, "sales is read-only on tickets; the service must not be called")
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_CustomerForbidden() {
	suite.actAs(models.RoleCustomer)

	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{1}, "status": "resolved"})

	suite.Equal(http.StatusForbidden, w.Code)
	suite.False(suite.stub.called)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_NotAssignedToSupport_ForbiddenNamesIDs() {
	suite.actAs(models.RoleSupport)
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrForbidden, apperrors.CodeInsufficientPermissions,
		"You can only update tickets assigned to you").WithDetail("forbidden_ids", []uint{5})

	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{4, 5}, "status": "resolved"})

	suite.Equal(http.StatusForbidden, w.Code)
	suite.Equal([]uint{5}, suite.detailIDs(suite.decode(w), "forbidden_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_MissingID_NotFoundNamesIDs() {
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrNotFound, apperrors.CodeNotFound,
		"One or more tickets were not found").WithDetail("missing_ids", []uint{8})

	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{8}, "status": "closed"})

	suite.Equal(http.StatusNotFound, w.Code)
	suite.Equal([]uint{8}, suite.detailIDs(suite.decode(w), "missing_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_ReopenClosed_BadRequestNamesIDs() {
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrClosedTicketReopen, apperrors.CodeInvalidStatusTransition,
		"Cannot reopen closed tickets").WithDetail("closed_ids", []uint{6})

	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{6}, "status": "open"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.Equal([]uint{6}, suite.detailIDs(suite.decode(w), "closed_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_InvalidStatus_BadRequest() {
	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{1}, "status": "pending"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_EmptyList_BadRequest() {
	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": []uint{}, "status": "closed"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTicketStatus_TooManyIDs_BadRequest() {
	w := suite.post("/tickets/bulk/status", gin.H{"ticket_ids": tooManyIDs(), "status": "closed"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

// --- Tasks -------------------------------------------------------------------

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_Success() {
	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": []uint{1, 2}, "status": "in_progress"})

	suite.Equal(http.StatusOK, w.Code)
	suite.True(suite.stub.called)
	suite.Equal([]uint{1, 2}, suite.stub.gotIDs)
	suite.Equal("in_progress", suite.stub.gotStatus)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_NotAssigned_ForbiddenNamesIDs() {
	suite.actAs(models.RoleSupport)
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrForbidden, apperrors.CodeInsufficientPermissions,
		"You can only update tasks assigned to you").WithDetail("forbidden_ids", []uint{2})

	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": []uint{1, 2}, "status": "completed"})

	suite.Equal(http.StatusForbidden, w.Code)
	suite.Equal([]uint{2}, suite.detailIDs(suite.decode(w), "forbidden_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_MissingID_NotFoundNamesIDs() {
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrNotFound, apperrors.CodeNotFound,
		"One or more tasks were not found").WithDetail("missing_ids", []uint{3, 4})

	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": []uint{3, 4}, "status": "cancelled"})

	suite.Equal(http.StatusNotFound, w.Code)
	suite.Equal([]uint{3, 4}, suite.detailIDs(suite.decode(w), "missing_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_CompletedTask_BadRequestNamesIDs() {
	suite.stub.result = nil
	suite.stub.err = apperrors.Wrap(apperrors.ErrCompletedTaskModify, apperrors.CodeInvalidStatusTransition,
		"Cannot change the status of completed tasks").WithDetail("completed_ids", []uint{1})

	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": []uint{1, 2}, "status": "pending"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.Equal([]uint{1}, suite.detailIDs(suite.decode(w), "completed_ids"))
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_InvalidStatus_BadRequest() {
	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": []uint{1}, "status": "resolved"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_EmptyList_BadRequest() {
	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": []uint{}, "status": "pending"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_TooManyIDs_BadRequest() {
	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": tooManyIDs(), "status": "pending"})

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.False(suite.stub.called)
}

// An unexpected failure must not leak internals to the client.
func (suite *BulkStatusHandlerTestSuite) TestBulkUpdateTaskStatus_UnexpectedError_InternalError() {
	suite.stub.result = nil
	suite.stub.err = fmt.Errorf("database is on fire")

	w := suite.post("/tasks/bulk/status", gin.H{"task_ids": []uint{1}, "status": "pending"})

	suite.Equal(http.StatusInternalServerError, w.Code)
	resp := suite.decode(w)
	suite.Equal(utils.ErrCodeInternal, resp.Error.Code)
	suite.NotContains(resp.Error.Message, "on fire")
}

// The bulk status routes are registered inside the entity groups, next to
// routes whose first segment is the wildcard :id. Gin has to be able to hold
// both, and a request for /leads/bulk/status must not be parsed as lead "bulk".
// This is the registration shape handed to the router setup, checked here so a
// conflict shows up as a failing test rather than a panic at boot.
func TestBulkStatusRoutesCoexistWithIDRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	stub := &stubBulkStatusService{result: &models.BulkStatusUpdateResult{Updated: 1}}
	bulkHandler := NewBulkHandler(stub)
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})

	noop := func(c *gin.Context) { c.Status(http.StatusNoContent) }

	require.NotPanics(t, func() {
		leads := router.Group("/leads")
		{
			leads.POST("", noop)
			leads.GET("/:id", noop)
			leads.PUT("/:id", noop)
			leads.DELETE("/:id", noop)
			leads.POST("/:id/convert", noop)
			leads.POST("/bulk/status", bulkHandler.BulkUpdateLeadStatus)
		}
		tickets := router.Group("/tickets")
		{
			tickets.POST("", noop)
			tickets.GET("/my", noop)
			tickets.PUT("/:id", noop)
			tickets.DELETE("/:id", noop)
			tickets.POST("/bulk/status", bulkHandler.BulkUpdateTicketStatus)
		}
		tasks := router.Group("/tasks")
		{
			tasks.POST("", noop)
			tasks.GET("/my", noop)
			tasks.PUT("/:id", noop)
			tasks.DELETE("/:id", noop)
			tasks.POST("/bulk/status", bulkHandler.BulkUpdateTaskStatus)
		}
	}, "the bulk status routes must be registrable alongside the entity routes")

	for _, route := range []struct {
		path string
		body interface{}
	}{
		{"/leads/bulk/status", gin.H{"lead_ids": []uint{1}, "status": "contacted"}},
		{"/tickets/bulk/status", gin.H{"ticket_ids": []uint{1}, "status": "resolved"}},
		{"/tasks/bulk/status", gin.H{"task_ids": []uint{1}, "status": "pending"}},
	} {
		stub.called = false
		payload, err := json.Marshal(route.body)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, route.path, bytes.NewBuffer(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, route.path)
		require.True(t, stub.called, "%s must reach the bulk handler", route.path)
	}
}
