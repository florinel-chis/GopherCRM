package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
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

// @Description Body of POST /users. Unlike public registration, the requested role is honoured.
type CreateUserRequest struct {
	// Login email address; must be valid and not used by any account (409).
	Email string `json:"email" binding:"required,email" format:"email" example:"sam.sales@example.com"`
	// Initial password: at least 10 characters with an uppercase letter, a
	// lowercase letter, a digit and a special character. At most 72 bytes (the
	// bcrypt limit); a longer password is answered with 400.
	Password string `json:"password" binding:"required,min=10" minLength:"10" example:"Str0ng!Passw0rd"`
	// Given name; required, non-empty.
	FirstName string `json:"first_name" binding:"required" example:"Sam"`
	// Family name; required, non-empty.
	LastName string `json:"last_name" binding:"required" example:"Seller"`
	// Role of the new account; required, one of admin, sales, support,
	// customer, custom. Any role other than customer needs users.assign_role.
	Role models.UserRole `json:"role" binding:"required,oneof=admin sales support customer custom" example:"sales"`
	// Id of the custom role; required with role custom and refused with any
	// other role (400 INVALID_ROLE_ASSIGNMENT).
	RoleID *uint `json:"role_id,omitempty" example:"7"`
}

// @Description Body of PUT /users/{id}. Every field is optional; an omitted or empty field leaves the stored value unchanged (names and email cannot be cleared).
type UpdateUserRequest struct {
	// New login email; must be valid and not used by another account (409).
	Email string `json:"email,omitempty" binding:"omitempty,email" format:"email" example:"sam.sales@example.com"`
	// New given name; empty leaves it unchanged.
	FirstName string `json:"first_name,omitempty" example:"Sam"`
	// New family name; empty leaves it unchanged.
	LastName string `json:"last_name,omitempty" example:"Seller"`
	// New role. Applied only when the caller holds users.assign_role and
	// silently ignored otherwise; a value outside admin, sales, support,
	// customer, custom is a 400 for every caller.
	Role models.UserRole `json:"role,omitempty" binding:"omitempty,oneof=admin sales support customer custom" example:"support"`
	// Id of the custom role; required with role custom and refused with any
	// other role or without role (400 INVALID_ROLE_ASSIGNMENT). Ignored, like
	// role, for a caller without users.assign_role.
	RoleID *uint `json:"role_id,omitempty" example:"7"`
	// New active flag. Applied only when the caller holds users.activate and
	// silently ignored otherwise. false deactivates the account (its tokens and
	// API keys stop working); deactivating the last active admin is refused
	// with 409 LAST_ADMIN.
	IsActive *bool `json:"is_active,omitempty" example:"true"`
	// The caller's own current password. Needed only when the caller changes
	// the email of its own account (400 CURRENT_PASSWORD_REQUIRED without it,
	// 400 INVALID_CURRENT_PASSWORD when it does not match); ignored and not
	// verified in every other case. It never changes a password.
	CurrentPassword string `json:"current_password,omitempty" example:"Curr3nt!Passw0rd"`
}

// MePermission is one effective permission of the calling account.
//
// @Description One effective permission of the calling account.
type MePermission struct {
	// Permission key, group.action, for example leads.view.
	Key string `json:"key" example:"leads.view"`
	// Which records the permission reaches: all of them, or own, the records
	// the account owns or is assigned.
	Scope models.PermissionScope `json:"scope" enums:"all,own" example:"own"`
}

// MeResponse is the body of GET /users/me: the account with its role name
// and effective permissions.
//
// @Description The calling account (the fields of a user) plus the name of its role and its effective permissions.
type MeResponse struct {
	models.User
	// Name of the account's role: admin, sales, support or customer for a
	// system role, the role's own name for a custom role.
	RoleName string `json:"role_name" example:"sales"`
	// Effective permissions of the account, sorted by key. Resolved on every
	// request, so a role change shows on the next call.
	Permissions []MePermission `json:"permissions"`
}

