package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// #218: GET /tickets takes optional status and priority filters. These cases
// run the real handler over a mock service, so every service call, and every
// argument it receives, is pinned.

type ticketFilterResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Tickets []models.Ticket `json:"tickets"`
		Total   int64           `json:"total"`
	} `json:"data"`
	Meta  utils.APIMeta `json:"meta"`
	Error struct {
		Code    string                 `json:"code"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

func newTicketFilterRouter(t *testing.T, svc *mocks.TicketService, role models.UserRole, userID uint) *gin.Engine {
	t.Helper()
	// internal/handler has no TestMain: List logs through utils.Logger.
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	h := NewTicketHandler(svc, new(mocks.CustomerService))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	router.GET("/tickets", h.List)
	return router
}

func getTicketFilter(t *testing.T, router *gin.Engine, path string) (*httptest.ResponseRecorder, ticketFilterResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body ticketFilterResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return w, body
}

func newStrictTicketService(t *testing.T) *mocks.TicketService {
	svc := new(mocks.TicketService)
	svc.Test(t)
	return svc
}

func assertNoTicketServiceCall(t *testing.T, svc *mocks.TicketService) {
	t.Helper()
	assert.Empty(t, svc.Calls, "no service method may be called")
}

func TestTicketListFilterHandler(t *testing.T) {
	staff := []struct {
		role models.UserRole
		id   uint
	}{{models.RoleAdmin, 1}, {models.RoleSales, 2}, {models.RoleSupport, 3}}

	t.Run("customers get 403 with no service call, filters present or not", func(t *testing.T) {
		for _, path := range []string{
			"/tickets",
			"/tickets?status=open",
			"/tickets?priority=urgent",
			"/tickets?status=resolved&priority=low&search=x",
			"/tickets?status=bogus",
			"/tickets?priority=URGENT",
		} {
			svc := newStrictTicketService(t)
			w, body := getTicketFilter(t, newTicketFilterRouter(t, svc, models.RoleCustomer, 9), path)
			assert.Equal(t, http.StatusForbidden, w.Code, path)
			assert.Equal(t, utils.ErrCodeForbidden, body.Error.Code, path)
			assertNoTicketServiceCall(t, svc)
		}
	})

	t.Run("invalid values are 400 VALIDATION_ERROR keyed by parameter, value not echoed", func(t *testing.T) {
		cases := []struct {
			query string
			keys  []string
			raw   []string
		}{
			{"status=bogus", []string{"status"}, []string{"bogus"}},
			{"status=Open", []string{"status"}, []string{"Open"}},
			{"status=OPEN", []string{"status"}, []string{"OPEN"}},
			{"status=in%20progress", []string{"status"}, []string{"in progress"}},
			{"status=open%20", []string{"status"}, []string{"open "}},
			{"priority=URGENT", []string{"priority"}, []string{"URGENT"}},
			{"priority=Low", []string{"priority"}, []string{"Low"}},
			{"priority=critical", []string{"priority"}, []string{"critical"}},
			{"status=zzbadstatus&priority=zzbadprio", []string{"status", "priority"}, []string{"zzbadstatus", "zzbadprio"}},
			{"status=zzbadstatus&priority=high", []string{"status"}, []string{"zzbadstatus"}},
			{"status=open&priority=zzbadprio", []string{"priority"}, []string{"zzbadprio"}},
			{"status=zzbadstatus&priority=", []string{"status"}, []string{"zzbadstatus"}},
			// The first value of a repeated parameter decides.
			{"status=zzbadstatus&status=open", []string{"status"}, []string{"zzbadstatus"}},
		}
		for _, who := range staff {
			for _, tc := range cases {
				svc := newStrictTicketService(t)
				path := "/tickets?" + tc.query + "&search=needle"
				w, body := getTicketFilter(t, newTicketFilterRouter(t, svc, who.role, who.id), path)
				context := string(who.role) + " " + path
				require.Equal(t, http.StatusBadRequest, w.Code, context)
				assert.False(t, body.Success, context)
				assert.Equal(t, utils.ErrCodeValidation, body.Error.Code, context)
				keys := make([]string, 0, len(body.Error.Details))
				for key := range body.Error.Details {
					keys = append(keys, key)
				}
				assert.ElementsMatch(t, tc.keys, keys, context)
				for _, raw := range tc.raw {
					assert.NotContains(t, w.Body.String(), raw, "%s: the value is not echoed", context)
				}
				for _, detail := range body.Error.Details {
					text, _ := detail.(string)
					for _, raw := range tc.raw {
						assert.NotContains(t, text, raw, context)
					}
				}
				assert.Empty(t, body.Data.Tickets, context)
				assertNoTicketServiceCall(t, svc)
			}
		}
	})

	t.Run("without a filter the existing branches run with unchanged arguments", func(t *testing.T) {
		rows := []models.Ticket{{Title: "Plain"}}
		for _, suffix := range []string{"", "&status=&priority=", "&status=", "&priority="} {
			// admin and sales: List, ListSorted, Search
			for _, who := range staff[:2] {
				svc := newStrictTicketService(t)
				svc.On("List", 0, 20).Return(rows, int64(1), nil).Once()
				w, body := getTicketFilter(t, newTicketFilterRouter(t, svc, who.role, who.id), "/tickets?x=1"+suffix)
				require.Equal(t, http.StatusOK, w.Code, suffix)
				assert.Equal(t, int64(1), body.Data.Total)
				svc.AssertExpectations(t)

				svc = newStrictTicketService(t)
				svc.On("ListSorted", 10, 5, "priority", "desc").Return(rows, int64(11), nil).Once()
				w, _ = getTicketFilter(t, newTicketFilterRouter(t, svc, who.role, who.id),
					"/tickets?offset=10&limit=5&sort_by=priority&sort_order=desc"+suffix)
				require.Equal(t, http.StatusOK, w.Code, suffix)
				svc.AssertExpectations(t)

				svc = newStrictTicketService(t)
				svc.On("Search", "printer", 5, 5, "", "asc").Return(rows, int64(6), nil).Once()
				w, _ = getTicketFilter(t, newTicketFilterRouter(t, svc, who.role, who.id),
					"/tickets?page=2&limit=5&search=printer&sort_by=nope"+suffix)
				require.Equal(t, http.StatusOK, w.Code, suffix)
				svc.AssertExpectations(t)
			}
			// support: ListAssigned
			svc := newStrictTicketService(t)
			svc.On("ListAssigned", uint(3), "printer", 10, 5, "title", "asc").Return(rows, int64(11), nil).Once()
			w, _ := getTicketFilter(t, newTicketFilterRouter(t, svc, models.RoleSupport, 3),
				"/tickets?offset=10&limit=5&search=printer&sort_by=title&sort_order=sideways"+suffix)
			require.Equal(t, http.StatusOK, w.Code, suffix)
			svc.AssertExpectations(t)
		}
	})

	t.Run("a filter calls ListFiltered with the parsed values", func(t *testing.T) {
		cases := []struct {
			query                    string
			status, priority, search string
			offset, limit            int
			sortBy, sortOrder        string
		}{
			{"status=open", "open", "", "", 0, 20, "", "asc"},
			{"priority=urgent", "", "urgent", "", 0, 20, "", "asc"},
			{"status=in_progress&priority=high", "in_progress", "high", "", 0, 20, "", "asc"},
			{"status=resolved&priority=", "resolved", "", "", 0, 20, "", "asc"},
			{"status=&priority=low", "", "low", "", 0, 20, "", "asc"},
			{"status=closed&search=printer&sort_by=title&sort_order=desc&page=3&limit=4", "closed", "", "printer", 8, 4, "title", "desc"},
			{"priority=medium&offset=7&limit=500&sort_by=assigned_to_id&sort_order=x", "", "medium", "", 7, 100, "", "asc"},
			// The first value of a repeated parameter is used.
			{"status=open&status=closed&priority=low&priority=URGENT", "open", "low", "", 0, 20, "", "asc"},
		}
		for _, who := range staff {
			for _, tc := range cases {
				want := repository.TicketListFilter{Search: tc.search, Status: tc.status, Priority: tc.priority}
				if who.role == models.RoleSupport {
					want.AssigneeID = uintPtr(who.id)
				}
				rows := []models.Ticket{{Title: "Filtered"}}
				svc := newStrictTicketService(t)
				svc.On("ListFiltered", want, tc.offset, tc.limit, tc.sortBy, tc.sortOrder).Return(rows, int64(9), nil).Once()
				path := "/tickets?" + tc.query
				w, body := getTicketFilter(t, newTicketFilterRouter(t, svc, who.role, who.id), path)
				context := string(who.role) + " " + path
				require.Equal(t, http.StatusOK, w.Code, "%s: %s", context, w.Body.String())
				svc.AssertExpectations(t)
				svc.AssertNotCalled(t, "List", mock.Anything, mock.Anything)
				assert.Len(t, svc.Calls, 1, context)
				assert.Equal(t, int64(9), body.Data.Total, context)
				assert.Equal(t, int64(9), body.Meta.Total, context)
				assert.Equal(t, (int64(9)+int64(tc.limit)-1)/int64(tc.limit), body.Meta.TotalPages, context)
				assert.Equal(t, tc.limit, body.Meta.PerPage, context)
				assert.Equal(t, tc.offset/tc.limit+1, body.Meta.Page, context)
				require.Len(t, body.Data.Tickets, 1, context)
				assert.Equal(t, "Filtered", body.Data.Tickets[0].Title, context)
			}
		}
	})

	t.Run("support cannot widen its scope through parameters", func(t *testing.T) {
		want := repository.TicketListFilter{Status: "open", AssigneeID: uintPtr(3)}
		svc := newStrictTicketService(t)
		svc.On("ListFiltered", want, 0, 20, "", "asc").Return([]models.Ticket{}, int64(0), nil).Once()
		w, body := getTicketFilter(t, newTicketFilterRouter(t, svc, models.RoleSupport, 3),
			"/tickets?status=open&assigned_to_id=4&assignee_id=4&user_id=4")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, int64(0), body.Meta.TotalPages)
		svc.AssertExpectations(t)
	})

	t.Run("meta reflects the filtered total", func(t *testing.T) {
		svc := newStrictTicketService(t)
		filter := repository.TicketListFilter{Priority: "high"}
		svc.On("ListFiltered", filter, 6, 3, "", "asc").Return([]models.Ticket{{Title: "a"}}, int64(7), nil).Once()
		w, body := getTicketFilter(t, newTicketFilterRouter(t, svc, models.RoleAdmin, 1), "/tickets?priority=high&page=3&limit=3")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, int64(7), body.Data.Total)
		assert.Equal(t, int64(7), body.Meta.Total)
		assert.Equal(t, int64(3), body.Meta.TotalPages)
		assert.Equal(t, 3, body.Meta.Page)
		assert.Equal(t, 3, body.Meta.PerPage)
	})

	t.Run("a service error is 500", func(t *testing.T) {
		svc := newStrictTicketService(t)
		svc.On("ListFiltered", repository.TicketListFilter{Status: "open"}, 0, 20, "", "asc").
			Return(nil, int64(0), assert.AnError).Once()
		w, body := getTicketFilter(t, newTicketFilterRouter(t, svc, models.RoleSales, 2), "/tickets?status=open")
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Equal(t, utils.ErrCodeInternal, body.Error.Code)
		svc.AssertExpectations(t)
	})
}
