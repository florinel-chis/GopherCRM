package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	servicemocks "github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type DashboardHandlerTestSuite struct {
	suite.Suite
	mockLeadService     *servicemocks.LeadService
	mockCustomerService *mocks.CustomerService
	mockTicketService   *mocks.TicketService
	mockTaskService     *MockTaskService
	handler             *DashboardHandler
}

func (suite *DashboardHandlerTestSuite) SetupSuite() {
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
	gin.SetMode(gin.TestMode)
}

func (suite *DashboardHandlerTestSuite) SetupTest() {
	suite.mockLeadService = new(servicemocks.LeadService)
	suite.mockCustomerService = new(mocks.CustomerService)
	suite.mockTicketService = new(mocks.TicketService)
	suite.mockTaskService = new(MockTaskService)
	suite.handler = NewDashboardHandler(
		suite.mockLeadService,
		suite.mockCustomerService,
		suite.mockTicketService,
		suite.mockTaskService,
	)
}

func (suite *DashboardHandlerTestSuite) TearDownTest() {
	suite.mockLeadService.AssertExpectations(suite.T())
	suite.mockCustomerService.AssertExpectations(suite.T())
	suite.mockTicketService.AssertExpectations(suite.T())
	suite.mockTaskService.AssertExpectations(suite.T())
}

// newRouterWithRole wires the real dashboard routes (including their
// middleware) behind a stub auth layer that injects the given role, so the
// tests exercise the exact route registration from routes.go.
func (suite *DashboardHandlerTestSuite) newRouterWithRole(role models.UserRole) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "test-request-id")
		c.Set("user_id", uint(1))
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupDashboardRoutes(router.Group(""), suite.handler)
	return router
}

func (suite *DashboardHandlerTestSuite) expectStatsCounts() {
	suite.mockLeadService.On("GetCount").Return(int64(10), nil)
	suite.mockCustomerService.On("GetCount").Return(int64(4), nil)
	suite.mockTicketService.On("GetOpenCount").Return(int64(3), nil)
	suite.mockTaskService.On("GetPendingCount").Return(int64(7), nil)
}

func (suite *DashboardHandlerTestSuite) TestGetStats_AdminAllowed() {
	router := suite.newRouterWithRole(models.RoleAdmin)
	suite.expectStatsCounts()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Data)

	stats := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), float64(10), stats["total_leads"])
	assert.Equal(suite.T(), float64(4), stats["total_customers"])
	assert.Equal(suite.T(), float64(3), stats["open_tickets"])
	assert.Equal(suite.T(), float64(7), stats["pending_tasks"])
	assert.Equal(suite.T(), float64(40), stats["conversion_rate"])
}

func (suite *DashboardHandlerTestSuite) TestGetStats_SalesAllowed() {
	router := suite.newRouterWithRole(models.RoleSales)
	suite.expectStatsCounts()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetStats_SupportAllowed() {
	router := suite.newRouterWithRole(models.RoleSupport)
	suite.expectStatsCounts()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetStats_CustomerForbidden() {
	router := suite.newRouterWithRole(models.RoleCustomer)
	// No service expectations: a forbidden request must never reach the
	// aggregate counts.

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Nil(suite.T(), response.Data)
}

func TestDashboardHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(DashboardHandlerTestSuite))
}

// --- analytics endpoints -----------------------------------------------------

// dashboardEnvelope is the outer utils.APIResponse with the payload left raw,
// so each test can decode it into the concrete shape the frontend expects.
type dashboardEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

type chartDataJSON struct {
	Labels   []string `json:"labels"`
	Datasets []struct {
		Label string  `json:"label"`
		Data  []int64 `json:"data"`
	} `json:"datasets"`
}

