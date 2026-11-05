package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// These suites drive PUT /tickets/{id} (#269) through the real route, service and
// repository on SQLite and, when TEST_MYSQL_DSN is set, on a disposable MySQL
// schema. GORM's Save used to write the preloaded AssignedTo id back over the
// foreign key, so every stored assertion reads the tickets row without preloads.

func TestTicketReassignmentPersistsSQLite(t *testing.T) { runTicketReassignmentPersists(t, false) }
func TestTicketReassignmentPersistsMySQL(t *testing.T)  { runTicketReassignmentPersists(t, true) }

func TestTicketReassignmentResponseSQLite(t *testing.T) { runTicketReassignmentResponse(t, false) }
func TestTicketReassignmentResponseMySQL(t *testing.T)  { runTicketReassignmentResponse(t, true) }

func TestTicketReassignmentBySupportAssigneeSQLite(t *testing.T) {
	runTicketReassignmentBySupportAssignee(t, false)
}

func TestTicketReassignmentBySupportAssigneeMySQL(t *testing.T) {
	runTicketReassignmentBySupportAssignee(t, true)
}

func TestTicketUpdateKeepsAssigneeSQLite(t *testing.T) { runTicketUpdateKeepsAssignee(t, false) }
func TestTicketUpdateKeepsAssigneeMySQL(t *testing.T)  { runTicketUpdateKeepsAssignee(t, true) }

func TestTicketReassignmentRefusedWritesNothingSQLite(t *testing.T) {
	runTicketReassignmentRefused(t, false)
}

func TestTicketReassignmentRefusedWritesNothingMySQL(t *testing.T) {
	runTicketReassignmentRefused(t, true)
}

// --- helpers -------------------------------------------------------------------

// reassignedTicket is a ticket as the API answers it. AssignedTo stays a map so a
// test can tell which keys the user object really carries.
type reassignedTicket struct {
	Title        string                 `json:"title"`
	Description  string                 `json:"description"`
	Status       models.TicketStatus    `json:"status"`
	Priority     models.TicketPriority  `json:"priority"`
	Resolution   string                 `json:"resolution"`
	CustomerID   uint                   `json:"customer_id"`
	AssignedToID *uint                  `json:"assigned_to_id"`
	AssignedTo   map[string]interface{} `json:"assigned_to"`
}

func ticketPath(ticket *models.Ticket) string {
	return fmt.Sprintf("/tickets/%d", ticket.ID)
}

func decodeReassignedTicket(t *testing.T, envelope ticketEnvelope, body string) reassignedTicket {
	t.Helper()
	var got reassignedTicket
	require.NoError(t, json.Unmarshal(envelope.Data, &got), body)
	return got
}

func errorCodeOf(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed), body)
	return parsed.Error.Code
}

// assertNames holds both assignee fields of an answer to one user, and keeps the
// stored password out of it.
func assertNames(t *testing.T, got reassignedTicket, user *models.User, context string) {
	t.Helper()
	require.NotNil(t, got.AssignedToID, "%s: assigned_to_id", context)
	assert.Equal(t, user.ID, *got.AssignedToID, "%s: assigned_to_id", context)
	require.NotNil(t, got.AssignedTo, "%s: assigned_to", context)
	assert.Equal(t, float64(user.ID), got.AssignedTo["id"], "%s: assigned_to.id", context)
	assert.Equal(t, user.Email, got.AssignedTo["email"], "%s: assigned_to.email", context)
	assert.NotContains(t, got.AssignedTo, "password", context)
}

func (e *ticketVisibilityEnv) assertStoredAssignee(t *testing.T, ticket *models.Ticket, user *models.User, context string) {
	t.Helper()
	stored := e.storedTicket(t, ticket)
	require.NotNil(t, stored.AssignedToID, "%s: stored assignee", context)
	assert.Equal(t, user.ID, *stored.AssignedToID, "%s: stored assignee", context)
}

// --- AC: the new assignee is stored ------------------------------------------------

