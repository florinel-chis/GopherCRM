package service

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
)

// formBlock mimics one form-submission block of lead notes.
func formBlock(ref string, body string) string {
	return "\n\n--- Form submission: Contact (2026-09-24) ---\nRef: " + ref + "\nMessage: " + body
}

func TestAppendLeadNotesAppendsWhileItFits(t *testing.T) {
	notes := appendLeadNotes("", formBlock("sub-1", "hello"))
	assert.True(t, strings.HasPrefix(notes, "--- Form submission"), "a first block starts without blank lines")

	notes = appendLeadNotes(notes, formBlock("sub-2", "again"))
	assert.Contains(t, notes, "Ref: sub-1")
	assert.Contains(t, notes, "Ref: sub-2")
	assert.NotContains(t, notes, leadNotesTrimmedMarker)
}

func TestAppendLeadNotesCapsOneBlock(t *testing.T) {
	notes := appendLeadNotes("", formBlock("big", strings.Repeat("😀", 10000))) // about 40 KB

	assert.LessOrEqual(t, len(notes), leadNotesBlockMaxBytes)
	assert.Contains(t, notes, "Ref: big", "the head of the block survives")
	assert.Contains(t, notes, leadNotesBlockTruncatedMarker)
	assert.True(t, utf8.ValidString(notes), "never cut inside a character")
}

func TestAppendLeadNotesStaysWithinTheColumnAndKeepsTheNewest(t *testing.T) {
	notes := ""
	for i := 1; i <= 12; i++ {
		ref := fmt.Sprintf("sub-%02d", i)
		notes = appendLeadNotes(notes, formBlock(ref, strings.Repeat("😀", 5000))) // about 20 KB each
		assert.LessOrEqual(t, len(notes), models.LeadNotesMaxBytes, "after submission %d", i)
		assert.Contains(t, notes, "Ref: "+ref, "the newest submission is kept (%d)", i)
		assert.True(t, utf8.ValidString(notes))
	}
	assert.True(t, strings.HasPrefix(notes, leadNotesTrimmedMarker), "trimming is marked")
	assert.NotContains(t, notes, "Ref: sub-01", "the oldest submissions go first")
	after := strings.TrimPrefix(notes, leadNotesTrimmedMarker)
	assert.True(t, strings.HasPrefix(after, "--- Form submission"), "cut at a block boundary")
}

func TestAppendLeadNotesTrimsHandWrittenNotesSafely(t *testing.T) {
	existing := strings.Repeat("ä", models.LeadNotesMaxBytes/2) // one long hand-written note, no block boundaries
	notes := appendLeadNotes(existing, formBlock("sub-1", "hello"))

	assert.LessOrEqual(t, len(notes), models.LeadNotesMaxBytes)
	assert.True(t, strings.HasPrefix(notes, leadNotesTrimmedMarker))
	assert.Contains(t, notes, "Ref: sub-1")
	assert.True(t, utf8.ValidString(notes), "the cut lands on a character boundary")
}
