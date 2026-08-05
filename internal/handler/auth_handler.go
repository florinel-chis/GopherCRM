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
	Password  string `json:"password" binding:"required,min=8"`
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