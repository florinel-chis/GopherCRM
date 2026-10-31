package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AEO (Answer Engine Optimization) tracks how a brand is represented in the
// answers produced by generative answer engines.
//
// Every list- or map-shaped attribute is persisted as serialized JSON in a TEXT
// column and mirrored by a `gorm:"-"` decoded twin. BeforeSave encodes the twin
// into the column, AfterFind decodes it back. Nothing here relies on the JSON
// functions of the database, so MySQL 8 (production) and in-memory SQLite
// (tests) behave identically.

// AEOCompetitor is one tracked competitor of the brand. It lives inside the
// serialized `competitors` column of aeo_profiles, never in a table of its own.
type AEOCompetitor struct {
	// Competitor name, trimmed. Counted as a mention when it appears in an
	// answer as a whole word, case-insensitively. Competitors saved with a blank
	// name are dropped.
	Name string `json:"name" example:"Globex CRM" maxLength:"120"`
	// Alternative spellings that also count as a mention of this competitor.
	// Trimmed, blanks dropped, de-duplicated case-insensitively; always an
	// array, empty when none.
	Aliases []string `json:"aliases" example:"Globex,Globex Corporation"`
	// Competitor website as a bare host (lower-cased, scheme, path and a
	// leading "www." removed). Citations of this host or any of its subdomains
	// are attributed to the competitor. Empty when not set.
	Domain string `json:"domain" example:"globex.example" maxLength:"255"`
}

// AEOProfile is the single brand profile the whole module is measured against.
// The service pins its ID to 1; there is never more than one live row.
type AEOProfile struct {
	BaseModel
	// Brand name searched for in every answer (whole word, case-insensitive).
	// Trimmed; required, at most 120 characters.
	BrandName string `gorm:"not null;type:varchar(120)" json:"brand_name" example:"Acme CRM" maxLength:"120"`
	// What the business does, in plain words. Used only as context for prompt
	// generation. Trimmed; at most 2000 characters; empty when not set.
	Description      string `gorm:"type:text" json:"description" example:"Self-hosted CRM for small sales and support teams." maxLength:"2000"`
	BrandAliasesJSON string `gorm:"column:brand_aliases;type:text" json:"-"`
	OwnedDomainsJSON string `gorm:"column:owned_domains;type:text" json:"-"`
	CompetitorsJSON  string `gorm:"column:competitors;type:text" json:"-"`

	// Alternative brand spellings that also count as a brand mention. Trimmed,
	// blanks dropped, de-duplicated case-insensitively; always an array.
	BrandAliases []string `gorm:"-" json:"brand_aliases" example:"Acme,AcmeCRM"`
	// Domains the brand owns, as bare hosts (lower-cased, scheme, path and a
	// leading "www." removed). A citation of one of these hosts or of any
	// subdomain counts as an owned citation. Always an array.
	OwnedDomains []string `gorm:"-" json:"owned_domains" example:"acme.example,docs.acme.example"`
	// Tracked competitors in the order they were saved; that order is the
	// order of the comparison tables in the dashboard and citation report.
	// Always an array.
	Competitors []AEOCompetitor `gorm:"-" json:"competitors"`
}

func (AEOProfile) TableName() string {
	return "aeo_profiles"
}

// BeforeSave serializes the decoded twins into their TEXT columns.
func (p *AEOProfile) BeforeSave(tx *gorm.DB) error {
	var err error
	if p.BrandAliasesJSON, err = encodeJSONSlice(p.BrandAliases); err != nil {
		return fmt.Errorf("aeo profile brand_aliases: %w", err)
	}
	if p.OwnedDomainsJSON, err = encodeJSONSlice(p.OwnedDomains); err != nil {
		return fmt.Errorf("aeo profile owned_domains: %w", err)
	}
	if p.CompetitorsJSON, err = encodeJSONSlice(p.Competitors); err != nil {
		return fmt.Errorf("aeo profile competitors: %w", err)
	}
	return nil
}

