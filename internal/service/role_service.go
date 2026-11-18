package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The role service (#259 S4, #348): the catalogue, custom roles and the audit
// listing. Every write is one transaction that locks the actor's users row,
// then the role rows it touches in ascending id, re-reads the actor's
// permissions and decides inside it; the audit row commits with the change.

// RoleDetail is a role as the API returns it: the stored row, its effective
// permissions (the code matrix for a system role) and the number of live
// users that hold it.
type RoleDetail struct {
	ID          uint
	Name        string
	Description string
	SystemKey   *string
	IsSystem    bool
	UserCount   int64
	Permissions []permissions.Grant
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RolePermissionInput is one requested permission of a role. An empty scope
// means all.
type RolePermissionInput struct {
	Key   string
	Scope string
}

// RoleCreateInput is the body of a role create.
type RoleCreateInput struct {
	Name        string
	Description string
	Permissions []RolePermissionInput
}

// RoleUpdateInput is the body of a role update. A nil Description keeps the
// stored one; a nil Permissions keeps the stored set, a non-nil one (empty
// included) replaces it.
type RoleUpdateInput struct {
	Name        string
	Description *string
	Permissions *[]RolePermissionInput
}

type roleService struct {
	governance *roleGovernance
}

// NewRoleService builds the role service over the repositories of the role
// tables, the user repository (which must implement
// repository.UserRoleMaintenance, as the production one does) and the
// transaction manager every write runs in.
func NewRoleService(roleRepo repository.RoleRepository, auditRepo repository.RoleAuditRepository,
	userRepo repository.UserRepository, txManager *utils.TransactionManager) RoleService {
	return &roleService{governance: &roleGovernance{users: userRepo, roles: roleRepo, audit: auditRepo, txManager: txManager}}
}

func (s *roleService) Catalogue() []permissions.Node {
	return permissions.Catalogue()
}

// effectiveRolePermissions is what a role grants: the code matrix of a system
// role, the stored rows of a custom one reduced to what the catalogue allows.
func effectiveRolePermissions(role *models.Role) permissions.Set {
	if role.SystemKey != nil {
		return permissions.ForSystemRole(*role.SystemKey)
	}
	return permissions.ForCustomRole(role.Permissions)
}

func roleDetail(role *models.Role, bySystemRole map[string]int64, byRoleID map[uint]int64) RoleDetail {
	detail := RoleDetail{
		ID:          role.ID,
		Name:        role.Name,
		Description: role.Description,
		SystemKey:   role.SystemKey,
		IsSystem:    role.SystemKey != nil,
		Permissions: effectiveRolePermissions(role).List(),
		CreatedAt:   role.CreatedAt,
		UpdatedAt:   role.UpdatedAt,
	}
	if role.SystemKey != nil {
		detail.UserCount = bySystemRole[*role.SystemKey]
	} else {
		detail.UserCount = byRoleID[role.ID]
	}
	return detail
}

func (s *roleService) holderCounts(users repository.UserRepository) (map[string]int64, map[uint]int64, error) {
	userRoles, ok := users.(repository.UserRoleMaintenance)
	if !ok {
		return nil, nil, errRoleGovernanceUnavailable
	}
	return userRoles.CountLiveRoleHolders()
}

func (s *roleService) List(offset, limit int, sortBy, sortOrder string) ([]RoleDetail, int64, error) {
	roles, total, err := s.governance.roles.List(offset, limit, sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}
	bySystemRole, byRoleID, err := s.holderCounts(s.governance.users)
	if err != nil {
		return nil, 0, err
	}
	details := make([]RoleDetail, 0, len(roles))
	for i := range roles {
		details = append(details, roleDetail(&roles[i], bySystemRole, byRoleID))
	}
	return details, total, nil
}

func (s *roleService) Get(id uint) (*RoleDetail, error) {
	role, err := s.governance.roles.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("role %d: %w", id, apperrors.ErrRoleNotFound)
		}
		return nil, err
	}
	bySystemRole, byRoleID, err := s.holderCounts(s.governance.users)
	if err != nil {
		return nil, err
	}
	detail := roleDetail(role, bySystemRole, byRoleID)
	return &detail, nil
}