func runTicketReassignmentPersists(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)
	other := e.ticket(t, ticketSpec{customer: e.customerA, assignee: e.sue, title: "Untouched neighbour", minute: 0})

	for _, tc := range []struct {
		name   string
		target *models.User
		status models.TicketStatus
	}{
		{name: "a support target", target: e.sam, status: models.TicketStatusOpen},
		{name: "an admin target", target: e.admin, status: models.TicketStatusOpen},
		{name: "a closed ticket", target: e.sam, status: models.TicketStatusClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticket := e.ticket(t, ticketSpec{customer: e.customerB, assignee: e.sue, title: "Reassign " + tc.name, minute: 1})
			require.NoError(t, e.db.Model(&models.Ticket{}).Where("id = ?", ticket.ID).Update("status", tc.status).Error)

			status, body, _ := e.call(t, e.admin, http.MethodPut, ticketPath(ticket), map[string]interface{}{"assigned_to_id": tc.target.ID})
			require.Equal(t, http.StatusOK, status, body)

			e.assertStoredAssignee(t, ticket, tc.target, tc.name)
			stored := e.storedTicket(t, ticket)
			assert.Equal(t, tc.status, stored.Status, "the status is untouched")
			assert.Equal(t, ticket.Title, stored.Title)
			assert.Equal(t, e.customerB.ID, stored.CustomerID)

			status, body, envelope := e.call(t, e.admin, http.MethodGet, ticketPath(ticket), nil)
			require.Equal(t, http.StatusOK, status, body)
			assertNames(t, decodeReassignedTicket(t, envelope, body), tc.target, "the next GET")
		})
	}

	t.Run("a ticket can move again and return", func(t *testing.T) {
		ticket := e.ticket(t, ticketSpec{customer: e.customerA, assignee: e.sue, title: "Moves twice", minute: 2})
		for _, target := range []*models.User{e.sam, e.sid, e.sue} {
			status, body, _ := e.call(t, e.admin, http.MethodPut, ticketPath(ticket), map[string]interface{}{"assigned_to_id": target.ID})
			require.Equal(t, http.StatusOK, status, body)
			e.assertStoredAssignee(t, ticket, target, target.Email)
		}
	})

	e.assertStoredAssignee(t, other, e.sue, "another ticket of the old assignee")
	var former models.User
	require.NoError(t, e.db.First(&former, e.sue.ID).Error)
	assert.Equal(t, e.sue.Email, former.Email, "the old assignee's row is not touched")
}

// --- AC: the answer names the new assignee -----------------------------------------

func runTicketReassignmentResponse(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)

	for _, tc := range []struct {
		name   string
		holder *models.User
		target *models.User
	}{
		{name: "assigned ticket, support target", holder: e.sue, target: e.sam},
		{name: "assigned ticket, admin target", holder: e.sue, target: e.admin},
		{name: "unassigned ticket, support target", target: e.sam},
		{name: "unassigned ticket, admin target", target: e.admin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticket := e.ticket(t, ticketSpec{customer: e.customerA, assignee: tc.holder, title: tc.name, minute: 1})

			status, body, envelope := e.call(t, e.admin, http.MethodPut, ticketPath(ticket), map[string]interface{}{"assigned_to_id": tc.target.ID})
			require.Equal(t, http.StatusOK, status, body)

			assertNames(t, decodeReassignedTicket(t, envelope, body), tc.target, tc.name)
			assert.NotContains(t, body, `"password"`, "no password anywhere in the answer")
			e.assertStoredAssignee(t, ticket, tc.target, tc.name)
		})
	}
}

// --- AC: a support assignee hands a ticket over ------------------------------------

func runTicketReassignmentBySupportAssignee(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)
	ticket := e.ticket(t, ticketSpec{customer: e.customerA, assignee: e.sue, title: "Handed over", minute: 1})

	status, body, envelope := e.call(t, e.sue, http.MethodPut, ticketPath(ticket), map[string]interface{}{"assigned_to_id": e.sam.ID})
	require.Equal(t, http.StatusOK, status, body)
	assertNames(t, decodeReassignedTicket(t, envelope, body), e.sam, "the handover answer")
	e.assertStoredAssignee(t, ticket, e.sam, "after the handover")

	before := e.storedTicket(t, ticket)
	for _, payload := range []map[string]interface{}{
		{"title": "The former assignee's edit"},
		{"assigned_to_id": e.sue.ID},
	} {
		status, body, _ = e.call(t, e.sue, http.MethodPut, ticketPath(ticket), payload)
		assert.Equal(t, http.StatusForbidden, status, body)
		assert.Equal(t, "FORBIDDEN", errorCodeOf(t, body))
		after := e.storedTicket(t, ticket)
		assert.Equal(t, before.Title, after.Title, "the row is unchanged")
		assert.Equal(t, before.AssignedToID, after.AssignedToID, "the row is unchanged")
		assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "the row is unchanged")
	}

	status, body, envelope = e.call(t, e.sam, http.MethodPut, ticketPath(ticket), map[string]interface{}{"title": "The new assignee's edit"})
	require.Equal(t, http.StatusOK, status, body)
	assertNames(t, decodeReassignedTicket(t, envelope, body), e.sam, "the new assignee's answer")
	assert.Equal(t, "The new assignee's edit", e.storedTicket(t, ticket).Title)
	e.assertStoredAssignee(t, ticket, e.sam, "after the new assignee's edit")
}

// --- AC: other updates keep the assignee -------------------------------------------

