package service

// Issue #513: isLastActiveAdmin counts the target's own locked row as an admin
// before the write, next to the admin set LockActiveAdmins returned, so a
// handover that committed while the write waited cannot hide the target.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestIsLastActiveAdmin(t *testing.T) {
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

	for _, tc := range []struct {
		name   string
		admins []models.User
		target *models.User
		want   bool
	}{
		{"empty admins, target an active admin", nil, target, true},
		{"admins is the target", []models.User{*target}, target, true},
		{"admins is another admin, target an active admin", []models.User{*other}, target, false},
		{"admins are the target and another", []models.User{*target, *other}, target, false},
		{"empty admins, target sales", nil, user(7, models.RoleSales, true, false), false},
		{"empty admins, target an inactive admin", nil, user(7, models.RoleAdmin, false, false), false},
		{"empty admins, target a soft-deleted admin", nil, user(7, models.RoleAdmin, true, true), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isLastActiveAdmin(tc.admins, tc.target))
		})
	}
}
