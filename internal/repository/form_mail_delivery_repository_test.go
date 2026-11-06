package repository

// Regression roots for the form mail delivery records (#216) on real SQL: a
// private SQLite database and a disposable schema on TEST_MYSQL_DSN. The MySQL
// roots skip without the variable and fail when it names an unreachable server.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openFormMailRepoDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	db := openAutomationRepoDB(t, useMySQL, 2)
	require.NoError(t, db.AutoMigrate(&models.FormMailDelivery{}))
	return db
}

// seedDelivery writes one row with the given status and updated_at.
func seedDelivery(t *testing.T, db *gorm.DB, submission *models.FormSubmission, kind, status string, updatedAt time.Time) *models.FormMailDelivery {
	t.Helper()
	row := &models.FormMailDelivery{
		CreatedAt:    updatedAt,
		UpdatedAt:    updatedAt,
		SubmissionID: submission.ID,
		FormID:       submission.FormID,
		Kind:         kind,
		Status:       status,
	}
	require.NoError(t, NewFormRepository(db).CreateDelivery(row))
	return row
}

func loadDelivery(t *testing.T, db *gorm.DB, id uint) models.FormMailDelivery {
	t.Helper()
	var row models.FormMailDelivery
	require.NoError(t, db.Unscoped().First(&row, id).Error)
	return row
}

func TestFormMailDeliveryRepositorySQLite(t *testing.T) {
	runFormMailDeliveryRepository(t, openFormMailRepoDB(t, false))
}

func TestFormMailDeliveryRepositoryMySQL(t *testing.T) {
	runFormMailDeliveryRepository(t, openFormMailRepoDB(t, true))
}

