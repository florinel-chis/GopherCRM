package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// #218: GET /tickets filters on status and priority in the query, before
// offset and limit. These roots drive the real ticket routes, handler, service
// and repositories over SQLite and, when TEST_MYSQL_DSN is set, a disposable
// MySQL schema (newTicketVisibilityEnv). The fixture mixes every status and
// priority across two agents, unassigned and admin-held tickets and a
// soft-deleted one, and the expected pages are computed from that fixture.

func TestTicketListFiltersSQLite(t *testing.T) {
	runTicketListFilters(t, false)
}

func TestTicketListFiltersMySQL(t *testing.T) {
	runTicketListFilters(t, true)
}

type filterTicket struct {
	ticket   *models.Ticket
	status   models.TicketStatus
	priority models.TicketPriority
	assignee *models.User
	text     string // title, description and resolution joined, for search
	at       time.Time
	deleted  bool
}

type filterQuery struct {
	status, priority, search string
}

func (q filterQuery) values() url.Values {
	v := url.Values{}
	if q.status != "" {
		v.Set("status", q.status)
	}
	if q.priority != "" {
		v.Set("priority", q.priority)
	}
	if q.search != "" {
		v.Set("search", q.search)
	}
	return v
}

// expectedFilterIDs returns the ids caller may see under q, ordered like the
// repository: id ascending without a sort, otherwise the column and then id,
// both in the requested direction.
func expectedFilterIDs(all []filterTicket, caller *models.User, q filterQuery, sortBy, sortOrder string) []uint {
	rows := []filterTicket{}
	for _, f := range all {
		if f.deleted {
			continue
		}
		if caller.Role == models.RoleSupport && (f.assignee == nil || f.assignee.ID != caller.ID) {
			continue
		}
		if q.status != "" && string(f.status) != q.status {
			continue
		}
		if q.priority != "" && string(f.priority) != q.priority {
			continue
		}
		if q.search != "" && !strings.Contains(f.text, q.search) {
			continue
		}
		rows = append(rows, f)
	}
	desc := sortBy != "" && sortOrder == "desc"
	sort.SliceStable(rows, func(i, j int) bool {
		c := 0
		switch sortBy {
		case "priority":
			c = strings.Compare(string(rows[i].priority), string(rows[j].priority))
		case "status":
			c = strings.Compare(string(rows[i].status), string(rows[j].status))
		case "title":
			c = strings.Compare(rows[i].ticket.Title, rows[j].ticket.Title)
		case "created_at":
			c = rows[i].at.Compare(rows[j].at)
		}
		if c == 0 {
			if rows[i].ticket.ID < rows[j].ticket.ID {
				c = -1
			} else if rows[i].ticket.ID > rows[j].ticket.ID {
				c = 1
			}
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	ids := []uint{}
	for _, f := range rows {
		ids = append(ids, f.ticket.ID)
	}
	return ids
}

type filterErrorEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code    string                 `json:"code"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

func runTicketListFilters(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)
	a, b := e.customerA, e.customerB

	var all []filterTicket
	add := func(customer *models.Customer, assignee *models.User, title, description, resolution string,
		status models.TicketStatus, priority models.TicketPriority, minute int) filterTicket {
		ticket := e.ticket(t, ticketSpec{customer: customer, assignee: assignee, title: title, description: description,
			resolution: resolution, priority: priority, minute: minute})
		require.NoError(t, e.db.Model(ticket).UpdateColumn("status", status).Error)
		ticket.Status = status
		f := filterTicket{ticket: ticket, status: status, priority: priority, assignee: assignee}
		f.text = strings.Join([]string{ticket.Title, ticket.Description, ticket.Resolution}, "\n")
		f.at = e.base.Add(time.Duration(minute) * time.Minute)
		all = append(all, f)
		return f
	}

	// Hidden from Sue and created first; they match her filters and searches.
	samOpen := add(a, e.sam, "Printer escalation", "", "", models.TicketStatusOpen, models.TicketPriorityHigh, 0)
	looseOpen := add(a, nil, "Printer loose", "", "", models.TicketStatusOpen, models.TicketPriorityHigh, 0)
	add(a, e.admin, "Admin audit", "", "", models.TicketStatusResolved, models.TicketPriorityUrgent, 0)
	// Sue's tickets, with ties on priority, title and timestamps.
	add(a, e.sue, "Printer jam", "", "", models.TicketStatusOpen, models.TicketPriorityHigh, 1)
	add(a, e.sue, "Login failure", "Printer offline after login", "", models.TicketStatusResolved, models.TicketPriorityLow, 2)
	add(b, e.sue, "VPN outage", "", "", models.TicketStatusOpen, models.TicketPriorityLow, 2)
	add(b, e.sue, "Printer driver", "", "", models.TicketStatusInProgress, models.TicketPriorityHigh, 3)
	add(b, e.sue, "Billing question", "", "Credited Printer toner", models.TicketStatusResolved, models.TicketPriorityHigh, 3)
	add(a, e.sam, "Sam resolved", "", "", models.TicketStatusResolved, models.TicketPriorityHigh, 4)
	add(b, nil, "Closed loose", "", "", models.TicketStatusClosed, models.TicketPriorityUrgent, 4)
	add(a, e.sue, "Sue closed", "", "", models.TicketStatusClosed, models.TicketPriorityUrgent, 5)
	gone := add(a, e.sue, "Printer gone", "", "", models.TicketStatusOpen, models.TicketPriorityHigh, 5)
	require.NoError(t, e.db.Delete(gone.ticket).Error)
	all[len(all)-1].deleted = true
	add(a, e.sue, "Shared title", "", "", models.TicketStatusOpen, models.TicketPriorityMedium, 6)
	add(b, e.sue, "Shared title", "", "", models.TicketStatusOpen, models.TicketPriorityHigh, 6)
	add(b, e.sue, "Shared title", "", "", models.TicketStatusInProgress, models.TicketPriorityUrgent, 6)

	staff := []*models.User{e.admin, e.sales, e.sue, e.sam, e.sid}
	filterQueries := []filterQuery{
		{status: "open"},
		{status: "in_progress"},
		{status: "resolved"},
		{status: "closed"},
		{priority: "low"},
		{priority: "medium"},
		{priority: "high"},
		{priority: "urgent"},
		{status: "open", priority: "high"},
		{status: "resolved", priority: "high"},
		{status: "closed", priority: "urgent"},
		{status: "open", priority: "medium"},
		{status: "closed", priority: "low"},
	}
	path := func(q filterQuery, extra string) string {
		p := "/tickets?" + q.values().Encode()
		if extra != "" {
			p += "&" + extra
		}
		return p
	}

	t.Run("each filter alone and both together, per role, with totals", func(t *testing.T) {
		for _, caller := range staff {
			for _, q := range filterQueries {
				want := expectedFilterIDs(all, caller, q, "", "")
				p := path(q, "limit=100")
				page := e.expectPage(t, caller, p, want, int64(len(want)))
				context := caller.Email + " " + p
				assert.Equal(t, 1, page.meta.Page, context)
				assert.Equal(t, 100, page.meta.PerPage, context)
				wantPages := int64(0)
				if len(want) > 0 {
					wantPages = 1
				}
				assert.Equal(t, wantPages, page.meta.TotalPages, context)
				if caller.Role == models.RoleSupport {
					assertAllAssignedTo(t, page, caller, context)
				}
				assert.NotContains(t, page.body, gone.ticket.Title, "%s: soft-deleted tickets are never listed", context)
			}
		}
		// Sanity: the fixture really separates the roles.
		assert.Equal(t, []uint{samOpen.ticket.ID, looseOpen.ticket.ID},
			expectedFilterIDs(all, e.admin, filterQuery{status: "open"}, "", "")[:2])
		assert.NotEmpty(t, expectedFilterIDs(all, e.sue, filterQuery{status: "open", priority: "high"}, "", ""))
		assert.Empty(t, expectedFilterIDs(all, e.sid, filterQuery{status: "open"}, "", ""))
	})

	t.Run("paging walks the filtered set exactly once", func(t *testing.T) {
		for _, caller := range staff {
			for _, q := range filterQueries {
				want := expectedFilterIDs(all, caller, q, "", "")
				for _, limit := range []int{1, 2, 3} {
					wantPages := (int64(len(want)) + int64(limit) - 1) / int64(limit)
					got := []uint{}
					for page := 1; page <= int(wantPages)+1; page++ {
						p := path(q, fmt.Sprintf("limit=%d&page=%d", limit, page))
						listed := e.list(t, caller, p)
						context := caller.Email + " " + p
						assert.Equal(t, int64(len(want)), listed.total, context)
						assert.Equal(t, int64(len(want)), listed.meta.Total, context)
						assert.Equal(t, wantPages, listed.meta.TotalPages, context)
						assert.Equal(t, page, listed.meta.Page, context)
						assert.Equal(t, limit, listed.meta.PerPage, context)
						if int64(page) > wantPages {
							assert.Empty(t, listed.ids, context)
						}
						got = append(got, listed.ids...)
					}
					assert.Equal(t, want, got, "%s %+v limit=%d", caller.Email, q, limit)
				}
				// offset-based paging agrees.
				if len(want) > 1 {
					p := path(q, "offset=1&limit=100")
					e.expectPage(t, caller, p, want[1:], int64(len(want)))
				}
			}
		}
	})

	t.Run("filters combine with search and sort", func(t *testing.T) {
		combos := []filterQuery{
			{status: "open", search: "Printer"},
			{priority: "high", search: "Printer"},
			{status: "resolved", priority: "high", search: "Printer"},
			{status: "open", search: "Shared"},
			{priority: "low", search: "zz-no-such-ticket"},
		}
		sorts := []struct{ by, order string }{
			{"", ""}, {"priority", "asc"}, {"priority", "desc"}, {"title", "asc"}, {"title", "desc"},
			{"created_at", "desc"}, {"status", "asc"},
		}
		for _, caller := range staff {
			for _, q := range append(combos, filterQueries...) {
				for _, s := range sorts {
					extra := "limit=100"
					if s.by != "" {
						extra += "&sort_by=" + s.by + "&sort_order=" + s.order
					}
					want := expectedFilterIDs(all, caller, q, s.by, s.order)
					e.expectPage(t, caller, path(q, extra), want, int64(len(want)))
				}
			}
		}
		// Sorted pages walk the same order without repeats or gaps.
		for _, caller := range []*models.User{e.admin, e.sue} {
			q := filterQuery{priority: "high"}
			want := expectedFilterIDs(all, caller, q, "title", "desc")
			got := []uint{}
			for page := 1; page <= len(want); page++ {
				got = append(got, e.list(t, caller, path(q, fmt.Sprintf("sort_by=title&sort_order=desc&limit=2&page=%d", page))).ids...)
			}
			assert.Equal(t, want, got, caller.Email)
		}
		// The search OR cannot let another status, priority or assignee through.
		page := e.expectPage(t, e.sue, path(filterQuery{status: "open", priority: "high", search: "Printer"}, ""),
			expectedFilterIDs(all, e.sue, filterQuery{status: "open", priority: "high", search: "Printer"}, "", ""), 1)
		assertNoneOf(t, page.body, []string{samOpen.ticket.Title, looseOpen.ticket.Title, "Printer driver", "Credited Printer toner",
			"Printer offline after login", gone.ticket.Title}, "sue open+high+Printer")
	})

	t.Run("support filters only narrow the assigned tickets", func(t *testing.T) {
		for _, q := range filterQueries {
			for _, extra := range []string{
				fmt.Sprintf("assigned_to_id=%d", e.sam.ID),
				fmt.Sprintf("assignee_id=%d", e.sam.ID),
				fmt.Sprintf("user_id=%d", e.sam.ID),
				fmt.Sprintf("customer_id=%d", a.ID),
				"search=" + url.QueryEscape("%"),
			} {
				want := expectedFilterIDs(all, e.sue, q, "", "")
				page := e.expectPage(t, e.sue, path(q, extra+"&limit=100"), want, int64(len(want)))
				assertAllAssignedTo(t, page, e.sue, "sue "+extra)
				assertNoneOf(t, page.body, []string{samOpen.ticket.Title, looseOpen.ticket.Title, "Admin audit", "Sam resolved",
					"Closed loose"}, "sue "+extra)
			}
		}
	})

	t.Run("a repeated parameter uses its first value", func(t *testing.T) {
		for _, caller := range []*models.User{e.admin, e.sales, e.sue} {
			want := expectedFilterIDs(all, caller, filterQuery{status: "resolved", priority: "high"}, "", "")
			e.expectPage(t, caller, "/tickets?status=resolved&status=open&priority=high&priority=low&limit=100", want, int64(len(want)))
		}
	})

	t.Run("customers remain forbidden with filters", func(t *testing.T) {
		for _, caller := range []*models.User{e.owner, e.otherOwner, e.unlinked} {
			for _, p := range []string{"/tickets?status=open", "/tickets?priority=high", "/tickets?status=open&priority=high",
				"/tickets?status=bogus", "/tickets?status=&priority="} {
				status, body, _ := e.call(t, caller, http.MethodGet, p, nil)
				assert.Equal(t, http.StatusForbidden, status, "%s %s", caller.Email, p)
				assert.NotContains(t, body, `"tickets"`, "%s %s", caller.Email, p)
				assertNoneOf(t, body, []string{samOpen.ticket.Title, "Printer jam"}, caller.Email+" "+p)
			}
		}
	})

	t.Run("invalid values are 400 VALIDATION_ERROR with no tickets", func(t *testing.T) {
		cases := []struct {
			query string
			keys  []string
		}{
			{"status=bogus", []string{"status"}},
			{"priority=URGENT", []string{"priority"}},
			{"status=Open", []string{"status"}},
			{"status=Open&priority=URGENT", []string{"status", "priority"}},
			{"status=open&priority=Urgent", []string{"priority"}},
			{"status=bogus&search=Printer&sort_by=title", []string{"status"}},
		}
		for _, caller := range staff {
			for _, tc := range cases {
				p := "/tickets?" + tc.query
				status, body, _ := e.call(t, caller, http.MethodGet, p, nil)
				context := caller.Email + " " + p
				require.Equal(t, http.StatusBadRequest, status, "%s: %s", context, body)
				var envelope filterErrorEnvelope
				require.NoError(t, json.Unmarshal([]byte(body), &envelope), body)
				assert.False(t, envelope.Success, context)
				assert.Equal(t, "VALIDATION_ERROR", envelope.Error.Code, context)
				keys := []string{}
				for key := range envelope.Error.Details {
					keys = append(keys, key)
				}
				assert.ElementsMatch(t, tc.keys, keys, context)
				assert.NotContains(t, body, `"tickets"`, context)
				assert.True(t, len(envelope.Data) == 0 || string(envelope.Data) == "null", context)
				assertNoneOf(t, body, []string{"bogus", "URGENT", "Open", "Urgent", "Printer jam", samOpen.ticket.Title}, context)
			}
		}
	})

	t.Run("empty values are ignored", func(t *testing.T) {
		for _, caller := range staff {
			for _, extra := range []string{"", "&limit=3&page=2", "&search=Printer", "&sort_by=priority&sort_order=desc"} {
				plain := e.list(t, caller, "/tickets?x=1"+extra)
				for _, empty := range []string{"status=&priority=", "status=", "priority="} {
					p := "/tickets?" + empty + extra
					got := e.list(t, caller, p)
					context := caller.Email + " " + p
					assert.Equal(t, plain.ids, got.ids, context)
					assert.Equal(t, plain.total, got.total, context)
					assert.Equal(t, plain.meta.Total, got.meta.Total, context)
					assert.Equal(t, plain.meta.TotalPages, got.meta.TotalPages, context)
					assert.Equal(t, plain.meta.Page, got.meta.Page, context)
					assert.Equal(t, plain.meta.PerPage, got.meta.PerPage, context)
				}
			}
		}
		// The exact request the ticket screen sends.
		for _, caller := range []*models.User{e.admin, e.sales, e.sue} {
			want := expectedFilterIDs(all, caller, filterQuery{status: "resolved"}, "", "")
			e.expectPage(t, caller, "/tickets?page=1&limit=10&status=resolved&priority=&search=", want, int64(len(want)))
		}
	})
}
