package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// RoleHandler serves the role API (#259 S4, #348): the permission catalogue,
// the roles and the role audit log. It decides nothing about roles itself:
// the route guards admit by permission and the role service applies the
// escalation rule, the system-role rule and the in-use rule inside its
// transaction.
type RoleHandler struct {
	roleService service.RoleService
}

func NewRoleHandler(roleService service.RoleService) *RoleHandler {
	return &RoleHandler{roleService: roleService}
}

// Error codes of the role API and of the role-governed user writes.
const (
	errCodeLastAdmin             = "LAST_ADMIN"
	errCodeRoleEscalation        = "ROLE_ESCALATION"
	errCodeCustomerLoginRole     = "CUSTOMER_LOGIN_ROLE"
	errCodeInvalidRoleAssignment = "INVALID_ROLE_ASSIGNMENT"
	errCodeInvalidPermission     = "INVALID_PERMISSION"
	errCodeSystemRoleImmutable   = "SYSTEM_ROLE_IMMUTABLE"
	errCodeRoleInUse             = "ROLE_IN_USE"
	errCodeRoleNameTaken         = "ROLE_NAME_TAKEN"
	errCodeRoleNotFound          = "ROLE_NOT_FOUND"
)

// RolePermissionEntry is one permission of a role.
//
// @Description One permission of a role: a catalogue key and the scope it is held at.
type RolePermissionEntry struct {
	// Permission key, group.action, for example leads.view.
	Key string `json:"key" example:"leads.view"`
	// Which records the permission reaches: all of them, or own, the records
	// the account owns or is assigned.
	Scope models.PermissionScope `json:"scope" enums:"all,own" example:"all"`
}

// RolePermissionRequest is one requested permission of a role.
//
// @Description One requested permission of a role.
type RolePermissionRequest struct {
	// Permission key from GET /permissions/catalogue; only selectable keys are accepted.
	Key string `json:"key" example:"leads.view"`
	// all or own; omitted or empty means all. own is accepted only on a key
	// whose catalogue node lists it under scopes.
	Scope string `json:"scope,omitempty" enums:"all,own" example:"own"`
}

// CreateRoleRequest is the body of POST /roles.
//
// @Description Body of POST /roles.
type CreateRoleRequest struct {
	// Display name, 1 to 100 characters after trimming, unique among all roles
	// whatever the letter case. It must not carry personal text.
	Name string `json:"name" binding:"required,max=100" maxLength:"100" example:"Sales lead"`
	// Free text, up to 500 characters.
	Description string `json:"description,omitempty" binding:"max=500" maxLength:"500" example:"Sales with the whole pipeline"`
	// The permissions of the role; omitted or empty creates a role without any.
	Permissions []RolePermissionRequest `json:"permissions"`
}

// UpdateRoleRequest is the body of PUT /roles/{id}.
//
// @Description Body of PUT /roles/{id}. name is always written; an omitted description or permissions field keeps the stored value.
type UpdateRoleRequest struct {
	// Display name, 1 to 100 characters after trimming, unique among all roles
	// whatever the letter case.
	Name string `json:"name" binding:"required,max=100" maxLength:"100" example:"Sales lead"`
	// Free text, up to 500 characters; omitted keeps the stored description.
	Description *string `json:"description,omitempty" binding:"omitempty,max=500" maxLength:"500" example:"Sales with the whole pipeline"`
	// When present, the array replaces the role's permissions; an empty array
	// leaves the role without any. Omitted keeps the stored permissions.
	Permissions *[]RolePermissionRequest `json:"permissions,omitempty"`
}

// CloneRoleRequest is the body of POST /roles/{id}/clone.
//
// @Description Body of POST /roles/{id}/clone.
type CloneRoleRequest struct {
	// Display name of the new role, 1 to 100 characters after trimming, unique
	// among all roles whatever the letter case.
	Name string `json:"name" binding:"required,max=100" maxLength:"100" example:"Sales (copy)"`
	// Free text, up to 500 characters; the source's description is not copied.
	Description string `json:"description,omitempty" binding:"max=500" maxLength:"500" example:"Cloned from Sales"`
}

