package repository

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupAEOTestDB gives each test its own in-memory schema.
//
// The connection pool is pinned to a single connection on purpose: with the
// ":memory:" DSN every new connection gets its own empty database, so a second
// pooled connection would see none of the seeded rows. The AEO tests open
// transactions (CreateAnswerWithCitations), which is exactly the situation that
// tempts the pool into a second connection.
func setupAEOTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&models.AEOProfile{},
		&models.AEOPrompt{},
		&models.AEORun{},
		&models.AEOAnswer{},
		&models.AEOCitation{},
	))
	return db
}

// makeAEOPrompt seeds one prompt.
//
// An inactive prompt needs a second write: AEOPrompt.IsActive carries
// `gorm:"default:true"`, and GORM omits a zero-valued field from the INSERT when
// the column has a default, so Create(IsActive: false) silently stores true.
// Deactivation therefore has to go through an explicit column write (the
// service's own update path uses Save, which writes every column).
func makeAEOPrompt(t *testing.T, db *gorm.DB, text string, active bool) models.AEOPrompt {
	t.Helper()
	prompt := models.AEOPrompt{Text: text, IsActive: active}
	require.NoError(t, db.Create(&prompt).Error)
	if !active {
		require.NoError(t, db.Model(&prompt).UpdateColumn("is_active", false).Error)
		prompt.IsActive = false
	}
	return prompt
}

func makeAEORun(t *testing.T, db *gorm.DB, status string, startedAt time.Time) models.AEORun {
	t.Helper()
	run := models.AEORun{
		Trigger:   models.AEOTriggerManual,
		Status:    status,
		StartedAt: startedAt,
	}
	require.NoError(t, db.Create(&run).Error)
	return run
}

// --- profile -----------------------------------------------------------------

func TestAEORepository_GetProfile_NotConfigured(t *testing.T) {
	repo := NewAEORepository(setupAEOTestDB(t))

	profile, err := repo.GetProfile()

	assert.Nil(t, profile)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound),
		"an unconfigured profile must surface the not-found sentinel the service maps to 404")
}

func TestAEORepository_UpsertProfile_InsertsThenUpdatesTheSameRow(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	first := &models.AEOProfile{
		BaseModel:    models.BaseModel{ID: 1},
		BrandName:    "Acme",
		Description:  "Widgets",
		BrandAliases: []string{"Acme Inc"},
		OwnedDomains: []string{"acme.com"},
		Competitors:  []models.AEOCompetitor{{Name: "Globex", Aliases: []string{"Globex Corp"}, Domain: "globex.com"}},
	}
	require.NoError(t, repo.UpsertProfile(first))

	second := &models.AEOProfile{
		BaseModel:    models.BaseModel{ID: 1},
		BrandName:    "Acme Corporation",
		Description:  "Widgets and gadgets",
		BrandAliases: []string{"Acme Inc", "ACME"},
		OwnedDomains: []string{"acme.com", "acme.io"},
		Competitors:  []models.AEOCompetitor{{Name: "Initech", Domain: "initech.com"}},
	}
	require.NoError(t, repo.UpsertProfile(second))

	var count int64
	require.NoError(t, db.Model(&models.AEOProfile{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "the profile is a single row; the second save must not insert a second one")

	loaded, err := repo.GetProfile()
	require.NoError(t, err)
	assert.Equal(t, uint(1), loaded.ID)
	assert.Equal(t, "Acme Corporation", loaded.BrandName)
	assert.Equal(t, []string{"Acme Inc", "ACME"}, loaded.BrandAliases)
	assert.Equal(t, []string{"acme.com", "acme.io"}, loaded.OwnedDomains)
	require.Len(t, loaded.Competitors, 1)
	assert.Equal(t, "Initech", loaded.Competitors[0].Name)
}

// --- prompts -----------------------------------------------------------------

func TestAEORepository_PromptCRUD(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	prompt := &models.AEOPrompt{Text: "Which CRM should a small team pick?", IsActive: true}
	require.NoError(t, repo.CreatePrompt(prompt))
	require.NotZero(t, prompt.ID)

	loaded, err := repo.GetPromptByID(prompt.ID)
	require.NoError(t, err)
	assert.Equal(t, "Which CRM should a small team pick?", loaded.Text)
	assert.True(t, loaded.IsActive)

	loaded.IsActive = false
	require.NoError(t, repo.UpdatePrompt(loaded))

	reloaded, err := repo.GetPromptByID(prompt.ID)
	require.NoError(t, err)
	assert.False(t, reloaded.IsActive)

	require.NoError(t, repo.DeletePrompt(prompt.ID))

	_, err = repo.GetPromptByID(prompt.ID)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "the delete is a soft delete, so the row must stop being readable")
}

func TestAEORepository_DeletePrompt_MissingRowIsNotFound(t *testing.T) {
	repo := NewAEORepository(setupAEOTestDB(t))

	err := repo.DeletePrompt(4242)

	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound),
		"deleting a row that never existed must not report success")
}

