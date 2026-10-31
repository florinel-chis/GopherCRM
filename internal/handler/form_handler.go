package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// FormHandler serves the CRM-side endpoints of the forms module: the form
// definitions and the submissions they collected. The visitor-facing endpoints
// live in their own handler; nothing here is reachable without authentication.
type FormHandler struct {
	formService service.FormService
}

func NewFormHandler(formService service.FormService) *FormHandler {
	return &FormHandler{formService: formService}
}

// CreateFormRequest is the body of POST /forms. It mirrors the writable JSON
// surface of a form; the public identifier and the author are assigned by the
// server and are not accepted from the client.
//
// create_lead is a pointer so an absent flag can be told apart from an explicit
// false: a form that omits it captures leads, which is what almost every form
// is for. The model deliberately carries no column default for it, so the
// default belongs here.
type CreateFormRequest struct {
	// Form name; required, at most 255 characters, not blank once trimmed.
	Name string `json:"name" binding:"required,max=255" example:"Website contact form" maxLength:"255"`
	// Internal description for staff; never shown publicly.
	Description string `json:"description" example:"Contact form on the pricing page."`
	// Publication state; absent or empty means draft. Only published forms are
	// served publicly.
	Status string `json:"status" binding:"omitempty,oneof=draft published archived" enums:"draft,published,archived" example:"published"`

	// Ordered field definitions, 1 to 50. Exactly one field of type email,
	// named "email"; names unique and matching ^[a-z][a-z0-9_]{0,49}$; select
	// fields need 1 to 50 non-blank options and only select fields may have
	// options; hidden fields cannot be required.
	Fields []models.FormFieldDef `json:"fields" binding:"required,min=1,max=50" minItems:"1" maxItems:"50"`

	// message (default when absent or empty) or redirect.
	SubmitAction string `json:"submit_action" binding:"omitempty,oneof=message redirect" enums:"message,redirect" example:"message"`
	// Message shown after submitting when submit_action is message; empty
	// means a built-in text.
	ThankYouMessage string `json:"thank_you_message" example:"Thanks, we will be in touch within one business day."`
	// Target page when submit_action is redirect: required then and must
	// start with http:// or https:// (trimmed). At most 512 characters.
	RedirectURL string `json:"redirect_url" binding:"omitempty,max=512" example:"https://www.example.com/thank-you" maxLength:"512"`
	// Consent statement; when non-empty every submission must carry
	// consent=true.
	ConsentText string `json:"consent_text" example:"I agree to be contacted about my enquiry."`

	// Staff addresses notified of each accepted submission: at most 20, each
	// at most 255 characters and shaped like an email address (trimmed).
	NotifyEmails []string `json:"notify_emails" binding:"omitempty,max=20,dive,max=255" example:"sales@example.com" maxItems:"20"`

	// Enable double opt-in (confirmation mail with a 48-hour single-use link
	// before the submission counts).
	DoubleOptIn bool `json:"double_opt_in" example:"false"`
	// Confirmation mail subject, at most 255 characters; empty means a
	// built-in subject.
	ConfirmationSubject string `json:"confirmation_subject" binding:"omitempty,max=255" example:"Please confirm your email address" maxLength:"255"`
	// Confirmation mail body (plain text) with an optional {confirmation_link}
	// placeholder; the link is appended when the placeholder is missing.
	ConfirmationBody string `json:"confirmation_body" example:"Hello, please confirm your address: {confirmation_link}"`
	// Follow-up mail subject, at most 255 characters; empty disables the
	// follow-up mail.
	FollowUpSubject string `json:"follow_up_subject" binding:"omitempty,max=255" example:"Your download link" maxLength:"255"`
	// Follow-up mail body (plain text); {content_link} is replaced by
	// content_url. Empty means a built-in text.
	FollowUpBody string `json:"follow_up_body" example:"Download the guide here: {content_link}"`
	// Link substituted for {content_link}, at most 512 characters.
	ContentURL string `json:"content_url" binding:"omitempty,max=512" example:"https://www.example.com/files/guide.pdf" maxLength:"512"`

	// Require a reCAPTCHA v3 token; ignored unless the server has both
	// reCAPTCHA keys.
	CaptchaEnabled bool `json:"captcha_enabled" example:"false"`
	// Feed accepted submissions into leads. Absent means true; send false
	// explicitly to turn it off.
	CreateLead *bool `json:"create_lead" example:"true"`
	// Owner of created leads; 0 means none. Must be non-zero when create_lead
	// is true or absent, and any non-zero value must name an existing, active
	// admin or sales user.
	DefaultOwnerID uint `json:"default_owner_id" example:"2"`

	// Origin allowlist of bare host names with optional port (no scheme, no
	// path), lower-cased on save; empty allows every origin. At most 20, each
	// at most 255 characters.
	AllowedDomains []string `json:"allowed_domains" binding:"omitempty,max=20,dive,max=255" example:"www.example.com" maxItems:"20"`
}

