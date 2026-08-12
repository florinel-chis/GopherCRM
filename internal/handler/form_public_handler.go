package handler

import (
	_ "embed"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// formEmbedJS is the renderer external sites load. It is compiled into the
// binary so a deployment cannot end up serving a stale or missing asset.
//
//go:embed assets/form_embed.js
var formEmbedJS []byte

const (
	// formSubmissionMaxBody caps a submission body. The definition limits every
	// field's length, but nothing stops a client from posting megabytes of
	// unknown keys, so the limit is enforced before the JSON is even parsed.
	formSubmissionMaxBody = 64 << 10

	// formUserAgentMaxLength matches the submissions column. The service clamps
	// it too; doing it here keeps the oversized value from travelling any
	// further than it has to.
	formUserAgentMaxLength = 255

	// formEmbedCacheControl lets browsers and CDNs keep the renderer for an
	// hour. An hour is short enough that a fix reaches live forms the same day
	// and long enough that a busy page does not refetch it on every view.
	formEmbedCacheControl = "public, max-age=3600"

	formEmbedContentType = "application/javascript; charset=utf-8"
)

// FormPublicHandler serves the unauthenticated half of the forms module: the
// renderer script, the form definition an external page needs, the submission
// endpoint, and the browser-facing pages of the double-opt-in flow.
//
// Everything here is reachable by anyone on the internet. The definition and
// submission endpoints answer in the usual API envelope because a script reads
// them; the confirmation and hosted pages answer in HTML because a person
// reads them.
type FormPublicHandler struct {
	formService service.FormService
	cfg         config.FormsConfig

	// apiPrefix is where the API is mounted, normalised to a leading slash and
	// no trailing one. The pages below build their links from it and keep them
	// host-relative, so a form keeps working behind a proxy or on a custom
	// domain whatever cfg.PublicBaseURL says. The configured base URL is what
	// the service puts in mail, where a relative link would be useless.
	apiPrefix string
}

func NewFormPublicHandler(s service.FormService, cfg config.FormsConfig, apiPrefix string) *FormPublicHandler {
	return &FormPublicHandler{
		formService: s,
		cfg:         cfg,
		apiPrefix:   normaliseAPIPrefix(apiPrefix),
	}
}

// normaliseAPIPrefix turns any of "", "/", "api/v1" or "/api/v1/" into the one
// shape the page templates can concatenate onto: "" or "/api/v1".
func normaliseAPIPrefix(prefix string) string {
	trimmed := strings.Trim(prefix, "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}

// EmbedScript serves the renderer. It is not an API endpoint — it is a static
// asset — so it carries no envelope and no swag annotation.
func (h *FormPublicHandler) EmbedScript(c *gin.Context) {
	c.Header("Cache-Control", formEmbedCacheControl)
	c.Data(http.StatusOK, formEmbedContentType, formEmbedJS)
}

// Definition godoc
// @Summary Get a public form definition
// @Description Everything a visitor's browser needs to render a published form: its name, the ordered field definitions, the consent text, the submit action, the reCAPTCHA site key when the form uses it, the name of the honeypot input and a short-lived signed challenge that the submission must carry back. Unauthenticated and cross-origin. Unknown, unpublished and origin-restricted forms are all a plain 404 — the response never distinguishes them.
// @Tags forms
// @Produce json
// @Param key path string true "Public form identifier"
// @Success 200 {object} utils.APIResponse{data=service.PublicFormDefinition} "Form definition retrieved successfully"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "No published form with this identifier is available to this origin"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /forms/public/{key} [get]
func (h *FormPublicHandler) Definition(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormPublicHandler.Definition")

	definition, err := h.formService.PublicDefinition(c.Param("key"), requestOrigin(c))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, definition)
	utils.RespondSuccess(c, http.StatusOK, definition)
}

// Submit godoc
// @Summary Submit a public form
// @Description Accepts a submission for a published form. The body carries the field values, the consent flag, the challenge handed out with the definition, the honeypot input and, when the form uses reCAPTCHA, the client token. Field-level problems come back as 400 with a field name to message map in error.details. A submission caught by a spam layer is answered exactly like a genuine one, so the response says nothing about which layers exist. Unauthenticated and cross-origin; bodies are capped at 64 KB.
// @Tags forms
// @Accept json
// @Produce json
// @Param key path string true "Public form identifier"
// @Param submission body service.PublicSubmissionRequest true "Submitted values"
// @Success 200 {object} utils.APIResponse{data=service.SubmitOutcome} "Submission accepted; the outcome says whether to show a message or redirect"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Validation failed - error.details maps field names to messages"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "No published form with this identifier is available to this origin"
// @Failure 413 {object} utils.APIResponse{error=utils.APIError} "Submission body exceeds 64 KB"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /forms/public/{key}/submissions [post]
func (h *FormPublicHandler) Submit(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormPublicHandler.Submit")

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, formSubmissionMaxBody)

	var req service.PublicSubmissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			logger.WithError(err).Warn("Form submission body exceeds the size limit")
			utils.RespondError(c, http.StatusRequestEntityTooLarge, utils.ErrCodeValidation,
				"Submission is too large", nil)
			return
		}
		logger.WithError(err).Warn("Malformed form submission body")
		utils.RespondBadRequest(c, "Invalid request body")
		return
	}

	outcome, err := h.formService.SubmitPublic(c.Param("key"), &req, service.SubmissionMeta{
		IP:        c.ClientIP(),
		UserAgent: clipRunes(c.GetHeader("User-Agent"), formUserAgentMaxLength),
		Origin:    requestOrigin(c),
	})
	if err != nil {
		h.respondError(c, logger, err)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, outcome)
	utils.RespondSuccess(c, http.StatusOK, outcome)
}