func TestAEORepository_ListPrompts_FilterSortAndPaginate(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	alpha := makeAEOPrompt(t, db, "Alpha question", true)
	bravo := makeAEOPrompt(t, db, "Bravo question", true)
	charlie := makeAEOPrompt(t, db, "Charlie question", false)

	active, err := repo.ListPrompts(true, 0, 20, "text", "asc")
	require.NoError(t, err)
	require.Len(t, active, 2)
	assert.Equal(t, alpha.ID, active[0].ID)
	assert.Equal(t, bravo.ID, active[1].ID)

	all, err := repo.ListPrompts(false, 0, 20, "text", "desc")
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, charlie.ID, all[0].ID)

	page, err := repo.ListPrompts(false, 1, 1, "text", "asc")
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, bravo.ID, page[0].ID, "offset 1 limit 1 is the second row of the sorted set")

	// Sorting by the tie-breaker column itself must not emit "id asc, id asc".
	byID, err := repo.ListPrompts(false, 0, 20, "id", "desc")
	require.NoError(t, err)
	require.Len(t, byID, 3)
	assert.Equal(t, charlie.ID, byID[0].ID)

	total, err := repo.CountPrompts(false)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)

	activeTotal, err := repo.CountPrompts(true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), activeTotal)
}

func TestAEORepository_ListPrompts_DefaultsToNewestFirst(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	older := makeAEOPrompt(t, db, "Older", true)
	newer := makeAEOPrompt(t, db, "Newer", true)
	base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	stampTimes(t, db, &older, base, base)
	stampTimes(t, db, &newer, base.Add(time.Hour), base.Add(time.Hour))

	prompts, err := repo.ListPrompts(false, 0, 20, "", "")
	require.NoError(t, err)
	require.Len(t, prompts, 2)
	assert.Equal(t, newer.ID, prompts[0].ID, "an empty sortBy means created_at desc, like utils.ValidateSort")
}

func TestAEORepository_ListPrompts_RejectsUnknownSortColumn(t *testing.T) {
	repo := NewAEORepository(setupAEOTestDB(t))

	prompts, err := repo.ListPrompts(false, 0, 20, "text); DROP TABLE aeo_prompts;--", "asc")

	assert.Nil(t, prompts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid sort column",
		"an unlisted column must be refused before any ORDER BY is built")
}

func TestAEORepository_ListRuns_RejectsUnknownSortColumn(t *testing.T) {
	repo := NewAEORepository(setupAEOTestDB(t))

	runs, err := repo.ListRuns(0, 20, "started_at UNION SELECT", "asc")

	assert.Nil(t, runs)
	assert.Error(t, err)
}

func TestValidateAEOSort(t *testing.T) {
	cases := []struct {
		name      string
		allowed   map[string]bool
		sortBy    string
		sortOrder string
		wantCol   string
		wantOrder string
		wantErr   bool
	}{
		{"empty falls back", aeoPromptSortColumns, "", "", "created_at", "desc", false},
		{"empty ignores the order", aeoPromptSortColumns, "", "asc", "created_at", "desc", false},
		{"allowed column keeps asc", aeoPromptSortColumns, "text", "asc", "text", "asc", false},
		{"order is case-insensitive", aeoPromptSortColumns, "text", "ASC", "text", "asc", false},
		{"nonsense order degrades to desc", aeoPromptSortColumns, "is_active", "sideways", "is_active", "desc", false},
		{"unlisted column errors", aeoPromptSortColumns, "password", "asc", "", "", true},
		{"run column is not a prompt column", aeoPromptSortColumns, "started_at", "asc", "", "", true},
		{"run allowlist accepts started_at", aeoRunSortColumns, "started_at", "asc", "started_at", "asc", false},
		{"run allowlist rejects text", aeoRunSortColumns, "text", "asc", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			col, order, err := validateAEOSort(tc.allowed, tc.sortBy, tc.sortOrder)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantCol, col)
			assert.Equal(t, tc.wantOrder, order)
		})
	}
}

func TestAEORepository_ExistsByTextInsensitive(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	existing := makeAEOPrompt(t, db, "Which CRM is best for startups?", true)

	hit, err := repo.ExistsByTextInsensitive("WHICH crm IS best FOR startups?", 0)
	require.NoError(t, err)
	assert.True(t, hit, "the pre-check is case-insensitive so MySQL and SQLite agree")

	miss, err := repo.ExistsByTextInsensitive("Something else entirely", 0)
	require.NoError(t, err)
	assert.False(t, miss)

	self, err := repo.ExistsByTextInsensitive("Which CRM is best for startups?", existing.ID)
	require.NoError(t, err)
	assert.False(t, self, "an update is allowed to collide with the row it is updating")

	require.NoError(t, repo.DeletePrompt(existing.ID))
	afterDelete, err := repo.ExistsByTextInsensitive("Which CRM is best for startups?", 0)
	require.NoError(t, err)
	assert.False(t, afterDelete, "a soft-deleted prompt must not reserve its text forever")
}

func TestAEORepository_ListActivePrompts(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	first := makeAEOPrompt(t, db, "First", true)
	makeAEOPrompt(t, db, "Paused", false)
	second := makeAEOPrompt(t, db, "Second", true)

	prompts, err := repo.ListActivePrompts()

	require.NoError(t, err)
	require.Len(t, prompts, 2)
	assert.Equal(t, first.ID, prompts[0].ID)
	assert.Equal(t, second.ID, prompts[1].ID)
}

