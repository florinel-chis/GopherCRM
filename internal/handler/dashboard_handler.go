package handler

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type DashboardHandler struct {
	leadService     service.LeadService
	customerService service.CustomerService
	ticketService   service.TicketService
	taskService     service.TaskService
	dealService     service.DealService
}

func NewDashboardHandler(
	leadService service.LeadService,
	customerService service.CustomerService,
	ticketService service.TicketService,
	taskService service.TaskService,
	dealService service.DealService,
) *DashboardHandler {
	return &DashboardHandler{
		leadService:     leadService,
		customerService: customerService,
		ticketService:   ticketService,
		taskService:     taskService,
		dealService:     dealService,
	}
}

type DashboardStats struct {
	TotalLeads     int64   `json:"total_leads"`
	TotalCustomers int64   `json:"total_customers"`
	OpenTickets    int64   `json:"open_tickets"`
	PendingTasks   int64   `json:"pending_tasks"`
	ConversionRate float64 `json:"conversion_rate"`
}

// GetStats godoc
// @Summary Get dashboard statistics
// @Description Aggregate counts for the dashboard: total leads, total customers, open tickets and pending tasks, plus a conversion rate computed as customers / leads * 100 (0 when there are no leads). Restricted to the admin, sales and support roles; customer-role callers receive 403. The totals returned to permitted roles are unscoped and system-wide; nothing is filtered to the caller's own records.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=DashboardStats} "Dashboard statistics retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/stats [get]
func (h *DashboardHandler) GetStats(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetStats")

	// Get total leads
	totalLeads, err := h.leadService.GetCount()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get leads count")
		utils.RespondInternalError(c)
		return
	}

	// Get total customers
	totalCustomers, err := h.customerService.GetCount()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get customers count")
		utils.RespondInternalError(c)
		return
	}

	// Get open tickets count
	openTickets, err := h.ticketService.GetOpenCount()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get open tickets count")
		utils.RespondInternalError(c)
		return
	}

	// Get pending tasks count
	pendingTasks, err := h.taskService.GetPendingCount()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get pending tasks count")
		utils.RespondInternalError(c)
		return
	}

	// Calculate conversion rate (customers / leads * 100)
	conversionRate := float64(0)
	if totalLeads > 0 {
		conversionRate = (float64(totalCustomers) / float64(totalLeads)) * 100
	}

	stats := DashboardStats{
		TotalLeads:     totalLeads,
		TotalCustomers: totalCustomers,
		OpenTickets:    openTickets,
		PendingTasks:   pendingTasks,
		ConversionRate: conversionRate,
	}

	utils.LogHandlerResponse(logger, http.StatusOK, stats)
	utils.RespondSuccess(c, http.StatusOK, stats)
}

// ChartDataset is one series of a chart. The shape is fixed by the frontend
// chart component, which expects Chart.js-style datasets.
type ChartDataset struct {
	Label string  `json:"label"`
	Data  []int64 `json:"data"`
}

// ChartData is the payload every distribution and time-series endpoint returns:
// a label per category and one dataset holding the matching counts. Both
// slices are always non-nil, so an empty database renders an empty chart rather
// than crashing the client on a JSON null.
type ChartData struct {
	Labels   []string       `json:"labels"`
	Datasets []ChartDataset `json:"datasets"`
}

// ActivityUser is the actor shown against an activity entry.
//
// Username carries the user's email address: models.User has no username
// column, and the email is the only human-readable handle an account has. The
// struct is always emitted, zero-valued when the underlying record has no
// resolvable owner or assignee (an unassigned ticket, say), so the client never
// has to guard against a missing object.
type ActivityUser struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// Activity is one entry of the recent-activity feed. There is no activity or
// audit table in this schema, so entries are synthesised from the timestamps
// already carried by leads, tickets and tasks.
type Activity struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	User        ActivityUser `json:"user"`
	CreatedAt   time.Time    `json:"created_at"`

	// sourceID is the numeric id of the underlying record, kept for the merge
	// tie-break only and never serialised.
	sourceID uint
}

