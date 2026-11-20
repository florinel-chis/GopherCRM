package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

// Role governance (#259 S4, #348): what the role service and the governed
// user writes share. Every such write is one root transaction that takes its
// locks in one order, re-reads the actor inside it and decides only then:
//
//	admin set → users (id ascending) → roles (id ascending)
//
// A role write skips the admin set (it cannot change it) and starts at the
// actor's users row.

// roleWriteMaxRetries bounds the replays of a governed role or user write that
// lost a deadlock or a lock wait.
const roleWriteMaxRetries = 3

// roleWriteGraphMaxRestarts bounds the whole-root restarts of a governed user
// delete whose nested erasure found the graph of the person changed under its
// locks (repository.ErrErasureGraphChanged).
const roleWriteGraphMaxRestarts = 3

// Actor is who performs a governed write and how its request authenticated.
// AuthMethod is the request's AuthMethodContextKey value, AuthMethodOIDC or
// AuthMethodSCIM, stored on the audit row as it is (UserID 0 stores NULL).
type Actor struct {
	UserID     uint
	AuthMethod string
}

// errRoleGovernanceUnavailable is a wiring fault: the user repository handed
// to a governed service lacks the role half (repository.UserRoleMaintenance).
// Nothing is written; it is answered with the generic 500.
var errRoleGovernanceUnavailable = errors.New("role governance needs a user repository with the role maintenance half")

// governedActor is the actor as the transaction re-read it under its lock:
// the users row and the permissions it holds now, not the ones its request
// was admitted with.
type governedActor struct {
	user *models.User
	set  permissions.Set
}

// permissionsExceed reports whether subject holds a grant that held does not
// cover: scope own is below all, and a missing key is below own.
func permissionsExceed(subject, held permissions.Set) bool {
	for _, grant := range subject.List() {
		switch held.Scope(grant.Key) {
		case models.PermissionScopeAll:
			continue
		case models.PermissionScopeOwn:
			if grant.Scope == models.PermissionScopeOwn {
				continue
			}
		}
		return true
	}
	return false
}

// checkEscalation is the escalation rule, the only copy: an actor may neither
// grant nor manage a permission set that exceeds its own. Every role write
// (the requested set, and on update and delete the stored one) and every user
// write (the target as it is, and the role it is to receive) goes through it.
// A system admin is exempt.
func checkEscalation(actor governedActor, subject permissions.Set) error {
	if actor.user.Role == models.RoleAdmin {
		return nil
	}
	if permissionsExceed(subject, actor.set) {
		return apperrors.ErrRoleEscalation
	}
	return nil
}

// escalationSet is the permission set a user's role stands for in the
// escalation rule: the code matrix of admin, sales and support, the effective
// permissions of a loaded custom role, and nothing for an unknown role or a
// custom user without a role.
//
// The customer system role is the floor: it stands for the empty set, so it
// never exceeds any actor, as a target and as a requested role. Its matrix
// holds the one system-only key, which no custom role can hold; comparing it
// would bar every custom user manager from every customer login.
func escalationSet(role models.UserRole, custom *models.Role) permissions.Set {
	switch role {
	case models.RoleCustomer:
		return permissions.Set{}
	case models.RoleCustom:
		if custom == nil {
			return permissions.Set{}
		}
		return permissions.ForCustomRole(custom.Permissions)
	default:
		return permissions.ForSystemRole(string(role))
	}
}

// roleGovernance is the dependencies of a governed write.
type roleGovernance struct {
	users     repository.UserRepository
	roles     repository.RoleRepository
	audit     repository.RoleAuditRepository
	txManager *utils.TransactionManager
}

// run executes fn as one root transaction, replayed on a deadlock or a lock
// wait, with the repositories bound to it.
//
// A governed user delete nests the erasure of the target (#385). Joined to
// this transaction the erasure runs once, and when the graph of the person
// moved under its locks it returns repository.ErrErasureGraphChanged: the
// whole root is then started again, at most roleWriteGraphMaxRestarts times
// (shouldRestartGovernedWrite). The deadlock replay is WithTransactionAndRetry's
// own decision and is not touched here.
func (g *roleGovernance) run(ctx context.Context, fn func(tx *governedTx) error) error {
	for restarts := 0; ; restarts++ {
		err := g.runOnce(ctx, fn)
		if !shouldRestartGovernedWrite(ctx, err) {
			return err
		}
		if restarts >= roleWriteGraphMaxRestarts {
			return fmt.Errorf("governed write abandoned after %d attempts: %w", restarts+1, err)
		}
	}
}

// shouldRestartGovernedWrite reports whether a failed governed write may be
// started again from its first statement: only an error that carries
// repository.ErrErasureGraphChanged, whose transaction rolled back cleanly (no
// ErrRollbackFailed, ErrTransactionPoisoned, ErrCommitFailed or ErrBeginFailed,
// which leave the outcome unknown), on a context that is not done. The bound
// is the caller's.
func shouldRestartGovernedWrite(ctx context.Context, err error) bool {
	if err == nil || !errors.Is(err, repository.ErrErasureGraphChanged) {
		return false
	}
	for _, unclean := range []error{utils.ErrRollbackFailed, utils.ErrTransactionPoisoned, utils.ErrCommitFailed, utils.ErrBeginFailed} {
		if errors.Is(err, unclean) {
			return false
		}
	}
	return ctx.Err() == nil
}

