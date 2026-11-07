package models

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #278: overdue is due_at strictly before now, compared in UTC, on an open or
// in-progress ticket.
func TestTicketOverdue(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	at := func(value time.Time) *time.Time { return &value }
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	berlin := time.FixedZone("CET", 60*60)
	newYork := time.FixedZone("EST", -5*60*60)

	for _, tc := range []struct {
		name   string
		dueAt  *time.Time
		status TicketStatus
		now    time.Time
		want   bool
	}{
		{name: "past due, open", dueAt: at(past), status: TicketStatusOpen, now: now, want: true},
		{name: "past due, in progress", dueAt: at(past), status: TicketStatusInProgress, now: now, want: true},
		{name: "one millisecond past, open", dueAt: at(now.Add(-time.Millisecond)), status: TicketStatusOpen, now: now, want: true},
		{name: "due exactly now", dueAt: at(now), status: TicketStatusOpen, now: now, want: false},
		{name: "future due", dueAt: at(future), status: TicketStatusOpen, now: now, want: false},
		{name: "future due, in progress", dueAt: at(future), status: TicketStatusInProgress, now: now, want: false},
		{name: "no due date", dueAt: nil, status: TicketStatusOpen, now: now, want: false},
		{name: "no due date, in progress", dueAt: nil, status: TicketStatusInProgress, now: now, want: false},
		{name: "past due, resolved", dueAt: at(past), status: TicketStatusResolved, now: now, want: false},
		{name: "past due, closed", dueAt: at(past), status: TicketStatusClosed, now: now, want: false},
		// The same instants carried in other locations give the same answers.
		{name: "past due in CET, now in UTC", dueAt: at(past.In(berlin)), status: TicketStatusOpen, now: now, want: true},
		{name: "past due in UTC, now in EST", dueAt: at(past), status: TicketStatusOpen, now: now.In(newYork), want: true},
		{name: "due now in CET, now in EST", dueAt: at(now.In(berlin)), status: TicketStatusOpen, now: now.In(newYork), want: false},
		{name: "future due in EST, now in CET", dueAt: at(future.In(newYork)), status: TicketStatusOpen, now: now.In(berlin), want: false},
		{name: "past due in EST, resolved, now in CET", dueAt: at(past.In(newYork)), status: TicketStatusResolved, now: now.In(berlin), want: false},
		// A wall-clock reading that would compare the other way without UTC:
		// 12:30 CET is 11:30 UTC, before 12:00 UTC.
		{name: "local wall clock later, instant earlier", dueAt: at(time.Date(2026, 11, 8, 12, 30, 0, 0, berlin)), status: TicketStatusOpen, now: now, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticket := Ticket{Status: tc.status, DueAt: tc.dueAt}
			assert.Equal(t, tc.want, ticket.IsOverdue(tc.now))
			assert.Equal(t, tc.want, (&ticket).IsOverdue(tc.now), "pointer receiver")
		})
	}
}

var ticketJSONTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

func decodeTicketKeys(t *testing.T, encoded []byte) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &body), string(encoded))
	return body
}

// legacyTicketJSON is the ticket's JSON shape before #278, field for field.
type legacyTicketJSON struct {
	BaseModel
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Status       TicketStatus    `json:"status"`
	Priority     TicketPriority  `json:"priority"`
	CustomerID   uint            `json:"customer_id"`
	Customer     Customer        `json:"customer,omitempty"`
	AssignedToID *uint           `json:"assigned_to_id,omitempty"`
	AssignedTo   *User           `json:"assigned_to,omitempty"`
	Resolution   string          `json:"resolution"`
	Comments     []TicketComment `json:"comments,omitempty"`
}

func legacyOf(ticket Ticket) legacyTicketJSON {
	return legacyTicketJSON{BaseModel: ticket.BaseModel, Title: ticket.Title, Description: ticket.Description,
		Status: ticket.Status, Priority: ticket.Priority, CustomerID: ticket.CustomerID, Customer: ticket.Customer,
		AssignedToID: ticket.AssignedToID, AssignedTo: ticket.AssignedTo, Resolution: ticket.Resolution, Comments: ticket.Comments}
}