// UpdateFormRequest is the body of PUT /forms/:id. The update replaces the
// whole definition, so every field is sent on every call and an omitted one is
// cleared rather than kept. create_lead follows the create request: absent
// means true.
type UpdateFormRequest CreateFormRequest

// FormListItem is one row of the form list: the stored form with the number of
// submissions it has collected so far. The form is embedded, so a row carries
// exactly the fields of a single-form response plus submission_count.
type FormListItem struct {
	models.Form
	// Number of live submissions the form has collected, spam included.
	SubmissionCount int64 `json:"submission_count" example:"128" readonly:"true"`
}

// toModel maps a request body onto a form. Everything the client may not set —
// the identifier, the public key, the author, the timestamps — is left alone
// so the service can fill it in or carry it over from the stored row.
func (r *CreateFormRequest) toModel() *models.Form {
	createLead := true
	if r.CreateLead != nil {
		createLead = *r.CreateLead
	}

	return &models.Form{
		Name:                r.Name,
		Description:         r.Description,
		Status:              models.FormStatus(r.Status),
		Fields:              r.Fields,
		SubmitAction:        r.SubmitAction,
		ThankYouMessage:     r.ThankYouMessage,
		RedirectURL:         r.RedirectURL,
		ConsentText:         r.ConsentText,
		NotifyEmails:        r.NotifyEmails,
		DoubleOptIn:         r.DoubleOptIn,
		ConfirmationSubject: r.ConfirmationSubject,
		ConfirmationBody:    r.ConfirmationBody,
		FollowUpSubject:     r.FollowUpSubject,
		FollowUpBody:        r.FollowUpBody,
		ContentURL:          r.ContentURL,
		CaptchaEnabled:      r.CaptchaEnabled,
		CreateLead:          createLead,
		DefaultOwnerID:      r.DefaultOwnerID,
		AllowedDomains:      r.AllowedDomains,
	}
}

// List godoc
// @Summary List forms
// @Description One page of form definitions (soft-deleted forms excluded), each with the number of live submissions it has collected, spam included.
// @Description Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default). meta carries page, per_page, total and total_pages.
// @Description Filtering and sorting: status is draft, published or archived; sort_by is id, name, status, created_at or updated_at; sort_order is asc or desc, anything else means desc. Without sort_by the order is created_at descending whatever sort_order says; ties are broken by id. An unknown status or sort column is answered with 400 VALIDATION_ERROR (message "Validation failed", error.details {"status": "..."} or {"sort_by": "..."}).
// @Description Roles: admin, sales, support.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param status query string false "Filter by publication state (unknown value: 400)" Enums(draft, published, archived)
// @Param sort_by query string false "Sort column (unknown value: 400)" Enums(id, name, status, created_at, updated_at)
// @Param sort_order query string false "Sort direction; ignored without sort_by" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]handler.FormListItem,meta=utils.APIMeta} "Forms retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - unknown status filter or sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms [get]
func (h *FormHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.List")

	offset, limit := utils.ParseOffsetLimit(c)
	status := strings.TrimSpace(c.Query("status"))
	sortBy := strings.TrimSpace(c.Query("sort_by"))
	sortOrder := strings.TrimSpace(c.Query("sort_order"))

	forms, counts, total, err := h.formService.List(offset, limit, status, sortBy, sortOrder)
	if err != nil {
		h.respondError(c, logger, err, "Form not found")
		return
	}

	items := make([]FormListItem, 0, len(forms))
	for i := range forms {
		items = append(items, FormListItem{
			Form:            forms[i],
			SubmissionCount: counts[forms[i].ID],
		})
	}

	meta := formListMeta(c, offset, limit, total)
	utils.LogHandlerResponse(logger, http.StatusOK, items)
	utils.RespondSuccessWithMeta(c, http.StatusOK, items, meta)
}