// RoleResponse is a role as the role API returns it.
//
// @Description A role: one of the four system roles or a custom role, with its permissions and the number of users on it.
type RoleResponse struct {
	// Role id.
	ID uint `json:"id" example:"7"`
	// Display name.
	Name string `json:"name" example:"Sales lead"`
	// Free text.
	Description string `json:"description" example:"Sales with the whole pipeline"`
	// admin, sales, support or customer on a system role; null on a custom role.
	SystemKey *string `json:"system_key" example:"sales"`
	// True for the four system roles, which cannot be edited or deleted, only cloned.
	IsSystem bool `json:"is_system" example:"false"`
	// Number of live users on the role, deactivated ones included.
	UserCount int64 `json:"user_count" example:"3"`
	// The role's permissions, sorted by key. A system role's come from its
	// fixed matrix.
	Permissions []RolePermissionEntry `json:"permissions"`
	// Creation time (RFC 3339).
	CreatedAt time.Time `json:"created_at" example:"2026-11-18T09:00:00Z"`
	// Time of the last change (RFC 3339).
	UpdatedAt time.Time `json:"updated_at" example:"2026-11-18T09:00:00Z"`
}

// PermissionCatalogueNode is one node of the permission catalogue.
//
// @Description One node of the permission catalogue: a group with children, or a permission a role can hold.
type PermissionCatalogueNode struct {
	// Group key (leads) or permission key (leads.view).
	Key string `json:"key" example:"leads.view"`
	// Display title.
	Title string `json:"title" example:"View leads"`
	// Scopes a role may hold the permission at: all and own for a permission
	// that can be limited to own records, empty for a permission that is
	// always all and for a group.
	Scopes []string `json:"scopes" example:"all,own"`
	// True for a permission a custom role can hold; false for a group and for
	// a permission only a system role holds.
	Selectable bool `json:"selectable" example:"true"`
	// The permissions of a group, in display order; empty for a permission.
	Children []PermissionCatalogueNode `json:"children"`
}

// RoleAuditEntryResponse is one row of the role audit log.
//
// @Description One row of the role audit log. It holds ids, role names, permission keys, scopes and the is_active flag only; an email-change row stores neither address.
type RoleAuditEntryResponse struct {
	// Row id.
	ID uint `json:"id" example:"42"`
	// UTC time of the change (RFC 3339).
	CreatedAt time.Time `json:"created_at" example:"2026-11-18T09:00:00Z"`
	// Id of the acting user; null for a SCIM write or a data step, which no
	// user performs.
	ActorUserID *uint `json:"actor_user_id" example:"1"`
	// How the acting request authenticated: bearer or api_key, oidc for the
	// role an OpenID Connect sign-in applied to its own account, scim for a
	// SCIM 2.0 user or group write (actor null), or migration for a
	// startup or migration data step (actor null).
	AuthMethod string `json:"auth_method" enums:"bearer,api_key,oidc,scim,migration" example:"bearer"`
	// Kind of change.
	Action models.RoleAuditAction `json:"action" enums:"role_created,role_updated,role_deleted,user_role_changed,user_activated,user_deactivated,user_erased,user_email_changed" example:"role_updated"`
	// The affected role, or the custom role a user received; null when a user
	// received a system role, and on an account row (user_activated,
	// user_deactivated, user_erased, user_email_changed).
	RoleID *uint `json:"role_id" example:"7"`
	// Name of that role at the time of the change; the system role's key when
	// a user received a system role; empty on an account row.
	RoleName string `json:"role_name" example:"Sales lead"`
	// The user whose role changed, or the account an account row records;
	// null on a role change.
	TargetUserID *uint `json:"target_user_id" example:"12"`
	// State before the change: name, description and permissions of a role,
	// or role and role_id of a user, or is_active of an activation or
	// deactivation row. null on a create, an erasure and an email change.
	BeforeState json.RawMessage `json:"before_state"`
	// State after the change, in the same shape. null on a role delete, an
	// erasure and an email change.
	AfterState json.RawMessage `json:"after_state"`
}

