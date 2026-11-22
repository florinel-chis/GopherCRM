package service

// The last-admin guard (#513, #638): the admin set before a write is the
// locked admin set plus every locked row passed to include that is an active
// admin, so a handover that committed while the write waited cannot hide an
// admin; checkAfter judges the state after the write, counting the accounts
// that gain the admin role as well as those that lose it.

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestLastAdminGuard(t *testing.T) {
	user := func(id uint, role models.UserRole, active, erased bool) *models.User {
		u := &models.User{Role: role, IsActive: active}
		u.ID = id
		if erased {
			u.DeletedAt = gorm.DeletedAt{Time: time.Now(), Valid: true}
		}
		return u
	}
	target := user(7, models.RoleAdmin, true, false)
	other := user(9, models.RoleAdmin, true, false)
	sales := user(11, models.RoleSales, true, false)

	demoted := userChange{role: models.RoleSales, active: true}
	deactivated := userChange{role: models.RoleAdmin, active: false}
	erased := userChange{role: models.RoleAdmin, active: true, erased: true}
	keptAdmin := userChange{role: models.RoleAdmin, active: true}
	promoted := userChange{role: models.RoleAdmin, active: true}
	// removed is the change the former single-target helper judged: the
	// account stops being an active admin.
	removed := map[uint]userChange{7: demoted}

	for _, tc := range []struct {
		name    string
		admins  []models.User
		include []*models.User
		changes map[uint]userChange
		refused bool
	}{
		// The seven cases of the former single-target helper.
		{"empty admins, target an active admin", nil, []*models.User{target}, removed, true},
		{"admins is the target", []models.User{*target}, []*models.User{target}, removed, true},
		{"admins is another admin, target an active admin", []models.User{*other}, []*models.User{target}, removed, false},
		{"admins are the target and another", []models.User{*target, *other}, []*models.User{target}, removed, false},
		{"empty admins, target sales", nil, []*models.User{user(7, models.RoleSales, true, false)}, removed, false},
		{"empty admins, target an inactive admin", nil, []*models.User{user(7, models.RoleAdmin, false, false)}, removed, false},
		{"empty admins, target a soft-deleted admin", nil, []*models.User{user(7, models.RoleAdmin, true, true)}, removed, false},

		// One account, the only admin.
		{"only admin demoted", []models.User{*target}, []*models.User{target}, map[uint]userChange{7: demoted}, true},
		{"only admin deactivated", []models.User{*target}, []*models.User{target}, map[uint]userChange{7: deactivated}, true},
		{"only admin erased", []models.User{*target}, []*models.User{target}, map[uint]userChange{7: erased}, true},
		{"only admin kept an active admin", []models.User{*target}, []*models.User{target}, map[uint]userChange{7: keptAdmin}, false},

		// The same writes with a second active admin.
		{"demoted with a second admin", []models.User{*target, *other}, []*models.User{target}, map[uint]userChange{7: demoted}, false},
		{"deactivated with a second admin", []models.User{*target, *other}, []*models.User{target}, map[uint]userChange{7: deactivated}, false},
		{"erased with a second admin", []models.User{*target, *other}, []*models.User{target}, map[uint]userChange{7: erased}, false},

		// Several accounts in one write.
		{"another account promoted while the only admin is demoted", []models.User{*target}, []*models.User{target, sales},
			map[uint]userChange{7: demoted, 11: promoted}, false},
		{"a group result removes every admin", []models.User{*target, *other}, []*models.User{target, other, sales},
			map[uint]userChange{7: demoted, 9: deactivated, 11: {role: models.RoleSales, active: true}}, true},
		{"handover X to Y", []models.User{*target}, []*models.User{target, sales},
			map[uint]userChange{7: deactivated, 11: promoted}, false},
		{"a promotion of an inactive account does not count", []models.User{*target}, []*models.User{target},
			map[uint]userChange{7: demoted, 11: {role: models.RoleAdmin, active: false}}, true},

		// A row promoted concurrently: in the locked rows, missing from the set.
		{"concurrently promoted row changed away", nil, []*models.User{other},
			map[uint]userChange{9: demoted}, true},
		{"concurrently promoted row left unchanged", nil, []*models.User{other},
			map[uint]userChange{7: demoted}, false},

		{"empty admin set", nil, nil, map[uint]userChange{7: demoted}, false},
		{"empty admin set, nothing changed", nil, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := newAdminSet(tc.admins)
			set.include(tc.include...)
			err := set.checkAfter(tc.changes)
			if tc.refused {
				assert.True(t, errors.Is(err, apperrors.ErrLastAdmin), "want ErrLastAdmin, got %v", err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLastAdminGuardIncludeSkipsNil(t *testing.T) {
	set := newAdminSet(nil)
	set.include(nil)
	assert.Empty(t, set.before)
}

func TestResultingActive(t *testing.T) {
	yes, no := true, false
	assert.True(t, resultingActive(true, nil))
	assert.False(t, resultingActive(false, nil))
	assert.True(t, resultingActive(false, &yes))
	assert.False(t, resultingActive(true, &no))
}
