package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Outbound webhook storage (#345) on the full production schema: SQLite, and a
// disposable MySQL schema on TEST_MYSQL_DSN (skipped without it, failing when
// the server is unreachable).

func TestWebhookRepositorySQLite(t *testing.T) {
	runWebhookRepository(t, openIdentityErasureDB(t, false))
}

func TestWebhookRepositoryMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runWebhookRepository(t, openIdentityErasureDB(t, true))
}

var webhookEventSeq atomic.Int64

func nextWebhookEventID() string {
	return fmt.Sprintf("evt_%032x", webhookEventSeq.Add(1))
}

func seedWebhookEndpoint(t *testing.T, repo WebhookRepository, active bool, types ...string) *models.WebhookEndpoint {
	t.Helper()
	endpoint := &models.WebhookEndpoint{URL: "https://hooks.example.com/" + nextWebhookEventID(), EventTypes: models.CanonicalWebhookEventTypes(types),
		SecretSealed: "enc:v1:test", IsActive: active}
	require.NoError(t, repo.CreateEndpoint(context.Background(), endpoint))
	return endpoint
}

type webhookRowSpec struct {
	status   string
	attempts int
	next     *time.Time
	lease    *time.Time
	updated  time.Time
}

func seedWebhookDelivery(t *testing.T, db *gorm.DB, endpointID uint, spec webhookRowSpec) *models.WebhookDelivery {
	t.Helper()
	occurred := time.Now().UTC().Truncate(time.Millisecond)
	row := &models.WebhookDelivery{EndpointID: endpointID, EventID: nextWebhookEventID(), EventType: models.WebhookEventLeadCreated,
		RecordID: 1, OccurredAt: occurred, Status: spec.status, Attempts: spec.attempts, NextAttemptAt: spec.next, LeaseUntil: spec.lease}
	if !spec.updated.IsZero() {
		row.CreatedAt = spec.updated
		row.UpdatedAt = spec.updated
	}
	require.NoError(t, db.Create(row).Error)
	return row
}

func webhookRow(t *testing.T, db *gorm.DB, id uint) models.WebhookDelivery {
	t.Helper()
	var row models.WebhookDelivery
	require.NoError(t, db.Where("id = ?", id).Take(&row).Error)
	return row
}

func timePtr(t time.Time) *time.Time { return &t }

// captureWebhookStatements records the SQL of every query, update and delete
// against webhook_deliveries while the returned stop function has not run.
func captureWebhookStatements(t *testing.T, db *gorm.DB, name string) (func() []string, func()) {
	t.Helper()
	var mu sync.Mutex
	var statements []string
	record := func(tx *gorm.DB) {
		if tx.Statement.Table != "webhook_deliveries" {
			return
		}
		mu.Lock()
		statements = append(statements, tx.Statement.SQL.String())
		mu.Unlock()
	}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name+":query", record))
	// Scan, Pluck-style and Rows reads go through the Row chain, not Query.
	require.NoError(t, db.Callback().Row().After("gorm:row").Register(name+":row", record))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(name+":update", record))
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register(name+":delete", record))
	stop := func() {
		_ = db.Callback().Query().Remove(name + ":query")
		_ = db.Callback().Row().Remove(name + ":row")
		_ = db.Callback().Update().Remove(name + ":update")
		_ = db.Callback().Delete().Remove(name + ":delete")
	}
	t.Cleanup(stop)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), statements...)
	}, stop
}

func assertNoLockingClause(t *testing.T, statement string) {
	t.Helper()
	upper := strings.ToUpper(statement)
	assert.NotContains(t, upper, "FOR UPDATE", statement)
	assert.NotContains(t, upper, "FOR SHARE", statement)
	assert.NotContains(t, upper, "LOCK IN SHARE MODE", statement)
}