const (
	// maxDashboardLimit caps every list-shaped dashboard endpoint. These widgets
	// are decoration on a page that also loads real lists; nobody needs 10,000
	// rows through them, and the cap keeps a hostile ?limit= from turning a
	// dashboard load into a table scan.
	maxDashboardLimit = 50

	defaultActivityLimit = 10
	defaultWidgetLimit   = 5
)

// canonicalLeadStatuses and friends fix both the order and the completeness of
// each chart's labels. Reading them off the data instead would give a chart
// whose axis changes shape as rows come and go, and whose category order is
// whatever the database happened to return.
var (
	canonicalLeadStatuses   = []string{"new", "contacted", "qualified", "unqualified", "converted"}
	canonicalTicketPriority = []string{"low", "medium", "high", "urgent"}
	canonicalTaskStatuses   = []string{"pending", "in_progress", "completed", "cancelled"}
)

// parseDashboardLimit reads ?limit=, falling back to fallback when it is absent,
// unparseable or non-positive, and capping it at maxDashboardLimit.
func parseDashboardLimit(c *gin.Context, fallback int) int {
	limit := fallback
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maxDashboardLimit {
		limit = maxDashboardLimit
	}
	return limit
}

// buildChart turns a sparse count map into a chart with a stable axis: every
// canonical category appears, in order, zero-filled when absent, and any status
// the map holds that the model does not know about is appended afterwards in
// sorted order rather than being silently dropped.
func buildChart(datasetLabel string, canonical []string, counts map[string]int64) ChartData {
	labels := make([]string, 0, len(canonical)+len(counts))
	data := make([]int64, 0, len(canonical)+len(counts))

	known := make(map[string]bool, len(canonical))
	for _, label := range canonical {
		known[label] = true
		labels = append(labels, label)
		data = append(data, counts[label])
	}

	extras := make([]string, 0)
	for label := range counts {
		if !known[label] {
			extras = append(extras, label)
		}
	}
	sort.Strings(extras)
	for _, label := range extras {
		labels = append(labels, label)
		data = append(data, counts[label])
	}

	return ChartData{
		Labels:   labels,
		Datasets: []ChartDataset{{Label: datasetLabel, Data: data}},
	}
}

// timeBucket is one column of a time series: a half-open interval [start, end)
// and the label the chart shows for it.
type timeBucket struct {
	label string
	start time.Time
	end   time.Time
}

// buildTimeBuckets lays out the window for a sales-performance period, oldest
// bucket first, ending with the bucket that contains `now`.
//
// All of this is deliberately Go rather than SQL. Bucketing by month or quarter
// in the database would need DATE_FORMAT on MySQL 8 and strftime on SQLite —
// two different dialects for the same query, only one of which the test suite
// ever exercises. Calendar arithmetic here is also correct across month lengths
// and leap years for free, which a "30 days ago" approximation is not.
//
// An unrecognised period falls back to months, which is what the frontend
// defaults to.
func buildTimeBuckets(period string, now time.Time) []timeBucket {
	loc := now.Location()

	switch period {
	case "week":
		// Weeks start on Monday. Go numbers Sunday 0, so Sunday has to be pulled
		// back six days rather than treated as the start of a new week.
		offset := (int(now.Weekday()) + 6) % 7
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
		return bucketsFrom(start, 12, func(t time.Time, n int) time.Time {
			return t.AddDate(0, 0, 7*n)
		}, func(t time.Time) string {
			return t.Format("2006-01-02")
		})

	case "quarter":
		firstMonthOfQuarter := time.Month(((int(now.Month())-1)/3)*3 + 1)
		start := time.Date(now.Year(), firstMonthOfQuarter, 1, 0, 0, 0, 0, loc)
		return bucketsFrom(start, 8, func(t time.Time, n int) time.Time {
			return t.AddDate(0, 3*n, 0)
		}, func(t time.Time) string {
			return fmt.Sprintf("%d-Q%d", t.Year(), (int(t.Month())-1)/3+1)
		})

	case "year":
		start := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, loc)
		return bucketsFrom(start, 5, func(t time.Time, n int) time.Time {
			return t.AddDate(n, 0, 0)
		}, func(t time.Time) string {
			return t.Format("2006")
		})

	default: // "month"
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return bucketsFrom(start, 12, func(t time.Time, n int) time.Time {
			return t.AddDate(0, n, 0)
		}, func(t time.Time) string {
			return t.Format("2006-01")
		})
	}
}

