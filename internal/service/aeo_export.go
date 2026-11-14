package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// AEOExportMaxAnswers caps one AEO export (#244). Every row is read into memory
// before the first byte of the file is written, so the cap bounds that memory;
// a 90-day window over many prompts and engines can exceed it and must be
// narrowed to a shorter window or a single run.
const AEOExportMaxAnswers = 10000

var (
	// ErrAEOExportTooLarge is returned when the selection holds more than
	// AEOExportMaxAnswers answers. It is detected on the answer read, before
	// any citation, run or prompt is read. The handler answers 422
	// EXPORT_TOO_LARGE.
	ErrAEOExportTooLarge = errors.New("the AEO export selects more than 10000 answers")

	// ErrAEOExportInvalidSelection guards direct callers: a selection must name
	// a positive run id or a 7, 30 or 90-day window, not both. The handler
	// validates first, so it never reaches the service over HTTP.
	ErrAEOExportInvalidSelection = errors.New("an AEO export selects one run or a 7, 30 or 90-day window")

	// ErrAEOExportUnavailable is returned when the repository the service was
	// built with does not implement repository.AEOExportReader.
	ErrAEOExportUnavailable = errors.New("the AEO export reads are not available")
)

// aeoExportWindowDays are the windows an export accepts. They are the
// dashboard's windows; unlike the dashboard, any other value is refused.
var aeoExportWindowDays = map[int]bool{7: true, 30: true, 90: true}