// --- runs --------------------------------------------------------------------

func TestAEORepository_RunLifecycle(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	base := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)

	run := &models.AEORun{Trigger: models.AEOTriggerScheduled, Status: models.AEORunStatusRunning, StartedAt: base}
	require.NoError(t, repo.CreateRun(run))
	require.NotZero(t, run.ID)

	running, err := repo.CountRunsByStatus(models.AEORunStatusRunning)
	require.NoError(t, err)
	assert.Equal(t, int64(1), running, "the overlap guard counts on this")

	completedAt := base.Add(4 * time.Minute)
	run.Status = models.AEORunStatusPartial
	run.CompletedAt = &completedAt
	run.TotalQueries = 10
	run.FailedQueries = 2
	require.NoError(t, repo.UpdateRun(run))

	loaded, err := repo.GetRunByID(run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AEORunStatusPartial, loaded.Status)
	assert.Equal(t, 10, loaded.TotalQueries)
	assert.Equal(t, 2, loaded.FailedQueries)
	require.NotNil(t, loaded.CompletedAt)
	assert.True(t, loaded.CompletedAt.UTC().Equal(completedAt))

	stillRunning, err := repo.CountRunsByStatus(models.AEORunStatusRunning)
	require.NoError(t, err)
	assert.Equal(t, int64(0), stillRunning)
}

func TestAEORepository_ListRunsAndLatest(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	base := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)

	oldest := makeAEORun(t, db, models.AEORunStatusCompleted, base)
	middle := makeAEORun(t, db, models.AEORunStatusFailed, base.Add(24*time.Hour))
	newest := makeAEORun(t, db, models.AEORunStatusCompleted, base.Add(48*time.Hour))

	runs, err := repo.ListRuns(0, 2, "started_at", "desc")
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, newest.ID, runs[0].ID)
	assert.Equal(t, middle.ID, runs[1].ID)

	page2, err := repo.ListRuns(2, 2, "started_at", "desc")
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, oldest.ID, page2[0].ID)

	total, err := repo.CountRuns()
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)

	latest, err := repo.GetLatestRun()
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, newest.ID, latest.ID)
}

// TestAEORepository_MarkStaleRunsFailed is the recovery path for a run stranded
// by a crash or a restart: the executor is in-process, so a row still flagged
// running long after it started belongs to a process that is gone. Without this
// the overlap guard rejects every later run with 409 forever.
func TestAEORepository_MarkStaleRunsFailed(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	now := time.Now().UTC()

	stale := makeAEORun(t, db, models.AEORunStatusRunning, now.Add(-8*time.Hour))
	fresh := makeAEORun(t, db, models.AEORunStatusRunning, now.Add(-2*time.Minute))
	finished := makeAEORun(t, db, models.AEORunStatusCompleted, now.Add(-9*time.Hour))

	swept, err := repo.MarkStaleRunsFailed(now.Add(-6 * time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), swept)

	recovered, err := repo.GetRunByID(stale.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AEORunStatusFailed, recovered.Status)
	require.NotNil(t, recovered.CompletedAt, "a failed run must carry a completion timestamp")

	untouched, err := repo.GetRunByID(fresh.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AEORunStatusRunning, untouched.Status,
		"a run younger than the cutoff may still be alive")

	unchanged, err := repo.GetRunByID(finished.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AEORunStatusCompleted, unchanged.Status)

	// The guard reads this count; the whole point of the sweep is that it drops.
	running, err := repo.CountRunsByStatus(models.AEORunStatusRunning)
	require.NoError(t, err)
	assert.Equal(t, int64(1), running)
}

// A cutoff of "now" is what the startup reconciliation passes: at boot no run
// can legitimately be in flight, so every running row is debris.
func TestAEORepository_MarkStaleRunsFailed_StartupSweepClearsEverything(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	now := time.Now().UTC()

	makeAEORun(t, db, models.AEORunStatusRunning, now.Add(-3*time.Hour))
	makeAEORun(t, db, models.AEORunStatusRunning, now.Add(-1*time.Minute))

	swept, err := repo.MarkStaleRunsFailed(now)
	require.NoError(t, err)
	assert.Equal(t, int64(2), swept)

	running, err := repo.CountRunsByStatus(models.AEORunStatusRunning)
	require.NoError(t, err)
	assert.Equal(t, int64(0), running)
}

func TestAEORepository_MarkStaleRunsFailed_NothingToDo(t *testing.T) {
	repo := NewAEORepository(setupAEOTestDB(t))

	swept, err := repo.MarkStaleRunsFailed(time.Now().UTC())

	require.NoError(t, err, "an empty table is a state, not a failure")
	assert.Equal(t, int64(0), swept)
}

