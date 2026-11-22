package service

import (
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// adminSet is the last-admin guard of every governed account write: a write
// must not leave the system without an active admin. before holds the ids of
// the active admins before the write.
type adminSet struct {
	before map[uint]bool
}

// lockAdminSet locks the active admin set, the first statement of the governed
// lock order, and returns it as the guard's state before the write.
func lockAdminSet(tx *governedTx) (adminSet, error) {
	admins, err := tx.userRoles.LockActiveAdmins()
	if err != nil {
		return adminSet{}, err
	}
	return newAdminSet(admins), nil
}

// newAdminSet seeds the guard with the locked active admins.
func newAdminSet(admins []models.User) adminSet {
	set := adminSet{before: make(map[uint]bool, len(admins))}
	for _, admin := range admins {
		set.before[admin.ID] = true
	}
	return set
}

// include adds every locked row that is an active, live admin to the set
// before the write.
//
// The repository discovers the admin ids with a consistent read, so on
// MySQL/MariaDB REPEATABLE READ an admin promoted by a transaction that
// committed while this one waited on the lock (a SCIM group handover X -> Y,
// #506) is missing from the locked set; only the locking read of lockUsers
// sees that row (#513). Counting it can only make the guard refuse more often
// during such a race, never less.
func (s adminSet) include(users ...*models.User) {
	for _, u := range users {
		if u != nil && u.Role == models.RoleAdmin && u.IsActive && !u.DeletedAt.Valid {
			s.before[u.ID] = true
		}
	}
}

// userChange is the state of one account after the write.
type userChange struct {
	role   models.UserRole
	active bool
	erased bool
}

// counts reports whether the account is an active admin after the write.
func (c userChange) counts() bool {
	return c.role == models.RoleAdmin && c.active && !c.erased
}

// checkAfter judges the state after the write: ErrLastAdmin when the set held
// an active admin before and none is left. An admin without an entry in
// changes keeps its membership; an account with an entry counts when it is an
// active admin after the write, so an account that gains the role counts as
// well as one that loses it.
func (s adminSet) checkAfter(changes map[uint]userChange) error {
	if len(s.before) == 0 {
		return nil
	}
	for id := range s.before {
		if c, ok := changes[id]; !ok || c.counts() {
			return nil
		}
	}
	for _, c := range changes {
		if c.counts() {
			return nil
		}
	}
	return apperrors.ErrLastAdmin
}

// resultingActive is is_active after a write that sets it to requested, or
// leaves current when requested is nil.
func resultingActive(current bool, requested *bool) bool {
	if requested != nil {
		return *requested
	}
	return current
}
