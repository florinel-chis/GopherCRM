package repository

// Roots of #571: an erasure runs on its caller's context. A request that is
// cancelled, or whose deadline passes, before the erasure unit commits leaves
// the person exactly as they were: no overwritten field, no purged credential,
// no soft delete. Inside a caller's transaction the unit's savepoint is rolled
// back on an uncancellable context, so the cancellation discards the unit's
// writes without poisoning the caller's transaction.
//
// The cancellation is placed with the in-package erasureScrubObserver, which
// fires after each scrub UPDATE; nothing sleeps. The MySQL root uses a
// disposable TEST_MYSQL_DSN schema, skips without it at the root only and
// fails when it is unreachable.

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestErasureCancellationSQLite(t *testing.T) {
	runErasureCancellation(t, false)
}

func TestErasureCancellationMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runErasureCancellation(t, true)
}

// cancellationRoot erases the person from one node, on a database handle or
// on a caller's transaction, with the given context.
type cancellationRoot struct {
	name  string
	erase func(ctx context.Context, db *gorm.DB, g *identityGraph) error
}

func cancellationRoots() []cancellationRoot {
	return []cancellationRoot{
		{"user", func(ctx context.Context, db *gorm.DB, g *identityGraph) error {
			return NewUserRepository(db).Delete(ctx, g.login.ID)
		}},
		{"customer", func(ctx context.Context, db *gorm.DB, g *identityGraph) error {
			return NewCustomerRepository(db).Delete(ctx, g.customerLive.ID)
		}},
		{"lead", func(ctx context.Context, db *gorm.DB, g *identityGraph) error {
			return NewLeadRepository(db).Delete(ctx, g.leadLive.ID)
		}},
	}
}

// withTx binds the root's repository to a caller's transaction.
func (r cancellationRoot) withTx(ctx context.Context, db, tx *gorm.DB, g *identityGraph) error {
	switch r.name {
	case "user":
		return NewUserRepository(db).WithTx(tx).Delete(ctx, g.login.ID)
	case "customer":
		return NewCustomerRepository(db).WithTx(tx).Delete(ctx, g.customerLive.ID)
	default:
		return NewLeadRepository(db).WithTx(tx).Delete(ctx, g.leadLive.ID)
	}
}

// observerDeadlineCtx is a context whose deadline "passes" when the test
// closes its Done channel: Err then reports context.DeadlineExceeded, exactly
// as an expired request context does, without waiting on a clock.
type observerDeadlineCtx struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newObserverDeadlineCtx() *observerDeadlineCtx {
	return &observerDeadlineCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *observerDeadlineCtx) Done() <-chan struct{} { return c.done }

func (c *observerDeadlineCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *observerDeadlineCtx) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *observerDeadlineCtx) expire() { c.once.Do(func() { close(c.done) }) }

// erasureSnapshot is what a cancelled erasure must leave untouched: the
// personal fields and deleted_at of every lead, customer and user of the
// graph, and the login's credential rows.
type erasureSnapshot struct {
	users       map[uint][4]string
	customers   map[uint][5]string
	leads       map[uint][5]string
	credentials map[string]int64
}

func deletedAtString(d gorm.DeletedAt) string {
	if !d.Valid {
		return "NULL"
	}
	return d.Time.UTC().Format(time.RFC3339Nano)
}

func snapshotErasureGraph(t *testing.T, db *gorm.DB, g *identityGraph) erasureSnapshot {
	t.Helper()
	s := erasureSnapshot{users: map[uint][4]string{}, customers: map[uint][5]string{}, leads: map[uint][5]string{}, credentials: map[string]int64{}}
	var u models.User
	require.NoError(t, db.Unscoped().First(&u, g.login.ID).Error)
	s.users[u.ID] = [4]string{u.Email, u.FirstName, u.LastName, deletedAtString(u.DeletedAt)}
	for _, id := range []uint{g.customerLive.ID, g.customerGone.ID} {
		var c models.Customer
		require.NoError(t, db.Unscoped().First(&c, id).Error)
		s.customers[id] = [5]string{c.Email, c.FirstName, c.LastName, c.Phone, deletedAtString(c.DeletedAt)}
	}
	for _, id := range []uint{g.leadLive.ID, g.leadGone.ID, g.leadB.ID} {
		var l models.Lead
		require.NoError(t, db.Unscoped().First(&l, id).Error)
		s.leads[id] = [5]string{l.Email, l.FirstName, l.LastName, l.Phone, deletedAtString(l.DeletedAt)}
	}
	for name, model := range map[string]interface{}{"api_keys": &models.APIKey{}, "refresh_tokens": &models.RefreshToken{}, "password_reset_tokens": &models.PasswordResetToken{}} {
		s.credentials[name] = countRows(t, db, model, "user_id = ?", g.login.ID)
	}
	return s
}