// TestAEORepository_ListRuns_SortByTriggerIsQuoted covers the reserved-word trap:
// aeo_runs.trigger is spelled the same as the MySQL keyword TRIGGER, so an
// unquoted "ORDER BY trigger desc" is error 1064 on MySQL 8 and MariaDB while
// SQLite accepts it happily. SQLite cannot reproduce the production failure, so
// the assertion is on the generated clause as well as on the returned rows.
func TestAEORepository_ListRuns_SortByTriggerIsQuoted(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	base := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)

	manual := makeAEORun(t, db, models.AEORunStatusCompleted, base)
	scheduled := makeAEORun(t, db, models.AEORunStatusCompleted, base.Add(time.Hour))
	require.NoError(t, db.Model(&models.AEORun{}).Where("id = ?", scheduled.ID).
		Update("trigger", models.AEOTriggerScheduled).Error)

	clause := aeoOrderClause("trigger", "desc")
	assert.Equal(t, "`trigger` desc, `id` desc", clause,
		"the reserved word must be quoted or MySQL rejects the statement with error 1064")

	runs, err := repo.ListRuns(0, 20, "trigger", "desc")
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, scheduled.ID, runs[0].ID)
	assert.Equal(t, manual.ID, runs[1].ID)
}

// TestAEOOrderClause_AlwaysQuotesIdentifiers pins the quoting for every
// allowlisted column, not just the one that happens to be reserved today.
func TestAEOOrderClause_AlwaysQuotesIdentifiers(t *testing.T) {
	assert.Equal(t, "`id` asc", aeoOrderClause("id", "asc"))
	assert.Equal(t, "`created_at` desc, `id` desc", aeoOrderClause("created_at", "desc"))

	for column := range aeoRunSortColumns {
		assert.Contains(t, aeoOrderClause(column, "asc"), "`"+column+"`")
	}
	for column := range aeoPromptSortColumns {
		assert.Contains(t, aeoOrderClause(column, "asc"), "`"+column+"`")
	}
}

func TestAEORepository_GetLatestRun_NoRunsIsNotAnError(t *testing.T) {
	repo := NewAEORepository(setupAEOTestDB(t))

	latest, err := repo.GetLatestRun()

	require.NoError(t, err, "a fresh install has no runs; that is a state, not a failure")
	assert.Nil(t, latest)
}

// --- answers and citations ----------------------------------------------------

func TestAEORepository_CreateAnswerWithCitations(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	prompt := makeAEOPrompt(t, db, "Which CRM?", true)
	run := makeAEORun(t, db, models.AEORunStatusRunning, time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC))

	answer := &models.AEOAnswer{
		RunID:              run.ID,
		PromptID:           prompt.ID,
		Provider:           "openai",
		Model:              "gpt-4o-mini",
		Attempt:            1,
		AnswerText:         "Acme is a solid pick, see acme.com.",
		BrandMentioned:     true,
		FirstMentionPos:    0,
		CompetitorMentions: map[string]int{"Globex": 2},
		LatencyMs:          812,
	}
	citations := []models.AEOCitation{
		{URL: "https://acme.com/pricing", Domain: "acme.com", IsOwned: true},
		{URL: "https://globex.com/crm", Domain: "globex.com", CompetitorName: "Globex"},
	}

	require.NoError(t, repo.CreateAnswerWithCitations(answer, citations))
	require.NotZero(t, answer.ID)

	loaded, err := repo.ListAnswersByPrompt(prompt.ID, nil, 0, 20)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, map[string]int{"Globex": 2}, loaded[0].CompetitorMentions,
		"the JSON TEXT column round-trips through the model hooks")
	require.Len(t, loaded[0].Citations, 2)
	for _, citation := range loaded[0].Citations {
		assert.Equal(t, answer.ID, citation.AnswerID, "the answer id is stamped onto every citation")
	}
}

func TestAEORepository_CreateAnswerWithCitations_RollsBackTheAnswerWhenACitationFails(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	prompt := makeAEOPrompt(t, db, "Which CRM?", true)
	run := makeAEORun(t, db, models.AEORunStatusRunning, time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC))

	existing := models.AEOCitation{AnswerID: 999, URL: "https://acme.com", Domain: "acme.com"}
	require.NoError(t, db.Create(&existing).Error)

	answer := &models.AEOAnswer{RunID: run.ID, PromptID: prompt.ID, Provider: "openai", Model: "gpt-4o-mini", Attempt: 1}
	// Re-using a primary key makes the citation insert fail on both drivers.
	citations := []models.AEOCitation{{
		BaseModel: models.BaseModel{ID: existing.ID},
		URL:       "https://globex.com",
		Domain:    "globex.com",
	}}

	err := repo.CreateAnswerWithCitations(answer, citations)

	require.Error(t, err)
	var orphans int64
	require.NoError(t, db.Model(&models.AEOAnswer{}).Count(&orphans).Error)
	assert.Equal(t, int64(0), orphans,
		"the answer and its citations are one unit of work; a failed citation must take the answer with it")
}

