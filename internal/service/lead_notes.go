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
// Only complete form blocks are ever trimmed, whole and oldest first: each
// submission is also stored in full with its form, so a trimmed block loses
// nothing. A block is exactly the span from its header line to its end line.
// Submitted values cannot forge either line (see neutraliseNotesHeader); text
// that reproduces both verbatim is a block by definition.
// Everything else is kept byte for byte wherever it sits, before, between or
// after blocks: staff text exists nowhere else, and a lead created by a form
// starts with a block, so staff notes usually follow one. Otherwise anyone who
// knows a lead's address could erase them by submitting a public form a few
// times. Blocks written before blocks carried an end line cannot be told apart
// from staff text that follows them, so they are kept too.
const (
	// leadNotesBlockMaxBytes caps what a single submission adds.
	leadNotesBlockMaxBytes = 16 << 10

	leadNotesBlockTruncatedMarker = "\n[Truncated here; the full submission is kept with the form.]"
	// leadNotesTrimmedMarker stands before the remaining form blocks once older
	// ones have been dropped.
	leadNotesTrimmedMarker = "\n\n[Earlier form submissions trimmed to fit; each is kept in full with its form.]"

	// leadNotesBlockHeader opens every submission block and leadNotesBlockEnd
	// closes it.
	leadNotesBlockHeader   = "--- Form submission:"
	leadNotesBlockEnd      = "--- End of form submission ---"
	leadNotesBlockBoundary = "\n\n" + leadNotesBlockHeader
)

var leadNotesTrimmedMarkerText = strings.TrimLeft(leadNotesTrimmedMarker, "\n")

// notesSegment is one piece of a lead's notes, in order: either a complete
// generated form block, which may be dropped, or any other text, which is kept.
type notesSegment struct {
	text  string
	block bool
}

// appendLeadNotes adds a submission block to a lead's notes and returns the
// result, which always fits models.LeadNotesMaxBytes. Older form blocks are
// dropped whole to make room. If the remaining text alone leaves no room for
// the block, the short pointer is appended instead, and if not even that fits
// the notes are returned unchanged.
func appendLeadNotes(existing, block, pointer string) string {
	block = capNotesBlock(block) + "\n" + leadNotesBlockEnd
	existing = strings.TrimRight(existing, "\n")
	if strings.TrimSpace(existing) == "" {
		return strings.TrimLeft(block, "\n")
	}
	if len(existing)+len(block) <= models.LeadNotesMaxBytes {
		return existing + block
	}

	segments := splitLeadNotes(existing)
	trimmed := strings.Contains(existing, leadNotesTrimmedMarkerText)
	for {
		if candidate := joinLeadNotes(segments, trimmed) + block; len(candidate) <= models.LeadNotesMaxBytes {
			return candidate
		}
		oldest := -1
		for i, segment := range segments {
			if segment.block {
				oldest = i
				break
			}
		}
		if oldest < 0 {
			break
		}
		segments = append(segments[:oldest:oldest], segments[oldest+1:]...)
		trimmed = true
	}

	// The text that must be kept is too long to leave room for the block.
	if len(existing)+len(pointer) <= models.LeadNotesMaxBytes {
		return existing + pointer
	}
	return existing
}

// splitLeadNotes cuts notes into ordered segments. A block segment runs from a
// header to the first end line after it, with no other header in between, and
// includes the blank line before the header. A header without its own end
// line starts ordinary text. Trim markers are removed; joinLeadNotes places
// one where it belongs.
func splitLeadNotes(notes string) []notesSegment {
	var segments []notesSegment
	addText := func(text string) {
		text = strings.ReplaceAll(text, leadNotesTrimmedMarker, "")
		text = strings.ReplaceAll(text, leadNotesTrimmedMarkerText, "")
		if text != "" {
			segments = append(segments, notesSegment{text: text})
		}
	}

	rest := notes
	for rest != "" {
		h := strings.Index(rest, leadNotesBlockHeader)
		if h < 0 {
			addText(rest)
			break
		}
		body := h + len(leadNotesBlockHeader)
		end := strings.Index(rest[body:], leadNotesBlockEnd)
		next := strings.Index(rest[body:], leadNotesBlockHeader)
		if end < 0 || (next >= 0 && next < end) {
			stop := len(rest)
			if next >= 0 {
				stop = body + next
				// The blank line before the next header belongs to that block,
				// if it is one, not to this text.
				if strings.HasSuffix(rest[:stop], "\n\n") {
					stop -= 2
				}
			}
			addText(rest[:stop])
			rest = rest[stop:]
			continue
		}
		start := h
		if strings.HasSuffix(rest[:h], "\n\n") {
			start = h - 2
		}
		stop := body + end + len(leadNotesBlockEnd)
		addText(rest[:start])
		segments = append(segments, notesSegment{text: rest[start:stop], block: true})
		rest = rest[stop:]
	}
	return segments
}

// joinLeadNotes puts segments back together. When blocks have been trimmed,
// one marker stands before the first remaining block, or at the end if none
// remains (the new block follows it).
func joinLeadNotes(segments []notesSegment, trimmed bool) string {
	var b strings.Builder
	// Generated text (a marker or a block) opens with a blank line to separate
	// it from what precedes it; at the very start there is nothing to separate.
	// Staff text is never trimmed, not even its leading newlines.
	write := func(text string, generated bool) {
		if b.Len() == 0 && generated {
			text = strings.TrimLeft(text, "\n")
		}
		b.WriteString(text)
	}
	markerPlaced := !trimmed
	for _, segment := range segments {
		if !segment.block {
			write(segment.text, false)
			continue
		}
		if !markerPlaced {
			write(leadNotesTrimmedMarker, true)
			markerPlaced = true
		}
		text := segment.text
		if !strings.HasPrefix(text, "\n\n") {
			text = "\n\n" + text
		}
		write(text, true)
	}
	if !markerPlaced {
		write(leadNotesTrimmedMarker, true)
	}
	return b.String()
}

// capNotesBlock cuts one submission block so that, with its end line, it stays
// within leadNotesBlockMaxBytes. It cuts on a character boundary and says so.
func capNotesBlock(block string) string {
	limit := leadNotesBlockMaxBytes - len("\n"+leadNotesBlockEnd)
	if len(block) <= limit {
		return block
	}
	cut := limit - len(leadNotesBlockTruncatedMarker)
	for cut > 0 && !utf8.RuneStart(block[cut]) {
		cut--
	}
	return block[:cut] + leadNotesBlockTruncatedMarker
}

// neutraliseNotesHeader keeps a submitted value from forging a block header or
// end line, which would make the notes split at the wrong place. It repeats
// until neither is left: one pass is not enough, because the dash it removes
// can leave another dash run that completes the marker ("----- End of form
// submission ---" would become "---- End ...", still containing it).
func neutraliseNotesHeader(line string) string {
	for strings.Contains(line, leadNotesBlockHeader) || strings.Contains(line, leadNotesBlockEnd) {
		line = strings.ReplaceAll(line, leadNotesBlockHeader, "-- Form submission:")
		line = strings.ReplaceAll(line, leadNotesBlockEnd, "-- End of form submission ---")
	}
	return line
}
