package handler

import (
	"errors"
	"net/http"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	authService service.AuthService
	userService service.UserService
}

func NewAuthHandler(authService service.AuthService, userService service.UserService) *AuthHandler {
	return &AuthHandler{
		authService: authService,
		userService: userService,
	}
}

// RegisterRequest deliberately has no Role field. Self-service registration always
// creates a customer; elevated roles are only assignable through the admin-guarded
// POST /users endpoint. A client-supplied role here would be a privilege escalation.
//
// @Description Self-service sign-up. There is no role field: the account is always a customer.
type RegisterRequest struct {
	// Login email address; must be a valid address and not already used by any
	// account (409 otherwise).
	Email string `json:"email" binding:"required,email" format:"email" example:"jane.doe@example.com"`
	// Password: at least 10 characters with an uppercase letter, a lowercase
	// letter, a digit and a special character (punctuation or symbol). bcrypt
	// cannot hash more than 72 bytes; a longer password is answered with 400.
	Password string `json:"password" binding:"required,min=10" minLength:"10" example:"Str0ng!Passw0rd"`
	// Given name; required, non-empty.
	FirstName string `json:"first_name" binding:"required" example:"Jane"`
	// Family name; required, non-empty.
	LastName string `json:"last_name" binding:"required" example:"Doe"`
}

// LoginRequest carries only the credentials the backend actually uses. Token
// lifetime is fixed by JWT_EXPIRY_HOURS, so there is no "remember me" input
// here; that choice is made client-side when deciding where to store the token.
// Any extra JSON keys a client sends are ignored by the binder.
//
// @Description Credentials for POST /auth/login. Unknown keys are ignored.
type LoginRequest struct {
	// Login email address; must be syntactically valid (400 otherwise).
	Email string `json:"email" binding:"required,email" format:"email" example:"jane.doe@example.com"`
	// Account password; required, no further format check.
	Password string `json:"password" binding:"required" example:"Str0ng!Passw0rd"`
}

// AuthResponse is the shared shape of login and refresh responses. Register
// responses omit refresh_token (self-service registration issues only a JWT).
// The refresh token is an opaque single-use value: presenting it at
// POST /auth/refresh revokes it and issues a replacement.
//
// @Description Session credentials returned by login, refresh and registration.
type AuthResponse struct {
	// Access token: an HS256-signed JWT (claims user_id, email, role, exp) valid
	// for JWT_EXPIRY_HOURS (24 hours by default). Send it as
	// `Authorization: Bearer <token>`. It cannot be revoked before it expires.
	Token string `json:"token" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyX2lkIjo0Mn0.c2lnbmF0dXJl"`
	// Opaque single-use refresh token (43 URL-safe base64 characters) for
	// POST /auth/refresh, valid for REFRESH_TOKEN_EXPIRY_DAYS (30 days by
	// default). Absent on registration, and on a login whose refresh token could
	// not be stored (the login then succeeds with the access token only).
	RefreshToken string `json:"refresh_token,omitempty" example:"Jx3k9QeV0m2b5sT8wY1zA4cD7fG0hJ3kL6nP9qR2tU5"`
	// The authenticated account.
	User *models.User `json:"user"`
}

// @Description Body of POST /auth/refresh.
type RefreshRequest struct {
	// The refresh token from the latest login, refresh or password change. It
	// is spent by this call.
	RefreshToken string `json:"refresh_token" binding:"required" example:"Jx3k9QeV0m2b5sT8wY1zA4cD7fG0hJ3kL6nP9qR2tU5"`
}

// LogoutRequest is optional: logout with no body (or an empty one) revokes
// every refresh token of the authenticated user.
//
// @Description Optional body of POST /auth/logout.
type LogoutRequest struct {
	// Refresh token to revoke. Omitted or empty: every refresh token of the
	// caller is revoked. A token that is unknown, already revoked or owned by
	// another user is ignored.
	RefreshToken string `json:"refresh_token" example:"Jx3k9QeV0m2b5sT8wY1zA4cD7fG0hJ3kL6nP9qR2tU5"`
}

