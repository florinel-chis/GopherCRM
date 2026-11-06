package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// These suites drive the ticket SLA fields of #278 through the real ticket,
// comment, bulk status and dashboard routes, services and repositories on
// SQLite. Every stored assertion reads the tickets row itself.

// --- helpers -------------------------------------------------------------------

var slaTimestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// slaView is the part of a ticket answer #278 is about.
type slaView struct {
	ID               uint    `json:"id"`
	Title            string  `json:"title"`
	Status           string  `json:"status"`
	DueAt            *string `json:"due_at"`
	FirstRespondedAt *string `json:"first_responded_at"`
	Overdue          bool    `json:"overdue"`
}

func slaFormat(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// decodeSLA reads one ticket object and requires the three keys to be present.
func decodeSLA(t *testing.T, raw json.RawMessage, context string) slaView {
	t.Helper()
	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &keys), "%s: %s", context, string(raw))
	for _, key := range []string{"due_at", "first_responded_at", "overdue"} {
		require.Contains(t, keys, key, "%s: %s is always present", context, key)
	}
	var view slaView
	require.NoError(t, json.Unmarshal(raw, &view), context)
	for _, value := range []*string{view.DueAt, view.FirstRespondedAt} {
		if value != nil {
			assert.Regexp(t, slaTimestampPattern, *value, "%s: UTC RFC 3339 with milliseconds", context)
		}
	}
	return view
}

type storedSLA struct {
	DueAt            *time.Time
	FirstRespondedAt *time.Time
}

func readStoredSLA(t *testing.T, e *ticketVisibilityEnv, id uint) storedSLA {
	t.Helper()
	var row storedSLA
	require.NoError(t, e.db.Unscoped().Model(&models.Ticket{}).Select("due_at, first_responded_at").Where("id = ?", id).Scan(&row).Error)
	return row
}

func ticketCount(t *testing.T, e *ticketVisibilityEnv) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Unscoped().Model(&models.Ticket{}).Count(&n).Error)
	return n
}

// assertDueAt checks one answer's due_at against the stored column.
func assertDueAt(t *testing.T, view slaView, stored *time.Time, context string) {
	t.Helper()
	if stored == nil {
		assert.Nil(t, view.DueAt, "%s: due_at is null", context)
		return
	}
	if assert.NotNil(t, view.DueAt, "%s: due_at", context) {
		assert.Equal(t, slaFormat(*stored), *view.DueAt, "%s: due_at is the stored value", context)
	}
}

func createTicketViaAPI(t *testing.T, e *ticketVisibilityEnv, as *models.User, payload map[string]interface{}) (uint, slaView) {
	t.Helper()
	status, body, envelope := e.call(t, as, http.MethodPost, "/tickets", payload)
	require.Equal(t, http.StatusCreated, status, body)
	view := decodeSLA(t, envelope.Data, "POST /tickets")
	require.NotZero(t, view.ID)
	return view.ID, view
}

func ticketBody(customer *models.Customer, title string, extra map[string]interface{}) map[string]interface{} {
	body := map[string]interface{}{"title": title, "description": "SLA request", "customer_id": customer.ID}
	for key, value := range extra {
		body[key] = value
	}
	return body
}

