package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Outbound webhook storage (#345).
//
// Lock discipline: the runtime discovers rows with plain, non-locking SELECTs
// (idx_webhook_deliveries_status_next and idx_webhook_deliveries_updated_at
// serve them) and writes every row by primary key with a conditional UPDATE or
// DELETE that re-checks the discovered state. No locking statement filters on
// a secondary index, so a sender never takes an index lock ahead of a primary
// key lock. Endpoint delete locks the endpoint row first and then deletes its
// delivery rows. MySQL reports changed rows, so a conditional write that
// reports zero is re-checked with a read of its own predicate before it is
// called a miss. A manual redelivery (RequeueDelivery, #502) is one such
// conditional UPDATE by primary key, fenced on status IN ('succeeded',
// 'failed'), with no locking read: it cannot overlap a claim, which needs
// status = 'pending'. A signing-secret rotation (RotateEndpointSecret, #503)
// is one UPDATE of the endpoint by primary key with no read before it, and
// the runtime's ClearExpiredPreviousSecret is a conditional UPDATE by primary
// key fenced on the expiry in the statement; neither adds a lock edge. The
// endpoint circuit (#569) adds two more conditional UPDATEs of
// webhook_endpoints by primary key, RecordEndpointFailure (fenced on the
// consecutive_failures the runtime read) and RecordEndpointSuccess (fenced on
// the endpoint not being healthy already), neither in a transaction nor after
// a locking read, and discovery leaves out paused endpoints with a plain
// subselect of webhook_endpoints; none of them adds a lock edge.

// WebhookPruneChunk bounds how many rows one prune DELETE removes.
const WebhookPruneChunk = 500

// ErrWebhookDeliveryNotClaimed is returned by ClaimDelivery when the row is no
// longer a due pending row: another claimer won, or it was finished or
// rescheduled since discovery.
var ErrWebhookDeliveryNotClaimed = errors.New("webhook delivery was not claimed")

// ErrWebhookDeliveryNotSending is returned by FinishDelivery when the row is
// no longer the sending row of the claimed attempt.
var ErrWebhookDeliveryNotSending = errors.New("webhook delivery is not sending under this claim")

// ErrWebhookDeliveryNotRequeued is returned by RequeueDelivery when no row
// with that id is succeeded or failed: it is pending or sending, or it does
// not exist.
var ErrWebhookDeliveryNotRequeued = errors.New("webhook delivery was not requeued")

type webhookRepository struct {
	db *gorm.DB
}

// NewWebhookRepository returns the outbound webhook repository.
func NewWebhookRepository(db *gorm.DB) WebhookRepository {
	return &webhookRepository{db: db}
}

func (r *webhookRepository) WithTx(tx *gorm.DB) WebhookRepository {
	return &webhookRepository{db: tx}
}

func (r *webhookRepository) CreateEndpoint(ctx context.Context, endpoint *models.WebhookEndpoint) error {
	return r.db.WithContext(ctx).Create(endpoint).Error
}

func (r *webhookRepository) GetEndpoint(ctx context.Context, id uint) (*models.WebhookEndpoint, error) {
	var endpoint models.WebhookEndpoint
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&endpoint).Error; err != nil {
		return nil, err
	}
	return &endpoint, nil
}

