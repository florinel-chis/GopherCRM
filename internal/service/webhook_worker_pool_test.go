package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// The sender pool (#568): a pass hands each endpoint's due rows to one
// worker, sorted by id, and that worker sends them one at a time, so a slow
// endpoint holds only its own worker. On SQLite and on a disposable
// TEST_MYSQL_DSN schema (the MySQL root skips without the variable and fails
// when it is unreachable). Every subtest runs on a fixed clock and sets every
// row's next_attempt_at itself, so the discovery order is the test's.

func TestWebhookWorkerPoolSQLite(t *testing.T) {
	runWebhookWorkerPool(t, openTicketIntakeDB(t, false))
}

func TestWebhookWorkerPoolMySQL(t *testing.T) {
	runWebhookWorkerPool(t, openTicketIntakeDB(t, true))
}

// seedPoolDelivery writes one pending delivery of endpointID and then sets
// its next_attempt_at to next with a direct update.
func seedPoolDelivery(t *testing.T, env *webhookEnv, endpointID, recordID uint, next time.Time) models.WebhookDelivery {
	t.Helper()
	eventID, err := newWebhookEventID()
	require.NoError(t, err)
	occurred := next.Add(-time.Minute)
	row := &models.WebhookDelivery{EndpointID: endpointID, EventID: eventID, EventType: models.WebhookEventLeadCreated,
		RecordID: recordID, OccurredAt: occurred, Status: models.WebhookDeliveryPending, NextAttemptAt: &occurred}
	require.NoError(t, env.db.Create(row).Error)
	require.NoError(t, env.db.Model(&models.WebhookDelivery{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{"next_attempt_at": next.UTC()}).Error)
	return env.delivery(row.ID)
}

// joinPass waits, bounded, for a RunOnce started on a goroutine.
func joinPass(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}

func runWebhookWorkerPool(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	consumer := newWebhookConsumer(t)
	recordID := env.seedLead().ID
	now := time.Now().UTC().Truncate(time.Second)
	clock := WithWebhookClock(func() time.Time { return now })

	t.Run("a slow endpoint does not delay a healthy one", func(t *testing.T) {
		slow := env.endpoint(consumer.url("/slow"), true, models.WebhookEventLeadCreated)
		healthy := env.endpoint(consumer.url("/healthy"), true, models.WebhookEventLeadCreated)
		slowRow := seedPoolDelivery(t, env, slow.ID, recordID, now.Add(-2*time.Minute))
		healthyRow := seedPoolDelivery(t, env, healthy.ID, recordID, now.Add(-time.Minute))
		require.Less(t, slowRow.ID, healthyRow.ID, "the slow row is discovered first")

		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		var once sync.Once
		releaseAll := func() { once.Do(func() { close(release) }) }
		t.Cleanup(releaseAll)
		consumer.setHandler(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/slow" {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
			}
			w.WriteHeader(http.StatusNoContent)
		})
		defer consumer.setHandler(nil)

		rt := env.runtime(consumer.guard(nil), WithWebhookWorkers(2), clock)
		done := make(chan struct{})
		var result WebhookPassResult
		var passErr error
		go func() {
			defer close(done)
			result, passErr = rt.RunOnce(context.Background())
		}()
		// fail releases the slow handler and joins the pass before failing,
		// so a failed wait never leaves the pass running.
		fail := func(format string, args ...interface{}) {
			releaseAll()
			joinPass(t, done, "the pass")
			t.Fatalf(format, args...)
		}

		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			fail("the slow endpoint was never reached")
		}
		deadline := time.Now().Add(5 * time.Second)
		for env.delivery(healthyRow.ID).Status != models.WebhookDeliverySucceeded {
			if time.Now().After(deadline) {
				fail("the healthy endpoint waited behind the slow one")
			}
			time.Sleep(10 * time.Millisecond)
		}
		select {
		case <-release:
			t.Fatal("the slow handler was released before the healthy row was sent")
		default:
		}

		releaseAll()
		joinPass(t, done, "the pass")
		require.NoError(t, passErr)
		assert.Equal(t, 2, result.Attempted)
		assert.Equal(t, 2, result.Succeeded)
		assert.Equal(t, models.WebhookDeliverySucceeded, env.delivery(slowRow.ID).Status)
		assert.Equal(t, models.WebhookDeliverySucceeded, env.delivery(healthyRow.ID).Status)
	})

	t.Run("one endpoint's deliveries are sent in id order, one at a time", func(t *testing.T) {
		endpoint := env.endpoint(consumer.url("/ordered"), true, models.WebhookEventLeadCreated)
		d1 := seedPoolDelivery(t, env, endpoint.ID, recordID, now.Add(-time.Minute))
		d2 := seedPoolDelivery(t, env, endpoint.ID, recordID, now.Add(-2*time.Minute))
		d3 := seedPoolDelivery(t, env, endpoint.ID, recordID, now.Add(-2*time.Minute))
		require.Less(t, d1.ID, d2.ID)
		require.Less(t, d2.ID, d3.ID)

		discovered, err := env.repo.ListDueDeliveries(context.Background(), now, 100)
		require.NoError(t, err)
		var order []uint
		for _, row := range discovered {
			if row.EndpointID == endpoint.ID {
				order = append(order, row.ID)
			}
		}
		require.Equal(t, []uint{d2.ID, d3.ID, d1.ID}, order, "discovery order is not id order")

		var mu sync.Mutex
		inFlight, maxInFlight := 0, 0
		var received []string
		second := make(chan struct{}, 1)
		consumer.setHandler(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			received = append(received, r.Header.Get("webhook-id"))
			first := len(received) == 1
			mu.Unlock()
			if first {
				// A pool that split the endpoint across workers sends a second
				// request now; give it the chance to overlap.
				select {
				case <-second:
				case <-time.After(300 * time.Millisecond):
				}
			} else {
				select {
				case second <- struct{}{}:
				default:
				}
			}
			mu.Lock()
			inFlight--
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
		defer consumer.setHandler(nil)

		rt := env.runtime(consumer.guard(nil), WithWebhookWorkers(4), clock)
		result, err := rt.RunOnce(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 3, result.Succeeded)

		byEvent := map[string]uint{d1.EventID: d1.ID, d2.EventID: d2.ID, d3.EventID: d3.ID}
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, maxInFlight, "one endpoint is sent to one request at a time")
		var sent []uint
		for _, id := range received {
			sent = append(sent, byEvent[id])
		}
		assert.Equal(t, []uint{d1.ID, d2.ID, d3.ID}, sent, "an endpoint's rows go in id order")
	})

	t.Run("no delivery is sent twice under parallel workers", func(t *testing.T) {
		const endpoints, rows = 4, 5
		var ids []uint
		for e := 0; e < endpoints; e++ {
			endpoint := env.endpoint(consumer.url("/replica-"+string(rune('a'+e))), true, models.WebhookEventLeadCreated)
			for i := 0; i < rows; i++ {
				ids = append(ids, seedPoolDelivery(t, env, endpoint.ID, recordID, now.Add(-time.Minute)).ID)
			}
		}

		var mu sync.Mutex
		counts := map[string]int{}
		consumer.setHandler(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			counts[r.Header.Get("webhook-id")]++
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
		defer consumer.setHandler(nil)

		replicas := []*WebhookRuntime{
			env.runtime(consumer.guard(nil), WithWebhookWorkers(4), clock),
			env.runtime(consumer.guard(nil), WithWebhookWorkers(4), clock),
		}
		barrier := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, len(replicas))
		for i, rt := range replicas {
			wg.Add(1)
			go func(i int, rt *WebhookRuntime) {
				defer wg.Done()
				<-barrier
				_, errs[i] = rt.RunOnce(context.Background())
			}(i, rt)
		}
		close(barrier)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		joinPass(t, done, "the two replicas")
		for _, err := range errs {
			require.NoError(t, err)
		}

		mu.Lock()
		defer mu.Unlock()
		assert.Len(t, counts, endpoints*rows, "every delivery reached the consumer")
		for id, n := range counts {
			assert.Equal(t, 1, n, "webhook-id %s was sent once", id)
		}
		for _, id := range ids {
			row := env.delivery(id)
			assert.Equal(t, models.WebhookDeliverySucceeded, row.Status)
			assert.Equal(t, 1, row.Attempts, "one claim per row")
		}
	})
}
