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

// @Description Body of POST /users (admin). Unlike public registration, the requested role is honoured.
type CreateUserRequest struct {
	// Login email address; must be valid and not used by any account (409).
	Email string `json:"email" binding:"required,email" format:"email" example:"sam.sales@example.com"`
	// Initial password: at least 10 characters with an uppercase letter, a
	// lowercase letter, a digit and a special character. More than 72 bytes is
	// answered with 400 (bcrypt limit).
	Password string `json:"password" binding:"required,min=10" minLength:"10" example:"Str0ng!Passw0rd"`
	// Given name; required, non-empty.
	FirstName string `json:"first_name" binding:"required" example:"Sam"`
	// Family name; required, non-empty.
	LastName string `json:"last_name" binding:"required" example:"Seller"`
	// Role of the new account; required, one of admin, sales, support, customer.
	Role models.UserRole `json:"role" binding:"required,oneof=admin sales support customer" example:"sales"`
}

// @Description Body of PUT /users/{id}. Every field is optional; an omitted or empty field leaves the stored value unchanged (names and email cannot be cleared).
type UpdateUserRequest struct {
	// New login email; must be valid and not used by another account (409).
	Email string `json:"email,omitempty" binding:"omitempty,email" format:"email" example:"sam.sales@example.com"`
	// New given name; empty leaves it unchanged.
	FirstName string `json:"first_name,omitempty" example:"Sam"`
	// New family name; empty leaves it unchanged.
	LastName string `json:"last_name,omitempty" example:"Seller"`
	// New role. Applied only when the caller is an admin and silently ignored
	// otherwise; a value outside admin, sales, support, customer is a 400 for
	// every caller.
	Role models.UserRole `json:"role,omitempty" binding:"omitempty,oneof=admin sales support customer" example:"support"`
	// New active flag. Applied only when the caller is an admin and silently
	// ignored otherwise. false deactivates the account (its tokens and API keys
	// stop working); unlike POST /users/{id}/deactivate this path does not stop
	// an admin from deactivating itself.
	IsActive *bool `json:"is_active,omitempty" example:"true"`
}

// @Description Body of PUT /users/me. Every field is optional; an omitted or empty field leaves the stored value unchanged. Role and active status cannot be changed here.
type UpdateMeRequest struct {
	// New login email; must be valid and not used by another account (409).
	Email string `json:"email,omitempty" binding:"omitempty,email" format:"email" example:"jane.doe@example.com"`
	// New given name; empty leaves it unchanged.
	FirstName string `json:"first_name,omitempty" example:"Jane"`
	// New family name; empty leaves it unchanged.
	LastName string `json:"last_name,omitempty" example:"Doe"`
	// New password: at least 10 characters with an uppercase letter, a
	// lowercase letter, a digit and a special character. The current password
	// is not asked for and existing refresh tokens are NOT revoked (POST
	// /auth/change-password does both). More than 72 bytes fails with 500.
	Password string `json:"password,omitempty" binding:"omitempty,min=10" minLength:"10" example:"N3w!Passw0rd2026"`
}