func TestAEORepository_ListAnswersByPromptAndRun(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)
	prompt := makeAEOPrompt(t, db, "Which CRM?", true)
	other := makeAEOPrompt(t, db, "Which helpdesk?", true)
	base := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)
	runA := makeAEORun(t, db, models.AEORunStatusCompleted, base)
	runB := makeAEORun(t, db, models.AEORunStatusCompleted, base.Add(24*time.Hour))

	first := models.AEOAnswer{RunID: runA.ID, PromptID: prompt.ID, Provider: "openai", Model: "m", Attempt: 1}
	second := models.AEOAnswer{RunID: runB.ID, PromptID: prompt.ID, Provider: "openai", Model: "m", Attempt: 1}
	elsewhere := models.AEOAnswer{RunID: runB.ID, PromptID: other.ID, Provider: "openai", Model: "m", Attempt: 1}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Create(&second).Error)
	require.NoError(t, db.Create(&elsewhere).Error)
	stampTimes(t, db, &first, base, base)
	stampTimes(t, db, &second, base.Add(24*time.Hour), base.Add(24*time.Hour))

	all, err := repo.ListAnswersByPrompt(prompt.ID, nil, 0, 20)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, second.ID, all[0].ID, "answers come back newest first")

	scoped, err := repo.ListAnswersByPrompt(prompt.ID, &runA.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, first.ID, scoped[0].ID)

	total, err := repo.CountAnswersByPrompt(prompt.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)

	scopedTotal, err := repo.CountAnswersByPrompt(prompt.ID, &runA.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), scopedTotal)

	byRun, err := repo.ListAnswersByRun(runB.ID)
	require.NoError(t, err)
	assert.Len(t, byRun, 2, "the run carries answers for every prompt")
}

// --- metrics ------------------------------------------------------------------

// aeoMetricsFixture is the seeded world every metrics assertion below is
// hand-computed against. Ids are named a1..a9 in insertion order.
//
//	id  prompt  provider   created_at        mentioned  error      competitors
//	a1  p1      openai     2026-08-01 10:00  yes        -          Globex:2
//	a2  p1      anthropic  2026-08-01 11:00  no         -          Globex:1 Initech:3
//	a3  p2      openai     2026-08-01 12:00  yes        -          -
//	a4  p1      openai     2026-08-02 10:00  no         "timeout"  -
//	a5  p1      anthropic  2026-08-02 11:00  yes        -          Initech:1
//	a6  p2      openai     2026-08-03 10:00  yes        -          -
//	a7  p2      anthropic  2026-08-03 11:00  no         -          Globex:5
//	a8  p1      openai     2026-07-31 23:00  yes        -          -   (before `from`)
//	a9  p1      openai     2026-08-04 00:00  yes        -          -   (exactly `to`)
//
// Citations (answer → domain):
//
//	a1 → acme.com (owned), globex.com (Globex)
//	a2 → globex.com (Globex), news.example.com
//	a3 → acme.com (owned)
//	a4 → acme.com (owned)          — errored answer, excluded everywhere
//	a5 → globex.com (Globex)
//	a7 → news.example.com
//	a8 → acme.com (owned)          — outside the range
type aeoMetricsFixture struct {
	db        *gorm.DB
	repo      AEORepository
	p1, p2    uint
	a         map[string]uint
	from, to  time.Time
	day1Stamp time.Time
}

func seedAEOMetrics(t *testing.T) aeoMetricsFixture {
	t.Helper()
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	p1 := makeAEOPrompt(t, db, "Which CRM for a 10-person sales team?", true)
	p2 := makeAEOPrompt(t, db, "Best helpdesk for B2B SaaS?", true)
	day1 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	run := makeAEORun(t, db, models.AEORunStatusCompleted, day1)

	owned := func(url, domain string) models.AEOCitation {
		return models.AEOCitation{URL: url, Domain: domain, IsOwned: true}
	}
	rival := func(url, domain, name string) models.AEOCitation {
		return models.AEOCitation{URL: url, Domain: domain, CompetitorName: name}
	}
	neutral := func(url, domain string) models.AEOCitation {
		return models.AEOCitation{URL: url, Domain: domain}
	}

	specs := []struct {
		name      string
		promptID  uint
		provider  string
		at        time.Time
		mentioned bool
		errText   string
		comps     map[string]int
		citations []models.AEOCitation
	}{
		{"a1", p1.ID, "openai", day1.Add(10 * time.Hour), true, "", map[string]int{"Globex": 2},
			[]models.AEOCitation{owned("https://acme.com/a", "acme.com"), rival("https://globex.com/a", "globex.com", "Globex")}},
		{"a2", p1.ID, "anthropic", day1.Add(11 * time.Hour), false, "", map[string]int{"Globex": 1, "Initech": 3},
			[]models.AEOCitation{rival("https://globex.com/b", "globex.com", "Globex"), neutral("https://news.example.com/b", "news.example.com")}},
		{"a3", p2.ID, "openai", day1.Add(12 * time.Hour), true, "", nil,
			[]models.AEOCitation{owned("https://acme.com/c", "acme.com")}},
		{"a4", p1.ID, "openai", day1.Add(34 * time.Hour), false, "timeout after 60s", nil,
			[]models.AEOCitation{owned("https://acme.com/d", "acme.com")}},
		{"a5", p1.ID, "anthropic", day1.Add(35 * time.Hour), true, "", map[string]int{"Initech": 1},
			[]models.AEOCitation{rival("https://globex.com/e", "globex.com", "Globex")}},
		{"a6", p2.ID, "openai", day1.Add(58 * time.Hour), true, "", nil, nil},
		{"a7", p2.ID, "anthropic", day1.Add(59 * time.Hour), false, "", map[string]int{"Globex": 5},
			[]models.AEOCitation{neutral("https://news.example.com/g", "news.example.com")}},
		{"a8", p1.ID, "openai", day1.Add(-1 * time.Hour), true, "", nil,
			[]models.AEOCitation{owned("https://acme.com/h", "acme.com")}},
		{"a9", p1.ID, "openai", day1.Add(72 * time.Hour), true, "", nil, nil},
	}

	ids := map[string]uint{}
	for _, spec := range specs {
		answer := &models.AEOAnswer{
			RunID:              run.ID,
			PromptID:           spec.promptID,
			Provider:           spec.provider,
			Model:              "test-model",
			Attempt:            1,
			AnswerText:         "answer " + spec.name,
			BrandMentioned:     spec.mentioned,
			FirstMentionPos:    -1,
			CompetitorMentions: spec.comps,
			Error:              spec.errText,
		}
		require.NoError(t, repo.CreateAnswerWithCitations(answer, spec.citations))
		stampTimes(t, db, answer, spec.at, spec.at)
		ids[spec.name] = answer.ID
	}

	return aeoMetricsFixture{
		db:        db,
		repo:      repo,
		p1:        p1.ID,
		p2:        p2.ID,
		a:         ids,
		from:      day1,
		to:        day1.Add(72 * time.Hour), // 2026-08-04 00:00 UTC, exclusive
		day1Stamp: day1,
	}
}

