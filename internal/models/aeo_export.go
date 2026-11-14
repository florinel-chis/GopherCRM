package models

import "time"

// AEO export (#244). These are serialisation shapes for GET /aeo/export, never
// persisted, bound or migrated. They deliberately do not embed AEORun or
// AEOAnswer: the export lists its fields one by one, so the run-ownership
// columns of #229 (owner_instance, heartbeat_at, running_lock, schedule_slot)
// and any column added to those models later cannot leak into a client file.

// AEOExportFormatVersion is the format_version of every JSON export.
const AEOExportFormatVersion = 1

// AEOExportSelection names what an export covers: one run (RunID) or a window
// of Days days ending at the start of tomorrow in UTC. The handler fills RunID
// or Days; the service fills From and To for a window from its own clock.
//
// @Description What the export covers: run_id for one run, or days with from and to for a window.
type AEOExportSelection struct {
	// The run exported; omitted for a window export.
	RunID uint `json:"run_id,omitempty" example:"12"`
	// Window length in days (7, 30 or 90); omitted for a run export.
	Days int `json:"days,omitempty" example:"30" enums:"7,30,90"`
	// Inclusive start of the window (UTC, RFC 3339): the start of tomorrow
	// minus days. Omitted for a run export.
	From *time.Time `json:"from,omitempty" example:"2026-10-16T00:00:00Z"`
	// Exclusive end of the window (UTC, RFC 3339): the start of tomorrow, so
	// today's answers are included. Omitted for a run export.
	To *time.Time `json:"to,omitempty" example:"2026-11-15T00:00:00Z"`
}

// AEOExportRun is one run referenced by the exported answers (or the selected
// run). It carries no ownership column.
//
// @Description A run referenced by the exported answers, or the selected run.
type AEOExportRun struct {
	// Run ID.
	ID uint `json:"id" example:"12"`
	// What started the run.
	Trigger string `json:"trigger" enums:"manual,scheduled" example:"scheduled"`
	// Run state when the export was read; a running run may be exported.
	Status string `json:"status" enums:"running,completed,failed,partial" example:"completed"`
	// When the run started (UTC, RFC 3339).
	StartedAt time.Time `json:"started_at" example:"2026-11-14T06:00:00Z"`
	// When the run reached a terminal status (UTC, RFC 3339); null while it is
	// running.
	CompletedAt *time.Time `json:"completed_at" example:"2026-11-14T06:03:41Z" extensions:"x-nullable"`
	// Provider calls in the run.
	TotalQueries int `json:"total_queries" example:"24"`
	// Provider calls that failed or could not be stored.
	FailedQueries int `json:"failed_queries" example:"0"`
	// ID of the user who started a manual run; null for scheduled runs.
	TriggeredByID *uint `json:"triggered_by_id" example:"1" extensions:"x-nullable"`
}

// AEOExportCitation is one live citation of an exported answer.
//
// @Description A URL cited by an exported answer.
type AEOExportCitation struct {
	// Citation ID.
	ID uint `json:"id" example:"301"`
	// Cited URL.
	URL string `json:"url" example:"https://docs.acme.example/self-hosting"`
	// Host of the URL, lower-cased, without a leading "www.".
	Domain string `json:"domain" example:"docs.acme.example"`
	// Whether the host is an owned domain (as configured when the answer was
	// analysed).
	IsOwned bool `json:"is_owned" example:"true"`
	// Competitor whose domain was cited; empty when none matched.
	CompetitorName string `json:"competitor_name" example:"Globex CRM"`
}

// AEOExportAnswer is one live answer of the selection with its prompt text and
// citations.
//
// @Description One stored answer with its prompt text and citations.
type AEOExportAnswer struct {
	// Answer ID.
	ID uint `json:"id" example:"88"`
	// Run the answer belongs to.
	RunID uint `json:"run_id" example:"12"`
	// Prompt that was asked.
	PromptID uint `json:"prompt_id" example:"3"`
	// Text of the prompt, also when the prompt has since been deleted.
	PromptText string `json:"prompt_text" example:"What is the best self-hosted CRM for a small sales team?"`
	// Engine that answered (not a closed set).
	Provider string `json:"provider" example:"anthropic"`
	// Model id the engine was called with.
	Model string `json:"model" example:"claude-opus-5"`
	// When the answer was stored (UTC, RFC 3339); the window predicate and the
	// first order key.
	CreatedAt time.Time `json:"created_at" example:"2026-11-14T06:00:12Z"`
	// Full answer text; empty when the call failed. JSON export only.
	AnswerText string `json:"answer_text" example:"For a small team, Acme CRM and Globex CRM are popular self-hosted options..."`
	// Whether the brand is mentioned.
	BrandMentioned bool `json:"brand_mentioned" example:"true"`
	// Code point offset of the first brand mention; -1 when absent.
	FirstMentionPos int `json:"first_mention_pos" example:"23" minimum:"-1"`
	// Duration of the provider call in milliseconds.
	LatencyMs int `json:"latency_ms" example:"2350"`
	// Sanitised failure description; empty when the call succeeded.
	Error string `json:"error" example:"openai: request timed out"`
	// Occurrences per competitor name; always an object.
	CompetitorMentions map[string]int `json:"competitor_mentions"`
	// Live citations in id order; always an array.
	Citations []AEOExportCitation `json:"citations"`
}

// AEOExport is the body of GET /aeo/export?format=json (the raw body, not the
// envelope) and the source of the CSV rows.
//
// @Description AEO export: the selected runs and their answers with citations.
type AEOExport struct {
	// Version of this format; always 1.
	FormatVersion int `json:"format_version" example:"1"`
	// When the export was assembled (service clock, UTC, RFC 3339).
	GeneratedAt time.Time `json:"generated_at" example:"2026-11-14T10:15:00Z"`
	// What the export covers.
	Selection AEOExportSelection `json:"selection"`
	// Runs referenced by the answers (and the selected run), id ascending.
	// Always an array.
	Runs []AEOExportRun `json:"runs"`
	// Answers ordered by created_at, then id, ascending. Always an array.
	Answers []AEOExportAnswer `json:"answers"`
}
