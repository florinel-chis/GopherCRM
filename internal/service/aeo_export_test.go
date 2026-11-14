package service

// Regression roots for aeoService.Export (#244): read order, the 10,000-answer
// cap checked before any citation read, the window computed on the instance
// clock, the assembled shape, and log privacy. The repository is the mockery
// double; the real reads are covered in internal/repository.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// initAEOExportServiceLogger installs a JSON logger writing into buf (or
// discarding) and restores the previous one afterwards.
func initAEOExportServiceLogger(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	if buf != nil {
		utils.Logger.SetOutput(buf)
	} else {
		utils.Logger.SetOutput(&bytes.Buffer{})
	}
	t.Cleanup(func() { utils.Logger = previous })
}

func newAEOExportTestService(repo repository.AEORepository, now time.Time) *aeoService {
	return newAEOService(repo, nil, nil, nil, WithAEOClock(func() time.Time { return now }))
}

func aeoExportAnswer(id, runID, promptID uint, createdAt time.Time) models.AEOAnswer {
	answer := models.AEOAnswer{
		RunID:              runID,
		PromptID:           promptID,
		Provider:           "anthropic",
		Model:              "model-a",
		AnswerText:         fmt.Sprintf("answer %d", id),
		FirstMentionPos:    -1,
		LatencyMs:          10,
		CompetitorMentions: map[string]int{},
		Citations:          []models.AEOCitation{},
	}
	answer.ID = id
	answer.CreatedAt = createdAt
	return answer
}

func aeoExportCitationRow(id, answerID uint, domain string) models.AEOCitation {
	citation := models.AEOCitation{AnswerID: answerID, URL: "https://" + domain + "/x", Domain: domain}
	citation.ID = id
	return citation
}

// windowFilter matches the window filter with exact bounds.
func windowFilter(from, to time.Time) interface{} {
	return mock.MatchedBy(func(filter repository.AEOExportFilter) bool {
		return filter.RunID == nil && filter.From.Equal(from) && filter.To.Equal(to)
	})
}

// noExportReaderRepo implements AEORepository only.
type noExportReaderRepo struct {
	repository.AEORepository
}