func TestAEORepository_ListAnswerFacts(t *testing.T) {
	f := seedAEOMetrics(t)

	facts, err := f.repo.ListAnswerFacts(f.from, f.to)

	require.NoError(t, err)
	require.Len(t, facts, 7, "a8 is before `from` and a9 sits exactly on the exclusive `to`")

	names := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"}
	for i, name := range names {
		assert.Equal(t, f.a[name], facts[i].AnswerID, "facts come back oldest first")
	}

	assert.True(t, facts[3].Errored, "a4 failed and must still be reported, flagged")
	assert.Equal(t, f.a["a4"], facts[3].AnswerID)
	for i, fact := range facts {
		if i == 3 {
			continue
		}
		assert.False(t, fact.Errored, "only a4 carries an error")
	}

	assert.Equal(t, map[string]int{"Globex": 1, "Initech": 3}, facts[1].CompetitorMentions,
		"the JSON TEXT column is decoded in Go, never by the database")
	assert.Empty(t, facts[2].CompetitorMentions)

	assert.Equal(t, f.p1, facts[0].PromptID)
	assert.Equal(t, "openai", facts[0].Provider)
	assert.True(t, facts[0].BrandMentioned)
	assert.True(t, facts[0].CreatedAt.UTC().Equal(f.from.Add(10*time.Hour)))

	// Competitor totals are a Go-side tally over the facts, which is the whole
	// point of returning them as rows: Globex 2+1+5, Initech 3+1.
	tally := map[string]int{}
	for _, fact := range facts {
		for name, count := range fact.CompetitorMentions {
			tally[name] += count
		}
	}
	assert.Equal(t, map[string]int{"Globex": 8, "Initech": 4}, tally)
}

func TestAEORepository_PromptVisibility(t *testing.T) {
	f := seedAEOMetrics(t)

	stats, err := f.repo.PromptVisibility(f.from, f.to, []uint{f.p1, f.p2})

	require.NoError(t, err)
	require.Len(t, stats, 2)

	// p1 in range: a1, a2, a4(errored), a5 → 3 counted answers, 2 mentions (a1, a5).
	one := stats[f.p1]
	assert.Equal(t, f.p1, one.PromptID)
	assert.Equal(t, int64(3), one.Answers, "the failed answer is out of the denominator")
	assert.Equal(t, int64(2), one.Mentions)
	require.NotNil(t, one.LastRunAt)
	assert.True(t, one.LastRunAt.UTC().Equal(f.from.Add(35*time.Hour)), "a5 is p1's newest answer in the range")

	// p2 in range: a3, a6, a7 → 3 answers, 2 mentions (a3, a6).
	two := stats[f.p2]
	assert.Equal(t, int64(3), two.Answers)
	assert.Equal(t, int64(2), two.Mentions)
	require.NotNil(t, two.LastRunAt)
	assert.True(t, two.LastRunAt.UTC().Equal(f.from.Add(59*time.Hour)))
}

