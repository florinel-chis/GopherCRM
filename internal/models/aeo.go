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
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Domain  string   `json:"domain"`
}

// AEOProfile is the single brand profile the whole module is measured against.
// The service pins its ID to 1; there is never more than one live row.
type AEOProfile struct {
	BaseModel
	BrandName        string `gorm:"not null;type:varchar(120)" json:"brand_name"`
	Description      string `gorm:"type:text" json:"description"`
	BrandAliasesJSON string `gorm:"column:brand_aliases;type:text" json:"-"`
	OwnedDomainsJSON string `gorm:"column:owned_domains;type:text" json:"-"`
	CompetitorsJSON  string `gorm:"column:competitors;type:text" json:"-"`

	BrandAliases []string        `gorm:"-" json:"brand_aliases"`
	OwnedDomains []string        `gorm:"-" json:"owned_domains"`
	Competitors  []AEOCompetitor `gorm:"-" json:"competitors"`
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
	Text string `gorm:"not null;type:varchar(500)" json:"text"`
	// IsActive defaults to true at the column level, which also means an INSERT
	// of a struct with IsActive=false stores true (GORM substitutes a literal
	// default for a zero-valued field). Prompts are always created active, and
	// deactivation goes through an UPDATE, so the substitution is harmless here.
	IsActive    bool  `gorm:"not null;default:true;index" json:"is_active"`
	CreatedByID *uint `gorm:"index" json:"created_by_id,omitempty"`

	// Computed per request over the requested window, never stored.
	Visibility   float64    `gorm:"-" json:"visibility"` // 0..100, one decimal
	AnswerCount  int64      `gorm:"-" json:"answer_count"`
	MentionCount int64      `gorm:"-" json:"mention_count"`
	LastRunAt    *time.Time `gorm:"-" json:"last_run_at,omitempty"`
}

func (AEOPrompt) TableName() string {
	return "aeo_prompts"
}

