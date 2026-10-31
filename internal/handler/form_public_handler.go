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

// EmbedScript serves the renderer. It is a static asset rather than an API
// endpoint, so it carries no envelope; it is documented so the public surface
// of the forms module is complete.
//
// EmbedScript godoc
// @Summary Form embed script
// @Description The JavaScript renderer that external pages load with <script src=".../forms/public/embed.js" data-form-key="<public_id>" async></script>. It fetches the definition from GET /forms/public/{key}, renders the form and posts submissions to POST /forms/public/{key}/submissions. Compiled into the server binary, so it always matches the API version. Served as raw JavaScript, never in the JSON envelope, with Cache-Control "public, max-age=3600"; only the rate limiter in front of it answers in JSON (429 TOO_MANY_REQUESTS).
// @Description Unauthenticated: no credentials are needed or accepted. Cross-origin calls get a permissive, credential-less CORS policy (the request Origin is echoed, methods GET, POST, OPTIONS, header Content-Type; preflights answer 204).
// @Tags public-forms
// @Produce application/javascript,json
// @Success 200 {string} string "The renderer script (application/javascript; charset=utf-8)"
// @Header 200 {string} Cache-Control "public, max-age=3600"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS (JSON envelope) - generous tier: 240 requests/min, burst 40, per client IP, one bucket shared by GET embed.js, GET {key} and GET {key}/view; error.details.retry_after is '60s'; no Retry-After header"
// @Router /forms/public/embed.js [get]
func (h *FormPublicHandler) EmbedScript(c *gin.Context) {
	c.Header("Cache-Control", formEmbedCacheControl)
	c.Data(http.StatusOK, formEmbedContentType, formEmbedJS)
}

