package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const (
	// aeoMaxActivePrompts caps how many prompts a run may fan out over. A run
	// costs active prompts x configured providers external calls, so the cap is
	// a cost guard as much as a data-volume one. Not configurable in v1.
	aeoMaxActivePrompts = 100

	// aeoPromptTextMaxLength mirrors the varchar(500) column. Rejecting here
	// turns a driver truncation error into a plain validation failure.
	aeoPromptTextMaxLength = 500

	// aeoBrandNameMaxLength and aeoDescriptionMaxLength mirror the profile
	// columns (varchar(120) and TEXT, the latter capped to keep the prompt
	// generation payload sane).
	aeoBrandNameMaxLength   = 120
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

	// ErrAEOInvalidProfile covers a missing or over-long brand name and an
	// over-long description.
	ErrAEOInvalidProfile = errors.New("brand name is required")

	// ErrAEOInvalidTrigger guards the aeo_runs.trigger column against values
	// the dashboard cannot interpret.
	ErrAEOInvalidTrigger = errors.New("run trigger must be manual or scheduled")
)

type aeoService struct {
	repo      repository.AEORepository
	executor  aeo.Executor
	providers []aeo.Provider
	txManager repository.TransactionManager

	// startMu serializes the overlap guard with the run insert. Counting
	// running rows and then inserting one are two statements: without this,
	// two concurrent StartRun calls — a manual POST landing on the scheduled
	// hour is the realistic case — can both read zero and both start a run,
	// doubling provider spend. A mutex is the right scope here because the
	// executor and the scheduler are both in-process: the module already
	// assumes a single API process (two replicas would each run their own
	// scheduler and fire their own daily run), so a cross-process lock would
	// be solving a problem this deployment does not have. Horizontal scaling
	// would need a database-level claim, and that is called out in the spec.
	startMu sync.Mutex

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
func NewAEOService(repo repository.AEORepository, executor aeo.Executor, providers []aeo.Provider, txManager repository.TransactionManager, opts ...AEOServiceOption) AEOService {
	s := &aeoService{
		repo:      repo,
		executor:  executor,
		providers: providers,
		txManager: txManager,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ---------------------------------------------------------------- profile ---

func (s *aeoService) GetProfile() (*models.AEOProfile, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_profile"), "AEOService", "GetProfile")

	profile, err := s.repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			logger.Debug("AEO profile is not configured yet")
			return nil, apperrors.ErrProfileNotConfigured
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return profile, nil
}

func (s *aeoService) SaveProfile(profile *models.AEOProfile) (*models.AEOProfile, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_profile"), "AEOService", "SaveProfile")

	if profile == nil {
		return nil, ErrAEOInvalidProfile
	}
	if err := normalizeAEOProfile(profile); err != nil {
		logger.WithError(err).Warn("Invalid AEO profile")
		return nil, err
	}

	if err := s.repo.UpsertProfile(profile); err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.WithField("brand_name", profile.BrandName).Info("AEO profile saved")
	return profile, nil
}

// ---------------------------------------------------------------- prompts ---

func (s *aeoService) ListPrompts(from, to time.Time, activeOnly bool, offset, limit int, sortBy, sortOrder string) ([]models.AEOPrompt, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_prompt"), "AEOService", "ListPrompts")

	prompts, err := s.repo.ListPrompts(activeOnly, offset, limit, sortBy, sortOrder)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if prompts == nil {
		prompts = []models.AEOPrompt{}
	}

	total, err := s.repo.CountPrompts(activeOnly)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	// Visibility is a property of the requested window, never of the row, so it
	// is recomputed on every listing rather than denormalized onto the table.
	if len(prompts) > 0 {
		ids := make([]uint, 0, len(prompts))
		for i := range prompts {
			ids = append(ids, prompts[i].ID)
		}

		stats, err := s.repo.PromptVisibility(from, to, ids)
		if err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, 0, err
		}
		for i := range prompts {
			stat := stats[prompts[i].ID]
			prompts[i].AnswerCount = stat.Answers
			prompts[i].MentionCount = stat.Mentions
			prompts[i].LastRunAt = stat.LastRunAt
			prompts[i].Visibility = aeoPercent(stat.Mentions, stat.Answers)
		}
	}

	return prompts, total, nil
}

func (s *aeoService) CreatePrompts(texts []string, createdByID uint) ([]models.AEOPrompt, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_prompt"), "AEOService", "CreatePrompts")

	if len(texts) == 0 {
		return nil, fmt.Errorf("no prompts supplied: %w", ErrAEOInvalidPrompt)
	}

	// Normalize first so the duplicate checks — both within the batch and
	// against the database — compare exactly the strings that will be stored.
	normalized := make([]string, 0, len(texts))
	seen := make(map[string]bool, len(texts))
	for _, raw := range texts {
		text, err := normalizeAEOPromptText(raw)
		if err != nil {
			logger.WithError(err).Warn("Invalid AEO prompt text")
			return nil, err
		}
		key := strings.ToLower(text)
		if seen[key] {
			logger.Warn("Duplicate prompt text within the batch")
			return nil, fmt.Errorf("prompt %q is repeated in this request: %w", text, apperrors.ErrDuplicatePrompt)
		}
		seen[key] = true
		normalized = append(normalized, text)
	}

	var created []models.AEOPrompt
	var ownerID *uint
	if createdByID != 0 {
		id := createdByID
		ownerID = &id
	}

	// All-or-nothing: a batch that trips the cap or a duplicate on its last
	// entry must leave nothing behind. The cap and the duplicate checks run
	// inside the transaction so a concurrent create cannot slip past them.
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		txRepo := s.repo.WithTx(tx)

		active, err := txRepo.CountPrompts(true)
		if err != nil {
			return err
		}
		if active+int64(len(normalized)) > aeoMaxActivePrompts {
			return ErrAEOPromptLimit
		}

		for _, text := range normalized {
			exists, err := txRepo.ExistsByTextInsensitive(text, 0)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("prompt %q already exists: %w", text, apperrors.ErrDuplicatePrompt)
			}

			prompt := models.AEOPrompt{Text: text, IsActive: true, CreatedByID: ownerID}
			if err := txRepo.CreatePrompt(&prompt); err != nil {
				return err
			}
			created = append(created, prompt)
		}
		return nil
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.WithField("count", len(created)).Info("AEO prompts created")
	return created, nil
}

func (s *aeoService) UpdatePrompt(id uint, text *string, isActive *bool) (*models.AEOPrompt, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_prompt_id", id), "AEOService", "UpdatePrompt")

	prompt, err := s.repo.GetPromptByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	if text != nil {
		newText, err := normalizeAEOPromptText(*text)
		if err != nil {
			logger.WithError(err).Warn("Invalid AEO prompt text")
			return nil, err
		}
		// The prompt being edited is allowed to collide with itself: toggling
		// is_active while leaving the text alone is not a duplicate.
		exists, err := s.repo.ExistsByTextInsensitive(newText, id)
		if err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("prompt %q already exists: %w", newText, apperrors.ErrDuplicatePrompt)
		}
		prompt.Text = newText
	}

	if isActive != nil {
		// Reactivating counts against the cap; deactivating never can.
		if *isActive && !prompt.IsActive {
			active, err := s.repo.CountPrompts(true)
			if err != nil {
				utils.LogServiceResponse(logger, err)
				return nil, err
			}
			if active >= aeoMaxActivePrompts {
				return nil, ErrAEOPromptLimit
			}
		}
		prompt.IsActive = *isActive
	}

	if err := s.repo.UpdatePrompt(prompt); err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.Info("AEO prompt updated")
	return prompt, nil
}