// Export implements AEOService.Export.
//
// Order of reads: the run (run selection only, so an unknown run is a 404
// before any answer is read), then at most AEOExportMaxAnswers+1 answers
// without citations; above the cap it returns ErrAEOExportTooLarge and reads
// nothing else. Otherwise the citations of those answers, their runs and their
// prompt texts (soft-deleted prompts included).
//
// Log lines carry operation, run_id, days, counts and error codes only; never
// prompt text, answer text, URLs, domains, competitor names, the brand or a
// model.
func (s *aeoService) Export(ctx context.Context, selection models.AEOExportSelection) (*models.AEOExport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("operation", "aeo_export"), "AEOService", "Export")

	reader, ok := s.repo.(repository.AEOExportReader)
	if !ok {
		logger.WithField("error_code", "export_unavailable").Error("AEO export refused: the repository has no export reads")
		return nil, ErrAEOExportUnavailable
	}

	runSelected := selection.RunID != 0
	if runSelected == (selection.Days != 0) || (!runSelected && !aeoExportWindowDays[selection.Days]) {
		logger.WithField("error_code", "invalid_selection").Warn("AEO export refused: invalid selection")
		return nil, ErrAEOExportInvalidSelection
	}

	// One reading of the instance clock serves the window and generated_at.
	now := s.clock()
	export := &models.AEOExport{
		FormatVersion: models.AEOExportFormatVersion,
		GeneratedAt:   now,
		Runs:          []models.AEOExportRun{},
		Answers:       []models.AEOExportAnswer{},
	}

	var filter repository.AEOExportFilter
	if runSelected {
		runID := selection.RunID
		logger = logger.WithField("run_id", runID)
		if _, err := s.repo.WithContext(ctx).GetRunByID(runID); err != nil {
			if isNotFound(err) {
				logger.WithField("error_code", "not_found").Warn("AEO export refused: unknown run")
				return nil, fmt.Errorf("aeo run %d not found: %w", runID, apperrors.ErrNotFound)
			}
			aeoExportLogFailure(logger, "run_read_failed", err)
			return nil, err
		}
		filter.RunID = &runID
		export.Selection = models.AEOExportSelection{RunID: runID}
	} else {
		logger = logger.WithField("days", selection.Days)
		to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		from := to.AddDate(0, 0, -selection.Days)
		filter.From, filter.To = from, to
		export.Selection = models.AEOExportSelection{Days: selection.Days, From: &from, To: &to}
	}

	answers, err := reader.ListAnswersForExport(ctx, filter, AEOExportMaxAnswers+1)
	if err != nil {
		aeoExportLogFailure(logger, "answer_read_failed", err)
		return nil, err
	}
	if len(answers) > AEOExportMaxAnswers {
		logger.WithFields(logrus.Fields{
			"error_code":  "export_too_large",
			"max_answers": AEOExportMaxAnswers,
		}).Warn("AEO export refused: the selection holds too many answers")
		return nil, ErrAEOExportTooLarge
	}

	runIDs := make([]uint, 0, 1)
	seenRuns := map[uint]bool{}
	addRun := func(id uint) {
		if id != 0 && !seenRuns[id] {
			seenRuns[id] = true
			runIDs = append(runIDs, id)
		}
	}
	addRun(selection.RunID)

	var citationCount int
	if len(answers) > 0 {
		answerIDs := make([]uint, 0, len(answers))
		promptIDs := make([]uint, 0, len(answers))
		seenPrompts := map[uint]bool{}
		for i := range answers {
			answerIDs = append(answerIDs, answers[i].ID)
			addRun(answers[i].RunID)
			if !seenPrompts[answers[i].PromptID] {
				seenPrompts[answers[i].PromptID] = true
				promptIDs = append(promptIDs, answers[i].PromptID)
			}
		}

		citations, err := reader.ListCitationsByAnswerIDs(ctx, answerIDs)
		if err != nil {
			aeoExportLogFailure(logger, "citation_read_failed", err)
			return nil, err
		}
		citationCount = len(citations)
		byAnswer := make(map[uint][]models.AEOExportCitation, len(answers))
		for _, citation := range citations {
			byAnswer[citation.AnswerID] = append(byAnswer[citation.AnswerID], models.AEOExportCitation{
				ID:             citation.ID,
				URL:            citation.URL,
				Domain:         citation.Domain,
				IsOwned:        citation.IsOwned,
				CompetitorName: citation.CompetitorName,
			})
		}

		texts, err := reader.ListPromptTextsByIDs(ctx, promptIDs)
		if err != nil {
			aeoExportLogFailure(logger, "prompt_read_failed", err)
			return nil, err
		}

		for i := range answers {
			answer := &answers[i]
			mentions := make(map[string]int, len(answer.CompetitorMentions))
			for name, count := range answer.CompetitorMentions {
				mentions[name] = count
			}
			answerCitations := byAnswer[answer.ID]
			if answerCitations == nil {
				answerCitations = []models.AEOExportCitation{}
			}
			export.Answers = append(export.Answers, models.AEOExportAnswer{
				ID:                 answer.ID,
				RunID:              answer.RunID,
				PromptID:           answer.PromptID,
				PromptText:         texts[answer.PromptID],
				Provider:           answer.Provider,
				Model:              answer.Model,
				CreatedAt:          answer.CreatedAt.UTC(),
				AnswerText:         answer.AnswerText,
				BrandMentioned:     answer.BrandMentioned,
				FirstMentionPos:    answer.FirstMentionPos,
				LatencyMs:          answer.LatencyMs,
				Error:              answer.Error,
				CompetitorMentions: mentions,
				Citations:          answerCitations,
			})
		}
	}

	if len(runIDs) > 0 {
		runs, err := reader.ListRunsByIDs(ctx, runIDs)
		if err != nil {
			aeoExportLogFailure(logger, "run_read_failed", err)
			return nil, err
		}
		for _, run := range runs {
			export.Runs = append(export.Runs, models.AEOExportRun{
				ID:            run.ID,
				Trigger:       run.Trigger,
				Status:        run.Status,
				StartedAt:     run.StartedAt.UTC(),
				CompletedAt:   aeoExportUTC(run.CompletedAt),
				TotalQueries:  run.TotalQueries,
				FailedQueries: run.FailedQueries,
				TriggeredByID: run.TriggeredByID,
			})
		}
	}

	logger.WithFields(logrus.Fields{
		"answers":   len(export.Answers),
		"citations": citationCount,
		"runs":      len(export.Runs),
	}).Info("AEO export assembled")
	return export, nil
}

// aeoExportUTC copies an optional timestamp in UTC.
func aeoExportUTC(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

// aeoExportLogFailure logs a failed export read with its error code and the
// error reduced by utils.LogError (a database error, never row content).
func aeoExportLogFailure(logger *logrus.Entry, code string, err error) {
	logger.WithField("error_code", code).WithError(utils.LogError(err)).Error("AEO export read failed")
}
