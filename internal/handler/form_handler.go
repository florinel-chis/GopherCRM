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
	Name        string `json:"name" binding:"required,max=255"`
	Description string `json:"description"`
	Status      string `json:"status" binding:"omitempty,oneof=draft published archived"`

	Fields []models.FormFieldDef `json:"fields" binding:"required,min=1,max=50"`

	SubmitAction    string `json:"submit_action" binding:"omitempty,oneof=message redirect"`
	ThankYouMessage string `json:"thank_you_message"`
	RedirectURL     string `json:"redirect_url" binding:"omitempty,max=512"`
	ConsentText     string `json:"consent_text"`

	NotifyEmails []string `json:"notify_emails" binding:"omitempty,max=20,dive,max=255"`

	DoubleOptIn         bool   `json:"double_opt_in"`
	ConfirmationSubject string `json:"confirmation_subject" binding:"omitempty,max=255"`
	ConfirmationBody    string `json:"confirmation_body"`
	FollowUpSubject     string `json:"follow_up_subject" binding:"omitempty,max=255"`
	FollowUpBody        string `json:"follow_up_body"`
	ContentURL          string `json:"content_url" binding:"omitempty,max=512"`

	CaptchaEnabled bool  `json:"captcha_enabled"`
	CreateLead     *bool `json:"create_lead"`
	DefaultOwnerID uint  `json:"default_owner_id"`

	AllowedDomains []string `json:"allowed_domains" binding:"omitempty,max=20,dive,max=255"`
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
	SubmissionCount int64 `json:"submission_count"`
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
// @Description Paginated list of form definitions, each carrying the number of submissions it has collected. Available to admin, sales and support; the customer role is rejected with 403. Sorting accepts id, name, status, created_at and updated_at; an unknown column is answered with 400 rather than silently ignored, and so is an unknown status filter. The default order is newest first.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Pagination offset (default 0)"
// @Param limit query int false "Page size (default 20, maximum 100)"
// @Param status query string false "Filter by publication state" Enums(draft, published, archived)
// @Param sort_by query string false "Sort column" Enums(id, name, status, created_at, updated_at)
// @Param sort_order query string false "Sort direction (default desc)" Enums(asc, desc)
// @Success 200 {object} utils.APIResponse{data=[]handler.FormListItem} "Forms retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid status filter or sort column"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Create a form definition (admin and sales only). The server assigns the public identifier used by the embed script and records the author. The definition is validated before it is stored: 1 to 50 fields with unique machine names matching ^[a-z][a-z0-9_]{0,49}$, exactly one field of type email named "email", select fields carrying 1 to 50 options, hidden fields never required, a redirect action requiring an http(s) redirect_url, and a create_lead form requiring a default_owner_id that names an active admin or sales user. Omitting create_lead means true. A rejected definition is answered with 400, with the offending fields in error.details when the service could attribute the failure.
// @Tags forms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateFormRequest true "Form definition"
// @Success 201 {object} utils.APIResponse{data=models.Form} "Form created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data or invalid form definition"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Get a single form definition by ID, including its notification recipients, mail bodies and origin allowlist. Available to admin, sales and support; the customer role is rejected with 403.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Success 200 {object} utils.APIResponse{data=models.Form} "Form retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid form ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Form not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Replace a form definition wholesale (admin and sales only), including its publication status. The body carries the complete document: a field left out is cleared, not kept. The public identifier and the author are immutable and are carried over from the stored row, so an embedded form keeps working across edits. Omitting create_lead means true. Validation is identical to creation.
// @Tags forms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Param request body UpdateFormRequest true "Complete form definition"
// @Success 200 {object} utils.APIResponse{data=models.Form} "Form updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid form ID, request data or form definition"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Form not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Soft-delete a form (admin role only). The public endpoints stop serving it immediately, while its submissions are retained so the leads they produced keep their provenance.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid form ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Form not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Paginated list of the submissions one form collected, newest first. Available to admin, sales and support; the customer role is rejected with 403. Spam submissions are listed alongside genuine ones with their status and the protection layer that caught them, so the filters can be audited.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Form ID"
// @Param offset query int false "Pagination offset (default 0)"
// @Param limit query int false "Page size (default 20, maximum 100)"
// @Param status query string false "Filter by submission state" Enums(received, pending, confirmed, spam)
// @Success 200 {object} utils.APIResponse{data=[]models.FormSubmission} "Submissions retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid form ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Form not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
// @Description Get a single submission with the values it carried, the address it came from and the lead it produced, if any. Available to admin, sales and support; the customer role is rejected with 403.
// @Tags forms
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Submission ID"
// @Success 200 {object} utils.APIResponse{data=models.FormSubmission} "Submission retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid submission ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin, sales or support role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Submission not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Warn("Form request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, "Validation failed", fieldErrors)
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(err).Warn("Form request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation, err.Error(), nil)
	case apperrors.IsNotFound(err):
		logger.WithError(err).Warn("Form resource not found")
		utils.RespondNotFound(c, notFoundMessage)
	default:
		logger.WithError(err).Error("Form operation failed")
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
