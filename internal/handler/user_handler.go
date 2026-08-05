package handler

import (
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
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
	Password  string           `json:"password" binding:"required,min=10"`
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
	Password  string `json:"password,omitempty" binding:"omitempty,min=10"`
}

// Create godoc
// @Summary Create a new user
// @Description Create a new user with any role (admin role only). Unlike the public /auth/register endpoint, this one honours the requested role.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateUserRequest true "User creation request"
// @Success 201 {object} utils.APIResponse{data=models.User} "User created successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data, password does not meet complexity requirements, or user could not be created"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "User with this email already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /users [post]
func (h *UserHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Create")
	
	var req CreateUserRequest
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
		Role:      req.Role,
		IsActive:  true,
	}

	if err := h.userService.Register(user, req.Password); err != nil {
		logger.WithError(err).Warn("Failed to create user")
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			utils.RespondConflict(c, "user with this email already exists")
		} else {
			utils.RespondBadRequest(c, err.Error())
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, user)
	utils.RespondSuccess(c, http.StatusCreated, user)
}

// List godoc
// @Summary List users
// @Description List users with pagination, optional search and sorting (admin role only)
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset, ignored when page is supplied" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param sort_by query string false "Sort column; ignored unless one of the allowed values" Enums(created_at, updated_at, email, first_name, last_name, role)
// @Param sort_order query string false "Sort direction; anything else falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Free-text search across user fields; takes precedence over sort_by"
// @Success 200 {object} utils.APIResponse{data=[]models.User,meta=utils.APIMeta} "Users retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /users [get]
func (h *UserHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.List")

	// Support both page-based and offset-based pagination
	page, _ := strconv.Atoi(c.DefaultQuery("page", "0"))
	offset, limit := utils.ParseOffsetLimit(c)

	// Convert page to offset if page is provided
	if page > 0 {
		offset = (page - 1) * limit
	}

	// Parse and validate sort parameters
	sortBy := c.Query("sort_by")
	sortOrder := c.DefaultQuery("sort_order", "asc")

	allowedSortColumns := map[string]bool{
		"created_at": true,
		"updated_at": true,
		"email":      true,
		"first_name": true,
		"last_name":  true,
		"role":       true,
	}

	if !allowedSortColumns[sortBy] {
		sortBy = ""
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}

	search := c.Query("search")

	var users []models.User
	var total int64
	var err error

	if search != "" {
		users, total, err = h.userService.Search(search, offset, limit, sortBy, sortOrder)
	} else if sortBy != "" {
		users, total, err = h.userService.ListSorted(offset, limit, sortBy, sortOrder)
	} else {
		users, total, err = h.userService.List(offset, limit)
	}

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

// Get godoc
// @Summary Get a user by ID
// @Description Retrieve a single user. Any authenticated user may fetch their own record; fetching another user's record requires the admin role.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 200 {object} utils.APIResponse{data=models.User} "User retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid user ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - you can only view your own profile unless you are an admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "User not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /users/{id} [get]
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

// Update godoc
// @Summary Update a user
// @Description Update a user. Any authenticated user may update their own record; updating another user's record requires the admin role. The role and is_active fields are applied only for admins and silently ignored for everyone else.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Param request body UpdateUserRequest true "User update request"
// @Success 200 {object} utils.APIResponse{data=models.User} "User updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid user ID or request data"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - you can only update your own profile unless you are an admin"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "User with this email already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error, including when the user does not exist"
// @Router /users/{id} [put]
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
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			utils.RespondConflict(c, "user with this email already exists")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

// Delete godoc
// @Summary Delete a user (irreversible erasure of personal data)
// @Description Delete a user (admin role only). This is a GDPR Article 17 erasure and is IRREVERSIBLE: every personal field is overwritten in place — the email is replaced with a random address in the reserved .invalid domain, the names are blanked and the password hash is made unusable — before the row is soft-deleted, and the user's API keys and refresh tokens are purged so no credential outlives the account. The row itself is kept so foreign keys from tickets, tasks and leads still resolve. To suspend someone reversibly, set is_active to false via the update endpoint instead. Admins cannot delete their own account.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 204 "No Content"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid user ID or attempt to delete your own account"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - Admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "User not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /users/{id} [delete]
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
		// An erasure that matched nobody and an erasure that failed must not
		// look the same to the operator: only the not-found sentinel is a 404,
		// everything else is a genuine failure and stays a 500.
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.WithError(err).Warn("User not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(err).Error("Failed to delete user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// GetMe godoc
// @Summary Get the current user
// @Description Retrieve the profile of the currently authenticated user
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=models.User} "User retrieved successfully"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "User not found"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Router /users/me [get]
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

// UpdateMe godoc
// @Summary Update the current user
// @Description Update the profile of the currently authenticated user. Only email, name and password can be changed here — role and active status are not settable through this endpoint.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body UpdateMeRequest true "Profile update request"
// @Success 200 {object} utils.APIResponse{data=models.User} "User updated successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid request data or password does not meet complexity requirements"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "User with this email already exists"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /users/me [put]
func (h *UserHandler) UpdateMe(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.UpdateMe")
	
	userID := c.GetUint("user_id")
	
	var req UpdateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	if req.Password != "" {
		if err := utils.ValidatePasswordComplexity(req.Password); err != nil {
			utils.RespondBadRequest(c, err.Error())
			return
		}
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
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			utils.RespondConflict(c, "user with this email already exists")
		} else {
			utils.RespondInternalError(c)
		}
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}