func runTicketUpdateKeepsAssignee(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)

	shapes := []struct {
		name  string
		apply func(body map[string]interface{}, current *models.User)
	}{
		{name: "omitted", apply: func(map[string]interface{}, *models.User) {}},
		{name: "null", apply: func(body map[string]interface{}, _ *models.User) { body["assigned_to_id"] = nil }},
		{name: "the current assignee", apply: func(body map[string]interface{}, current *models.User) { body["assigned_to_id"] = current.ID }},
	}
	callers := []struct {
		name string
		user *models.User
	}{
		{name: "admin", user: e.admin},
		{name: "assigned support", user: e.sue},
	}

	for _, caller := range callers {
		for _, shape := range shapes {
			t.Run(caller.name+", "+shape.name, func(t *testing.T) {
				ticket := e.ticket(t, ticketSpec{customer: e.customerB, assignee: e.sue, title: "Original title", minute: 1})
				e.comment(t, ticket, e.admin, "A public reply", false)
				e.comment(t, ticket, e.sue, "An internal note", true)

				payload := map[string]interface{}{
					"title":       "New title " + shape.name,
					"description": "New description " + shape.name,
					"status":      string(models.TicketStatusResolved),
					"priority":    string(models.TicketPriorityUrgent),
					"resolution":  "New resolution " + shape.name,
				}
				shape.apply(payload, e.sue)

				status, body, envelope := e.call(t, caller.user, http.MethodPut, ticketPath(ticket), payload)
				require.Equal(t, http.StatusOK, status, body)

				got := decodeReassignedTicket(t, envelope, body)
				assertNames(t, got, e.sue, "the answer")
				assert.Equal(t, payload["title"], got.Title)

				stored := e.storedTicket(t, ticket)
				e.assertStoredAssignee(t, ticket, e.sue, "the row")
				assert.Equal(t, payload["title"], stored.Title)
				assert.Equal(t, payload["description"], stored.Description)
				assert.Equal(t, models.TicketStatusResolved, stored.Status)
				assert.Equal(t, models.TicketPriorityUrgent, stored.Priority)
				assert.Equal(t, payload["resolution"], stored.Resolution)
				assert.Equal(t, e.customerB.ID, stored.CustomerID)
				assert.Equal(t, int64(2), e.commentCount(t, ticket))
			})
		}
	}

	t.Run("an unassigned ticket stays unassigned without a value", func(t *testing.T) {
		ticket := e.ticket(t, ticketSpec{customer: e.customerA, title: "Nobody's", minute: 2})
		status, body, envelope := e.call(t, e.admin, http.MethodPut, ticketPath(ticket), map[string]interface{}{"assigned_to_id": nil, "title": "Still nobody's"})
		require.Equal(t, http.StatusOK, status, body)
		got := decodeReassignedTicket(t, envelope, body)
		assert.Nil(t, got.AssignedToID)
		assert.Nil(t, got.AssignedTo)
		assert.Nil(t, e.storedTicket(t, ticket).AssignedToID)
	})
}

// --- AC: a refusal writes nothing --------------------------------------------------

func runTicketReassignmentRefused(t *testing.T, useMySQL bool) {
	e := newTicketVisibilityEnv(t, useMySQL)
	const unknownUser = uint(999999)

	for _, tc := range []struct {
		name    string
		holder  *models.User
		caller  *models.User
		target  uint
		status  int
		errCode string
	}{
		{name: "admin, unknown user", holder: e.sue, caller: e.admin, target: unknownUser, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "admin, customer target", holder: e.sue, caller: e.admin, target: e.owner.ID, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "admin, sales target", holder: e.sue, caller: e.admin, target: e.sales.ID, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "assigned support, unknown user", holder: e.sue, caller: e.sue, target: unknownUser, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "assigned support, customer target", holder: e.sue, caller: e.sue, target: e.owner.ID, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "assigned support, sales target", holder: e.sue, caller: e.sue, target: e.sales.ID, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "admin on an unassigned ticket, sales target", caller: e.admin, target: e.sales.ID, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "admin on an unassigned ticket, unknown user", caller: e.admin, target: unknownUser, status: http.StatusBadRequest, errCode: "BAD_REQUEST"},
		{name: "sales caller", holder: e.sue, caller: e.sales, target: e.sam.ID, status: http.StatusForbidden, errCode: "FORBIDDEN"},
		{name: "customer caller", holder: e.sue, caller: e.owner, target: e.sam.ID, status: http.StatusForbidden, errCode: "FORBIDDEN"},
		{name: "support caller not assigned", holder: e.sue, caller: e.sam, target: e.sam.ID, status: http.StatusForbidden, errCode: "FORBIDDEN"},
		{name: "support caller on an unassigned ticket", caller: e.sam, target: e.sam.ID, status: http.StatusForbidden, errCode: "FORBIDDEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticket := e.ticket(t, ticketSpec{customer: e.customerA, assignee: tc.holder, title: "Stays as it was", minute: 1})
			before := e.storedTicket(t, ticket)

			status, body, _ := e.call(t, tc.caller, http.MethodPut, ticketPath(ticket),
				map[string]interface{}{"assigned_to_id": tc.target, "title": "Must not be stored"})
			assert.Equal(t, tc.status, status, body)
			assert.Equal(t, tc.errCode, errorCodeOf(t, body), body)

			after := e.storedTicket(t, ticket)
			assert.Equal(t, before.AssignedToID, after.AssignedToID, "the stored assignee")
			assert.Equal(t, "Stays as it was", after.Title, "the title sent beside it")
			assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "no write at all")
		})
	}
}