func TestAEORepository_PromptVisibility_LastRunAtCountsFailedRuns(t *testing.T) {
	f := seedAEOMetrics(t)

	// Append a failed answer for p1 the way the engine would: written after
	// everything else, so its id and its created_at are both the newest. It
	// contributes nothing to the counters, but it is still the last time the
	// prompt ran.
	latest := f.from.Add(70 * time.Hour)
	failed := &models.AEOAnswer{
		PromptID: f.p1, Provider: "openai", Model: "test-model", Attempt: 1,
		FirstMentionPos: -1, Error: "429 after one retry",
	}
	require.NoError(t, f.repo.CreateAnswerWithCitations(failed, nil))
	stampTimes(t, f.db, failed, latest, latest)

	stats, err := f.repo.PromptVisibility(f.from, f.to, []uint{f.p1})

	require.NoError(t, err)
	one := stats[f.p1]
	assert.Equal(t, int64(3), one.Answers, "the new failure stays out of the denominator")
	assert.Equal(t, int64(2), one.Mentions)
	require.NotNil(t, one.LastRunAt)
	assert.True(t, one.LastRunAt.UTC().Equal(latest),
		"a run that failed still ran, so it sets last_run_at")
}

func TestAEORepository_PromptVisibility_EmptyAndUnknownPrompts(t *testing.T) {
	f := seedAEOMetrics(t)

	empty, err := f.repo.PromptVisibility(f.from, f.to, nil)
	require.NoError(t, err)
	assert.Empty(t, empty, "asking about no prompts returns nothing, it does not fall back to all of them")

	unknown, err := f.repo.PromptVisibility(f.from, f.to, []uint{f.p1, 9999})
	require.NoError(t, err)
	assert.Len(t, unknown, 1)
	_, present := unknown[9999]
	assert.False(t, present, "a prompt with no answers in the range is simply absent")

	outside := f.from.Add(-240 * time.Hour)
	none, err := f.repo.PromptVisibility(outside, outside.Add(time.Hour), []uint{f.p1, f.p2})
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestAEORepository_CitationDomainStats(t *testing.T) {
	f := seedAEOMetrics(t)

	rows, err := f.repo.CitationDomainStats(f.from, f.to)

	require.NoError(t, err)
	require.Len(t, rows, 3, "a4's citation is dropped with its errored answer and a8's is out of range")

	// globex.com: a1, a2, a5 → 3 citations, brand mentioned in a1 and a5.
	assert.Equal(t, "globex.com", rows[0].Domain, "most-cited domain first")
	assert.False(t, rows[0].IsOwned)
	assert.Equal(t, "Globex", rows[0].CompetitorName)
	assert.Equal(t, int64(3), rows[0].Citations)
	assert.Equal(t, int64(2), rows[0].WithBrandMention)

	// acme.com: a1, a3 → 2 citations, both in answers that mention the brand.
	assert.Equal(t, "acme.com", rows[1].Domain, "ties break on the domain, ascending")
	assert.True(t, rows[1].IsOwned)
	assert.Equal(t, "", rows[1].CompetitorName)
	assert.Equal(t, int64(2), rows[1].Citations)
	assert.Equal(t, int64(2), rows[1].WithBrandMention)

	// news.example.com: a2, a7 → 2 citations, neither answer mentions the brand.
	assert.Equal(t, "news.example.com", rows[2].Domain)
	assert.False(t, rows[2].IsOwned)
	assert.Equal(t, "", rows[2].CompetitorName)
	assert.Equal(t, int64(2), rows[2].Citations)
	assert.Equal(t, int64(0), rows[2].WithBrandMention)
}

func TestAEORepository_CitationDomainStats_EmptyRange(t *testing.T) {
	f := seedAEOMetrics(t)

	rows, err := f.repo.CitationDomainStats(f.to, f.to.Add(24*time.Hour))

	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestAEORepository_CountAnswersInRange(t *testing.T) {
	f := seedAEOMetrics(t)

	// a1..a7 are in range; a4 failed. 6 counted answers, 4 of them mention the
	// brand (a1, a3, a5, a6).
	total, mentions, err := f.repo.CountAnswersInRange(f.from, f.to)

	require.NoError(t, err)
	assert.Equal(t, int64(6), total)
	assert.Equal(t, int64(4), mentions)
}

func TestAEORepository_CountAnswersInRange_BoundariesAndEmptiness(t *testing.T) {
	f := seedAEOMetrics(t)

	// Widening `to` by an hour pulls a9 in: 7 answers, 5 mentions.
	total, mentions, err := f.repo.CountAnswersInRange(f.from, f.to.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(7), total, "`to` is exclusive, so a9 only counts once the window moves past it")
	assert.Equal(t, int64(5), mentions)

	// Widening `from` backwards pulls a8 in as well.
	total, mentions, err = f.repo.CountAnswersInRange(f.from.Add(-2*time.Hour), f.to)
	require.NoError(t, err)
	assert.Equal(t, int64(7), total)
	assert.Equal(t, int64(5), mentions)

	// An empty window must not divide by anything or return NULL. It starts
	// past a9, which sits exactly on `to`.
	total, mentions, err = f.repo.CountAnswersInRange(f.to.Add(time.Hour), f.to.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), total, "SUM over an empty group is NULL and has to arrive as 0")
	assert.Equal(t, int64(0), mentions)
}

func TestAEORepository_CountAnswersWithCitations(t *testing.T) {
	f := seedAEOMetrics(t)

	// a1, a2, a3, a5, a7 carry citations and did not fail. a4 failed, a6 has
	// none, a8 is out of range.
	count, err := f.repo.CountAnswersWithCitations(f.from, f.to)

	require.NoError(t, err)
	assert.Equal(t, int64(5), count)

	empty, err := f.repo.CountAnswersWithCitations(f.to, f.to.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), empty)
}