func (r *webhookRepository) ListEndpoints(ctx context.Context, offset, limit int, sortBy, sortOrder string) ([]models.WebhookEndpoint, int64, error) {
	db := r.db.WithContext(ctx)
	query, err := orderPage(db.Model(&models.WebhookEndpoint{}), "webhook_endpoints", sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := db.Model(&models.WebhookEndpoint{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	endpoints := []models.WebhookEndpoint{}
	if err := query.Offset(offset).Limit(limit).Find(&endpoints).Error; err != nil {
		return nil, 0, err
	}
	return endpoints, total, nil
}

// RecordEndpointFailure is one conditional UPDATE by primary key fenced on
// the failure count the caller read: a concurrent reset, another replica's
// failure or a deleted endpoint makes it match nothing, which is (false, nil)
// and not an error. It reports true only when exactly one row was updated
// (#586). It always changes consecutive_failures, so it is never an identical
// write and MySQL's default changed-rows count equals the matched rows.
// updated_at is left alone: the circuit is housekeeping of a send, not a change
// an admin made.
func (r *webhookRepository) RecordEndpointFailure(ctx context.Context, id uint, failuresBefore int, pausedUntil *time.Time) (bool, error) {
	var until interface{}
	if pausedUntil != nil {
		until = pausedUntil.UTC()
	}
	result := r.db.WithContext(ctx).Exec(
		"UPDATE webhook_endpoints SET consecutive_failures = consecutive_failures + 1, paused_until = ? WHERE id = ? AND consecutive_failures = ?",
		until, id, failuresBefore)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// RecordEndpointSuccess is one conditional UPDATE by primary key that resets
// the circuit only when it is not already reset, so a healthy endpoint is not
// rewritten on every success. updated_at is left alone.
func (r *webhookRepository) RecordEndpointSuccess(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Exec(
		"UPDATE webhook_endpoints SET consecutive_failures = 0, paused_until = NULL WHERE id = ? AND (consecutive_failures <> 0 OR paused_until IS NOT NULL)",
		id).Error
}

// webhookEndpointColumns is the closed set UpdateEndpoint writes.
var webhookEndpointColumns = map[string]bool{"url": true, "event_types": true, "is_active": true}

func (r *webhookRepository) UpdateEndpoint(ctx context.Context, id uint, columns map[string]interface{}) error {
	values := make(map[string]interface{}, len(columns)+1)
	for name, value := range columns {
		if !webhookEndpointColumns[name] {
			return fmt.Errorf("webhook endpoint column %q cannot be updated", name)
		}
		values[name] = value
	}
	values["updated_at"] = r.db.NowFunc().UTC()
	db := r.db.WithContext(ctx)
	result := db.Model(&models.WebhookEndpoint{}).Where("id = ?", id).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	// MySQL reports changed rows: an identical write affects zero.
	var count int64
	if err := db.Model(&models.WebhookEndpoint{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// RotateEndpointSecret is one UPDATE by primary key with no read before it:
// previous_secret_sealed takes the stored secret_sealed, then secret_sealed
// takes sealed. The assignment order is explicit (db.Exec) because MySQL
// evaluates SET left to right and reads the value an earlier assignment
// wrote; SQLite reads the old row for every assignment. Concurrent rotations
// serialise on the row lock, and each keeps the other's secret as the
// previous one.
func (r *webhookRepository) RotateEndpointSecret(ctx context.Context, id uint, sealed string, expiresAt, now time.Time) error {
	db := r.db.WithContext(ctx)
	result := db.Exec(
		"UPDATE webhook_endpoints SET previous_secret_sealed = secret_sealed, previous_secret_expires_at = ?, secret_sealed = ?, updated_at = ? WHERE id = ?",
		expiresAt.UTC(), sealed, now.UTC(), id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := db.Model(&models.WebhookEndpoint{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ClearExpiredPreviousSecret is one conditional UPDATE fenced on the expiry in
// the statement, so a rotation committed after the caller's read is never
// cleared. updated_at is deliberately left alone: clearing is housekeeping of
// a send, not a change an admin made.
func (r *webhookRepository) ClearExpiredPreviousSecret(ctx context.Context, id uint, now time.Time) error {
	return r.db.WithContext(ctx).Exec(
		"UPDATE webhook_endpoints SET previous_secret_sealed = NULL, previous_secret_expires_at = NULL WHERE id = ? AND previous_secret_expires_at IS NOT NULL AND previous_secret_expires_at <= ?",
		id, now.UTC()).Error
}

func (r *webhookRepository) DeleteEndpoint(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked models.WebhookEndpoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&locked).Error; err != nil {
			return err
		}
		if err := tx.Where("endpoint_id = ?", id).Delete(&models.WebhookDelivery{}).Error; err != nil {
			return fmt.Errorf("deleting webhook_deliveries of endpoint %d: %w", id, err)
		}
		result := tx.Where("id = ?", id).Delete(&models.WebhookEndpoint{})
		if result.Error != nil {
			return fmt.Errorf("deleting webhook endpoint %d: %w", id, result.Error)
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (r *webhookRepository) ListActiveEndpointsFor(ctx context.Context, eventType string) ([]models.WebhookEndpoint, error) {
	var endpoints []models.WebhookEndpoint
	if err := r.db.WithContext(ctx).Where("is_active = ?", true).Order("id").Find(&endpoints).Error; err != nil {
		return nil, err
	}
	out := endpoints[:0]
	for _, endpoint := range endpoints {
		if endpoint.EventTypes.Contains(eventType) {
			out = append(out, endpoint)
		}
	}
	return out, nil
}

func (r *webhookRepository) EnqueueDeliveries(ctx context.Context, deliveries []models.WebhookDelivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&deliveries).Error
}

// WebhookDueDelivery is one row discovery returns: its id and its endpoint.
type WebhookDueDelivery struct {
	ID         uint
	EndpointID uint
}

func (r *webhookRepository) ListDueDeliveries(ctx context.Context, now time.Time, limit int, skipEndpoints []uint) ([]WebhookDueDelivery, error) {
	var due []WebhookDueDelivery
	now = now.UTC()
	// The paused and the skipped endpoints are left out in SQL, before the
	// limit, so the rows of one such endpoint never fill the batch and starve
	// the others.
	query := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Select("id", "endpoint_id").
		Where("status = ? AND next_attempt_at <= ?", models.WebhookDeliveryPending, now).
		Where("endpoint_id NOT IN (SELECT id FROM webhook_endpoints WHERE paused_until IS NOT NULL AND paused_until > ?)", now)
	if len(skipEndpoints) > 0 {
		// Only a non-empty list: an empty one would render NOT IN (NULL),
		// which matches no row.
		query = query.Where("endpoint_id NOT IN ?", skipEndpoints)
	}
	err := query.Order("next_attempt_at").Order("id").Limit(limit).Scan(&due).Error
	return due, err
}

// WebhookEndpointSecret is the id and the sealed current secret of one
// endpoint.
type WebhookEndpointSecret struct {
	ID           uint
	SecretSealed string
}

func (r *webhookRepository) ListActiveEndpointSecrets(ctx context.Context) ([]WebhookEndpointSecret, error) {
	var secrets []WebhookEndpointSecret
	err := r.db.WithContext(ctx).Model(&models.WebhookEndpoint{}).Select("id", "secret_sealed").
		Where("is_active = ?", true).Order("id").Scan(&secrets).Error
	return secrets, err
}

// matchesAfterZero re-checks a conditional write that reported zero changed
// rows (MySQL reports changed, not matched, rows): true when a row still
// matches the write's own predicate, false when nothing matches it any more.
func (r *webhookRepository) matchesAfterZero(ctx context.Context, where string, args ...interface{}) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Where(where, args...).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *webhookRepository) ClaimDelivery(ctx context.Context, id uint, now, leaseUntil time.Time) (*models.WebhookDelivery, error) {
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	where := "id = ? AND status = ? AND next_attempt_at <= ?"
	args := []interface{}{id, models.WebhookDeliveryPending, now}
	result := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Where(where, args...).
		Updates(map[string]interface{}{
			"status":          models.WebhookDeliverySending,
			"attempts":        gorm.Expr("attempts + 1"),
			"last_attempt_at": now,
			"lease_until":     leaseUntil,
			"updated_at":      now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// Re-checked with the claim's own predicate. The claim always changes
		// status, so it can never be an identical write: a row that no longer
		// matches was taken or moved by someone else (the ordinary miss), and
		// one that still matches was not written at all. A re-check against
		// the written values instead would let a second claimer with the same
		// clock reading count the first claimer's row as its own.
		matched, err := r.matchesAfterZero(ctx, where, args...)
		if err != nil {
			return nil, err
		}
		if matched {
			return nil, fmt.Errorf("webhook delivery %d: claim matched a row but changed none", id)
		}
		return nil, ErrWebhookDeliveryNotClaimed
	}
	return r.GetDelivery(ctx, id)
}

func (r *webhookRepository) RequeueDelivery(ctx context.Context, id uint, now time.Time) error {
	now = now.UTC()
	where := "id = ? AND status IN ?"
	args := []interface{}{id, []string{models.WebhookDeliverySucceeded, models.WebhookDeliveryFailed}}
	result := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Where(where, args...).
		Updates(map[string]interface{}{
			"status":           models.WebhookDeliveryPending,
			"attempts":         0,
			"next_attempt_at":  now,
			"lease_until":      nil,
			"last_http_status": nil,
			"error_code":       "",
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// Re-checked with the write's own predicate. The requeue always moves
		// the row to pending, so it is never an identical write: a row that no
		// longer matches is pending, sending or gone, and one that still
		// matches was not written at all.
		matched, err := r.matchesAfterZero(ctx, where, args...)
		if err != nil {
			return err
		}
		if matched {
			return fmt.Errorf("webhook delivery %d: requeue matched a row but changed none", id)
		}
		return ErrWebhookDeliveryNotRequeued
	}
	return nil
}

func (r *webhookRepository) FinishDelivery(ctx context.Context, id uint, claimedAttempts int, outcome WebhookDeliveryOutcome) error {
	switch outcome.Status {
	case models.WebhookDeliveryPending, models.WebhookDeliverySucceeded, models.WebhookDeliveryFailed:
	default:
		return fmt.Errorf("webhook delivery cannot be finished as %q", outcome.Status)
	}
	var next interface{}
	if outcome.NextAttemptAt != nil {
		next = outcome.NextAttemptAt.UTC()
	}
	var httpStatus interface{}
	if outcome.LastHTTPStatus != nil {
		httpStatus = *outcome.LastHTTPStatus
	}
	where := "id = ? AND status = ? AND attempts = ?"
	args := []interface{}{id, models.WebhookDeliverySending, claimedAttempts}
	result := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Where(where, args...).
		Updates(map[string]interface{}{
			"status":           outcome.Status,
			"next_attempt_at":  next,
			"lease_until":      nil,
			"last_http_status": httpStatus,
			"error_code":       outcome.ErrorCode,
			"updated_at":       outcome.Now.UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// Re-checked with the write's own predicate. The outcome always moves
		// the row out of sending, so it is never an identical write: a row
		// that no longer matches was finished or recovered by someone else
		// (a lost fence), and one that still matches was not written at all.
		matched, err := r.matchesAfterZero(ctx, where, args...)
		if err != nil {
			return err
		}
		if matched {
			return fmt.Errorf("webhook delivery %d: finish matched a row but changed none", id)
		}
		return ErrWebhookDeliveryNotSending
	}
	return nil
}

// expiredLease is one row found by the plain discovery of RecoverExpiredLeases.
type expiredLease struct {
	ID       uint
	Attempts int
}

func (r *webhookRepository) RecoverExpiredLeases(ctx context.Context, now time.Time, maxAttempts int) (int64, int64, error) {
	now = now.UTC()
	var rows []expiredLease
	if err := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Select("id", "attempts").
		Where("status = ? AND lease_until < ?", models.WebhookDeliverySending, now).
		Order("id").Scan(&rows).Error; err != nil {
		return 0, 0, err
	}
	var recovered, failed int64
	for _, row := range rows {
		values := map[string]interface{}{
			"lease_until": nil,
			"error_code":  models.WebhookErrorLeaseExpired,
			"updated_at":  now,
		}
		if row.Attempts >= maxAttempts {
			values["status"] = models.WebhookDeliveryFailed
			values["next_attempt_at"] = nil
		} else {
			values["status"] = models.WebhookDeliveryPending
			values["next_attempt_at"] = now
		}
		// Fenced on the discovered state: a row re-claimed (attempts moved),
		// renewed or finished since the discovery is left alone.
		where := "id = ? AND status = ? AND attempts = ? AND lease_until < ?"
		args := []interface{}{row.ID, models.WebhookDeliverySending, row.Attempts, now}
		result := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Where(where, args...).Updates(values)
		if result.Error != nil {
			return recovered, failed, result.Error
		}
		if result.RowsAffected == 0 {
			// Re-checked with the write's own predicate (the write clears
			// lease_until, so it is never identical): a row re-claimed,
			// renewed or finished since the discovery no longer matches and is
			// skipped; one that still matches was not written at all.
			matched, err := r.matchesAfterZero(ctx, where, args...)
			if err != nil {
				return recovered, failed, err
			}
			if matched {
				return recovered, failed, fmt.Errorf("webhook delivery %d: lease recovery matched a row but changed none", row.ID)
			}
			continue
		}
		if row.Attempts >= maxAttempts {
			failed++
		} else {
			recovered++
		}
	}
	return recovered, failed, nil
}

func (r *webhookRepository) PruneFinished(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoff = cutoff.UTC()
	finished := []string{models.WebhookDeliverySucceeded, models.WebhookDeliveryFailed}
	var ids []uint
	if err := r.db.WithContext(ctx).Model(&models.WebhookDelivery{}).
		Where("status IN ? AND updated_at < ?", finished, cutoff).
		Order("id").Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	var removed int64
	for start := 0; start < len(ids); start += WebhookPruneChunk {
		end := start + WebhookPruneChunk
		if end > len(ids) {
			end = len(ids)
		}
		result := r.db.WithContext(ctx).
			Where("id IN ? AND status IN ? AND updated_at < ?", ids[start:end], finished, cutoff).
			Delete(&models.WebhookDelivery{})
		if result.Error != nil {
			return removed, result.Error
		}
		removed += result.RowsAffected
	}
	return removed, nil
}

func (r *webhookRepository) GetDelivery(ctx context.Context, id uint) (*models.WebhookDelivery, error) {
	var delivery models.WebhookDelivery
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&delivery).Error; err != nil {
		return nil, err
	}
	return &delivery, nil
}

func (r *webhookRepository) ListDeliveries(ctx context.Context, filter WebhookDeliveryFilter, offset, limit int) ([]models.WebhookDelivery, int64, error) {
	scope := func(db *gorm.DB) *gorm.DB {
		db = db.Model(&models.WebhookDelivery{})
		if filter.EndpointID != 0 {
			db = db.Where("endpoint_id = ?", filter.EndpointID)
		}
		if filter.Status != "" {
			db = db.Where("status = ?", filter.Status)
		}
		if filter.EventType != "" {
			db = db.Where("event_type = ?", filter.EventType)
		}
		return db
	}
	db := r.db.WithContext(ctx)
	var total int64
	if err := scope(db).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	deliveries := []models.WebhookDelivery{}
	query := orderByColumnThenID(scope(db), "webhook_deliveries", "id", true)
	if err := query.Offset(offset).Limit(limit).Find(&deliveries).Error; err != nil {
		return nil, 0, err
	}
	return deliveries, total, nil
}
