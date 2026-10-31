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
	// Competitor name; required, at most 120 characters. Trimmed on save; an
	// entry whose name is only whitespace is silently dropped.
	Name string `json:"name" binding:"required,max=120" example:"Globex CRM" maxLength:"120"`
	// Alternative spellings counted as mentions of this competitor: at most 20,
	// each at most 120 characters. Trimmed, blanks dropped and de-duplicated
	// case-insensitively on save.
	Aliases []string `json:"aliases" binding:"omitempty,max=20,dive,max=120" example:"Globex,Globex Corporation" maxItems:"20"`
	// Competitor website, at most 255 characters. Any form is accepted
	// ("https://www.Globex.example/about") and stored as a bare lower-case host
	// without scheme, path or leading "www.".
	Domain string `json:"domain" binding:"omitempty,max=255" example:"globex.example" maxLength:"255"`
}

// SaveAEOProfileRequest is the body of PUT /aeo/profile. The profile is a
// single row, so this endpoint both creates and updates it.
type SaveAEOProfileRequest struct {
	// Brand name searched for in answers; required, at most 120 characters, and
	// not blank once trimmed.
	BrandName string `json:"brand_name" binding:"required,max=120" example:"Acme CRM" maxLength:"120"`
	// What the business does; context for prompt generation only. At most 2000
	// characters; trimmed.
	Description string `json:"description" binding:"omitempty,max=2000" example:"Self-hosted CRM for small sales and support teams." maxLength:"2000"`
	// Alternative brand spellings: at most 20, each at most 120 characters.
	// Trimmed, blanks dropped and de-duplicated case-insensitively. Omitting the
	// field clears the list.
	BrandAliases []string `json:"brand_aliases" binding:"omitempty,max=20,dive,max=120" example:"Acme,AcmeCRM" maxItems:"20"`
	// Owned domains: at most 20, each at most 255 characters. Normalised to
	// bare lower-case hosts (scheme, path and leading "www." removed),
	// de-duplicated. Omitting the field clears the list.
	OwnedDomains []string `json:"owned_domains" binding:"omitempty,max=20,dive,max=255" example:"acme.example" maxItems:"20"`
	// Tracked competitors, at most 20, kept in the order sent. Omitting the
	// field clears the list.
	Competitors []AEOCompetitorRequest `json:"competitors" binding:"omitempty,max=20,dive" maxItems:"20"`
}

// CreateAEOPromptsRequest is the body of POST /aeo/prompts. Several prompts
// may be added at once; the batch is all-or-nothing, so one duplicate rejects
// the whole request.
type CreateAEOPromptsRequest struct {
	// Prompt texts to track: 1 to 25 entries, each non-empty and at most 500
	// characters. Entries are trimmed; a whitespace-only entry, a repeat within
	// the request (case-insensitive) or a text matching a live prompt rejects
	// the whole batch.
	Prompts []string `json:"prompts" binding:"required,min=1,max=25,dive,required,max=500" example:"What is the best self-hosted CRM for a small sales team?,Which CRM tools support embeddable lead-capture forms?" minItems:"1" maxItems:"25"`
}

// UpdateAEOPromptRequest is the body of PUT /aeo/prompts/:id. Both fields are
// pointers so that an absent field is distinguishable from "is_active": false.
type UpdateAEOPromptRequest struct {
	// New prompt text; at most 500 characters, not blank once trimmed, unique
	// case-insensitively among live prompts. Absent keeps the current text.
	Text *string `json:"text,omitempty" binding:"omitempty,max=500" example:"Which self-hosted CRM is best for a five-person sales team?" maxLength:"500"`
	// Include (true) or exclude (false) the prompt from full runs. Absent keeps
	// the current state. Reactivating fails when 100 prompts are already active.
	IsActive *bool `json:"is_active,omitempty" example:"false"`
}

// GenerateAEOPromptsRequest is the body of POST /aeo/prompts/generate. The
// body itself is optional; an empty request generates the default count.
type GenerateAEOPromptsRequest struct {
	// Number of suggestions to ask for, 1 to 25; absent or 0 means 10 (0 is
	// accepted: the binding skips a zero value). The engine may return fewer
	// usable suggestions than requested.
	Count int `json:"count,omitempty" binding:"omitempty,min=1,max=25" example:"10" minimum:"0" maximum:"25"`
}