// Definition godoc
// @Summary Get a public form definition
// @Description Everything a visitor's browser needs to render a published form: its name and public id, the ordered field definitions (with the max_length the server enforces), the consent text, the submit action, the reCAPTCHA site key when the form uses reCAPTCHA and the server has both keys, the name of the honeypot input and a freshly signed time-trap challenge that the submission must carry back. Staff-only settings (notification addresses, mail texts, lead owner, allowlist) are never included.
// @Description The origin is taken from the Origin header, or from Referer when Origin is absent. When the form has an allowlist, a request whose origin host is not on it, or that carries neither header, is refused. Unknown, unpublished (draft, archived), deleted and origin-restricted forms are all the same 404 NOT_FOUND (message "Form not found"); the response never says which.
// @Description Unauthenticated: no credentials are needed or accepted. Cross-origin calls get a permissive, credential-less CORS policy (the request Origin is echoed, methods GET, POST, OPTIONS, header Content-Type; preflights answer 204).
// @Tags public-forms
// @Produce json
// @Param key path string true "Public form identifier (the form's public_id)"
// @Success 200 {object} utils.APIResponse{data=service.PublicFormDefinition} "Form definition retrieved successfully"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no published form with this identifier is available to this origin"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS (JSON envelope) - generous tier: 240 requests/min, burst 40, per client IP, one bucket shared by GET embed.js, GET {key} and GET {key}/view; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure"
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
// @Description Accept a submission for a published form. The body carries the field values, the consent flag, the challenge handed out with the definition, the honeypot input, the reCAPTCHA token when the form uses it, and the page URL. Bodies are capped at 64 KiB (65536 bytes).
// @Description Checks run in a fixed order and the first that trips decides: (1) values are validated against the definition; (2) the challenge must be present and correctly signed; then the spam layers: (3) time trap - submitted less than 3 seconds or more than 24 hours after the definition was served; (4) origin - the Origin header (or Referer) host must be on the form's allowlist when it has one; (5) honeypot - the decoy input must be empty; (6) reCAPTCHA v3, only when the form enables it and the server has both keys - a missing token, a score below RECAPTCHA_MIN_SCORE or an unreachable verification service fails it.
// @Description A submission caught by a spam layer is stored with status spam and its reason and answered with exactly the same 200 outcome a genuine one gets, so the response reveals nothing about the layers; no mail is sent and no lead is touched. This is deliberate.
// @Description Side effects of a genuine submission: without double opt-in it is stored as received; when the form creates leads, the newest live lead with the same email gains a note (automation event lead_updated, origin form) or a new lead is created (status new, owner default_owner_id, source the form name; automation event lead_created, origin form), in the same transaction; after the commit one notification mail goes to each notify_emails address and, when follow_up_subject is set, the follow-up mail goes to the submitter. With double opt-in it is stored as pending, earlier outstanding confirmation links for the same form and address are invalidated, and a confirmation mail with a single-use link valid for 48 hours goes to the submitted address; lead, notification and follow-up wait for POST /forms/public/confirm. Mail failures are logged and never change the response.
// @Description Errors: 400 VALIDATION_ERROR (message "The submission could not be accepted") with error.details mapping field names to messages - an undeclared field ("unknown field"), a missing required value, a value longer than its max_length, an invalid email, a value that is not one of a select's options, "consent" when the form has consent text and consent is not true, or "challenge" when the challenge is missing or invalid (reported only once the values are valid); 400 BAD_REQUEST ("Invalid request body") for a body that is not valid JSON; 404 NOT_FOUND for an unknown, unpublished or deleted form (an origin outside the allowlist is NOT a 404 here: it is stored as spam); 413 with code VALIDATION_ERROR ("Submission is too large") for a body over 64 KiB.
// @Description Unauthenticated: no credentials are needed or accepted. Cross-origin calls get a permissive, credential-less CORS policy (the request Origin is echoed, methods GET, POST, OPTIONS, header Content-Type; preflights answer 204).
// @Tags public-forms
// @Accept json
// @Produce json
// @Param key path string true "Public form identifier (the form's public_id)"
// @Param submission body service.PublicSubmissionRequest true "Submitted values"
// @Success 200 {object} utils.APIResponse{data=service.SubmitOutcome} "Submission accepted (or silently filed as spam); the outcome says whether to show a message or redirect"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - error.details maps field names to messages; BAD_REQUEST - malformed JSON"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no published form with this identifier"
// @Failure 413 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR - submission body exceeds 64 KiB"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS (JSON envelope) - strict tier: 10 requests/min, burst 5, per client IP, one bucket shared by public submissions and both confirmation routes (lifted by DISABLE_RATE_LIMIT=true); error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure, including a request cancelled during the reCAPTCHA check"
// @Router /forms/public/{key}/submissions [post]
func (h *FormPublicHandler) Submit(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "FormPublicHandler.Submit")

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, formSubmissionMaxBody)

	var req service.PublicSubmissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			logger.WithError(utils.LogError(err)).Warn("Form submission body exceeds the size limit")
			utils.RespondError(c, http.StatusRequestEntityTooLarge, utils.ErrCodeValidation,
				"Submission is too large", nil)
			return
		}
		logger.WithError(utils.LogError(err)).Warn("Malformed form submission body")
		utils.RespondBadRequest(c, "Invalid request body")
		return
	}

	outcome, err := h.formService.SubmitPublic(c.Request.Context(), c.Param("key"), &req, service.SubmissionMeta{
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
// answers in HTML rather than in the envelope. The key is not checked here
// — the renderer's own definition request decides whether anything appears,
// which keeps this page free of any enumeration signal.
//
// ViewPage godoc
// @Summary Hosted form page
// @Description A standalone HTML page (shareable URL) that loads the embed script for this key, so a form can be used without embedding it anywhere. The key is not checked: the page is the same 200 text/html shell for any key, and only the script's own definition request decides whether a form appears (an unknown, unpublished or origin-restricted key renders nothing). The definition request from this page normally carries no Origin header but a Referer naming this server, so a form with an allowlist renders here only when this server's host is listed. The page is marked noindex, nofollow and needs JavaScript.
// @Description Unauthenticated.
// @Tags public-forms
// @Produce html,json
// @Param key path string true "Public form identifier (the form's public_id)"
// @Success 200 {string} string "HTML page (text/html; charset=utf-8)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS (JSON envelope) - generous tier: 240 requests/min, burst 40, per client IP, one bucket shared by GET embed.js, GET {key} and GET {key}/view; error.details.retry_after is '60s'; no Retry-After header"
// @Router /forms/public/{key}/view [get]
func (h *FormPublicHandler) ViewPage(c *gin.Context) {
	c.Data(http.StatusOK, formPageContentType,
		formHostedViewPage(h.apiPrefix+"/forms/public/embed.js", c.Param("key")))
}

// ConfirmPage renders the button a visitor presses to confirm their address.
// It deliberately does nothing else: a GET never spends the token, so mail
// scanners and link previews that fetch every URL in a message cannot confirm
// on the visitor's behalf or burn the link before they click it.
//
// ConfirmPage godoc
// @Summary Double opt-in confirmation page
// @Description The page the link in a double opt-in confirmation mail opens (PUBLIC_BASE_URL + API prefix + /forms/public/confirm?token=...). It renders an HTML page with a "Confirm email" button that POSTs the token to /forms/public/confirm. A GET never spends or even checks the token, so mail scanners and link previews cannot confirm on the visitor's behalf; any non-empty token gets the button page, a missing or blank one gets the generic "This link is no longer valid" page. The handler always answers 200 text/html, never the JSON envelope; only the rate limiter in front of it answers in JSON (429 TOO_MANY_REQUESTS).
// @Description Unauthenticated.
// @Tags public-forms
// @Produce html,json
// @Param token query string false "Confirmation token from the mailed link"
// @Success 200 {string} string "HTML page: the confirmation button, or the invalid-link page when the token is missing (text/html; charset=utf-8)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS (JSON envelope) - strict tier: 10 requests/min, burst 5, per client IP, one bucket shared by public submissions and both confirmation routes (lifted by DISABLE_RATE_LIMIT=true); error.details.retry_after is '60s'; no Retry-After header"
// @Router /forms/public/confirm [get]
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
//
// Confirm godoc
// @Summary Confirm a double opt-in submission
// @Description Spend a confirmation token. The token is read from the form-encoded field token (what the confirmation page posts) or, when that is empty, from a JSON body {"token": "..."}. On success the pending submission becomes confirmed (confirmed_at set) and, in the same transaction, the token is claimed and the lead is created or noted exactly as for a submission without double opt-in (automation event lead_created or lead_updated, origin form); after the commit the notification mails and the follow-up mail go out, and the response is the "Email confirmed" page. Tokens are single-use and expire 48 hours after the submission; a re-submission of the same form and address invalidates earlier tokens.
// @Description Every failure - missing, unknown, expired or already used token, a submission that is no longer pending, a deleted form or submission, a stored value too long for its lead column, or an internal error - renders the same "This link is no longer valid" page with the same 200 status, so token validity cannot be probed. A failure after the claim and before the commit rolls back, so the link stays usable for a retry; when the COMMIT itself fails the outcome is ambiguous (the lead may have been created and the link spent) although the page still says the link is no longer valid. The handler always answers 200 text/html, never the JSON envelope; only the rate limiter in front of it answers in JSON (429 TOO_MANY_REQUESTS).
// @Description Unauthenticated.
// @Tags public-forms
// @Accept x-www-form-urlencoded
// @Accept json
// @Produce html,json
// @Param token formData string false "Confirmation token (form-encoded; a JSON body with a token field is accepted too)"
// @Success 200 {string} string "HTML page: 'Email confirmed' on success, otherwise the generic invalid-link page (text/html; charset=utf-8)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS (JSON envelope) - strict tier: 10 requests/min, burst 5, per client IP, one bucket shared by public submissions and both confirmation routes (lifted by DISABLE_RATE_LIMIT=true); error.details.retry_after is '60s'; no Retry-After header"
// @Router /forms/public/confirm [post]
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

	if err := h.formService.ConfirmSubmission(c.Request.Context(), token); err != nil {
		if errors.Is(err, service.ErrInvalidConfirmationToken) {
			logger.WithError(utils.LogError(err)).Warn("Form confirmation token rejected")
		} else {
			logger.WithError(utils.LogError(err)).Error("Form confirmation failed")
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
		logger.WithError(utils.LogError(err)).Warn("Form submission failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation,
			"The submission could not be accepted", fieldErrors)
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(utils.LogError(err)).Warn("Form request failed validation")
		utils.RespondError(c, http.StatusBadRequest, utils.ErrCodeValidation,
			"The submission could not be accepted", nil)
	case apperrors.IsNotFound(err):
		logger.WithError(utils.LogError(err)).Warn("Public form not available")
		utils.RespondNotFound(c, "Form not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Public form request failed")
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