func roleResponse(detail *service.RoleDetail) RoleResponse {
	response := RoleResponse{
		ID:          detail.ID,
		Name:        detail.Name,
		Description: detail.Description,
		SystemKey:   detail.SystemKey,
		IsSystem:    detail.IsSystem,
		UserCount:   detail.UserCount,
		Permissions: make([]RolePermissionEntry, 0, len(detail.Permissions)),
		CreatedAt:   detail.CreatedAt,
		UpdatedAt:   detail.UpdatedAt,
	}
	for _, grant := range detail.Permissions {
		response.Permissions = append(response.Permissions, RolePermissionEntry{Key: grant.Key, Scope: grant.Scope})
	}
	return response
}

func catalogueNode(node permissions.Node) PermissionCatalogueNode {
	out := PermissionCatalogueNode{Key: node.Key, Title: node.Title, Scopes: []string{}, Children: []PermissionCatalogueNode{}}
	if node.IsLeaf() {
		out.Selectable = node.CustomRoleMayHold()
		if node.AllowsOwn {
			out.Scopes = []string{string(models.PermissionScopeAll), string(models.PermissionScopeOwn)}
		}
		return out
	}
	for _, child := range node.Children {
		out.Children = append(out.Children, catalogueNode(child))
	}
	return out
}

func auditState(state *string) json.RawMessage {
	if state == nil || !json.Valid([]byte(*state)) {
		return json.RawMessage("null")
	}
	return json.RawMessage(*state)
}

func permissionInputs(requested []RolePermissionRequest) []service.RolePermissionInput {
	inputs := make([]service.RolePermissionInput, 0, len(requested))
	for _, permission := range requested {
		inputs = append(inputs, service.RolePermissionInput{Key: permission.Key, Scope: permission.Scope})
	}
	return inputs
}

// requestActor is the caller of a governed write: its id and how the request
// authenticated, which the audit row records.
func requestActor(c *gin.Context) service.Actor {
	return service.Actor{UserID: c.GetUint("user_id"), AuthMethod: c.GetString(service.AuthMethodContextKey)}
}

// respondRoleGovernanceError answers the refusals the role service and the
// governed user writes share and reports whether it did. Anything else is the
// caller's to answer.
func respondRoleGovernanceError(c *gin.Context, err error) bool {
	var invalid *apperrors.InvalidPermissionError
	var inUse *apperrors.RoleInUseError
	switch {
	case errors.Is(err, apperrors.ErrRoleNotFound):
		utils.RespondError(c, http.StatusNotFound, errCodeRoleNotFound, "Role not found", nil)
	case errors.Is(err, apperrors.ErrForbidden):
		utils.RespondForbidden(c, "Insufficient permissions")
	case errors.Is(err, apperrors.ErrSystemRoleImmutable):
		utils.RespondError(c, http.StatusConflict, errCodeSystemRoleImmutable, "A system role cannot be edited or deleted; clone it instead", nil)
	case errors.Is(err, apperrors.ErrRoleEscalation):
		utils.RespondError(c, http.StatusForbidden, errCodeRoleEscalation, "The change reaches permissions above your own", nil)
	case errors.As(err, &invalid):
		utils.RespondError(c, http.StatusBadRequest, errCodeInvalidPermission, "The permission list is not valid", gin.H{"keys": invalid.Keys})
	case errors.Is(err, apperrors.ErrRoleNameTaken):
		utils.RespondError(c, http.StatusConflict, errCodeRoleNameTaken, "A role with this name already exists", nil)
	case errors.As(err, &inUse):
		utils.RespondError(c, http.StatusConflict, errCodeRoleInUse, "The role is still assigned to users", gin.H{"user_count": inUse.UserCount})
	case errors.Is(err, apperrors.ErrRoleInUse):
		utils.RespondError(c, http.StatusConflict, errCodeRoleInUse, "The role is still assigned to users", nil)
	case errors.Is(err, apperrors.ErrInvalidRoleAssignment):
		utils.RespondError(c, http.StatusBadRequest, errCodeInvalidRoleAssignment, "role custom requires role_id, and role_id is accepted with role custom only", nil)
	case errors.Is(err, apperrors.ErrCustomerLoginRole):
		utils.RespondError(c, http.StatusConflict, errCodeCustomerLoginRole, "A customer login cannot receive a custom role", nil)
	case errors.Is(err, apperrors.ErrLastAdmin):
		utils.RespondError(c, http.StatusConflict, errCodeLastAdmin, "The last active administrator cannot be demoted, deactivated or erased", nil)
	default:
		return false
	}
	return true
}