// AfterFind restores the decoded twins from their TEXT columns.
func (p *AEOProfile) AfterFind(tx *gorm.DB) error {
	p.BrandAliases = decodeJSONSlice[string](p.BrandAliasesJSON)
	p.OwnedDomains = decodeJSONSlice[string](p.OwnedDomainsJSON)
	p.Competitors = decodeJSONSlice[AEOCompetitor](p.CompetitorsJSON)
	return nil
}

// AEOPrompt is a question sent to every configured answer engine on each run.
//
// There is deliberately no unique index on `text`: rows are soft-deleted, and a
// unique index would keep a deleted prompt's text reserved forever (the trap
// documented for labels). Uniqueness is a service-level LOWER(text) pre-check
// over live rows only.
type AEOPrompt struct {
	BaseModel
	// The question sent to every configured engine. Trimmed, 1 to 500
	// characters, unique case-insensitively among live (not deleted) prompts.
	Text string `gorm:"not null;type:varchar(500)" json:"text" example:"What is the best self-hosted CRM for a small sales team?" maxLength:"500"`

	// IsActive defaults to true at the column level, which also means an INSERT
	// of a struct with IsActive=false stores true (GORM substitutes a literal
	// default for a zero-valued field). Prompts are always created active, and
	// deactivation goes through an UPDATE, so the substitution is harmless here.

	// Whether the prompt is included in full runs (POST /aeo/runs and the
	// daily schedule). New prompts are always active; toggled through
	// PUT /aeo/prompts/{id}. At most 100 prompts may be active at once.
	IsActive bool `gorm:"not null;default:true;index" json:"is_active" example:"true"`
	// ID of the user who created the prompt. Server-assigned; omitted when
	// unknown.
	CreatedByID *uint `gorm:"index" json:"created_by_id,omitempty" example:"1" readonly:"true"`

	// Computed per request over the requested window, never stored.

	// Share of the non-error answers in the requested window (?days=) that
	// mention the brand, as a percentage 0..100 rounded to one decimal; 0 when
	// there are no answers. Computed per request, never stored. Only the list
	// endpoint fills it; other responses carry 0.
	Visibility float64 `gorm:"-" json:"visibility" example:"42.5" minimum:"0" maximum:"100" readonly:"true"`
	// Number of non-error answers for this prompt in the requested window.
	// Computed per request; filled only by the list endpoint.
	AnswerCount int64 `gorm:"-" json:"answer_count" example:"40" readonly:"true"`
	// Number of those answers that mention the brand. Computed per request;
	// filled only by the list endpoint.
	MentionCount int64 `gorm:"-" json:"mention_count" example:"17" readonly:"true"`
	// Time of the most recent answer for this prompt in the requested window
	// (RFC 3339). Filled only by the list endpoint; omitted when there is none.
	LastRunAt *time.Time `gorm:"-" json:"last_run_at,omitempty" example:"2026-10-30T06:00:12Z" readonly:"true"`
}

func (AEOPrompt) TableName() string {
	return "aeo_prompts"
}

// AEORun is one batch execution of every active prompt against every
// configured provider.
type AEORun struct {
	BaseModel
	// What started the run: "manual" for POST /aeo/runs and
	// POST /aeo/prompts/{id}/run, "scheduled" for the daily scheduler.
	Trigger string `gorm:"not null;type:varchar(20);index" json:"trigger" enums:"manual,scheduled" example:"manual" readonly:"true"`
	// Run state. "running" while provider calls are in flight; then
	// "completed" (no call failed), "partial" (some failed) or "failed" (every
	// call failed). A run interrupted by shutdown ends "failed" when no answer
	// was recorded and "partial" otherwise. A run still "running" after 6
	// hours, or left running by a previous process, is marked "failed".
	Status string `gorm:"not null;type:varchar(20);index" json:"status" enums:"running,completed,failed,partial" example:"running" readonly:"true"`
	// When the run was started (UTC, RFC 3339).
	StartedAt time.Time `gorm:"not null" json:"started_at" example:"2026-10-30T06:00:00Z" readonly:"true"`
	// When the run reached a terminal status (RFC 3339); omitted while it is
	// running.
	CompletedAt *time.Time `json:"completed_at,omitempty" example:"2026-10-30T06:03:41Z" readonly:"true"`
	// Number of provider calls in the run: prompts in the run times the
	// engines configured when it started.
	TotalQueries int `gorm:"not null;default:0" json:"total_queries" example:"24" readonly:"true"`
	// Number of provider calls that failed or whose answer could not be
	// stored. 0 while running.
	FailedQueries int `gorm:"not null;default:0" json:"failed_queries" example:"0" readonly:"true"`
	// ID of the user who started a manual run; omitted for scheduled runs.
	TriggeredByID *uint `gorm:"index" json:"triggered_by_id,omitempty" example:"1" readonly:"true"`
}

