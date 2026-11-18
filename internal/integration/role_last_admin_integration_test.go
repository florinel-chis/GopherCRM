package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The last-admin invariant (#259 AC-13, AC-14; #348): no role change,
// deactivation or erasure leaves zero active admins. The concurrency roots
// use no sleep as synchronisation: on SQLite the requests are stopped after
// their admission and released together; on MySQL they are shown parked on
// the admin-set lock through information_schema.PROCESSLIST before the lock
// is released. Outcomes are read from committed rows.

// roleRaceWait bounds every wait of the concurrency roots.
const roleRaceWait = 20 * time.Second

func (e *permissionEnv) activeAdmins() int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Model(&models.User{}).
		Where("role = ? AND is_active = ?", models.RoleAdmin, true).Count(&count).Error)
	return count
}

// --- service -------------------------------------------------------------------

func TestLastAdminInvariantSQLite(t *testing.T) {
	runLastAdminInvariant(t, false)
}

func TestLastAdminInvariantMySQL(t *testing.T) {
	runLastAdminInvariant(t, true)
}

// runLastAdminInvariant drives the governed user service itself: with one
// active admin its demotion, deactivation and erasure are refused and write
// nothing; with a second active admin each succeeds.
func runLastAdminInvariant(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	users := service.NewUserService(env.userRepo, service.WithUserRoleGovernance(
		env.roles, repository.NewRoleAuditRepository(env.db), utils.NewTransactionManager(env.db)))
	ctx := context.Background()

	// The actor passes every other rule: it holds the whole admin matrix on a
	// custom role, so only the invariant can refuse it.
	full := env.customUser("full", env.role("Full", adminMatrixGrants(t)...))
	actor := service.Actor{UserID: full.ID, AuthMethod: service.AuthMethodBearer}
	within := env.role("Within", "leads.view")

	demote := func(id uint) error {
		_, err := users.UpdateAs(ctx, actor, id, map[string]interface{}{"role": models.RoleSales})
		return err
	}
	demoteToCustom := func(id uint) error {
		_, err := users.UpdateAs(ctx, actor, id, map[string]interface{}{"role": models.RoleCustom, "role_id": &within.ID})
		return err
	}
	deactivateByUpdate := func(id uint) error {
		_, err := users.UpdateAs(ctx, actor, id, map[string]interface{}{"is_active": false})
		return err
	}
	deactivate := func(id uint) error {
		_, err := users.SetActiveAs(ctx, actor, id, false)
		return err
	}
	erase := func(id uint) error { return users.DeleteAs(ctx, actor, id) }

	admin := env.user("admin", models.RoleAdmin)

	t.Run("one active admin", func(t *testing.T) {
		// A deactivated and an erased admin do not count as active.
		inactive := env.user("inactive-admin", models.RoleAdmin)
		require.NoError(t, env.db.Model(&models.User{}).Where("id = ?", inactive.ID).Update("is_active", false).Error)
		erased := env.user("erased-admin", models.RoleAdmin)
		require.NoError(t, env.userRepo.Delete(erased.ID))
		require.EqualValues(t, 1, env.activeAdmins())

		for name, change := range map[string]func(uint) error{
			"demotion to a system role": demote,
			"demotion to a custom role": demoteToCustom,
			"deactivation by update":    deactivateByUpdate,
			"deactivation":              deactivate,
			"erasure":                   erase,
		} {
			state, audits := env.userState(admin.ID), env.auditCount()
			err := change(admin.ID)
			require.ErrorIs(t, err, apperrors.ErrLastAdmin, name)
			assert.Equal(t, state, env.userState(admin.ID), "%s wrote nothing to the row", name)
			assert.Equal(t, audits, env.auditCount(), "%s wrote no audit row", name)
		}
		assert.EqualValues(t, 1, env.activeAdmins())

		// What does not take the admin out of the active set is not refused.
		_, err := users.UpdateAs(ctx, actor, admin.ID, map[string]interface{}{"role": models.RoleAdmin, "first_name": "Still"})
		require.NoError(t, err)
		_, err = users.SetActiveAs(ctx, actor, admin.ID, true)
		require.NoError(t, err)
		// The deactivated admin is not the last active one: it can go.
		require.NoError(t, erase(inactive.ID))
	})

	t.Run("with a second active admin each change succeeds", func(t *testing.T) {
		current := admin
		for _, c := range []struct {
			name   string
			change func(uint) error
			want   string
			audits int64
		}{
			{"demotion", demote, "role=sales role_id=null active=true erased=false", 1},
			{"deactivation", deactivate, "role=admin role_id=null active=false erased=false", 0},
			{"erasure", erase, "active=false erased=true", 0},
		} {
			second := env.user("second-admin", models.RoleAdmin)
			require.EqualValues(t, 2, env.activeAdmins(), c.name)
			audits := env.auditCount()
			require.NoError(t, c.change(current.ID), c.name)
			assert.Contains(t, env.userState(current.ID), c.want, c.name)
			assert.Equal(t, audits+c.audits, env.auditCount(), c.name)
			assert.EqualValues(t, 1, env.activeAdmins(), c.name)
			current = second
		}
	})
}

