package handler

import (
	"net/http"
	"strconv"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	userService service.UserService
}

func NewUserHandler(userService service.UserService) *UserHandler {
	return &UserHandler{userService: userService}
}

type CreateUserRequest struct {
	Email     string           `json:"email" binding:"required,email"`
	Password  string           `json:"password" binding:"required,min=8"`
	FirstName string           `json:"first_name" binding:"required"`
	LastName  string           `json:"last_name" binding:"required"`
	Role      models.UserRole  `json:"role" binding:"required,oneof=admin sales support customer"`
}

type UpdateUserRequest struct {
	Email     string           `json:"email,omitempty" binding:"omitempty,email"`
	FirstName string           `json:"first_name,omitempty"`
	LastName  string           `json:"last_name,omitempty"`
	Role      models.UserRole  `json:"role,omitempty" binding:"omitempty,oneof=admin sales support customer"`
	IsActive  *bool            `json:"is_active,omitempty"`
}

type UpdateMeRequest struct {
	Email     string `json:"email,omitempty" binding:"omitempty,email"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Password  string `json:"password,omitempty" binding:"omitempty,min=8"`
}

func (h *UserHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Create")
	
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	user := &models.User{
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Role:      req.Role,
		IsActive:  true,
	}

	if err := h.userService.Register(user, req.Password); err != nil {
		logger.WithError(err).Warn("Failed to create user")
		if err.Error() == "user with this email already exists" {
			utils.RespondConflict(c, err.Error())
		} else {
			utils.RespondBadRequest(c, err.Error())
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, user)
	utils.RespondSuccess(c, http.StatusCreated, user)
}

func (h *UserHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.List")
	
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	
	if limit > 100 {
		limit = 100
	}

	users, total, err := h.userService.List(offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list users")
		utils.RespondInternalError(c)
		return
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}

	utils.LogHandlerResponse(logger, http.StatusOK, gin.H{"users": users, "total": total})
	utils.RespondSuccessWithMeta(c, http.StatusOK, users, meta)
}

func (h *UserHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Get")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid user ID")
		return
	}

	// Check permissions - users can only view themselves unless admin
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	if uint(id) != currentUserID && currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "You can only view your own profile")
		return
	}

	user, err := h.userService.GetByID(uint(id))
	if err != nil {
		logger.WithError(err).Warn("User not found")
		utils.RespondNotFound(c, "User not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

func (h *UserHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Update")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid user ID")
		return
	}

	// Check permissions - users can only update themselves unless admin
	currentUserID := c.GetUint("user_id")
	currentUserRole := c.GetString("user_role")
	
	if uint(id) != currentUserID && currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "You can only update your own profile")
		return
	}

	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Build updates map
	updates := make(map[string]interface{})
	if req.Email != "" {
		updates["email"] = req.Email
	}
	if req.FirstName != "" {
		updates["first_name"] = req.FirstName
	}
	if req.LastName != "" {
		updates["last_name"] = req.LastName
	}
	
	// Only admins can update role and active status
	if currentUserRole == string(models.RoleAdmin) {
		if req.Role != "" {
			updates["role"] = req.Role
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}
	}

	user, err := h.userService.Update(uint(id), updates)
	if err != nil {
		logger.WithError(err).Error("Failed to update user")
		if err.Error() == "user with this email already exists" {
			utils.RespondConflict(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

func (h *UserHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Delete")
	
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid user ID")
		return
	}

	// Only admins can delete users
	currentUserRole := c.GetString("user_role")
	if currentUserRole != string(models.RoleAdmin) {
		utils.RespondForbidden(c, "Only administrators can delete users")
		return
	}

	// Prevent self-deletion
	currentUserID := c.GetUint("user_id")
	if uint(id) == currentUserID {
		utils.RespondBadRequest(c, "You cannot delete your own account")
		return
	}

	if err := h.userService.Delete(uint(id)); err != nil {
		logger.WithError(err).Error("Failed to delete user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

func (h *UserHandler) GetMe(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.GetMe")
	
	userID := c.GetUint("user_id")
	
	user, err := h.userService.GetByID(userID)
	if err != nil {
		logger.WithError(err).Error("Failed to get current user")
		utils.RespondNotFound(c, "User not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

func (h *UserHandler) UpdateMe(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.UpdateMe")
	
	userID := c.GetUint("user_id")
	
	var req UpdateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// Build updates map
	updates := make(map[string]interface{})
	if req.Email != "" {
		updates["email"] = req.Email
	}
	if req.FirstName != "" {
		updates["first_name"] = req.FirstName
	}
	if req.LastName != "" {
		updates["last_name"] = req.LastName
	}
	if req.Password != "" {
		updates["password"] = req.Password
	}

	user, err := h.userService.Update(userID, updates)
	if err != nil {
		logger.WithError(err).Error("Failed to update user")
		if err.Error() == "user with this email already exists" {
			utils.RespondConflict(c, err.Error())
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}