type slaErrorBody struct {
	Success bool `json:"success"`
	Error   struct {
		Code    string                 `json:"code"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

// --- AC: due_at is set, changed, kept and cleared ------------------------------------

func TestTicketDueAtLifecycleSQLite(t *testing.T) {
	e := newTicketVisibilityEnv(t, false)

	getView := func(t *testing.T, as *models.User, id uint) slaView {
		t.Helper()
		status, body, envelope := e.call(t, as, http.MethodGet, fmt.Sprintf("/tickets/%d", id), nil)
		require.Equal(t, http.StatusOK, status, body)
		return decodeSLA(t, envelope.Data, "GET")
	}
	put := func(t *testing.T, as *models.User, id uint, payload map[string]interface{}) slaView {
		t.Helper()
		status, body, envelope := e.call(t, as, http.MethodPut, fmt.Sprintf("/tickets/%d", id), payload)
		require.Equal(t, http.StatusOK, status, body)
		return decodeSLA(t, envelope.Data, "PUT")
	}
	// check holds the answer, a fresh GET and the stored column to want.
	check := func(t *testing.T, as *models.User, id uint, answer slaView, want *time.Time, context string) {
		t.Helper()
		stored := readStoredSLA(t, e, id)
		if want == nil {
			assert.Nil(t, stored.DueAt, "%s: stored NULL", context)
		} else if assert.NotNil(t, stored.DueAt, "%s: stored", context) {
			assert.True(t, want.Equal(*stored.DueAt), "%s: stored %v, want %v", context, *stored.DueAt, *want)
			assert.Zero(t, stored.DueAt.Nanosecond()%int(time.Millisecond), "%s: millisecond precision", context)
		}
		assertDueAt(t, answer, stored.DueAt, context+" answer")
		assertDueAt(t, getView(t, as, id), stored.DueAt, context+" fresh GET")
	}

	first := time.Date(2026, 12, 1, 8, 0, 0, 123_000_000, time.UTC)
	second := time.Date(2027, 1, 15, 16, 30, 0, 0, time.UTC)
	third := time.Date(2027, 2, 1, 0, 0, 0, 999_000_000, time.UTC)

	for _, caller := range []*models.User{e.admin, e.sue} {
		t.Run(caller.Email, func(t *testing.T) {
			id, view := createTicketViaAPI(t, e, caller, ticketBody(e.customerA, "Due on create",
				map[string]interface{}{"due_at": "2026-12-01T10:00:00.123456789+02:00"}))
			check(t, caller, id, view, &first, "POST with an offset")

			noDue, view := createTicketViaAPI(t, e, caller, ticketBody(e.customerA, "No due date", nil))
			check(t, caller, noDue, view, nil, "POST without due_at")
			nullDue, view := createTicketViaAPI(t, e, caller, ticketBody(e.customerA, "Null due date",
				map[string]interface{}{"due_at": nil}))
			check(t, caller, nullDue, view, nil, "POST with null")

			view = put(t, caller, noDue, map[string]interface{}{"due_at": "2026-12-01T08:00:00.123Z"})
			check(t, caller, noDue, view, &first, "PUT sets")

			view = put(t, caller, noDue, map[string]interface{}{"due_at": "2027-01-15T11:30:00-05:00"})
			check(t, caller, noDue, view, &second, "PUT changes")

			view = put(t, caller, noDue, map[string]interface{}{"title": "Renamed only"})
			check(t, caller, noDue, view, &second, "PUT with a title only")
			assert.Equal(t, "Renamed only", view.Title)

			view = put(t, caller, noDue, map[string]interface{}{"status": "in_progress"})
			check(t, caller, noDue, view, &second, "PUT with a status only")
			assert.Equal(t, "in_progress", view.Status)

			view = put(t, caller, noDue, map[string]interface{}{})
			check(t, caller, noDue, view, &second, "PUT with an empty body")

			view = put(t, caller, noDue, map[string]interface{}{"due_at": "2027-02-01T00:00:00.999999Z", "priority": "urgent"})
			check(t, caller, noDue, view, &third, "PUT changes again beside another field")

			view = put(t, caller, noDue, map[string]interface{}{"due_at": nil})
			check(t, caller, noDue, view, nil, "PUT null clears")

			view = put(t, caller, noDue, map[string]interface{}{"title": "Still no due date"})
			check(t, caller, noDue, view, nil, "PUT without due_at keeps NULL")

			view = put(t, caller, id, map[string]interface{}{"due_at": nil, "title": "Cleared beside a title"})
			check(t, caller, id, view, nil, "PUT null beside a title")
		})
	}
}

// --- AC: an invalid due_at is 400 and writes nothing --------------------------------

func TestTicketDueAtInvalidWritesNothingSQLite(t *testing.T) {
	e := newTicketVisibilityEnv(t, false)
	invalid := map[string]interface{}{
		"impossible month":      "2026-13-01",
		"impossible month time": "2026-13-01T10:00:00Z",
		"a word":                "tomorrow",
		"a date without a time": "2026-12-01",
		"no offset":             "2026-12-01T10:00:00",
		"empty string":          "",
		"a number":              1764583200,
		"a float":               12.5,
		"a boolean":             true,
		"false":                 false,
		"an object":             map[string]interface{}{"at": "2026-12-01T10:00:00Z"},
		"an array":              []string{"2026-12-01T10:00:00Z"},
	}
	assertRefused := func(t *testing.T, status int, body, context string) {
		t.Helper()
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", context, body)
		var parsed slaErrorBody
		require.NoError(t, json.Unmarshal([]byte(body), &parsed), body)
		assert.False(t, parsed.Success)
		assert.Equal(t, "INVALID_DUE_AT", parsed.Error.Code, "%s: %s", context, body)
		assert.Equal(t, "due_at", parsed.Error.Details["field"], context)
	}

	t.Run("POST /tickets", func(t *testing.T) {
		for _, caller := range []*models.User{e.admin, e.sue} {
			for name, value := range invalid {
				before := ticketCount(t, e)
				status, body, _ := e.call(t, caller, http.MethodPost, "/tickets",
					ticketBody(e.customerA, "Must not exist "+name, map[string]interface{}{"due_at": value, "priority": "high"}))
				assertRefused(t, status, body, caller.Email+" "+name)
				assert.Equal(t, before, ticketCount(t, e), "%s %s: the tickets row count is unchanged", caller.Email, name)
			}
		}
		var leaked int64
		require.NoError(t, e.db.Unscoped().Model(&models.Ticket{}).Where("title LIKE ?", "Must not exist%").Count(&leaked).Error)
		assert.Zero(t, leaked)
	})

	t.Run("PUT /tickets/{id}", func(t *testing.T) {
		stored := time.Date(2026, 12, 24, 18, 0, 0, 250_000_000, time.UTC)
		for _, caller := range []*models.User{e.admin, e.sue} {
			for _, withDue := range []bool{true, false} {
				extra := map[string]interface{}{}
				if withDue {
					extra["due_at"] = "2026-12-24T18:00:00.250Z"
				}
				id, _ := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerA, "Kept as it was",
					mergeExtra(extra, map[string]interface{}{"assigned_to_id": e.sue.ID})))
				for name, value := range invalid {
					var before models.Ticket
					require.NoError(t, e.db.First(&before, id).Error)
					count := ticketCount(t, e)

					status, body, _ := e.call(t, caller, http.MethodPut, fmt.Sprintf("/tickets/%d", id), map[string]interface{}{
						"due_at": value, "title": "Must not be stored", "status": "resolved", "priority": "urgent",
						"resolution": "Must not be stored either", "description": "Nor this",
					})
					context := fmt.Sprintf("%s withDue=%t %s", caller.Email, withDue, name)
					assertRefused(t, status, body, context)

					var after models.Ticket
					require.NoError(t, e.db.First(&after, id).Error)
					assert.Equal(t, count, ticketCount(t, e), context)
					assert.Equal(t, before.Title, after.Title, "%s: title", context)
					assert.Equal(t, before.Status, after.Status, "%s: status", context)
					assert.Equal(t, before.Priority, after.Priority, "%s: priority", context)
					assert.Equal(t, before.Description, after.Description, "%s: description", context)
					assert.Equal(t, before.Resolution, after.Resolution, "%s: resolution", context)
					assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "%s: updated_at", context)
					if withDue {
						require.NotNil(t, after.DueAt, context)
						assert.True(t, stored.Equal(*after.DueAt), "%s: due_at", context)
					} else {
						assert.Nil(t, after.DueAt, "%s: due_at", context)
					}
					assert.Nil(t, after.FirstRespondedAt, context)
				}
			}
		}
	})
}

func mergeExtra(a, b map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for key, value := range a {
		out[key] = value
	}
	for key, value := range b {
		out[key] = value
	}
	return out
}

// --- AC: first_responded_at is the first staff reply, set once ------------------------

func TestTicketFirstResponseSQLite(t *testing.T) {
	e := newTicketVisibilityEnv(t, false)
	forged := "2001-01-01T00:00:00Z"

	commentOn := func(t *testing.T, as *models.User, id uint, body string, internal bool, want int) {
		t.Helper()
		status, raw, _ := e.call(t, as, http.MethodPost, fmt.Sprintf("/tickets/%d/comments", id),
			map[string]interface{}{"body": body, "is_internal": internal})
		require.Equal(t, want, status, raw)
	}
	commentCreatedAt := func(t *testing.T, id uint, body string) time.Time {
		t.Helper()
		var comment models.TicketComment
		require.NoError(t, e.db.Where("ticket_id = ? AND body = ?", id, body).First(&comment).Error)
		return comment.CreatedAt
	}
	put := func(t *testing.T, as *models.User, id uint, payload map[string]interface{}) slaView {
		t.Helper()
		status, body, envelope := e.call(t, as, http.MethodPut, fmt.Sprintf("/tickets/%d", id), payload)
		require.Equal(t, http.StatusOK, status, body)
		return decodeSLA(t, envelope.Data, "PUT")
	}
	get := func(t *testing.T, as *models.User, id uint) (slaView, map[string]interface{}) {
		t.Helper()
		status, body, envelope := e.call(t, as, http.MethodGet, fmt.Sprintf("/tickets/%d", id), nil)
		require.Equal(t, http.StatusOK, status, body)
		var full map[string]interface{}
		require.NoError(t, json.Unmarshal(envelope.Data, &full))
		return decodeSLA(t, envelope.Data, "GET"), full
	}
	unstamped := func(t *testing.T, id uint, context string) {
		t.Helper()
		assert.Nil(t, readStoredSLA(t, e, id).FirstRespondedAt, "%s: first_responded_at stays NULL", context)
	}

	// The comment author is the assigned support user or the admin.
	for _, responder := range []*models.User{e.sue, e.admin} {
		t.Run("first reply by "+responder.Email, func(t *testing.T) {
			id, view := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerA, "Needs an answer", map[string]interface{}{
				"assigned_to_id": e.sue.ID, "first_responded_at": forged, "overdue": true,
			}))
			assert.Nil(t, view.FirstRespondedAt, "a new ticket has first_responded_at null")
			assert.False(t, view.Overdue)
			unstamped(t, id, "POST with first_responded_at in the body")

			commentOn(t, e.admin, id, "Admin internal note", true, http.StatusCreated)
			unstamped(t, id, "admin internal note")
			commentOn(t, e.sue, id, "Support internal note", true, http.StatusCreated)
			unstamped(t, id, "support internal note")
			commentOn(t, e.sales, id, "Sales try", false, http.StatusForbidden)
			commentOn(t, e.owner, id, "Customer try", false, http.StatusForbidden)
			commentOn(t, e.sam, id, "Other agent try", false, http.StatusForbidden)
			unstamped(t, id, "refused comments")

			put(t, e.admin, id, map[string]interface{}{"status": "in_progress"})
			unstamped(t, id, "status change")
			put(t, e.admin, id, map[string]interface{}{"assigned_to_id": e.sam.ID})
			put(t, e.admin, id, map[string]interface{}{"assigned_to_id": e.sue.ID})
			unstamped(t, id, "reassignment")
			put(t, e.sue, id, map[string]interface{}{"due_at": "2026-12-01T08:00:00Z"})
			unstamped(t, id, "due_at change")
			put(t, e.sue, id, map[string]interface{}{"first_responded_at": forged, "overdue": true})
			unstamped(t, id, "PUT with first_responded_at in the body")
			status, body, _ := e.call(t, e.admin, http.MethodPost, "/tickets/bulk/status",
				map[string]interface{}{"ticket_ids": []uint{id}, "status": "open"})
			require.Equal(t, http.StatusOK, status, body)
			unstamped(t, id, "bulk status")

			// A client reads the ticket before the first reply ...
			_, staleRead := get(t, e.admin, id)
			assert.Nil(t, staleRead["first_responded_at"])

			commentOn(t, responder, id, "First public reply", false, http.StatusCreated)
			stamp := readStoredSLA(t, e, id).FirstRespondedAt
			require.NotNil(t, stamp, "the first non-internal staff comment stamps the ticket")
			createdAt := commentCreatedAt(t, id, "First public reply")
			assert.True(t, createdAt.Equal(*stamp), "stamp %v is the comment's created_at %v", *stamp, createdAt)
			view, _ = get(t, e.admin, id)
			require.NotNil(t, view.FirstRespondedAt)
			assert.Equal(t, slaFormat(*stamp), *view.FirstRespondedAt)

			// ... and later comments, notes and writes never move it.
			time.Sleep(5 * time.Millisecond)
			commentOn(t, e.admin, id, "Second public reply", false, http.StatusCreated)
			commentOn(t, e.sue, id, "Third public reply", false, http.StatusCreated)
			commentOn(t, e.sue, id, "Late internal note", true, http.StatusCreated)
			assertStamp := func(context string) {
				t.Helper()
				now := readStoredSLA(t, e, id).FirstRespondedAt
				if assert.NotNil(t, now, context) {
					assert.True(t, stamp.Equal(*now), "%s: the first stamp is kept", context)
				}
			}
			assertStamp("later comments")

			// ... including a PUT built from the read taken before the stamp.
			stalePut := map[string]interface{}{}
			for _, key := range []string{"title", "description", "status", "priority", "resolution", "due_at", "first_responded_at", "overdue"} {
				stalePut[key] = staleRead[key]
			}
			view = put(t, e.admin, id, stalePut)
			assertStamp("stale PUT")
			require.NotNil(t, view.FirstRespondedAt, "the PUT answer carries the stored stamp")
			assert.Equal(t, slaFormat(*stamp), *view.FirstRespondedAt)

			put(t, e.admin, id, map[string]interface{}{"first_responded_at": nil})
			assertStamp("PUT with first_responded_at null")
			put(t, e.admin, id, map[string]interface{}{"first_responded_at": forged, "status": "resolved"})
			assertStamp("PUT with a forged first_responded_at")
			status, body, _ = e.call(t, e.admin, http.MethodPost, "/tickets/bulk/status",
				map[string]interface{}{"ticket_ids": []uint{id}, "status": "closed"})
			require.Equal(t, http.StatusOK, status, body)
			assertStamp("bulk status after the stamp")
		})
	}

	t.Run("another ticket is not stamped by this one's replies", func(t *testing.T) {
		replied, _ := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerA, "Replied", map[string]interface{}{"assigned_to_id": e.sue.ID}))
		quiet, _ := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerA, "Quiet", map[string]interface{}{"assigned_to_id": e.sue.ID}))
		commentOn(t, e.sue, replied, "Only here", false, http.StatusCreated)
		assert.NotNil(t, readStoredSLA(t, e, replied).FirstRespondedAt)
		unstamped(t, quiet, "a neighbouring ticket")
	})
}

// --- AC: every read path carries the three fields -----------------------------------

func TestTicketSLAFieldsOnEveryReadPathSQLite(t *testing.T) {
	e := newTicketVisibilityEnv(t, false)

	// The dashboard routes on the same router and database.
	require.NoError(t, e.db.AutoMigrate(&models.Lead{}, &models.Label{}, &models.Task{}))
	userRepo := repository.NewUserRepository(e.db)
	leadRepo := repository.NewLeadRepository(e.db)
	customerRepo := repository.NewCustomerRepository(e.db)
	companyRepo := repository.NewCompanyRepository(e.db)
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, userRepo, utils.NewTransactionManager(e.db))
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo)
	ticketService := service.NewTicketService(repository.NewTicketRepository(e.db), customerRepo, userRepo)
	taskService := service.NewTaskService(repository.NewTaskRepository(e.db), userRepo, leadRepo, customerRepo,
		repository.NewLabelRepository(e.db), repository.NewDealRepository(e.db))
	handler.SetupDashboardRoutes(e.router.Group(""),
		handler.NewDashboardHandler(leadService, customerService, ticketService, taskService, nil))

	// A hidden neighbour of another agent and customer, so every list has a row
	// the denied callers could have seen.
	hidden, _ := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerB, "Hidden neighbour",
		map[string]interface{}{"assigned_to_id": e.sam.ID, "due_at": "2001-01-01T00:00:00Z"}))

	pastDue := "2000-06-01T12:00:00.250Z"
	id, created := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerA, "Overdue ticket",
		map[string]interface{}{"assigned_to_id": e.sue.ID, "due_at": pastDue}))
	require.NotNil(t, created.DueAt)
	assert.Equal(t, pastDue, *created.DueAt)
	assert.True(t, created.Overdue, "POST /tickets answer")
	assert.Nil(t, created.FirstRespondedAt)

	status, body, _ := e.call(t, e.sue, http.MethodPost, fmt.Sprintf("/tickets/%d/comments", id), map[string]interface{}{"body": "Looking"})
	require.Equal(t, http.StatusCreated, status, body)
	stamp := readStoredSLA(t, e, id).FirstRespondedAt
	require.NotNil(t, stamp)
	wantFirst := slaFormat(*stamp)

	findIn := func(t *testing.T, raw json.RawMessage, list bool, context string) (slaView, bool) {
		t.Helper()
		var rows []json.RawMessage
		if list {
			var data struct {
				Tickets []json.RawMessage `json:"tickets"`
			}
			require.NoError(t, json.Unmarshal(raw, &data), context)
			rows = data.Tickets
		} else {
			require.NoError(t, json.Unmarshal(raw, &rows), context)
		}
		for _, row := range rows {
			view := decodeSLA(t, row, context)
			if view.ID == id {
				return view, true
			}
		}
		return slaView{}, false
	}

	type route struct {
		caller *models.User
		path   string
		kind   string // "one", "list" or "array"
	}
	listPath := "/tickets?limit=100"
	detailPath := fmt.Sprintf("/tickets/%d", id)
	customerPath := fmt.Sprintf("/customers/%d/tickets?limit=100", e.customerA.ID)
	allowed := []route{
		{e.admin, listPath, "list"}, {e.sales, listPath, "list"}, {e.sue, listPath, "list"},
		{e.admin, listPath + "&search=Overdue", "list"}, {e.admin, listPath + "&sort_by=title", "list"},
		{e.sue, "/tickets/my?limit=100", "list"},
		{e.admin, detailPath, "one"}, {e.sales, detailPath, "one"}, {e.sue, detailPath, "one"}, {e.owner, detailPath, "one"},
		{e.admin, customerPath, "list"}, {e.sales, customerPath, "list"}, {e.sue, customerPath, "list"}, {e.owner, customerPath, "list"},
		{e.admin, "/dashboard/recent-tickets?limit=50", "array"}, {e.sales, "/dashboard/recent-tickets?limit=50", "array"},
		{e.sue, "/dashboard/recent-tickets?limit=50", "array"},
	}

	checkRoutes := func(t *testing.T, wantOverdue bool, wantStatus string) {
		t.Helper()
		for _, r := range allowed {
			context := r.caller.Email + " " + r.path
			status, body, envelope := e.call(t, r.caller, http.MethodGet, r.path, nil)
			require.Equal(t, http.StatusOK, status, "%s: %s", context, body)
			var view slaView
			if r.kind == "one" {
				view = decodeSLA(t, envelope.Data, context)
			} else {
				var found bool
				view, found = findIn(t, envelope.Data, r.kind == "list", context)
				require.True(t, found, "%s lists the ticket", context)
			}
			assert.Equal(t, wantStatus, view.Status, context)
			if assert.NotNil(t, view.DueAt, context) {
				assert.Equal(t, pastDue, *view.DueAt, context)
			}
			if assert.NotNil(t, view.FirstRespondedAt, context) {
				assert.Equal(t, wantFirst, *view.FirstRespondedAt, context)
			}
			assert.Equal(t, wantOverdue, view.Overdue, context)
		}
	}

	checkDenied := func(t *testing.T) {
		t.Helper()
		for _, denied := range []struct {
			caller *models.User
			path   string
			status int
		}{
			{e.sam, detailPath, http.StatusForbidden},
			{e.sid, detailPath, http.StatusForbidden},
			{e.otherOwner, detailPath, http.StatusForbidden},
			{e.unlinked, detailPath, http.StatusForbidden},
			{e.owner, listPath, http.StatusForbidden},
			{e.owner, "/tickets/my", http.StatusForbidden},
			{e.otherOwner, customerPath, http.StatusForbidden},
			{e.owner, "/dashboard/recent-tickets", http.StatusForbidden},
			{e.admin, "/tickets/999999", http.StatusNotFound},
		} {
			status, body, _ := e.call(t, denied.caller, http.MethodGet, denied.path, nil)
			assert.Equal(t, denied.status, status, "%s %s: %s", denied.caller.Email, denied.path, body)
			assert.NotContains(t, body, "Overdue ticket", "%s %s", denied.caller.Email, denied.path)
		}
		for _, r := range []route{
			{e.sam, listPath, "list"}, {e.sid, listPath, "list"}, {e.sam, "/tickets/my?limit=100", "list"},
			{e.sam, customerPath, "list"}, {e.sam, "/dashboard/recent-tickets?limit=50", "array"},
		} {
			status, body, envelope := e.call(t, r.caller, http.MethodGet, r.path, nil)
			require.Equal(t, http.StatusOK, status, body)
			_, found := findIn(t, envelope.Data, r.kind == "list", r.caller.Email+" "+r.path)
			assert.False(t, found, "%s %s does not find the ticket", r.caller.Email, r.path)
		}
		status, body, envelope := e.call(t, e.sam, http.MethodGet, fmt.Sprintf("/tickets/%d", hidden), nil)
		require.Equal(t, http.StatusOK, status, body)
		decodeSLA(t, envelope.Data, "the hidden neighbour for its own agent")
	}

	t.Run("open and past due", func(t *testing.T) {
		checkRoutes(t, true, "open")
		checkDenied(t)
	})

	t.Run("the PUT answer", func(t *testing.T) {
		status, body, envelope := e.call(t, e.sue, http.MethodPut, detailPath, map[string]interface{}{"status": "in_progress"})
		require.Equal(t, http.StatusOK, status, body)
		view := decodeSLA(t, envelope.Data, "PUT in_progress")
		assert.True(t, view.Overdue, "in progress and past due")
		require.NotNil(t, view.FirstRespondedAt)
		assert.Equal(t, wantFirst, *view.FirstRespondedAt)
		checkRoutes(t, true, "in_progress")

		status, body, envelope = e.call(t, e.sue, http.MethodPut, detailPath, map[string]interface{}{"status": "resolved"})
		require.Equal(t, http.StatusOK, status, body)
		view = decodeSLA(t, envelope.Data, "PUT resolved")
		assert.False(t, view.Overdue, "a resolved ticket is never overdue")
		require.NotNil(t, view.DueAt)
		assert.Equal(t, pastDue, *view.DueAt, "resolving keeps due_at")
	})

	t.Run("resolved", func(t *testing.T) {
		checkRoutes(t, false, "resolved")
		checkDenied(t)
	})

	t.Run("closed", func(t *testing.T) {
		status, body, _ := e.call(t, e.admin, http.MethodPost, "/tickets/bulk/status",
			map[string]interface{}{"ticket_ids": []uint{id}, "status": "closed"})
		require.Equal(t, http.StatusOK, status, body)
		checkRoutes(t, false, "closed")
	})

	t.Run("a future due date is not overdue", func(t *testing.T) {
		future, view := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerA, "Due later",
			map[string]interface{}{"assigned_to_id": e.sue.ID, "due_at": "2999-01-01T00:00:00Z"}))
		assert.False(t, view.Overdue)
		status, body, envelope := e.call(t, e.sue, http.MethodGet, fmt.Sprintf("/tickets/%d", future), nil)
		require.Equal(t, http.StatusOK, status, body)
		assert.False(t, decodeSLA(t, envelope.Data, "future").Overdue)

		none, view := createTicketViaAPI(t, e, e.admin, ticketBody(e.customerA, "No due date", map[string]interface{}{"assigned_to_id": e.sue.ID}))
		assert.False(t, view.Overdue)
		assert.Nil(t, view.DueAt)
		status, body, envelope = e.call(t, e.sue, http.MethodGet, fmt.Sprintf("/tickets/%d", none), nil)
		require.Equal(t, http.StatusOK, status, body)
		assert.False(t, decodeSLA(t, envelope.Data, "no due date").Overdue)
	})
}
