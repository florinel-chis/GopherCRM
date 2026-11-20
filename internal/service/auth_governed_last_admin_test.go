package service

// Issue #513: a users write that judges the last-admin rule while a SCIM group
// handover X -> Y commits. The write discovers the active admins with a
// consistent read before the handover commits (X), waits for X's row lock,
// and then finds X demoted, so the admin set it locked is empty. Only the
// locking read of the target (Y) sees the admin the handover promoted; the
// guard must count it, or the write leaves no active admin. The helpers below
// drive that interleaving deterministically on MySQL/MariaDB; the SCIM user
// replace, patch and delete and the OIDC sign-in reuse them in their own
// tables, TestGovernedLastAdminAfterHandoverMySQL covers the governed user
// write behind /users.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// holdAdminHandover opens a raw transaction that demotes x to sales and
// promotes y to admin (role and system role_id both) and leaves it
// uncommitted, so it holds both rows locked. The caller commits it through
// awaitLockWaitThenCommit; a deferred Rollback is registered for a failure.
func holdAdminHandover(t *testing.T, db *gorm.DB, roleIDs map[models.UserRole]uint, x, y uint) *gorm.DB {
	t.Helper()
	require.NotZero(t, roleIDs[models.RoleSales])
	require.NotZero(t, roleIDs[models.RoleAdmin])
	handover := db.Begin()
	require.NoError(t, handover.Error)
	t.Cleanup(func() { handover.Rollback() })
	require.NoError(t, handover.Model(&models.User{}).Where("id = ?", x).
		Updates(map[string]interface{}{"role": models.RoleSales, "role_id": roleIDs[models.RoleSales]}).Error)
	require.NoError(t, handover.Model(&models.User{}).Where("id = ?", y).
		Updates(map[string]interface{}{"role": models.RoleAdmin, "role_id": roleIDs[models.RoleAdmin]}).Error)
	return handover
}

// awaitLockWaitThenCommit waits, bounded, until the write running in another
// goroutine is parked on the admin-set lock, commits the handover and returns
// the write's result. The signal is a statement containing FOR UPDATE on
// another connection of this schema in information_schema.processlist: in
// every guarded body LockActiveAdmins is the first locking statement (what
// precedes it are plain reads, and the handover's own statements are plain
// UPDATEs), and its first FOR UPDATE names X, which the handover holds, so
// once it shows it is waiting. information_schema.innodb_trx is no signal
// here: MySQL 8 does not list the waiting transaction there, because it has
// only read until then.
func awaitLockWaitThenCommit(t *testing.T, db, handover *gorm.DB, done <-chan error) error {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM information_schema.processlist "+
			"WHERE id <> CONNECTION_ID() AND db = DATABASE() AND info LIKE ?", "%FOR UPDATE%").
			Scan(&waiting).Error)
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the write finished before the handover committed: %v", err)
		default:
		}
		require.True(t, time.Now().Before(deadline), "the write never waited for the handover's lock")
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, handover.Commit().Error)

	select {
	case err := <-done:
		return err
	case <-time.After(60 * time.Second):
		t.Fatal("the write did not finish after the handover committed")
		return nil
	}
}

// countActiveAdmins counts the live, active admin accounts.
func countActiveAdmins(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM users WHERE role = ? AND is_active = ? AND deleted_at IS NULL",
		models.RoleAdmin, true).Scan(&n).Error)
	return n
}

// TestGovernedLastAdminAfterHandoverMySQL: Y deactivates itself through the
// governed user service (PUT/activate/deactivate/DELETE /users) while a
// handover X -> Y commits. The actor is Y, whose locked row is admin after
// the handover, so the key and escalation checks pass and the last-admin rule
// is what decides: the write is refused and Y stays the one active admin.
func TestGovernedLastAdminAfterHandoverMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	ctx := context.Background()

	roleIDs := map[models.UserRole]uint{}
	var roles []models.Role
	require.NoError(t, db.Where("system_key IS NOT NULL").Find(&roles).Error)
	for _, role := range roles {
		roleIDs[models.UserRole(*role.SystemKey)] = role.ID
	}
	adminID, salesID := roleIDs[models.RoleAdmin], roleIDs[models.RoleSales]
	x := &models.User{Email: "x@handover.test", Password: "x", FirstName: "X", LastName: "Admin",
		Role: models.RoleAdmin, RoleID: &adminID, IsActive: true}
	y := &models.User{Email: "y@handover.test", Password: "x", FirstName: "Y", LastName: "Sales",
		Role: models.RoleSales, RoleID: &salesID, IsActive: true}
	require.NoError(t, db.Create(x).Error)
	require.NoError(t, db.Create(y).Error)

	users := NewUserService(repository.NewUserRepository(db),
		WithUserRoleGovernance(repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db), utils.NewTransactionManager(db)))

	handover := holdAdminHandover(t, db, roleIDs, x.ID, y.ID)
	done := make(chan error, 1)
	go func() {
		_, err := users.SetActiveAs(ctx, Actor{UserID: y.ID, AuthMethod: AuthMethodBearer}, y.ID, false)
		done <- err
	}()
	err := awaitLockWaitThenCommit(t, db, handover, done)

	require.ErrorIs(t, err, apperrors.ErrLastAdmin)
	assert.Equal(t, int64(1), countActiveAdmins(t, db), "exactly one active admin remains")
	var stored models.User
	require.NoError(t, db.Unscoped().First(&stored, y.ID).Error)
	assert.Equal(t, models.RoleAdmin, stored.Role, "Y stays admin")
	assert.True(t, stored.IsActive, "Y stays active")
	var roleAudits int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM role_audit_log").Scan(&roleAudits).Error)
	assert.Zero(t, roleAudits, "the refused write wrote no audit row")
}