func TestAEOExportService(t *testing.T) {
	initAEOExportServiceLogger(t, nil)
	ctx := context.Background()
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)

	t.Run("unknown run is not found before any answer read", func(t *testing.T) {
		repo := new(mocks.AEORepository)
		repo.On("GetRunByID", uint(77)).Return(nil, gorm.ErrRecordNotFound).Once()
		_, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{RunID: 77})
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound))
		assert.True(t, apperrors.IsNotFound(err))
		repo.AssertNotCalled(t, "ListAnswersForExport", mock.Anything, mock.Anything, mock.Anything)
		repo.AssertExpectations(t)
	})

	t.Run("a failed run read is not a not-found", func(t *testing.T) {
		repo := new(mocks.AEORepository)
		boom := errors.New("connection refused")
		repo.On("GetRunByID", uint(5)).Return(nil, boom).Once()
		_, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{RunID: 5})
		assert.ErrorIs(t, err, boom)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		repo.AssertNotCalled(t, "ListAnswersForExport", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("invalid selections make no read", func(t *testing.T) {
		for _, selection := range []models.AEOExportSelection{
			{}, {Days: 14}, {Days: -7}, {RunID: 1, Days: 7},
		} {
			repo := new(mocks.AEORepository)
			_, err := newAEOExportTestService(repo, now).Export(ctx, selection)
			assert.ErrorIs(t, err, ErrAEOExportInvalidSelection, "%+v", selection)
			assert.Empty(t, repo.Calls)
		}
	})

	t.Run("a repository without the export reads is refused", func(t *testing.T) {
		_, err := newAEOExportTestService(noExportReaderRepo{}, now).Export(ctx, models.AEOExportSelection{Days: 7})
		assert.ErrorIs(t, err, ErrAEOExportUnavailable)
	})

	t.Run("more than the cap is refused before citations are read", func(t *testing.T) {
		assert.Equal(t, 10000, AEOExportMaxAnswers)
		answers := make([]models.AEOAnswer, AEOExportMaxAnswers+1)
		for i := range answers {
			answers[i] = aeoExportAnswer(uint(i+1), 1, 1, now)
		}
		repo := new(mocks.AEORepository)
		to := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)
		repo.On("ListAnswersForExport", mock.Anything, windowFilter(to.AddDate(0, 0, -90), to), 10001).Return(answers, nil).Once()

		export, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{Days: 90})
		assert.Nil(t, export)
		assert.ErrorIs(t, err, ErrAEOExportTooLarge)
		repo.AssertNotCalled(t, "ListCitationsByAnswerIDs", mock.Anything, mock.Anything)
		repo.AssertNotCalled(t, "ListRunsByIDs", mock.Anything, mock.Anything)
		repo.AssertNotCalled(t, "ListPromptTextsByIDs", mock.Anything, mock.Anything)
		repo.AssertExpectations(t)
	})

	t.Run("exactly the cap is exported", func(t *testing.T) {
		answers := make([]models.AEOAnswer, AEOExportMaxAnswers)
		ids := make([]uint, AEOExportMaxAnswers)
		for i := range answers {
			answers[i] = aeoExportAnswer(uint(i+1), 1, 1, now)
			ids[i] = uint(i + 1)
		}
		repo := new(mocks.AEORepository)
		repo.On("ListAnswersForExport", mock.Anything, mock.Anything, 10001).Return(answers, nil).Once()
		repo.On("ListCitationsByAnswerIDs", mock.Anything, ids).Return([]models.AEOCitation{}, nil).Once()
		repo.On("ListPromptTextsByIDs", mock.Anything, []uint{1}).Return(map[uint]string{1: "q"}, nil).Once()
		repo.On("ListRunsByIDs", mock.Anything, []uint{1}).Return([]models.AEORun{{BaseModel: models.BaseModel{ID: 1}}}, nil).Once()

		export, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{Days: 30})
		require.NoError(t, err)
		assert.Len(t, export.Answers, AEOExportMaxAnswers)
		repo.AssertExpectations(t)
	})

	t.Run("window bounds and generated_at come from the instance clock", func(t *testing.T) {
		cases := []struct {
			name string
			now  time.Time
			days int
			to   time.Time
		}{
			{"last millisecond of the day", time.Date(2026, time.November, 14, 23, 59, 59, 999000000, time.UTC), 30,
				time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)},
			{"first instant of the day", time.Date(2026, time.November, 14, 0, 0, 0, 0, time.UTC), 7,
				time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)},
			{"a clock east of UTC", time.Date(2026, time.November, 15, 1, 30, 0, 0, time.FixedZone("UTC+2", 2*3600)), 90,
				time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)},
			{"a clock west of UTC", time.Date(2026, time.November, 14, 22, 0, 0, 0, time.FixedZone("UTC-5", -5*3600)), 7,
				time.Date(2026, time.November, 16, 0, 0, 0, 0, time.UTC)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				from := tc.to.AddDate(0, 0, -tc.days)
				repo := new(mocks.AEORepository)
				repo.On("ListAnswersForExport", mock.Anything, windowFilter(from, tc.to), 10001).Return([]models.AEOAnswer{}, nil).Once()

				export, err := newAEOExportTestService(repo, tc.now).Export(ctx, models.AEOExportSelection{Days: tc.days})
				require.NoError(t, err)
				assert.Equal(t, tc.days, export.Selection.Days)
				assert.Zero(t, export.Selection.RunID)
				require.NotNil(t, export.Selection.From)
				require.NotNil(t, export.Selection.To)
				assert.True(t, from.Equal(*export.Selection.From), "from %s", export.Selection.From)
				assert.True(t, tc.to.Equal(*export.Selection.To), "to %s", export.Selection.To)
				assert.True(t, tc.now.Equal(export.GeneratedAt))
				assert.Equal(t, time.UTC, export.GeneratedAt.Location())
				repo.AssertExpectations(t)
			})
		}
	})

	t.Run("an empty window returns empty slices and reads nothing else", func(t *testing.T) {
		repo := new(mocks.AEORepository)
		repo.On("ListAnswersForExport", mock.Anything, mock.Anything, 10001).Return([]models.AEOAnswer{}, nil).Once()
		export, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{Days: 7})
		require.NoError(t, err)
		assert.Equal(t, models.AEOExportFormatVersion, export.FormatVersion)
		assert.NotNil(t, export.Runs)
		assert.NotNil(t, export.Answers)
		assert.Empty(t, export.Runs)
		assert.Empty(t, export.Answers)
		repo.AssertNotCalled(t, "ListCitationsByAnswerIDs", mock.Anything, mock.Anything)
		repo.AssertNotCalled(t, "ListRunsByIDs", mock.Anything, mock.Anything)
		repo.AssertNotCalled(t, "ListPromptTextsByIDs", mock.Anything, mock.Anything)
		repo.AssertExpectations(t)
	})

	t.Run("a run without answers still lists the run", func(t *testing.T) {
		run := models.AEORun{Trigger: "manual", Status: "running", StartedAt: now}
		run.ID = 12
		repo := new(mocks.AEORepository)
		repo.On("GetRunByID", uint(12)).Return(&run, nil).Once()
		repo.On("ListAnswersForExport", mock.Anything, mock.MatchedBy(func(filter repository.AEOExportFilter) bool {
			return filter.RunID != nil && *filter.RunID == 12
		}), 10001).Return([]models.AEOAnswer{}, nil).Once()
		repo.On("ListRunsByIDs", mock.Anything, []uint{12}).Return([]models.AEORun{run}, nil).Once()

		export, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{RunID: 12})
		require.NoError(t, err)
		assert.Equal(t, models.AEOExportSelection{RunID: 12}, export.Selection)
		require.Len(t, export.Runs, 1)
		assert.Equal(t, "running", export.Runs[0].Status)
		assert.Nil(t, export.Runs[0].CompletedAt)
		assert.Empty(t, export.Answers)
		repo.AssertNotCalled(t, "ListCitationsByAnswerIDs", mock.Anything, mock.Anything)
		repo.AssertExpectations(t)
	})

	t.Run("a valid selection assembles runs, prompt texts and grouped citations", func(t *testing.T) {
		owner := "instance-a"
		lock := uint8(1)
		slot := "2026-11-14T06:00Z"
		completed := now.Add(-time.Hour)
		triggeredBy := uint(9)
		runA := models.AEORun{Trigger: "scheduled", Status: "completed", StartedAt: now.Add(-2 * time.Hour),
			CompletedAt: &completed, TotalQueries: 4, FailedQueries: 1, OwnerInstance: &owner, RunningLock: &lock, ScheduleSlot: &slot}
		runA.ID = 12
		runB := models.AEORun{Trigger: "manual", Status: "running", StartedAt: now.Add(-time.Minute), TriggeredByID: &triggeredBy}
		runB.ID = 13

		a1 := aeoExportAnswer(101, 12, 3, now.Add(-90*time.Minute))
		a1.BrandMentioned, a1.FirstMentionPos = true, 4
		a1.CompetitorMentions = map[string]int{"Globex": 2}
		a2 := aeoExportAnswer(102, 13, 4, now.Add(-30*time.Minute))
		a2.Error, a2.AnswerText = "openai: request timed out", ""
		a3 := aeoExportAnswer(103, 12, 3, now.Add(-30*time.Minute))

		repo := new(mocks.AEORepository)
		to := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)
		repo.On("ListAnswersForExport", mock.Anything, windowFilter(to.AddDate(0, 0, -30), to), 10001).
			Return([]models.AEOAnswer{a1, a2, a3}, nil).Once()
		repo.On("ListCitationsByAnswerIDs", mock.Anything, []uint{101, 102, 103}).Return([]models.AEOCitation{
			aeoExportCitationRow(201, 101, "acme.example"),
			aeoExportCitationRow(202, 101, "globex.example"),
			aeoExportCitationRow(203, 103, "docs.example"),
		}, nil).Once()
		repo.On("ListPromptTextsByIDs", mock.Anything, []uint{3, 4}).
			Return(map[uint]string{3: "Which CRM?", 4: "A deleted prompt"}, nil).Once()
		repo.On("ListRunsByIDs", mock.Anything, []uint{12, 13}).Return([]models.AEORun{runA, runB}, nil).Once()

		export, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{Days: 30})
		require.NoError(t, err)
		repo.AssertExpectations(t)

		require.Len(t, export.Runs, 2)
		assert.Equal(t, models.AEOExportRun{ID: 12, Trigger: "scheduled", Status: "completed", StartedAt: runA.StartedAt,
			CompletedAt: &completed, TotalQueries: 4, FailedQueries: 1}, export.Runs[0])
		assert.Equal(t, uint(13), export.Runs[1].ID)
		assert.Equal(t, &triggeredBy, export.Runs[1].TriggeredByID)

		require.Len(t, export.Answers, 3)
		assert.Equal(t, []uint{101, 102, 103}, []uint{export.Answers[0].ID, export.Answers[1].ID, export.Answers[2].ID})
		first := export.Answers[0]
		assert.Equal(t, "Which CRM?", first.PromptText)
		assert.Equal(t, "answer 101", first.AnswerText)
		assert.True(t, first.BrandMentioned)
		assert.Equal(t, 4, first.FirstMentionPos)
		assert.Equal(t, map[string]int{"Globex": 2}, first.CompetitorMentions)
		require.Len(t, first.Citations, 2)
		assert.Equal(t, models.AEOExportCitation{ID: 201, URL: "https://acme.example/x", Domain: "acme.example"}, first.Citations[0])
		assert.Equal(t, uint(202), first.Citations[1].ID)

		second := export.Answers[1]
		assert.Equal(t, "A deleted prompt", second.PromptText)
		assert.Equal(t, "openai: request timed out", second.Error)
		assert.NotNil(t, second.Citations)
		assert.Empty(t, second.Citations)
		assert.NotNil(t, second.CompetitorMentions)

		require.Len(t, export.Answers[2].Citations, 1)
		assert.Equal(t, uint(203), export.Answers[2].Citations[0].ID)
		assert.True(t, now.Equal(export.GeneratedAt))
	})

	t.Run("read failures are returned", func(t *testing.T) {
		boom := errors.New("read failed")
		repo := new(mocks.AEORepository)
		repo.On("ListAnswersForExport", mock.Anything, mock.Anything, 10001).
			Return([]models.AEOAnswer{aeoExportAnswer(1, 1, 1, now)}, nil).Once()
		repo.On("ListCitationsByAnswerIDs", mock.Anything, []uint{1}).Return(nil, boom).Once()
		_, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{Days: 7})
		assert.ErrorIs(t, err, boom)
		repo.AssertNotCalled(t, "ListRunsByIDs", mock.Anything, mock.Anything)
	})
}