// validateRolePermissions turns a requested list into rows. A key outside the
// catalogue, a system-only key, a key named twice, a scope other than all or
// own, and own on a leaf that does not allow it are refused together: the
// error carries every offending key, sorted.
func validateRolePermissions(inputs []RolePermissionInput) ([]models.RolePermission, error) {
	rows := make([]models.RolePermission, 0, len(inputs))
	seen := make(map[string]bool, len(inputs))
	offending := map[string]bool{}
	for _, input := range inputs {
		scope := models.PermissionScope(input.Scope)
		if scope == "" {
			scope = models.PermissionScopeAll
		}
		leaf, known := permissions.Leaf(input.Key)
		switch {
		case !known, leaf.SystemOnly, seen[input.Key]:
			offending[input.Key] = true
		case scope == models.PermissionScopeAll:
		case scope == models.PermissionScopeOwn && leaf.AllowsOwn:
		default:
			offending[input.Key] = true
		}
		seen[input.Key] = true
		rows = append(rows, models.RolePermission{PermissionKey: input.Key, Scope: scope})
	}
	if len(offending) > 0 {
		keys := make([]string, 0, len(offending))
		for key := range offending {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, &apperrors.InvalidPermissionError{Keys: keys}
	}
	return rows, nil
}

func grantRowsOf(set permissions.Set) []models.RolePermission {
	grants := set.List()
	rows := make([]models.RolePermission, 0, len(grants))
	for _, grant := range grants {
		rows = append(rows, models.RolePermission{PermissionKey: grant.Key, Scope: grant.Scope})
	}
	return rows
}

func stateOfRole(role *models.Role) roleAuditState {
	return roleAuditState{Name: role.Name, Description: role.Description, Permissions: effectiveRolePermissions(role).List()}
}

// lockActor locks the actor's users row (users before roles) and returns it.
func lockActor(tx *governedTx, actor Actor) (*models.User, error) {
	users, err := tx.lockUsers(actor.UserID)
	if err != nil {
		return nil, err
	}
	user := users[actor.UserID]
	if user == nil {
		return nil, fmt.Errorf("the acting account is gone: %w", apperrors.ErrForbidden)
	}
	return user, nil
}

// storeRole writes a new custom role with its permissions and its
// role_created audit row, and returns the stored role.
func storeRole(tx *governedTx, actor Actor, name, description string, rows []models.RolePermission) (*models.Role, error) {
	role := &models.Role{Name: name, Description: description}
	if err := tx.roles.Create(role); err != nil {
		return nil, err
	}
	if err := tx.roles.ReplacePermissions(role.ID, rows); err != nil {
		return nil, err
	}
	stored, err := tx.roles.GetByID(role.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.record(actor, models.RoleAuditRoleCreated, &stored.ID, stored.Name, nil, nil, stateOfRole(stored)); err != nil {
		return nil, err
	}
	return stored, nil
}

// Create writes a custom role. Refusals, in order: an actor that no longer
// holds roles.manage (ErrForbidden), an invalid permission list
// (ErrInvalidPermission), a list above the actor's own permissions
// (ErrRoleEscalation), then the repository's name rules (ErrValidation,
// ErrRoleNameTaken).
func (s *roleService) Create(ctx context.Context, actor Actor, input RoleCreateInput) (*RoleDetail, error) {
	var created *models.Role
	err := s.governance.run(ctx, func(tx *governedTx) error {
		actorUser, err := lockActor(tx, actor)
		if err != nil {
			return err
		}
		roles, err := tx.lockRoles(actorUser.RoleID)
		if err != nil {
			return err
		}
		acting, err := tx.actor(actorUser, roles)
		if err != nil {
			return err
		}
		if err := acting.requireKeys(permissions.RolesManage); err != nil {
			return err
		}
		rows, err := validateRolePermissions(input.Permissions)
		if err != nil {
			return err
		}
		if err := checkEscalation(acting, permissions.ForCustomRole(rows)); err != nil {
			return err
		}
		created, err = storeRole(tx, actor, input.Name, input.Description, rows)
		return err
	})
	if err != nil {
		return nil, err
	}
	detail := roleDetail(created, nil, nil)
	return &detail, nil
}

// Update renames a custom role and, when the input carries a list, replaces
// its permissions. Refusals, in order: a missing role (ErrRoleNotFound), an
// actor that no longer holds roles.manage (ErrForbidden), a system role
// (ErrSystemRoleImmutable), a role whose stored permissions are above the
// actor's own (ErrRoleEscalation), an invalid list (ErrInvalidPermission), a
// requested list above the actor's own (ErrRoleEscalation), then the name
// rules. A refusal writes nothing.
func (s *roleService) Update(ctx context.Context, actor Actor, id uint, input RoleUpdateInput) (*RoleDetail, error) {
	var updated *models.Role
	err := s.governance.run(ctx, func(tx *governedTx) error {
		actorUser, err := lockActor(tx, actor)
		if err != nil {
			return err
		}
		roles, err := tx.lockRoles(actorUser.RoleID, &id)
		if err != nil {
			return err
		}
		role := roles[id]
		if role == nil {
			return fmt.Errorf("role %d: %w", id, apperrors.ErrRoleNotFound)
		}
		acting, err := tx.actor(actorUser, roles)
		if err != nil {
			return err
		}
		if err := acting.requireKeys(permissions.RolesManage); err != nil {
			return err
		}
		if role.SystemKey != nil {
			return fmt.Errorf("role %d: %w", id, apperrors.ErrSystemRoleImmutable)
		}
		if err := checkEscalation(acting, effectiveRolePermissions(role)); err != nil {
			return err
		}
		var rows []models.RolePermission
		if input.Permissions != nil {
			if rows, err = validateRolePermissions(*input.Permissions); err != nil {
				return err
			}
			if err := checkEscalation(acting, permissions.ForCustomRole(rows)); err != nil {
				return err
			}
		}

		before := stateOfRole(role)
		change := &models.Role{ID: id, Name: input.Name, Description: role.Description}
		if input.Description != nil {
			change.Description = *input.Description
		}
		if err := tx.roles.Update(change); err != nil {
			return err
		}
		if input.Permissions != nil {
			if err := tx.roles.ReplacePermissions(id, rows); err != nil {
				return err
			}
		}
		if updated, err = tx.roles.GetByID(id); err != nil {
			return err
		}
		return tx.record(actor, models.RoleAuditRoleUpdated, &id, updated.Name, nil, before, stateOfRole(updated))
	})
	if err != nil {
		return nil, err
	}
	// The holders did not change; count them outside the write.
	bySystemRole, byRoleID, err := s.holderCounts(s.governance.users)
	if err != nil {
		return nil, err
	}
	detail := roleDetail(updated, bySystemRole, byRoleID)
	return &detail, nil
}

// Delete removes a custom role nobody holds. Locks: the actor's users row, a
// locking read of the live holders, the role rows, the holders again (an
// assignment that committed while the role lock was awaited is seen by the
// second read). Refusals, in order: a missing role (ErrRoleNotFound), an
// actor that no longer holds roles.manage (ErrForbidden), a system role
// (ErrSystemRoleImmutable), a role whose stored permissions are above the
// actor's own (ErrRoleEscalation, before the holder count so a manager below
// the role does not learn it), any live holder, deactivated included
// (RoleInUseError). Erased holders are detached first. A foreign-key refusal
// that still arrives is RoleInUseError too.
func (s *roleService) Delete(ctx context.Context, actor Actor, id uint) error {
	return s.governance.run(ctx, func(tx *governedTx) error {
		actorUser, err := lockActor(tx, actor)
		if err != nil {
			return err
		}
		if _, err := tx.userRoles.LockLiveRoleHolders(id); err != nil {
			return err
		}
		roles, err := tx.lockRoles(actorUser.RoleID, &id)
		if err != nil {
			return err
		}
		role := roles[id]
		if role == nil {
			return fmt.Errorf("role %d: %w", id, apperrors.ErrRoleNotFound)
		}
		acting, err := tx.actor(actorUser, roles)
		if err != nil {
			return err
		}
		if err := acting.requireKeys(permissions.RolesManage); err != nil {
			return err
		}
		if role.SystemKey != nil {
			return fmt.Errorf("role %d: %w", id, apperrors.ErrSystemRoleImmutable)
		}
		if err := checkEscalation(acting, effectiveRolePermissions(role)); err != nil {
			return err
		}
		holders, err := tx.userRoles.LockLiveRoleHolders(id)
		if err != nil {
			return err
		}
		if len(holders) > 0 {
			return &apperrors.RoleInUseError{UserCount: int64(len(holders))}
		}
		if _, err := tx.userRoles.ClearRoleIDOfErased(id); err != nil {
			return err
		}
		if err := tx.roles.Delete(id); err != nil {
			if errors.Is(err, apperrors.ErrRoleInUse) {
				// An assignment got in after the second holder read. The
				// count is read again; it is at least the one the foreign
				// key saw, even where this transaction's snapshot hides it.
				count := int64(1)
				if again, countErr := tx.userRoles.LockLiveRoleHolders(id); countErr == nil && int64(len(again)) > count {
					count = int64(len(again))
				}
				return &apperrors.RoleInUseError{UserCount: count}
			}
			if isNotFound(err) {
				return fmt.Errorf("role %d: %w", id, apperrors.ErrRoleNotFound)
			}
			return err
		}
		return tx.record(actor, models.RoleAuditRoleDeleted, &id, role.Name, nil, stateOfRole(role), nil)
	})
}

// Clone creates a custom role holding the source's effective permissions: the
// code matrix of a system role or the stored set of a custom one, in both
// cases without system-only keys. Refusals, in order: a missing source
// (ErrRoleNotFound), an actor that no longer holds roles.manage
// (ErrForbidden), a copied set above the actor's own (ErrRoleEscalation), then
// the name rules. Audited as role_created.
func (s *roleService) Clone(ctx context.Context, actor Actor, sourceID uint, name, description string) (*RoleDetail, error) {
	var created *models.Role
	err := s.governance.run(ctx, func(tx *governedTx) error {
		actorUser, err := lockActor(tx, actor)
		if err != nil {
			return err
		}
		roles, err := tx.lockRoles(actorUser.RoleID, &sourceID)
		if err != nil {
			return err
		}
		source := roles[sourceID]
		if source == nil {
			return fmt.Errorf("role %d: %w", sourceID, apperrors.ErrRoleNotFound)
		}
		acting, err := tx.actor(actorUser, roles)
		if err != nil {
			return err
		}
		if err := acting.requireKeys(permissions.RolesManage); err != nil {
			return err
		}
		// ForCustomRole drops the system-only keys of a system matrix.
		copied := permissions.ForCustomRole(grantRowsOf(effectiveRolePermissions(source)))
		if err := checkEscalation(acting, copied); err != nil {
			return err
		}
		created, err = storeRole(tx, actor, name, description, grantRowsOf(copied))
		return err
	})
	if err != nil {
		return nil, err
	}
	detail := roleDetail(created, nil, nil)
	return &detail, nil
}

func (s *roleService) ListAudit(filter repository.RoleAuditFilter, offset, limit int, sortBy, sortOrder string) ([]models.RoleAuditEntry, int64, error) {
	return s.governance.audit.List(filter, offset, limit, sortBy, sortOrder)
}