// respondRoleWriteError answers a failed role write.
func (h *RoleHandler) respondRoleWriteError(c *gin.Context, logger *logrus.Entry, err error, action string) {
	logger = logger.WithField("action", action)
	if respondRoleGovernanceError(c, err) {
		logger.WithError(utils.LogError(err)).Warn("Role write refused")
		return
	}
	if errors.Is(err, apperrors.ErrValidation) {
		logger.WithError(utils.LogError(err)).Warn("Role write refused")
		utils.RespondValidationError(c, gin.H{"Name": "must be 1 to 100 characters after trimming; the description at most 500"})
		return
	}
	logger.WithError(utils.LogError(err)).Error("Role write failed")
	utils.RespondInternalError(c)
}

func roleIDParam(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		utils.RespondBadRequest(c, "Invalid role ID")
		return 0, false
	}
	return uint(id), true
}

// Catalogue godoc
// @Summary Get the permission catalogue
// @Description The tree of permissions a role can hold, in display order: groups, each with its permissions. A permission lists the scopes it can be held at (all and own, or none when it is always all) and whether a custom role can hold it (selectable is false for a group and for a permission only a system role holds). The catalogue is fixed in code. Needs roles.view or users.assign_role (the admin system role holds both).
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Success 200 {object} utils.APIResponse{data=[]PermissionCatalogueNode} "The catalogue groups"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller holds neither roles.view nor users.assign_role (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Router /permissions/catalogue [get]
func (h *RoleHandler) Catalogue(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.Catalogue")

	nodes := h.roleService.Catalogue()
	catalogue := make([]PermissionCatalogueNode, 0, len(nodes))
	for _, node := range nodes {
		catalogue = append(catalogue, catalogueNode(node))
	}

	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, catalogue)
}

// List godoc
// @Summary List roles
// @Description A page of roles, system and custom, as a bare array in data with the pagination in meta (page, per_page, total, total_pages; each omitted when 0). Each role carries its permissions (a system role's come from its fixed matrix) and user_count, the number of live users on it. Without sort_by the rows come in ascending id order; with an allowed sort_by they are ordered by that column, then by id, in the requested direction. Needs roles.view or users.assign_role.
// @Description Pagination: limit defaults to 20 and is capped at 100 (a missing, non-numeric or non-positive value gives 20); offset defaults to 0 (non-numeric or negative gives 0). An unknown sort_by is ignored rather than rejected.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Number of rows to skip" default(0)
// @Param limit query int false "Page size; capped at 100, non-positive or invalid values give 20" default(20)
// @Param sort_by query string false "Sort column; any other value is ignored (ascending id order)" Enums(id, name, created_at, updated_at)
// @Param sort_order query string false "Sort direction; any other value falls back to asc" Enums(asc, desc) default(asc)
// @Success 200 {object} utils.APIResponse{data=[]RoleResponse,meta=utils.APIMeta} "A page of roles; pagination in meta"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller holds neither roles.view nor users.assign_role (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the roles could not be read"
// @Router /roles [get]
func (h *RoleHandler) List(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.List")

	offset, limit := utils.ParseOffsetLimit(c)
	sortBy := c.Query("sort_by")
	sortOrder := c.DefaultQuery("sort_order", "asc")
	if !utils.AllowedSortColumns["roles"][sortBy] {
		sortBy = ""
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}

	details, total, err := h.roleService.List(offset, limit, sortBy, sortOrder)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list roles")
		utils.RespondInternalError(c)
		return
	}
	roles := make([]RoleResponse, 0, len(details))
	for i := range details {
		roles = append(roles, roleResponse(&details[i]))
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccessWithMeta(c, http.StatusOK, roles, meta)
}

