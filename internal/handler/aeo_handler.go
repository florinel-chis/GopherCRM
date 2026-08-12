package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// AEOHandler serves the Answer Engine Optimization endpoints: the brand
// profile, the tracked prompts, the run history and the aggregated visibility
// and citation reports.
type AEOHandler struct {
	aeoService service.AEOService
}

func NewAEOHandler(aeoService service.AEOService) *AEOHandler {
	return &AEOHandler{aeoService: aeoService}
}

// aeoDefaultRangeDays is the reporting window used when ?days= is absent or
// carries a value outside the supported set. The window is clamped rather than
// rejected, in keeping with the other range parameters in this API.
const aeoDefaultRangeDays = 30

// aeoAllowedRangeDays are the only windows the dashboard, the citation report
// and the per-prompt visibility figures accept.
var aeoAllowedRangeDays = map[int]bool{7: true, 30: true, 90: true}

// aeoPromptSortColumns and aeoRunSortColumns mirror the repository's sort
// allowlists. Validating here as well turns a bad sort_by into a 400 with a
// usable message instead of letting the repository's error surface as an
// opaque 500. The lists are fixed by the module contract, so the duplication
// cannot drift silently.
var (
	aeoPromptSortColumns = map[string]bool{
		"id": true, "text": true, "is_active": true, "created_at": true, "updated_at": true,
	}
	aeoRunSortColumns = map[string]bool{
		"id": true, "status": true, "trigger": true, "started_at": true,
		"completed_at": true, "created_at": true,
	}
)

// AEOCompetitorRequest is one competitor entry of the brand profile body.
type AEOCompetitorRequest struct {
	Name    string   `json:"name" binding:"required,max=120"`
	Aliases []string `json:"aliases" binding:"omitempty,max=20,dive,max=120"`
	Domain  string   `json:"domain" binding:"omitempty,max=255"`
}

// SaveAEOProfileRequest is the body of PUT /aeo/profile. The profile is a
// single row, so this endpoint both creates and updates it.
type SaveAEOProfileRequest struct {
	BrandName    string                 `json:"brand_name" binding:"required,max=120"`
	Description  string                 `json:"description" binding:"omitempty,max=2000"`
	BrandAliases []string               `json:"brand_aliases" binding:"omitempty,max=20,dive,max=120"`
	OwnedDomains []string               `json:"owned_domains" binding:"omitempty,max=20,dive,max=255"`
	Competitors  []AEOCompetitorRequest `json:"competitors" binding:"omitempty,max=20,dive"`
}

// CreateAEOPromptsRequest is the body of POST /aeo/prompts. Several prompts
// may be added at once; the batch is all-or-nothing, so one duplicate rejects
// the whole request.
type CreateAEOPromptsRequest struct {
	Prompts []string `json:"prompts" binding:"required,min=1,max=25,dive,required,max=500"`
}

// UpdateAEOPromptRequest is the body of PUT /aeo/prompts/:id. Both fields are
// pointers so that an absent field is distinguishable from "is_active": false.
type UpdateAEOPromptRequest struct {
	Text     *string `json:"text,omitempty" binding:"omitempty,max=500"`
	IsActive *bool   `json:"is_active,omitempty"`
}

// GenerateAEOPromptsRequest is the body of POST /aeo/prompts/generate. The
// body itself is optional; an empty request generates the default count.
type GenerateAEOPromptsRequest struct {
	Count int `json:"count,omitempty" binding:"omitempty,min=1,max=25"`
}

