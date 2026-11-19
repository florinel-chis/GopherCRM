package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// quoteResponseRejectedMessage is the one message of every refused client
// response link, whatever the cause: unknown, used, expired or invalidated
// token, or a quote that is no longer sent.
const quoteResponseRejectedMessage = "Invalid or expired quote link"

// quoteRenderRejectedMessage is the one message of every refused render
// credential: unknown, expired, or of a render that has finished.
const quoteRenderRejectedMessage = "Invalid or expired render link"

// QuotePublicHandler serves the unauthenticated client response link of a
// quote email (#382). It reads no credential and discloses only
// models.QuotePublicView. The token is never logged here: the server's own
// access log records the path without the query string, and nothing here puts
// the token on a log field. The read takes the token from the X-Quote-Token
// request header, not from the URL. A reverse proxy in front logs whatever it
// is configured to log, including the query string of the emailed
// /quote-response link; see the deployment docs.
type QuotePublicHandler struct {
	quoteService service.QuoteService
}

func NewQuotePublicHandler(quoteService service.QuoteService) *QuotePublicHandler {
	return &QuotePublicHandler{quoteService: quoteService}
}

// QuotePublicResponseRequest is the body of POST /quotes/public/response.
type QuotePublicResponseRequest struct {
	// The token from the link in the quote email. Required.
	Token string `json:"token" example:"3q2-7wEXAMPLEtokenEXAMPLEtokenEXAMPLEtoken0"`
	// The client's answer. Required.
	Decision string `json:"decision" enums:"accept,decline" example:"accept"`
}

// QuotePublicRenderRequest is the body of POST /quotes/public/render.
type QuotePublicRenderRequest struct {
	// The render credential from the fragment of the render page's URL.
	// Required. Every other field of the body is ignored.
	Token string `json:"token" example:"3q2-7wEXAMPLEtokenEXAMPLEtokenEXAMPLEtoken0"`
}

// respondError maps a service failure: the single generic rejection, or an
// internal error whose cause stays in the log.
func (h *QuotePublicHandler) respondError(c *gin.Context, err error, logMessage string) {
	if errors.Is(err, apperrors.ErrQuoteResponseRejected) {
		utils.RespondBadRequest(c, quoteResponseRejectedMessage)
		return
	}
	if errors.Is(err, apperrors.ErrQuoteRenderRejected) {
		utils.RespondBadRequest(c, quoteRenderRejectedMessage)
		return
	}
	utils.GetLogger(c).WithError(utils.LogError(err)).Error(logMessage)
	utils.RespondInternalError(c)
}

// View godoc
// @Summary Show the quote behind a client response link
// @Description Unauthenticated. Returns the quote the token of an emailed response link belongs to: its number, status, currency, validity date, lines and the five totals, and nothing else (no id, title, notes, owner, customer, deal or product reference). The request never writes, so opening the link does not spend the token.
// @Description A token is usable while it is unused, the quote is still sent and not deleted, and the day of the quote's valid_until (server's local date) has not ended; a quote without valid_until keeps its link while it is sent. A newer quote email, any staff status change away from sent and the erasure of the quote's customer invalidate the link. The token is read from the X-Quote-Token request header only; a token in the query string is ignored. A missing, empty, unknown, used, expired or invalidated token is the same 400 'Invalid or expired quote link', so validity cannot be probed. Rate limit: the strict tier, one bucket per client IP shared by both /quotes/public/response routes.
// @Tags public-quotes
// @Produce json
// @Param X-Quote-Token header string false "The token from the link in the quote email; missing or empty is the generic 400"
// @Success 200 {object} utils.APIResponse{data=models.QuotePublicView} "The quote as the client may see it; status is sent"
// @Header 200 {string} Cache-Control "no-store"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST 'Invalid or expired quote link': the X-Quote-Token header is missing or empty, or the token is unknown, used, expired or invalidated, or its quote is no longer sent"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict tier, 10 requests per minute, burst 5, per client IP, one bucket shared by both /quotes/public/response routes (lifted by DISABLE_RATE_LIMIT=true); error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /quotes/public/response [get]
func (h *QuotePublicHandler) View(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuotePublicHandler.View")

	view, err := h.quoteService.GetPublicResponse(c.GetHeader("X-Quote-Token"))
	if err != nil {
		h.respondError(c, err, "Quote response view failed")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, view)
}

