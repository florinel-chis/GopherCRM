package repository

// ErasureHooks are TEST-ONLY seams of the erasure protocol (#385): discover,
// cancel, lock in one order, re-check, scrub (see eraseGraph). A root that
// forces an interleaving parks an erasure at one of them, or makes a lock
// stage fail. They are instance-local: set on one user, customer or lead
// repository through the option of its constructor and copied by its WithTx,
// never a package global. No production file references this type or the
// options (TestLeadErasureHooksNotWiredInProduction in cmd/ enforces it).
//
// A nil func is skipped.
type ErasureHooks struct {
	// AfterDiscovery runs once per discovery of the graph, after its plain
	// reads and before the transaction of the attempt begins (on a WithTx
	// repository: before the unit's savepoint).
	AfterDiscovery func()
	// BeforeLock runs before the locking reads of a stage. table is the stage:
	// leads, customers, users, refresh_tokens, password_reset_tokens, api_keys,
	// in that order, each called whether or not the graph has rows there. The
	// call for leads comes after the automation cancel and happens once per
	// transaction. A returned error ends the attempt as that error.
	BeforeLock func(table string) error
	// AfterLock runs after the locking reads of a stage returned, while the
	// erasure holds those rows.
	AfterLock func(table string)
}

func (h ErasureHooks) seams() *erasureSeams {
	return &erasureSeams{afterDiscovery: h.AfterDiscovery, beforeLock: h.BeforeLock, afterLock: h.AfterLock}
}

// WithUserErasureHooks is a NewUserRepository option that installs hooks on
// that repository instance (and on every repository its WithTx returns).
// Test-only.
func WithUserErasureHooks(hooks ErasureHooks) UserRepositoryOption {
	return func(r *userRepository) { r.lockSeams = hooks.seams() }
}

// WithCustomerErasureHooks is the NewCustomerRepository counterpart of
// WithUserErasureHooks. Test-only.
func WithCustomerErasureHooks(hooks ErasureHooks) CustomerRepositoryOption {
	return func(r *customerRepository) { r.lockSeams = hooks.seams() }
}

// WithLeadErasureLockHooks is the NewLeadRepository counterpart of
// WithUserErasureHooks; it combines with WithLeadErasureHooks. Test-only.
func WithLeadErasureLockHooks(hooks ErasureHooks) LeadRepositoryOption {
	return func(r *leadRepository) { r.lockSeams = hooks.seams() }
}
