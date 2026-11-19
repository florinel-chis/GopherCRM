package handler

import (
	"errors"
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Limits of the OIDC callback body, in bytes.
const (
	oidcCallbackCodeMaxBytes  = 4096
	oidcCallbackStateMaxBytes = 256
)

// ErrCodeSSOUnavailable is answered with 503 when single sign-on is switched
// on but cannot be used: the issuer or client is not configured, the issuer
// cannot be discovered or redirects, or its token endpoint failed.
const ErrCodeSSOUnavailable = "SSO_UNAVAILABLE"

// AuthOIDCHandler serves the OpenID Connect sign-in (#435).
type AuthOIDCHandler struct {
	oidc service.OIDCService
}

// NewAuthOIDCHandler returns the handler of POST /auth/oidc/start and
// POST /auth/oidc/callback.
func NewAuthOIDCHandler(oidc service.OIDCService) *AuthOIDCHandler {
	return &AuthOIDCHandler{oidc: oidc}
}

// OIDCStartResponse is the answer of POST /auth/oidc/start.
//
// @Description Where to send the browser to sign in with the configured OpenID Connect issuer.
type OIDCStartResponse struct {
	// The issuer's authorization URL: response_type=code, the client_id, the
	// redirect_uri APP_BASE_URL/auth/callback, the scopes, a state, a nonce
	// and an S256 PKCE challenge. Valid for one callback within ten minutes.
	AuthorizationURL string `json:"authorization_url" format:"uri" example:"https://idp.example.com/authorize?client_id=crm&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256&nonce=n-0S6_WzA2Mj&redirect_uri=https%3A%2F%2Fcrm.example.com%2Fauth%2Fcallback&response_type=code&scope=openid+profile+email&state=af0ifjsldkj"`
}

// OIDCCallbackRequest is the body of POST /auth/oidc/callback: the two query
// parameters the issuer redirected the browser back with.
//
// @Description The authorization code and state the issuer returned to APP_BASE_URL/auth/callback.
type OIDCCallbackRequest struct {
	// Authorization code from the issuer; required, at most 4096 bytes.
	Code string `json:"code" binding:"required" maxLength:"4096" example:"SplxlOBeZQQYbYS6WxSbIA"`
	// The state of the authorization URL; required, at most 256 bytes.
	State string `json:"state" binding:"required" maxLength:"256" example:"af0ifjsldkj"`
}

// Start godoc
// @Summary Start an OpenID Connect sign-in
// @Description Begins a sign-in with the issuer and client of the single sign-on settings (re-read on every call): discovers the issuer, stores a single-use attempt (the HMAC of a fresh state, a nonce and a PKCE verifier, valid ten minutes) and returns the issuer's authorization URL with redirect_uri APP_BASE_URL/auth/callback. The scope is the configured scopes; for an Entra ID issuer (host login.microsoftonline.com) whose role mapping has a groups entry it also carries https://graph.microsoft.com/GroupMember.Read.All, once. No body. Sets no cookie.
// @Description Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route.
// @Tags auth
// @Produce json
// @Success 200 {object} utils.APIResponse{data=OIDCStartResponse} "The authorization URL"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: single sign-on is disabled"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the settings could not be read or the attempt could not be stored"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "SSO_UNAVAILABLE: single sign-on is enabled but the issuer or client is not configured, or the issuer cannot be discovered (unreachable, a redirect, a timeout)"
// @Router /auth/oidc/start [post]
func (h *AuthOIDCHandler) Start(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthOIDCHandler.Start")
	url, err := h.oidc.Start(c.Request.Context())
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.RespondSuccess(c, http.StatusOK, OIDCStartResponse{AuthorizationURL: url})
}

// Callback godoc
// @Summary Finish an OpenID Connect sign-in
// @Description Spends the attempt the state names (single-use, ten minutes, started against the issuer still configured), exchanges the code with the PKCE verifier and redirect_uri APP_BASE_URL/auth/callback, and verifies the ID token: signature, iss, aud, exp and the nonce. The identity is the exact (iss, sub) pair; a sub with leading or trailing whitespace is refused. The role is the first role-mapping entry, in stored order, whose claim (groups or roles, each absent or an array of strings) holds its value exactly. When the ID token moves groups into _claim_names (an overflow of groups) and the mapping has a groups entry: for an Entra ID issuer (host login.microsoftonline.com) the group object ids come from Microsoft Graph POST /v1.0/me/getMemberObjects (security groups only) with the access token of the exchange, within one 5 s budget and at most OIDC_GRAPH_MAX_PAGES pages, and any Graph failure refuses; for any other issuer the sign-in is refused. With no groups entry in the mapping that overflow is ignored. When the mapping has a groups entry and the ID token has no groups claim (an empty groups array counts as present) and no Entra ID overflow is being resolved, the groups come from the issuer's userinfo endpoint (from discovery): one GET with the access token of the exchange, within 5 s, no redirects followed, a body of at most 1 MiB of application/json (signed application/jwt userinfo is not supported); the userinfo sub must equal the ID token's sub, only groups is read (absent means no groups), and any other userinfo failure refuses. email, preferred_username and name are never used to link an account.
// @Description A known identity signs in as its user, which must be live and active; the mapped role is re-applied through the governed role write (the last active admin cannot be demoted; a change is audited with auth_method oidc). An unknown identity provisions an active user with the mapped role, the email claim and given_name/family_name, and no usable password. While allow_password in /settings/sso is off (the default), password reset and password sign-in are refused for a linked account (POST /auth/password-reset issues nothing, /auth/password-reset/confirm answers the generic 400 and POST /auth/login the generic 401); with it on, a linked account may set and use a password like a local one. The email_verified claim is not required or read and the email claim is stored as given: the email never links or authorizes an account. Provisioning is refused when the token has no email claim or any account, erased ones included, already holds that address in any letter case, and it is never linked to that account. last_login_at and the lockout counters are not touched.
// @Description Success returns the same token, refresh_token and user as POST /auth/login. Every refusal is the same 401 Single sign-on failed. Sets no cookie. Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body OIDCCallbackRequest true "The code and state from the issuer's redirect"
// @Success 200 {object} utils.APIResponse{data=AuthResponse} "Signed in; token, refresh_token and user returned"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: code or state missing, or code longer than 4096 bytes or state longer than 256 bytes (details maps Code or State to a message). BAD_REQUEST: malformed JSON (Invalid request format)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: Single sign-on failed, whatever the reason (unknown, used or expired state, issuer changed, token or nonce invalid, claims malformed or unmapped, groups not readable from Microsoft Graph, groups not readable from the userinfo endpoint or a userinfo sub that differs, account deactivated, erased or already holding the address, last admin)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: single sign-on is disabled"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: a database or signing failure"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "SSO_UNAVAILABLE: single sign-on is enabled but not configured, the issuer cannot be discovered, or its token endpoint is unreachable or answered with an error"
// @Router /auth/oidc/callback [post]
func (h *AuthOIDCHandler) Callback(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthOIDCHandler.Callback")

	var req OIDCCallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	fields := map[string]string{}
	if len(req.Code) > oidcCallbackCodeMaxBytes {
		fields["Code"] = "must be at most 4096 bytes"
	}
	if len(req.State) > oidcCallbackStateMaxBytes {
		fields["State"] = "must be at most 256 bytes"
	}
	if len(fields) > 0 {
		utils.RespondValidationError(c, fields)
		return
	}

	tokens, err := h.oidc.Callback(c.Request.Context(), req.Code, req.State)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, "single sign-on succeeded")
	utils.RespondSuccess(c, http.StatusOK, AuthResponse{
		Token:        tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		User:         tokens.User,
	})
}

func (h *AuthOIDCHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, service.ErrSSODisabled):
		utils.RespondNotFound(c, "Single sign-on is disabled")
	case errors.Is(err, service.ErrSSOUnavailable):
		utils.RespondError(c, http.StatusServiceUnavailable, ErrCodeSSOUnavailable, "Single sign-on is unavailable", nil)
	case errors.Is(err, service.ErrSSOSignInFailed):
		utils.RespondUnauthorized(c, "Single sign-on failed")
	default:
		logger.WithError(utils.LogError(err)).Error("Single sign-on failed with an internal error")
		utils.RespondInternalError(c)
	}
}