// #278: due_at and first_responded_at are RFC 3339 strings or null, never
// omitted; overdue is a boolean from the wall clock; every older key is
// unchanged. The value, the pointer and a slice element all go through it.
func TestTicketJSONSLAFields(t *testing.T) {
	assignee := uint(5)
	due2000 := time.Date(2000, 1, 2, 3, 4, 5, 678_900_000, time.FixedZone("CET", 60*60))
	due2999 := time.Date(2999, 12, 31, 23, 59, 59, 0, time.UTC)
	responded := time.Date(2026, 11, 8, 9, 30, 0, 123_456_789, time.UTC)
	base := Ticket{
		BaseModel:    BaseModel{ID: 7, CreatedAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)},
		Title:        "Printer offline",
		Description:  "Since this morning",
		Status:       TicketStatusOpen,
		Priority:     TicketPriorityHigh,
		CustomerID:   3,
		AssignedToID: &assignee,
		Resolution:   "",
	}

	marshalAll := func(t *testing.T, ticket Ticket) map[string]map[string]json.RawMessage {
		t.Helper()
		out := map[string]map[string]json.RawMessage{}
		encoded, err := json.Marshal(ticket)
		require.NoError(t, err)
		out["value"] = decodeTicketKeys(t, encoded)
		encoded, err = json.Marshal(&ticket)
		require.NoError(t, err)
		out["pointer"] = decodeTicketKeys(t, encoded)
		encoded, err = json.Marshal([]Ticket{ticket})
		require.NoError(t, err)
		var list []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &list))
		require.Len(t, list, 1)
		out["slice"] = list[0]
		return out
	}

	t.Run("unset timestamps are null, never omitted", func(t *testing.T) {
		for name, body := range marshalAll(t, base) {
			for _, key := range []string{"due_at", "first_responded_at"} {
				raw, ok := body[key]
				require.True(t, ok, "%s: %s is always present", name, key)
				assert.JSONEq(t, "null", string(raw), "%s: %s", name, key)
			}
			assert.JSONEq(t, "false", string(body["overdue"]), "%s: no due date is never overdue", name)
		}
	})

	t.Run("set timestamps are UTC RFC 3339 strings with milliseconds", func(t *testing.T) {
		ticket := base
		ticket.DueAt = &due2999
		ticket.FirstRespondedAt = &responded
		for name, body := range marshalAll(t, ticket) {
			var dueAt, respondedAt string
			require.NoError(t, json.Unmarshal(body["due_at"], &dueAt), name)
			require.NoError(t, json.Unmarshal(body["first_responded_at"], &respondedAt), name)
			assert.Equal(t, "2999-12-31T23:59:59.000Z", dueAt, name)
			assert.Equal(t, "2026-11-08T09:30:00.123Z", respondedAt, name)
			for _, value := range []string{dueAt, respondedAt} {
				assert.Regexp(t, ticketJSONTimestamp, value, name)
				_, err := time.Parse(time.RFC3339, value)
				assert.NoError(t, err, "%s: %s is RFC 3339", name, value)
			}
			assert.JSONEq(t, "false", string(body["overdue"]), "%s: due in 2999", name)
		}

		ticket.DueAt = &due2000
		for name, body := range marshalAll(t, ticket) {
			var dueAt string
			require.NoError(t, json.Unmarshal(body["due_at"], &dueAt), name)
			assert.Equal(t, "2000-01-02T02:04:05.678Z", dueAt, "%s: converted to UTC", name)
			assert.JSONEq(t, "true", string(body["overdue"]), "%s: an open ticket due in 2000", name)
		}
	})

	t.Run("overdue follows the status", func(t *testing.T) {
		for status, want := range map[TicketStatus]string{
			TicketStatusOpen:       "true",
			TicketStatusInProgress: "true",
			TicketStatusResolved:   "false",
			TicketStatusClosed:     "false",
		} {
			ticket := base
			ticket.Status = status
			ticket.DueAt = &due2000
			for name, body := range marshalAll(t, ticket) {
				assert.JSONEq(t, want, string(body["overdue"]), "%s: %s", name, status)
			}
		}
	})

	t.Run("every existing key keeps its name, value and omission", func(t *testing.T) {
		withAssignee := base
		withAssignee.AssignedTo = &User{BaseModel: BaseModel{ID: assignee}, Email: "agent@example.com", Role: RoleSupport}
		withAssignee.Customer = Customer{BaseModel: BaseModel{ID: 3}, FirstName: "C", Email: "c@example.com"}
		withAssignee.Comments = []TicketComment{{BaseModel: BaseModel{ID: 1}, TicketID: 7, AuthorUserID: assignee, Body: "Hello"}}
		withAssignee.Resolution = "Rebooted"
		unassigned := base
		unassigned.AssignedToID = nil
		withSLA := withAssignee
		withSLA.DueAt = &due2000
		withSLA.FirstRespondedAt = &responded

		for label, ticket := range map[string]Ticket{"assigned": withAssignee, "unassigned": unassigned, "with SLA": withSLA} {
			encoded, err := json.Marshal(legacyOf(ticket))
			require.NoError(t, err)
			legacy := decodeTicketKeys(t, encoded)
			for name, body := range marshalAll(t, ticket) {
				wantKeys := []string{"due_at", "first_responded_at", "overdue"}
				for key, value := range legacy {
					wantKeys = append(wantKeys, key)
					if assert.Contains(t, body, key, "%s %s", label, name) {
						assert.JSONEq(t, string(value), string(body[key]), "%s %s: %s", label, name, key)
					}
				}
				gotKeys := []string{}
				for key := range body {
					gotKeys = append(gotKeys, key)
				}
				assert.ElementsMatch(t, wantKeys, gotKeys, "%s %s: exactly the old keys plus the three new ones", label, name)
			}
		}
		// Omission behaviour of the old optional keys is kept.
		encoded, err := json.Marshal(unassigned)
		require.NoError(t, err)
		body := decodeTicketKeys(t, encoded)
		assert.NotContains(t, body, "assigned_to_id")
		assert.NotContains(t, body, "assigned_to")
		assert.NotContains(t, body, "comments")
		assert.Contains(t, body, "customer")
		assert.Contains(t, body, "deleted_at")
	})

	t.Run("a body round-trips into the struct", func(t *testing.T) {
		ticket := base
		ticket.DueAt = &due2999
		encoded, err := json.Marshal(ticket)
		require.NoError(t, err)
		var decoded Ticket
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.NotNil(t, decoded.DueAt)
		assert.True(t, due2999.Equal(*decoded.DueAt))
		assert.Nil(t, decoded.FirstRespondedAt)
	})
}