// bucketsFrom walks back `count-1` periods from the newest bucket's start and
// then forward again, so consecutive buckets always share a boundary exactly:
// every bucket's end is the next one's start, leaving no gap for a timestamp to
// fall through and no overlap for it to be double counted.
//
// step must be exact calendar arithmetic (AddDate), never a duration multiple,
// or February and the DST changeovers drift.
func bucketsFrom(newestStart time.Time, count int, step func(time.Time, int) time.Time, label func(time.Time) string) []timeBucket {
	buckets := make([]timeBucket, 0, count)
	oldestStart := step(newestStart, -(count - 1))
	for i := 0; i < count; i++ {
		start := step(oldestStart, i)
		buckets = append(buckets, timeBucket{
			label: label(start),
			start: start,
			end:   step(oldestStart, i+1),
		})
	}
	return buckets
}

// bucketTimestamps counts timestamps into buckets. Anything outside the window
// is ignored rather than clamped into the first or last bucket, which would
// invent a spike that never happened.
func bucketTimestamps(buckets []timeBucket, timestamps []time.Time) []int64 {
	counts := make([]int64, len(buckets))
	for _, ts := range timestamps {
		for i, bucket := range buckets {
			if !ts.Before(bucket.start) && ts.Before(bucket.end) {
				counts[i]++
				break
			}
		}
	}
	return counts
}

// GetLeadsByStatus godoc
// @Summary Lead count by status
// @Description Distribution of leads across the lead statuses, as chart data with a single "Leads" dataset. Every status the model defines is always present, in the canonical order new, contacted, qualified, unqualified, converted, with a zero count when no lead holds it; a status found in the data but unknown to the model is appended afterwards rather than dropped. The counts are system-wide and are not narrowed to the caller's own records. Restricted to the admin, sales and support roles; customer-role callers receive 403.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ChartData} "Lead status distribution retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/leads-by-status [get]
func (h *DashboardHandler) GetLeadsByStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetLeadsByStatus")

	counts, err := h.leadService.GetStatusCounts()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get lead status counts")
		utils.RespondInternalError(c)
		return
	}

	chart := buildChart("Leads", canonicalLeadStatuses, counts)
	utils.LogHandlerResponse(logger, http.StatusOK, chart)
	utils.RespondSuccess(c, http.StatusOK, chart)
}

// GetTicketsByPriority godoc
// @Summary Ticket count by priority
// @Description Distribution of tickets across the ticket priorities, as chart data with a single "Tickets" dataset. Every priority the model defines is always present, in the canonical order low, medium, high, urgent, with a zero count when no ticket holds it; a priority found in the data but unknown to the model is appended afterwards rather than dropped. The counts are system-wide and are not narrowed to the caller's own records. Restricted to the admin, sales and support roles; customer-role callers receive 403.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ChartData} "Ticket priority distribution retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/tickets-by-priority [get]
func (h *DashboardHandler) GetTicketsByPriority(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetTicketsByPriority")

	counts, err := h.ticketService.GetPriorityCounts()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get ticket priority counts")
		utils.RespondInternalError(c)
		return
	}

	chart := buildChart("Tickets", canonicalTicketPriority, counts)
	utils.LogHandlerResponse(logger, http.StatusOK, chart)
	utils.RespondSuccess(c, http.StatusOK, chart)
}

// GetTasksByStatus godoc
// @Summary Task count by status
// @Description Distribution of tasks across the task statuses, as chart data with a single "Tasks" dataset. Every status the model defines is always present, in the canonical order pending, in_progress, completed, cancelled, with a zero count when no task holds it; a status found in the data but unknown to the model is appended afterwards rather than dropped. The counts are system-wide and are not narrowed to the caller's own assignments. Restricted to the admin, sales and support roles; customer-role callers receive 403.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ChartData} "Task status distribution retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/tasks-by-status [get]
func (h *DashboardHandler) GetTasksByStatus(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetTasksByStatus")

	counts, err := h.taskService.GetStatusCounts()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get task status counts")
		utils.RespondInternalError(c)
		return
	}

	chart := buildChart("Tasks", canonicalTaskStatuses, counts)
	utils.LogHandlerResponse(logger, http.StatusOK, chart)
	utils.RespondSuccess(c, http.StatusOK, chart)
}