// GetProfile godoc
// @Summary Get the AEO brand profile
// @Description The single brand profile that drives mention detection: brand name, aliases, owned domains, business description and the tracked competitors. Returns 404 until the profile has been saved for the first time. Available to admin, sales and support; the customer role is rejected with 403.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=models.AEOProfile} "Profile retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "The brand profile has not been configured yet"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/profile [get]
func (h *AEOHandler) GetProfile(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.GetProfile")

	profile, err := h.aeoService.GetProfile()
	if err != nil {
		// This is the one route where an unconfigured profile is a plain
		// absence rather than a precondition failure, so it answers 404 here
		// instead of the 409 the shared mapper returns everywhere else.
		if errors.Is(err, apperrors.ErrProfileNotConfigured) {
			logger.WithError(err).Warn("AEO profile not configured")
			utils.RespondNotFound(c, "AEO profile not found")
			return
		}
		h.respondError(c, logger, err, "AEO profile not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, profile)
	utils.RespondSuccess(c, http.StatusOK, profile)
}

// SaveProfile godoc
// @Summary Create or update the AEO brand profile
// @Description Save the single brand profile (admin and sales only). Aliases and domains are trimmed and de-duplicated by the service, domains are lowercased and a leading "www." is stripped, so the stored form matches what the citation analysis compares against. At most 20 competitors, and at most 20 aliases or domains in each list.
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body SaveAEOProfileRequest true "Brand profile"
// @Success 200 {object} utils.APIResponse{data=models.AEOProfile} "Profile saved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/profile [put]
func (h *AEOHandler) SaveProfile(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.SaveProfile")

	var req SaveAEOProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	profile := &models.AEOProfile{
		BrandName:    req.BrandName,
		Description:  req.Description,
		BrandAliases: req.BrandAliases,
		OwnedDomains: req.OwnedDomains,
	}
	for _, competitor := range req.Competitors {
		profile.Competitors = append(profile.Competitors, models.AEOCompetitor{
			Name:    competitor.Name,
			Aliases: competitor.Aliases,
			Domain:  competitor.Domain,
		})
	}

	saved, err := h.aeoService.SaveProfile(profile)
	if err != nil {
		h.respondError(c, logger, err, "AEO profile not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, saved)
	utils.RespondSuccess(c, http.StatusOK, saved)
}

// ListPrompts godoc
// @Summary List tracked AEO prompts
// @Description The tracked prompts, each carrying its visibility percentage over the requested window (share of non-error answers that mention the brand, 0..100 with one decimal), the answer and mention counts behind that figure, and the timestamp of the most recent answer. The window is the same 7/30/90-day selector the dashboard uses; any other value falls back to 30. The total is reported in the response meta.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param days query int false "Reporting window in days (7, 30 or 90)" default(30)
// @Param active_only query bool false "Return only active prompts" default(false)
// @Param offset query int false "Pagination offset" default(0)
// @Param limit query int false "Page size (max 100)" default(20)
// @Param sort_by query string false "Sort column" Enums(id, text, is_active, created_at, updated_at)
// @Param sort_order query string false "Sort direction" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AEOPrompt,meta=utils.APIMeta} "Prompts retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Unsupported sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/prompts [get]
func (h *AEOHandler) ListPrompts(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.ListPrompts")

	from, to, _ := aeoReportingRange(c)
	activeOnly := aeoQueryBool(c, "active_only")
	offset, limit := utils.ParseOffsetLimit(c)

	sortBy, sortOrder, err := aeoSortParams(c, aeoPromptSortColumns)
	if err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	prompts, total, err := h.aeoService.ListPrompts(from, to, activeOnly, offset, limit, sortBy, sortOrder)
	if err != nil {
		h.respondError(c, logger, err, "AEO prompt not found")
		return
	}
	if prompts == nil {
		prompts = []models.AEOPrompt{}
	}

	meta := aeoListMeta(c, offset, limit, total)
	utils.LogHandlerResponse(logger, http.StatusOK, prompts)
	utils.RespondSuccessWithMeta(c, http.StatusOK, prompts, meta)
}

// CreatePrompts godoc
// @Summary Add tracked AEO prompts
// @Description Add one or more prompts to track (admin and sales only). Up to 25 per request, each at most 500 characters. The batch is written in a single transaction, so a duplicate anywhere in it saves nothing and answers 409. Prompt text is unique case-insensitively among live prompts; a soft-deleted prompt does not reserve its text. Exceeding the cap of 100 active prompts is answered with 400.
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateAEOPromptsRequest true "Prompts to add"
// @Success 201 {object} utils.APIResponse{data=[]models.AEOPrompt} "Prompts created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data or the active prompt limit has been reached"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A prompt with this text already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/prompts [post]
func (h *AEOHandler) CreatePrompts(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.CreatePrompts")

	var req CreateAEOPromptsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	prompts, err := h.aeoService.CreatePrompts(req.Prompts, c.GetUint("user_id"))
	if err != nil {
		h.respondError(c, logger, err, "AEO prompt not found")
		return
	}
	if prompts == nil {
		prompts = []models.AEOPrompt{}
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, prompts)
	utils.RespondSuccess(c, http.StatusCreated, prompts)
}

// UpdatePrompt godoc
// @Summary Update a tracked AEO prompt
// @Description Edit a prompt's text or toggle whether it is included in future runs (admin and sales only). Both fields are optional and only the ones present in the body are applied, so deactivating a prompt never rewrites its text. Historical answers keep pointing at the prompt whatever the edit.
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Param request body UpdateAEOPromptRequest true "Fields to update"
// @Success 200 {object} utils.APIResponse{data=models.AEOPrompt} "Prompt updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid prompt ID, blank text or invalid request data"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Prompt not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A prompt with this text already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/prompts/{id} [put]
func (h *AEOHandler) UpdatePrompt(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.UpdatePrompt")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid prompt ID")
		return
	}

	var req UpdateAEOPromptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	// "max=500" says nothing about a text of only whitespace, and a blank
	// prompt would be rejected deeper down as an unclassified error, so it is
	// caught here where it can still be reported as a validation failure.
	if req.Text != nil && strings.TrimSpace(*req.Text) == "" {
		utils.RespondBadRequest(c, "Prompt text cannot be blank")
		return
	}

	prompt, err := h.aeoService.UpdatePrompt(uint(id), req.Text, req.IsActive)
	if err != nil {
		h.respondError(c, logger, err, "AEO prompt not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, prompt)
	utils.RespondSuccess(c, http.StatusOK, prompt)
}

// DeletePrompt godoc
// @Summary Delete a tracked AEO prompt
// @Description Soft-delete a prompt (admin only) so it is excluded from future runs. The answers already collected for it are retained, which is why this is a soft delete and why the prompt's text is not freed for reuse detection by any unique index — uniqueness is checked against live prompts only.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid prompt ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Prompt not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/prompts/{id} [delete]
func (h *AEOHandler) DeletePrompt(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.DeletePrompt")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid prompt ID")
		return
	}

	if err := h.aeoService.DeletePrompt(uint(id)); err != nil {
		h.respondError(c, logger, err, "AEO prompt not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// GeneratePrompts godoc
// @Summary Generate candidate AEO prompts
// @Description Ask the Anthropic model for buyer-style questions derived from the brand profile (admin and sales only). Nothing is stored: the suggestions come back as plain strings and are only tracked once POSTed to /aeo/prompts. The body is optional and defaults to 10 suggestions. Requires the brand profile to exist (409 otherwise) and an Anthropic API key, from the admin key settings or ANTHROPIC_API_KEY (503 with code PROVIDER_NOT_CONFIGURED otherwise).
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body GenerateAEOPromptsRequest false "How many suggestions to generate (1..25, default 10)"
// @Success 200 {object} utils.APIResponse{data=object{prompts=[]string}} "Suggestions generated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The brand profile has not been configured yet"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "The prompt generation provider is not configured"
// @Router /aeo/prompts/generate [post]
func (h *AEOHandler) GeneratePrompts(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.GeneratePrompts")

	// The body is optional, so an empty request is not a binding failure.
	var req GenerateAEOPromptsRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	count := req.Count
	if count == 0 {
		count = 10
	}

	texts, err := h.aeoService.GeneratePrompts(c.Request.Context(), count)
	if err != nil {
		// Generation runs on one specific provider, so a missing key there
		// gets its own code and an error that names the engine, rather than
		// the module-wide "no providers" answer.
		if errors.Is(err, apperrors.ErrGenerationProviderNotConfigured) {
			logger.WithError(err).Warn("Prompt generation provider not configured")
			utils.RespondError(c, http.StatusServiceUnavailable, "PROVIDER_NOT_CONFIGURED", err.Error(), nil)
			return
		}
		// A configured engine that answers is a different failure from a
		// missing one: a 4xx from the engine almost always means the stored
		// key is wrong (or out of quota), and saying so beats a bare 500.
		// Answered as 503, not 502: proxies (Cloudflare included) replace an
		// origin 502 body with their own error page, which would swallow
		// this message.
		if status := aeo.ProviderHTTPStatus(err); status != 0 {
			logger.WithError(err).WithField("provider_status", status).
				Warn("Prompt generation rejected by the engine")
			message := fmt.Sprintf("the generation engine is unavailable (HTTP %d); try again later", status)
			if status >= 400 && status < 500 {
				message = fmt.Sprintf(
					"the generation engine rejected the request (HTTP %d) — check its API key in the AEO settings", status)
			}
			utils.RespondError(c, http.StatusServiceUnavailable, "PROVIDER_REJECTED", message, nil)
			return
		}
		h.respondError(c, logger, err, "AEO profile not found")
		return
	}
	if texts == nil {
		texts = []string{}
	}

	responseData := gin.H{"prompts": texts}
	utils.LogHandlerResponse(logger, http.StatusOK, responseData)
	utils.RespondSuccess(c, http.StatusOK, responseData)
}

// ListPromptAnswers godoc
// @Summary List the answers collected for a prompt
// @Description The stored answers for one prompt, newest first, each with its provider, model, latency, mention analysis and extracted citations. Failed provider calls are stored too and come back with a populated error field and empty text. Pass run_id to narrow the list to a single run; omit it to page through every run.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Param run_id query int false "Restrict to one run"
// @Param offset query int false "Pagination offset" default(0)
// @Param limit query int false "Page size (max 100)" default(20)
// @Success 200 {object} utils.APIResponse{data=[]models.AEOAnswer,meta=utils.APIMeta} "Answers retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid prompt ID or run ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Prompt not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/prompts/{id}/answers [get]
func (h *AEOHandler) ListPromptAnswers(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.ListPromptAnswers")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid prompt ID")
		return
	}

	var runID *uint
	if raw := c.Query("run_id"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			utils.RespondBadRequest(c, "Invalid run ID")
			return
		}
		value := uint(parsed)
		runID = &value
	}

	offset, limit := utils.ParseOffsetLimit(c)

	answers, total, err := h.aeoService.GetPromptAnswers(uint(id), runID, offset, limit)
	if err != nil {
		h.respondError(c, logger, err, "AEO prompt not found")
		return
	}
	if answers == nil {
		answers = []models.AEOAnswer{}
	}

	meta := aeoListMeta(c, offset, limit, total)
	utils.LogHandlerResponse(logger, http.StatusOK, answers)
	utils.RespondSuccessWithMeta(c, http.StatusOK, answers, meta)
}

// CreateRun godoc
// @Summary Start an AEO run
// @Description Queue every active prompt against every configured provider (admin and sales only) and return immediately with the run row in status "running"; the work continues in the background, so poll GET /aeo/runs/{id} for progress. Only one run may be in flight at a time, which is what the manual and the scheduled trigger share as an overlap guard.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 202 {object} utils.APIResponse{data=models.AEORun} "Run accepted and started"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "There are no active prompts to run"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A run is already in progress, or the brand profile has not been configured"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "No AEO providers are configured"
// @Router /aeo/runs [post]
func (h *AEOHandler) CreateRun(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.CreateRun")

	var triggeredByID *uint
	if userID := c.GetUint("user_id"); userID != 0 {
		triggeredByID = &userID
	}

	run, err := h.aeoService.StartRun(c.Request.Context(), "manual", triggeredByID)
	if err != nil {
		h.respondError(c, logger, err, "No active AEO prompts to run")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusAccepted, run)
	utils.RespondSuccess(c, http.StatusAccepted, run)
}

// RunPrompt godoc
// @Summary Run a single AEO prompt
// @Description Queue ONE prompt — active or not, which is how a draft prompt gets tested before joining the daily run — against every configured provider (admin and sales only). Returns immediately with the run row in status "running"; the same single-run-in-flight overlap guard applies as for a full run.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Success 202 {object} utils.APIResponse{data=models.AEORun} "Run accepted and started"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid prompt id"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Prompt not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A run is already in progress, or the brand profile has not been configured"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "No AEO providers are configured"
// @Router /aeo/prompts/{id}/run [post]
func (h *AEOHandler) RunPrompt(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.RunPrompt")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		utils.RespondValidationError(c, "Invalid prompt id")
		return
	}

	var triggeredByID *uint
	if userID := c.GetUint("user_id"); userID != 0 {
		triggeredByID = &userID
	}

	run, err := h.aeoService.StartPromptRun(c.Request.Context(), uint(id), triggeredByID)
	if err != nil {
		h.respondError(c, logger, err, "Prompt not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusAccepted, run)
	utils.RespondSuccess(c, http.StatusAccepted, run)
}

// ListRuns godoc
// @Summary List AEO runs
// @Description The run history, newest first by default, with the trigger, the status and the query counters of each batch. The total is reported in the response meta.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset" default(0)
// @Param limit query int false "Page size (max 100)" default(20)
// @Param sort_by query string false "Sort column" Enums(id, status, trigger, started_at, completed_at, created_at)
// @Param sort_order query string false "Sort direction" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AEORun,meta=utils.APIMeta} "Runs retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Unsupported sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/runs [get]
func (h *AEOHandler) ListRuns(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.ListRuns")

	offset, limit := utils.ParseOffsetLimit(c)
	sortBy, sortOrder, err := aeoSortParams(c, aeoRunSortColumns)
	if err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	runs, total, err := h.aeoService.ListRuns(offset, limit, sortBy, sortOrder)
	if err != nil {
		h.respondError(c, logger, err, "AEO run not found")
		return
	}
	if runs == nil {
		runs = []models.AEORun{}
	}

	meta := aeoListMeta(c, offset, limit, total)
	utils.LogHandlerResponse(logger, http.StatusOK, runs)
	utils.RespondSuccessWithMeta(c, http.StatusOK, runs, meta)
}

// GetRun godoc
// @Summary Get one AEO run
// @Description A single run with its current status and counters. This is the endpoint to poll after POST /aeo/runs: the status moves from "running" to "completed", "partial" (some provider calls failed) or "failed" (all of them did).
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Run ID"
// @Success 200 {object} utils.APIResponse{data=models.AEORun} "Run retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid run ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Run not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/runs/{id} [get]
func (h *AEOHandler) GetRun(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.GetRun")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid run ID")
		return
	}

	run, err := h.aeoService.GetRun(uint(id))
	if err != nil {
		h.respondError(c, logger, err, "AEO run not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, run)
	utils.RespondSuccess(c, http.StatusOK, run)
}

// GetDashboard godoc
// @Summary AEO visibility dashboard
// @Description Aggregated visibility over the requested window: the overall percentage of non-error answers mentioning the brand, the same figure per provider, a daily timeline with one entry per day in range (days without answers are present with an overall of 0 and no per-provider entries), the share of voice across the brand and its competitors, and the competitor timeline. Windows are 7, 30 or 90 days; anything else falls back to 30. The upper bound is exclusive and is the start of tomorrow in UTC.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param days query int false "Reporting window in days (7, 30 or 90)" default(30)
// @Success 200 {object} utils.APIResponse{data=models.AEODashboard} "Dashboard retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The brand profile has not been configured yet"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/dashboard [get]
func (h *AEOHandler) GetDashboard(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.GetDashboard")

	from, to, _ := aeoReportingRange(c)

	dashboard, err := h.aeoService.Dashboard(from, to)
	if err != nil {
		h.respondError(c, logger, err, "AEO data not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, dashboard)
	utils.RespondSuccess(c, http.StatusOK, dashboard)
}

// GetCitations godoc
// @Summary AEO citation report
// @Description Which sources the answers cite over the requested window: the owned-domain citation rate, the per-company citation and brand-mention rates (the brand first, then each competitor in profile order) and the twenty most cited domains, ordered by citation count descending. Windows are 7, 30 or 90 days; anything else falls back to 30.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param days query int false "Reporting window in days (7, 30 or 90)" default(30)
// @Success 200 {object} utils.APIResponse{data=models.AEOCitationsReport} "Citation report retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The brand profile has not been configured yet"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /aeo/citations [get]
func (h *AEOHandler) GetCitations(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.GetCitations")

	from, to, _ := aeoReportingRange(c)

	report, err := h.aeoService.Citations(from, to)
	if err != nil {
		h.respondError(c, logger, err, "AEO data not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, report)
	utils.RespondSuccess(c, http.StatusOK, report)
}

// GetProviders godoc
// @Summary List the AEO answer engines
// @Description Every supported engine with the model it would use and whether it is configured. An engine without an API key is reported with configured=false and is skipped by runs; keys themselves are never returned.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]models.AEOProviderStatus} "Provider statuses retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /aeo/providers [get]
func (h *AEOHandler) GetProviders(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.GetProviders")

	providers := h.aeoService.Providers()
	if providers == nil {
		providers = []models.AEOProviderStatus{}
	}

	utils.LogHandlerResponse(logger, http.StatusOK, providers)
	utils.RespondSuccess(c, http.StatusOK, providers)
}

// respondError maps the AEO sentinels onto status codes. An unconfigured
// profile is a precondition failure for every route except GET /aeo/profile,
// which handles it inline as a 404 before calling this. Unclassified errors
// are server errors and are never echoed back to the client.
func (h *AEOHandler) respondError(c *gin.Context, logger *logrus.Entry, err error, notFoundMessage string) {
	switch {
	case errors.Is(err, apperrors.ErrDuplicatePrompt):
		logger.WithError(err).Warn("Duplicate AEO prompt")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrRunInProgress):
		logger.WithError(err).Warn("AEO run already in progress")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrProfileNotConfigured):
		logger.WithError(err).Warn("AEO profile not configured")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrNoProvidersConfigured):
		logger.WithError(err).Warn("No AEO providers configured")
		utils.RespondError(c, http.StatusServiceUnavailable, "PROVIDERS_UNAVAILABLE", err.Error(), nil)
	case errors.Is(err, apperrors.ErrGenerationProviderNotConfigured):
		logger.WithError(err).Warn("Prompt generation provider not configured")
		utils.RespondError(c, http.StatusServiceUnavailable, "PROVIDER_NOT_CONFIGURED", err.Error(), nil)
	case errors.Is(err, service.ErrAEOPromptLimit):
		logger.WithError(err).Warn("AEO active prompt limit reached")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case errors.Is(err, service.ErrAEOInvalidPrompt),
		errors.Is(err, service.ErrAEOInvalidProfile),
		errors.Is(err, service.ErrAEOInvalidTrigger):
		// The service validates beyond what the binding tags can express —
		// whitespace-only text passes gin's `required` but is empty once
		// trimmed. Those are client mistakes, not server faults.
		logger.WithError(err).Warn("AEO request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case apperrors.IsNotFound(err):
		logger.WithError(err).Warn("AEO resource not found")
		utils.RespondNotFound(c, notFoundMessage)
	default:
		logger.WithError(err).Error("AEO operation failed")
		utils.RespondInternalError(c)
	}
}

// aeoReportingRange derives the reporting window from ?days=. The upper bound
// is exclusive and is the start of tomorrow in UTC, so today's answers are
// always included whatever the caller's timezone.
func aeoReportingRange(c *gin.Context) (from, to time.Time, days int) {
	days = aeoDefaultRangeDays
	if raw := c.Query("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && aeoAllowedRangeDays[parsed] {
			days = parsed
		}
	}

	to = time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	from = to.AddDate(0, 0, -days)
	return from, to, days
}

// aeoQueryBool reads an optional boolean query parameter. An unparseable value
// is treated as absent rather than rejected, matching the other list filters.
func aeoQueryBool(c *gin.Context, key string) bool {
	raw := c.Query(key)
	if raw == "" {
		return false
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false
	}
	return parsed
}

// aeoSortParams validates sort_by against the allowlist for the entity being
// listed. An empty sort_by is passed through untouched so the repository can
// apply its own default; an unknown column is a client error. sort_order is
// normalised the same way the shared sort validator normalises it: anything
// other than asc becomes desc.
func aeoSortParams(c *gin.Context, allowed map[string]bool) (sortBy, sortOrder string, err error) {
	sortBy = strings.TrimSpace(c.Query("sort_by"))
	sortOrder = strings.ToLower(strings.TrimSpace(c.Query("sort_order")))

	if sortBy != "" && !allowed[sortBy] {
		return "", "", errors.New("Invalid sort column: " + sortBy)
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "desc"
	}
	return sortBy, sortOrder, nil
}

// aeoListMeta builds the pagination metadata for the AEO list endpoints.
// utils.ParseOffsetLimit never returns a zero limit, so the arithmetic here is
// safe without a further guard.
func aeoListMeta(c *gin.Context, offset, limit int, total int64) *utils.APIMeta {
	return &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
}