func TestAEORepository_MetricsIgnoreSoftDeletedRows(t *testing.T) {
	f := seedAEOMetrics(t)

	// a1 is the only answer citing acme.com AND globex.com in one row, so
	// deleting it moves every aggregate at once.
	require.NoError(t, f.db.Delete(&models.AEOAnswer{}, f.a["a1"]).Error)

	facts, err := f.repo.ListAnswerFacts(f.from, f.to)
	require.NoError(t, err)
	assert.Len(t, facts, 6)

	total, mentions, err := f.repo.CountAnswersInRange(f.from, f.to)
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Equal(t, int64(3), mentions)

	stats, err := f.repo.PromptVisibility(f.from, f.to, []uint{f.p1})
	require.NoError(t, err)
	assert.Equal(t, int64(2), stats[f.p1].Answers)
	assert.Equal(t, int64(1), stats[f.p1].Mentions)

	rows, err := f.repo.CitationDomainStats(f.from, f.to)
	require.NoError(t, err)
	byDomain := map[string]models.AEOCitationAggRow{}
	for _, row := range rows {
		byDomain[row.Domain] = row
	}
	assert.Equal(t, int64(2), byDomain["globex.com"].Citations, "a1's globex citation went with the answer")
	assert.Equal(t, int64(1), byDomain["globex.com"].WithBrandMention)
	assert.Equal(t, int64(1), byDomain["acme.com"].Citations)

	withCitations, err := f.repo.CountAnswersWithCitations(f.from, f.to)
	require.NoError(t, err)
	assert.Equal(t, int64(4), withCitations)

	// Deleting a citation on its own must move only the citation aggregates.
	var citation models.AEOCitation
	require.NoError(t, f.db.Where("answer_id = ?", f.a["a7"]).First(&citation).Error)
	require.NoError(t, f.db.Delete(&models.AEOCitation{}, citation.ID).Error)

	rows, err = f.repo.CitationDomainStats(f.from, f.to)
	require.NoError(t, err)
	byDomain = map[string]models.AEOCitationAggRow{}
	for _, row := range rows {
		byDomain[row.Domain] = row
	}
	assert.Equal(t, int64(1), byDomain["news.example.com"].Citations)

	withCitations, err = f.repo.CountAnswersWithCitations(f.from, f.to)
	require.NoError(t, err)
	assert.Equal(t, int64(3), withCitations)
}

// --- transactions and portability ---------------------------------------------

func TestAEORepository_WithTx(t *testing.T) {
	db := setupAEOTestDB(t)
	repo := NewAEORepository(db)

	err := db.Transaction(func(tx *gorm.DB) error {
		txRepo := repo.WithTx(tx)
		if err := txRepo.CreatePrompt(&models.AEOPrompt{Text: "Rolled back", IsActive: true}); err != nil {
			return err
		}
		return errors.New("boom")
	})
	require.Error(t, err)

	count, err := repo.CountPrompts(false)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "WithTx has to bind the repository to the caller's transaction")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return repo.WithTx(tx).CreatePrompt(&models.AEOPrompt{Text: "Committed", IsActive: true})
	}))

	count, err = repo.CountPrompts(false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// TestAEORepository_SQLIsPortable guards the dual-database constraint from the
// top of the metrics section: the suite runs on SQLite and production runs on
// MySQL 8, so a green test proves only half of any statement written with a
// dialect-specific function. Reading the source is the only way to catch the
// other half without a MySQL server in CI.
func TestAEORepository_SQLIsPortable(t *testing.T) {
	source, err := os.ReadFile("aeo_repository.go")
	require.NoError(t, err)
	text := stripLineComments(string(source))

	// Matched case-sensitively against the upper-case spelling every SQL
	// fragment in the file uses, so Go identifiers such as UpdateRun cannot
	// trip the DATE( check.
	forbidden := []string{
		"DATE(", "DATE_FORMAT", "EXTRACT(", "DATEDIFF", "TIMESTAMPDIFF",
		"NOW()", "CURDATE", "UNIX_TIMESTAMP", "CONVERT_TZ",
		"JSON_", "GROUP_CONCAT", "IFNULL", "ISNULL(",
		"MAX(created_at)", "MIN(created_at)", "MAX(started_at)",
	}
	for _, token := range forbidden {
		assert.NotContains(t, text, token,
			"%s is not portable across MySQL 8 and SQLite (or is a time aggregate the driver returns untyped)", token)
	}

	for _, token := range []string{"strftime", "julianday", "->>", "->'"} {
		assert.NotContains(t, strings.ToLower(text), token,
			"%s is SQLite- or MySQL-specific", token)
	}
}

// stripLineComments drops // comments so the portability scan reads code only —
// the comment block documenting the constraint names every banned function and
// would otherwise fail the check it describes. Crude on purpose: no string
// literal in aeo_repository.go contains "//".
func stripLineComments(source string) string {
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
