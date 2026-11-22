package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/lifecycle"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

const (
	// aeoMaxActivePrompts caps how many prompts a run may fan out over. A run
	// costs active prompts x configured providers external calls, so the cap is
	// a cost guard as much as a data-volume one. Not configurable in v1.
	aeoMaxActivePrompts = 100

	// aeoPromptTextMaxLength, aeoBrandNameMaxLength and
	// aeoCompetitorNameMaxLength are the column limits from internal/models.
	// Rejecting here turns a driver "data too long" error into a plain
	// validation failure. A competitor name is stored in the profile's TEXT
	// column, but it is copied into aeo_citations.competitor_name later.
	aeoPromptTextMaxLength     = models.AEOPromptTextMaxLength
	aeoBrandNameMaxLength      = models.AEOBrandNameMaxLength
	aeoCompetitorNameMaxLength = models.AEOCompetitorNameMaxLength

	// aeoDescriptionMaxLength caps a TEXT column to keep the prompt generation
	// payload sane.
	aeoDescriptionMaxLength = 2000

	// aeoProfileID pins the single profile row. The table is a singleton by
	// construction: the service always writes ID 1 and never inserts another.
	aeoProfileID = 1

	// aeoMaxRangeDays bounds every metrics query. A caller asking for more gets
	// the most recent 90 days rather than an error.
	aeoMaxRangeDays = 90

	// aeoDayFormat is the wire format of every per-day bucket. Buckets are cut
	// in UTC so a server timezone change cannot shift a point on a chart.
	aeoDayFormat = "2006-01-02"

	// Prompt generation bounds. The default and the ceiling mirror the handler
	// binding so a direct service caller behaves like an HTTP one.
	aeoDefaultGeneratedPrompts = 10
	aeoMaxGeneratedPrompts     = 25

	// aeoMinGeneratedPromptLength discards preamble fragments ("Here they are:")
	// that survive list parsing. No real buyer question is shorter than this.
	aeoMinGeneratedPromptLength = 12

	// aeoGenerationTimeout bounds the single generation call. It is longer than
	// a run's per-query deadline because the model is asked for a list rather
	// than one short answer.
	aeoGenerationTimeout = 90 * time.Second

	// aeoRunStaleAfter is how long a run may stay in "running" before the
	// overlap guard treats it as debris from a dead process.
	//
	// The worst-case honest run is aeoMaxActivePrompts (100) prompts times six
	// engines = 600 queries, drained four at a time with a 60s per-query
	// deadline: 150 waves x 60s = 2h30m. Six hours leaves a wide margin over
	// that and is still far inside the 24h scheduling period, so a run stranded
	// by a crash cannot block the next day's scheduled run.
	aeoRunStaleAfter = 6 * time.Hour

	// aeoRunHeartbeatInterval is how often the owner of a running run renews
	// aeo_runs.heartbeat_at (#229).
	aeoRunHeartbeatInterval = 30 * time.Second

	// aeoRunHeartbeatStaleAfter is the horizon after which a running row whose
	// heartbeat was not renewed is abandoned: its owner is gone (crash, kill,
	// lost database) and any replica may fail it, at boot or before a start.
	// Six missed heartbeats leave room for a slow database and for clock skew
	// between replicas well under 90 seconds.
	aeoRunHeartbeatStaleAfter = 3 * time.Minute
)

// Run triggers and statuses. Kept unexported here because they are wire values
// of the aeo_runs table, not a domain enum shared with other packages.
const (
	aeoTriggerManual    = "manual"
	aeoTriggerScheduled = "scheduled"
	aeoRunStatusRunning = "running"
)