func (s *aeoService) DeletePrompt(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_prompt_id", id), "AEOService", "DeletePrompt")

	if _, err := s.repo.GetPromptByID(id); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	if err := s.repo.DeletePrompt(id); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("AEO prompt deleted")
	return nil
}

// GeneratePrompts asks the Anthropic engine for buyer-style questions derived
// from the brand profile. Nothing is stored: the caller reviews the suggestions
// and POSTs the ones worth tracking to /aeo/prompts.
//
// Generation deliberately runs on one named engine rather than "whatever is
// configured": the wording of the meta-prompt is tuned for it, and a silent
// fallback to another engine would change the shape of the output without the
// operator noticing. A missing key is therefore
// ErrGenerationProviderNotConfigured, which the handler reports as 503
// PROVIDER_NOT_CONFIGURED and which names the engine so the operator knows
// which key to add.
func (s *aeoService) GeneratePrompts(ctx context.Context, count int) ([]string, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("count", count), "AEOService", "GeneratePrompts")

	if count <= 0 {
		count = aeoDefaultGeneratedPrompts
	}
	if count > aeoMaxGeneratedPrompts {
		count = aeoMaxGeneratedPrompts
	}

	profile, err := s.repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			return nil, apperrors.ErrProfileNotConfigured
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	provider := s.generationProvider()
	if provider == nil {
		return nil, apperrors.ErrGenerationProviderNotConfigured
	}

	callCtx, cancel := context.WithTimeout(ctx, aeoGenerationTimeout)
	defer cancel()

	answer, err := provider.Query(callCtx, buildAEOGenerationPrompt(profile, count))
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	texts := parseAEOGeneratedPrompts(answer.Text, count)
	if len(texts) == 0 {
		err := fmt.Errorf("prompt generation returned no usable suggestions")
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.WithField("generated", len(texts)).Info("AEO prompts generated")
	return texts, nil
}

