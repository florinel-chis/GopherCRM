package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLeadStatusIsValid pins the closed set GET /leads validates its status
// filter against (#426) and bulk status shares: exactly the five declared
// constants, matched case-sensitively.
func TestLeadStatusIsValid(t *testing.T) {
	valid := []LeadStatus{LeadStatusNew, LeadStatusContacted, LeadStatusQualified, LeadStatusUnqualified, LeadStatusConverted}
	assert.Equal(t, []LeadStatus{"new", "contacted", "qualified", "unqualified", "converted"}, valid)
	for _, s := range valid {
		assert.True(t, s.IsValid(), "%q", s)
	}
	for _, s := range []LeadStatus{"", "lost", "Lost", "Qualified", "QUALIFIED", "New", " new", "new ", "un-qualified",
		"won", "open", "new,contacted", "%"} {
		assert.False(t, s.IsValid(), "%q", s)
	}
}