// assertGraphUnchanged compares the graph, read on the root handle, with the
// snapshot taken right after seeding.
func assertGraphUnchanged(t *testing.T, db *gorm.DB, g *identityGraph, seeded erasureSnapshot) {
	t.Helper()
	now := snapshotErasureGraph(t, db, g)
	assert.Equal(t, seeded.users, now.users, "the login keeps its email, names and deleted_at")
	assert.Equal(t, seeded.customers, now.customers, "every customer keeps its email, names, phone and deleted_at")
	assert.Equal(t, seeded.leads, now.leads, "every lead keeps its email, names, phone and deleted_at")
	assert.Equal(t, seeded.credentials, now.credentials, "the login's credentials stay")
	for name, n := range now.credentials {
		assert.Positive(t, n, "the seeded %s row still exists", name)
	}
	// The live rows really are live, so an unchanged deleted_at is a NULL one.
	assert.Equal(t, "NULL", now.users[g.login.ID][3])
	assert.Equal(t, "NULL", now.customers[g.customerLive.ID][4])
	assert.Equal(t, "NULL", now.leads[g.leadLive.ID][4])
}

// observeScrubs registers an observer that counts the scrubs and calls
// onFirst once, after the first scrub UPDATE. It is removed on cleanup.
func observeScrubs(t *testing.T, onFirst func()) *atomic.Int64 {
	t.Helper()
	require.Nil(t, erasureScrubObserver.Load(), "no other test may have left a scrub observer behind")
	t.Cleanup(func() { erasureScrubObserver.Store(nil) })
	var fired atomic.Int64
	observe := func(string, uint) {
		if fired.Add(1) == 1 && onFirst != nil {
			onFirst()
		}
	}
	erasureScrubObserver.Store(&observe)
	return &fired
}

func runErasureCancellation(t *testing.T, useMySQL bool) {
	for _, root := range cancellationRoots() {
		root := root
		t.Run(root.name, func(t *testing.T) {
			db := openIdentityErasureDB(t, useMySQL)

			t.Run("completes_when_not_cancelled", func(t *testing.T) {
				g := seedIdentityGraph(t, db)
				fired := observeScrubs(t, nil)
				require.NoError(t, root.erase(context.Background(), db, g))
				assert.Positive(t, fired.Load())
				assertGraphErased(t, db, g)
			})

			t.Run("cancelled_before_the_call", func(t *testing.T) {
				g := seedIdentityGraph(t, db)
				seeded := snapshotErasureGraph(t, db, g)
				fired := observeScrubs(t, nil)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				err := root.erase(ctx, db, g)
				require.Error(t, err)
				assert.True(t, errors.Is(err, context.Canceled), "error %v must match context.Canceled", err)
				assert.Zero(t, fired.Load(), "nothing is scrubbed on a context that was already cancelled")
				assertGraphUnchanged(t, db, g, seeded)
			})

			t.Run("cancelled_after_the_first_scrub", func(t *testing.T) {
				g := seedIdentityGraph(t, db)
				seeded := snapshotErasureGraph(t, db, g)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fired := observeScrubs(t, cancel)
				err := root.erase(ctx, db, g)
				require.Error(t, err)
				assert.True(t, errors.Is(err, context.Canceled), "error %v must match context.Canceled", err)
				assert.Positive(t, fired.Load(), "an overwrite was issued before the cancel")
				assertGraphUnchanged(t, db, g, seeded)
			})

			t.Run("deadline_after_the_first_scrub", func(t *testing.T) {
				g := seedIdentityGraph(t, db)
				seeded := snapshotErasureGraph(t, db, g)
				ctx := newObserverDeadlineCtx()
				defer ctx.expire()
				fired := observeScrubs(t, ctx.expire)
				err := root.erase(ctx, db, g)
				require.Error(t, err)
				assert.True(t, errors.Is(err, context.DeadlineExceeded), "error %v must match context.DeadlineExceeded", err)
				assert.Positive(t, fired.Load(), "an overwrite was issued before the deadline")
				assertGraphUnchanged(t, db, g, seeded)
			})

			t.Run("cancelled_inside_a_caller_transaction", func(t *testing.T) {
				g := seedIdentityGraph(t, db)
				seeded := snapshotErasureGraph(t, db, g)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fired := observeScrubs(t, cancel)

				callerTx := db.Begin()
				require.NoError(t, callerTx.Error)
				committed := false
				defer func() {
					if !committed {
						callerTx.Rollback()
					}
				}()
				err := root.withTx(ctx, db, callerTx, g)
				require.Error(t, err)
				assert.True(t, errors.Is(err, context.Canceled), "error %v must match context.Canceled", err)
				assert.False(t, errors.Is(err, utils.ErrTransactionPoisoned), "the caller's live transaction must not be poisoned: %v", err)
				assert.Positive(t, fired.Load(), "an overwrite was issued before the cancel")

				require.NoError(t, callerTx.Commit().Error, "the caller's transaction still commits")
				committed = true
				assertGraphUnchanged(t, db, g, seeded)
			})
		})
	}
}