// ChangePasswordRequest carries the current browser session in the optional
// refresh_token: when present (and the request authenticated with a bearer
// token) that session is renewed and the response carries its replacement
// credentials. It is ignored for API-key requests.
//
// @Description Body of POST /auth/change-password.
type ChangePasswordRequest struct {
	// The account's current password; a wrong value is a 400.
	CurrentPassword string `json:"current_password" binding:"required" example:"Str0ng!Passw0rd"`
	// New password: at least 10 characters with an uppercase letter, a
	// lowercase letter, a digit and a special character. More than 72 bytes
	// fails with 500 (bcrypt limit).
	NewPassword string `json:"new_password" binding:"required,min=10" minLength:"10" example:"N3w!Passw0rd2026"`
	// Optional: the caller's current refresh token. With bearer
	// authentication it renews that session (the response then carries new
	// credentials); it must be a live refresh token of the caller or the
	// request fails with 409 SESSION_STALE. Ignored for API-key requests.
	RefreshToken string `json:"refresh_token,omitempty" example:"Jx3k9QeV0m2b5sT8wY1zA4cD7fG0hJ3kL6nP9qR2tU5"`
}

// ChangePasswordResponse always carries the message. token, refresh_token and
// user are present only when the current session was renewed.
//
// @Description Result of POST /auth/change-password. token, refresh_token and user are present only when the current session was renewed.
type ChangePasswordResponse struct {
	// Confirmation text, always "Password changed successfully".
	Message string `json:"message" example:"Password changed successfully"`
	// Replacement access token (JWT); present only when the session was renewed.
	Token string `json:"token,omitempty" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyX2lkIjo0Mn0.c2lnbmF0dXJl"`
	// Replacement refresh token; present only when the session was renewed.
	RefreshToken string `json:"refresh_token,omitempty" example:"Vb7n1Mq4We8rT2yU6iO0pA3sD9fG5hJ1kL4zX7cV0bN"`
	// The account; present only when the session was renewed.
	User *models.User `json:"user,omitempty"`
}

// @Description Body of POST /auth/password-reset.
type PasswordResetRequest struct {
	// Email address of the account to reset; must be syntactically valid. The
	// response does not reveal whether an account exists.
	Email string `json:"email" binding:"required,email" format:"email" example:"jane.doe@example.com"`
}

// @Description Body of POST /auth/password-reset/confirm.
type PasswordResetConfirmRequest struct {
	// The token from the emailed link (its token query parameter). Single use,
	// valid for one hour.
	Token string `json:"token" binding:"required" example:"q8Zt3Lm0Xw5Rv9Kp2Hn6Bc1Yd4Fg7Js0Ua3Ei6Oo9Pq"`
	// New password: at least 10 characters with an uppercase letter, a
	// lowercase letter, a digit and a special character. More than 72 bytes
	// fails with 500 (bcrypt limit) without spending the token.
	NewPassword string `json:"new_password" binding:"required,min=10" minLength:"10" example:"N3w!Passw0rd2026"`
}

// @Description A confirmation message.
type MessageResponse struct {
	// Human-readable confirmation; informational, not a stable value.
	Message string `json:"message" example:"Logged out successfully"`
}

// Register godoc
// @Summary Register a new customer account
// @Description Public self-service sign-up, open only while the security.allow_public_registration setting is true (it ships false; GET /auth/registration reports the current state). The gate fails closed: a setting that cannot be read is treated as false. The account is always created with the customer role and active; the body has no role field and nothing in it can change the role. Elevated roles come only from the admin-guarded POST /users.
// @Description On success the response carries a JWT for the new account and the user, but no refresh token: the client signs in through POST /auth/login to obtain one. Password policy: at least 10 characters with an uppercase letter, a lowercase letter, a digit and a special character (punctuation or symbol); bcrypt rejects more than 72 bytes.
// @Description Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, one bucket shared by every unauthenticated /auth route. No side effects besides the new user row: no mail is sent and no automation event is recorded.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Registration details"
// @Success 201 {object} utils.APIResponse{data=AuthResponse} "Account created; token and user returned (no refresh_token)"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: a field failed its binding rule (details maps each Go field name, e.g. Email, to a message). BAD_REQUEST: malformed JSON (Invalid request format), the password fails the complexity policy (message names the missing class), or the user could not be stored (the underlying message is returned, e.g. a password over 72 bytes)"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: public registration is disabled or the setting could not be read"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: an account already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the token could not be signed (the account has already been created)"
// @Router /auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthHandler.Register")

	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	if err := utils.ValidatePasswordComplexity(req.Password); err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	user := &models.User{
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Role:      models.RoleCustomer,
	}

	if err := h.userService.Register(user, req.Password); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Registration failed")
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			utils.RespondConflict(c, "user with this email already exists")
		} else {
			utils.RespondBadRequest(c, err.Error())
		}
		return
	}

	token, err := h.authService.GenerateJWT(user)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to generate token")
		utils.RespondInternalError(c)
		return
	}

	response := AuthResponse{
		Token: token,
		User:  user,
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, response)
	utils.RespondSuccess(c, http.StatusCreated, response)
}