// generationProvider returns the engine used for prompt generation, or nil when
// it has no credentials.
func (s *aeoService) generationProvider() aeo.Provider {
	for _, p := range s.currentProviders() {
		if p != nil && p.Name() == aeo.ProviderAnthropic {
			return p
		}
	}
	return nil
}

// buildAEOGenerationPrompt renders the meta-prompt. It asks for a bare JSON
// array because that survives round-tripping far better than prose, but the
// parser tolerates a plain list as well.
func buildAEOGenerationPrompt(profile *models.AEOProfile, count int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "You are helping a company measure how often AI assistants mention it.\n")
	fmt.Fprintf(&b, "Brand: %s\n", profile.BrandName)
	if strings.TrimSpace(profile.Description) != "" {
		fmt.Fprintf(&b, "What it does: %s\n", strings.TrimSpace(profile.Description))
	}
	if len(profile.Competitors) > 0 {
		names := make([]string, 0, len(profile.Competitors))
		for _, c := range profile.Competitors {
			if strings.TrimSpace(c.Name) != "" {
				names = append(names, strings.TrimSpace(c.Name))
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "Competitors: %s\n", strings.Join(names, ", "))
		}
	}
	fmt.Fprintf(&b, "\nWrite %d distinct questions a prospective buyer would ask an AI assistant "+
		"when researching this category. The questions must be answerable without knowing the brand, "+
		"must not name %s, and must each be under %d characters.\n",
		count, profile.BrandName, aeoPromptTextMaxLength)
	fmt.Fprintf(&b, "Reply with a JSON array of strings and nothing else.")

	return b.String()
}

// parseAEOGeneratedPrompts turns a model reply into clean prompt texts. It
// accepts a JSON array (the requested shape) and falls back to one-per-line
// parsing, stripping bullets, numbering and quoting. Duplicates are dropped
// case-insensitively so the caller does not immediately hit ErrDuplicatePrompt.
func parseAEOGeneratedPrompts(raw string, limit int) []string {
	candidates := decodeAEOPromptArray(raw)
	if len(candidates) == 0 {
		candidates = strings.Split(raw, "\n")
	}

	seen := make(map[string]bool, len(candidates))
	texts := make([]string, 0, limit)
	for _, candidate := range candidates {
		text := cleanGeneratedPromptText(candidate)
		if text == "" || len([]rune(text)) > aeoPromptTextMaxLength {
			continue
		}
		key := strings.ToLower(text)
		if seen[key] {
			continue
		}
		seen[key] = true
		texts = append(texts, text)
		if len(texts) == limit {
			break
		}
	}
	return texts
}

// decodeAEOPromptArray extracts the outermost JSON array from a reply that may
// be wrapped in prose or a fenced code block. A decode failure is not an error:
// the caller falls back to line parsing.
func decodeAEOPromptArray(raw string) []string {
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
		return nil
	}
	return out
}

// cleanGeneratedPromptText strips list decoration from one candidate line and
// returns "" for anything too short to be a real question.
func cleanGeneratedPromptText(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.TrimLeft(text, "-*•\t ")
	// Leading "1." / "1)" numbering, but only when the digits are actually
	// followed by a separator: "5 best CRMs?" must survive intact.
	if idx := strings.IndexFunc(text, func(r rune) bool { return !unicode.IsDigit(r) }); idx > 0 {
		if rest := text[idx:]; strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, ")") {
			text = rest[1:]
		}
	}
	text = strings.TrimSpace(text)
	text = strings.Trim(text, `"'`)
	text = strings.TrimRight(text, ",")
	text = strings.TrimSpace(text)

	if len([]rune(text)) < aeoMinGeneratedPromptLength {
		return ""
	}
	return text
}

