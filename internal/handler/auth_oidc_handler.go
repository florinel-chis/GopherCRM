package handler

import (
	"errors"
	"net/http"
	"path"
	"time"

	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Limits of the OIDC callback body and of the front-channel sign-out query,
// in bytes.
const (
	oidcCallbackCodeMaxBytes  = 4096
	oidcCallbackStateMaxBytes = 256
	oidcFrontChannelSIDMax    = 256
	oidcFrontChannelIssMax    = 512
)

// OIDCStateCookieName is the sign-in cookie POST /auth/oidc/start sets and
// POST /auth/oidc/callback requires (#528). It holds the raw state of the
// sign-in, is HttpOnly and SameSite=Lax, reaches only the two OIDC routes and
// authenticates nothing on its own.
const OIDCStateCookieName = "gophercrm_oidc_state"

// oidcStateCookieMaxAge is the lifetime of the sign-in cookie in seconds: the
// lifetime of the attempt it names.
const oidcStateCookieMaxAge = int(service.OIDCAttemptTTL / time.Second)

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

// OIDCCallbackResponse is the answer of POST /auth/oidc/callback: the session
// of AuthResponse plus the ID token of the sign-in.
//
// @Description Session credentials of a single sign-on sign-in and its ID token.
type OIDCCallbackResponse struct {
	AuthResponse
	// The raw ID token the issuer returned for this sign-in. It is not a
	// credential for this API: keep it with the session and send it as
	// id_token_hint of POST /auth/logout, so the logout can also end the
	// session at the identity provider. It is never stored server-side.
	IDToken string `json:"id_token" example:"eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0.eyJpc3MiOiJodHRwczovL2lkcC5leGFtcGxlLmNvbSIsInN1YiI6IjI0NDAwMzIwIn0.c2lnbmF0dXJl"`
}

// Start godoc
// @Summary Start an OpenID Connect sign-in
// @Description Begins a sign-in with the issuer and client of the single sign-on settings (re-read on every call): discovers the issuer, stores a single-use attempt (the HMAC of a fresh state, a nonce and a PKCE verifier, valid ten minutes) and returns the issuer's authorization URL with redirect_uri APP_BASE_URL/auth/callback. The scope is the configured scopes; for an Entra ID issuer (host login.microsoftonline.com) whose role mapping has a groups entry it also carries https://graph.microsoft.com/User.Read, once. No body.
// @Description On 200 only, sets the sign-in cookie gophercrm_oidc_state, holding the state of the returned URL: Path API_PREFIX/auth/oidc (so it reaches only start and callback), Max-Age=600 (the lifetime of the attempt), HttpOnly, SameSite=Lax, no Domain, and sent only over HTTPS when APP_BASE_URL is https. The callback needs it, so the SPA and the API must be served from the same site. A refused start sets no cookie.
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
	authz, err := h.oidc.Start(utils.WithRequestID(c.Request.Context(), c.GetString("request_id")))
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OIDCStateCookieName,
		Value:    authz.State,
		Path:     oidcCookiePath(c),
		MaxAge:   oidcStateCookieMaxAge,
		HttpOnly: true,
		Secure:   authz.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	utils.RespondSuccess(c, http.StatusOK, OIDCStartResponse{AuthorizationURL: authz.URL})
}

// oidcCookiePath is the directory of the matched route: API_PREFIX/auth/oidc
// for start and callback alike.
func oidcCookiePath(c *gin.Context) string {
	return path.Dir(c.FullPath())
}