// AEORun is one batch execution of every active prompt against every
// configured provider.
type AEORun struct {
	BaseModel
	Trigger       string     `gorm:"not null;type:varchar(20);index" json:"trigger"` // "manual" | "scheduled"
	Status        string     `gorm:"not null;type:varchar(20);index" json:"status"`  // "running" | "completed" | "failed" | "partial"
	StartedAt     time.Time  `gorm:"not null" json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	TotalQueries  int        `gorm:"not null;default:0" json:"total_queries"`
	FailedQueries int        `gorm:"not null;default:0" json:"failed_queries"`
	TriggeredByID *uint      `gorm:"index" json:"triggered_by_id,omitempty"`
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
	RunID          uint   `gorm:"not null;index" json:"run_id"`
	PromptID       uint   `gorm:"not null;index" json:"prompt_id"`
	Provider       string `gorm:"not null;type:varchar(40);index" json:"provider"`
	Model          string `gorm:"not null;type:varchar(120)" json:"model"`
	Attempt        int    `gorm:"not null;default:1" json:"attempt"`
	AnswerText     string `gorm:"type:longtext" json:"answer_text"`
	BrandMentioned bool   `gorm:"not null;default:false;index" json:"brand_mentioned"`
	// FirstMentionPos is the rune index of the earliest brand hit, -1 when the
	// brand is absent. It deliberately carries NO `default:-1` tag: GORM
	// substitutes a literal column default whenever the Go field holds its zero
	// value (callbacks.ConvertToCreateValues), so a brand named in the very
	// first rune of an answer — a common case — would be persisted as -1.
	// Writers must always set this field explicitly.
	FirstMentionPos        int    `gorm:"not null" json:"first_mention_pos"`
	CompetitorMentionsJSON string `gorm:"column:competitor_mentions;type:text" json:"-"`
	LatencyMs              int    `gorm:"not null;default:0" json:"latency_ms"`
	Error                  string `gorm:"type:text" json:"error,omitempty"`

	CompetitorMentions map[string]int `gorm:"-" json:"competitor_mentions"`
	Citations          []AEOCitation  `gorm:"foreignKey:AnswerID" json:"citations"`
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

// AEOCitation is a URL referenced by an answer. Citations live in their own
// table so the citations report is a plain SQL aggregation over scalar columns.
type AEOCitation struct {
	BaseModel
	AnswerID       uint   `gorm:"not null;index" json:"answer_id"`
	URL            string `gorm:"column:url;not null;type:varchar(1024)" json:"url"`
	Domain         string `gorm:"not null;type:varchar(255);index" json:"domain"`
	IsOwned        bool   `gorm:"not null;default:false" json:"is_owned"`
	CompetitorName string `gorm:"type:varchar(120)" json:"competitor_name,omitempty"`
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
	Name       string `json:"name"`
	Model      string `json:"model"`
	Configured bool   `json:"configured"`
}

// AEOProviderVisibility is the brand visibility for a single provider.
type AEOProviderVisibility struct {
	Provider   string  `json:"provider"`
	Answers    int64   `json:"answers"`
	Mentions   int64   `json:"mentions"`
	Visibility float64 `json:"visibility"`
}

// AEOTimelinePoint is one day of the visibility time series.
type AEOTimelinePoint struct {
	Day        string             `json:"day"` // "YYYY-MM-DD", UTC
	Overall    float64            `json:"overall"`
	ByProvider map[string]float64 `json:"by_provider"`
}

// AEOCompetitorTimelinePoint is one day of the per-company visibility series.
type AEOCompetitorTimelinePoint struct {
	Day       string             `json:"day"` // "YYYY-MM-DD", UTC
	ByCompany map[string]float64 `json:"by_company"`
}

// AEOShareOfVoiceEntry is one company's slice of all mention events in range.
type AEOShareOfVoiceEntry struct {
	Company    string  `json:"company"`
	IsBrand    bool    `json:"is_brand"`
	Mentions   int64   `json:"mentions"`
	Share      float64 `json:"share"`
	Visibility float64 `json:"visibility"`
}

// AEODashboard is the payload of GET /aeo/dashboard. Every rate is a
// percentage in 0..100 rounded to one decimal.
type AEODashboard struct {
	From               string                       `json:"from"`
	To                 string                       `json:"to"`
	Days               int                          `json:"days"`
	TotalAnswers       int64                        `json:"total_answers"`
	FailedAnswers      int64                        `json:"failed_answers"`
	BrandMentions      int64                        `json:"brand_mentions"`
	Visibility         float64                      `json:"visibility"`
	ByProvider         []AEOProviderVisibility      `json:"by_provider"`
	Timeline           []AEOTimelinePoint           `json:"timeline"`
	ShareOfVoice       []AEOShareOfVoiceEntry       `json:"share_of_voice"`
	CompetitorTimeline []AEOCompetitorTimelinePoint `json:"competitor_timeline"`
	LastRunAt          *time.Time                   `json:"last_run_at,omitempty"`
}

// AEOCitationCompanyStat aggregates citations for one company (brand or
// competitor).
type AEOCitationCompanyStat struct {
	Company          string  `json:"company"`
	IsBrand          bool    `json:"is_brand"`
	Citations        int64   `json:"citations"`
	CitationRate     float64 `json:"citation_rate"`
	WithBrandMention int64   `json:"with_brand_mention"`
	BrandMentionRate float64 `json:"brand_mention_rate"`
}

// AEOCitationDomainStat aggregates citations for one domain.
type AEOCitationDomainStat struct {
	Domain           string  `json:"domain"`
	Company          string  `json:"company"`
	IsOwned          bool    `json:"is_owned"`
	Citations        int64   `json:"citations"`
	CitationRate     float64 `json:"citation_rate"`
	WithBrandMention int64   `json:"with_brand_mention"`
	BrandMentionRate float64 `json:"brand_mention_rate"`
}

// AEOCitationsReport is the payload of GET /aeo/citations.
type AEOCitationsReport struct {
	From                 string                   `json:"from"`
	To                   string                   `json:"to"`
	TotalAnswers         int64                    `json:"total_answers"`
	TotalCitations       int64                    `json:"total_citations"`
	AnswersWithCitations int64                    `json:"answers_with_citations"`
	OwnedCitationRate    float64                  `json:"owned_citation_rate"`
	ByCompany            []AEOCitationCompanyStat `json:"by_company"`
	TopDomains           []AEOCitationDomainStat  `json:"top_domains"` // max 20, citations desc
}