// GetSalesPerformance godoc
// @Summary Lead conversions over time
// @Description Lead CONVERSIONS over time, as chart data with a single "Conversions" dataset: the number of leads that reached the converted status in each bucket of the chosen window. It is a count of conversions, not revenue or pipeline value, of which this CRM tracks neither. A lead carries no dedicated converted_at column, so its updated_at stands in for the conversion time — a converted lead edited later moves to the bucket of that edit. Buckets are calendar-aligned and end with the one containing the current instant: week gives the last 12 ISO weeks labelled by their Monday (2026-08-03), month the last 12 months (2026-08), quarter the last 8 quarters (2026-Q3), year the last 5 years (2026). An unrecognised period falls back to month. The counts are system-wide and are not narrowed to the caller's own leads. Restricted to the admin, sales and support roles; customer-role callers receive 403.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param period query string false "Bucket size and window length" Enums(week, month, quarter, year) default(month)
// @Success 200 {object} utils.APIResponse{data=ChartData} "Conversion time series retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/sales-performance [get]
func (h *DashboardHandler) GetSalesPerformance(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetSalesPerformance")

	period := c.DefaultQuery("period", "month")
	buckets := buildTimeBuckets(period, time.Now())

	timestamps, err := h.leadService.GetConversionTimestamps(buckets[0].start)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get lead conversion timestamps")
		utils.RespondInternalError(c)
		return
	}

	labels := make([]string, len(buckets))
	for i, bucket := range buckets {
		labels[i] = bucket.label
	}

	chart := ChartData{
		Labels:   labels,
		Datasets: []ChartDataset{{Label: "Conversions", Data: bucketTimestamps(buckets, timestamps)}},
	}

	utils.LogHandlerResponse(logger, http.StatusOK, chart)
	utils.RespondSuccess(c, http.StatusOK, chart)
}