func runFormMailDeliveryRepository(t *testing.T, db *gorm.DB) {
	repo := NewFormRepository(db)
	form := makeForm(t, db, "Delivery form", "delivery-form-public-id", models.FormStatusPublished)
	other := makeForm(t, db, "Other form", "delivery-other-public-id", models.FormStatusPublished)
	submission := makeSubmission(t, db, form.ID, "visitor@example.com", models.FormSubmissionReceived)
	otherSubmission := makeSubmission(t, db, other.ID, "other@example.com", models.FormSubmissionReceived)

	now := time.Now().UTC().Truncate(time.Second)
	stuckBefore := now.Add(-15 * time.Minute)
	fresh := now.Add(-time.Minute)
	old := now.Add(-time.Hour)

	t.Run("ClaimPendingDelivery moves only a pending row", func(t *testing.T) {
		pending := seedDelivery(t, db, submission, models.FormMailKindFollowUp, models.FormMailStatusPending, fresh)
		require.NoError(t, repo.ClaimPendingDelivery(pending.ID, now))
		row := loadDelivery(t, db, pending.ID)
		assert.Equal(t, models.FormMailStatusSending, row.Status)
		assert.Equal(t, 1, row.Attempts)
		require.NotNil(t, row.LastAttemptAt)
		assert.WithinDuration(t, now, *row.LastAttemptAt, time.Second)
		assert.WithinDuration(t, now, row.UpdatedAt, time.Second)

		assert.ErrorIs(t, repo.ClaimPendingDelivery(pending.ID, now), gorm.ErrRecordNotFound, "a sending row is not claimed again")
		for _, status := range []string{models.FormMailStatusFailed, models.FormMailStatusLogOnly, models.FormMailStatusSent, models.FormMailStatusSkipped} {
			row := seedDelivery(t, db, submission, models.FormMailKindFollowUp, status, old)
			assert.ErrorIsf(t, repo.ClaimPendingDelivery(row.ID, now), gorm.ErrRecordNotFound, "%s is not claimable", status)
			assert.Equal(t, status, loadDelivery(t, db, row.ID).Status)
		}
		assert.ErrorIs(t, repo.ClaimPendingDelivery(999999, now), gorm.ErrRecordNotFound)
	})

	t.Run("ClaimDeliveryForRetry moves failed, log_only and stuck rows only", func(t *testing.T) {
		eligible := []*models.FormMailDelivery{
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusFailed, fresh),
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusLogOnly, fresh),
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusPending, old),
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusSending, old),
		}
		for _, row := range eligible {
			require.NoErrorf(t, repo.ClaimDeliveryForRetry(row.ID, now, stuckBefore), "%s is claimable", row.Status)
			stored := loadDelivery(t, db, row.ID)
			assert.Equal(t, models.FormMailStatusSending, stored.Status)
			assert.Equal(t, 1, stored.Attempts)
			// Now fresh, so a second retry claim loses.
			assert.ErrorIs(t, repo.ClaimDeliveryForRetry(row.ID, now, stuckBefore), gorm.ErrRecordNotFound)
		}
		ineligible := []*models.FormMailDelivery{
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusSent, old),
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusSkipped, old),
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusPending, fresh),
			seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusSending, fresh),
		}
		for _, row := range ineligible {
			assert.ErrorIsf(t, repo.ClaimDeliveryForRetry(row.ID, now, stuckBefore), gorm.ErrRecordNotFound, "%s (%s) is not claimable", row.Status, row.UpdatedAt)
			stored := loadDelivery(t, db, row.ID)
			assert.Equal(t, row.Status, stored.Status)
			assert.Zero(t, stored.Attempts)
		}
	})

	t.Run("FinishDelivery stamps only a sending row", func(t *testing.T) {
		row := seedDelivery(t, db, submission, models.FormMailKindFollowUp, models.FormMailStatusSending, fresh)
		sentAt := now
		require.NoError(t, repo.FinishDelivery(row.ID, FormMailDeliveryOutcome{Status: models.FormMailStatusSent, SentAt: &sentAt, Now: now}))
		stored := loadDelivery(t, db, row.ID)
		assert.Equal(t, models.FormMailStatusSent, stored.Status)
		assert.Empty(t, stored.ErrorCode)
		require.NotNil(t, stored.SentAt)
		assert.WithinDuration(t, now, *stored.SentAt, time.Second)
		assert.ErrorIs(t, repo.FinishDelivery(row.ID, FormMailDeliveryOutcome{Status: models.FormMailStatusFailed, ErrorCode: models.FormMailErrorMailFailed, Now: now}), gorm.ErrRecordNotFound, "a stamped row is not stamped again")
		assert.Equal(t, models.FormMailStatusSent, loadDelivery(t, db, row.ID).Status)

		for _, status := range []string{models.FormMailStatusPending, models.FormMailStatusFailed} {
			other := seedDelivery(t, db, submission, models.FormMailKindFollowUp, status, fresh)
			assert.ErrorIs(t, repo.FinishDelivery(other.ID, FormMailDeliveryOutcome{Status: models.FormMailStatusSent, Now: now}), gorm.ErrRecordNotFound)
			assert.Equal(t, status, loadDelivery(t, db, other.ID).Status)
		}
		assert.ErrorIs(t, repo.FinishDelivery(999999, FormMailDeliveryOutcome{Status: models.FormMailStatusSent, Now: now}), gorm.ErrRecordNotFound)
		assert.Error(t, repo.FinishDelivery(row.ID, FormMailDeliveryOutcome{Status: models.FormMailStatusSending, Now: now}), "sending is not an outcome")
	})

	t.Run("ReArmConfirmationToken updates one token by primary key and inserts nothing", func(t *testing.T) {
		pending := makeSubmission(t, db, form.ID, "rearm@example.com", models.FormSubmissionPending)
		used := now.Add(-time.Hour)
		token := &models.FormConfirmationToken{SubmissionID: pending.ID, TokenHash: fmt.Sprintf("%064d", 1), ExpiresAt: now.Add(-time.Hour), UsedAt: &used}
		require.NoError(t, db.Create(token).Error)
		var before int64
		require.NoError(t, db.Unscoped().Model(&models.FormConfirmationToken{}).Count(&before).Error)

		ids, err := repo.ListConfirmationTokenIDs(pending.ID)
		require.NoError(t, err)
		assert.Equal(t, []uint{token.ID}, ids)

		expires := now.Add(48 * time.Hour)
		require.NoError(t, repo.ReArmConfirmationToken(token.ID, fmt.Sprintf("%064d", 2), expires))
		var stored models.FormConfirmationToken
		require.NoError(t, db.First(&stored, token.ID).Error)
		assert.Equal(t, fmt.Sprintf("%064d", 2), stored.TokenHash)
		assert.Nil(t, stored.UsedAt)
		assert.WithinDuration(t, expires, stored.ExpiresAt, time.Second)
		var after int64
		require.NoError(t, db.Unscoped().Model(&models.FormConfirmationToken{}).Count(&after).Error)
		assert.Equal(t, before, after, "nothing is inserted")
		assert.ErrorIs(t, repo.ReArmConfirmationToken(999999, fmt.Sprintf("%064d", 3), expires), gorm.ErrRecordNotFound)
	})

	t.Run("ListAttentionDeliveries", func(t *testing.T) {
		require.NoError(t, db.Unscoped().Where("1 = 1").Delete(&models.FormMailDelivery{}).Error)
		failed := seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusFailed, fresh)
		logOnly := seedDelivery(t, db, submission, models.FormMailKindFollowUp, models.FormMailStatusLogOnly, fresh)
		stuck := seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusPending, old)
		stuckSending := seedDelivery(t, db, submission, models.FormMailKindFollowUp, models.FormMailStatusSending, old)
		seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusPending, fresh)
		sent := seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusSent, old)
		seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusSkipped, old)
		otherFailed := seedDelivery(t, db, otherSubmission, models.FormMailKindNotification, models.FormMailStatusFailed, fresh)

		ids := func(rows []models.FormMailDelivery) []uint {
			out := make([]uint, 0, len(rows))
			for _, row := range rows {
				out = append(out, row.ID)
			}
			return out
		}
		list := func(q FormMailDeliveryQuery) ([]uint, int64) {
			t.Helper()
			if q.Limit == 0 {
				q.Limit = 100
			}
			q.StuckBefore = stuckBefore
			rows, total, err := repo.ListAttentionDeliveries(q)
			require.NoError(t, err)
			return ids(rows), total
		}

		got, total := list(FormMailDeliveryQuery{})
		assert.Equal(t, []uint{otherFailed.ID, stuckSending.ID, stuck.ID, logOnly.ID, failed.ID}, got, "failed, log_only and stuck rows, id descending")
		assert.EqualValues(t, 5, total)

		got, total = list(FormMailDeliveryQuery{FormID: form.ID})
		assert.Equal(t, []uint{stuckSending.ID, stuck.ID, logOnly.ID, failed.ID}, got)
		assert.EqualValues(t, 4, total)

		got, total = list(FormMailDeliveryQuery{FormID: form.ID, Kind: models.FormMailKindNotification})
		assert.Equal(t, []uint{stuck.ID, failed.ID}, got)
		assert.EqualValues(t, 2, total)

		got, total = list(FormMailDeliveryQuery{Status: models.FormMailStatusSent})
		assert.Equal(t, []uint{sent.ID}, got, "an explicit status selects exactly that stored status")
		assert.EqualValues(t, 1, total)

		got, total = list(FormMailDeliveryQuery{Status: models.FormMailStatusPending})
		assert.Len(t, got, 2, "fresh and stuck pending rows alike")
		assert.EqualValues(t, 2, total)

		got, total = list(FormMailDeliveryQuery{Offset: 1, Limit: 2})
		assert.Equal(t, []uint{stuckSending.ID, stuck.ID}, got, "paginated")
		assert.EqualValues(t, 5, total, "the total counts the whole filter")
		got, total = list(FormMailDeliveryQuery{Offset: 4, Limit: 2})
		assert.Equal(t, []uint{failed.ID}, got)
		assert.EqualValues(t, 5, total)
	})

	t.Run("ListDeliveriesForSubmissions returns each submission's rows in id order", func(t *testing.T) {
		require.NoError(t, db.Unscoped().Where("1 = 1").Delete(&models.FormMailDelivery{}).Error)
		first := seedDelivery(t, db, submission, models.FormMailKindNotification, models.FormMailStatusSent, fresh)
		otherRow := seedDelivery(t, db, otherSubmission, models.FormMailKindFollowUp, models.FormMailStatusFailed, fresh)
		second := seedDelivery(t, db, submission, models.FormMailKindFollowUp, models.FormMailStatusSent, fresh)

		rows, err := repo.ListDeliveriesForSubmissions([]uint{submission.ID, otherSubmission.ID})
		require.NoError(t, err)
		var got []uint
		bySubmission := map[uint][]uint{}
		for _, row := range rows {
			got = append(got, row.ID)
			bySubmission[row.SubmissionID] = append(bySubmission[row.SubmissionID], row.ID)
		}
		assert.Equal(t, []uint{first.ID, otherRow.ID, second.ID}, got)
		assert.Equal(t, []uint{first.ID, second.ID}, bySubmission[submission.ID])
		assert.Equal(t, []uint{otherRow.ID}, bySubmission[otherSubmission.ID])

		rows, err = repo.ListDeliveriesForSubmissions(nil)
		require.NoError(t, err)
		assert.Empty(t, rows)
	})

	t.Run("the claims run inside a transaction and roll back with it", func(t *testing.T) {
		row := seedDelivery(t, db, submission, models.FormMailKindFollowUp, models.FormMailStatusFailed, fresh)
		injected := errors.New("injected")
		err := db.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, repo.WithTx(tx).ClaimDeliveryForRetry(row.ID, now, stuckBefore))
			return injected
		})
		require.ErrorIs(t, err, injected)
		assert.Equal(t, models.FormMailStatusFailed, loadDelivery(t, db, row.ID).Status)
	})
}