// UpdateMeRequest carries the profile fields of PUT /users/me.
//
// password and new_password are captured raw only so the handler can refuse
// them: a json.RawMessage receives the literal bytes of any JSON value, null
// included, so a non-nil field means the key was present, and encoding/json
// matches the key in any letter case. omitempty keeps a nil field out of a
// marshalled body. The spec documents them as any JSON value
// (scripts/docs/swag-overrides maps json.RawMessage to interface{}).
// current_password is a plain string: it is the proof an email change needs
// (#394), and null or an empty string count as absent.
//
// @Description Body of PUT /users/me. Every profile field is optional; an omitted or empty field leaves the stored value unchanged. Role and active status cannot be changed here. Changing the email needs current_password. A body that carries password or new_password is refused.
type UpdateMeRequest struct {
	// New login email; must be valid and not used by another account (409).
	// An email that differs from the stored one needs current_password.
	Email string `json:"email,omitempty" binding:"omitempty,email" format:"email" example:"jane.doe@example.com"`
	// New given name; empty leaves it unchanged.
	FirstName string `json:"first_name,omitempty" example:"Jane"`
	// New family name; empty leaves it unchanged.
	LastName string `json:"last_name,omitempty" example:"Doe"`
	// Refused: this key, with any value (null included) and in any letter case, is
	// answered with 400 PASSWORD_FIELD_NOT_ACCEPTED. Use POST /auth/change-password.
	Password json.RawMessage `json:"password,omitempty"`
	// The account's current password. Needed only when email differs from the
	// stored one (400 CURRENT_PASSWORD_REQUIRED when absent, null or empty, 400
	// INVALID_CURRENT_PASSWORD when it does not match); ignored and not verified
	// in every other case. It never changes a password.
	CurrentPassword string `json:"current_password,omitempty" example:"Curr3nt!Passw0rd"`
	// Refused: this key, with any value (null included) and in any letter case, is
	// answered with 400 PASSWORD_FIELD_NOT_ACCEPTED. Use POST /auth/change-password.
	NewPassword json.RawMessage `json:"new_password,omitempty"`
}

