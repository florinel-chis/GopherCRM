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

type LoginRequest struct {
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required"`
	RememberMe bool   `json:"remember_me"`
}

type AuthResponse struct {
	Token string       `json:"token"`
	User  *models.User `json:"user"`
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
// @Description Exchange email and password for a JWT bearer token and the authenticated user. After 5 consecutive failed attempts the account is locked for 15 minutes; while locked, and for a deactivated account, the response is the same 401 with the same generic message as a wrong password, so no account state is disclosed. The remember_me field is accepted but does not currently alter the token's lifetime.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Login request"
// @Success 200 {object} utils.APIResponse{data=AuthResponse} "Authenticated; JWT and user returned"
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

	token, err := h.authService.Login(req.Email, req.Password)
	if err != nil {
		logger.WithError(err).Warn("Login failed")
		utils.RespondUnauthorized(c, "Invalid email or password")
		return
	}

	user, err := h.userService.GetByEmail(req.Email)
	if err != nil {
		logger.WithError(err).Error("Failed to get user after successful login")
		utils.RespondInternalError(c)
		return
	}

	response := AuthResponse{
		Token: token,
		User:  user,
	}
	
	utils.LogHandlerResponse(logger, http.StatusOK, response)
	utils.RespondSuccess(c, http.StatusOK, response)
}