// Callback godoc
// @Summary Finish an OpenID Connect sign-in
// @Description Spends the attempt the state names (single-use, ten minutes, started against the issuer still configured), exchanges the code with the PKCE verifier and redirect_uri APP_BASE_URL/auth/callback, and verifies the ID token: signature, iss, aud, exp and the nonce. The identity is the exact (iss, sub) pair; a sub with leading or trailing whitespace is refused. The role is the first role-mapping entry, in stored order, whose claim (groups or roles, each absent or an array of strings) holds its value exactly. When the ID token moves groups into _claim_names (an overflow of groups) and the mapping has a groups entry: for an Entra ID issuer (host login.microsoftonline.com) the group object ids come from Microsoft Graph POST /v1.0/me/getMemberObjects (security groups only) with the access token of the exchange, within one 5 s budget and at most OIDC_GRAPH_MAX_PAGES pages, and any Graph failure refuses; for any other issuer the sign-in is refused. With no groups entry in the mapping that overflow is ignored. When the mapping has a groups entry and the ID token has no groups claim (an empty groups array counts as present) and no Entra ID overflow is being resolved, the groups come from the issuer's userinfo endpoint (from discovery): one GET with the access token of the exchange, within 5 s, no redirects followed, a body of at most 1 MiB of application/json (signed application/jwt userinfo is not supported); the userinfo sub must equal the ID token's sub, only groups is read (absent means no groups), and any other userinfo failure refuses. email, preferred_username and name are never used to link an account. An unknown identity whose issuer is Entra ID (host login.microsoftonline.com), whose tid claim equals the tenant segment of the configured issuer (its first path segment, compared exactly) and whose oid claim equals the subject of the identity SCIM created under that issuer signs in as that account, which must be live and active or the sign-in is refused, and links its (iss, sub) pair to it; only after that does an unknown identity provision.
// @Description A known identity signs in as its user, which must be live and active; the mapped role is re-applied through the governed role write (the last active admin cannot be demoted; a change is audited with auth_method oidc). An unknown identity provisions an active user with the mapped role, the email claim and given_name/family_name (or name split at the first space when both are absent), and no usable password. While allow_password in /settings/sso is off (the default), password reset and password sign-in are refused for a linked account (POST /auth/password-reset issues nothing, /auth/password-reset/confirm answers the generic 400 and POST /auth/login the generic 401); with it on, a linked account may set and use a password like a local one. The email_verified claim is not required or read and the email claim is stored as given: the email never links or authorizes an account. Provisioning is refused when the token has no email claim or any account, erased ones included, already holds that address in any letter case, and it is never linked to that account. last_login_at and the lockout counters are not touched.
// @Description The callback needs the gophercrm_oidc_state cookie that POST /auth/oidc/start set, sent by the browser that called start: the attempt is spent first, then a missing cookie, or one naming another sign-in, is the same 401. A later start in the same browser replaces the cookie, so only the latest sign-in started there can finish. The cookie is never a credential on any other route.
// @Description Success returns the same token, refresh_token and user as POST /auth/login, plus id_token, the raw ID token of the exchange, for the client to keep with the session and send as id_token_hint of POST /auth/logout (the server never stores it). The refresh token records a keyed hash of the issuer and the ID token's sid claim, when it has one, so GET /auth/oidc/frontchannel-logout can end the session later. It clears the cookie (Max-Age=0, same path). The session can be refreshed through POST /auth/refresh until 24 hours after this sign-in, and the refresh token expires then (REFRESH_TOKEN_EXPIRY_DAYS does not extend it); after that the person signs in at the identity provider again. Every refusal is the same 401 Single sign-on failed and sets no cookie. Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body OIDCCallbackRequest true "The code and state from the issuer's redirect"
// @Success 200 {object} utils.APIResponse{data=OIDCCallbackResponse} "Signed in; token, refresh_token, user and id_token returned"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: code or state missing, or code longer than 4096 bytes or state longer than 256 bytes (details maps Code or State to a message). BAD_REQUEST: malformed JSON (Invalid request format)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: Single sign-on failed, whatever the reason (unknown, used or expired state, no sign-in cookie, or one from another sign-in, issuer changed, token or nonce invalid, claims malformed or unmapped, groups not readable from Microsoft Graph, groups not readable from the userinfo endpoint or a userinfo sub that differs, account deactivated, erased or already holding the address, last admin)"
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

	binding := ""
	if cookie, err := c.Request.Cookie(OIDCStateCookieName); err == nil {
		binding = cookie.Value
	}
	tokens, err := h.oidc.Callback(auditContext(c), req.Code, req.State, binding)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	// The attempt is spent: clear the sign-in cookie. Refusals leave it, so
	// the untouched callback of the same sign-in can still finish.
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OIDCStateCookieName,
		Path:     oidcCookiePath(c),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	utils.LogHandlerResponse(logger, http.StatusOK, "single sign-on succeeded")
	utils.RespondSuccess(c, http.StatusOK, OIDCCallbackResponse{
		AuthResponse: AuthResponse{
			Token:        tokens.AccessToken,
			RefreshToken: tokens.RefreshToken,
			User:         tokens.User,
		},
		IDToken: tokens.IDToken,
	})
}