// Create godoc
// @Summary Create a user (admin)
// @Description Admin only (route guard). Creates an active account with the requested role, unlike the public POST /auth/register, which always creates a customer. Password policy: at least 10 characters with an uppercase letter, a lowercase letter, a digit and a special character. No mail is sent; the new user signs in with the password the admin chose.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateUserRequest true "New account"
// @Success 201 {object} utils.APIResponse{data=models.User} "User created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: a required field is missing, email is invalid, password is shorter than 10 characters or role is not one of admin, sales, support, customer (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format), the password fails the complexity policy, or the user could not be stored (the underlying message is returned, e.g. a password over 72 bytes)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: an account already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
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
		logger.WithError(utils.LogError(err)).Warn("Failed to create user")
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
// @Summary List users (admin)
// @Description Admin only (route guard). Returns a page of live accounts as a bare array in data, with the pagination in meta (page, per_page, total, total_pages; each omitted when 0). Erased accounts are not listed. Without sort_by the rows come in ascending id order; with an allowed sort_by they are ordered by that column, then by id, in the requested direction. search matches a substring (SQL LIKE) of email, first_name or last_name and combines with the sort.
// @Description Pagination: limit defaults to 20 and is capped at 100 (a missing, non-numeric or non-positive value gives 20); offset defaults to 0 (non-numeric or negative gives 0); page, when a positive integer, overrides offset with (page - 1) * limit. An unknown sort_by is ignored rather than rejected.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "1-based page number; a positive value overrides offset"
// @Param offset query int false "Number of rows to skip, ignored when page is given" default(0)
// @Param limit query int false "Page size; capped at 100, non-positive or invalid values give 20" default(20)
// @Param sort_by query string false "Sort column; any other value is ignored (ascending id order)" Enums(created_at, updated_at, email, first_name, last_name, role)
// @Param sort_order query string false "Sort direction; any other value falls back to asc" Enums(asc, desc) default(asc)
// @Param search query string false "Substring matched against email, first_name and last_name"
// @Success 200 {object} utils.APIResponse{data=[]models.User,meta=utils.APIMeta} "A page of users; pagination in meta"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the users could not be read"
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
		logger.WithError(utils.LogError(err)).Error("Failed to list users")
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
// @Summary Get a user
// @Description Any authenticated role may read its own account; reading another account requires the admin role. The permission check runs before the lookup, so a non-admin asking for another id gets 403 whether or not it exists. Erased accounts are not found.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 200 {object} utils.APIResponse{data=models.User} "The user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID (not an unsigned 32-bit integer; 0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: a non-admin asked for another account (You can only view your own profile)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id. Any lookup failure, a database error included, is answered this way"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
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
		logger.WithError(utils.LogError(err)).Warn("User not found")
		utils.RespondNotFound(c, "User not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

// Update godoc
// @Summary Update a user
// @Description Any authenticated role may update its own account; updating another account requires the admin role (checked before the body is read). Only non-empty fields are written. role and is_active are applied only for an admin caller and silently ignored for everyone else; unlike POST /users/{id}/deactivate, nothing here stops an admin from changing its own role or deactivating itself. A body with nothing to apply returns the account unchanged. Passwords are changed through PUT /users/me or POST /auth/change-password, not here. Changing role or is_active takes effect on the account's next request (tokens are re-checked against the stored account); no session is revoked.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Param request body UpdateUserRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.User} "The updated user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID, or malformed JSON (Invalid request format). VALIDATION_ERROR: email is not an email address or role is not one of admin, sales, support, customer (details maps the Go field name to a message)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: a non-admin tried to update another account (You can only update your own profile)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: another account already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the user could not be updated"
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
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			logger.WithError(utils.LogError(err)).Warn("Failed to update user")
			utils.RespondConflict(c, "user with this email already exists")
			return
		}
		// An update that matched nobody is a client error, not a server fault:
		// only a genuine failure stays a 500.
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.WithError(utils.LogError(err)).Warn("User not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to update user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

// Delete godoc
// @Summary Erase a user (irreversible)
// @Description Admin only (route guard, repeated in the handler); an admin cannot erase its own account. This is a GDPR Article 17 erasure and cannot be undone: in one transaction every personal field is overwritten in place (the email becomes a random address in the reserved .invalid domain, the names are blanked, the password hash is made unusable, is_active becomes false and the login bookkeeping is cleared), the user's API keys, refresh tokens, password-reset tokens and notifications are deleted, the bodies of the ticket comments the user wrote become [deleted], and the row is soft-deleted. The row is kept so references from tickets, tasks, leads and deals still resolve, and the original email becomes free for a new account.
// @Description Erasing a customer-role user also erases, in the same transaction, every customer record whose user_id is that login and every lead converted into those customers. Erasing a staff user leaves the records it owns, manages or is assigned to untouched. To suspend an account reversibly use POST /users/{id}/deactivate instead.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 204 "Erased; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID, or You cannot delete your own account"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id (an already erased account included)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the erasure failed; before the commit it was rolled back and nothing was erased, while a failed COMMIT is ambiguous (the erasure may have been applied)"
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
			logger.WithError(utils.LogError(err)).Warn("User not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to delete user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// GetMe godoc
// @Summary Get the caller's account
// @Description Any authenticated role. Returns the account the credential belongs to.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=models.User} "The caller's account"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: the account could not be read (erased after authentication, or a database error)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Router /users/me [get]
func (h *UserHandler) GetMe(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.GetMe")

	userID := c.GetUint("user_id")

	user, err := h.userService.GetByID(userID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to get current user")
		utils.RespondNotFound(c, "User not found")
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

// UpdateMe godoc
// @Summary Update the caller's account
// @Description Any authenticated role. Changes the caller's email, names and/or password; only non-empty fields are written, and role and active status cannot be changed here. A new password must meet the policy (at least 10 characters with an uppercase letter, a lowercase letter, a digit and a special character); unlike POST /auth/change-password the current password is not required and existing refresh tokens are not revoked. A body with nothing to apply returns the account unchanged.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body UpdateMeRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.User} "The updated account"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: email is not an email address or password is shorter than 10 characters (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format), or the password fails the complexity policy"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: the account was erased while the credential was still in use"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: another account already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the account could not be updated, or the password is over 72 bytes (bcrypt limit)"
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
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			logger.WithError(utils.LogError(err)).Warn("Failed to update user")
			utils.RespondConflict(c, "user with this email already exists")
			return
		}
		// The id comes from the token, so this means the account was deleted
		// while the session was still live. 404 remains the honest answer.
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.WithError(utils.LogError(err)).Warn("User not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to update user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

// Activate godoc
// @Summary Activate a user (admin)
// @Description Admin only (route guard). Sets is_active to true, so the account can log in again and its unexpired access tokens and active API keys work again. Idempotent: an already active account is returned unchanged.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 200 {object} utils.APIResponse{data=models.User} "The activated user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the user could not be updated"
// @Router /users/{id}/activate [post]
func (h *UserHandler) Activate(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Activate")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid user ID")
		return
	}

	user, err := h.userService.Update(uint(id), map[string]interface{}{"is_active": true})
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.WithError(utils.LogError(err)).Warn("User not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to activate user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

// Deactivate godoc
// @Summary Deactivate a user (admin)
// @Description Admin only (route guard); an admin cannot deactivate its own account here. Sets is_active to false, the reversible alternative to erasure: from the next request on the account cannot log in, its bearer tokens and API keys are rejected, and a refresh of its tokens fails. No data is changed and this call deletes no refresh token, but a refresh attempted while the account is deactivated revokes the token it presented. Idempotent.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 200 {object} utils.APIResponse{data=models.User} "The deactivated user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID, or You cannot deactivate your own account"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller is not an admin (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the user could not be updated"
// @Router /users/{id}/deactivate [post]
func (h *UserHandler) Deactivate(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Deactivate")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid user ID")
		return
	}

	// Prevent self-deactivation
	currentUserID := c.GetUint("user_id")
	if uint(id) == currentUserID {
		utils.RespondBadRequest(c, "You cannot deactivate your own account")
		return
	}

	user, err := h.userService.Update(uint(id), map[string]interface{}{"is_active": false})
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			logger.WithError(utils.LogError(err)).Warn("User not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to deactivate user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}