// ------------------------------------------------------------------- runs ---

func (s *aeoService) StartRun(ctx context.Context, trigger string, triggeredByID *uint) (*models.AEORun, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("trigger", trigger), "AEOService", "StartRun")

	trigger = strings.ToLower(strings.TrimSpace(trigger))
	if trigger == "" {
		trigger = aeoTriggerManual
	}
	if trigger != aeoTriggerManual && trigger != aeoTriggerScheduled {
		return nil, ErrAEOInvalidTrigger
	}

	// Order matters: the caller gets the most actionable reason first. A missing
	// profile means mention detection has nothing to look for, so it outranks
	// "no providers", which outranks the overlap guard.
	profile, err := s.repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			return nil, apperrors.ErrProfileNotConfigured
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	// Credentials are administrator-editable, so the engine set is resolved
	// here rather than reused from boot: a key stored since the process
	// started is in force from this run on.
	providers := s.currentProviders()
	if len(providers) == 0 {
		return nil, apperrors.ErrNoProvidersConfigured
	}
	if s.executor == nil {
		return nil, apperrors.ErrNoProvidersConfigured
	}
	executor := s.currentExecutor(providers)

	// The guard, the prompt read and the insert are one critical section: see
	// startMu. Sweeping stale rows first means a run stranded by a crash
	// unblocks itself once it is older than aeoRunStaleAfter, rather than
	// requiring a hand-written UPDATE against the database.
	s.startMu.Lock()
	defer s.startMu.Unlock()

	if swept, err := s.repo.MarkStaleRunsFailed(time.Now().UTC().Add(-aeoRunStaleAfter)); err != nil {
		// A failed sweep is not a reason to refuse the run: fall through to the
		// guard, which is still correct, just less forgiving.
		logger.WithError(err).Warn("Could not sweep stale AEO runs")
	} else if swept > 0 {
		logger.WithField("stale_runs", swept).Warn("Failed AEO runs left running by an earlier process")
	}

	running, err := s.repo.CountRunsByStatus(aeoRunStatusRunning)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	if running > 0 {
		logger.Warn("Refusing to start an overlapping AEO run")
		return nil, apperrors.ErrRunInProgress
	}

	prompts, err := s.repo.ListActivePrompts()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	if len(prompts) == 0 {
		return nil, fmt.Errorf("no active AEO prompts: %w", apperrors.ErrNotFound)
	}

	run := &models.AEORun{
		Trigger:       trigger,
		Status:        aeoRunStatusRunning,
		StartedAt:     time.Now().UTC(),
		TotalQueries:  len(prompts) * len(providers),
		TriggeredByID: triggeredByID,
	}
	if err := s.repo.CreateRun(run); err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	// The run outlives the HTTP request that asked for it, so the executor gets
	// a fresh background context. Cancelling the request must not abort a run
	// that is already writing answers.
	go executor.Execute(context.Background(), run, prompts, profile)

	logger.WithFields(map[string]interface{}{
		"run_id":        run.ID,
		"total_queries": run.TotalQueries,
	}).Info("AEO run started")
	return run, nil
}

// ReconcileRunningRuns fails every run still marked "running" and returns how
// many it recovered. It is meant to be called once at startup, before the
// scheduler is armed.
//
// The engine runs inside this process and writes the terminal status itself, so
// a "running" row observed at boot cannot belong to a live run: its executor
// died with the previous process (deploy, OOM, docker restart, or a SIGTERM —
// graceful shutdown drains HTTP, it does not wait for a run that may still have
// hours of provider calls left). Left alone, that row makes the overlap guard
// reject every manual run with 409 and skip every scheduled run, forever.
//
// This assumes one API process, which is the same assumption the daily
// scheduler already makes; a second replica would need a database-level claim
// instead.
func (s *aeoService) ReconcileRunningRuns() (int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_run"), "AEOService", "ReconcileRunningRuns")

	s.startMu.Lock()
	defer s.startMu.Unlock()

	recovered, err := s.repo.MarkStaleRunsFailed(time.Now().UTC())
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return 0, err
	}
	if recovered > 0 {
		logger.WithField("recovered_runs", recovered).
			Warn("Marked AEO runs stranded by a previous process as failed")
	}
	return recovered, nil
}