func TestAEOExportLogsOmitPersonalData(t *testing.T) {
	var buf bytes.Buffer
	initAEOExportServiceLogger(t, &buf)
	ctx := context.Background()
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)

	const (
		promptCanary     = "PromptCanaryA1"
		answerCanary     = "AnswerCanaryB2"
		urlCanary        = "https://url-canary-c3.example/p"
		domainCanary     = "domain-canary-d4.example"
		competitorCanary = "CompetitorCanaryE5"
		brandCanary      = "BrandCanaryF6"
		modelCanary      = "ModelCanaryG7"
	)
	answer := aeoExportAnswer(1, 12, 3, now)
	answer.AnswerText = answerCanary + " mentions " + brandCanary
	answer.Model = modelCanary
	answer.CompetitorMentions = map[string]int{competitorCanary: 1}
	citation := models.AEOCitation{AnswerID: 1, URL: urlCanary, Domain: domainCanary, CompetitorName: competitorCanary}
	citation.ID = 9
	run := models.AEORun{Trigger: "manual", Status: "completed", StartedAt: now}
	run.ID = 12

	// A successful run export.
	repo := new(mocks.AEORepository)
	repo.On("GetRunByID", uint(12)).Return(&run, nil)
	repo.On("ListAnswersForExport", mock.Anything, mock.Anything, 10001).Return([]models.AEOAnswer{answer}, nil)
	repo.On("ListCitationsByAnswerIDs", mock.Anything, mock.Anything).Return([]models.AEOCitation{citation}, nil)
	repo.On("ListPromptTextsByIDs", mock.Anything, mock.Anything).Return(map[uint]string{3: promptCanary}, nil)
	repo.On("ListRunsByIDs", mock.Anything, mock.Anything).Return([]models.AEORun{run}, nil)
	_, err := newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{RunID: 12})
	require.NoError(t, err)

	// A too-large window.
	large := make([]models.AEOAnswer, AEOExportMaxAnswers+1)
	for i := range large {
		large[i] = answer
	}
	repo = new(mocks.AEORepository)
	repo.On("ListAnswersForExport", mock.Anything, mock.Anything, 10001).Return(large, nil)
	_, err = newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{Days: 90})
	require.ErrorIs(t, err, ErrAEOExportTooLarge)

	// An unknown run.
	repo = new(mocks.AEORepository)
	repo.On("GetRunByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)
	_, err = newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{RunID: 404})
	require.True(t, apperrors.IsNotFound(err))

	// A failed prompt read.
	repo = new(mocks.AEORepository)
	repo.On("ListAnswersForExport", mock.Anything, mock.Anything, 10001).Return([]models.AEOAnswer{answer}, nil)
	repo.On("ListCitationsByAnswerIDs", mock.Anything, mock.Anything).Return([]models.AEOCitation{citation}, nil)
	repo.On("ListPromptTextsByIDs", mock.Anything, mock.Anything).Return(nil, errors.New("prompt read failed"))
	_, err = newAEOExportTestService(repo, now).Export(ctx, models.AEOExportSelection{Days: 7})
	require.Error(t, err)

	output := buf.String()
	require.NotEmpty(t, output)
	assert.Contains(t, output, `"operation":"aeo_export"`)
	assert.Contains(t, output, `"error_code":"export_too_large"`)
	assert.Contains(t, output, `"error_code":"not_found"`)
	assert.Contains(t, output, `"error_code":"prompt_read_failed"`)
	assert.Contains(t, output, `"answers":1`)
	assert.Contains(t, output, `"citations":1`)
	for _, needle := range []string{
		promptCanary, answerCanary, urlCanary, "url-canary", domainCanary, competitorCanary, brandCanary, modelCanary,
	} {
		assert.NotContains(t, output, needle, "no export log line carries content")
	}
}
