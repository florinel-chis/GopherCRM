package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SCIM 2.0 group patch (#506). One governed transaction, in the lock order
// of the other SCIM writes:
//
//	admin set → users → roles → scim_group_members → role_audit_log
//
// The admin set is locked first by every group patch, so two group patches
// never interleave while an active admin exists. The group's membership rows
// are then read with a locking read (FOR UPDATE on MySQL/MariaDB), so the
// patch sees the rows a group patch committed while it waited for the admin
// set, not its REPEATABLE READ snapshot; the affected users' other
// memberships are read the same way after the users and roles locks.
//
// The operations apply in order to the member set. Every id an add or a
// replace names must be a managed account (live, not customer, linked under
// the effective single sign-on issuer, see repository.SCIMUserRepository; a
// deactivated one may be added), else the patch is refused with
// ErrSCIMInvalidValue like an unknown id. A remove of a non-member is a
// no-op, and a member row is removed even when its account is not managed;
// such an account is otherwise left alone: no role change, no users write, no
// audit row, and the last-admin rule does not count it as losing its role.
// Each affected managed account (added or removed) gets the role of the
// sign-in's first-match rule on its groups after the change (the groups claim
// only, never roles); no match keeps its role. A mapping to a missing role
// row or to customer refuses the patch with ErrSCIMInvalidValue. The
// last-admin rule judges the result: the patch is refused with
// ErrSCIMGroupLastAdmin only when an active admin exists before it and none
// after it, so a handover to another active user passes. A role change
// writes its user_role_changed row (auth_method scim, NULL actor); after the
// commit one successful auth_audit_log row with method scim names each
// managed account whose membership changed. A refusal writes nothing; a patch
// that changes nothing writes nothing.

// ErrSCIMGroupLastAdmin refuses a group patch whose result leaves no active
// admin (400 mutability). It deliberately does not wrap ErrSCIMInvalidValue,
// unlike ErrSCIMLastAdmin of the Users writes.
var ErrSCIMGroupLastAdmin = errors.New("scim: the last active admin must stay an active admin")

// errSCIMGroupWritesNotWired is a wiring fault (500): NewSCIMGroupService was
// built without WithSCIMGroupWrites.
var errSCIMGroupWritesNotWired = errors.New("scim group writes need WithSCIMGroupWrites")

// SCIMGroupPatch is a parsed SCIM PatchOp on a Group (#506).
type SCIMGroupPatch = scim.GroupPatch

// scimGroupWrites is what the patch needs besides the read half.
type scimGroupWrites struct {
	governance *roleGovernance
	audit      AuthAuditService
}

// WithSCIMGroupWrites enables Patch over the governed user writes and the
// sign-in audit log.
func WithSCIMGroupWrites(userRepo repository.UserRepository, roleRepo repository.RoleRepository, roleAuditRepo repository.RoleAuditRepository,
	txManager *utils.TransactionManager, audit AuthAuditService) SCIMGroupServiceOption {
	return func(s *scimGroupService) {
		s.writes = &scimGroupWrites{
			governance: &roleGovernance{users: userRepo, roles: roleRepo, audit: roleAuditRepo, txManager: txManager},
			audit:      audit,
		}
	}
}

// parseSCIMMemberID parses a member value: a positive decimal integer
// fitting a uint, or invalidValue.
func parseSCIMMemberID(raw string) (uint, error) {
	parsed, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil || parsed == 0 {
		return 0, scimInvalid("a member value is not a user id")
	}
	return uint(parsed), nil
}

// lockingMembers binds the membership repository to a locking read of the
// transaction. The fresh Session keeps the clause from leaking into the next
// statement; SQLite drops the clause.
func lockingMembers(repo repository.SCIMGroupMemberRepository, handle *gorm.DB) repository.SCIMGroupMemberRepository {
	return repo.WithTx(handle.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{}))
}