func (AEORun) TableName() string {
	return "aeo_runs"
}

// AEO run triggers and statuses.
const (
	AEOTriggerManual    = "manual"
	AEOTriggerScheduled = "scheduled"

	AEORunStatusRunning   = "running"
	AEORunStatusCompleted = "completed"
	AEORunStatusFailed    = "failed"
	AEORunStatusPartial   = "partial"
)

// AEOAnswer is one provider response to one prompt inside one run. A failed
// call is recorded too, with Error set and an empty AnswerText, so failures
// stay visible instead of silently shrinking the denominator.
type AEOAnswer struct {
	BaseModel
	// ID of the run this answer belongs to.
	RunID uint `gorm:"not null;index" json:"run_id" example:"12" readonly:"true"`
	// ID of the prompt that was asked.
	PromptID uint `gorm:"not null;index" json:"prompt_id" example:"3" readonly:"true"`
	// Engine that answered: anthropic, openai, gemini, kimi or perplexity, or
	// the custom engine's operator-configured name (AEO_CUSTOM_NAME, "custom"
	// when unset). Not a closed set.
	Provider string `gorm:"not null;type:varchar(40);index" json:"provider" example:"anthropic" readonly:"true"`
	// Model id the engine was called with, as configured at the time.
	Model string `gorm:"not null;type:varchar(120)" json:"model" example:"claude-opus-5" readonly:"true"`
	// Attempt number; always 1 (calls are not retried).
	Attempt int `gorm:"not null;default:1" json:"attempt" example:"1" readonly:"true"`
	// Full answer text as returned by the engine (often markdown). Empty when
	// the call failed.
	AnswerText string `gorm:"type:longtext" json:"answer_text" example:"For a small team, Acme CRM and Globex CRM are popular self-hosted options..." readonly:"true"`
	// Whether the brand name or one of its aliases appears in the answer as a
	// whole word (case-insensitive). False for failed calls.
	BrandMentioned bool `gorm:"not null;default:false;index" json:"brand_mentioned" example:"true" readonly:"true"`

	// FirstMentionPos is the rune index of the earliest brand hit, -1 when the
	// brand is absent. It deliberately carries NO `default:-1` tag: GORM
	// substitutes a literal column default whenever the Go field holds its zero
	// value (callbacks.ConvertToCreateValues), so a brand named in the very
	// first rune of an answer — a common case — would be persisted as -1.
	// Writers must always set this field explicitly.

	// Character (Unicode code point) offset of the first brand mention in
	// answer_text, 0-based; -1 when the brand is not mentioned or the call
	// failed.
	FirstMentionPos        int    `gorm:"not null" json:"first_mention_pos" example:"23" minimum:"-1" readonly:"true"`
	CompetitorMentionsJSON string `gorm:"column:competitor_mentions;type:text" json:"-"`
	// Wall-clock duration of the provider call in milliseconds.
	LatencyMs int `gorm:"not null;default:0" json:"latency_ms" example:"2350" readonly:"true"`
	// Sanitized failure description ("<engine>: <reason>", optionally with
	// "(HTTP <status>)"); omitted when the call succeeded.
	Error string `gorm:"type:text" json:"error,omitempty" example:"openai: request timed out" readonly:"true"`

	// Occurrences per competitor name in answer_text (whole-word,
	// case-insensitive, names and aliases counted together). Competitors that
	// were not mentioned are absent; always an object, empty for failed calls.
	CompetitorMentions map[string]int `gorm:"-" json:"competitor_mentions" readonly:"true"`
	// URLs cited by the answer, de-duplicated. Always an array.
	Citations []AEOCitation `gorm:"foreignKey:AnswerID" json:"citations" readonly:"true"`
}