// FrontChannelLogout godoc
// @Summary Single sign-on front-channel sign-out
// @Description The identity provider's front-channel sign-out (OpenID Connect Front-Channel Logout 1.0): the provider loads this URL in a hidden frame of the browser when the person signs out there, naming its session by sid. No credential; register https://<API origin>API_PREFIX/auth/oidc/frontchannel-logout as the front-channel logout URL of the client. iss is optional (Entra ID sends only sid); when sent it must equal the configured issuer exactly, and any other value ends nothing and still answers 200.
// @Description Every account with a live refresh token whose single sign-on sign-in carried this sid (its ID token's sid claim; a token without one is never reached) under the configured issuer is logged out as POST /auth/logout without a body does: every refresh token of the account is revoked, its token_version is incremented so every access token issued before is refused with 401, and one logout row is written to the sign-in audit. That ends all of the account's sessions, password sessions and single sign-on sessions on other devices included, not only the one that signed in under this sid. The sid is compared as stored, a keyed hash of the issuer and the sid, so a sid that differs in letter case ends nothing. API keys are not affected.
// @Description The answer is 200 with a message whether or not a session ended, with Cache-Control no-cache, no-store and Pragma no-cache, and without X-Frame-Options or a Content-Security-Policy, so the provider can load it in a frame. Nothing about the sid, the issuer or the accounts is logged except how many accounts ended. Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route.
// @Tags auth
// @Produce json
// @Param sid query string true "The provider's session id (the sid claim), at most 256 bytes" maxlength(256)
// @Param iss query string false "The issuer; when sent, must equal the configured issuer exactly. At most 512 bytes" maxlength(512)
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Handled (message: Signed out); also when no session matched"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: sid missing, sid longer than 256 bytes or iss longer than 512 bytes (details maps SID or Iss to a message)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: single sign-on is disabled"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the settings could not be read or the sessions could not be ended"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "SSO_UNAVAILABLE: single sign-on is enabled but the issuer or client is not configured"
// @Router /auth/oidc/frontchannel-logout [get]
func (h *AuthOIDCHandler) FrontChannelLogout(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthOIDCHandler.FrontChannelLogout")
	// Every answer, a refusal included, must not be cached by the browser
	// or a proxy (Front-Channel Logout 1.0, section 3).
	c.Header("Cache-Control", "no-cache, no-store")
	c.Header("Pragma", "no-cache")

	// sid is the provider's session id (the sid claim of the ID tokens it
	// issued in that session); iss is optional (Entra ID leaves it out).
	sid, iss := c.Query("sid"), c.Query("iss")
	fields := map[string]string{}
	switch {
	case sid == "":
		fields["SID"] = "is required"
	case len(sid) > oidcFrontChannelSIDMax:
		fields["SID"] = "must be at most 256 bytes"
	}
	if len(iss) > oidcFrontChannelIssMax {
		fields["Iss"] = "must be at most 512 bytes"
	}
	if len(fields) > 0 {
		utils.RespondValidationError(c, fields)
		return
	}

	if _, err := h.oidc.FrontChannelLogout(auditContext(c), iss, sid); err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, "front-channel sign-out handled")
	utils.RespondSuccess(c, http.StatusOK, MessageResponse{Message: "Signed out"})
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