// Create godoc
// @Summary Create a form
// @Description Create a form definition. The server assigns the public identifier used by the embed script and records the caller as author. The definition is normalised and validated before it is stored: name trimmed and non-blank; 1 to 50 fields with unique machine names matching ^[a-z][a-z0-9_]{0,49}$; exactly one field of type email, named "email"; select fields with 1 to 50 non-blank options and no options on other types; hidden fields never required; max_length defaulted and clamped (see FormFieldDef); a redirect action needs an http(s) redirect_url; notification addresses shaped like email addresses; allowed domains bare hosts. Omitting create_lead means true, and then default_owner_id must name an active admin or sales user. status defaults to draft, submit_action to message. The response is the stored form.
// @Description Roles: admin, sales.
// @Description Errors: 400 VALIDATION_ERROR (message "Validation failed", error.details maps the failing Go struct field name, e.g. Name or Fields, to a message) when a binding rule fails; 400 BAD_REQUEST ("Invalid request format") for a body that is not valid JSON or does not match the field types; 400 VALIDATION_ERROR without error.details (the member is omitted) when a definition rule fails (the message states the rule, e.g. "a form that creates leads needs a default owner: invalid form definition: validation failed"); 400 VALIDATION_ERROR (message "Validation failed") with error.details {"default_owner_id": "..."} when the lead owner does not exist, is deactivated or is not an admin or sales user.
// @Tags forms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateFormRequest true "Form definition"
// @Success 201 {object} utils.APIResponse{data=models.Form} "Form created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR or BAD_REQUEST - invalid body or form definition (see description)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms [post]
func (h *FormHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.Create")

	var req CreateFormRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	form := req.toModel()
	if err := h.formService.Create(form, c.GetUint("user_id")); err != nil {
		h.respondError(c, logger, err, "Form not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, form)
	utils.RespondSuccess(c, http.StatusCreated, form)
}

// Get godoc
// @Summary Get a form
// @Description A single form definition by ID, including its notification recipients, mail texts and origin allowlist. Soft-deleted forms are 404.
// @Description Roles: admin, sales, support.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Success 200 {object} utils.APIResponse{data=models.Form} "Form retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not a positive integer ('Invalid form ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - form not found (message 'Form not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms/{id} [get]
func (h *FormHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.Get")

	id, ok := formPathID(c, "Invalid form ID")
	if !ok {
		return
	}

	form, err := h.formService.GetByID(id)
	if err != nil {
		h.respondError(c, logger, err, "Form not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, form)
	utils.RespondSuccess(c, http.StatusOK, form)
}

// Update godoc
// @Summary Update a form
// @Description Replace a form definition wholesale, publication status included. The body is the complete document: a field left out is cleared (status falls back to draft, submit_action to message, create_lead to true), not kept. id, public identifier, author and created_at are carried over from the stored row, so embedded forms keep working across edits. Validation is identical to creation. Submissions already stored are not touched.
// @Description Roles: admin, sales.
// @Description Errors: 400 BAD_REQUEST for a path id that is not a positive integer ("Invalid form ID"); 400 VALIDATION_ERROR (message "Validation failed", error.details maps the failing Go struct field name, e.g. Name or Fields, to a message) when a binding rule fails; 400 BAD_REQUEST ("Invalid request format") for a body that is not valid JSON or does not match the field types; 400 VALIDATION_ERROR without error.details (the member is omitted) when a definition rule fails (the message states the rule, e.g. "a form that creates leads needs a default owner: invalid form definition: validation failed"); 400 VALIDATION_ERROR (message "Validation failed") with error.details {"default_owner_id": "..."} when the lead owner does not exist, is deactivated or is not an admin or sales user; 404 NOT_FOUND for an unknown or deleted form.
// @Tags forms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Param request body UpdateFormRequest true "Complete form definition"
// @Success 200 {object} utils.APIResponse{data=models.Form} "Form updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST or VALIDATION_ERROR - invalid id, body or form definition (see description)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin or sales"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - form not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms/{id} [put]
func (h *FormHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.Update")

	id, ok := formPathID(c, "Invalid form ID")
	if !ok {
		return
	}

	var req UpdateFormRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	form := (*CreateFormRequest)(&req).toModel()
	if err := h.formService.Update(id, form); err != nil {
		h.respondError(c, logger, err, "Form not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, form)
	utils.RespondSuccess(c, http.StatusOK, form)
}

// Delete godoc
// @Summary Delete a form
// @Description Soft-delete a form. The public endpoints stop serving it at once (its definition and submissions answer 404, and outstanding double opt-in links stop working), its submission list is no longer reachable through GET /forms/{id}/submissions, while single submissions stay readable through GET /forms/submissions/{id} and the leads they produced are untouched.
// @Description Roles: admin only.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not a positive integer ('Invalid form ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - form not found or already deleted"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms/{id} [delete]
func (h *FormHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.Delete")

	id, ok := formPathID(c, "Invalid form ID")
	if !ok {
		return
	}

	if err := h.formService.Delete(id); err != nil {
		h.respondError(c, logger, err, "Form not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// ListSubmissions godoc
// @Summary List the submissions of a form
// @Description One page of the submissions one form collected, newest first (created_at, then id, descending). Spam submissions are listed alongside genuine ones with their status and the protection layer that caught them, so the filters can be audited. The status filter is not validated: an unknown value returns an empty page.
// @Description Pagination: offset (default 0) and limit (default 20, capped at 100; non-positive or unparseable values fall back to the default). meta carries page, per_page, total and total_pages. No sorting parameters.
// @Description Roles: admin, sales, support.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Param offset query int false "Pagination offset" default(0) minimum(0)
// @Param limit query int false "Page size, capped at 100" default(20) minimum(1) maximum(100)
// @Param status query string false "Filter by submission state" Enums(received, pending, confirmed, spam)
// @Success 200 {object} utils.APIResponse{data=[]models.FormSubmission,meta=utils.APIMeta} "Submissions retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not a positive integer ('Invalid form ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - form not found or deleted"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms/{id}/submissions [get]
func (h *FormHandler) ListSubmissions(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.ListSubmissions")

	id, ok := formPathID(c, "Invalid form ID")
	if !ok {
		return
	}

	offset, limit := utils.ParseOffsetLimit(c)
	status := strings.TrimSpace(c.Query("status"))

	submissions, total, err := h.formService.ListSubmissions(id, offset, limit, status)
	if err != nil {
		h.respondError(c, logger, err, "Form not found")
		return
	}
	if submissions == nil {
		submissions = []models.FormSubmission{}
	}

	meta := formListMeta(c, offset, limit, total)
	utils.LogHandlerResponse(logger, http.StatusOK, submissions)
	utils.RespondSuccessWithMeta(c, http.StatusOK, submissions, meta)
}

// GetSubmission godoc
// @Summary Get a submission
// @Description A single submission with the values it carried, where it came from and the lead it produced, if any. Works for submissions of soft-deleted forms too.
// @Description Roles: admin, sales, support.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Submission ID"
// @Success 200 {object} utils.APIResponse{data=models.FormSubmission} "Submission retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not a positive integer ('Invalid submission ID')"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or an invalid or expired token or API key"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - role is not admin, sales or support (the customer role is always refused)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - submission not found (message 'Submission not found')"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - moderate tier: 120 requests/min, burst 30, per client IP, shared by every authenticated route; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; the cause is never echoed"
// @Router /forms/submissions/{id} [get]
func (h *FormHandler) GetSubmission(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormHandler.GetSubmission")

	id, ok := formPathID(c, "Invalid submission ID")
	if !ok {
		return
	}

	submission, err := h.formService.GetSubmission(id)
	if err != nil {
		h.respondError(c, logger, err, "Submission not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, submission)
	utils.RespondSuccess(c, http.StatusOK, submission)
}

// respondError maps the errors the form service returns onto status codes.
// Per-field failures come back as service.FieldErrors, which both unwraps to
// the validation sentinel and carries the field→message map the UI places next
// to its inputs; the definition rules report a single wrapped message instead.
// Anything unclassified is a server fault and is never echoed back.
func (h *FormHandler) respondError(c *gin.Context, logger *logrus.Entry, err error, notFoundMessage string) {
	var fieldErrors service.FieldErrors

	switch {
	case errors.As(err, &fieldErrors):
		logger.WithError(utils.LogError(err)).Warn("Form request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, "Validation failed", fieldErrors)
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(utils.LogError(err)).Warn("Form request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Form resource not found")
		utils.RespondNotFound(c, notFoundMessage)
	default:
		logger.WithError(utils.LogError(err)).Error("Form operation failed")
		utils.RespondInternalError(c)
	}
}

// formPathID parses the :id path parameter, answering 400 with the caller's
// message when it is not a positive integer.
func formPathID(c *gin.Context, message string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		utils.RespondBadRequest(c, message)
		return 0, false
	}
	return uint(id), true
}

// formListMeta builds the pagination metadata of the form list endpoints, in
// the same shape every other list endpoint of this API reports.
// utils.ParseOffsetLimit never returns a zero limit, so the arithmetic here is
// safe without a further guard.
func formListMeta(c *gin.Context, offset, limit int, total int64) *utils.APIMeta {
	return &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
}