func (AEOAnswer) TableName() string {
	return "aeo_answers"
}

// BeforeSave serializes the competitor mention counts into their TEXT column.
func (a *AEOAnswer) BeforeSave(tx *gorm.DB) error {
	encoded, err := encodeJSONMap(a.CompetitorMentions)
	if err != nil {
		return fmt.Errorf("aeo answer competitor_mentions: %w", err)
	}
	a.CompetitorMentionsJSON = encoded
	return nil
}

// AfterFind restores the competitor mention counts and guarantees that both
// JSON-facing collections marshal as `{}` / `[]` rather than `null`.
// GORM runs preloads before this hook, so an eagerly loaded Citations slice is
// left untouched here.
func (a *AEOAnswer) AfterFind(tx *gorm.DB) error {
	a.CompetitorMentions = DecodeCompetitorMentions(a.CompetitorMentionsJSON)
	if a.Citations == nil {
		a.Citations = []AEOCitation{}
	}
	return nil
}

// Length limits of the sized AEO string columns, in characters (MySQL counts
// varchar in characters, not bytes). They are the single source for every
// check that keeps a value out of a column it would not fit: request binding,
// service validation and citation filtering. TestAEOLengthLimitsMatchTheColumns
// fails if one of them drifts from the gorm tag it mirrors.
const (
	// AEOCitationURLMaxLength is aeo_citations.url.
	AEOCitationURLMaxLength = 1024
	// AEOCitationDomainMaxLength is aeo_citations.domain.
	AEOCitationDomainMaxLength = 255
	// AEOCompetitorNameMaxLength is aeo_citations.competitor_name, which is
	// filled from the profile's competitor names.
	AEOCompetitorNameMaxLength = 120
	// AEOBrandNameMaxLength is aeo_profiles.brand_name.
	AEOBrandNameMaxLength = 120
	// AEOPromptTextMaxLength is aeo_prompts.text.
	AEOPromptTextMaxLength = 500
	// AEOAnswerProviderMaxLength is aeo_answers.provider, filled from the
	// engine name (operator-configurable for the custom engine).
	AEOAnswerProviderMaxLength = 40
	// AEOAnswerModelMaxLength is aeo_answers.model, filled from the configured
	// model id.
	AEOAnswerModelMaxLength = 120
)

// AEOCitation is a URL referenced by an answer. Citations live in their own
// table so the citations report is a plain SQL aggregation over scalar columns.
type AEOCitation struct {
	BaseModel
	// ID of the answer that cited the URL.
	AnswerID uint `gorm:"not null;index" json:"answer_id" example:"88" readonly:"true"`
	// Cited URL, with trailing punctuation removed. URLs longer than 1024
	// characters are not stored.
	URL string `gorm:"column:url;not null;type:varchar(1024)" json:"url" example:"https://docs.acme.example/self-hosting" readonly:"true"`
	// Host of the URL, lower-cased, without a leading "www.".
	Domain string `gorm:"not null;type:varchar(255);index" json:"domain" example:"docs.acme.example" readonly:"true"`
	// Whether the host is one of the profile's owned domains or a subdomain of
	// one, as configured when the answer was analysed.
	IsOwned bool `gorm:"not null;default:false" json:"is_owned" example:"true" readonly:"true"`
	// Name of the competitor whose domain (or a subdomain of it) was cited, as
	// configured when the answer was analysed; omitted when none matched.
	CompetitorName string `gorm:"type:varchar(120)" json:"competitor_name,omitempty" example:"Globex CRM" readonly:"true"`
}