// Login godoc
// @Summary Log in and obtain session credentials
// @Description Exchanges email and password for an access token (JWT, valid JWT_EXPIRY_HOURS, 24 hours by default), a single-use refresh token for POST /auth/refresh (valid REFRESH_TOKEN_EXPIRY_DAYS, 30 days by default, stored server-side only as a hash) and the user. If the refresh token cannot be stored the login still succeeds without one.
// @Description Every failure is the same 401 with the message Invalid email or password: unknown email, wrong password, locked account and deactivated or erased account cannot be told apart. After 5 consecutive wrong passwords the account is locked for 15 minutes; while locked even the right password is refused, and a lapsed lock starts a fresh five-attempt window. A successful login clears the counter and stamps last_login_at (the returned user still shows the previous value).
// @Description Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Credentials"
// @Success 200 {object} utils.APIResponse{data=AuthResponse} "Authenticated; token, refresh_token and user returned"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: email missing or not an email address, or password missing (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: Invalid email or password, whatever the cause (unknown email, wrong password, locked, deactivated or erased account, or a server-side failure while signing the token)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Router /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthHandler.Login")

	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	tokens, err := h.authService.LoginWithTokens(req.Email, req.Password)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Login failed")
		utils.RespondUnauthorized(c, "Invalid email or password")
		return
	}

	response := AuthResponse{
		Token:        tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		User:         tokens.User,
	}

	// Log the outcome without the token material.
	utils.LogHandlerResponse(logger, http.StatusOK, "login succeeded")
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Refresh godoc
// @Summary Rotate a refresh token
// @Description Strict rotation: the presented refresh token is spent and a new access token, a new refresh token and the user are returned, the same shape as login. Clients must keep the replacement from every response; presenting a spent token fails. Of several concurrent refreshes of one token exactly one succeeds.
// @Description Every rejection of the token (unknown, expired, revoked, already spent, or owned by a deactivated or erased account) is the same 401; the reason is never disclosed. A refresh whose owner turns out to be deactivated or erased also revokes the token. An operational failure (database, signing) is 503 SESSION_REFRESH_UNAVAILABLE: when it happened before the commit the rotation was rolled back and the presented token is still usable, so the client keeps it and retries; when the COMMIT itself failed the outcome is ambiguous and the retry may answer 401.
// @Description Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body RefreshRequest true "The refresh token to spend"
// @Success 200 {object} utils.APIResponse{data=AuthResponse} "Rotated; new token, refresh_token and user returned"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: refresh_token missing or empty (details maps RefreshToken to a message). BAD_REQUEST: malformed JSON (Invalid request format)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: Invalid or expired refresh token, whatever the reason"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 503 {object} utils.APIResponse{error=utils.APIError} "SESSION_REFRESH_UNAVAILABLE: operational failure; before the commit nothing rotated and the presented token is still usable, retry later. A failed COMMIT is ambiguous and the retry may answer 401"
// @Router /auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthHandler.Refresh")

	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	tokens, err := h.authService.RefreshAccessToken(req.RefreshToken)
	if err != nil {
		if errors.Is(err, service.ErrInvalidRefreshToken) {
			// Deliberately generic: every rejection of the token reads the
			// same (unknown, expired, replayed, dead owner).
			logger.WithError(utils.LogError(err)).Warn("Refresh token rejected")
			utils.RespondUnauthorized(c, "Invalid or expired refresh token")
			return
		}
		// An operational failure (database, signing) rolled the rotation
		// back, so the presented token is still usable. It is answered with
		// a retryable 503 rather than the 401 a client treats as the end of
		// the session; 503 rather than 500/502 keeps the JSON body intact
		// behind Cloudflare.
		logger.WithError(utils.LogError(err)).Error("Refresh failed")
		utils.RespondError(c, http.StatusServiceUnavailable, "SESSION_REFRESH_UNAVAILABLE",
			"Session renewal is temporarily unavailable. Please retry.", nil)
		return
	}

	response := AuthResponse{
		Token:        tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		User:         tokens.User,
	}

	utils.LogHandlerResponse(logger, http.StatusOK, "session rotated")
	utils.RespondSuccess(c, http.StatusOK, response)
}

