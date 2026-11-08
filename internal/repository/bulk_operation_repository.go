package repository

import (
	"context"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// BulkOperationSweepChunk is the most stale pending rows one step of
// MarkStalePendingFailed reads and updates.
const BulkOperationSweepChunk = 500

type bulkOperationRepository struct {
	db *gorm.DB
}

func NewBulkOperationRepository(db *gorm.DB) BulkOperationRepository {
	return &bulkOperationRepository{db: db}
}

// WithTx binds the repository to a transaction, so CompletePending commits or
// rolls back with the batch it records.
func (r *bulkOperationRepository) WithTx(tx *gorm.DB) BulkOperationRepository {
	return &bulkOperationRepository{db: tx}
}

// Create writes the history row on the caller's context, so a request that
// has gone away stops waiting for a connection instead of recording an
// operation that will never run.
func (r *bulkOperationRepository) Create(ctx context.Context, operation *models.BulkOperation) error {
	return r.db.WithContext(ctx).Create(operation).Error
}

// CompletePending marks a pending row completed. The batch calls it on the
// transaction-bound repository, as the last write of its transaction.
func (r *bulkOperationRepository) CompletePending(ctx context.Context, id uint) error {
	return r.transitionPending(ctx, id, models.StatusCompleted)
}

// UpdateStatus records an operation's outcome on the caller's context, which
// bounds how long it may wait for a connection. It only changes a row that is
// still pending, so a late write never overwrites a terminal status.
func (r *bulkOperationRepository) UpdateStatus(ctx context.Context, id uint, status models.BulkOperationStatus) error {
	return r.transitionPending(ctx, id, status)
}

// transitionPending is one conditional UPDATE by primary key from pending.
// The status always changes when the row matches, so MySQL's changed-rows
// count equals the matched count; zero means the row is not (or no longer) a
// live pending row, and nothing was written.
func (r *bulkOperationRepository) transitionPending(ctx context.Context, id uint, status models.BulkOperationStatus) error {
	result := r.db.WithContext(ctx).Model(&models.BulkOperation{}).
		Where("id = ? AND status = ?", id, models.StatusPending).
		Update("status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperrors.ErrBulkOperationNotPending
	}
	return nil
}

// MarkStalePendingFailed fails every live pending row created strictly before
// cutoff and returns how many rows it changed.
//
// It works in two steps per chunk so that it never takes a lock through
// idx_bulk_operations_status: a plain, non-locking SELECT of at most
// BulkOperationSweepChunk candidate ids in id order (a keyset walk, so a row
// is read once per sweep), then one UPDATE by primary key over exactly those
// ids that repeats the whole predicate. An UPDATE whose predicate is the
// status index would lock the secondary index before the primary key, the
// reverse of CompletePending, and deadlock against live batches on
// MySQL/MariaDB. A row a batch completed between the two steps no longer
// matches and is left alone.
//
// The cutoff is compared in the zone of the handle's clock, the one that
// stamped created_at: SQLite compares timestamps as text.
func (r *bulkOperationRepository) MarkStalePendingFailed(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoff = cutoff.In(r.db.NowFunc().Location())
	var total int64
	var afterID uint
	for {
		var ids []uint
		if err := r.db.WithContext(ctx).Model(&models.BulkOperation{}).
			Where("status = ? AND created_at < ? AND id > ?", models.StatusPending, cutoff, afterID).
			Order("id").
			Limit(BulkOperationSweepChunk).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		result := r.db.WithContext(ctx).Model(&models.BulkOperation{}).
			Where("id IN ? AND status = ? AND created_at < ?", ids, models.StatusPending, cutoff).
			Update("status", models.StatusFailed)
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
		if len(ids) < BulkOperationSweepChunk {
			return total, nil
		}
		afterID = ids[len(ids)-1]
	}
}