func (AEOCitation) TableName() string {
	return "aeo_citations"
}

// ---------------------------------------------------------------------------
// JSON column helpers
// ---------------------------------------------------------------------------

// encodeJSONSlice serializes a slice for storage in a TEXT column. An empty or
// nil slice collapses to the empty string so rows stay small and an untouched
// column round-trips to an empty value instead of the literal "null".
func encodeJSONSlice[T any](values []T) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// decodeJSONSlice is the inverse of encodeJSONSlice. It always returns a
// non-nil slice so the API emits `[]` rather than `null`.
//
// A column that does not parse yields an empty slice rather than an error: the
// only writer is encodeJSONSlice, so malformed content means the row was edited
// outside the application, and one such row must not fail an entire list query.
func decodeJSONSlice[T any](raw string) []T {
	values := make([]T, 0)
	if strings.TrimSpace(raw) == "" {
		return values
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return make([]T, 0)
	}
	return values
}

// encodeJSONMap serializes a counter map for storage in a TEXT column, with the
// same empty-string convention as encodeJSONSlice.
func encodeJSONMap(values map[string]int) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// DecodeCompetitorMentions turns a serialized `competitor_mentions` column into
// its map form. It is exported because the repository projects that column
// directly into AEOAnswerFact, bypassing the model hooks. It never returns nil
// and never fails, for the reason given on decodeJSONSlice.
func DecodeCompetitorMentions(raw string) map[string]int {
	counts := make(map[string]int)
	if strings.TrimSpace(raw) == "" {
		return counts
	}
	if err := json.Unmarshal([]byte(raw), &counts); err != nil {
		return make(map[string]int)
	}
	return counts
}

// ---------------------------------------------------------------------------
// Transport / metric DTOs
//
// Plain structs, never persisted: no gorm tags, no table.
// ---------------------------------------------------------------------------

// AEOAnswerFact is the projection the repository hands to the service for
// metric computation: one row per answer in the requested range. Per-day
// bucketing and competitor tallying happen in Go over these facts, because
// neither date functions nor JSON functions are portable across MySQL and
// SQLite.
type AEOAnswerFact struct {
	AnswerID           uint
	PromptID           uint
	Provider           string
	CreatedAt          time.Time
	BrandMentioned     bool
	Errored            bool
	CompetitorMentions map[string]int
}

// AEOPromptVisibility carries the per-prompt counters computed over a window.
type AEOPromptVisibility struct {
	PromptID  uint
	Answers   int64
	Mentions  int64
	LastRunAt *time.Time
}

// AEOCitationAggRow is one GROUP BY row of the citation aggregation.
type AEOCitationAggRow struct {
	Domain           string
	IsOwned          bool
	CompetitorName   string
	Citations        int64
	WithBrandMention int64
}

// AEOProviderStatus reports one answer engine and whether it is configured.
type AEOProviderStatus struct {
	// Engine name: anthropic, openai, gemini, kimi, perplexity, and last the
	// custom engine under its operator-configured name (AEO_CUSTOM_NAME,
	// "custom" when unset). Not a closed set.
	Name string `json:"name" example:"anthropic"`
	// Model id the engine is called with (admin setting first, then the
	// AEO_<ENGINE>_MODEL environment variable, then the built-in default).
	// Empty when the custom engine has no model configured.
	Model string `json:"model" example:"claude-opus-5"`
	// Whether the engine takes part in runs: an API key is set for the five
	// named engines, a base URL for the custom engine. Keys are never returned.
	Configured bool `json:"configured" example:"true"`
}

// AEOProviderVisibility is the brand visibility for a single provider.
type AEOProviderVisibility struct {
	// Engine name as stored on the answers (see AEOAnswer.provider). Only
	// engines with at least one non-error answer in the window are listed,
	// sorted by name.
	Provider string `json:"provider" example:"openai"`
	// Non-error answers from this engine in the window.
	Answers int64 `json:"answers" example:"30"`
	// Of those, answers that mention the brand.
	Mentions int64 `json:"mentions" example:"12"`
	// mentions / answers as a percentage 0..100, one decimal.
	Visibility float64 `json:"visibility" example:"40" minimum:"0" maximum:"100"`
}