// Respond godoc
// @Summary Accept or decline a quote through its client response link
// @Description Unauthenticated. Spends the token of an emailed response link and moves the quote from sent to accepted (decision accept) or rejected (decision decline). In one transaction the token is claimed, the quote's status and closed_at are written and one status history row is added; the history row has no user (changed_by_id null) and source client, so the staff history shows the answer as the client's. The linked deal is not changed. A failure before the commit rolls everything back and the link stays usable.
// @Description The body is validated before the token is looked at: malformed JSON, a missing token or a decision other than accept or decline is 400 VALIDATION_ERROR and leaves the link usable. The token is single use: a second request, and every unknown, used, expired or invalidated token or a quote that is no longer sent, is the same 400 'Invalid or expired quote link'. Of two concurrent requests with one token exactly one succeeds. Rate limit: the strict tier, one bucket per client IP shared by both /quotes/public/response routes.
// @Tags public-quotes
// @Accept json
// @Produce json
// @Param request body QuotePublicResponseRequest true "The token and the decision"
// @Success 200 {object} utils.APIResponse{data=models.QuotePublicView} "The quote with its new status, accepted or rejected"
// @Header 200 {string} Cache-Control "no-store"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: malformed JSON, token missing or empty, or decision not accept or decline (details names the field). BAD_REQUEST 'Invalid or expired quote link': the token is unknown, used, expired or invalidated, or its quote is no longer sent"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict tier, 10 requests per minute, burst 5, per client IP, one bucket shared by both /quotes/public/response routes (lifted by DISABLE_RATE_LIMIT=true); error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /quotes/public/response [post]
func (h *QuotePublicHandler) Respond(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuotePublicHandler.Respond")

	var req QuotePublicResponseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.RespondValidationError(c, map[string]string{"body": "must be a JSON object with token and decision"})
		return
	}
	if req.Token == "" {
		utils.RespondValidationError(c, map[string]string{"token": "is required"})
		return
	}
	if !models.IsQuoteResponseDecision(req.Decision) {
		utils.RespondValidationError(c, map[string]string{"decision": "must be accept or decline"})
		return
	}

	view, err := h.quoteService.RespondPublic(req.Token, req.Decision)
	if err != nil {
		h.respondError(c, err, "Quote response failed")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, view)
}

// Render godoc
// @Summary Load the quote for the PDF render page
// @Description Unauthenticated. Called by the render page of the web app (APP_BASE_URL/quote-render) while the server prints a quote to PDF: it returns the quote a render credential was minted for, exactly as GET /quotes/{id} returns it, and the product name that heads the document. It is a POST so that the credential travels in the body and never in a URL a proxy logs; it writes nothing.
// @Description A render credential is 32 random bytes the server mints for one render and keeps, as a hash, in the memory of the process that renders. It opens exactly one quote, read-only, once, while that render runs: at most 15 seconds. It is single use: a second presentation is refused and fails that render. It is not a session: sent as a Bearer token to any other route it is 401. The quote returned is always the credential's own; an id or any other field in the body is ignored. A missing or empty token, or a body that is not a JSON object, is 400 VALIDATION_ERROR; an unknown or expired credential, one already redeemed, one whose render has finished, or one presented to another server process is the same 400 'Invalid or expired render link'. With PDF output off every credential is refused. Rate limit: the generous tier, its own bucket per client IP.
// @Tags public-quotes
// @Accept json
// @Produce json
// @Param request body QuotePublicRenderRequest true "The render credential"
// @Success 200 {object} utils.APIResponse{data=models.QuoteRenderViewJSON} "The quote of the credential and the product name"
// @Header 200 {string} Cache-Control "no-store"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: malformed JSON, or token missing or empty (details names the field). BAD_REQUEST 'Invalid or expired render link': the credential is unknown, expired or already redeemed, its render has finished, or its quote no longer exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: generous tier, 240 requests per minute, burst 40, per client IP, a bucket of this route alone; error.details.retry_after is '60s'; no Retry-After header"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: unexpected failure; the message is always 'An unexpected error occurred'"
// @Router /quotes/public/render [post]
func (h *QuotePublicHandler) Render(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "QuotePublicHandler.Render")

	var req QuotePublicRenderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.RespondValidationError(c, map[string]string{"body": "must be a JSON object with token"})
		return
	}
	if req.Token == "" {
		utils.RespondValidationError(c, map[string]string{"token": "is required"})
		return
	}

	view, err := h.quoteService.GetRenderView(req.Token)
	if err != nil {
		h.respondError(c, err, "Quote render view failed")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, view)
}