// Logout godoc
// @Summary Log out (revoke refresh tokens)
// @Description Revokes refresh tokens of the caller (any authenticated role, bearer or API-key). With no body, an empty body or no refresh_token, every refresh token of the caller is revoked; with a refresh_token only that token is revoked, and only when it belongs to the caller. Idempotent: an unknown, already revoked or foreign token still answers 200 and discloses nothing.
// @Description Logout does not invalidate access tokens: an issued JWT stays valid until it expires (there is no blocklist), so the client must discard it. API keys are not affected.
// @Description Rate limit: the authenticated tier, 120 requests per minute with a burst of 30 per client IP, shared by every authenticated route and checked after authentication.
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body LogoutRequest false "Optional: the one refresh token to revoke"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Logged out (message: Logged out successfully)"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: a non-empty body that is not valid JSON (Invalid request format)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the refresh tokens could not be revoked"
// @Router /auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthHandler.Logout")

	userID := c.GetUint("user_id")
	if userID == 0 {
		utils.RespondUnauthorized(c, "Unauthorized")
		return
	}

	// The body is optional — the frontend calls logout with none at all.
	var req LogoutRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.Error(err).SetType(gin.ErrorTypeBind)
			return
		}
	}

	if err := h.authService.Logout(userID, req.RefreshToken); err != nil {
		logger.WithError(utils.LogError(err)).Error("Logout failed")
		utils.RespondInternalError(c)
		return
	}

	response := MessageResponse{Message: "Logged out successfully"}
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// ChangePassword godoc
// @Summary Change the caller's password
// @Description Any authenticated role. Re-authenticates with current_password, then replaces it with new_password (at least 10 characters with an uppercase letter, a lowercase letter, a digit and a special character). The password write, the revocation of the caller's refresh tokens and the renewal of the current session are one transaction: a failure before the commit leaves the password and every session unchanged. A COMMIT whose acknowledgement fails is ambiguous (the change may have been applied) and is reported as a 500 without credentials. The login lockout counter is not touched.
// @Description With a bearer token and the client's current refresh_token in the body, that session is renewed: the token is spent, every other refresh token of the caller is revoked, and the response carries a replacement token, refresh_token and user. A refresh_token that is not a live session of the caller (unknown, revoked, expired, foreign) is 409 SESSION_STALE and nothing changes. Without a refresh_token, or for an API-key request (whose refresh_token is ignored and which never receives session credentials), every refresh token is revoked and only the message is returned.
// @Description Revocation ends renewal, not access: access tokens already issued stay valid until they expire (there is no blocklist). A wrong current password is a 400, not a 401, because the request itself is authenticated. Rate limit: the authenticated tier (120 per minute, burst 30, per client IP).
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body ChangePasswordRequest true "Current and new password, optionally the current refresh token"
// @Success 200 {object} utils.APIResponse{data=ChangePasswordResponse} "Password changed; refresh tokens revoked; renewed credentials when a refresh_token was presented with a bearer token"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: a required field is missing or new_password is shorter than 10 characters (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format), new_password fails the complexity policy, or The current password is incorrect"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or invalid credentials, or the account was deactivated or erased during the request"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "SESSION_STALE: the presented refresh_token is not a live session of the caller; nothing was changed"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: storage failure (nothing changed when it came before the commit; a failed COMMIT is ambiguous), or a new_password over 72 bytes (bcrypt limit)"
// @Router /auth/change-password [post]
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthHandler.ChangePassword")

	userID := c.GetUint("user_id")
	if userID == 0 {
		utils.RespondUnauthorized(c, "Unauthorized")
		return
	}

	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	if err := utils.ValidatePasswordComplexity(req.NewPassword); err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	// Only a bearer (browser) session can be renewed. An API-key caller never
	// receives a refresh token or a JWT, whatever its body says.
	refreshToken := req.RefreshToken
	if c.GetString(service.AuthMethodContextKey) != service.AuthMethodBearer {
		refreshToken = ""
	}

	tokens, err := h.authService.ChangePassword(userID, req.CurrentPassword, req.NewPassword, refreshToken)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidCurrentPassword):
			utils.RespondBadRequest(c, "The current password is incorrect")
		case errors.Is(err, service.ErrSessionStale):
			utils.RespondError(c, http.StatusConflict, "SESSION_STALE",
				"Your session is no longer current. Sign in again, then change your password.", nil)
		case errors.Is(err, service.ErrAccountUnavailable):
			utils.RespondUnauthorized(c, "Unauthorized")
		default:
			logger.WithError(utils.LogError(err)).Error("Password change failed")
			utils.RespondInternalError(c)
		}
		return
	}

	response := ChangePasswordResponse{Message: "Password changed successfully"}
	if tokens != nil {
		response.Token = tokens.AccessToken
		response.RefreshToken = tokens.RefreshToken
		response.User = tokens.User
	}
	// Log the outcome without the token material.
	utils.LogHandlerResponse(logger, http.StatusOK, "password changed")
	utils.RespondSuccess(c, http.StatusOK, response)
}