type activityJSON struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	User        struct {
		ID        uint   `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

// newAnalyticsRouter mirrors the intended route registration for the analytics
// endpoints — all of them behind RequireRole(admin, sales, support), exactly
// like /dashboard/stats — without touching routes.go, which is owned
// elsewhere. A fresh engine per call also keeps these registrations from
// colliding with SetupDashboardRoutes.
func (suite *DashboardHandlerTestSuite) newAnalyticsRouter(role models.UserRole, userID uint) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "test-request-id")
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	group := router.Group("/dashboard", middleware.RequireRole(models.RoleAdmin, models.RoleSales, models.RoleSupport))
	group.GET("/leads-by-status", suite.handler.GetLeadsByStatus)
	group.GET("/tickets-by-priority", suite.handler.GetTicketsByPriority)
	group.GET("/tasks-by-status", suite.handler.GetTasksByStatus)
	group.GET("/sales-performance", suite.handler.GetSalesPerformance)
	group.GET("/activities", suite.handler.GetActivities)
	group.GET("/upcoming-tasks", suite.handler.GetUpcomingTasks)
	group.GET("/recent-tickets", suite.handler.GetRecentTickets)
	group.GET("/new-leads", suite.handler.GetNewLeads)
	return router
}

func (suite *DashboardHandlerTestSuite) doGet(router *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// assertEmptyDataArray checks the payload is an empty JSON array rather than a
// null — utils.RespondSuccess also attaches a meta block, so the body is never
// compared verbatim.
func (suite *DashboardHandlerTestSuite) assertEmptyDataArray(rec *httptest.ResponseRecorder) {
	var env dashboardEnvelope
	require.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &env))
	require.True(suite.T(), env.Success)
	assert.Equal(suite.T(), "[]", string(env.Data))
}

func (suite *DashboardHandlerTestSuite) decodeChart(rec *httptest.ResponseRecorder) chartDataJSON {
	var env dashboardEnvelope
	require.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &env))
	require.True(suite.T(), env.Success)
	var chart chartDataJSON
	require.NoError(suite.T(), json.Unmarshal(env.Data, &chart))
	return chart
}

func (suite *DashboardHandlerTestSuite) TestGetLeadsByStatus_Success() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	suite.mockLeadService.On("GetStatusCounts").Return(map[string]int64{
		"new":       4,
		"converted": 2,
	}, nil)

	rec := suite.doGet(router, "/dashboard/leads-by-status")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Equal(suite.T(), []string{"new", "contacted", "qualified", "unqualified", "converted"}, chart.Labels)
	require.Len(suite.T(), chart.Datasets, 1)
	assert.Equal(suite.T(), "Leads", chart.Datasets[0].Label)
	assert.Equal(suite.T(), []int64{4, 0, 0, 0, 2}, chart.Datasets[0].Data)
}

// A status the model does not know about must still be reported rather than
// silently dropped, and it must land after the canonical statuses.
func (suite *DashboardHandlerTestSuite) TestGetLeadsByStatus_UnknownStatusAppended() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	suite.mockLeadService.On("GetStatusCounts").Return(map[string]int64{
		"new":     1,
		"revived": 3,
	}, nil)

	rec := suite.doGet(router, "/dashboard/leads-by-status")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Equal(suite.T(), []string{"new", "contacted", "qualified", "unqualified", "converted", "revived"}, chart.Labels)
	assert.Equal(suite.T(), []int64{1, 0, 0, 0, 0, 3}, chart.Datasets[0].Data)
}

// No rows at all must still yield a well-formed chart: the canonical labels
// with zero counts, and JSON arrays rather than nulls.
func (suite *DashboardHandlerTestSuite) TestGetLeadsByStatus_EmptyData() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	suite.mockLeadService.On("GetStatusCounts").Return(map[string]int64{}, nil)

	rec := suite.doGet(router, "/dashboard/leads-by-status")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.NotContains(suite.T(), rec.Body.String(), "null")
	chart := suite.decodeChart(rec)
	assert.Len(suite.T(), chart.Labels, 5)
	require.Len(suite.T(), chart.Datasets, 1)
	assert.Equal(suite.T(), []int64{0, 0, 0, 0, 0}, chart.Datasets[0].Data)
}

func (suite *DashboardHandlerTestSuite) TestGetLeadsByStatus_CustomerForbidden() {
	router := suite.newAnalyticsRouter(models.RoleCustomer, 1)

	rec := suite.doGet(router, "/dashboard/leads-by-status")

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetLeadsByStatus_ServiceError() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	suite.mockLeadService.On("GetStatusCounts").Return(map[string]int64(nil), assert.AnError)

	rec := suite.doGet(router, "/dashboard/leads-by-status")

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetTicketsByPriority_Success() {
	router := suite.newAnalyticsRouter(models.RoleSupport, 1)
	suite.mockTicketService.On("GetPriorityCounts").Return(map[string]int64{
		"high":   2,
		"urgent": 1,
	}, nil)

	rec := suite.doGet(router, "/dashboard/tickets-by-priority")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Equal(suite.T(), []string{"low", "medium", "high", "urgent"}, chart.Labels)
	require.Len(suite.T(), chart.Datasets, 1)
	assert.Equal(suite.T(), "Tickets", chart.Datasets[0].Label)
	assert.Equal(suite.T(), []int64{0, 0, 2, 1}, chart.Datasets[0].Data)
}

func (suite *DashboardHandlerTestSuite) TestGetTicketsByPriority_EmptyData() {
	router := suite.newAnalyticsRouter(models.RoleSupport, 1)
	suite.mockTicketService.On("GetPriorityCounts").Return(map[string]int64{}, nil)

	rec := suite.doGet(router, "/dashboard/tickets-by-priority")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Equal(suite.T(), []int64{0, 0, 0, 0}, chart.Datasets[0].Data)
}

func (suite *DashboardHandlerTestSuite) TestGetTasksByStatus_Success() {
	router := suite.newAnalyticsRouter(models.RoleSales, 1)
	suite.mockTaskService.On("GetStatusCounts").Return(map[string]int64{
		"pending":   5,
		"completed": 3,
	}, nil)

	rec := suite.doGet(router, "/dashboard/tasks-by-status")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Equal(suite.T(), []string{"pending", "in_progress", "completed", "cancelled"}, chart.Labels)
	require.Len(suite.T(), chart.Datasets, 1)
	assert.Equal(suite.T(), "Tasks", chart.Datasets[0].Label)
	assert.Equal(suite.T(), []int64{5, 0, 3, 0}, chart.Datasets[0].Data)
}

func (suite *DashboardHandlerTestSuite) TestGetTasksByStatus_EmptyData() {
	router := suite.newAnalyticsRouter(models.RoleSales, 1)
	suite.mockTaskService.On("GetStatusCounts").Return(map[string]int64{}, nil)

	rec := suite.doGet(router, "/dashboard/tasks-by-status")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Equal(suite.T(), []int64{0, 0, 0, 0}, chart.Datasets[0].Data)
}

func (suite *DashboardHandlerTestSuite) TestGetSalesPerformance_DefaultsToTwelveMonths() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	now := time.Now()
	suite.mockLeadService.On("GetConversionTimestamps", mock.AnythingOfType("time.Time")).
		Return([]time.Time{now, now}, nil)

	rec := suite.doGet(router, "/dashboard/sales-performance")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Len(suite.T(), chart.Labels, 12)
	require.Len(suite.T(), chart.Datasets, 1)
	assert.Equal(suite.T(), "Conversions", chart.Datasets[0].Label)
	assert.Len(suite.T(), chart.Datasets[0].Data, 12)
	// Both conversions happened now, so they land in the newest bucket.
	assert.Equal(suite.T(), int64(2), chart.Datasets[0].Data[11])
	assert.Equal(suite.T(), now.Format("2006-01"), chart.Labels[11])
}

func (suite *DashboardHandlerTestSuite) TestGetSalesPerformance_UnknownPeriodFallsBackToMonth() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	suite.mockLeadService.On("GetConversionTimestamps", mock.AnythingOfType("time.Time")).
		Return([]time.Time{}, nil)

	rec := suite.doGet(router, "/dashboard/sales-performance?period=decade")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	chart := suite.decodeChart(rec)
	assert.Len(suite.T(), chart.Labels, 12)
	assert.Equal(suite.T(), []int64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, chart.Datasets[0].Data)
}

func (suite *DashboardHandlerTestSuite) TestGetSalesPerformance_PeriodWindows() {
	cases := []struct {
		period  string
		buckets int
	}{
		{"week", 12},
		{"month", 12},
		{"quarter", 8},
		{"year", 5},
	}
	for _, tc := range cases {
		suite.SetupTest()
		router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
		suite.mockLeadService.On("GetConversionTimestamps", mock.AnythingOfType("time.Time")).
			Return([]time.Time{}, nil)

		rec := suite.doGet(router, "/dashboard/sales-performance?period="+tc.period)

		assert.Equal(suite.T(), http.StatusOK, rec.Code, tc.period)
		chart := suite.decodeChart(rec)
		assert.Len(suite.T(), chart.Labels, tc.buckets, tc.period)
		assert.Len(suite.T(), chart.Datasets[0].Data, tc.buckets, tc.period)
	}
}

func (suite *DashboardHandlerTestSuite) TestGetSalesPerformance_CustomerForbidden() {
	router := suite.newAnalyticsRouter(models.RoleCustomer, 1)

	rec := suite.doGet(router, "/dashboard/sales-performance")

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetActivities_MergedAndSortedDescending() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)

	owner := models.User{Email: "sales@example.com", FirstName: "Sam", LastName: "Sales"}
	owner.ID = 7
	assignee := models.User{Email: "support@example.com", FirstName: "Sue", LastName: "Support"}
	assignee.ID = 9

	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	lead := models.Lead{FirstName: "Lea", LastName: "Dee", Company: "ACME", OwnerID: 7, Owner: owner}
	lead.ID = 42
	lead.CreatedAt = base.Add(-4 * time.Hour)

	converted := models.Lead{FirstName: "Con", LastName: "Verted", OwnerID: 7, Owner: owner, Status: models.LeadStatusConverted}
	converted.ID = 43
	converted.CreatedAt = base.Add(-100 * time.Hour)
	converted.UpdatedAt = base.Add(-1 * time.Hour)

	ticket := models.Ticket{Title: "Printer on fire", AssignedToID: &assignee.ID, AssignedTo: &assignee}
	ticket.ID = 5
	ticket.CreatedAt = base.Add(-3 * time.Hour)

	resolved := models.Ticket{Title: "Password reset", Status: models.TicketStatusResolved, AssignedToID: &assignee.ID, AssignedTo: &assignee}
	resolved.ID = 6
	resolved.UpdatedAt = base.Add(-2 * time.Hour)

	task := models.Task{Title: "Call back", Status: models.TaskStatusCompleted, AssignedToID: 9, AssignedTo: assignee}
	task.ID = 11
	task.UpdatedAt = base.Add(-5 * time.Hour)

	suite.mockLeadService.On("GetRecent", 10).Return([]models.Lead{lead}, nil)
	suite.mockLeadService.On("GetRecentlyConverted", 10).Return([]models.Lead{converted}, nil)
	suite.mockTicketService.On("GetRecent", 10).Return([]models.Ticket{ticket}, nil)
	suite.mockTicketService.On("GetRecentlyResolved", 10).Return([]models.Ticket{resolved}, nil)
	suite.mockTaskService.On("GetRecentlyCompleted", 10).Return([]models.Task{task}, nil)

	rec := suite.doGet(router, "/dashboard/activities")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var env dashboardEnvelope
	require.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &env))
	var activities []activityJSON
	require.NoError(suite.T(), json.Unmarshal(env.Data, &activities))
	require.Len(suite.T(), activities, 5)

	assert.Equal(suite.T(), []string{
		"lead-43-converted",
		"ticket-6-resolved",
		"ticket-5-created",
		"lead-42-created",
		"task-11-completed",
	}, []string{activities[0].ID, activities[1].ID, activities[2].ID, activities[3].ID, activities[4].ID})

	assert.Equal(suite.T(), "lead_converted", activities[0].Type)
	assert.Equal(suite.T(), "ticket_resolved", activities[1].Type)
	assert.Equal(suite.T(), "ticket_created", activities[2].Type)
	assert.Equal(suite.T(), "lead_created", activities[3].Type)
	assert.Equal(suite.T(), "task_completed", activities[4].Type)

	// username is the user's email: models.User has no username column.
	assert.Equal(suite.T(), "sales@example.com", activities[3].User.Username)
	assert.Equal(suite.T(), uint(7), activities[3].User.ID)
	assert.Equal(suite.T(), "Sam", activities[3].User.FirstName)
	assert.Equal(suite.T(), "Sales", activities[3].User.LastName)
	assert.True(suite.T(), activities[0].CreatedAt.Equal(base.Add(-1*time.Hour)))
}

// Tickets need not be assigned to anyone; an unassigned one must still produce
// an activity rather than panicking on the nil *User.
func (suite *DashboardHandlerTestSuite) TestGetActivities_UnassignedTicketIsGraceful() {
	router := suite.newAnalyticsRouter(models.RoleSupport, 1)

	ticket := models.Ticket{Title: "Orphan"}
	ticket.ID = 8
	ticket.CreatedAt = time.Now()

	suite.mockLeadService.On("GetRecent", 10).Return([]models.Lead{}, nil)
	suite.mockLeadService.On("GetRecentlyConverted", 10).Return([]models.Lead{}, nil)
	suite.mockTicketService.On("GetRecent", 10).Return([]models.Ticket{ticket}, nil)
	suite.mockTicketService.On("GetRecentlyResolved", 10).Return([]models.Ticket{}, nil)
	suite.mockTaskService.On("GetRecentlyCompleted", 10).Return([]models.Task{}, nil)

	rec := suite.doGet(router, "/dashboard/activities")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	var env dashboardEnvelope
	require.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &env))
	var activities []activityJSON
	require.NoError(suite.T(), json.Unmarshal(env.Data, &activities))
	require.Len(suite.T(), activities, 1)
	assert.Equal(suite.T(), uint(0), activities[0].User.ID)
	assert.Equal(suite.T(), "", activities[0].User.Username)
}

func (suite *DashboardHandlerTestSuite) TestGetActivities_LimitIsCappedAtFifty() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)

	suite.mockLeadService.On("GetRecent", 50).Return([]models.Lead{}, nil)
	suite.mockLeadService.On("GetRecentlyConverted", 50).Return([]models.Lead{}, nil)
	suite.mockTicketService.On("GetRecent", 50).Return([]models.Ticket{}, nil)
	suite.mockTicketService.On("GetRecentlyResolved", 50).Return([]models.Ticket{}, nil)
	suite.mockTaskService.On("GetRecentlyCompleted", 50).Return([]models.Task{}, nil)

	rec := suite.doGet(router, "/dashboard/activities?limit=500")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	suite.assertEmptyDataArray(rec)
}

// More candidate events than the requested limit must be truncated after the
// merge, keeping the most recent ones.
func (suite *DashboardHandlerTestSuite) TestGetActivities_TruncatedToLimitAfterMerge() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)

	now := time.Now()
	oldLead := models.Lead{FirstName: "Old"}
	oldLead.ID = 1
	oldLead.CreatedAt = now.Add(-48 * time.Hour)
	newTicket := models.Ticket{Title: "New"}
	newTicket.ID = 2
	newTicket.CreatedAt = now

	suite.mockLeadService.On("GetRecent", 1).Return([]models.Lead{oldLead}, nil)
	suite.mockLeadService.On("GetRecentlyConverted", 1).Return([]models.Lead{}, nil)
	suite.mockTicketService.On("GetRecent", 1).Return([]models.Ticket{newTicket}, nil)
	suite.mockTicketService.On("GetRecentlyResolved", 1).Return([]models.Ticket{}, nil)
	suite.mockTaskService.On("GetRecentlyCompleted", 1).Return([]models.Task{}, nil)

	rec := suite.doGet(router, "/dashboard/activities?limit=1")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	var env dashboardEnvelope
	require.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &env))
	var activities []activityJSON
	require.NoError(suite.T(), json.Unmarshal(env.Data, &activities))
	require.Len(suite.T(), activities, 1)
	assert.Equal(suite.T(), "ticket-2-created", activities[0].ID)
}

func (suite *DashboardHandlerTestSuite) TestGetUpcomingTasks_AdminSeesEveryAssignee() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	task := models.Task{Title: "Renewal", AssignedToID: 4}
	task.ID = 3
	suite.mockTaskService.On("GetUpcoming", 5).Return([]models.Task{task}, nil)

	rec := suite.doGet(router, "/dashboard/upcoming-tasks")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Contains(suite.T(), rec.Body.String(), "Renewal")
}

func (suite *DashboardHandlerTestSuite) TestGetUpcomingTasks_NonAdminScopedToAssignee() {
	router := suite.newAnalyticsRouter(models.RoleSupport, 12)
	suite.mockTaskService.On("GetUpcomingByAssignee", uint(12), 5).Return([]models.Task{}, nil)

	rec := suite.doGet(router, "/dashboard/upcoming-tasks")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	suite.assertEmptyDataArray(rec)
}

func (suite *DashboardHandlerTestSuite) TestGetUpcomingTasks_LimitIsCapped() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	suite.mockTaskService.On("GetUpcoming", 50).Return([]models.Task{}, nil)

	rec := suite.doGet(router, "/dashboard/upcoming-tasks?limit=999")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetRecentTickets_Success() {
	router := suite.newAnalyticsRouter(models.RoleSupport, 3)
	ticket := models.Ticket{Title: "Latest"}
	ticket.ID = 77
	suite.mockTicketService.On("GetRecent", 5).Return([]models.Ticket{ticket}, nil)

	rec := suite.doGet(router, "/dashboard/recent-tickets")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Contains(suite.T(), rec.Body.String(), "Latest")
}

func (suite *DashboardHandlerTestSuite) TestGetRecentTickets_CustomerForbidden() {
	router := suite.newAnalyticsRouter(models.RoleCustomer, 3)

	rec := suite.doGet(router, "/dashboard/recent-tickets")

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetNewLeads_AdminSeesAll() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	lead := models.Lead{FirstName: "Fresh"}
	lead.ID = 1
	suite.mockLeadService.On("GetRecent", 5).Return([]models.Lead{lead}, nil)

	rec := suite.doGet(router, "/dashboard/new-leads")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Contains(suite.T(), rec.Body.String(), "Fresh")
}

// Sales users only ever see leads they own, mirroring LeadHandler.List.
func (suite *DashboardHandlerTestSuite) TestGetNewLeads_SalesSeesOnlyOwnLeads() {
	router := suite.newAnalyticsRouter(models.RoleSales, 21)
	suite.mockLeadService.On("GetRecentByOwner", uint(21), 5).Return([]models.Lead{}, nil)

	rec := suite.doGet(router, "/dashboard/new-leads")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	suite.assertEmptyDataArray(rec)
}

// Support cannot list leads anywhere else in the API, so the widget returns an
// empty list rather than a 403 that would break the shared dashboard page.
func (suite *DashboardHandlerTestSuite) TestGetNewLeads_SupportSeesEmptyList() {
	router := suite.newAnalyticsRouter(models.RoleSupport, 5)
	// No lead-service expectations: support must not reach the data at all.

	rec := suite.doGet(router, "/dashboard/new-leads")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	suite.assertEmptyDataArray(rec)
}

func (suite *DashboardHandlerTestSuite) TestGetNewLeads_LimitIsCapped() {
	router := suite.newAnalyticsRouter(models.RoleAdmin, 1)
	suite.mockLeadService.On("GetRecent", 50).Return([]models.Lead{}, nil)

	rec := suite.doGet(router, "/dashboard/new-leads?limit=1000")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

// --- pure bucketing ----------------------------------------------------------

func bucketLabels(buckets []timeBucket) []string {
	labels := make([]string, len(buckets))
	for i, b := range buckets {
		labels[i] = b.label
	}
	return labels
}

func TestBuildTimeBuckets_MonthsEndOnTheCurrentMonth(t *testing.T) {
	now := time.Date(2026, 8, 5, 13, 45, 0, 0, time.UTC)

	buckets := buildTimeBuckets("month", now)

	require.Len(t, buckets, 12)
	assert.Equal(t, "2025-09", buckets[0].label)
	assert.Equal(t, "2026-08", buckets[11].label)
	assert.Equal(t, time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC), buckets[0].start)
	assert.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), buckets[11].end)
	// Buckets must tile the window with no gaps and no overlap.
	for i := 1; i < len(buckets); i++ {
		assert.Equal(t, buckets[i-1].end, buckets[i].start)
	}
}

// The classic off-by-one: an event on the last instant of a month must not leak
// into the next bucket, and one on the first instant of the next must not fall
// back into the previous.
func TestBucketTimestamps_MonthBoundaryJanuaryFebruary(t *testing.T) {
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	buckets := buildTimeBuckets("month", now)

	jan31 := time.Date(2026, 1, 31, 23, 59, 59, 999999999, time.UTC)
	feb1 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	feb28 := time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC)

	counts := bucketTimestamps(buckets, []time.Time{jan31, feb1, feb28})

	labels := bucketLabels(buckets)
	byLabel := map[string]int64{}
	for i, l := range labels {
		byLabel[l] = counts[i]
	}
	assert.Equal(t, int64(1), byLabel["2026-01"])
	assert.Equal(t, int64(2), byLabel["2026-02"])
	assert.Equal(t, int64(0), byLabel["2026-03"])
}

// A leap-day event must be counted, and February 2024 must be a bucket of its
// own rather than a 30-day approximation that swallows March 1.
func TestBucketTimestamps_LeapDay(t *testing.T) {
	now := time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)
	buckets := buildTimeBuckets("month", now)

	counts := bucketTimestamps(buckets, []time.Time{
		time.Date(2024, 2, 29, 23, 0, 0, 0, time.UTC),
		time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
	})

	byLabel := map[string]int64{}
	for i, l := range bucketLabels(buckets) {
		byLabel[l] = counts[i]
	}
	assert.Equal(t, int64(1), byLabel["2024-02"])
	assert.Equal(t, int64(1), byLabel["2024-03"])
}

func TestBuildTimeBuckets_QuarterRollover(t *testing.T) {
	// 1 April is the first instant of Q2: the newest bucket must be 2026-Q2.
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	buckets := buildTimeBuckets("quarter", now)

	require.Len(t, buckets, 8)
	assert.Equal(t, []string{
		"2024-Q3", "2024-Q4", "2025-Q1", "2025-Q2", "2025-Q3", "2025-Q4", "2026-Q1", "2026-Q2",
	}, bucketLabels(buckets))
	assert.Equal(t, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), buckets[7].start)
	assert.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), buckets[7].end)
}

func TestBucketTimestamps_QuarterBoundary(t *testing.T) {
	now := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	buckets := buildTimeBuckets("quarter", now)

	counts := bucketTimestamps(buckets, []time.Time{
		time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC), // last instant of Q1
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),     // first instant of Q2
		time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),   // 2025-Q4
	})

	byLabel := map[string]int64{}
	for i, l := range bucketLabels(buckets) {
		byLabel[l] = counts[i]
	}
	assert.Equal(t, int64(1), byLabel["2026-Q1"])
	assert.Equal(t, int64(1), byLabel["2026-Q2"])
	assert.Equal(t, int64(1), byLabel["2025-Q4"])
}

func TestBuildTimeBuckets_WeeksStartOnMonday(t *testing.T) {
	// 2026-08-05 is a Wednesday; its week starts Monday 2026-08-03.
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)

	buckets := buildTimeBuckets("week", now)

	require.Len(t, buckets, 12)
	assert.Equal(t, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), buckets[11].start)
	assert.Equal(t, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), buckets[11].end)
	assert.Equal(t, "2026-08-03", buckets[11].label)
	assert.Equal(t, "2026-05-18", buckets[0].label)
}

// Sunday is the last day of its week, not the first: Go's Weekday() puts Sunday
// at 0, which is the trap this guards.
func TestBuildTimeBuckets_SundayBelongsToTheWeekThatStartedMonday(t *testing.T) {
	now := time.Date(2026, 8, 9, 23, 0, 0, 0, time.UTC) // a Sunday

	buckets := buildTimeBuckets("week", now)

	assert.Equal(t, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), buckets[11].start)
	counts := bucketTimestamps(buckets, []time.Time{now})
	assert.Equal(t, int64(1), counts[11])
}

func TestBuildTimeBuckets_Years(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)

	buckets := buildTimeBuckets("year", now)

	require.Len(t, buckets, 5)
	assert.Equal(t, []string{"2022", "2023", "2024", "2025", "2026"}, bucketLabels(buckets))
	assert.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), buckets[4].end)
}

func TestBucketTimestamps_IgnoresTimestampsOutsideTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	buckets := buildTimeBuckets("month", now)

	counts := bucketTimestamps(buckets, []time.Time{
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), // long before the window
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), // after the window
	})

	var total int64
	for _, c := range counts {
		total += c
	}
	assert.Equal(t, int64(0), total)
	assert.Len(t, counts, 12)
}

func TestBucketTimestamps_EmptyInputYieldsZeroedCounts(t *testing.T) {
	buckets := buildTimeBuckets("month", time.Now())

	counts := bucketTimestamps(buckets, nil)

	require.Len(t, counts, 12)
	for _, c := range counts {
		assert.Equal(t, int64(0), c)
	}
}