// sortedIDs returns the keys of set, ascending.
func sortedIDs(set map[uint]bool) []uint {
	ids := make([]uint, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// scimGroupRoleChange is the role an affected user gets.
type scimGroupRoleChange struct {
	user   *models.User
	role   models.UserRole
	roleID *uint
	custom *models.Role
}

// Patch applies a Group PatchOp to the group rawID names.
func (s *scimGroupService) Patch(ctx context.Context, rawID string, p SCIMGroupPatch) (*SCIMGroupRecord, error) {
	if s.writes == nil {
		return nil, errSCIMGroupWritesNotWired
	}
	cfg, err := s.sso.SSOConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	group, err := resolveGroup(cfg, rawID)
	if err != nil {
		return nil, err
	}
	issuer := cfg.IssuerURL

	var changed []uint
	err = s.writes.governance.run(ctx, func(tx *governedTx) error {
		changed = nil
		admins, err := tx.userRoles.LockActiveAdmins()
		if err != nil {
			return err
		}
		members := lockingMembers(s.members, tx.handle)
		currentIDs, err := members.ListMemberIDs(group)
		if err != nil {
			return err
		}
		current := make(map[uint]bool, len(currentIDs))
		for _, id := range currentIDs {
			current[id] = true
		}

		// Apply the operations to an in-memory set.
		final := make(map[uint]bool, len(current))
		for id := range current {
			final[id] = true
		}
		named := map[uint]bool{}
		for _, op := range p.Ops {
			ids := make([]uint, 0, len(op.Values))
			for _, raw := range op.Values {
				id, err := parseSCIMMemberID(raw)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			switch op.Op {
			case scim.GroupPatchAdd:
				for _, id := range ids {
					final[id], named[id] = true, true
				}
			case scim.GroupPatchReplace:
				final = make(map[uint]bool, len(ids))
				for _, id := range ids {
					final[id], named[id] = true, true
				}
			case scim.GroupPatchRemove:
				if op.All {
					final = map[uint]bool{}
				}
				for _, id := range ids {
					delete(final, id)
				}
			default:
				return scimInvalid("unsupported operation")
			}
		}

		added, removed := map[uint]bool{}, map[uint]bool{}
		for id := range final {
			if !current[id] {
				added[id] = true
			}
		}
		for id := range current {
			if !final[id] {
				removed[id] = true
			}
		}
		affected := map[uint]bool{}
		lockSet := map[uint]bool{}
		for id := range added {
			affected[id], lockSet[id] = true, true
		}
		for id := range removed {
			affected[id], lockSet[id] = true, true
		}
		for id := range named {
			lockSet[id] = true
		}
		if len(lockSet) == 0 {
			return nil
		}
		users, err := tx.lockUsers(sortedIDs(lockSet)...)
		if err != nil {
			return err
		}
		// A plain read of the identity links of the locked ids: the
		// application never deletes a user_identities row.
		linked, err := s.members.WithTx(tx.handle).ManagedUserIDs(issuer, sortedIDs(lockSet))
		if err != nil {
			return err
		}
		managed := func(id uint) *models.User {
			if u := users[id]; u != nil && u.Role != models.RoleCustomer && linked[id] {
				return u
			}
			return nil
		}
		for id := range named {
			if managed(id) == nil {
				return scimInvalid("a member is not a managed account")
			}
		}
		if len(affected) == 0 {
			return nil
		}

		// The groups of each affected managed account after the change. A
		// row of an account that is not managed is only removed: its role,
		// its users row and its audit rows are left alone.
		affectedManaged := []uint{}
		for _, id := range sortedIDs(affected) {
			if managed(id) != nil {
				affectedManaged = append(affectedManaged, id)
			}
		}
		mappedIDs := map[uint]uint{}
		if len(affectedManaged) > 0 {
			groupsOf, err := members.GroupsOfUsers(affectedManaged)
			if err != nil {
				return err
			}
			for _, id := range affectedManaged {
				groups := make([]string, 0, len(groupsOf[id])+1)
				for _, value := range groupsOf[id] {
					if value != group {
						groups = append(groups, value)
					}
				}
				if final[id] {
					groups = append(groups, group)
				}
				if roleID, matched := oidcMatchRole(cfg.RoleMapping, groups, nil); matched {
					mappedIDs[id] = roleID
				}
			}
		}
		var roleIDs []*uint
		for _, id := range affectedManaged {
			roleIDs = append(roleIDs, users[id].RoleID)
			if mapped, ok := mappedIDs[id]; ok {
				mapped := mapped
				roleIDs = append(roleIDs, &mapped)
			}
		}
		roles, err := tx.lockRoles(roleIDs...)
		if err != nil {
			return err
		}
		results := map[uint]scimGroupRoleChange{}
		for _, id := range affectedManaged {
			u := users[id]
			result := scimGroupRoleChange{user: u, role: u.Role, roleID: u.RoleID}
			if mapped, ok := mappedIDs[id]; ok {
				if result.role, result.roleID, result.custom, err = scimMappedRole(roles[mapped]); err != nil {
					return err
				}
			}
			results[id] = result
		}

		// The last-admin rule, judged on the result. The admin set before
		// the patch is the locked admins plus every affected managed account
		// that the locking read of lockUsers shows as an active admin:
		// LockActiveAdmins discovers its ids with a consistent read, so on
		// MySQL/MariaDB REPEATABLE READ a patch that committed while this
		// one waited for the admin set (a handover X -> Y) leaves the new
		// admin Y out of admins, and only the current read of Y counts it.
		before := map[uint]bool{}
		for _, admin := range admins {
			before[admin.ID] = true
		}
		for _, id := range affectedManaged {
			if u := users[id]; u.IsActive && u.Role == models.RoleAdmin {
				before[id] = true
			}
		}
		// An admin the patch gives no role result keeps its role: it is
		// unaffected, or only a leftover membership row of an account that
		// is not managed is removed.
		after := map[uint]bool{}
		for _, admin := range admins {
			if r, ok := results[admin.ID]; !ok || r.role == models.RoleAdmin {
				after[admin.ID] = true
			}
		}
		for id, r := range results {
			if r.user.IsActive && r.role == models.RoleAdmin {
				after[id] = true
			}
		}
		if len(before) > 0 && len(after) == 0 {
			return fmt.Errorf("group patch: %w", ErrSCIMGroupLastAdmin)
		}

		if err := members.Remove(group, sortedIDs(removed)); err != nil {
			return fmt.Errorf("remove scim group members: %w", err)
		}
		if err := members.Add(group, sortedIDs(added)); err != nil {
			return fmt.Errorf("add scim group members: %w", err)
		}
		for _, id := range affectedManaged {
			r := results[id]
			if r.role == r.user.Role && sameRoleID(r.roleID, r.user.RoleID) {
				continue
			}
			fields := map[string]interface{}{"role": r.role, "role_id": nil}
			if r.roleID != nil {
				fields["role_id"] = *r.roleID
			}
			if err := tx.users.UpdateFields(id, fields); err != nil {
				return err
			}
			targetID := id
			if err := tx.record(Actor{AuthMethod: AuthMethodSCIM}, models.RoleAuditUserRoleChanged,
				r.roleID, assignedRoleName(r.role, r.custom), &targetID,
				userRoleAuditState{Role: r.user.Role, RoleID: r.user.RoleID},
				userRoleAuditState{Role: r.role, RoleID: r.roleID}); err != nil {
				return err
			}
		}
		changed = affectedManaged
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.writes.audit != nil {
		for _, id := range changed {
			s.writes.audit.Record(ctx, AuthAuditEntry{Method: AuthAuditMethodSCIM, Outcome: AuthAuditOutcomeSuccess, UserID: id})
		}
	}
	records, err := s.withMembers(issuer, []string{group})
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}