// Create godoc
// @Summary Create a user
// @Description Needs users.create (route guard; the admin system role holds it). Creates an active account with the requested role, unlike the public POST /auth/register, which always creates a customer. role custom needs role_id, the id of a custom role, and any other role forbids it. A caller without users.assign_role can only create a customer: any other role is 403 FORBIDDEN, answered before role_id is looked at. Password policy: at least 10 characters with an uppercase letter, a lowercase letter, a digit and a special character, and at most 72 bytes. No mail is sent; the new user signs in with the password the caller chose.
// @Description The account is written in one transaction that locks the caller's account and the custom role, reads the caller's permissions again and applies the escalation rule: a caller that is not on the admin system role cannot give a role whose permissions are above its own (a missing permission, or all where it holds own). The customer role never counts as above the caller. The role the account is created with is recorded in the role audit log (user_role_changed) in the same transaction.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateUserRequest true "New account"
// @Success 201 {object} utils.APIResponse{data=models.User} "User created"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: a required field is missing, email is invalid, password is shorter than 10 characters or role is none of admin, sales, support, customer, custom (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format), the password is longer than 72 bytes (password must be at most 72 bytes long; checked first), or it fails the complexity policy. INVALID_ROLE_ASSIGNMENT: role custom without role_id, role_id with any other role, or a role_id that names a system role"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold users.create, or asks for a role other than customer without users.assign_role (Insufficient permissions), also when it lost the permission while the request waited. ROLE_ESCALATION: the requested role's permissions are above the caller's own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "ROLE_NOT_FOUND: role_id names no role"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: an account already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the user could not be stored (the cause is logged, never returned); nothing was written"
// @Router /users [post]
func (h *UserHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.Create")

	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	if err := utils.ValidateNewPassword(req.Password); err != nil {
		utils.RespondBadRequest(c, err.Error())
		return
	}

	// Only a customer can be created without users.assign_role. The refusal
	// comes before role_id is looked at, so such a caller learns nothing
	// about the roles.
	if service.RoleNeedsAssignPermission(req.Role) && !hasPermission(c, permissions.UsersAssignRole) {
		utils.RespondForbidden(c, "Insufficient permissions")
		return
	}
	if err := service.CheckRoleAssignment(req.Role, req.RoleID); err != nil {
		respondRoleGovernanceError(c, err)
		return
	}

	user := &models.User{
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Role:      req.Role,
		RoleID:    req.RoleID,
		IsActive:  true,
	}

	if err := h.userService.CreateAs(c.Request.Context(), requestActor(c), user, req.Password); err != nil {
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			logger.WithError(utils.LogError(err)).Warn("Failed to create user")
			utils.RespondConflict(c, "user with this email already exists")
			return
		}
		if respondRoleGovernanceError(c, err) {
			logger.WithError(utils.LogError(err)).Warn("Failed to create user")
			return
		}
		// Anything else is a failure of the store, not of the request: the
		// detail goes to the log and the client gets the generic answer.
		logger.WithError(utils.LogError(err)).Error("Failed to create user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusCreated, user)
	utils.RespondSuccess(c, http.StatusCreated, user)
}

// List godoc
// @Summary List users (admin)
// @Description Needs users.view (route guard; the admin system role holds it). Returns a page of live accounts as a bare array in data, with the pagination in meta (page, per_page, total, total_pages; each omitted when 0). Erased accounts are not listed. Without sort_by the rows come in ascending id order; with an allowed sort_by they are ordered by that column, then by id, in the requested direction. search matches a substring (SQL LIKE) of email, first_name or last_name and combines with the sort.
// @Description Pagination: limit defaults to 20 and is capped at 100 (a missing, non-numeric or non-positive value gives 20); offset defaults to 0 (non-numeric or negative gives 0); page, when a positive integer, overrides offset with (page - 1) * limit. An unknown sort_by is ignored rather than rejected.
// @Description role keeps only the users whose role is exactly that value; the filter applies in the query before offset and limit, so meta counts only matching users, and it combines with search and the sort. An empty role is ignored; any other value is rejected before anything is read.
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
// @Param role query string false "Only users with this role (exact, case-sensitive match); empty is ignored, an unknown value is 400" Enums(admin, sales, support, customer, custom)
// @Success 200 {object} utils.APIResponse{data=[]models.User,meta=utils.APIMeta} "A page of users; pagination in meta"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: role is none of admin, sales, support, customer, custom (error.details.role; the value is not echoed)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold users.view (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the users could not be read"
// @Router /users [get]
func (h *UserHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.List")

	// The role filter is validated before anything else is parsed or read.
	// An empty value is no filter; it is not trimmed, so a blank is invalid.
	role := c.Query("role")
	if role != "" && !models.UserRole(role).IsValid() {
		logger.Warn("Rejected user list: invalid role filter")
		utils.RespondValidationError(c, gin.H{"role": "must be one of admin, sales, support, customer, custom"})
		return
	}

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

	if role != "" {
		users, total, err = h.userService.ListFiltered(repository.UserListFilter{Search: search, Role: role}, offset, limit, sortBy, sortOrder)
	} else if search != "" {
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
// @Description Any authenticated account may read its own account without any permission; reading another account needs users.view, checked in the handler (the admin system role holds it). The check runs before the lookup, so a caller without users.view asking for another id gets 403 whether or not it exists. Erased accounts are not found.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 200 {object} utils.APIResponse{data=models.User} "The user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID (not an unsigned 32-bit integer; 0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: a caller without users.view asked for another account ('You can only view your own profile'); its own id never needs users.view"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the user could not be read"
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

	if uint(id) != currentUserID && !hasPermission(c, permissions.UsersView) {
		utils.RespondForbidden(c, "You can only view your own profile")
		return
	}

	user, err := h.userService.GetByID(uint(id))
	if err != nil {
		// Only a missing record is a 404; a failed read is the server's.
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("User not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, user)
}

// Update godoc
// @Summary Update a user
// @Description Any authenticated account may update its own profile fields; updating another account needs users.update (checked before the body is read; the admin system role holds it). Only non-empty fields are written. role and role_id are applied only for a caller holding users.assign_role and is_active only for one holding users.activate; each is silently ignored otherwise, an inconsistent role and role_id pair included. A body with nothing to apply returns the account unchanged. A password key in the body is ignored; passwords are changed through POST /auth/change-password, not here. A caller that changes the email of its own account (an email that differs from the stored one) must send its current password in current_password, for a Bearer token and an API key alike and also when the body carries role or is_active: without it the answer is 400 CURRENT_PASSWORD_REQUIRED, with a wrong one 400 INVALID_CURRENT_PASSWORD, and nothing is written; this is checked before the email is looked up, so the answer does not tell whether the address is in use. current_password is ignored and not verified when the email does not change or the account is another one, and a wrong value is not counted toward the login lockout. Changing role, role_id or is_active takes effect on the account's next request (tokens are re-checked against the stored account); a role change revokes no session. is_active false does what POST /users/{id}/deactivate does: the same transaction increments the account token_version, revokes every refresh token and revokes every API key of the account, and none of them works again after a reactivation.
// @Description Every update of another account, and every update that writes role, role_id or is_active (the caller's own account included), is one transaction: the active admins are locked, then the caller and the target in ascending id, then the custom roles of caller, target and the requested role in ascending id; all are read again and the checks answer in this order: FORBIDDEN (the caller lacks a permission the request needs), ROLE_ESCALATION, CUSTOMER_LOGIN_ROLE, LAST_ADMIN. Changing the email of a customer login that has a linked customer record (a customer record whose user_id is that login) also needs customers.update; its other fields, and a customer login without a linked customer record, need users.update alone. The escalation rule: a caller that is not on the admin system role can neither edit an account whose permissions are above its own (profile fields included) nor give a role whose permissions are above its own; the customer role never counts as above the caller. role custom needs role_id and any other role clears it. A changed role or role_id is recorded in the role audit log (user_role_changed) in the same transaction; so are a changed email (user_email_changed, storing neither address) and a changed is_active (user_deactivated or user_activated), written before the role row. A caller's own email change without role or is_active is not one of these transactions and writes no audit row. The last active admin can be neither demoted nor deactivated, by itself or by anyone else.
// @Description A changed email also ends every password reset link issued for the account before the change: its unused reset tokens are spent in the same write.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Param request body UpdateUserRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.User} "The updated user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID, or malformed JSON or a current_password that is not a string (Invalid request format). VALIDATION_ERROR: email is not an email address or role is none of admin, sales, support, customer, custom (details maps the Go field name to a message). CURRENT_PASSWORD_REQUIRED: the caller changes the email of its own account and current_password is absent, null or empty (no details). INVALID_CURRENT_PASSWORD: the caller changes the email of its own account and current_password does not match (no details). INVALID_ROLE_ASSIGNMENT (only for a caller holding users.assign_role): role custom without role_id, role_id with any other role or without role, or a role_id that names a system role"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: a caller without users.update tried to update another account (You can only update your own profile), or the caller lost a permission the request needs while it waited, or it lacks customers.update for an email change of a customer login with a linked customer record (Insufficient permissions). ROLE_ESCALATION: the target's permissions, or those of the requested role, are above the caller's own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id. ROLE_NOT_FOUND: role_id names no role"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: another account already uses this email. LAST_ADMIN: the change would demote or deactivate the last active admin. CUSTOMER_LOGIN_ROLE: a customer login cannot receive a custom role"
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

	// Check permissions - users can only update themselves without users.update
	currentUserID := c.GetUint("user_id")

	if uint(id) != currentUserID && !hasPermission(c, permissions.UsersUpdate) {
		utils.RespondForbidden(c, "You can only update your own profile")
		return
	}

	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// The caller's own email changes only with its current password, whichever
	// of the two writes below the body leads to.
	if uint(id) == currentUserID && h.refusedOwnEmailChange(c, "UserHandler.Update", currentUserID, req.Email, req.CurrentPassword) {
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

	// Role and active status each need their own permission (admins hold
	// both); without it the field is silently ignored, as it always was, and
	// so is an inconsistent role and role_id pair.
	governed := uint(id) != currentUserID
	if hasPermission(c, permissions.UsersAssignRole) && (req.Role != "" || req.RoleID != nil) {
		if req.Role == "" {
			respondRoleGovernanceError(c, apperrors.ErrInvalidRoleAssignment)
			return
		}
		if err := service.CheckRoleAssignment(req.Role, req.RoleID); err != nil {
			respondRoleGovernanceError(c, err)
			return
		}
		updates["role"] = req.Role
		updates["role_id"] = req.RoleID
		governed = true
	}
	if hasPermission(c, permissions.UsersActivate) {
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
			governed = true
		}
	}

	// Another account, and any write of role, role_id or is_active, goes
	// through the governed transaction; a profile-only update of the caller's
	// own account stays the plain update it was.
	var user *models.User
	if governed {
		user, err = h.userService.UpdateAs(c.Request.Context(), requestActor(c), uint(id), updates)
	} else {
		user, err = h.userService.Update(uint(id), updates)
	}
	if err != nil {
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			logger.WithError(utils.LogError(err)).Warn("Failed to update user")
			utils.RespondConflict(c, "user with this email already exists")
			return
		}
		if respondRoleGovernanceError(c, err) {
			logger.WithError(utils.LogError(err)).Warn("User update refused")
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
// @Description Needs users.delete (route guard, repeated in the handler; the admin system role holds it); a caller cannot erase its own account. The erasure runs in one transaction that first locks the active admins, the caller and the target (ascending id) and their custom roles, reads them again and answers in this order: FORBIDDEN (the caller lacks a permission the erasure needs), ROLE_ESCALATION (a caller that is not on the admin system role cannot erase an account whose permissions are above its own; a customer login never is), LAST_ADMIN (the last active admin cannot be erased). This is a GDPR Article 17 erasure and cannot be undone: in one transaction every personal field is overwritten in place (the email becomes a random address in the reserved .invalid domain, the names are blanked, the password hash is made unusable, is_active becomes false and the login bookkeeping is cleared), the user's API keys, refresh tokens, password-reset tokens and notifications are deleted, the bodies of the ticket comments the user wrote become [deleted], and the row is soft-deleted. The row is kept so references from tickets, tasks, leads and deals still resolve, and the original email becomes free for a new account.
// @Description Erasing a customer-role user also erases, in the same transaction, every customer record whose user_id is that login and every lead converted into those customers; erasing a customer login that has such a linked customer record therefore needs customers.delete next to users.delete (FORBIDDEN without it, and nothing is erased), while a customer login without one needs users.delete alone. Erasing a staff user leaves the records it owns, manages or is assigned to untouched. The erasure writes a user_erased row to the role audit log in the same transaction. To suspend an account reversibly use POST /users/{id}/deactivate instead.
// @Description When a customer-role user is erased, the deals and quotes of the customers and leads it reaches keep their links, stages, amounts, statuses, totals and history, but their staff-typed text is overwritten in the same transaction: deal title [redacted] with empty notes and lost_reason, quote title [redacted] with empty notes, each quote line's description set to its product's name; notifications about those deals are deleted. Deals and quotes owned by an erased staff user are not touched (owner_id is never followed).
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 204 "Erased; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID, or You cannot delete your own account"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold users.delete, also when it lost the permission while the request waited, or it lacks customers.delete for a customer login with a linked customer record. ROLE_ESCALATION: the target's permissions are above the caller's own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id (an already erased account included)"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "LAST_ADMIN: the target is the last active admin"
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

	// Deleting users needs users.delete
	if !hasPermission(c, permissions.UsersDelete) {
		utils.RespondForbidden(c, "Only administrators can delete users")
		return
	}

	// Prevent self-deletion
	currentUserID := c.GetUint("user_id")
	if uint(id) == currentUserID {
		utils.RespondBadRequest(c, "You cannot delete your own account")
		return
	}

	if err := h.userService.DeleteAs(c.Request.Context(), requestActor(c), uint(id)); err != nil {
		if respondRoleGovernanceError(c, err) {
			logger.WithError(utils.LogError(err)).Warn("User erasure refused")
			return
		}
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
// @Description Any authenticated account; no permission is needed. Returns the account the credential belongs to, the name of its role (role_name: admin, sales, support or customer for a system role, the role's own name for a custom role) and its effective permissions (permissions: key and scope, sorted by key). The permissions are resolved on every request from the account's current role, for a Bearer token and an API key alike, so a role change shows on the next call. Every user field keeps its name and value.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=MeResponse} "The caller's account with role_name and permissions"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: the account was erased after authentication"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the account could not be read"
// @Router /users/me [get]
func (h *UserHandler) GetMe(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.GetMe")

	userID := c.GetUint("user_id")

	user, err := h.userService.GetByID(userID)
	if err != nil {
		// The account can be erased between authentication and this read.
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Current user not found")
			utils.RespondNotFound(c, "User not found")
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to get current user")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, user)
	utils.RespondSuccess(c, http.StatusOK, meResponse(user))
}

// UpdateMe godoc
// @Summary Update the caller's account
// @Description Any authenticated role. Changes the caller's email and/or names; only non-empty fields are written, and role and active status cannot be changed here. Changing the email (an email that differs from the stored one) needs the account's current password in current_password, for a Bearer token and an API key alike: without it the answer is 400 CURRENT_PASSWORD_REQUIRED, with a wrong one 400 INVALID_CURRENT_PASSWORD, and nothing is written; this is checked before the email is looked up, so the answer does not tell whether the address is in use. current_password is ignored and not verified when the email does not change, and a wrong value is not counted toward the login lockout. This route does not change a password; a body that carries password or new_password, with any value (null included) and in any letter case, is refused with 400 PASSWORD_FIELD_NOT_ACCEPTED, nothing is written, and a correct current_password next to it does not open the route. Use POST /auth/change-password, which verifies the current password and revokes refresh tokens. A body with nothing to apply returns the account unchanged.
// @Description A changed email also ends every password reset link issued for the account before the change: its unused reset tokens are spent in the same write.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body UpdateMeRequest true "Fields to change"
// @Success 200 {object} utils.APIResponse{data=models.User} "The updated account"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "PASSWORD_FIELD_NOT_ACCEPTED: the body carries password or new_password, with any value (no details; use POST /auth/change-password); answered first. CURRENT_PASSWORD_REQUIRED: email differs from the stored one and current_password is absent, null or empty (no details). INVALID_CURRENT_PASSWORD: email differs from the stored one and current_password does not match (no details). VALIDATION_ERROR: email is not an email address (details maps the Go field name to a message). BAD_REQUEST: malformed JSON or a current_password that is not a string (Invalid request format). A body that fails binding keeps these answers even when it also carries a password field"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: the account was erased while the credential was still in use"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "CONFLICT: another account already uses this email"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the account could not be updated"
// @Router /users/me [put]
func (h *UserHandler) UpdateMe(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "UserHandler.UpdateMe")

	userID := c.GetUint("user_id")

	var req UpdateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	// POST /auth/change-password is the only authenticated way to change a
	// password: it verifies the current one and revokes the refresh tokens. Only
	// the fact of the refusal is logged, never the supplied value.
	if req.Password != nil || req.NewPassword != nil {
		logger.Warn("Refused PUT /users/me: body carries a password field")
		utils.RespondError(c, http.StatusBadRequest, "PASSWORD_FIELD_NOT_ACCEPTED",
			"This route does not accept password fields; use POST /auth/change-password", nil)
		return
	}

	if h.refusedOwnEmailChange(c, "UserHandler.UpdateMe", userID, req.Email, req.CurrentPassword) {
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

// refusedOwnEmailChange runs the guard of #394 for an account that writes its
// own profile and reports whether it answered the request. A body without an
// email never reaches the service, so its current_password is not looked at.
// The login email is where a password reset goes, so it moves only with the
// account's current password. The log line names the refusal alone: neither
// the supplied password nor an email.
func (h *UserHandler) refusedOwnEmailChange(c *gin.Context, handlerName string, userID uint, email, currentPassword string) bool {
	if email == "" {
		return false
	}
	logger := utils.GetLogger(c).WithField("handler", handlerName)
	err := h.userService.CheckOwnEmailChange(userID, email, currentPassword)
	switch {
	case err == nil:
		return false
	case errors.Is(err, apperrors.ErrCurrentPasswordRequired):
		logger.Warn("Refused own email change: no current password supplied")
		utils.RespondError(c, http.StatusBadRequest, "CURRENT_PASSWORD_REQUIRED",
			"Changing your email requires your current password", nil)
	case errors.Is(err, apperrors.ErrCurrentPasswordIncorrect):
		logger.Warn("Refused own email change: the current password does not match")
		utils.RespondError(c, http.StatusBadRequest, "INVALID_CURRENT_PASSWORD",
			"The current password is incorrect", nil)
	case errors.Is(err, apperrors.ErrNotFound):
		logger.WithError(utils.LogError(err)).Warn("User not found")
		utils.RespondNotFound(c, "User not found")
	default:
		logger.WithError(utils.LogError(err)).Error("Failed to check the email change")
		utils.RespondInternalError(c)
	}
	return true
}

// Activate godoc
// @Summary Activate a user
// @Description Needs users.activate (route guard; the admin system role holds it). Sets is_active to true, so the account can log in again. No access token, refresh token or API key from before the deactivation works again: the deactivation ended them, so the account signs in afresh and its owner issues new API keys. Idempotent: an already active account is returned unchanged. Activating an inactive account writes a user_activated row to the role audit log in the same transaction. One transaction with the locks of PUT /users/{id}; the escalation rule applies: a caller that is not on the admin system role cannot activate an account whose permissions are above its own (a customer login never is).
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 200 {object} utils.APIResponse{data=models.User} "The activated user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold users.activate (Insufficient permissions), also when it lost the permission while the request waited. ROLE_ESCALATION: the target's permissions are above the caller's own"
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

	user, err := h.userService.SetActiveAs(c.Request.Context(), requestActor(c), uint(id), true)
	if err != nil {
		if respondRoleGovernanceError(c, err) {
			logger.WithError(utils.LogError(err)).Warn("User activation refused")
			return
		}
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
// @Summary Deactivate a user
// @Description Needs users.activate (route guard; the admin system role holds it); a caller cannot deactivate its own account here. One transaction with the locks and the order of checks of PUT /users/{id}: FORBIDDEN (the caller lost the permission), ROLE_ESCALATION (a caller that is not on the admin system role cannot deactivate an account whose permissions are above its own; a customer login never is), LAST_ADMIN (the last active admin cannot be deactivated). Sets is_active to false, the reversible alternative to erasure: from the next request on the account cannot log in, its bearer tokens and API keys are rejected, and a refresh of its tokens fails. The same transaction increments the account token_version, revokes every refresh token and revokes every API key of the account, so none of them works again after a later POST /users/{id}/activate. No business data is changed. Deactivating an active account writes a user_deactivated row to the role audit log in the same transaction. A repeat on an inactive account answers the same, increments token_version again and writes no audit row.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "User ID"
// @Success 200 {object} utils.APIResponse{data=models.User} "The deactivated user"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid user ID, or You cannot deactivate your own account"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold users.activate (Insufficient permissions), also when it lost the permission while the request waited. ROLE_ESCALATION: the target's permissions are above the caller's own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND: no live user with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "LAST_ADMIN: the target is the last active admin"
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

	user, err := h.userService.SetActiveAs(c.Request.Context(), requestActor(c), uint(id), false)
	if err != nil {
		if respondRoleGovernanceError(c, err) {
			logger.WithError(utils.LogError(err)).Warn("User deactivation refused")
			return
		}
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
