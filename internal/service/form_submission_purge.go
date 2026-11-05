package service

import (
	"context"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const (
	// FormRetentionMinimum is the shortest retention a purger accepts. It sits
	// above the 48 hours a confirmation link stays clickable, and it is what
	// config's 3-day floor on FORM_RETENTION_DAYS corresponds to.
	FormRetentionMinimum = 72 * time.Hour

	// formPurgeChunkSize is the most submissions one transaction removes, so a
	// first run over a long backlog never holds its locks for long.
	formPurgeChunkSize = 500

	// formPurgeChunkRetries is how many times a chunk that lost a deadlock or
	// timed out on a lock is replayed in a fresh transaction.
	formPurgeChunkRetries = 5
)

// ErrFormRetentionTooShort is returned by Sweep when the purger was built with a
// retention below FormRetentionMinimum. Nothing has been read or written.
var ErrFormRetentionTooShort = errors.New("form retention is below the 72-hour minimum")

// FormPurgeResult counts what a sweep removed. Chunks counts only the committed
// transactions that removed something, so a sweep with nothing to do reports
// zeros.
type FormPurgeResult struct {
	Spam    int64
	Pending int64
	Tokens  int64
	Chunks  int64
}

// FormSubmissionPurger hard-deletes spam and never-confirmed pending submissions
// that have no linked lead and are older than the retention, with their
// confirmation tokens. Received and confirmed rows and lead-linked rows are never
// touched: lead erasure owns the latter.
type FormSubmissionPurger struct {
	repo      repository.FormRepository
	txManager *utils.TransactionManager
	retention time.Duration
	clock     func() time.Time
}

// FormSubmissionPurgerOption configures NewFormSubmissionPurger beyond its
// required dependencies.
type FormSubmissionPurgerOption func(*FormSubmissionPurger)

// WithFormPurgeClock replaces time.Now. The clock's zone matters on SQLite: see
// repository.FormSubmissionPurgeQuery.Now.
func WithFormPurgeClock(clock func() time.Time) FormSubmissionPurgerOption {
	return func(p *FormSubmissionPurger) { p.clock = clock }
}

// NewFormSubmissionPurger builds a purger. A retention below
// FormRetentionMinimum is not refused here but by Sweep, so a wiring mistake
// surfaces in the sweep's error and never as a deletion.
func NewFormSubmissionPurger(repo repository.FormRepository, txManager *utils.TransactionManager, retention time.Duration, opts ...FormSubmissionPurgerOption) *FormSubmissionPurger {
	p := &FormSubmissionPurger{repo: repo, txManager: txManager, retention: retention, clock: time.Now}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Sweep removes every eligible row, spam first and then pending, one transaction
// per chunk. It stops at the first chunk that fails, or as soon as ctx is done,
// and returns what the committed chunks removed along with the error; the next
// sweep picks up the remaining rows. Two sweeps may run at once: each re-checks
// the rows under lock, so the loser of a chunk finds nothing left to remove and
// a row is counted once.
//
// A sweep commits chunk by chunk, so it refuses a ctx that already carries a
// transaction (utils.ErrTransactionRootRequired): an enclosing transaction would
// turn every chunk into a provisional write and hold all their locks to its end.
func (p *FormSubmissionPurger) Sweep(ctx context.Context) (FormPurgeResult, error) {
	var result FormPurgeResult
	if p.retention < FormRetentionMinimum {
		return result, ErrFormRetentionTooShort
	}
	if utils.InTransaction(ctx) {
		return result, utils.ErrTransactionRootRequired
	}

	cutoff := p.clock().UTC().Add(-p.retention)
	for _, status := range []models.FormSubmissionStatus{models.FormSubmissionSpam, models.FormSubmissionPending} {
		var afterID uint
		for {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			chunk, err := p.purgeChunk(ctx, repository.FormSubmissionPurgeQuery{
				Status:  status,
				Cutoff:  cutoff,
				Now:     p.clock(),
				AfterID: afterID,
				Limit:   formPurgeChunkSize,
			})
			if err != nil {
				return result, err
			}
			result.add(status, chunk)
			if chunk.Selected < formPurgeChunkSize {
				break
			}
			afterID = chunk.LastID
		}
	}
	return result, nil
}

func (r *FormPurgeResult) add(status models.FormSubmissionStatus, chunk repository.FormSubmissionPurgeChunk) {
	if chunk.Submissions == 0 {
		return
	}
	if status == models.FormSubmissionSpam {
		r.Spam += chunk.Submissions
	} else {
		r.Pending += chunk.Submissions
	}
	r.Tokens += chunk.Tokens
	r.Chunks++
}

// purgeChunk runs one chunk in its own transaction. A replay starts from a clean
// slate, so only the attempt that committed is reported.
func (p *FormSubmissionPurger) purgeChunk(ctx context.Context, q repository.FormSubmissionPurgeQuery) (repository.FormSubmissionPurgeChunk, error) {
	var chunk repository.FormSubmissionPurgeChunk
	err := p.txManager.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		chunk, err = p.repo.WithTx(tx).PurgeSubmissionChunk(q)
		return err
	}, formPurgeChunkRetries)
	if err != nil {
		return repository.FormSubmissionPurgeChunk{}, err
	}
	return chunk, nil
}