// Service-level errors for the AEO module. They live here rather than in
// internal/errors because they are not part of the cross-cutting sentinel set:
// handlers match them with errors.Is and answer 400 VALIDATION_ERROR.
var (
	// ErrAEOPromptLimit is returned when a create or an activation would push
	// the number of active prompts past aeoMaxActivePrompts.
	ErrAEOPromptLimit = errors.New("active prompt limit of 100 reached")

	// ErrAEOInvalidPrompt covers empty and over-long prompt text.
	ErrAEOInvalidPrompt = errors.New("prompt text must be between 1 and 500 characters")

	// ErrAEOInvalidProfile covers a missing or over-long brand name, an
	// over-long description and an over-long competitor name. Each case wraps
	// it with its own message.
	ErrAEOInvalidProfile = errors.New("invalid brand profile")

	// ErrAEOInvalidTrigger guards the aeo_runs.trigger column against values
	// the dashboard cannot interpret.
	ErrAEOInvalidTrigger = errors.New("run trigger must be manual or scheduled")
)

type aeoService struct {
	repo      repository.AEORepository
	executor  aeo.Executor
	providers []aeo.Provider
	txManager repository.TransactionManager

	// startMu serializes this process's starts and its reconciliation. It is
	// not what keeps runs from overlapping: admission is the atomic ClaimRun
	// insert guarded by the unique running_lock index, which holds across every
	// replica sharing the database (#229). The mutex only keeps two local
	// starts from sweeping and claiming at the same time.
	startMu sync.Mutex

	// instanceID names this service in aeo_runs.owner_instance: 32 lowercase
	// hex characters from crypto/rand, generated once at construction. It is
	// never logged.
	instanceID string

	// now is the service clock (UTC on use). Tests inject one to move past the
	// heartbeat horizon without waiting.
	now func() time.Time

	// heartbeatInterval is how often the owner renews heartbeat_at while a
	// run executes; aeoRunHeartbeatInterval unless a test overrides it.
	heartbeatInterval time.Duration

	// lifecycle owns every run this service starts: admission, the shared run
	// context and the join at shutdown. See aeo_runtime.go.
	lifecycle *lifecycle.Owner

	// providerStatuses is the full engine roster, including the ones with no
	// credentials. The service cannot derive it from providers — an engine with
	// no key is simply absent there — so main.go supplies it.
	providerStatuses []models.AEOProviderStatus

	// configSource, when set, returns the AEO configuration in force right
	// now — environment values with the administrator-stored keys overlaid.
	// The service consults it instead of its boot-time providers and statuses
	// so a key entered in the UI takes effect on the next run without a
	// restart. It is assigned once at construction and only read afterwards.
	configSource func() config.AEOConfig

	// generationEngineSource, when set, names the engine prompt generation
	// runs on (admin-configurable). Empty answers fall back to Anthropic.
	generationEngineSource func() string
}

// AEOServiceOption customizes the service at construction time.
type AEOServiceOption func(*aeoService)

// WithAEOProviderStatuses supplies the full engine roster reported by
// GET /aeo/providers, so the settings page can show which keys are still
// missing rather than only the engines that already work. Without it the
// service falls back to reporting just the configured engines.
func WithAEOProviderStatuses(statuses []models.AEOProviderStatus) AEOServiceOption {
	return func(s *aeoService) { s.providerStatuses = statuses }
}

// WithAEOGenerationEngineSource makes prompt generation run on the engine the
// source names (a provider name such as "gemini") instead of the built-in
// Anthropic default. An empty or errored answer falls back to the default, so
// a missing configuration row can never break generation for a deployment
// that relies on environment keys.
func WithAEOGenerationEngineSource(source func() string) AEOServiceOption {
	return func(s *aeoService) { s.generationEngineSource = source }
}

// WithAEOConfigSource makes the service resolve its engines from the current
// configuration rather than from the set handed to the constructor.
//
// The source is called on every run creation — manual and scheduled alike — and
// on every provider-status read, so a provider key stored through the
// configuration API is in force from the next run onwards, without restarting
// the process. Without this option the service keeps using the providers and
// statuses it was constructed with.
func WithAEOConfigSource(source func() config.AEOConfig) AEOServiceOption {
	return func(s *aeoService) { s.configSource = source }
}

