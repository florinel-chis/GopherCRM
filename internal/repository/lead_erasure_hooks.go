package repository

import "gorm.io/gorm"

// LeadErasureHooks are TEST-ONLY seams of the lead-rooted erasure (#337). A
// root that forces an interleaving against a concurrent lead conversion parks
// the erasure at one of them. They are instance-local: set on one lead
// repository through WithLeadErasureHooks and copied by its WithTx, never a
// package global. No production file references this type or the option
// (TestLeadErasureHooksNotWiredInProduction in cmd/ enforces it).
//
// Each func receives the id of the lead being erased; a nil func is skipped.
type LeadErasureHooks struct {
	// AfterAutomationCancel runs after the lead's automation runs are
	// cancelled and before the locking read of the lead row. It also receives
	// the erasure unit's transaction handle, so that a root can issue a plain
	// (consistent) read on it before it parks: on MySQL/MariaDB that read
	// opens the unit's REPEATABLE READ snapshot, as the pre-#337 non-locking
	// discovery read did, which is what makes a conversion committed while the
	// erasure is parked a stale-snapshot case. The hook must not write.
	AfterAutomationCancel func(tx *gorm.DB, leadID uint)
	// AfterLeadLock runs right after the locking read of the lead found the
	// row, while the erasure holds the lead's row lock and before the refusal
	// or the graph decision.
	AfterLeadLock func(leadID uint)
}

// WithLeadErasureHooks is a NewLeadRepository option that installs hooks on
// that repository instance (and on every repository its WithTx returns).
// Test-only.
func WithLeadErasureHooks(hooks LeadErasureHooks) LeadRepositoryOption {
	return func(r *leadRepository) {
		r.erasureSeams = &leadErasureSeams{
			afterAutomationCancel: hooks.AfterAutomationCancel,
			afterLeadLock:         hooks.AfterLeadLock,
		}
	}
}
