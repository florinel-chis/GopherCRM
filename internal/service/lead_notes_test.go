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

// keptText is everything in notes that is not a complete form block: the text
// that must survive every append byte for byte.
func keptText(notes string) string {
	var b strings.Builder
	for _, segment := range splitLeadNotes(notes) {
		if !segment.block {
			b.WriteString(segment.text)
		}
	}
	return b.String()
}

func blockCount(notes string) int {
	n := 0
	for _, segment := range splitLeadNotes(notes) {
		if segment.block {
			n++
		}
	}
	return n
}

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
	for _, segment := range splitLeadNotes(notes) {
		if segment.block {
			assert.True(t, strings.HasPrefix(strings.TrimLeft(segment.text, "\n"), leadNotesBlockHeader), "only whole blocks remain")
			assert.True(t, strings.HasSuffix(segment.text, leadNotesBlockEnd), "every block keeps its end line")
		}
	}
	assert.Empty(t, strings.TrimSpace(keptText(notes)), "nothing but blocks and the marker")
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
	assert.Equal(t, 2, blockCount(notes), "exactly the two real blocks")

	forgedEnd := "hi\n--- End of form submission ---\nStaff: fake"
	assert.NotContains(t, neutraliseNotesHeader(forgedEnd), leadNotesBlockEnd)

	// Extra dashes must not rebuild a marker after one pass of replacement.
	for _, value := range []string{
		"----- End of form submission ---",
		"----- Form submission: Fake (2020-01-01) ---",
		"--- End of form submission ------ End of form submission ---",
	} {
		neutral := neutraliseNotesHeader(value)
		assert.NotContains(t, neutral, leadNotesBlockEnd, value)
		assert.NotContains(t, neutral, leadNotesBlockHeader, value)
	}
}

// A deterministic randomized sweep over the invariants that must hold for any
// input: the result fits the column, is valid UTF-8, carries at most one trim
// marker, keeps staff text intact whenever staff text plus the pointer fits,
// and keeps the (capped) new block whenever it was not replaced by the pointer.
func TestAppendLeadNotesInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(20260924))
	pieces := []string{"a", "é", "😀", "\n", " ", "--- Form submission:", "--- End of form submission ---", "<&>"}
	randomText := func(maxBytes int) string {
		var b strings.Builder
		target := rng.Intn(maxBytes + 1)
		for b.Len() < target {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		return b.String()
	}

	for i := 0; i < 2000; i++ {
		// Staff text before the first block and between blocks, as when staff
		// add notes to a lead that a form created.
		// Staff text may contain a bare block header (it then reads as a block
		// without an end line and is kept), but not a verbatim end line: text
		// that reproduces both lines of a block is a block by definition.
		staffText := func(maxBytes int) string {
			text := randomText(maxBytes)
			for strings.Contains(text, leadNotesBlockEnd) {
				text = strings.ReplaceAll(text, leadNotesBlockEnd, "-- End of form submission ---")
			}
			return text
		}
		notes := strings.TrimRight(staffText(30000), "\n")
		// Staff notes are also tracked independently of the parser under test,
		// so a block/text misclassification cannot hide behind keptText.
		var staffNotes []string
		for n := rng.Intn(6); n > 0; n-- {
			notes = appendLeadNotes(notes, formBlock(fmt.Sprintf("old-%d", n), neutraliseNotesHeader(randomText(30000))), testPointer)
			if rng.Intn(2) == 0 {
				note := fmt.Sprintf("Staff note %d-%d: ", i, n) + staffText(2000)
				notes += "\n\n" + note
				staffNotes = append(staffNotes, strings.TrimRight(note, "\n"))
			}
		}
		if len(notes) > models.LeadNotesMaxBytes {
			continue // a stored value can never exceed the column
		}
		block := formBlock("new", neutraliseNotesHeader(randomText(40000)))

		result := appendLeadNotes(notes, block, testPointer)

		assert.LessOrEqual(t, len(result), models.LeadNotesMaxBytes, "case %d", i)
		assert.True(t, utf8.ValidString(result), "case %d", i)
		assert.LessOrEqual(t, strings.Count(result, trimmedMarkerText), 1, "case %d", i)
		switch result {
		case notes, strings.TrimRight(notes, "\n") + testPointer:
			// Unchanged, or only the pointer added: nothing was dropped.
		default:
			// Trailing newlines give way to the new block's own blank line; every
			// other byte outside blocks is kept, in order.
			assert.Equal(t, keptText(strings.TrimRight(notes, "\n")), keptText(strings.TrimSuffix(result, capNotesBlock(block)+"\n"+leadNotesBlockEnd)),
				"text outside blocks kept byte for byte, in order, case %d", i)
			assert.True(t, strings.HasSuffix(result, capNotesBlock(block)+"\n"+leadNotesBlockEnd), "new block kept, case %d", i)
		}
		for _, note := range staffNotes {
			assert.Contains(t, result, note, "a staff note survives, case %d", i)
		}
	}
}