// currentProviders returns the engines that have credentials right now. Without
// a configuration source it is the boot-time set.
func (s *aeoService) currentProviders() []aeo.Provider {
	if s.configSource == nil {
		return s.providers
	}
	return aeo.LoadProvidersFor(s.configSource())
}

// currentExecutor binds the executor to the engines a run is about to use. An
// executor that cannot be rebound — a test double — is returned unchanged, so
// the provider set it was built with stays in force.
func (s *aeoService) currentExecutor(providers []aeo.Provider) aeo.Executor {
	if s.configSource == nil {
		return s.executor
	}
	if rebindable, ok := s.executor.(aeo.ProviderSetExecutor); ok {
		return rebindable.WithProviders(providers)
	}
	return s.executor
}

// NewAEOService wires the AEO service. providers is the ordered list of engines
// that actually have credentials — an empty list makes StartRun fail fast with
// apperrors.ErrNoProvidersConfigured instead of recording a run that can never
// produce an answer.
//
// The service owns the runs it starts; a process that must drain them at
// shutdown wires NewAEORuntime instead, which exposes the same service plus
// Shutdown.
func NewAEOService(repo repository.AEORepository, executor aeo.Executor, providers []aeo.Provider, txManager repository.TransactionManager, opts ...AEOServiceOption) AEOService {
	return newAEOService(repo, executor, providers, txManager, opts...)
}

func newAEOService(repo repository.AEORepository, executor aeo.Executor, providers []aeo.Provider, txManager repository.TransactionManager, opts ...AEOServiceOption) *aeoService {
	s := &aeoService{
		repo:              repo,
		executor:          executor,
		providers:         providers,
		txManager:         txManager,
		lifecycle:         newAEORunOwner(),
		instanceID:        newAEOInstanceID(),
		now:               time.Now,
		heartbeatInterval: aeoRunHeartbeatInterval,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithAEOInstanceID replaces the generated instance id. Tests use it to give
// two services on one database recognisable owners; an empty id keeps the
// generated one.
func WithAEOInstanceID(id string) AEOServiceOption {
	return func(s *aeoService) {
		if strings.TrimSpace(id) != "" {
			s.instanceID = id
		}
	}
}

// WithAEOClock replaces the service clock used for started_at, heartbeats and
// the abandoned-run horizons. A nil clock keeps time.Now.
func WithAEOClock(now func() time.Time) AEOServiceOption {
	return func(s *aeoService) {
		if now != nil {
			s.now = now
		}
	}
}

// WithAEOHeartbeatInterval replaces aeoRunHeartbeatInterval. A non-positive
// interval keeps the default.
func WithAEOHeartbeatInterval(interval time.Duration) AEOServiceOption {
	return func(s *aeoService) {
		if interval > 0 {
			s.heartbeatInterval = interval
		}
	}
}

// newAEOInstanceID returns 32 lowercase hex characters from crypto/rand. A
// failing random source is not survivable for ownership, so it panics at
// construction rather than handing two processes the same id.
func newAEOInstanceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("aeo: cannot generate an instance id: %v", err))
	}
	return hex.EncodeToString(raw[:])
}

// clock returns the service clock in UTC.
func (s *aeoService) clock() time.Time {
	return s.now().UTC()
}

func (s *aeoService) Providers() []models.AEOProviderStatus {
	// Resolved on every read: the settings page is how an administrator sees
	// a key they just stored take effect.
	if s.configSource != nil {
		return aeo.ProviderStatusesFor(s.configSource())
	}
	if s.providerStatuses != nil {
		return s.providerStatuses
	}

	statuses := make([]models.AEOProviderStatus, 0, len(s.providers))
	for _, provider := range s.providers {
		if provider == nil {
			continue
		}
		statuses = append(statuses, models.AEOProviderStatus{
			Name:       provider.Name(),
			Model:      provider.Model(),
			Configured: true,
		})
	}
	return statuses
}