// AEOTimelinePoint is one day of the visibility time series.
type AEOTimelinePoint struct {
	// UTC calendar day, YYYY-MM-DD. Every day of the window is present.
	Day string `json:"day" example:"2026-10-30" format:"date"`
	// Visibility over all engines that day (percentage 0..100, one decimal);
	// 0 on a day without answers.
	Overall float64 `json:"overall" example:"45.8" minimum:"0" maximum:"100"`
	// Visibility per engine name that day (percentage 0..100, one decimal).
	// Only engines with a non-error answer that day are present; empty object
	// on a day without answers.
	ByProvider map[string]float64 `json:"by_provider"`
}

// AEOCompetitorTimelinePoint is one day of the per-company visibility series.
type AEOCompetitorTimelinePoint struct {
	// UTC calendar day, YYYY-MM-DD. Every day of the window is present.
	Day string `json:"day" example:"2026-10-30" format:"date"`
	// Per company (brand and each competitor, keyed by name): the share of
	// that day's non-error answers mentioning the company, percentage 0..100
	// with one decimal. Empty object on a day without answers.
	ByCompany map[string]float64 `json:"by_company"`
}

// AEOShareOfVoiceEntry is one company's slice of all mention events in range.
type AEOShareOfVoiceEntry struct {
	// Company name: the brand first, then each competitor in profile order.
	// Before a profile exists, the competitor names found in the answers,
	// alphabetically.
	Company string `json:"company" example:"Acme CRM"`
	// True for the brand's own entry.
	IsBrand bool `json:"is_brand" example:"true"`
	// Non-error answers in the window that mention this company (counted once
	// per answer, however often the name occurs).
	Mentions int64 `json:"mentions" example:"55"`
	// This company's mentions as a percentage of all companies' mentions in
	// the table, 0..100 with one decimal.
	Share float64 `json:"share" example:"61.1" minimum:"0" maximum:"100"`
	// This company's mentions as a percentage of all non-error answers in the
	// window, 0..100 with one decimal.
	Visibility float64 `json:"visibility" example:"45.8" minimum:"0" maximum:"100"`
}

// AEODashboard is the payload of GET /aeo/dashboard. Every rate is a
// percentage in 0..100 rounded to one decimal.
type AEODashboard struct {
	// First day of the window (UTC, YYYY-MM-DD, inclusive).
	From string `json:"from" example:"2026-10-02" format:"date"`
	// Exclusive end of the window (UTC, YYYY-MM-DD): the day after today.
	To string `json:"to" example:"2026-11-01" format:"date"`
	// Window length in days: 7, 30 or 90.
	Days int `json:"days" example:"30" enums:"7,30,90"`
	// All answers recorded in the window, failed calls included.
	TotalAnswers int64 `json:"total_answers" example:"120"`
	// Answers in the window that record a failed provider call. They are
	// excluded from every percentage.
	FailedAnswers int64 `json:"failed_answers" example:"4"`
	// Non-error answers in the window that mention the brand.
	BrandMentions int64 `json:"brand_mentions" example:"53"`
	// brand_mentions as a percentage of the non-error answers, 0..100 with
	// one decimal; 0 without answers.
	Visibility float64 `json:"visibility" example:"45.7" minimum:"0" maximum:"100"`
	// Visibility per engine, sorted by engine name. Always an array.
	ByProvider []AEOProviderVisibility `json:"by_provider"`
	// One point per UTC day of the window, oldest first. Always an array.
	Timeline []AEOTimelinePoint `json:"timeline"`
	// Share of voice across the brand and its competitors. Always an array.
	ShareOfVoice []AEOShareOfVoiceEntry `json:"share_of_voice"`
	// One point per UTC day of the window with per-company visibility, oldest
	// first. Always an array.
	CompetitorTimeline []AEOCompetitorTimelinePoint `json:"competitor_timeline"`
	// When the most recent run (in any window) finished, or started if it is
	// still running (RFC 3339); omitted when no run exists.
	LastRunAt *time.Time `json:"last_run_at,omitempty" example:"2026-10-30T06:03:41Z"`
}

