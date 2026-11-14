package handler

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// AEO export (#244): GET /aeo/export downloads the answers and citations of
// one run or of a 7/30/90-day window as CSV or JSON.

const (
	aeoExportFormatCSV  = "csv"
	aeoExportFormatJSON = "json"

	// aeoExportErrorTooLarge is the error.code of the 422 answer.
	aeoExportErrorTooLarge = "EXPORT_TOO_LARGE"
)

// aeoExportCSVColumns pins the CSV header. One row per citation; an answer
// without citations has one row with the five citation cells empty. There is
// deliberately no answer_text column: answers are long markdown, and the JSON
// format carries them.
var aeoExportCSVColumns = []string{
	"run_id", "run_trigger", "run_status", "run_started_at",
	"answer_id", "answer_created_at", "prompt_id", "prompt_text",
	"provider", "model", "brand_mentioned", "first_mention_pos", "latency_ms",
	"error", "competitor_mentions",
	"citation_id", "citation_url", "citation_domain", "citation_is_owned", "citation_competitor_name",
}

// Export godoc
// @Summary Export AEO results as CSV or JSON
// @Description Download the stored answers and their citations for one run (run_id) or for a window of 7, 30 or 90 days (days), as a CSV or JSON attachment. Without run_id and days the window is 30 days; without format the format is csv. The window is [start of tomorrow UTC minus days, start of tomorrow UTC) on the answer's created_at, computed by the server clock, so today's answers are included. run_id and days together, a format other than csv or json, a run_id that is not a positive integer or a days value other than 7, 30 or 90 are refused (no fallback). Answers are ordered by created_at, then id; soft-deleted answers and citations are excluded, the text of a deleted prompt is kept. No pagination and no sort parameter.
// @Description The success body is NOT the JSON envelope: it is the raw file. CSV: Content-Type text/csv; charset=utf-8, Content-Disposition attachment; filename=aeo-export-run-<id>.csv or aeo-export-<days>d-<YYYY-MM-DD>.csv, where the date is the last day of the window (UTC). Columns, in this order: run_id, run_trigger, run_status, run_started_at, answer_id, answer_created_at, prompt_id, prompt_text, provider, model, brand_mentioned, first_mention_pos, latency_ms, error, competitor_mentions, citation_id, citation_url, citation_domain, citation_is_owned, citation_competitor_name. One row per citation, and one row with empty citation cells per answer without citations; the answer text is not in the CSV. Timestamps are RFC 3339 UTC, booleans true/false, competitor_mentions is name=count pairs sorted by name and joined by ';'. A text cell starting with =, @, a tab, a carriage return, a line feed, a full-width ＝ ＋ － ＠, or with + or - followed by anything other than a plain number, is prefixed with an apostrophe so spreadsheets do not run it as a formula.
// @Description JSON: Content-Type application/json; charset=utf-8, the same file names with .json; the body is models.AEOExport (format_version 1, generated_at, selection, runs, answers with answer_text, competitor_mentions as an object and citations as an array). The run-ownership columns are never exported. An empty selection is a 200 with the header row only, or empty arrays.
// @Description At most 10,000 answers are exported; a larger selection is refused with 422 before any citation is read. Every row is read before the first byte is written, so a failure is a JSON error, never a truncated file. Errors use the JSON envelope and carry no Content-Disposition header.
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce text/csv
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param format query string false "File format" Enums(csv, json) default(csv)
// @Param run_id query int false "Export this run (a positive integer); cannot be combined with days" minimum(1)
// @Param days query int false "Export this window in days; cannot be combined with run_id; 30 when neither is given" Enums(7, 30, 90)
// @Success 200 {object} models.AEOExport "The export file (raw body, not the envelope): CSV text, or this JSON document"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - invalid format, run_id or days, or run_id together with days; error.details is keyed by the parameter name (format, run_id, days) and never echoes the value"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - run_id names no run (message 'AEO run not found')"
// @Failure 422 {object} utils.APIResponse{error=utils.APIError} "EXPORT_TOO_LARGE - the selection holds more than 10,000 answers; error.details.max_answers is 10000; choose a shorter window or a single run"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - the export could not be read; the cause is never echoed"
// @Router /aeo/export [get]
func (h *AEOHandler) Export(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.Export").WithField("operation", "aeo_export")

	format, selection, details := parseAEOExportQuery(c)
	if len(details) > 0 {
		// Neither the raw values nor the refused parameter names' values are
		// logged; the error code is enough to count refusals.
		logger.WithField("error_code", utils.ErrCodeValidation).Warn("AEO export refused: invalid query parameters")
		utils.RespondValidationError(c, details)
		return
	}
	logger = logger.WithFields(aeoExportLogFields(format, selection))

	export, err := h.aeoService.Export(c.Request.Context(), selection)
	if err != nil {
		h.respondExportError(c, logger, err)
		return
	}
	if export == nil {
		logger.WithField("error_code", utils.ErrCodeInternal).Error("AEO export returned nothing")
		utils.RespondInternalError(c)
		return
	}
	if export.Runs == nil {
		export.Runs = []models.AEOExportRun{}
	}
	if export.Answers == nil {
		export.Answers = []models.AEOExportAnswer{}
	}

	filename := aeoExportFilename(format, selection, export.Selection)

	// The whole file is rendered into memory before the status line goes out,
	// so an encoding failure is still a JSON 500 rather than a truncated file.
	var body []byte
	var contentType string
	switch format {
	case aeoExportFormatJSON:
		body, err = json.Marshal(export)
		contentType = "application/json; charset=utf-8"
	default:
		body, err = aeoExportCSV(export)
		contentType = "text/csv; charset=utf-8"
	}
	if err != nil {
		logger.WithField("error_code", "encode_failed").Error("AEO export could not be encoded")
		utils.RespondInternalError(c)
		return
	}

	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Data(http.StatusOK, contentType, body)
	utils.LogHandlerResponse(logger, http.StatusOK, nil)
}