// AC-9: a chunk removes the delivery rows of every submission it purges, by
// primary key after its token and submission locks and before the submission
// delete, and reports them; the rows of kept submissions survive.
func TestFormMailDeliveryPurgedWithSubmissionSQLite(t *testing.T) {
	runFormMailDeliveryPurgedWithSubmission(t, newFormPurgeRepoEnv(t, false))
}

func TestFormMailDeliveryPurgedWithSubmissionMySQL(t *testing.T) {
	runFormMailDeliveryPurgedWithSubmission(t, newFormPurgeRepoEnv(t, true))
}

func runFormMailDeliveryPurgedWithSubmission(t *testing.T, e *formPurgeRepoEnv) {
	purgedSpam := e.submission(t, formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.old})
	purgedPending := e.submission(t, formPurgeSeed{status: models.FormSubmissionPending, createdAt: e.old})
	e.token(t, purgedPending, -time.Hour, false, false)
	keptPending := e.submission(t, formPurgeSeed{status: models.FormSubmissionPending, createdAt: e.old})
	e.token(t, keptPending, time.Hour, false, false)
	keptReceived := e.submission(t, formPurgeSeed{status: models.FormSubmissionReceived, createdAt: e.old})

	seed := func(submissionID uint, kind, status string) uint {
		t.Helper()
		row := &models.FormMailDelivery{
			CreatedAt:    e.old,
			UpdatedAt:    e.old,
			SubmissionID: submissionID,
			FormID:       e.form.ID,
			Kind:         kind,
			Status:       status,
		}
		require.NoError(t, e.db.Create(row).Error)
		return row.ID
	}
	doomed := []uint{
		seed(purgedPending, models.FormMailKindConfirmation, models.FormMailStatusFailed),
		seed(purgedPending, models.FormMailKindConfirmation, models.FormMailStatusSent),
		seed(purgedSpam, models.FormMailKindNotification, models.FormMailStatusFailed),
	}
	kept := []uint{
		seed(keptPending, models.FormMailKindConfirmation, models.FormMailStatusSent),
		seed(keptReceived, models.FormMailKindNotification, models.FormMailStatusFailed),
		seed(keptReceived, models.FormMailKindFollowUp, models.FormMailStatusSent),
	}

	// The statement order of one chunk: no delivery delete before the token and
	// submission locks, none after the submission delete.
	var statements []string
	require.NoError(t, e.db.Callback().Delete().Before("gorm:delete").Register("test:record_delete", func(tx *gorm.DB) {
		statements = append(statements, "delete:"+tx.Statement.Table)
	}))
	require.NoError(t, e.db.Callback().Query().Before("gorm:query").Register("test:record_query", func(tx *gorm.DB) {
		if _, locking := tx.Statement.Clauses["FOR"]; locking {
			statements = append(statements, "lock:"+tx.Statement.Table)
		}
	}))
	t.Cleanup(func() {
		_ = e.db.Callback().Delete().Remove("test:record_delete")
		_ = e.db.Callback().Query().Remove("test:record_query")
	})

	var deliveries int64
	repo := NewFormRepository(e.db)
	for _, status := range []models.FormSubmissionStatus{models.FormSubmissionSpam, models.FormSubmissionPending} {
		statements = nil
		var chunk FormSubmissionPurgeChunk
		require.NoError(t, e.db.Transaction(func(tx *gorm.DB) error {
			var err error
			chunk, err = repo.WithTx(tx).PurgeSubmissionChunk(FormSubmissionPurgeQuery{
				Status: status, Cutoff: e.cutoff, Now: e.now, Limit: formPurgeRepoLimit,
			})
			return err
		}))
		assert.EqualValues(t, 1, chunk.Submissions, "one %s row is purged", status)
		deliveries += chunk.Deliveries

		deliveryDelete, submissionDelete, lastLock := -1, -1, -1
		for i, statement := range statements {
			switch statement {
			case "delete:form_mail_deliveries":
				deliveryDelete = i
			case "delete:form_submissions":
				submissionDelete = i
			case "lock:form_confirmation_tokens", "lock:form_submissions":
				lastLock = i
			}
		}
		require.NotEqual(t, -1, deliveryDelete, "%s: the chunk deletes delivery rows: %v", status, statements)
		assert.Greater(t, deliveryDelete, lastLock, "%s: after the locks: %v", status, statements)
		assert.Less(t, deliveryDelete, submissionDelete, "%s: before the submissions: %v", status, statements)
	}
	assert.EqualValues(t, len(doomed), deliveries, "the chunks report the delivery rows they removed")

	var count int64
	require.NoError(t, e.db.Unscoped().Model(&models.FormMailDelivery{}).Where("id IN ?", doomed).Count(&count).Error)
	assert.Zero(t, count, "the purged submissions' delivery rows are gone")
	require.NoError(t, e.db.Unscoped().Model(&models.FormMailDelivery{}).Where("id IN ?", kept).Count(&count).Error)
	assert.EqualValues(t, len(kept), count, "the kept submissions' delivery rows survive")
	assert.EqualValues(t, 2, e.unscopedSubmissions(t, []uint{keptPending, keptReceived}))
	assert.Zero(t, e.unscopedSubmissions(t, []uint{purgedSpam, purgedPending}))
}
