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

// @Description System-wide counters of the dashboard; never narrowed to the caller.
type DashboardStats struct {
	// Number of live leads, every status included.
	TotalLeads int64 `json:"total_leads" example:"120"`
	// Number of live customers.
	TotalCustomers int64 `json:"total_customers" example:"45"`
	// Number of tickets whose status is open or in_progress.
	OpenTickets int64 `json:"open_tickets" example:"8"`
	// Number of tasks whose status is pending or in_progress.
	PendingTasks int64 `json:"pending_tasks" example:"17"`
	// total_customers / total_leads * 100 as an unrounded percentage, 0 when
	// there are no leads. Customers created directly count too, so it can
	// exceed 100.
	ConversionRate float64 `json:"conversion_rate" example:"37.5"`
}

// GetStats godoc
// @Summary Dashboard counters
// @Description Admin, sales and support (route guard; customer gets 403). Returns system-wide counts: live leads, live customers, tickets in open or in_progress, tasks in pending or in_progress, and conversion_rate = customers / leads * 100 (0 without leads). Deliberately not narrowed to the caller: a sales or support user sees the same totals as an admin.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=DashboardStats} "The counters"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: a count could not be read"
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
//
// @Description One series of a chart: a label and one count per chart label.
type ChartDataset struct {
	// Series name, fixed per endpoint: Leads, Tickets, Tasks or Conversions.
	Label string `json:"label" example:"Leads"`
	// Counts, index-aligned with the chart's labels; never null.
	Data []int64 `json:"data" example:"12,30,9,4,20"`
}

// ChartData is the payload every distribution and time-series endpoint returns:
// a label per category and one dataset holding the matching counts. Both
// slices are always non-nil, so an empty database renders an empty chart rather
// than crashing the client on a JSON null.
//
// @Description Chart payload of the distribution and time-series widgets: category labels and a single dataset of counts.
type ChartData struct {
	// Category labels (statuses, priorities or time buckets) in display order; never null.
	Labels []string `json:"labels" example:"new,contacted,qualified,unqualified,converted"`
	// Exactly one dataset whose data is index-aligned with labels; never null.
	Datasets []ChartDataset `json:"datasets"`
}

// ActivityUser is the actor shown against an activity entry.
//
// Username carries the user's email address: models.User has no username
// column, and the email is the only human-readable handle an account has. The
// struct is always emitted, zero-valued when the underlying record has no
// resolvable owner or assignee (an unassigned ticket, say), so the client never
// has to guard against a missing object.
//
// @Description Account shown against an activity entry: the lead's owner or the ticket's or task's assignee. Always present; every field is zero-valued (id 0, empty strings) when the record has no such account.
type ActivityUser struct {
	// User id; 0 when there is no account.
	ID uint `json:"id" example:"7"`
	// The account's email address (there is no username column); "" when there is no account.
	Username string `json:"username" example:"sam.sales@example.com"`
	// Given name; "" when there is no account.
	FirstName string `json:"first_name" example:"Sam"`
	// Family name; "" when there is no account.
	LastName string `json:"last_name" example:"Seller"`
}