// --- concurrency ---------------------------------------------------------------

// raceRequest is one request of a concurrent pair.
type raceRequest struct {
	as     *models.User
	method string
	path   string
	body   interface{}
}

// adminSetLockStatement is the admin-set lock of a governed user write as
// PROCESSLIST shows it: the locking read of users rows by primary key that
// LockActiveAdmins issues first.
var adminSetLockStatement = regexp.MustCompile("(?s)^SELECT \\* FROM `users` WHERE id IN .*FOR UPDATE$")

// awaitLockWaiters returns once n connections are seen executing a statement
// matching target on two consecutive PROCESSLIST polls: an uncontended
// statement finishes in microseconds, so one still running a poll later waits
// for a lock. PROCESSLIST exists on MySQL 8 and MariaDB 10.11.
func awaitLockWaiters(t *testing.T, db *gorm.DB, target *regexp.Regexp, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), roleRaceWait)
	defer cancel()
	previous := ""
	for ctx.Err() == nil {
		var rows []struct {
			ID   int64
			Info string
		}
		err := db.WithContext(ctx).Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
			AND INFO IS NOT NULL`).Scan(&rows).Error
		var ids []int64
		if err == nil {
			for _, row := range rows {
				if target.MatchString(row.Info) {
					ids = append(ids, row.ID)
				}
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		current := fmt.Sprint(ids)
		if len(ids) >= n && current == previous {
			return
		}
		previous = current
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%d statement(s) matching %s did not wait on a lock", n, target)
}

// holdAdminSet opens a transaction that holds the row locks of the active
// admins, the first lock of every governed user write.
func holdAdminSet(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	holder := db.Begin()
	require.NoError(t, holder.Error)
	var ids []uint
	require.NoError(t, holder.Raw("SELECT id FROM users WHERE role = 'admin' AND is_active = 1 AND deleted_at IS NULL ORDER BY id FOR UPDATE").
		Scan(&ids).Error)
	require.NotEmpty(t, ids, "there is an admin set to hold")
	return holder
}

// race sends the requests concurrently and returns their answers in order.
// On SQLite every request is stopped right after its admission and all are
// released together; on MySQL the admin set is held until every request is
// parked on it.
func (e *permissionEnv) race(useMySQL bool, requests ...raceRequest) []permissionResponse {
	e.t.Helper()
	type prepared struct {
		authorization string
		body          []byte
	}
	ready := make([]prepared, len(requests))
	for i, request := range requests {
		ready[i].authorization = "Bearer " + e.token(request.as)
		if request.body != nil {
			ready[i].body = mustJSON(e.t, request.body)
		}
	}
	answers := make([]chan permissionResponse, len(requests))
	launch := func() {
		for i := range requests {
			answers[i] = make(chan permissionResponse, 1)
			go func(i int) {
				answers[i] <- e.serve(ready[i].authorization, requests[i].method, requests[i].path, ready[i].body)
			}(i)
		}
	}

	if useMySQL {
		holder := holdAdminSet(e.t, e.db)
		launch()
		awaitLockWaiters(e.t, e.db, adminSetLockStatement, len(requests))
		for i := range answers {
			select {
			case early := <-answers[i]:
				_ = holder.Rollback().Error
				e.t.Fatalf("request %d answered %s while the admin set was held", i, statusText(early))
			default:
			}
		}
		require.NoError(e.t, holder.Rollback().Error)
	} else {
		admitted := make(chan struct{}, len(requests))
		release := make(chan struct{})
		e.onAdmitted(func(*gin.Context) {
			admitted <- struct{}{}
			select {
			case <-release:
			case <-time.After(roleRaceWait):
			}
		})
		launch()
		for range requests {
			select {
			case <-admitted:
			case <-time.After(roleRaceWait):
				e.onAdmitted(nil)
				close(release)
				e.t.Fatal("a request was not admitted in time")
			}
		}
		e.onAdmitted(nil)
		close(release)
	}

	out := make([]permissionResponse, len(requests))
	for i := range answers {
		select {
		case out[i] = <-answers[i]:
		case <-time.After(roleRaceWait):
			e.t.Fatalf("request %d did not answer in time", i)
		}
	}
	return out
}

func TestLastAdminConcurrentSQLite(t *testing.T) {
	runLastAdminConcurrent(t, false)
}

func TestLastAdminConcurrentMySQL(t *testing.T) {
	runLastAdminConcurrent(t, true)
}

// runLastAdminConcurrent (#259 AC-13): of two competing changes that would
// together remove both admins exactly one commits; the other is refused with
// 403 FORBIDDEN (its actor was the one changed) or 409 LAST_ADMIN and writes
// nothing.
func runLastAdminConcurrent(t *testing.T, useMySQL bool) {
	// The subtests each open their own schema and would skip one by one,
	// leaving the root reported as passed; skip the root itself instead.
	if useMySQL && os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	demoteBody := map[string]interface{}{"role": "sales"}

	for _, scenario := range []struct {
		name string
		// requests builds the pair for admins x and y. The first request is
		// always a demotion, which writes one audit row when it commits.
		requests func(x, y *models.User) []raceRequest
		// outcomes are the committed states of x and y when the first request
		// won, and when the second did.
		firstWon, secondWon [2]string
		// secondAudits is the audit rows the second request writes when it
		// commits: one when it is a demotion too.
		secondAudits int64
	}{
		{
			name: "mutual demotion",
			requests: func(x, y *models.User) []raceRequest {
				return []raceRequest{
					{x, http.MethodPut, userRoutePath(y.ID), demoteBody},
					{y, http.MethodPut, userRoutePath(x.ID), demoteBody},
				}
			},
			firstWon:     [2]string{"role=admin role_id=null active=true erased=false", "role=sales role_id=null active=true erased=false"},
			secondWon:    [2]string{"role=sales role_id=null active=true erased=false", "role=admin role_id=null active=true erased=false"},
			secondAudits: 1,
		},
		{
			name: "demote against deactivate",
			requests: func(x, y *models.User) []raceRequest {
				return []raceRequest{
					{x, http.MethodPut, userRoutePath(y.ID), demoteBody},
					{y, http.MethodPost, userRoutePath(x.ID) + "/deactivate", nil},
				}
			},
			firstWon:  [2]string{"role=admin role_id=null active=true erased=false", "role=sales role_id=null active=true erased=false"},
			secondWon: [2]string{"role=admin role_id=null active=false erased=false", "role=admin role_id=null active=true erased=false"},
		},
		{
			name: "demote against erase",
			requests: func(x, y *models.User) []raceRequest {
				return []raceRequest{
					{x, http.MethodPut, userRoutePath(y.ID), demoteBody},
					{y, http.MethodDelete, userRoutePath(x.ID), nil},
				}
			},
			firstWon:  [2]string{"role=admin role_id=null active=true erased=false", "role=sales role_id=null active=true erased=false"},
			secondWon: [2]string{"role=admin role_id=null active=false erased=true", "role=admin role_id=null active=true erased=false"},
		},
		{
			name: "self-demotion against self-deactivation",
			requests: func(x, y *models.User) []raceRequest {
				return []raceRequest{
					{x, http.MethodPut, userRoutePath(x.ID), demoteBody},
					{y, http.MethodPut, userRoutePath(y.ID), map[string]interface{}{"is_active": false}},
				}
			},
			firstWon:  [2]string{"role=sales role_id=null active=true erased=false", "role=admin role_id=null active=true erased=false"},
			secondWon: [2]string{"role=admin role_id=null active=true erased=false", "role=admin role_id=null active=false erased=false"},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			env := newPermissionEnv(t, useMySQL)
			x, y := env.user("x", models.RoleAdmin), env.user("y", models.RoleAdmin)
			require.EqualValues(t, 2, env.activeAdmins())
			audits := env.auditCount()

			answers := env.race(useMySQL, scenario.requests(x, y)...)

			var won, lost []int
			for i, answer := range answers {
				if answer.Status >= 200 && answer.Status <= 299 {
					won = append(won, i)
				} else {
					lost = append(lost, i)
				}
			}
			require.Len(t, won, 1, "exactly one change commits: %s / %s", statusText(answers[0]), statusText(answers[1]))
			loser := answers[lost[0]]
			refusal := fmt.Sprintf("%d %s", loser.Status, loser.Code)
			assert.Contains(t, []string{"403 FORBIDDEN", "409 LAST_ADMIN"}, refusal, loser.Raw)

			// The committed rows: the winner's change and nothing of the loser's.
			want, wantAudits := scenario.firstWon, audits+1
			if won[0] == 1 {
				want, wantAudits = scenario.secondWon, audits+scenario.secondAudits
			}
			assert.Contains(t, env.userState(x.ID), want[0], "x")
			assert.Contains(t, env.userState(y.ID), want[1], "y")
			assert.Equal(t, wantAudits, env.auditCount(), "only a committed role change is audited")
			assert.EqualValues(t, 1, env.activeAdmins(), "one active admin remains")
		})
	}
}

// --- API -------------------------------------------------------------------------

// TestLastAdminAPISQLite (#259 AC-14): the answers of the user routes with
// exactly one active admin. Every refused caller holds the route's key and
// sends an otherwise valid request, so no refusal is the guard's.
func TestLastAdminAPISQLite(t *testing.T) {
	env := newPermissionEnv(t, false)
	admin := env.user("admin", models.RoleAdmin)
	full := env.customUser("full", env.role("Full", adminMatrixGrants(t)...))
	activator := env.customUser("activator", env.role("Activator", "users.activate"))
	eraser := env.customUser("eraser", env.role("Eraser", "users.delete"))
	require.EqualValues(t, 1, env.activeAdmins())

	target := userRoutePath(admin.ID)
	for _, c := range []struct {
		name   string
		as     *models.User
		method string
		path   string
		body   interface{}
		status int
		code   string
	}{
		{"the admin demotes itself", admin, http.MethodPut, target, map[string]interface{}{"role": "sales"}, http.StatusConflict, "LAST_ADMIN"},
		{"the admin deactivates itself through the update", admin, http.MethodPut, target, map[string]interface{}{"is_active": false}, http.StatusConflict, "LAST_ADMIN"},
		{"a full custom role deactivates the admin", full, http.MethodPost, target + "/deactivate", nil, http.StatusConflict, "LAST_ADMIN"},
		{"a full custom role erases the admin", full, http.MethodDelete, target, nil, http.StatusConflict, "LAST_ADMIN"},
		{"the admin deactivates itself", admin, http.MethodPost, target + "/deactivate", nil, http.StatusBadRequest, "BAD_REQUEST"},
		{"the admin erases itself", admin, http.MethodDelete, target, nil, http.StatusBadRequest, "BAD_REQUEST"},
		{"a role holding only users.activate deactivates the admin", activator, http.MethodPost, target + "/deactivate", nil, http.StatusForbidden, "ROLE_ESCALATION"},
		{"a role holding only users.delete erases the admin", eraser, http.MethodDelete, target, nil, http.StatusForbidden, "ROLE_ESCALATION"},
	} {
		t.Run(c.name, func(t *testing.T) {
			state, audits := env.userState(admin.ID), env.auditCount()
			response := env.do(c.as, c.method, c.path, c.body)
			requireAnswer(t, response, c.status, c.code)
			assert.Equal(t, state, env.userState(admin.ID), "the target's row is unchanged")
			assert.Equal(t, audits, env.auditCount(), "no audit row")
			assert.EqualValues(t, 1, env.activeAdmins())
		})
	}

	t.Run("with a second active admin the same requests succeed", func(t *testing.T) {
		second := env.user("second", models.RoleAdmin)
		demoted := env.do(admin, http.MethodPut, target, map[string]interface{}{"role": "sales"})
		requireAnswer(t, demoted, http.StatusOK, "")
		assert.Equal(t, "sales", decodeUser(t, demoted).Role)

		third := env.user("third", models.RoleAdmin)
		deactivated := env.do(full, http.MethodPost, userRoutePath(second.ID)+"/deactivate", nil)
		requireAnswer(t, deactivated, http.StatusOK, "")

		env.user("fourth", models.RoleAdmin)
		erased := env.do(full, http.MethodDelete, userRoutePath(third.ID), nil)
		requireAnswer(t, erased, http.StatusNoContent, "")
		assert.EqualValues(t, 1, env.activeAdmins())
	})
}