// runOnce is one root transaction of run, with its deadlock and lock-wait
// replays.
func (g *roleGovernance) runOnce(ctx context.Context, fn func(tx *governedTx) error) error {
	return g.txManager.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
		handle, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		users := g.users.WithTx(handle)
		userRoles, ok := users.(repository.UserRoleMaintenance)
		if !ok {
			return errRoleGovernanceUnavailable
		}
		return fn(&governedTx{
			handle:    handle,
			users:     users,
			userRoles: userRoles,
			roles:     g.roles.WithTx(handle),
			audit:     g.audit.WithTx(handle),
		})
	}, roleWriteMaxRetries)
}

// governedTx is the repositories of one governed transaction.
type governedTx struct {
	// handle is the transaction itself, for the writes of a flow that
	// continues past the role tables (the OIDC sign-in's user_identities and
	// refresh_tokens, #435).
	handle    *gorm.DB
	users     repository.UserRepository
	userRoles repository.UserRoleMaintenance
	roles     repository.RoleRepository
	audit     repository.RoleAuditRepository
}

// lockUsers locks the users rows in ascending id order and returns the live
// ones by id.
func (tx *governedTx) lockUsers(ids ...uint) (map[uint]*models.User, error) {
	locked, err := tx.userRoles.LockLiveByIDs(uniqueSortedIDs(ids))
	if err != nil {
		return nil, err
	}
	users := make(map[uint]*models.User, len(locked))
	for i := range locked {
		users[locked[i].ID] = &locked[i]
	}
	return users, nil
}

// lockRoles locks the role rows the ids name (nil ids are skipped) in
// ascending id order and returns the existing ones by id, each with its
// current permissions.
func (tx *governedTx) lockRoles(ids ...*uint) (map[uint]*models.Role, error) {
	var named []uint
	for _, id := range ids {
		if id != nil {
			named = append(named, *id)
		}
	}
	locked, err := tx.roles.LockByIDs(uniqueSortedIDs(named))
	if err != nil {
		return nil, err
	}
	roles := make(map[uint]*models.Role, len(locked))
	for i := range locked {
		roles[locked[i].ID] = &locked[i]
	}
	return roles, nil
}

// actor resolves the locked actor row to its current permissions. A missing,
// erased or deactivated actor holds nothing and is refused: its credential was
// valid when the request was admitted and is not any more.
func (tx *governedTx) actor(user *models.User, roles map[uint]*models.Role) (governedActor, error) {
	if user == nil || !user.IsActive {
		return governedActor{}, fmt.Errorf("the acting account is gone or deactivated: %w", apperrors.ErrForbidden)
	}
	if user.RoleID != nil {
		user.CustomRole = roles[*user.RoleID]
	}
	return governedActor{user: user, set: permissions.ForUser(user)}, nil
}

// requireKeys refuses an actor that no longer holds one of the keys its
// request was admitted with.
func (a governedActor) requireKeys(keys ...string) error {
	for _, key := range keys {
		if !a.set.Has(key) {
			return fmt.Errorf("the acting account no longer holds %s: %w", key, apperrors.ErrForbidden)
		}
	}
	return nil
}

// record appends the audit row of a change, in the transaction of the change.
// before and after are marshalled to JSON; nil is stored as NULL. An actor id
// of 0 (a SCIM write, #470 and #471) stores actor_user_id NULL.
func (tx *governedTx) record(actor Actor, action models.RoleAuditAction, roleID *uint, roleName string, targetUserID *uint, before, after interface{}) error {
	entry := &models.RoleAuditEntry{
		AuthMethod:   actor.AuthMethod,
		Action:       action,
		RoleID:       roleID,
		RoleName:     roleName,
		TargetUserID: targetUserID,
	}
	if actor.UserID != 0 {
		actorID := actor.UserID
		entry.ActorUserID = &actorID
	}
	var err error
	if entry.BeforeState, err = auditState(before); err != nil {
		return err
	}
	if entry.AfterState, err = auditState(after); err != nil {
		return err
	}
	if err := tx.audit.Create(entry); err != nil {
		return fmt.Errorf("write role audit row: %w", err)
	}
	return nil
}

func auditState(state interface{}) (*string, error) {
	if state == nil {
		return nil, nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode role audit state: %w", err)
	}
	encoded := string(raw)
	return &encoded, nil
}

// roleAuditState is the before/after state of a role change: the role's name,
// description and effective permissions. It holds no user data.
type roleAuditState struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Permissions []permissions.Grant `json:"permissions"`
}

// userRoleAuditState is the before/after state of a user's role change: the
// users.role value and the custom role id. It holds ids only, never a name or
// an address.
type userRoleAuditState struct {
	Role   models.UserRole `json:"role"`
	RoleID *uint           `json:"role_id"`
}

func uniqueSortedIDs(ids []uint) []uint {
	seen := make(map[uint]bool, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameRoleID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
