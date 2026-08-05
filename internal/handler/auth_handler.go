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
type RegisterRequest struct {
	Email     string `json:"email" binding:"required,email"`
	Password  string `json:"password" binding:"required,min=10"`
	FirstName string `json:"first_name" binding:"required"`
	LastName  string `json:"last_name" binding:"required"`
}

// LoginRequest carries only the credentials the backend actually uses. Token
// lifetime is fixed by JWT_EXPIRY_HOURS, so there is no "remember me" input
// here; that choice is made client-side when deciding where to store the token.
// Any extra JSON keys a client sends are ignored by the binder.
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// AuthResponse is the shared shape of login and refresh responses. Register
// responses omit refresh_token (self-service registration issues only a JWT).
// The refresh token is an opaque single-use value: presenting it at
// POST /auth/refresh revokes it and issues a replacement.
type AuthResponse struct {
	Token        string       `json:"token"`
	RefreshToken string       `json:"refresh_token,omitempty"`
	User         *models.User `json:"user"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// LogoutRequest is optional: logout with no body (or an empty one) revokes
// every refresh token of the authenticated user.
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=10"`
}

type PasswordResetRequest struct {
	Email string `json:"email" binding:"required,email"`
}

type PasswordResetConfirmRequest struct {
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=10"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

// Register godoc
// @Summary Register a new account
// @Description Public self-service registration. The account is always created with the customer role — the request body carries no role field and no client input can influence it. Elevated roles are assignable only through the admin-guarded POST /users endpoint. On success a JWT for the new account is returned alongside the user. The password must be at least 10 characters and contain an uppercase letter, a lowercase letter, a digit and a special character.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Registration request"
// @Success 201 {object} utils.APIResponse{data=AuthResponse} "Account created; JWT and user returned"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Malformed body, failed field validation, or password complexity not met"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "A user with this email already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Rate limit exceeded (10 requests per minute per IP)"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Warn("Registration failed")
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			utils.RespondConflict(c, "user with this email already exists")
		} else {
			utils.RespondBadRequest(c, err.Error())
		}
		return
	}

	token, err := h.authService.GenerateJWT(user)
	if err != nil {
		logger.WithError(err).Error("Failed to generate token")
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
// @Summary Authenticate and obtain a JWT
// @Description Exchange email and password for a JWT bearer token, a refresh token and the authenticated user. After 5 consecutive failed attempts the account is locked for 15 minutes; while locked, and for a deactivated account, the response is the same 401 with the same generic message as a wrong password, so no account state is disclosed. The JWT's lifetime is fixed by server configuration; the refresh token is a single-use opaque value for POST /auth/refresh, stored server-side only as a hash.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Login request"
// @Success 200 {object} utils.APIResponse{data=AuthResponse} "Authenticated; JWT, refresh token and user returned"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Malformed body or failed field validation"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Invalid email or password, account locked, or account deactivated"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Rate limit exceeded (10 requests per minute per IP)"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Warn("Login failed")
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
// @Summary Exchange a refresh token for a new JWT
// @Description Rotates the session: the presented refresh token is revoked and a fresh JWT, refresh token and the user are returned — the same shape as login. Any invalid token (unknown, expired, revoked, or belonging to a deactivated or erased account) yields the same generic 401; the reason is never disclosed. Replaying a consumed token always fails, so clients must persist the replacement from every response.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body RefreshRequest true "Refresh request"
// @Success 200 {object} utils.APIResponse{data=AuthResponse} "Rotated; new JWT, new refresh token and user returned"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Malformed body or missing refresh_token"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Invalid or expired refresh token"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Rate limit exceeded (10 requests per minute per IP)"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		// Deliberately generic: every rejection reads the same. Internal
		// failures are logged server-side but still answered with 401 rather
		// than 500, so the error channel cannot be used to probe token state.
		logger.WithError(err).Warn("Refresh token rejected")
		utils.RespondUnauthorized(c, "Invalid or expired refresh token")
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
// @Summary Log out and revoke refresh tokens
// @Description Revokes refresh tokens of the authenticated user. With no body (or an empty one) every session of the user is revoked; with a refresh_token only that token is revoked, and only when it belongs to the caller. Logout is idempotent: unknown or already-revoked tokens still yield 200 and disclose nothing. The JWT itself stays valid until its expiry — clients must discard it.
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body LogoutRequest false "Optional: the specific refresh token to revoke"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Logged out"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Malformed JSON body"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Error("Logout failed")
		utils.RespondInternalError(c)
		return
	}

	response := MessageResponse{Message: "Logged out successfully"}
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// ChangePassword godoc
// @Summary Change the authenticated user's password
// @Description Re-authenticates with the current password, then replaces it. The new password must be at least 10 characters with an uppercase letter, a lowercase letter, a digit and a special character. On success every refresh token of the user is revoked, ending all other sessions. A wrong current password is a 400 validation failure of the request — the session presenting it is still authenticated, so it is not a 401.
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body ChangePasswordRequest true "Password change request"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Password changed; other sessions revoked"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Malformed body, wrong current password, or new password fails the complexity policy"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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

	if err := h.authService.ChangePassword(userID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, service.ErrInvalidCurrentPassword) {
			utils.RespondBadRequest(c, "The current password is incorrect")
			return
		}
		logger.WithError(err).Error("Password change failed")
		utils.RespondInternalError(c)
		return
	}

	response := MessageResponse{Message: "Password changed successfully"}
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// RequestPasswordReset godoc
// @Summary Request a password reset link
// @Description Public. Always answers the same 200 with the same body, whether or not an account exists for the email — the same anti-enumeration posture as login. When the account exists and is active, a single-use reset token valid for one hour is created and a link is emailed; storage or delivery problems are logged server-side and never change the response.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body PasswordResetRequest true "Password reset request"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Uniform acknowledgement, independent of account existence"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Malformed body or invalid email format"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Rate limit exceeded (10 requests per minute per IP)"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Error("Unexpected password reset failure")
	}

	response := MessageResponse{Message: "If an account exists for that email, a password reset link has been sent"}
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}

// ConfirmPasswordReset godoc
// @Summary Complete a password reset
// @Description Public. Spends a reset token from the emailed link and sets the new password, which must meet the complexity policy (min 10 chars, upper + lower + digit + special). Tokens are single-use and expire after one hour; an unknown, expired or already-used token yields the same generic 400 without disclosing which. On success every refresh token of the account is revoked.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body PasswordResetConfirmRequest true "Password reset confirmation"
// @Success 200 {object} utils.APIResponse{data=MessageResponse} "Password reset; all sessions revoked"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Malformed body, invalid or expired reset token, or new password fails the complexity policy"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Rate limit exceeded (10 requests per minute per IP)"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
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
		logger.WithError(err).Error("Password reset confirmation failed")
		utils.RespondInternalError(c)
		return
	}

	response := MessageResponse{Message: "Password has been reset successfully"}
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}
