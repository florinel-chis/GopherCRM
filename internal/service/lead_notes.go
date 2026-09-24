package service

import (
	"strings"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// A lead's notes hold two kinds of text: whatever staff wrote, and one block
// per form submission from the lead's address. The column is TEXT, so the
// notes are kept within models.LeadNotesMaxBytes instead of growing until MySQL
// rejects every further submission.
//
// Only form blocks are ever trimmed, and only whole blocks, oldest first: each
// submission is also stored in full with its form, so a trimmed block loses
// nothing. Staff text before the first block exists nowhere else and is never
// touched — otherwise anyone who knows a lead's address could erase it by
// submitting a public form a few times.
const (
	// leadNotesBlockMaxBytes caps what a single submission adds.
	leadNotesBlockMaxBytes = 16 << 10

	leadNotesBlockTruncatedMarker = "\n[Truncated here; the full submission is kept with the form.]"
	// leadNotesTrimmedMarker stands where older form blocks were dropped.
	leadNotesTrimmedMarker = "\n\n[Earlier form submissions trimmed to fit; each is kept in full with its form.]"

	// leadNotesBlockHeader opens every submission block. Submitted values are
	// neutralised so they cannot forge one (see neutraliseNotesHeader).
	leadNotesBlockHeader   = "--- Form submission:"
	leadNotesBlockBoundary = "\n\n" + leadNotesBlockHeader
)

// appendLeadNotes adds a submission block to a lead's notes and returns the
// result, which always fits models.LeadNotesMaxBytes. Older form blocks are
// dropped whole to make room. If the staff text alone leaves no room for the
// block, the short pointer is appended instead, and if not even that fits the
// notes are returned unchanged.
func appendLeadNotes(existing, block, pointer string) string {
	block = capNotesBlock(block)
	existing = strings.TrimRight(existing, "\n")
	if strings.TrimSpace(existing) == "" {
		return strings.TrimLeft(block, "\n")
	}
	if len(existing)+len(block) <= models.LeadNotesMaxBytes {
		return existing + block
	}

	staff, blocks := splitLeadNotes(existing)
	for len(blocks) > 0 && len(staff)+len(leadNotesTrimmedMarker)+len(strings.Join(blocks, ""))+len(block) > models.LeadNotesMaxBytes {
		blocks = blocks[1:]
	}
	if len(staff)+len(leadNotesTrimmedMarker)+len(strings.Join(blocks, ""))+len(block) <= models.LeadNotesMaxBytes {
		marker := leadNotesTrimmedMarker
		if staff == "" {
			marker = strings.TrimLeft(marker, "\n") // no staff text to separate it from
		}
		return staff + marker + strings.Join(blocks, "") + block
	}

	// The staff text itself is too long to leave room for the block.
	if len(existing)+len(pointer) <= models.LeadNotesMaxBytes {
		return existing + pointer
	}
	return existing
}

// splitLeadNotes separates the staff text (everything before the first form
// block, without a trim marker left by an earlier call) from the form blocks,
// each block keeping its leading boundary.
func splitLeadNotes(notes string) (string, []string) {
	var staff, rest string
	if strings.HasPrefix(notes, leadNotesBlockHeader) {
		rest = "\n\n" + notes
	} else if i := strings.Index(notes, leadNotesBlockBoundary); i >= 0 {
		staff, rest = notes[:i], notes[i:]
	} else {
		return notes, nil
	}
	staff = strings.TrimSuffix(staff, leadNotesTrimmedMarker)
	staff = strings.TrimPrefix(staff, strings.TrimLeft(leadNotesTrimmedMarker, "\n"))

	var blocks []string
	for rest != "" {
		next := strings.Index(rest[len(leadNotesBlockBoundary):], leadNotesBlockBoundary)
		if next < 0 {
			blocks = append(blocks, rest)
			break
		}
		cut := len(leadNotesBlockBoundary) + next
		blocks = append(blocks, rest[:cut])
		rest = rest[cut:]
	}
	return staff, blocks
}

// capNotesBlock cuts one submission block to leadNotesBlockMaxBytes, on a
// character boundary, and says so.
func capNotesBlock(block string) string {
	if len(block) <= leadNotesBlockMaxBytes {
		return block
	}
	cut := leadNotesBlockMaxBytes - len(leadNotesBlockTruncatedMarker)
	for cut > 0 && !utf8.RuneStart(block[cut]) {
		cut--
	}
	return block[:cut] + leadNotesBlockTruncatedMarker
}

// neutraliseNotesHeader keeps a submitted value from forging a block header,
// which would make the notes split at the wrong place.
func neutraliseNotesHeader(line string) string {
	return strings.ReplaceAll(line, leadNotesBlockHeader, "-- Form submission:")
}