// Get godoc
// @Summary Get a role
// @Description One role, system or custom, with its permissions and user_count. Needs roles.view or users.assign_role.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Role ID"
// @Success 200 {object} utils.APIResponse{data=RoleResponse} "The role"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid role ID (not an unsigned 32-bit integer)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller holds neither roles.view nor users.assign_role (Insufficient permissions)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "ROLE_NOT_FOUND: no role with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the role could not be read"
// @Router /roles/{id} [get]
func (h *RoleHandler) Get(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.Get")

	id, ok := roleIDParam(c)
	if !ok {
		return
	}
	detail, err := h.roleService.Get(id)
	if err != nil {
		if respondRoleGovernanceError(c, err) {
			return
		}
		logger.WithError(utils.LogError(err)).Error("Failed to read role")
		utils.RespondInternalError(c)
		return
	}

	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccess(c, http.StatusOK, roleResponse(detail))
}

// Create godoc
// @Summary Create a custom role
// @Description Creates a custom role with the given permissions. Needs roles.manage. One transaction: the caller's account is locked and its permissions are read again, the permission list is validated, the escalation rule is applied, and the role is written together with its role_created audit row. The escalation rule: a caller that is not on the admin system role cannot grant a permission it does not hold itself, or hold only at scope own while granting all. The name is trimmed and must be unique among all roles, system roles included, whatever the letter case.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body CreateRoleRequest true "The role"
// @Success 201 {object} utils.APIResponse{data=RoleResponse} "The created role; user_count is 0"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "VALIDATION_ERROR: name is missing, blank after trimming or over 100 characters, or description is over 500 (details maps the Go field name to a message). BAD_REQUEST: malformed JSON (Invalid request format). INVALID_PERMISSION: the list holds a key outside the catalogue, a key only a system role holds, a key twice, a scope other than all or own, or own on a key that does not allow it; details.keys lists the offending keys and nothing is written"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold roles.manage (Insufficient permissions), also when it lost the permission while the request waited. ROLE_ESCALATION: the list grants a permission above the caller's own"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "ROLE_NAME_TAKEN: another role already has this name"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the role could not be written; nothing was stored"
// @Router /roles [post]
func (h *RoleHandler) Create(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.Create")

	var req CreateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	detail, err := h.roleService.Create(c.Request.Context(), requestActor(c), service.RoleCreateInput{
		Name: req.Name, Description: req.Description, Permissions: permissionInputs(req.Permissions)})
	if err != nil {
		h.respondRoleWriteError(c, logger, err, "role_created")
		return
	}

	logger.WithField("role_id", detail.ID).Info("Role created")
	utils.RespondSuccess(c, http.StatusCreated, roleResponse(detail))
}

// Update godoc
// @Summary Update a custom role
// @Description Renames a custom role and, when the body carries permissions, replaces its permission set; the holders are affected from their next request. Needs roles.manage. One transaction: the caller's account, then the caller's role and this role (ascending id) are locked, the caller's permissions are read again, and the role is written together with its role_updated audit row holding the state before and after. The checks answer in this order: ROLE_NOT_FOUND, FORBIDDEN, SYSTEM_ROLE_IMMUTABLE, ROLE_ESCALATION for the role's current permissions, INVALID_PERMISSION, ROLE_ESCALATION for the requested permissions, then the name rules. The escalation rule covers both directions: a caller that is not on the admin system role can neither grant a permission above its own nor edit a role whose current permissions are above its own. A refusal writes nothing.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Role ID"
// @Param request body UpdateRoleRequest true "The new name and, optionally, description and permissions"
// @Success 200 {object} utils.APIResponse{data=RoleResponse} "The updated role"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid role ID, or malformed JSON (Invalid request format). VALIDATION_ERROR: name is missing, blank after trimming or over 100 characters, or description is over 500 (details maps the Go field name to a message). INVALID_PERMISSION: the list holds a key outside the catalogue, a key only a system role holds, a key twice, a scope other than all or own, or own on a key that does not allow it; details.keys lists the offending keys"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold roles.manage (Insufficient permissions), also when it lost the permission while the request waited. ROLE_ESCALATION: the role's current permissions, or the requested ones, are above the caller's own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "ROLE_NOT_FOUND: no role with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "SYSTEM_ROLE_IMMUTABLE: the role is one of the four system roles. ROLE_NAME_TAKEN: another role already has this name"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the role could not be written; nothing was changed"
// @Router /roles/{id} [put]
func (h *RoleHandler) Update(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.Update")

	id, ok := roleIDParam(c)
	if !ok {
		return
	}
	var req UpdateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	input := service.RoleUpdateInput{Name: req.Name, Description: req.Description}
	if req.Permissions != nil {
		inputs := permissionInputs(*req.Permissions)
		input.Permissions = &inputs
	}
	detail, err := h.roleService.Update(c.Request.Context(), requestActor(c), id, input)
	if err != nil {
		h.respondRoleWriteError(c, logger, err, "role_updated")
		return
	}

	logger.WithField("role_id", detail.ID).Info("Role updated")
	utils.RespondSuccess(c, http.StatusOK, roleResponse(detail))
}