// respondExportError maps the export's service errors. The error text is not
// logged here: the service already logged the cause with its own code.
func (h *AEOHandler) respondExportError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, service.ErrAEOExportTooLarge):
		logger.WithField("error_code", aeoExportErrorTooLarge).Warn("AEO export refused: too many answers")
		utils.RespondError(c, http.StatusUnprocessableEntity, aeoExportErrorTooLarge,
			fmt.Sprintf("The export selects more than %d answers; choose a shorter window or a single run", service.AEOExportMaxAnswers),
			gin.H{"max_answers": service.AEOExportMaxAnswers})
	case apperrors.IsNotFound(err):
		logger.WithField("error_code", utils.ErrCodeNotFound).Warn("AEO export refused: run not found")
		utils.RespondNotFound(c, "AEO run not found")
	default:
		logger.WithField("error_code", utils.ErrCodeInternal).Error("AEO export failed")
		utils.RespondInternalError(c)
	}
}

// parseAEOExportQuery validates format, run_id and days. An empty value counts
// as absent. details is keyed by the parameter name and never echoes a value;
// it is empty when the query is valid.
func parseAEOExportQuery(c *gin.Context) (string, models.AEOExportSelection, map[string]string) {
	details := map[string]string{}
	var selection models.AEOExportSelection

	format := c.Query("format")
	switch format {
	case "":
		format = aeoExportFormatCSV
	case aeoExportFormatCSV, aeoExportFormatJSON:
	default:
		details["format"] = "format must be csv or json"
	}

	rawRunID := c.Query("run_id")
	rawDays := c.Query("days")

	if rawRunID != "" && rawDays != "" {
		details["run_id"] = "run_id cannot be combined with days"
		details["days"] = "days cannot be combined with run_id"
		return format, selection, details
	}

	switch {
	case rawRunID != "":
		parsed, err := strconv.ParseUint(rawRunID, 10, 32)
		if err != nil || parsed == 0 {
			details["run_id"] = "run_id must be a positive integer"
		} else {
			selection.RunID = uint(parsed)
		}
	case rawDays != "":
		parsed, err := strconv.Atoi(rawDays)
		if err != nil || !aeoAllowedRangeDays[parsed] {
			details["days"] = "days must be 7, 30 or 90"
		} else {
			selection.Days = parsed
		}
	default:
		selection.Days = aeoDefaultRangeDays
	}
	return format, selection, details
}

// aeoExportLogFields are the only request fields an export log line carries:
// the validated format, run_id or days.
func aeoExportLogFields(format string, selection models.AEOExportSelection) logrus.Fields {
	fields := logrus.Fields{"format": format}
	if selection.RunID != 0 {
		fields["run_id"] = selection.RunID
	} else {
		fields["days"] = selection.Days
	}
	return fields
}

