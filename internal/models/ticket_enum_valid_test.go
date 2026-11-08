package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTicketEnumIsValid pins the closed sets GET /tickets validates its status
// and priority filters against (#218): exactly the declared constants, matched
// case-sensitively.
func TestTicketEnumIsValid(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		for _, s := range []TicketStatus{TicketStatusOpen, TicketStatusInProgress, TicketStatusResolved, TicketStatusClosed} {
			assert.True(t, s.IsValid(), "%q", s)
		}
		assert.Equal(t, []TicketStatus{"open", "in_progress", "resolved", "closed"},
			[]TicketStatus{TicketStatusOpen, TicketStatusInProgress, TicketStatusResolved, TicketStatusClosed})
		for _, s := range []TicketStatus{"", "Open", "OPEN", "In_Progress", "in progress", "inprogress", "in-progress",
			" open", "open ", "Resolved", "CLOSED", "bogus", "archived", "low", "urgent", "open,closed", "%"} {
			assert.False(t, s.IsValid(), "%q", s)
		}
	})

	t.Run("priority", func(t *testing.T) {
		for _, p := range []TicketPriority{TicketPriorityLow, TicketPriorityMedium, TicketPriorityHigh, TicketPriorityUrgent} {
			assert.True(t, p.IsValid(), "%q", p)
		}
		assert.Equal(t, []TicketPriority{"low", "medium", "high", "urgent"},
			[]TicketPriority{TicketPriorityLow, TicketPriorityMedium, TicketPriorityHigh, TicketPriorityUrgent})
		for _, p := range []TicketPriority{"", "URGENT", "Urgent", "Low", "MEDIUM", "High", " high", "high ", "critical",
			"bogus", "open", "closed", "low,high", "%"} {
			assert.False(t, p.IsValid(), "%q", p)
		}
	})
}