func (s *aeoService) ListRuns(offset, limit int, sortBy, sortOrder string) ([]models.AEORun, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_run"), "AEOService", "ListRuns")

	runs, err := s.repo.ListRuns(offset, limit, sortBy, sortOrder)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if runs == nil {
		runs = []models.AEORun{}
	}

	total, err := s.repo.CountRuns()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	return runs, total, nil
}

func (s *aeoService) GetRun(id uint) (*models.AEORun, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_run_id", id), "AEOService", "GetRun")

	run, err := s.repo.GetRunByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("aeo run %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return run, nil
}

func (s *aeoService) GetPromptAnswers(promptID uint, runID *uint, offset, limit int) ([]models.AEOAnswer, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_prompt_id", promptID), "AEOService", "GetPromptAnswers")

	// An unknown prompt is a 404, not an empty transcript.
	if _, err := s.repo.GetPromptByID(promptID); err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("aeo prompt %d not found: %w", promptID, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	answers, err := s.repo.ListAnswersByPrompt(promptID, runID, offset, limit)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if answers == nil {
		answers = []models.AEOAnswer{}
	}

	total, err := s.repo.CountAnswersByPrompt(promptID, runID)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	return answers, total, nil
}

// ---------------------------------------------------------------- metrics ---

// aeoCounter accumulates one bucket of the visibility arithmetic: how many
// scored (non-errored) answers landed in it and how many of those mentioned the
// brand.
type aeoCounter struct {
	answers  int64
	mentions int64
}

func (s *aeoService) Dashboard(from, to time.Time) (*models.AEODashboard, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_dashboard"), "AEOService", "Dashboard")

	from, to = normalizeAEORange(from, to)

	// One projection query for the whole window; every bucket below is cut in
	// Go. No date function is spelled the same on MySQL 8 and SQLite, which is
	// why the repository never groups by day itself.
	facts, err := s.repo.ListAnswerFacts(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	profile, err := s.profileOrNil()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	brandName := ""
	if profile != nil {
		brandName = profile.BrandName
	}
	companies := aeoCompanyNames(profile, facts)

	dashboard := &models.AEODashboard{
		From:               from.Format(aeoDayFormat),
		To:                 to.Format(aeoDayFormat),
		Days:               aeoRangeDays(from, to),
		ByProvider:         []models.AEOProviderVisibility{},
		Timeline:           []models.AEOTimelinePoint{},
		ShareOfVoice:       []models.AEOShareOfVoiceEntry{},
		CompetitorTimeline: []models.AEOCompetitorTimelinePoint{},
	}

	byProvider := map[string]*aeoCounter{}
	byDay := map[string]*aeoCounter{}
	byDayProvider := map[string]map[string]*aeoCounter{}
	byCompany := map[string]int64{}
	byDayCompany := map[string]map[string]int64{}
	var scored int64

	for _, fact := range facts {
		dashboard.TotalAnswers++
		if fact.Errored {
			// A failed provider call is recorded so the run history is honest,
			// but it is not evidence of absence: it never enters a percentage.
			dashboard.FailedAnswers++
			continue
		}
		scored++

		day := fact.CreatedAt.UTC().Format(aeoDayFormat)
		aeoBump(byDay, day, fact.BrandMentioned)
		aeoBump(byProvider, fact.Provider, fact.BrandMentioned)
		if byDayProvider[day] == nil {
			byDayProvider[day] = map[string]*aeoCounter{}
		}
		aeoBump(byDayProvider[day], fact.Provider, fact.BrandMentioned)

		if byDayCompany[day] == nil {
			byDayCompany[day] = map[string]int64{}
		}
		// Mention events are counted once per answer per company, for the brand
		// and competitors alike. Counting raw occurrences would let one verbose
		// answer dominate the share-of-voice table.
		if fact.BrandMentioned && brandName != "" {
			dashboard.BrandMentions++
			byCompany[brandName]++
			byDayCompany[day][brandName]++
		}
		for name, count := range fact.CompetitorMentions {
			if count <= 0 || name == brandName {
				continue
			}
			byCompany[name]++
			byDayCompany[day][name]++
		}
	}

	// BrandMentions must stay meaningful even before a profile exists, where
	// there is no brand name to key the share-of-voice map by.
	if brandName == "" {
		for _, fact := range facts {
			if !fact.Errored && fact.BrandMentioned {
				dashboard.BrandMentions++
			}
		}
	}

	dashboard.Visibility = aeoPercent(dashboard.BrandMentions, scored)

	providerNames := make([]string, 0, len(byProvider))
	for name := range byProvider {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		counter := byProvider[name]
		dashboard.ByProvider = append(dashboard.ByProvider, models.AEOProviderVisibility{
			Provider:   name,
			Answers:    counter.answers,
			Mentions:   counter.mentions,
			Visibility: aeoPercent(counter.mentions, counter.answers),
		})
	}

	// Every day in range gets a point, including the ones with no answers, so
	// the chart shows the gap instead of interpolating across it.
	for day := aeoTruncateDay(from); day.Before(to); day = day.AddDate(0, 0, 1) {
		key := day.Format(aeoDayFormat)

		point := models.AEOTimelinePoint{Day: key, ByProvider: map[string]float64{}}
		if counter := byDay[key]; counter != nil {
			point.Overall = aeoPercent(counter.mentions, counter.answers)
		}
		for name, counter := range byDayProvider[key] {
			point.ByProvider[name] = aeoPercent(counter.mentions, counter.answers)
		}
		dashboard.Timeline = append(dashboard.Timeline, point)

		competitorPoint := models.AEOCompetitorTimelinePoint{Day: key, ByCompany: map[string]float64{}}
		if counter := byDay[key]; counter != nil && counter.answers > 0 {
			for _, company := range companies {
				competitorPoint.ByCompany[company] = aeoPercent(byDayCompany[key][company], counter.answers)
			}
		}
		dashboard.CompetitorTimeline = append(dashboard.CompetitorTimeline, competitorPoint)
	}

	var mentionEvents int64
	for _, company := range companies {
		mentionEvents += byCompany[company]
	}
	for _, company := range companies {
		mentions := byCompany[company]
		dashboard.ShareOfVoice = append(dashboard.ShareOfVoice, models.AEOShareOfVoiceEntry{
			Company:    company,
			IsBrand:    brandName != "" && company == brandName,
			Mentions:   mentions,
			Share:      aeoPercent(mentions, mentionEvents),
			Visibility: aeoPercent(mentions, scored),
		})
	}

	lastRunAt, err := s.lastRunAt()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	dashboard.LastRunAt = lastRunAt

	return dashboard, nil
}

func (s *aeoService) Citations(from, to time.Time) (*models.AEOCitationsReport, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_citations"), "AEOService", "Citations")

	from, to = normalizeAEORange(from, to)

	totalAnswers, _, err := s.repo.CountAnswersInRange(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	answersWithCitations, err := s.repo.CountAnswersWithCitations(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	rows, err := s.repo.CitationDomainStats(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	profile, err := s.profileOrNil()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	report := &models.AEOCitationsReport{
		From:                 from.Format(aeoDayFormat),
		To:                   to.Format(aeoDayFormat),
		TotalAnswers:         totalAnswers,
		AnswersWithCitations: answersWithCitations,
		ByCompany:            []models.AEOCitationCompanyStat{},
		TopDomains:           []models.AEOCitationDomainStat{},
	}

	brandName := ""
	if profile != nil {
		brandName = profile.BrandName
	}

	// Rates are shares of all citations in the window: the aggregation rows are
	// per-domain totals, so "citations to this company / citations to anybody"
	// is the only ratio the data supports without a second scan.
	var ownedCitations int64
	perCompany := map[string]*models.AEOCitationCompanyStat{}
	for _, row := range rows {
		report.TotalCitations += row.Citations
		if row.IsOwned {
			ownedCitations += row.Citations
		}

		company := aeoCitationCompany(row, brandName)
		if company == "" {
			// A domain that belongs to neither the brand nor a tracked
			// competitor still counts towards the totals and can still surface
			// in top_domains; it just has no company row to fold into.
			continue
		}
		stat := perCompany[company]
		if stat == nil {
			stat = &models.AEOCitationCompanyStat{Company: company, IsBrand: row.IsOwned}
			perCompany[company] = stat
		}
		stat.Citations += row.Citations
		stat.WithBrandMention += row.WithBrandMention
	}

	report.OwnedCitationRate = aeoPercent(ownedCitations, report.TotalCitations)

	// Brand first, then every tracked competitor in profile order — including
	// the ones with no citations at all, which is exactly the comparison the
	// Citations page is for.
	for _, company := range aeoCompanyNames(profile, nil) {
		stat := perCompany[company]
		if stat == nil {
			stat = &models.AEOCitationCompanyStat{Company: company, IsBrand: company == brandName && brandName != ""}
		}
		stat.IsBrand = company == brandName && brandName != ""
		stat.CitationRate = aeoPercent(stat.Citations, report.TotalCitations)
		stat.BrandMentionRate = aeoPercent(stat.WithBrandMention, stat.Citations)
		report.ByCompany = append(report.ByCompany, *stat)
	}

	domains := make([]models.AEOCitationDomainStat, 0, len(rows))
	for _, row := range rows {
		domains = append(domains, models.AEOCitationDomainStat{
			Domain:           row.Domain,
			Company:          aeoCitationCompany(row, brandName),
			IsOwned:          row.IsOwned,
			Citations:        row.Citations,
			CitationRate:     aeoPercent(row.Citations, report.TotalCitations),
			WithBrandMention: row.WithBrandMention,
			BrandMentionRate: aeoPercent(row.WithBrandMention, row.Citations),
		})
	}
	sort.SliceStable(domains, func(i, j int) bool {
		if domains[i].Citations != domains[j].Citations {
			return domains[i].Citations > domains[j].Citations
		}
		return domains[i].Domain < domains[j].Domain
	})
	if len(domains) > aeoTopDomainLimit {
		domains = domains[:aeoTopDomainLimit]
	}
	report.TopDomains = domains

	return report, nil
}

// aeoTopDomainLimit bounds the citations table so a long tail of one-off
// domains cannot bloat the response.
const aeoTopDomainLimit = 20

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

// ---------------------------------------------------------------- helpers ---

// profileOrNil returns the brand profile, or nil when none has been configured.
// The metrics endpoints must still answer before setup: an empty dashboard is a
// better first-run experience than an error page.
func (s *aeoService) profileOrNil() (*models.AEOProfile, error) {
	profile, err := s.repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return profile, nil
}

// lastRunAt reports when the most recent run finished, falling back to when it
// started for a run that is still going.
func (s *aeoService) lastRunAt() (*time.Time, error) {
	run, err := s.repo.GetLatestRun()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if run == nil {
		return nil, nil
	}
	if run.CompletedAt != nil {
		return run.CompletedAt, nil
	}
	startedAt := run.StartedAt
	return &startedAt, nil
}

func aeoBump(buckets map[string]*aeoCounter, key string, mentioned bool) {
	counter := buckets[key]
	if counter == nil {
		counter = &aeoCounter{}
		buckets[key] = counter
	}
	counter.answers++
	if mentioned {
		counter.mentions++
	}
}

// aeoCompanyNames lists the companies a comparison table covers: the brand
// first, then each competitor in profile order. Before a profile exists it
// falls back to whatever competitor names the answers themselves carry, so the
// dashboard is not blank for data collected under an older profile.
func aeoCompanyNames(profile *models.AEOProfile, facts []models.AEOAnswerFact) []string {
	companies := make([]string, 0, 8)
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		companies = append(companies, name)
	}

	if profile != nil {
		add(profile.BrandName)
		for _, competitor := range profile.Competitors {
			add(competitor.Name)
		}
		return companies
	}

	extra := make([]string, 0, 8)
	for _, fact := range facts {
		for name, count := range fact.CompetitorMentions {
			if count > 0 {
				extra = append(extra, name)
			}
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		add(name)
	}
	return companies
}

// aeoCitationCompany attributes a citation row to a company: an owned domain is
// the brand, a domain carrying a competitor name is that competitor, and
// anything else is unattributed.
func aeoCitationCompany(row models.AEOCitationAggRow, brandName string) string {
	if row.IsOwned {
		return brandName
	}
	return strings.TrimSpace(row.CompetitorName)
}

// normalizeAEORange bounds a metrics window. `from` is inclusive and `to` is
// exclusive; a window wider than aeoMaxRangeDays keeps its end and loses its
// oldest days, and a degenerate window is widened to a single day so the
// per-day arithmetic always has at least one bucket.
func normalizeAEORange(from, to time.Time) (time.Time, time.Time) {
	from = from.UTC()
	to = to.UTC()

	if !to.After(from) {
		to = from.Add(24 * time.Hour)
	}
	maxWidth := time.Duration(aeoMaxRangeDays) * 24 * time.Hour
	if to.Sub(from) > maxWidth {
		from = to.Add(-maxWidth)
	}
	return from, to
}

func aeoRangeDays(from, to time.Time) int {
	days := int(math.Round(to.Sub(from).Hours() / 24))
	if days < 1 {
		days = 1
	}
	return days
}

func aeoTruncateDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// aeoPercent is the single place a ratio becomes a percentage: zero
// denominators yield 0 rather than NaN, and every result carries one decimal.
func aeoPercent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(part)/float64(total)*1000) / 10
}

func normalizeAEOPromptText(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ErrAEOInvalidPrompt
	}
	if len([]rune(text)) > aeoPromptTextMaxLength {
		return "", fmt.Errorf("prompt text is longer than %d characters: %w", aeoPromptTextMaxLength, ErrAEOInvalidPrompt)
	}
	return text, nil
}

// normalizeAEOProfile trims and de-duplicates the profile in place so the row
// that is stored is exactly the row that was validated, and pins the singleton
// primary key.
func normalizeAEOProfile(profile *models.AEOProfile) error {
	profile.BrandName = strings.TrimSpace(profile.BrandName)
	if profile.BrandName == "" {
		return ErrAEOInvalidProfile
	}
	if len([]rune(profile.BrandName)) > aeoBrandNameMaxLength {
		return fmt.Errorf("brand name is longer than %d characters: %w", aeoBrandNameMaxLength, ErrAEOInvalidProfile)
	}

	profile.Description = strings.TrimSpace(profile.Description)
	if len([]rune(profile.Description)) > aeoDescriptionMaxLength {
		return fmt.Errorf("description is longer than %d characters: %w", aeoDescriptionMaxLength, ErrAEOInvalidProfile)
	}

	profile.BrandAliases = aeoDedupeStrings(profile.BrandAliases, false)
	profile.OwnedDomains = aeoDedupeStrings(profile.OwnedDomains, true)

	competitors := make([]models.AEOCompetitor, 0, len(profile.Competitors))
	for _, competitor := range profile.Competitors {
		competitor.Name = strings.TrimSpace(competitor.Name)
		if competitor.Name == "" {
			continue
		}
		competitor.Aliases = aeoDedupeStrings(competitor.Aliases, false)
		competitor.Domain = aeoNormalizeProfileDomain(competitor.Domain)
		competitors = append(competitors, competitor)
	}
	profile.Competitors = competitors

	// Single row by construction: the service never inserts a second profile.
	profile.ID = aeoProfileID
	return nil
}

// aeoDedupeStrings trims, drops blanks and removes case-insensitive duplicates
// while preserving order. With asDomain set, each entry is also normalized to a
// bare lowercase host so "https://WWW.Acme.com/" and "acme.com" collapse.
func aeoDedupeStrings(values []string, asDomain bool) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if asDomain {
			value = aeoNormalizeProfileDomain(value)
		} else {
			value = strings.TrimSpace(value)
		}
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

// aeoNormalizeProfileDomain reduces whatever the user typed to a bare host:
// lowercase, no scheme, no path, no leading "www.". It accepts a bare domain,
// which url.Parse does not, so it is deliberately a string operation rather
// than a URL parse.
func aeoNormalizeProfileDomain(raw string) string {
	domain := strings.ToLower(strings.TrimSpace(raw))
	if domain == "" {
		return ""
	}
	if idx := strings.Index(domain, "://"); idx >= 0 {
		domain = domain[idx+3:]
	}
	if idx := strings.IndexAny(domain, "/?#"); idx >= 0 {
		domain = domain[:idx]
	}
	if idx := strings.Index(domain, "@"); idx >= 0 {
		domain = domain[idx+1:]
	}
	domain = strings.TrimPrefix(domain, "www.")
	return strings.Trim(domain, ".")
}