// RequestPasswordReset godoc
// @Summary Request a password reset link
// @Description Public. Always the same 200 and the same message, whether or not an account exists for the email (anti-enumeration, like login). When an active account exists, its outstanding reset tokens are invalidated, a new single-use token valid for one hour is created and a link to APP_BASE_URL/reset-password?token=... is emailed (only logged when SMTP is not configured). A deactivated account receives nothing. Storage and delivery failures are logged server-side and never change the response.
// @Description Rate limit: the strict /auth tier, 10 requests per minute with a burst of 5 per client IP, shared by every unauthenticated /auth route.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body PasswordResetRequest true "Account email"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Uniform acknowledgement, independent of whether the account exists"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: email missing or not an email address (details maps Email to a message). BAD_REQUEST: malformed JSON (Invalid request format)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Router /auth/password-reset [post]
func (h *AuthHandler) RequestPasswordReset(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthHandler.RequestPasswordReset")

	var req PasswordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// The service swallows every account-dependent failure by design, so this
	// response is identical for existing and unknown accounts.
	if err := h.authService.RequestPasswordReset(req.Email); err != nil {
		logger.WithError(utils.LogError(err)).Error("Unexpected password reset failure")
	}

	response := MessageResponse{Message: "If an account exists for that email, a password reset link has been sent"}
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// ConfirmPasswordReset godoc
// @Summary Complete a password reset
// @Description Public. Spends the token from the emailed link and sets new_password, which must meet the policy (at least 10 characters with an uppercase letter, a lowercase letter, a digit and a special character). The policy is checked before the token is touched, so a weak password leaves the link usable. Tokens are single use and expire after one hour; an unknown, expired or used token, or one whose account is deactivated or erased, is the same generic 400.
// @Description In one transaction the token is claimed, the password written, the login lockout cleared and every refresh token of the account revoked. A failure before the commit rolls all of it back and the link stays usable; when the COMMIT itself fails the outcome is ambiguous (the password may have changed and the link been spent), so a retry may be refused as an invalid or expired link. Issued access tokens stay valid until they expire. Rate limit: the strict /auth tier (10 per minute, burst 5, per client IP).
// @Tags auth
// @Accept json
// @Produce json
// @Param request body PasswordResetConfirmRequest true "Reset token and new password"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Password reset; every refresh token revoked"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: token or new_password missing, or new_password shorter than 10 characters (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format), new_password fails the complexity policy, or Invalid or expired reset token"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: strict /auth tier exceeded; details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: storage failure (before the commit: rolled back and the link stays usable; a failed COMMIT is ambiguous and the reset may have taken effect) or a new_password over 72 bytes (bcrypt limit; the token is not spent)"
// @Router /auth/password-reset/confirm [post]
func (h *AuthHandler) ConfirmPasswordReset(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AuthHandler.ConfirmPasswordReset")

	var req PasswordResetConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	if err := utils.ValidatePasswordComplexity(req.NewPassword); err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	if err := h.authService.ConfirmPasswordReset(req.Token, req.NewPassword); err != nil {
		if errors.Is(err, service.ErrInvalidResetToken) {
			utils.RespondBadRequest(c, "Invalid or expired reset token")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Password reset confirmation failed")
		utils.RespondInternalError(c)
		return
	}

	response := MessageResponse{Message: "Password has been reset successfully"}
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}
