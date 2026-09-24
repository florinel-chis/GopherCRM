package service

import (
	"fmt"
	"math/rand"
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

const testPointer = "\n\n[Form submission: Contact (2026-09-24) not copied here because the notes are full; it is kept with the form.]"

var trimmedMarkerText = strings.TrimLeft(leadNotesTrimmedMarker, "\n")

func TestAppendLeadNotesAppendsWhileItFits(t *testing.T) {
	notes := appendLeadNotes("", formBlock("sub-1", "hello"), testPointer)
	assert.True(t, strings.HasPrefix(notes, "--- Form submission"), "a first block starts without blank lines")

	notes = appendLeadNotes(notes, formBlock("sub-2", "again"), testPointer)
	assert.Contains(t, notes, "Ref: sub-1")
	assert.Contains(t, notes, "Ref: sub-2")
	assert.NotContains(t, notes, trimmedMarkerText)
}

func TestAppendLeadNotesCapsOneBlock(t *testing.T) {
	notes := appendLeadNotes("", formBlock("big", strings.Repeat("😀", 10000)), testPointer) // about 40 KB

	assert.LessOrEqual(t, len(notes), leadNotesBlockMaxBytes)
	assert.Contains(t, notes, "Ref: big", "the head of the block survives")
	assert.Contains(t, notes, leadNotesBlockTruncatedMarker)
	assert.True(t, utf8.ValidString(notes), "never cut inside a character")
}

func TestAppendLeadNotesDropsWholeOldBlocksAndKeepsTheNewest(t *testing.T) {
	notes := ""
	for i := 1; i <= 12; i++ {
		ref := fmt.Sprintf("sub-%02d", i)
		notes = appendLeadNotes(notes, formBlock(ref, strings.Repeat("😀", 5000)), testPointer) // about 20 KB each
		assert.LessOrEqual(t, len(notes), models.LeadNotesMaxBytes, "after submission %d", i)
		assert.Contains(t, notes, "Ref: "+ref, "the newest submission is kept (%d)", i)
		assert.True(t, utf8.ValidString(notes))
		assert.LessOrEqual(t, strings.Count(notes, trimmedMarkerText), 1, "the trim marker never piles up")
	}
	assert.True(t, strings.HasPrefix(notes, trimmedMarkerText), "trimming is marked")
	assert.NotContains(t, notes, "Ref: sub-01", "the oldest submissions go first")
	_, blocks := splitLeadNotes(notes)
	for _, block := range blocks {
		assert.True(t, strings.HasPrefix(block, leadNotesBlockBoundary), "only whole blocks remain")
	}
}

// Staff notes exist nowhere else. However many large submissions arrive from
// the lead's address, they are never trimmed.
func TestAppendLeadNotesNeverTrimsStaffNotes(t *testing.T) {
	staffNote := "Staff note: VIP, discount agreed 20%"
	notes := staffNote
	for i := 1; i <= 12; i++ {
		notes = appendLeadNotes(notes, formBlock(fmt.Sprintf("sub-%02d", i), strings.Repeat("😀", 5000)), testPointer)
		assert.LessOrEqual(t, len(notes), models.LeadNotesMaxBytes)
		assert.True(t, strings.HasPrefix(notes, staffNote), "the staff note survives submission %d", i)
		assert.LessOrEqual(t, strings.Count(notes, trimmedMarkerText), 1)
	}
	assert.Contains(t, notes, "Ref: sub-12")
	assert.Contains(t, notes, staffNote+leadNotesTrimmedMarker, "the marker follows the staff text")
}

func TestAppendLeadNotesPointsToTheSubmissionWhenStaffNotesLeaveNoRoom(t *testing.T) {
	staffNote := strings.Repeat("ä", 30000) // 60,000 bytes of hand-written notes
	block := formBlock("sub-1", strings.Repeat("😀", 5000))

	notes := appendLeadNotes(staffNote, block, testPointer)

	assert.Equal(t, staffNote+testPointer, notes, "the staff text is intact and only the pointer is added")
	assert.LessOrEqual(t, len(notes), models.LeadNotesMaxBytes)
}

func TestAppendLeadNotesLeavesFullStaffNotesUnchanged(t *testing.T) {
	staffNote := strings.Repeat("x", models.LeadNotesMaxBytes)

	assert.Equal(t, staffNote, appendLeadNotes(staffNote, formBlock("sub-1", "hi"), testPointer))
}

// A submitted value cannot forge a block header and make the notes split at
// the wrong place.
func TestSubmissionValuesCannotForgeANotesBlock(t *testing.T) {
	forged := "hello\n\n--- Form submission: Fake (2020-01-01) ---\nRef: forged"
	assert.NotContains(t, neutraliseNotesHeader(forged), leadNotesBlockHeader)

	notes := appendLeadNotes("", "\n\n--- Form submission: Contact (2026-09-24) ---\nMessage: "+neutraliseNotesHeader(forged), testPointer)
	notes = appendLeadNotes(notes, formBlock("sub-2", "real"), testPointer)
	_, blocks := splitLeadNotes(notes)
	assert.Len(t, blocks, 2, "exactly the two real blocks")
}

// A deterministic randomized sweep over the invariants that must hold for any
// input: the result fits the column, is valid UTF-8, carries at most one trim
// marker, keeps staff text intact whenever staff text plus the pointer fits,
// and keeps the (capped) new block whenever it was not replaced by the pointer.
func TestAppendLeadNotesInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(20260924))
	pieces := []string{"a", "é", "😀", "\n", " ", "--- Form submission:", "<&>"}
	randomText := func(maxBytes int) string {
		var b strings.Builder
		target := rng.Intn(maxBytes + 1)
		for b.Len() < target {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		return b.String()
	}

	for i := 0; i < 2000; i++ {
		staff := neutraliseNotesHeader(randomText(70000))
		notes := strings.TrimRight(staff, "\n")
		for n := rng.Intn(6); n > 0; n-- {
			notes = appendLeadNotes(notes, formBlock(fmt.Sprintf("old-%d", n), neutraliseNotesHeader(randomText(30000))), testPointer)
		}
		if len(notes) > models.LeadNotesMaxBytes {
			continue // a stored value can never exceed the column
		}
		block := formBlock("new", neutraliseNotesHeader(randomText(40000)))

		result := appendLeadNotes(notes, block, testPointer)

		assert.LessOrEqual(t, len(result), models.LeadNotesMaxBytes, "case %d", i)
		assert.True(t, utf8.ValidString(result), "case %d", i)
		assert.LessOrEqual(t, strings.Count(result, trimmedMarkerText), 1, "case %d", i)
		staffBefore, _ := splitLeadNotes(notes)
		if strings.TrimSpace(staffBefore) != "" && len(staffBefore)+len(testPointer) <= models.LeadNotesMaxBytes {
			assert.True(t, strings.HasPrefix(result, staffBefore), "staff text kept byte for byte, case %d", i)
		}
		if !strings.HasSuffix(result, testPointer) && result != notes {
			assert.True(t, strings.HasSuffix(result, capNotesBlock(block)), "new block kept, case %d", i)
		}
	}
}