// TestTicketSLAFieldsSQLiteUpgrade is the SQLite half of #278: SQLite installs
// never run the SQL migration, so MigrateDatabase on a file written before
// this change adds the two nullable columns, keeps every ticket value, reads
// NULL for both on existing rows, and a second run changes nothing.
func TestTicketSLAFieldsSQLiteUpgrade(t *testing.T) {
	// The v1.2.0 fixture holds one ticket (open, medium, assigned, on customer
	// 1); assertUpgradedV120 checks that no row is lost or gained.
	openV120Fixture(t)
	for _, column := range []string{"due_at", "first_responded_at"} {
		require.False(t, DB.Migrator().HasColumn("tickets", column), "the fixture predates %s", column)
	}

	type ticketRow struct {
		ID           uint
		CreatedAt    string
		UpdatedAt    string
		Title        string
		Description  string
		Status       string
		Priority     string
		CustomerID   uint
		AssignedToID *uint
		Resolution   *string
	}
	readRows := func() []ticketRow {
		var rows []ticketRow
		require.NoError(t, DB.Raw(`SELECT id, CAST(created_at AS TEXT) AS created_at, CAST(updated_at AS TEXT) AS updated_at,
			title, description, status, priority, customer_id, assigned_to_id, resolution FROM tickets ORDER BY id`).Scan(&rows).Error)
		return rows
	}
	before := readRows()
	require.Len(t, before, 1)
	assert.Equal(t, "Ticket on customer 1", before[0].Title)

	require.NoError(t, MigrateDatabase())
	assertUpgradedV120(t)

	for _, column := range []string{"due_at", "first_responded_at"} {
		assert.True(t, DB.Migrator().HasColumn("tickets", column), "tickets.%s", column)
		var notNull int
		require.NoError(t, DB.Raw("SELECT \"notnull\" FROM pragma_table_info('tickets') WHERE name = ?", column).Scan(&notNull).Error)
		assert.Equal(t, 0, notNull, "tickets.%s is nullable", column)
	}
	assert.NotContains(t, tableSQL(t, "tickets"), "`overdue`", "overdue is never stored")

	assert.Equal(t, before, readRows(), "every existing value is kept")
	var unset int64
	require.NoError(t, DB.Raw("SELECT COUNT(*) FROM tickets WHERE due_at IS NULL AND first_responded_at IS NULL").Scan(&unset).Error)
	assert.Equal(t, int64(1), unset, "the existing ticket reads NULL for both new columns")

	var stored []Ticket
	require.NoError(t, DB.Order("id").Find(&stored).Error)
	require.Len(t, stored, 1)
	for _, ticket := range stored {
		assert.Nil(t, ticket.DueAt)
		assert.Nil(t, ticket.FirstRespondedAt)
		assert.False(t, ticket.IsOverdue(time.Now()))
	}

	// A second run changes nothing: the schema and the rows stay as they are.
	schema := schemaSnapshot(t)
	rows := readRows()
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "the second MigrateDatabase leaves the schema alone")
	assert.Equal(t, rows, readRows(), "the second MigrateDatabase leaves the rows alone")

	// The new columns take writes.
	due := time.Date(2026, 12, 1, 10, 0, 0, 123_000_000, time.UTC)
	require.NoError(t, DB.Model(&Ticket{}).Where("id = ?", stored[0].ID).Update("due_at", due).Error)
	var reread Ticket
	require.NoError(t, DB.First(&reread, stored[0].ID).Error)
	require.NotNil(t, reread.DueAt)
	assert.True(t, due.Equal(*reread.DueAt))
}