// Delete godoc
// @Summary Delete a custom role
// @Description Deletes a custom role that no live user holds; there is no automatic reassignment. Needs roles.manage. One transaction: the caller's account, the users holding the role and the role itself are locked, the holders are read again, erased accounts that still referenced the role are detached, the role and its permissions are deleted and the role_deleted audit row is written. The checks answer in this order: ROLE_NOT_FOUND, FORBIDDEN, SYSTEM_ROLE_IMMUTABLE, ROLE_ESCALATION, ROLE_IN_USE. A caller that is not on the admin system role cannot delete a role whose permissions are above its own, and is told so before the holders are counted. A deactivated holder blocks the delete like an active one; an erased one does not. A refusal writes nothing.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Role ID"
// @Success 204 "Deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid role ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold roles.manage (Insufficient permissions), also when it lost the permission while the request waited. ROLE_ESCALATION: the role's permissions are above the caller's own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "ROLE_NOT_FOUND: no role with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "SYSTEM_ROLE_IMMUTABLE: the role is one of the four system roles. ROLE_IN_USE: a live user still holds the role, also when the assignment committed while the delete ran; details.user_count is the number of holders"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the role could not be deleted; nothing was changed"
// @Router /roles/{id} [delete]
func (h *RoleHandler) Delete(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.Delete")

	id, ok := roleIDParam(c)
	if !ok {
		return
	}
	if err := h.roleService.Delete(c.Request.Context(), requestActor(c), id); err != nil {
		h.respondRoleWriteError(c, logger, err, "role_deleted")
		return
	}

	logger.WithField("role_id", id).Info("Role deleted")
	c.Status(http.StatusNoContent)
}

// Clone godoc
// @Summary Clone a role
// @Description Creates a custom role holding the permissions of the source: the stored set of a custom role, or the fixed matrix of a system role, in both cases without the permissions only a system role can hold. This is how a system role is adapted, since it cannot be edited. Needs roles.manage. The escalation check runs inside the transaction after the caller's account and the source role are locked: a caller that is not on the admin system role cannot clone a role whose permissions are above its own. Audited as role_created.
// @Tags users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "ID of the role to copy"
// @Param request body CloneRoleRequest true "Name and description of the new role"
// @Success 201 {object} utils.APIResponse{data=RoleResponse} "The new custom role; user_count is 0"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST: Invalid role ID, or malformed JSON (Invalid request format). VALIDATION_ERROR: name is missing, blank after trimming or over 100 characters, or description is over 500 (details maps the Go field name to a message)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold roles.manage (Insufficient permissions), also when it lost the permission while the request waited. ROLE_ESCALATION: the source's permissions are above the caller's own"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "ROLE_NOT_FOUND: no role with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "ROLE_NAME_TAKEN: another role already has this name"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the role could not be written; nothing was stored"
// @Router /roles/{id}/clone [post]
func (h *RoleHandler) Clone(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.Clone")

	id, ok := roleIDParam(c)
	if !ok {
		return
	}
	var req CloneRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}

	detail, err := h.roleService.Clone(c.Request.Context(), requestActor(c), id, req.Name, req.Description)
	if err != nil {
		h.respondRoleWriteError(c, logger, err, "role_created")
		return
	}

	logger.WithField("role_id", detail.ID).Info("Role cloned")
	utils.RespondSuccess(c, http.StatusCreated, roleResponse(detail))
}

