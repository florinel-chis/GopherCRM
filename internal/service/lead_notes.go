package service

import (
	"strings"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// Lead notes are a readable running log: every form submission from an
// address the CRM already knows adds a block to that lead's notes. The column
// is TEXT, so the log is kept within models.LeadNotesMaxBytes instead of
// growing until MySQL rejects every further submission. Nothing is lost by
// trimming it: each submission is stored in full with its form.
const (
	// leadNotesBlockMaxBytes caps what a single submission adds.
	leadNotesBlockMaxBytes = 16 << 10

	leadNotesBlockTruncatedMarker = "\n[Truncated here; the full submission is kept with the form.]"
	leadNotesTrimmedMarker        = "[Earlier notes trimmed to fit.]\n\n"

	// leadNotesBlockBoundary starts every submission block after the first.
	leadNotesBlockBoundary = "\n\n--- Form submission:"
)

// appendLeadNotes adds a submission block to a lead's notes and returns the
// result, which always fits models.LeadNotesMaxBytes. When it would not, the
// oldest content goes first — at a block boundary where there is one — and the
// newest block is always kept.
func appendLeadNotes(existing, block string) string {
	block = capNotesBlock(block)
	existing = strings.TrimRight(existing, "\n")
	if strings.TrimSpace(existing) == "" {
		return strings.TrimLeft(block, "\n")
	}
	if len(existing)+len(block) <= models.LeadNotesMaxBytes {
		return existing + block
	}

	room := models.LeadNotesMaxBytes - len(leadNotesTrimmedMarker) - len(block)
	kept := notesTailWithin(existing, room)
	if kept == "" {
		return leadNotesTrimmedMarker + strings.TrimLeft(block, "\n")
	}
	return leadNotesTrimmedMarker + kept + block
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

// notesTailWithin returns the newest part of notes that fits in room bytes,
// starting at the first block boundary inside it, or on a character boundary
// when the kept text holds no block boundary (hand-written notes).
func notesTailWithin(notes string, room int) string {
	if room <= 0 {
		return ""
	}
	if len(notes) <= room {
		return notes
	}
	start := len(notes) - room
	if i := strings.Index(notes[start:], leadNotesBlockBoundary); i >= 0 {
		return notes[start+i+len("\n\n"):]
	}
	for start < len(notes) && !utf8.RuneStart(notes[start]) {
		start++
	}
	return notes[start:]
}