// AEOCitationCompanyStat aggregates citations for one company (brand or
// competitor).
type AEOCitationCompanyStat struct {
	// Company name: the brand (citations of owned domains) or a competitor
	// (citations of its domain).
	Company string `json:"company" example:"Acme CRM"`
	// True for the brand's own row.
	IsBrand bool `json:"is_brand" example:"true"`
	// Citations of this company's domains in the window.
	Citations int64 `json:"citations" example:"18"`
	// citations as a percentage of all citations in the window, 0..100 with
	// one decimal.
	CitationRate float64 `json:"citation_rate" example:"22.5" minimum:"0" maximum:"100"`
	// Of those citations, the ones in answers that also mention the brand.
	WithBrandMention int64 `json:"with_brand_mention" example:"15"`
	// with_brand_mention / citations as a percentage, 0..100 with one decimal;
	// 0 without citations.
	BrandMentionRate float64 `json:"brand_mention_rate" example:"83.3" minimum:"0" maximum:"100"`
}

// AEOCitationDomainStat aggregates citations for one domain.
type AEOCitationDomainStat struct {
	// Cited host (lower-cased, no leading "www.").
	Domain string `json:"domain" example:"docs.acme.example"`
	// Company the host is attributed to: the brand name for an owned domain,
	// the competitor name for a competitor domain, empty otherwise.
	Company string `json:"company" example:"Acme CRM"`
	// Whether the host is an owned domain (or a subdomain of one).
	IsOwned bool `json:"is_owned" example:"true"`
	// Citations of this host in the window.
	Citations int64 `json:"citations" example:"9"`
	// citations as a percentage of all citations in the window, 0..100 with
	// one decimal.
	CitationRate float64 `json:"citation_rate" example:"11.3" minimum:"0" maximum:"100"`
	// Of those citations, the ones in answers that also mention the brand.
	WithBrandMention int64 `json:"with_brand_mention" example:"8"`
	// with_brand_mention / citations as a percentage, 0..100 with one decimal.
	BrandMentionRate float64 `json:"brand_mention_rate" example:"88.9" minimum:"0" maximum:"100"`
}

// AEOCitationsReport is the payload of GET /aeo/citations.
type AEOCitationsReport struct {
	// First day of the window (UTC, YYYY-MM-DD, inclusive).
	From string `json:"from" example:"2026-10-02" format:"date"`
	// Exclusive end of the window (UTC, YYYY-MM-DD): the day after today.
	To string `json:"to" example:"2026-11-01" format:"date"`
	// Non-error answers recorded in the window (failed calls excluded).
	TotalAnswers int64 `json:"total_answers" example:"116"`
	// Citations extracted from the non-error answers in the window.
	TotalCitations int64 `json:"total_citations" example:"80"`
	// Answers in the window with at least one citation.
	AnswersWithCitations int64 `json:"answers_with_citations" example:"47"`
	// Citations of owned domains as a percentage of all citations, 0..100
	// with one decimal; 0 without citations.
	OwnedCitationRate float64 `json:"owned_citation_rate" example:"22.5" minimum:"0" maximum:"100"`
	// One row for the brand, then one per competitor in profile order,
	// including companies with no citations. Empty before a profile exists.
	ByCompany []AEOCitationCompanyStat `json:"by_company"`
	// The 20 most cited hosts, by citation count descending, then host name.
	// A host is listed once per attribution (owned flag and competitor name),
	// so a host whose attribution changed between profile edits can appear
	// twice. Always an array.
	TopDomains []AEOCitationDomainStat `json:"top_domains"`
}