// GetProfile godoc
// @Summary Get the AEO brand profile
// @Description The single brand profile that drives mention detection: brand name, aliases, owned domains, business description and the tracked competitors. There is only ever one profile (id 1). Answers 404 NOT_FOUND until the profile has been saved for the first time (every other AEO route that needs it answers 409 instead).
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=models.AEOProfile} "Profile retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - the brand profile has not been configured yet (message 'AEO profile not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /aeo/profile [get]
func (h *AEOHandler) GetProfile(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AEOHandler.GetProfile")

	profile, err := h.aeoService.GetProfile()
	if err != nil {
		// This is the one route where an unconfigured profile is a plain
		// absence rather than a precondition failure, so it answers 404 here
		// instead of the 409 the shared mapper returns everywhere else.
		if errors.Is(err, apperrors.ErrProfileNotConfigured) {
			logger.WithError(utils.LogError(err)).Warn("AEO profile not configured")
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
// @Description Save the single brand profile; the first call creates it, later calls replace it wholesale (a list left out of the body is cleared). The service trims every value, drops blank aliases, domains and competitors, de-duplicates aliases and domains case-insensitively, and reduces domains to bare lower-case hosts (scheme, path and a leading "www." removed). The response is the stored row. Only answers collected after the save are analysed against the new profile; stored answers keep their mention and citation analysis.
// @Description Roles: admin, sales.
// @Description Errors: 400 VALIDATION_ERROR (message "Validation failed", error.details maps the failing Go struct field name, e.g. BrandName or Prompts[0], to a message) when a binding rule fails; 400 BAD_REQUEST ("Invalid request format") for a body that is not valid JSON or does not match the field types; 400 VALIDATION_ERROR without error.details (the member is omitted) when the brand name is blank once trimmed or a value is too long after trimming (message names the rule, e.g. "brand name is required: invalid brand profile").
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body SaveAEOProfileRequest true "Complete brand profile"
// @Success 200 {object} utils.APIResponse{data=models.AEOProfile} "Profile saved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR or BAD_REQUEST - invalid body or profile (see description)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description One page of tracked prompts (soft-deleted prompts are never listed). Each carries computed figures over the requested window: visibility (share of non-error answers that mention the brand, percentage 0..100 with one decimal), the answer and mention counts behind it and the time of the most recent answer. The window is the 7/30/90-day selector the dashboard uses; any other or unparseable value falls back to 30.
// @Description Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default). meta carries page, per_page, total (prompts matching active_only) and total_pages.
// @Description Sorting: sort_by is one of id, text, is_active, created_at, updated_at; any other value is answered with 400 BAD_REQUEST ("Invalid sort column: <value>"). sort_order is asc or desc; anything else means desc. Without sort_by the order is created_at descending whatever sort_order says. Ties are broken by id in the same direction.
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param days query int false "Reporting window in days; 7, 30 or 90, anything else means 30" Enums(7, 30, 90) default(30)
// @Param active_only query bool false "true returns only active prompts; unparseable values mean false" default(false)
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param sort_by query string false "Sort column (unknown value: 400)" Enums(id, text, is_active, created_at, updated_at)
// @Param sort_order query string false "Sort direction; ignored without sort_by" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AEOPrompt,meta=utils.APIMeta} "Prompts retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - unsupported sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description Add 1 to 25 prompts in one request; new prompts are always active. Texts are trimmed and must be 1 to 500 characters. The batch is all-or-nothing and runs in one transaction: a repeat within the request or a text matching a live prompt (case-insensitive; soft-deleted prompts do not reserve their text) saves nothing and answers 409 CONFLICT, and so does nothing when the batch would push the number of active prompts above 100 (400). The response lists the created prompts in request order; their computed visibility figures are 0.
// @Description Roles: admin, sales.
// @Description Errors: 400 VALIDATION_ERROR (message "Validation failed", error.details maps the failing Go struct field name, e.g. BrandName or Prompts[0], to a message) when a binding rule fails; 400 BAD_REQUEST ("Invalid request format") for a body that is not valid JSON or does not match the field types; 400 VALIDATION_ERROR without error.details (the member is omitted) when an entry is blank once trimmed ("prompt text must be between 1 and 500 characters") or the batch would exceed 100 active prompts ("active prompt limit of 100 reached"); 409 CONFLICT naming the duplicated text.
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateAEOPromptsRequest true "Prompts to add"
// @Success 201 {object} utils.APIResponse{data=[]models.AEOPrompt} "Prompts created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR or BAD_REQUEST - invalid body, blank prompt or active prompt limit reached"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - a prompt with this text already exists or is repeated in the request"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description Edit a prompt's text and/or toggle whether full runs include it. Both fields are optional and only the ones present are applied, so deactivating never rewrites the text; a body of {} changes nothing but still answers 200. Stored answers keep pointing at the prompt whatever the edit. The response carries the prompt without computed figures (visibility and counts are 0, last_run_at is omitted).
// @Description Roles: admin, sales.
// @Description Errors: 400 BAD_REQUEST for a path id that is not an unsigned integer ("Invalid prompt ID") or a text that is blank once trimmed ("Prompt text cannot be blank"); 400 VALIDATION_ERROR (message "Validation failed", error.details maps the failing Go struct field name, e.g. BrandName or Prompts[0], to a message) when a binding rule fails; 400 BAD_REQUEST ("Invalid request format") for a body that is not valid JSON or does not match the field types (an empty body is BAD_REQUEST); 400 VALIDATION_ERROR when reactivating a prompt while 100 are already active; 404 NOT_FOUND for an unknown or deleted prompt; 409 CONFLICT when another live prompt has the same text (case-insensitive).
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Param request body UpdateAEOPromptRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.AEOPrompt} "Prompt updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST or VALIDATION_ERROR - invalid id, body or text, or active prompt limit reached"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - prompt not found (message 'AEO prompt not found')"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - another prompt already has this text"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description Soft-delete a prompt so it is excluded from future runs and from the prompt list. Its stored answers and citations are kept and still count in the dashboard and citation report. Its text is no longer reserved: uniqueness is checked against live prompts only, so the same text can be added again.
// @Description Roles: admin only.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer ('Invalid prompt ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - prompt not found or already deleted"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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

// GeneratedAEOPromptsData documents the payload of POST /aeo/prompts/generate
// (the handler answers a gin.H with the same shape). Documentation only: it is
// never bound or encoded.
//
// @Description Suggested prompts returned by POST /aeo/prompts/generate. Nothing is stored until they are POSTed to /aeo/prompts.
type GeneratedAEOPromptsData struct {
	// Suggested buyer-style questions: de-duplicated, each 12 to 500
	// characters, at most the requested count.
	Prompts []string `json:"prompts" example:"Which CRM is best for a small B2B sales team?"`
}

// GeneratePrompts godoc
// @Summary Generate candidate AEO prompts
// @Description Ask the generation engine for buyer-style questions derived from the brand profile. The engine is the integration.aeo.generation_engine setting (default anthropic; openai, gemini, kimi or perplexity are the other choices) and generation never falls back to another engine. Its API key is read from the admin settings first and from the engine's environment variable otherwise (ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, MOONSHOT_API_KEY, PERPLEXITY_API_KEY). The call is bounded by a 90-second timeout. Nothing is stored: the suggestions come back as plain strings (de-duplicated, each 12 to 500 characters, at most count of them) and are only tracked once POSTed to /aeo/prompts. The body is optional; an empty body asks for 10.
// @Description Side effects: one billed request to the external engine.
// @Description Roles: admin, sales.
// @Description Errors: 400 VALIDATION_ERROR (message "Validation failed", error.details maps the failing Go struct field name, e.g. BrandName or Prompts[0], to a message) when a binding rule fails; 400 BAD_REQUEST ("Invalid request format") for a body that is not valid JSON or does not match the field types; 409 CONFLICT when the brand profile has not been configured; 503 PROVIDER_NOT_CONFIGURED when the selected engine has no API key (the message names the engine); 503 PROVIDER_REJECTED when the engine answered with an HTTP error ("the generation engine rejected the request (HTTP 401) — check its API key in the AEO settings" for a 4xx, "the generation engine is unavailable (HTTP 529); try again later" otherwise). 503 rather than 502 so the JSON body survives proxies such as Cloudflare. A failure without an HTTP status (timeout, DNS, TLS, egress policy) and an answer with no usable suggestion are 500 INTERNAL_ERROR.
// @Tags aeo
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body GenerateAEOPromptsRequest false "How many suggestions to generate (1..25; absent or 0 means 10)"
// @Success 200 {object} utils.APIResponse{data=GeneratedAEOPromptsData} "Suggestions generated successfully (data.prompts)"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR or BAD_REQUEST - count negative or above 25 (0 means the default 10), or a malformed body"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - the brand profile has not been configured yet"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - engine unreachable without an HTTP status, or no usable suggestion in its answer"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "PROVIDER_NOT_CONFIGURED - the selected generation engine has no API key; PROVIDER_REJECTED - the engine answered with an HTTP error"
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
			logger.WithError(utils.LogError(err)).Warn("Prompt generation provider not configured")
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
			logger.WithError(utils.LogError(err)).WithField("provider_status", status).
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
// @Description One page of the stored answers for one prompt, newest first (created_at, then id, descending), each with its engine, model, latency, mention analysis and extracted citations. Failed provider calls are stored too: they carry a populated error and empty answer_text. Pass run_id to narrow the list to one run; an unknown run_id simply yields an empty page. Answers of a soft-deleted prompt are not reachable here (404).
// @Description Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default). meta carries page, per_page, total and total_pages. No sorting parameters.
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Param run_id query int false "Restrict to one run"
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Success 200 {object} utils.APIResponse{data=[]models.AEOAnswer,meta=utils.APIMeta} "Answers retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the prompt id or run_id is not an unsigned integer ('Invalid prompt ID' / 'Invalid run ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - prompt not found or deleted"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description Queue every active prompt against every configured engine (trigger "manual") and return at once with the run row in status "running"; total_queries is prompts x engines. The work continues in the background and outlives the request, so poll GET /aeo/runs/{id} for progress. Only one run may be in flight at a time, manual and scheduled runs alike.
// @Description Side effects: one billed request per prompt and engine to the external engines; one answer row (with its citations) is stored per call, failed calls included.
// @Description Roles: admin, sales.
// @Description Errors: Checks run in this order and the first failing one answers: 503 SHUTTING_DOWN while the server is draining for shutdown; 409 CONFLICT when the brand profile has not been configured; 503 PROVIDERS_UNAVAILABLE when no engine is configured; 409 CONFLICT when a run is already in progress (runs left "running" for more than 6 hours are first marked failed, so they do not block); 404 NOT_FOUND when there is no active prompt ("No active AEO prompts to run").
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 202 {object} utils.APIResponse{data=models.AEORun} "Run accepted and started"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - there are no active prompts to run"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - a run is already in progress, or the brand profile has not been configured"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "PROVIDERS_UNAVAILABLE - no AEO engine is configured; SHUTTING_DOWN - the server is shutting down, retry later"
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
// @Description Queue ONE prompt against every configured engine and return at once with the run row (trigger "manual", status "running"). The prompt may be inactive, which is how a draft is tested before it joins full runs; a soft-deleted prompt is 404. The same single-run-in-flight guard applies as for a full run.
// @Description Side effects: one billed request per configured engine; one answer row per call is stored.
// @Description Roles: admin, sales.
// @Description Errors: 400 VALIDATION_ERROR for an id that is not a positive integer (message "Validation failed", error.details is the string "Invalid prompt id"); Checks run in this order and the first failing one answers: 503 SHUTTING_DOWN while the server is draining for shutdown; 409 CONFLICT when the brand profile has not been configured; 503 PROVIDERS_UNAVAILABLE when no engine is configured; 409 CONFLICT when a run is already in progress (runs left "running" for more than 6 hours are first marked failed, so they do not block); 404 NOT_FOUND for an unknown prompt ("Prompt not found").
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Prompt ID"
// @Success 202 {object} utils.APIResponse{data=models.AEORun} "Run accepted and started"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - invalid prompt id"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - prompt not found or deleted"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT - a run is already in progress, or the brand profile has not been configured"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "PROVIDERS_UNAVAILABLE - no AEO engine is configured; SHUTTING_DOWN - the server is shutting down, retry later"
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
// @Description One page of the run history with the trigger, status and query counters of each run.
// @Description Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default). meta carries page, per_page, total and total_pages.
// @Description Sorting: sort_by is one of id, status, trigger, started_at, completed_at, created_at; any other value is answered with 400 BAD_REQUEST ("Invalid sort column: <value>"). sort_order is asc or desc; anything else means desc. Without sort_by the order is created_at descending whatever sort_order says. Ties are broken by id in the same direction.
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param sort_by query string false "Sort column (unknown value: 400)" Enums(id, status, trigger, started_at, completed_at, created_at)
// @Param sort_order query string false "Sort direction; ignored without sort_by" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AEORun,meta=utils.APIMeta} "Runs retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - unsupported sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description A single run with its current status and counters. This is the endpoint to poll after POST /aeo/runs or POST /aeo/prompts/{id}/run: the status moves from "running" to "completed", "partial" (some provider calls failed) or "failed" (all of them did), and completed_at is set at that point.
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Run ID"
// @Success 200 {object} utils.APIResponse{data=models.AEORun} "Run retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer ('Invalid run ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - run not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description Aggregated visibility over the requested window: the overall percentage of non-error answers that mention the brand, the same figure per engine, a daily timeline with one entry per UTC day (days without answers carry overall 0 and no per-engine entries), the share of voice across the brand and its competitors, and the per-company daily timeline. Windows are 7, 30 or 90 days; anything else falls back to 30. The window ends (exclusive) at the start of tomorrow in UTC, so today's answers are included. Works before a brand profile exists: the brand figures are then empty and share of voice falls back to the competitor names found in the answers.
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param days query int false "Reporting window in days; 7, 30 or 90, anything else means 30" Enums(7, 30, 90) default(30)
// @Success 200 {object} utils.APIResponse{data=models.AEODashboard} "Dashboard retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description Which sources the non-error answers cite over the requested window: the owned-domain citation rate, per-company citation and brand-mention rates (the brand first, then each competitor in profile order, companies without citations included) and the twenty most cited hosts by citation count descending. Windows are 7, 30 or 90 days; anything else falls back to 30. Works before a brand profile exists (by_company is then empty).
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param days query int false "Reporting window in days; 7, 30 or 90, anything else means 30" Enums(7, 30, 90) default(30)
// @Success 200 {object} utils.APIResponse{data=models.AEOCitationsReport} "Citation report retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
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
// @Description Every supported engine with the model it would use and whether it is configured, re-read on every call so a key just stored in the settings shows up at once: anthropic, openai, gemini, kimi, perplexity, then the custom engine under its configured name (AEO_CUSTOM_NAME, "custom" when unset). A named engine counts as configured when it has an API key, the custom engine when it has a base URL. Unconfigured engines are skipped by runs. API keys are never returned. Always six entries, never paginated.
// @Description Roles: admin, sales, support.
// @Tags aeo
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]models.AEOProviderStatus} "Provider statuses retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
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
		logger.WithError(utils.LogError(err)).Warn("Duplicate AEO prompt")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrRunInProgress):
		logger.WithError(utils.LogError(err)).Warn("AEO run already in progress")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrProfileNotConfigured):
		logger.WithError(utils.LogError(err)).Warn("AEO profile not configured")
		utils.RespondConflict(c, err.Error())
	case errors.Is(err, apperrors.ErrNoProvidersConfigured):
		logger.WithError(utils.LogError(err)).Warn("No AEO providers configured")
		utils.RespondError(c, http.StatusServiceUnavailable, "PROVIDERS_UNAVAILABLE", err.Error(), nil)
	case errors.Is(err, service.ErrAEOShuttingDown):
		// The process is draining: admission is closed so the run would be
		// cancelled before it could do anything. 503 tells the client to retry
		// against the restarted process rather than treat it as its own fault.
		logger.WithError(utils.LogError(err)).Warn("AEO run refused: the runtime is shutting down")
		utils.RespondError(c, http.StatusServiceUnavailable, "SHUTTING_DOWN", err.Error(), nil)
	case errors.Is(err, apperrors.ErrGenerationProviderNotConfigured):
		logger.WithError(utils.LogError(err)).Warn("Prompt generation provider not configured")
		utils.RespondError(c, http.StatusServiceUnavailable, "PROVIDER_NOT_CONFIGURED", err.Error(), nil)
	case errors.Is(err, service.ErrAEOPromptLimit):
		logger.WithError(utils.LogError(err)).Warn("AEO active prompt limit reached")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case errors.Is(err, service.ErrAEOInvalidPrompt),
		errors.Is(err, service.ErrAEOInvalidProfile),
		errors.Is(err, service.ErrAEOInvalidTrigger):
		// The service validates beyond what the binding tags can express —
		// whitespace-only text passes gin's `required` but is empty once
		// trimmed. Those are client mistakes, not server faults.
		logger.WithError(utils.LogError(err)).Warn("AEO request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("AEO resource not found")
		utils.RespondNotFound(c, notFoundMessage)
	default:
		logger.WithError(utils.LogError(err)).Error("AEO operation failed")
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