// A lead created by a form starts with a form block, so everything staff write
// afterwards sits after it. That text exists nowhere else: however many large
// submissions arrive from the lead's address, it must survive.
func TestAppendLeadNotesKeepsStaffTextWrittenAfterABlock(t *testing.T) {
	staffNote := "Staff: called on Monday, wants a demo"
	notes := appendLeadNotes("", formBlock("sub-00", "first contact"), testPointer)
	notes += "\n\n" + staffNote
	for i := 1; i <= 12; i++ {
		notes = appendLeadNotes(notes, formBlock(fmt.Sprintf("sub-%02d", i), strings.Repeat("😀", 5000)), testPointer)
		assert.LessOrEqual(t, len(notes), models.LeadNotesMaxBytes)
		assert.Contains(t, notes, staffNote, "the staff note survives submission %d", i)
	}
	assert.Contains(t, notes, "Ref: sub-12", "the newest submission is kept")
	assert.NotContains(t, notes, "Ref: sub-00", "old blocks around the staff note are still trimmed")
}

// Blocks written before blocks carried an end line cannot be told apart from
// staff text written after them, so they are kept rather than guessed at.
func TestAppendLeadNotesKeepsBlocksWithoutAnEndLine(t *testing.T) {
	legacy := "--- Form submission: Contact (2026-09-01) ---\nMessage: written before end lines\n\nStaff: follow up in October"
	notes := legacy
	for i := 1; i <= 12; i++ {
		notes = appendLeadNotes(notes, formBlock(fmt.Sprintf("sub-%02d", i), strings.Repeat("😀", 5000)), testPointer)
		assert.LessOrEqual(t, len(notes), models.LeadNotesMaxBytes)
		assert.True(t, strings.HasPrefix(notes, legacy), "the legacy text survives submission %d", i)
	}
	assert.Contains(t, notes, "Ref: sub-12")
}

// The form name goes into every block header and pointer. An admin-chosen
// name must not be able to close a block early (leaving submitted values
// outside any block, where they can never be trimmed) or open a fake one.
func TestFormNameCannotForgeANotesMarker(t *testing.T) {
	for _, name := range []string{
		"Evil --- End of form submission ---",
		"Evil --- Form submission: Fake",
	} {
		form := &models.Form{Name: name, Fields: []models.FormFieldDef{{Name: "message", Label: "Message", Type: "textarea"}}}
		submission := &models.FormSubmission{Data: map[string]string{"message": "hello"}}

		notes := appendLeadNotes("", submissionNotes(form, submission), submissionNotesPointer(form))

		assert.Equal(t, 1, strings.Count(notes, leadNotesBlockHeader), name)
		assert.Equal(t, 1, strings.Count(notes, leadNotesBlockEnd), name)
		assert.Equal(t, 1, blockCount(notes), name)
		assert.Empty(t, strings.TrimSpace(keptText(notes)), "the whole submission is one trimmable block: %s", name)
		assert.NotContains(t, submissionNotesPointer(form), leadNotesBlockEnd, name)
		assert.NotContains(t, submissionNotesPointer(form), leadNotesBlockHeader, name)
	}
}