// Activity is one entry of the recent-activity feed. There is no activity or
// audit table in this schema, so entries are synthesised from the timestamps
// already carried by leads, tickets and tasks.
//
// @Description One entry of the recent-activity feed, synthesised from lead, ticket and task rows (there is no activity table).
type Activity struct {
	// Stable id {entity}-{record id}-{event}, e.g. lead-42-created.
	ID string `json:"id" example:"lead-42-created"`
	// Event kind.
	Type string `json:"type" enums:"lead_created,lead_converted,ticket_created,ticket_resolved,task_completed" example:"lead_created"`
	// Fixed title per type: New lead, Lead converted, New ticket, Ticket resolved, Task completed.
	Title string `json:"title" example:"New lead"`
	// For lead events "First Last (Company) was added as a lead" or "... was converted to a customer" (Unnamed lead when the names are empty); for ticket and task events the record's title.
	Description string `json:"description" example:"Ana Popescu (Acme SRL) was added as a lead"`
	// The lead's owner or the ticket's or task's assignee.
	User ActivityUser `json:"user"`
	// Event time (RFC 3339): created_at for lead_created and ticket_created, otherwise the record's updated_at, so a later edit moves the entry.
	CreatedAt time.Time `json:"created_at" example:"2026-10-30T09:12:00Z"`

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
// @Description Admin, sales and support (route guard; customer gets 403). Distribution of live leads across statuses as chart data with one dataset labelled Leads. Every status is always present, in the order new, contacted, qualified, unqualified, converted, with 0 when no lead holds it; a status found in the data but unknown to the model is appended afterwards in alphabetical order rather than dropped. System-wide: not narrowed to the caller's own leads (support, which cannot read leads elsewhere, sees these counts too).
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ChartData} "Lead status distribution"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the counts could not be read"
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
// @Description Admin, sales and support (route guard; customer gets 403). Distribution of live tickets across priorities as chart data with one dataset labelled Tickets. Every priority is always present, in the order low, medium, high, urgent, with 0 when no ticket holds it; a priority found in the data but unknown to the model is appended afterwards in alphabetical order. System-wide: not narrowed to the caller's assignments.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ChartData} "Ticket priority distribution"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the counts could not be read"
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
// @Description Admin, sales and support (route guard; customer gets 403). Distribution of live tasks across statuses as chart data with one dataset labelled Tasks. Every status is always present, in the order pending, in_progress, completed, cancelled, with 0 when no task holds it; a status found in the data but unknown to the model is appended afterwards in alphabetical order. System-wide: not narrowed to the caller's assignments.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=ChartData} "Task status distribution"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the counts could not be read"
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
// @Description Admin, sales and support (route guard; customer gets 403). Lead conversions over time as chart data with one dataset labelled Conversions: the number of leads in the converted status whose updated_at falls in each bucket. It counts conversions, not revenue. A lead has no converted_at column, so updated_at stands in for the conversion time and a converted lead edited later moves to the bucket of that edit. Buckets are calendar-aligned in the server's time zone and end with the one containing now: week gives the last 12 weeks starting on Monday, labelled by that Monday (2026-08-03); month the last 12 months (2026-08); quarter the last 8 quarters (2026-Q3); year the last 5 years (2026). Any other period falls back to month. System-wide: not narrowed to the caller's leads.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param period query string false "Bucket size and window; any other value gives month" Enums(week, month, quarter, year) default(month)
// @Success 200 {object} utils.APIResponse{data=ChartData} "Conversions per bucket, oldest first"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the conversions could not be read"
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
// @Description Admin, sales and support (route guard; customer gets 403). Recent events the caller may see, newest first, as a bare array. There is no activity table: entries are synthesised from leads created (lead_created) and converted (lead_converted), tickets created (ticket_created) and resolved or closed (ticket_resolved), and tasks completed (task_completed).
// @Description Each source is narrowed to the caller inside its query, before its limit: lead events cover every lead for admin, the caller's own leads for sales and none for support; ticket events cover every ticket for admin and sales and the tickets assigned to the caller for support; task events cover every task for admin and the tasks assigned to the caller for sales and support. Hidden records never appear and never crowd out visible ones. Each source contributes at most limit entries; they are merged, sorted by event time descending (ties by type, then record id descending) and cut to limit. Events without a dedicated timestamp use updated_at, so a later edit moves the entry.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum entries; values above 50 are capped at 50, missing, invalid or non-positive values give 10" default(10)
// @Success 200 {object} utils.APIResponse{data=[]Activity} "The feed (possibly empty)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: a source could not be read"
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
// @Description Admin, sales and support (route guard; customer gets 403). Open tasks with the nearest due dates, as a bare array ordered by due date ascending, ties by id. Completed and cancelled tasks and tasks without a due date are excluded; overdue tasks are included. Admin sees every assignee's tasks; sales and support only the tasks assigned to them. A task carries its lead (lead_id and lead) only when the caller may see that lead (admin: any; sales: own leads; support: none), as on GET /tasks/{id}.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum tasks; values above 50 are capped at 50, missing, invalid or non-positive values give 5" default(5)
// @Success 200 {object} utils.APIResponse{data=[]models.Task} "Upcoming tasks (possibly empty)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the tasks could not be read"
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
// @Description Admin, sales and support (route guard; customer gets 403). The most recently created tickets the caller may see, as a bare array ordered by creation time descending, ties by id descending. Admin and sales see every ticket; support only the tickets assigned to it (unassigned tickets excluded). The narrowing is part of the query and applies before the limit.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum tickets; values above 50 are capped at 50, missing, invalid or non-positive values give 5" default(5)
// @Success 200 {object} utils.APIResponse{data=[]models.Ticket} "Recent tickets (possibly empty)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the tickets could not be read"
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
// @Description Admin, sales and support (route guard; customer gets 403). The most recently created leads, as a bare array ordered by creation time descending. Admin sees every lead, sales only the leads it owns. Support, which cannot read leads anywhere in this API, gets an empty array rather than a 403 so its dashboard still loads.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param limit query int false "Maximum leads; values above 50 are capped at 50, missing, invalid or non-positive values give 5" default(5)
// @Success 200 {object} utils.APIResponse{data=[]models.Lead} "New leads; always empty for support"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the leads could not be read"
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
// @Description Admin and sales. Support passes the dashboard route guard but is refused by the handler with 403, as the customer role is by the guard. Admin sees every live deal, sales only its own. data.stages is the payload of GET /deals/pipeline without filters: all five stages in pipeline order (qualification, proposal, negotiation, won, lost), each present even when empty, with count and per-currency totals of amount_cents and weighted_cents = round_half_up(sum of amount_cents x probability / 100), rounded once per stage-and-currency total. data.won_this_month counts the deals in the won stage whose closed_at falls in the current calendar month in UTC (first instant of the month inclusive to first instant of the next exclusive), with the amounts per currency. Amounts in different currencies are never added together; totals are sorted by currency code and empty lists are [], never null.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=models.DealDashboardPipeline} "Pipeline widgets"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is support (Deals are available to the admin and sales roles only) or a customer (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the deals could not be read"
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