// ViewPage serves the CRM-hosted standalone page for a form: a shell that
// loads the renderer for this key. It is a page, not an API endpoint, so it
// answers in HTML and carries no swag annotation. The key is not checked here
// — the renderer's own definition request decides whether anything appears,
// which keeps this page free of any enumeration signal.
func (h *FormPublicHandler) ViewPage(c *gin.Context) {
	c.Data(http.StatusOK, formPageContentType,
		formHostedViewPage(h.apiPrefix+"/forms/public/embed.js", c.Param("key")))
}

// ConfirmPage renders the button a visitor presses to confirm their address.
// It deliberately does nothing else: a GET never spends the token, so mail
// scanners and link previews that fetch every URL in a message cannot confirm
// on the visitor's behalf or burn the link before they click it.
func (h *FormPublicHandler) ConfirmPage(c *gin.Context) {
	token := c.Query("token")
	if strings.TrimSpace(token) == "" {
		c.Data(http.StatusOK, formPageContentType, formInvalidLinkPage())
		return
	}

	c.Data(http.StatusOK, formPageContentType,
		formConfirmPage(h.apiPrefix+"/forms/public/confirm", token))
}

// confirmRequest is the JSON shape POST /forms/public/confirm also accepts.
// The page itself posts a form-encoded body; the JSON form exists so the flow
// can be driven from a script or a smoke test without simulating a browser.
type confirmRequest struct {
	Token string `json:"token"`
}

// Confirm spends the confirmation token. Every failure — unknown, expired,
// already used, or an internal fault — renders the same generic page with the
// same status, so a visitor cannot probe token validity and neither can a bot.
func (h *FormPublicHandler) Confirm(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormPublicHandler.Confirm")

	token := c.PostForm("token")
	if strings.TrimSpace(token) == "" {
		var body confirmRequest
		if err := c.ShouldBindJSON(&body); err == nil {
			token = body.Token
		}
	}
	if strings.TrimSpace(token) == "" {
		logger.Warn("Form confirmation attempted without a token")
		c.Data(http.StatusOK, formPageContentType, formInvalidLinkPage())
		return
	}

	if err := h.formService.ConfirmSubmission(token); err != nil {
		if errors.Is(err, service.ErrInvalidConfirmationToken) {
			logger.WithError(err).Warn("Form confirmation token rejected")
		} else {
			logger.WithError(err).Error("Form confirmation failed")
		}
		c.Data(http.StatusOK, formPageContentType, formInvalidLinkPage())
		return
	}

	logger.Info("Form submission confirmed")
	c.Data(http.StatusOK, formPageContentType, formConfirmedPage())
}

// respondError maps the service's errors onto the JSON routes. Field-level
// failures carry their per-field messages into error.details; everything the
// service refuses to explain — unknown key, unpublished form, origin mismatch
// — is the same 404.
func (h *FormPublicHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	var fieldErrors service.FieldErrors

	switch {
	case errors.As(err, &fieldErrors):
		logger.WithError(err).Warn("Form submission failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation,
			"The submission could not be accepted", fieldErrors)
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(err).Warn("Form request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation,
			"The submission could not be accepted", nil)
	case apperrors.IsNotFound(err):
		logger.WithError(err).Warn("Public form not available")
		utils.RespondNotFound(c, "Form not found")
	default:
		logger.WithError(err).Error("Public form request failed")
		utils.RespondInternalError(c)
	}
}

// requestOrigin is where the browser says the request came from. Origin is the
// header browsers attach to cross-origin requests; Referer covers the
// same-origin hosted page, which sends no Origin on a GET.
func requestOrigin(c *gin.Context) string {
	if origin := c.GetHeader("Origin"); origin != "" {
		return origin
	}
	return c.GetHeader("Referer")
}

// clipRunes shortens a value to fit its column without splitting a multi-byte
// character in half.
func clipRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