func runWebhookRepository(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	repo := NewWebhookRepository(db)

	t.Run("endpoints", func(t *testing.T) {
		lead := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadCreated)
		both := seedWebhookEndpoint(t, repo, true, models.WebhookEventTicketCreated, models.WebhookEventLeadCreated)
		inactive := seedWebhookEndpoint(t, repo, false, models.WebhookEventLeadCreated)
		ticket := seedWebhookEndpoint(t, repo, true, models.WebhookEventTicketCreated)

		got, err := repo.GetEndpoint(ctx, inactive.ID)
		require.NoError(t, err)
		assert.False(t, got.IsActive, "an explicit false is stored")
		_, err = repo.GetEndpoint(ctx, 99999999)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

		active, err := repo.ListActiveEndpointsFor(ctx, models.WebhookEventLeadCreated)
		require.NoError(t, err)
		ids := []uint{}
		for _, endpoint := range active {
			ids = append(ids, endpoint.ID)
		}
		assert.Contains(t, ids, lead.ID)
		assert.Contains(t, ids, both.ID)
		assert.NotContains(t, ids, inactive.ID, "inactive endpoints get nothing")
		assert.NotContains(t, ids, ticket.ID, "unsubscribed endpoints get nothing")

		require.NoError(t, repo.UpdateEndpoint(ctx, lead.ID, map[string]interface{}{"is_active": false}))
		got, err = repo.GetEndpoint(ctx, lead.ID)
		require.NoError(t, err)
		assert.False(t, got.IsActive)
		require.NoError(t, repo.UpdateEndpoint(ctx, lead.ID, map[string]interface{}{"is_active": false}), "an identical write is not a miss")
		assert.ErrorIs(t, repo.UpdateEndpoint(ctx, 99999999, map[string]interface{}{"is_active": true}), gorm.ErrRecordNotFound)
		require.Error(t, repo.UpdateEndpoint(ctx, lead.ID, map[string]interface{}{"secret_sealed": "x"}), "the secret is never updated")

		page, total, err := repo.ListEndpoints(ctx, 0, 2, "", "")
		require.NoError(t, err)
		assert.GreaterOrEqual(t, total, int64(4))
		require.Len(t, page, 2)
		assert.Less(t, page[0].ID, page[1].ID, "id ascending without a sort")
		page, _, err = repo.ListEndpoints(ctx, 0, 100, "id", "desc")
		require.NoError(t, err)
		assert.Greater(t, page[0].ID, page[len(page)-1].ID)
		_, _, err = repo.ListEndpoints(ctx, 0, 10, "url", "asc")
		require.Error(t, err, "only allowlisted sort columns")

		t.Run("delete removes the endpoint and its deliveries only", func(t *testing.T) {
			other := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadCreated)
			seedWebhookDelivery(t, db, both.ID, webhookRowSpec{status: models.WebhookDeliveryPending, next: timePtr(time.Now().UTC())})
			seedWebhookDelivery(t, db, both.ID, webhookRowSpec{status: models.WebhookDeliverySucceeded})
			kept := seedWebhookDelivery(t, db, other.ID, webhookRowSpec{status: models.WebhookDeliveryPending, next: timePtr(time.Now().UTC())})

			require.NoError(t, repo.DeleteEndpoint(ctx, both.ID))
			_, err := repo.GetEndpoint(ctx, both.ID)
			assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
			var left int64
			require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("endpoint_id = ?", both.ID).Count(&left).Error)
			assert.Zero(t, left)
			webhookRow(t, db, kept.ID)
			assert.ErrorIs(t, repo.DeleteEndpoint(ctx, both.ID), gorm.ErrRecordNotFound)

			// Under WithTx a caller's rollback keeps everything.
			victim := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadCreated)
			row := seedWebhookDelivery(t, db, victim.ID, webhookRowSpec{status: models.WebhookDeliveryFailed})
			rollback := errors.New("caller rolls back")
			err = db.Transaction(func(tx *gorm.DB) error {
				if err := repo.WithTx(tx).DeleteEndpoint(ctx, victim.ID); err != nil {
					return err
				}
				return rollback
			})
			require.ErrorIs(t, err, rollback)
			_, err = repo.GetEndpoint(ctx, victim.ID)
			require.NoError(t, err)
			webhookRow(t, db, row.ID)
		})
	})

	endpoint := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadCreated)

	t.Run("enqueue, discovery and claim", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		occurred := now.Add(-time.Second)
		event := nextWebhookEventID()
		rows := []models.WebhookDelivery{{EndpointID: endpoint.ID, EventID: event, EventType: models.WebhookEventLeadCreated, RecordID: 5,
			OccurredAt: occurred, Status: models.WebhookDeliveryPending, NextAttemptAt: timePtr(occurred)}}
		require.NoError(t, repo.EnqueueDeliveries(ctx, rows))
		require.NoError(t, repo.EnqueueDeliveries(ctx, nil), "nothing to enqueue is a no-op")
		var due models.WebhookDelivery
		require.NoError(t, db.Where("event_id = ?", event).Take(&due).Error)
		later := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliveryPending, next: timePtr(now.Add(time.Hour))})

		statements, stop := captureWebhookStatements(t, db, "webhook_repo_discovery")
		ids, err := repo.ListDueDeliveryIDs(ctx, now, 100)
		require.NoError(t, err)
		stop()
		assert.Contains(t, ids, due.ID)
		assert.NotContains(t, ids, later.ID, "a row not due yet is not discovered")
		captured := statements()
		require.NotEmpty(t, captured)
		for _, statement := range captured {
			assertNoLockingClause(t, statement)
		}

		lease := now.Add(2 * time.Minute)
		claimed, err := repo.ClaimDelivery(ctx, due.ID, now, lease)
		require.NoError(t, err)
		assert.Equal(t, models.WebhookDeliverySending, claimed.Status)
		assert.Equal(t, 1, claimed.Attempts)
		require.NotNil(t, claimed.LastAttemptAt)
		require.NotNil(t, claimed.LeaseUntil)
		assert.WithinDuration(t, lease, *claimed.LeaseUntil, time.Millisecond)

		_, err = repo.ClaimDelivery(ctx, due.ID, now, lease)
		assert.ErrorIs(t, err, ErrWebhookDeliveryNotClaimed, "a sending row is not claimed twice")
		_, err = repo.ClaimDelivery(ctx, later.ID, now, lease)
		assert.ErrorIs(t, err, ErrWebhookDeliveryNotClaimed, "a row not due is not claimed")

		t.Run("finish is fenced on sending and the claimed attempts", func(t *testing.T) {
			status := 200
			outcome := WebhookDeliveryOutcome{Status: models.WebhookDeliverySucceeded, LastHTTPStatus: &status, Now: now}
			assert.ErrorIs(t, repo.FinishDelivery(ctx, due.ID, 2, outcome), ErrWebhookDeliveryNotSending, "another attempt's fence")
			require.NoError(t, repo.FinishDelivery(ctx, due.ID, 1, outcome))
			row := webhookRow(t, db, due.ID)
			assert.Equal(t, models.WebhookDeliverySucceeded, row.Status)
			assert.Nil(t, row.NextAttemptAt)
			assert.Nil(t, row.LeaseUntil)
			require.NotNil(t, row.LastHTTPStatus)
			assert.Equal(t, 200, *row.LastHTTPStatus)
			assert.Equal(t, "", row.ErrorCode)
			failed := WebhookDeliveryOutcome{Status: models.WebhookDeliveryFailed, ErrorCode: models.WebhookErrorTimeout, Now: now}
			assert.ErrorIs(t, repo.FinishDelivery(ctx, due.ID, 1, failed), ErrWebhookDeliveryNotSending, "a finished row is never rewritten")
			assert.Equal(t, models.WebhookDeliverySucceeded, webhookRow(t, db, due.ID).Status)
			require.Error(t, repo.FinishDelivery(ctx, due.ID, 1, WebhookDeliveryOutcome{Status: models.WebhookDeliverySending, Now: now}))
		})
	})

	t.Run("two concurrent claims of one row commit exactly one", func(t *testing.T) {
		for round := 0; round < 5; round++ {
			now := time.Now().UTC()
			row := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliveryPending, next: timePtr(now.Add(-time.Second))})
			var wins, losses atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, err := repo.ClaimDelivery(ctx, row.ID, now, now.Add(time.Minute))
					switch {
					case err == nil:
						wins.Add(1)
					case errors.Is(err, ErrWebhookDeliveryNotClaimed):
						losses.Add(1)
					default:
						t.Errorf("unexpected claim error: %v", err)
					}
				}()
			}
			close(start)
			wg.Wait()
			assert.EqualValues(t, 1, wins.Load(), "round %d", round)
			assert.EqualValues(t, 1, losses.Load(), "round %d", round)
			stored := webhookRow(t, db, row.ID)
			assert.Equal(t, 1, stored.Attempts, "the row was incremented once")
			assert.Equal(t, models.WebhookDeliverySending, stored.Status)
		}
	})

	t.Run("expired leases", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		expired := timePtr(now.Add(-time.Minute))
		live := timePtr(now.Add(time.Minute))
		retry := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliverySending, attempts: 3, lease: expired})
		capped := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliverySending, attempts: models.WebhookMaxAttempts, lease: expired})
		holding := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliverySending, attempts: 2, lease: live})
		reclaimed := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliverySending, attempts: 4, lease: expired})
		finished := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliverySending, attempts: 5, lease: expired})

		// Between the plain discovery and the per-row updates, another sender
		// re-claims one row and finishes another.
		name := "webhook_repo:interleave"
		var fired atomic.Bool
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table != "webhook_deliveries" || !fired.CompareAndSwap(false, true) {
				return
			}
			// The statement's own connection (its implicit transaction once
			// begun), so the one-connection SQLite pool cannot deadlock; the
			// conditional updates that follow see these writes either way.
			other := tx.Session(&gorm.Session{NewDB: true})
			require.NoError(t, other.Exec("UPDATE webhook_deliveries SET attempts = attempts + 1, lease_until = ? WHERE id = ?", now.Add(time.Minute), reclaimed.ID).Error)
			require.NoError(t, other.Exec("UPDATE webhook_deliveries SET status = ?, lease_until = NULL WHERE id = ?", models.WebhookDeliverySucceeded, finished.ID).Error)
		}))
		t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })

		statements, stop := captureWebhookStatements(t, db, "webhook_repo_recover")
		recovered, failed, err := repo.RecoverExpiredLeases(ctx, now, models.WebhookMaxAttempts)
		stop()
		require.NoError(t, db.Callback().Update().Remove(name))
		require.NoError(t, err)
		require.True(t, fired.Load())
		assert.EqualValues(t, 1, recovered)
		assert.EqualValues(t, 1, failed)

		row := webhookRow(t, db, retry.ID)
		assert.Equal(t, models.WebhookDeliveryPending, row.Status, "back to pending below the cap")
		assert.Equal(t, 3, row.Attempts, "the attempt count is kept")
		assert.Nil(t, row.LeaseUntil)
		require.NotNil(t, row.NextAttemptAt)
		row = webhookRow(t, db, capped.ID)
		assert.Equal(t, models.WebhookDeliveryFailed, row.Status, "failed at the cap")
		assert.Equal(t, models.WebhookErrorLeaseExpired, row.ErrorCode)
		assert.Nil(t, row.NextAttemptAt)
		assert.Equal(t, models.WebhookDeliverySending, webhookRow(t, db, holding.ID).Status, "a live lease is untouched")
		row = webhookRow(t, db, reclaimed.ID)
		assert.Equal(t, models.WebhookDeliverySending, row.Status, "a row re-claimed since discovery is untouched")
		assert.Equal(t, 5, row.Attempts)
		assert.Equal(t, models.WebhookDeliverySucceeded, webhookRow(t, db, finished.ID).Status, "a row finished since discovery is untouched")

		captured := statements()
		require.NotEmpty(t, captured)
		assertNoLockingClause(t, captured[0])
		assert.True(t, strings.HasPrefix(strings.ToUpper(strings.TrimSpace(captured[0])), "SELECT"), "discovery first: %s", captured[0])
		for _, statement := range captured[1:] {
			assert.Contains(t, statement, "id =", "every write is by primary key: %s", statement)
		}
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("id IN ?", []uint{holding.ID, reclaimed.ID}).
			Updates(map[string]interface{}{"status": models.WebhookDeliveryFailed, "lease_until": nil}).Error)
	})

	t.Run("prune removes old finished rows in chunks of 500", func(t *testing.T) {
		pruneEndpoint := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadCreated)
		now := time.Now().UTC().Truncate(time.Millisecond)
		old := now.Add(-31 * 24 * time.Hour)
		cutoff := now.Add(-30 * 24 * time.Hour)
		batch := make([]models.WebhookDelivery, 0, 1201)
		for i := 0; i < 1201; i++ {
			status := models.WebhookDeliverySucceeded
			if i%2 == 1 {
				status = models.WebhookDeliveryFailed
			}
			batch = append(batch, models.WebhookDelivery{EndpointID: pruneEndpoint.ID, EventID: nextWebhookEventID(),
				EventType: models.WebhookEventLeadCreated, RecordID: uint(i + 1), OccurredAt: old, Status: status, CreatedAt: old, UpdatedAt: old})
		}
		require.NoError(t, db.CreateInBatches(&batch, 200).Error)
		oldPending := seedWebhookDelivery(t, db, pruneEndpoint.ID, webhookRowSpec{status: models.WebhookDeliveryPending, next: timePtr(old), updated: old})
		oldSending := seedWebhookDelivery(t, db, pruneEndpoint.ID, webhookRowSpec{status: models.WebhookDeliverySending, attempts: 1, lease: timePtr(now.Add(time.Minute)), updated: old})
		recent := seedWebhookDelivery(t, db, pruneEndpoint.ID, webhookRowSpec{status: models.WebhookDeliverySucceeded, updated: now.Add(-time.Hour)})

		statements, stop := captureWebhookStatements(t, db, "webhook_repo_prune")
		removed, err := repo.PruneFinished(ctx, cutoff)
		stop()
		require.NoError(t, err)
		assert.EqualValues(t, 1201, removed)

		deletes, selects := 0, 0
		for _, statement := range statements() {
			upper := strings.ToUpper(strings.TrimSpace(statement))
			switch {
			case strings.HasPrefix(upper, "DELETE"):
				deletes++
				assert.Contains(t, statement, "id IN", "by primary key: %s", statement)
			case strings.HasPrefix(upper, "SELECT"):
				selects++
				assertNoLockingClause(t, statement)
			}
		}
		assert.Equal(t, 3, deletes, "1201 rows are deleted in chunks of 500")
		assert.Equal(t, 1, selects, "one plain discovery")

		var left int64
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("endpoint_id = ?", pruneEndpoint.ID).Count(&left).Error)
		assert.EqualValues(t, 3, left)
		assert.Equal(t, models.WebhookDeliveryPending, webhookRow(t, db, oldPending.ID).Status, "pending rows are never pruned")
		assert.Equal(t, models.WebhookDeliverySending, webhookRow(t, db, oldSending.ID).Status, "sending rows are never pruned")
		webhookRow(t, db, recent.ID)
		removed, err = repo.PruneFinished(ctx, cutoff)
		require.NoError(t, err)
		assert.Zero(t, removed)
	})

	t.Run("delivery log: newest first, filters and pagination", func(t *testing.T) {
		logEndpoint := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadCreated, models.WebhookEventTicketCreated)
		var ids []uint
		for i := 0; i < 5; i++ {
			status := models.WebhookDeliverySucceeded
			if i%2 == 0 {
				status = models.WebhookDeliveryFailed
			}
			row := seedWebhookDelivery(t, db, logEndpoint.ID, webhookRowSpec{status: status})
			ids = append(ids, row.ID)
		}
		require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("id = ?", ids[4]).Update("event_type", models.WebhookEventTicketCreated).Error)

		page, total, err := repo.ListDeliveries(ctx, WebhookDeliveryFilter{EndpointID: logEndpoint.ID}, 0, 2)
		require.NoError(t, err)
		assert.EqualValues(t, 5, total)
		require.Len(t, page, 2)
		assert.Equal(t, ids[4], page[0].ID, "newest first")
		assert.Equal(t, ids[3], page[1].ID)
		page, _, err = repo.ListDeliveries(ctx, WebhookDeliveryFilter{EndpointID: logEndpoint.ID}, 4, 2)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, ids[0], page[0].ID)

		page, total, err = repo.ListDeliveries(ctx, WebhookDeliveryFilter{EndpointID: logEndpoint.ID, Status: models.WebhookDeliveryFailed}, 0, 10)
		require.NoError(t, err)
		assert.EqualValues(t, 3, total)
		for _, row := range page {
			assert.Equal(t, models.WebhookDeliveryFailed, row.Status)
		}
		page, total, err = repo.ListDeliveries(ctx, WebhookDeliveryFilter{EndpointID: logEndpoint.ID, EventType: models.WebhookEventTicketCreated}, 0, 10)
		require.NoError(t, err)
		assert.EqualValues(t, 1, total)
		require.Len(t, page, 1)
		assert.Equal(t, ids[4], page[0].ID)
	})

	t.Run("announced records (#437)", func(t *testing.T) {
		first := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadDeleted)
		second := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadDeleted)
		other := seedWebhookEndpoint(t, repo, true, models.WebhookEventLeadDeleted)
		const lead, otherLead, customer = 9001, 9002, 9003
		seed := func(endpointID uint, eventType string, recordID uint, status string) {
			t.Helper()
			row := seedWebhookDelivery(t, db, endpointID, webhookRowSpec{status: status})
			require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("id = ?", row.ID).
				Updates(map[string]interface{}{"event_type": eventType, "record_id": recordID}).Error)
		}
		// first announced the lead twice (created, then updated, finished);
		// second announced it once, failed; other only announced another lead;
		// a lead.created row for the customer's id is not the customer's.
		seed(first.ID, models.WebhookEventLeadCreated, lead, models.WebhookDeliverySucceeded)
		seed(first.ID, models.WebhookEventLeadUpdated, lead, models.WebhookDeliveryPending)
		seed(second.ID, models.WebhookEventLeadUpdated, lead, models.WebhookDeliveryFailed)
		seed(other.ID, models.WebhookEventLeadCreated, otherLead, models.WebhookDeliveryPending)
		seed(first.ID, models.WebhookEventLeadCreated, customer, models.WebhookDeliveryPending)
		seed(second.ID, models.WebhookEventTicketCreated, lead, models.WebhookDeliveryPending)

		leadTypes := []string{models.WebhookEventLeadCreated, models.WebhookEventLeadUpdated}
		customerTypes := []string{models.WebhookEventCustomerCreated, models.WebhookEventCustomerUpdated}
		endpoints := []uint{first.ID, second.ID}

		statements, stop := captureWebhookStatements(t, db, "webhook_repo_announced")
		got, err := repo.ListAnnouncedRecords(ctx, endpoints, leadTypes, []uint{lead, otherLead, customer})
		require.NoError(t, err)
		stop()
		assert.Equal(t, map[uint][]uint{lead: {first.ID, second.ID}, customer: {first.ID}}, got,
			"one entry per record, endpoints ascending, another endpoint's row ignored")
		captured := statements()
		require.Len(t, captured, 1, "one statement")
		assertNoLockingClause(t, captured[0])

		got, err = repo.ListAnnouncedRecords(ctx, endpoints, customerTypes, []uint{lead, customer})
		require.NoError(t, err)
		assert.Empty(t, got, "a lead type does not announce a customer")

		got, err = repo.ListAnnouncedRecords(ctx, []uint{other.ID}, leadTypes, []uint{lead})
		require.NoError(t, err)
		assert.Empty(t, got, "another record does not match")

		statements, stop = captureWebhookStatements(t, db, "webhook_repo_announced_empty")
		for _, call := range [][3]int{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}} {
			ids := func(n int) []uint { return []uint{lead}[:n] }
			types := leadTypes[:call[1]]
			got, err = repo.ListAnnouncedRecords(ctx, []uint{first.ID}[:call[0]], types, ids(call[2]))
			require.NoError(t, err)
			assert.Empty(t, got)
		}
		stop()
		assert.Empty(t, statements(), "empty input reads nothing")
	})

	t.Run("requeue of a finished row (#502)", func(t *testing.T) {
		seeded := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
		attempted := seeded.Add(-time.Minute)
		finished := func(status string, attempts int, httpStatus *int, code string) *models.WebhookDelivery {
			row := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: status, attempts: attempts, updated: seeded})
			require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("id = ?", row.ID).UpdateColumns(map[string]interface{}{
				"last_attempt_at": attempted, "last_http_status": httpStatus, "error_code": code,
			}).Error)
			stored := webhookRow(t, db, row.ID)
			return &stored
		}
		assertRequeued := func(t *testing.T, before *models.WebhookDelivery, now time.Time) {
			t.Helper()
			row := webhookRow(t, db, before.ID)
			assert.Equal(t, models.WebhookDeliveryPending, row.Status)
			assert.Equal(t, 0, row.Attempts)
			require.NotNil(t, row.NextAttemptAt)
			assert.WithinDuration(t, now, *row.NextAttemptAt, time.Second, "due now")
			assert.Nil(t, row.LeaseUntil)
			assert.Nil(t, row.LastHTTPStatus)
			assert.Equal(t, "", row.ErrorCode)
			assert.Equal(t, before.EventID, row.EventID, "the same event id")
			assert.Equal(t, before.RecordID, row.RecordID)
			assert.Equal(t, before.EndpointID, row.EndpointID)
			require.NotNil(t, row.LastAttemptAt, "last_attempt_at keeps its value")
			assert.WithinDuration(t, *before.LastAttemptAt, *row.LastAttemptAt, time.Second)
			assert.True(t, row.UpdatedAt.After(before.UpdatedAt), "updated_at moves forward, so a concurrent prune pass leaves the row alone")
		}

		t.Run("a failed row is pending again, attempts and error fields cleared", func(t *testing.T) {
			status := 503
			before := finished(models.WebhookDeliveryFailed, models.WebhookMaxAttempts, &status, models.WebhookErrorHTTPStatus)
			now := time.Now().UTC()
			statements, stop := captureWebhookStatements(t, db, "webhook_repo_requeue")
			require.NoError(t, repo.RequeueDelivery(ctx, before.ID, now))
			stop()
			assertRequeued(t, before, now)
			captured := statements()
			require.Len(t, captured, 1, "one statement: %v", captured)
			upper := strings.ToUpper(captured[0])
			assert.True(t, strings.HasPrefix(strings.TrimSpace(upper), "UPDATE"), captured[0])
			assert.Contains(t, captured[0], "id =", "by primary key: %s", captured[0])
			assert.Contains(t, upper, "STATUS IN", "the status predicate is in the statement: %s", captured[0])
			assertNoLockingClause(t, captured[0])
		})

		t.Run("a succeeded row is pending again", func(t *testing.T) {
			status := 200
			before := finished(models.WebhookDeliverySucceeded, 1, &status, "")
			now := time.Now().UTC()
			require.NoError(t, repo.RequeueDelivery(ctx, before.ID, now))
			assertRequeued(t, before, now)
		})

		t.Run("pending, sending and missing rows are not requeued and stay byte-identical", func(t *testing.T) {
			now := time.Now().UTC()
			pending := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliveryPending, attempts: 2,
				next: timePtr(now.Add(time.Minute).Truncate(time.Second)), updated: seeded})
			sending := seedWebhookDelivery(t, db, endpoint.ID, webhookRowSpec{status: models.WebhookDeliverySending, attempts: 3,
				lease: timePtr(now.Add(time.Minute).Truncate(time.Second)), updated: seeded})
			for _, id := range []uint{pending.ID, sending.ID} {
				before := webhookRow(t, db, id)
				assert.ErrorIs(t, repo.RequeueDelivery(ctx, id, now), ErrWebhookDeliveryNotRequeued)
				assert.Equal(t, before, webhookRow(t, db, id), "the row is unchanged")
			}
			var maxID uint
			require.NoError(t, db.Model(&models.WebhookDelivery{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error)
			assert.ErrorIs(t, repo.RequeueDelivery(ctx, maxID+1000, now), ErrWebhookDeliveryNotRequeued)
			require.NoError(t, db.Model(&models.WebhookDelivery{}).Where("id IN ?", []uint{pending.ID, sending.ID}).
				Updates(map[string]interface{}{"status": models.WebhookDeliveryFailed, "lease_until": nil, "next_attempt_at": nil}).Error)
		})
	})
}