// GetActivities godoc
// @Summary Recent activity feed
// @Description Recent activity the caller may see, newest first, as a bare array. There is no activity or audit table in this schema, so entries are synthesised from the rows themselves: leads created (lead_created) and converted (lead_converted), tickets created (ticket_created) and resolved or closed (ticket_resolved), and tasks completed (task_completed). Each source is narrowed to the caller's visibility inside its query, before its limit: lead events cover every lead for admin, only the caller's own leads for sales and no lead at all for support; ticket events cover every ticket for admin and sales and only the tickets assigned to the caller for support; task events cover every task for admin and only the tasks assigned to the caller for every other role. Hidden records therefore never appear and never crowd out permitted ones. Each source contributes at most `limit` candidates; they are merged, sorted by event time descending (ties broken by type, then by record id descending) and truncated to `limit`. Events with no dedicated timestamp column use updated_at as the event time, so a later edit moves the entry. Each id is stable and of the form "lead-42-created". The user is the lead's owner or the ticket's or task's assignee, with username carrying the account's email address because there is no username column; when the record has no assignee the user object is present but zero-valued. Restricted to the admin, sales and support roles; customer-role callers receive 403.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum entries to return, capped at 50" minimum(1) maximum(50) default(10)
// @Success 200 {object} utils.APIResponse{data=[]Activity} "Activity feed retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/activities [get]
func (h *DashboardHandler) GetActivities(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetActivities")

	limit := parseDashboardLimit(c, defaultActivityLimit)
	role := c.GetString("user_role")
	userID := c.GetUint("user_id")
	activities := make([]Activity, 0, limit)

	// Each source is asked for `limit` rows: any one of them could supply the
	// whole page, and none of them can supply more than it. Every source is
	// narrowed to the caller inside its query, so the limit counts only rows
	// the caller may see.
	//
	// Lead events mirror GetNewLeads: admin sees every lead, sales only the
	// leads it owns, and support, which has no lead visibility anywhere in this
	// API, reads no lead at all.
	var createdLeads, convertedLeads []models.Lead
	var err error
	switch role {
	case string(models.RoleAdmin):
		createdLeads, err = h.leadService.GetRecent(limit)
	case string(models.RoleSales):
		createdLeads, err = h.leadService.GetRecentByOwner(userID, limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get recent leads")
		utils.RespondInternalError(c)
		return
	}
	for _, lead := range createdLeads {
		activities = append(activities, Activity{
			ID:          fmt.Sprintf("lead-%d-created", lead.ID),
			Type:        "lead_created",
			Title:       "New lead",
			Description: describePerson(lead.FirstName, lead.LastName, lead.Company) + " was added as a lead",
			User:        activityUserFrom(&lead.Owner),
			CreatedAt:   lead.CreatedAt,
			sourceID:    lead.ID,
		})
	}

	switch role {
	case string(models.RoleAdmin):
		convertedLeads, err = h.leadService.GetRecentlyConverted(limit)
	case string(models.RoleSales):
		convertedLeads, err = h.leadService.GetRecentlyConvertedByOwner(userID, limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get recently converted leads")
		utils.RespondInternalError(c)
		return
	}
	for _, lead := range convertedLeads {
		activities = append(activities, Activity{
			ID:          fmt.Sprintf("lead-%d-converted", lead.ID),
			Type:        "lead_converted",
			Title:       "Lead converted",
			Description: describePerson(lead.FirstName, lead.LastName, lead.Company) + " was converted to a customer",
			User:        activityUserFrom(&lead.Owner),
			CreatedAt:   lead.UpdatedAt,
			sourceID:    lead.ID,
		})
	}

	allTickets := seesEveryTicket(role)
	var createdTickets []models.Ticket
	if allTickets {
		createdTickets, err = h.ticketService.GetRecent(limit)
	} else {
		createdTickets, err = h.ticketService.GetRecentByAssignee(userID, limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get recent tickets")
		utils.RespondInternalError(c)
		return
	}
	for _, ticket := range createdTickets {
		activities = append(activities, Activity{
			ID:          fmt.Sprintf("ticket-%d-created", ticket.ID),
			Type:        "ticket_created",
			Title:       "New ticket",
			Description: ticket.Title,
			User:        activityUserFrom(ticket.AssignedTo),
			CreatedAt:   ticket.CreatedAt,
			sourceID:    ticket.ID,
		})
	}

	var resolvedTickets []models.Ticket
	if allTickets {
		resolvedTickets, err = h.ticketService.GetRecentlyResolved(limit)
	} else {
		resolvedTickets, err = h.ticketService.GetRecentlyResolvedByAssignee(userID, limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get recently resolved tickets")
		utils.RespondInternalError(c)
		return
	}
	for _, ticket := range resolvedTickets {
		activities = append(activities, Activity{
			ID:          fmt.Sprintf("ticket-%d-resolved", ticket.ID),
			Type:        "ticket_resolved",
			Title:       "Ticket resolved",
			Description: ticket.Title,
			User:        activityUserFrom(ticket.AssignedTo),
			CreatedAt:   ticket.UpdatedAt,
			sourceID:    ticket.ID,
		})
	}

	// Task events follow GetUpcomingTasks: admin sees every assignee, every
	// other role only its own assignments.
	var completedTasks []models.Task
	if role == string(models.RoleAdmin) {
		completedTasks, err = h.taskService.GetRecentlyCompleted(limit)
	} else {
		completedTasks, err = h.taskService.GetRecentlyCompletedByAssignee(userID, limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get recently completed tasks")
		utils.RespondInternalError(c)
		return
	}
	for _, task := range completedTasks {
		activities = append(activities, Activity{
			ID:          fmt.Sprintf("task-%d-completed", task.ID),
			Type:        "task_completed",
			Title:       "Task completed",
			Description: task.Title,
			User:        activityUserFrom(&task.AssignedTo),
			CreatedAt:   task.UpdatedAt,
			sourceID:    task.ID,
		})
	}

	sortActivities(activities)

	if len(activities) > limit {
		activities = activities[:limit]
	}

	utils.LogHandlerResponse(logger, http.StatusOK, activities)
	utils.RespondSuccess(c, http.StatusOK, activities)
}

// seesEveryTicket reports whether a role sees every ticket on the dashboard.
// Admin and sales do, mirroring the ticket list; support, and any role this
// code does not know, sees only the tickets assigned to it.
func seesEveryTicket(role string) bool {
	return role == string(models.RoleAdmin) || role == string(models.RoleSales)
}

// sortActivities orders the merged feed newest first. Equal timestamps, which
// second-granularity columns produce routinely, are broken by type and then by
// record id descending. Within one source that is exactly the order of its
// query (event time DESC, id DESC), so truncating the merge to the limit keeps
// the same rows the sources would have returned for the whole feed.
func sortActivities(activities []Activity) {
	sort.SliceStable(activities, func(i, j int) bool {
		a, b := activities[i], activities[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.sourceID > b.sourceID
	})
}

// describePerson renders a lead as "First Last (Company)", degrading gracefully
// when either half is missing.
func describePerson(firstName, lastName, company string) string {
	name := firstName
	if lastName != "" {
		if name != "" {
			name += " "
		}
		name += lastName
	}
	if name == "" {
		name = "Unnamed lead"
	}
	if company != "" {
		return name + " (" + company + ")"
	}
	return name
}

// activityUserFrom projects a user onto the feed's actor shape. A nil user, or
// one that was never loaded, yields the zero value rather than a nil object:
// the frontend reads activity.user.username unconditionally.
func activityUserFrom(user *models.User) ActivityUser {
	if user == nil || user.ID == 0 {
		return ActivityUser{}
	}
	return ActivityUser{
		ID:        user.ID,
		Username:  user.Email,
		FirstName: user.FirstName,
		LastName:  user.LastName,
	}
}

// GetUpcomingTasks godoc
// @Summary Tasks due soonest
// @Description The open tasks with the nearest due dates, as a bare array ordered by due date ascending. Completed and cancelled tasks are excluded, as are tasks with no due date; overdue tasks are deliberately included, since they are the ones that most need attention. Admins see every assignee's tasks; every other role sees only the tasks assigned to them. A task carries its lead (lead_id and lead) only when the caller may see that lead, as on GET /tasks/{id}. Restricted to the admin, sales and support roles; customer-role callers receive 403.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum tasks to return, capped at 50" minimum(1) maximum(50) default(5)
// @Success 200 {object} utils.APIResponse{data=[]models.Task} "Upcoming tasks retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/upcoming-tasks [get]
func (h *DashboardHandler) GetUpcomingTasks(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetUpcomingTasks")

	limit := parseDashboardLimit(c, defaultWidgetLimit)

	var tasks []models.Task
	var err error
	if c.GetString("user_role") == string(models.RoleAdmin) {
		tasks, err = h.taskService.GetUpcoming(limit)
	} else {
		tasks, err = h.taskService.GetUpcomingByAssignee(c.GetUint("user_id"), limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get upcoming tasks")
		utils.RespondInternalError(c)
		return
	}
	if tasks == nil {
		tasks = []models.Task{}
	}
	service.RedactTaskLeads(leadViewer(c), tasks)

	utils.LogHandlerResponse(logger, http.StatusOK, tasks)
	utils.RespondSuccess(c, http.StatusOK, tasks)
}

// GetRecentTickets godoc
// @Summary Newest tickets
// @Description The most recently created tickets the caller may see, as a bare array ordered by creation time descending, ties broken by id descending. Admin and sales see every ticket; support sees only the tickets assigned to it, so tickets assigned to someone else and unassigned tickets are excluded. The narrowing is part of the query and applies before the limit, so hidden tickets never crowd out permitted ones; a caller with no visible ticket gets an empty array. Restricted to the admin, sales and support roles; customer-role callers receive 403.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum tickets to return, capped at 50" minimum(1) maximum(50) default(5)
// @Success 200 {object} utils.APIResponse{data=[]models.Ticket} "Recent tickets retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/recent-tickets [get]
func (h *DashboardHandler) GetRecentTickets(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetRecentTickets")

	limit := parseDashboardLimit(c, defaultWidgetLimit)

	var tickets []models.Ticket
	var err error
	if seesEveryTicket(c.GetString("user_role")) {
		tickets, err = h.ticketService.GetRecent(limit)
	} else {
		tickets, err = h.ticketService.GetRecentByAssignee(c.GetUint("user_id"), limit)
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get recent tickets")
		utils.RespondInternalError(c)
		return
	}
	if tickets == nil {
		tickets = []models.Ticket{}
	}

	utils.LogHandlerResponse(logger, http.StatusOK, tickets)
	utils.RespondSuccess(c, http.StatusOK, tickets)
}

// GetNewLeads godoc
// @Summary Newest leads
// @Description The most recently created leads, as a bare array ordered by creation time descending. Visibility mirrors the lead list: admins see every lead, and sales users see only the leads they own. Support users cannot list leads anywhere in this API — the lead routes are admin and sales only — so they receive an empty array here rather than a 403, which would otherwise break the dashboard page they are entitled to load. Customer-role callers receive 403 from the role guard.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum leads to return, capped at 50" minimum(1) maximum(50) default(5)
// @Success 200 {object} utils.APIResponse{data=[]models.Lead} "New leads retrieved successfully; empty for support users"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/new-leads [get]
func (h *DashboardHandler) GetNewLeads(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetNewLeads")

	limit := parseDashboardLimit(c, defaultWidgetLimit)
	role := c.GetString("user_role")

	var leads []models.Lead
	var err error
	switch role {
	case string(models.RoleAdmin):
		leads, err = h.leadService.GetRecent(limit)
	case string(models.RoleSales):
		// Sales users own a subset of the pipeline and see only that subset,
		// exactly as LeadHandler.List narrows them.
		leads, err = h.leadService.GetRecentByOwner(c.GetUint("user_id"), limit)
	default:
		// Support reaches this endpoint through the shared dashboard guard but
		// has no lead visibility anywhere else; an empty widget is the honest
		// answer, and it keeps the page loading for them.
		leads = []models.Lead{}
	}
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get new leads")
		utils.RespondInternalError(c)
		return
	}
	if leads == nil {
		leads = []models.Lead{}
	}

	utils.LogHandlerResponse(logger, http.StatusOK, leads)
	utils.RespondSuccess(c, http.StatusOK, leads)
}

// dashboardDealRoles are the roles that see the deal widgets: deals are admin
// and sales only, so support, which passes the dashboard guard, does not.
var dashboardDealRoles = map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true}

// GetPipeline godoc
// @Summary Deal pipeline and won-this-month widgets
// @Description The pipeline widget and the won-this-month tile of the dashboard. Admin sees every live deal, sales only its own. Support passes the dashboard guard but has no access to deals and gets 403, as does the customer role. data.stages is the same payload as GET /deals/pipeline without filters: all five stages in pipeline order (qualification, proposal, negotiation, won, lost), each present even when empty, with count and per-currency totals of amount_cents and weighted_cents = round_half_up(sum of amount_cents × probability / 100), rounded once per stage-and-currency total. data.won_this_month counts the deals in the won stage whose closed_at falls in the current calendar month in UTC (from the first instant of the month, inclusive, to the first instant of the next, exclusive), with the amounts per currency. Amounts in different currencies are never added together; totals are sorted by currency code and empty lists are [], never null.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=models.DealDashboardPipeline} "Pipeline widgets retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required (support has no access to deals)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /dashboard/pipeline [get]
func (h *DashboardHandler) GetPipeline(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetPipeline")

	role := models.UserRole(c.GetString("user_role"))
	if !dashboardDealRoles[role] {
		logger.WithField("role", role).Warn("Deal widgets refused")
		utils.RespondForbidden(c, "Deals are available to the admin and sales roles only")
		return
	}

	pipeline, err := h.dealService.DashboardPipeline(c.GetUint("user_id"), role)
	if err != nil {
		if errors.Is(err, apperrors.ErrForbidden) {
			logger.WithError(utils.LogError(err)).Warn("Deal widgets refused")
			utils.RespondForbidden(c, "Deals are available to the admin and sales roles only")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to get the deal pipeline")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, pipeline)
	utils.RespondSuccess(c, http.StatusOK, pipeline)
}