// aeoExportFilename names the attachment. A window's date is the last day of
// the window, the returned selection's exclusive end minus 24 hours in UTC;
// the handler reads no clock of its own.
func aeoExportFilename(format string, requested, returned models.AEOExportSelection) string {
	if requested.RunID != 0 {
		return fmt.Sprintf("aeo-export-run-%d.%s", requested.RunID, format)
	}
	if returned.To == nil {
		return fmt.Sprintf("aeo-export-%dd.%s", requested.Days, format)
	}
	day := returned.To.UTC().Add(-24 * time.Hour).Format("2006-01-02")
	return fmt.Sprintf("aeo-export-%dd-%s.%s", requested.Days, day, format)
}

// aeoExportCSV renders the export as CSV: the pinned header, then one row per
// citation in answer order then citation order, and one row with empty
// citation cells per answer without citations.
func aeoExportCSV(export *models.AEOExport) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write(aeoExportCSVColumns); err != nil {
		return nil, err
	}
	for _, record := range aeoExportCSVRecords(export) {
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// aeoExportCSVRecords builds the data rows (without the header). Every
// free-text cell passes through aeoCSVSafeField.
func aeoExportCSVRecords(export *models.AEOExport) [][]string {
	runs := make(map[uint]models.AEOExportRun, len(export.Runs))
	for _, run := range export.Runs {
		runs[run.ID] = run
	}

	records := make([][]string, 0, len(export.Answers))
	for _, answer := range export.Answers {
		run, known := runs[answer.RunID]
		answerCells := []string{
			strconv.FormatUint(uint64(answer.RunID), 10),
			"", "", "",
			strconv.FormatUint(uint64(answer.ID), 10),
			aeoExportTime(answer.CreatedAt),
			strconv.FormatUint(uint64(answer.PromptID), 10),
			aeoCSVSafeField(answer.PromptText),
			aeoCSVSafeField(answer.Provider),
			aeoCSVSafeField(answer.Model),
			strconv.FormatBool(answer.BrandMentioned),
			strconv.Itoa(answer.FirstMentionPos),
			strconv.Itoa(answer.LatencyMs),
			aeoCSVSafeField(answer.Error),
			aeoCSVSafeField(aeoExportCompetitorMentions(answer.CompetitorMentions)),
		}
		if known {
			answerCells[1] = aeoCSVSafeField(run.Trigger)
			answerCells[2] = aeoCSVSafeField(run.Status)
			answerCells[3] = aeoExportTime(run.StartedAt)
		}

		if len(answer.Citations) == 0 {
			records = append(records, append(append([]string{}, answerCells...), "", "", "", "", ""))
			continue
		}
		for _, citation := range answer.Citations {
			records = append(records, append(append([]string{}, answerCells...),
				strconv.FormatUint(uint64(citation.ID), 10),
				aeoCSVSafeField(citation.URL),
				aeoCSVSafeField(citation.Domain),
				strconv.FormatBool(citation.IsOwned),
				aeoCSVSafeField(citation.CompetitorName),
			))
		}
	}
	return records
}

// aeoExportTime formats a timestamp as RFC 3339 in UTC.
func aeoExportTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// aeoExportCompetitorMentions encodes the counts as name=count pairs sorted by
// name and joined by ';'.
func aeoExportCompetitorMentions(mentions map[string]int) string {
	if len(mentions) == 0 {
		return ""
	}
	names := make([]string, 0, len(mentions))
	for name := range mentions {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+strconv.Itoa(mentions[name]))
	}
	return strings.Join(pairs, ";")
}

// aeoCSVSafeField is csvSafeField plus the cases an AEO cell needs on top: a
// leading line feed and the full-width ＝ ＋ － ＠ (U+FF1D, U+FF0B, U+FF0D,
// U+FF20), which some spreadsheet locales normalise to their ASCII forms
// before deciding whether a cell is a formula. Answer engines and prompt
// generation produce such text routinely, so every free-text AEO cell passes
// through here. csvSafeField itself, and with it the customer and lead
// exports, is unchanged.
func aeoCSVSafeField(value string) string {
	if value == "" {
		return value
	}
	if guarded := csvSafeField(value); guarded != value {
		return guarded
	}
	first, _ := utf8.DecodeRuneInString(value)
	switch first {
	case '\n', '＝', '＋', '－', '＠':
		return "'" + value
	}
	return value
}