// ListAudit godoc
// @Summary List the role audit log
// @Description A page of the append-only role audit log as a bare array in data, with the pagination in meta (page, per_page, total, total_pages; each omitted when 0). A row is written, in the transaction of the change it records, for every role create, clone (role_created), update and delete, for every change of a user's role or role_id (user_role_changed, also for the role a new user is created with), and for every account change made by the user routes or SCIM: an activation (user_activated) or deactivation (user_deactivated, before and after holding is_active only), an erasure (user_erased) and a change of the login email (user_email_changed, storing neither address). An account row has role_id null and an empty role_name. Rows hold ids, role names, permission keys, scopes and the is_active flag only. Without sort_by the newest row comes first (created_at descending, then id descending); with an allowed sort_by the rows are ordered by that column, then by id, in the requested direction. Needs roles.audit_view.
// @Description Pagination: limit defaults to 20 and is capped at 100 (a missing, non-numeric or non-positive value gives 20); offset defaults to 0 (non-numeric or negative gives 0). An unknown sort_by is ignored rather than rejected. A role_id or target_user_id that is not an unsigned integer is ignored too.
// @Tags users
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param offset query int false "Number of rows to skip" default(0)
// @Param limit query int false "Page size; capped at 100, non-positive or invalid values give 20" default(20)
// @Param sort_by query string false "Sort column; any other value is ignored (newest first)" Enums(id, created_at, action)
// @Param sort_order query string false "Sort direction; any other value falls back to desc" Enums(asc, desc) default(desc)
// @Param role_id query int false "Only the rows of this role"
// @Param target_user_id query int false "Only the rows of this user"
// @Success 200 {object} utils.APIResponse{data=[]RoleAuditEntryResponse,meta=utils.APIMeta} "A page of audit rows; pagination in meta"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED: missing or malformed Authorization header, or an invalid, expired or revoked credential, or a deactivated or erased account"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN: the caller does not hold roles.audit_view (Insufficient permissions)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS: authenticated tier exceeded (120 per minute, burst 30, per client IP); details.retry_after is 60s and no Retry-After header is sent"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR: the audit log could not be read"
// @Router /roles/audit [get]
func (h *RoleHandler) ListAudit(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "RoleHandler.ListAudit")

	offset, limit := utils.ParseOffsetLimit(c)
	sortBy := c.Query("sort_by")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	if !utils.AllowedSortColumns["role_audit_log"][sortBy] {
		sortBy = ""
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "desc"
	}
	filter := repository.RoleAuditFilter{}
	if id, err := strconv.ParseUint(c.Query("role_id"), 10, 32); err == nil {
		roleID := uint(id)
		filter.RoleID = &roleID
	}
	if id, err := strconv.ParseUint(c.Query("target_user_id"), 10, 32); err == nil {
		targetUserID := uint(id)
		filter.TargetUserID = &targetUserID
	}

	entries, total, err := h.roleService.ListAudit(filter, offset, limit, sortBy, sortOrder)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list the role audit log")
		utils.RespondInternalError(c)
		return
	}
	rows := make([]RoleAuditEntryResponse, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, RoleAuditEntryResponse{
			ID:           entry.ID,
			CreatedAt:    entry.CreatedAt,
			ActorUserID:  entry.ActorUserID,
			AuthMethod:   entry.AuthMethod,
			Action:       entry.Action,
			RoleID:       entry.RoleID,
			RoleName:     entry.RoleName,
			TargetUserID: entry.TargetUserID,
			BeforeState:  auditState(entry.BeforeState),
			AfterState:   auditState(entry.AfterState),
		})
	}

	meta := &utils.APIMeta{
		RequestID:  c.GetString("request_id"),
		Page:       (offset / limit) + 1,
		PerPage:    limit,
		Total:      total,
		TotalPages: (total + int64(limit) - 1) / int64(limit),
	}
	utils.LogHandlerResponse(logger, http.StatusOK, nil)
	utils.RespondSuccessWithMeta(c, http.StatusOK, rows, meta)
}
