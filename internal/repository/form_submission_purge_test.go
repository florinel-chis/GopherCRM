package repository

// Regression roots for the retention purge's eligibility rule (issue #217), on
// real SQL: a private SQLite database and a disposable schema on TEST_MYSQL_DSN.
// The MySQL roots skip without the variable and fail when it names an unreachable
// server.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const formPurgeRepoLimit = 500

// formPurgeRepoEnv is one seeded database. now is whole seconds, so a row seeded
// exactly at the cutoff compares equal on both engines.
type formPurgeRepoEnv struct {
	db     *gorm.DB
	form   *models.Form
	now    time.Time
	cutoff time.Time
	old    time.Time
	seq    int
}

func newFormPurgeRepoEnv(t *testing.T, useMySQL bool) *formPurgeRepoEnv {
	t.Helper()
	db := openAutomationRepoDB(t, useMySQL, 2)
	now := time.Now().Truncate(time.Second)
	cutoff := now.UTC().Add(-90 * 24 * time.Hour)
	return &formPurgeRepoEnv{
		db:     db,
		form:   makeForm(t, db, "Purge form", "purge-form-public-id", models.FormStatusPublished),
		now:    now,
		cutoff: cutoff,
		old:    cutoff.Add(-24 * time.Hour),
	}
}

type formPurgeSeed struct {
	formID    uint
	status    models.FormSubmissionStatus
	createdAt time.Time
	leadID    *uint
	deleted   bool
}

func (e *formPurgeRepoEnv) submission(t *testing.T, seed formPurgeSeed) uint {
	t.Helper()
	e.seq++
	row := &models.FormSubmission{
		FormID:    seed.formID,
		Status:    seed.status,
		Email:     fmt.Sprintf("visitor-%d@example.com", e.seq),
		Data:      map[string]string{"email": fmt.Sprintf("visitor-%d@example.com", e.seq)},
		IPAddress: "203.0.113.7",
		UserAgent: "Mozilla/5.0",
		Referrer:  "https://customer.example/contact",
		LeadID:    seed.leadID,
	}
	if row.FormID == 0 {
		row.FormID = e.form.ID
	}
	row.CreatedAt = seed.createdAt
	row.UpdatedAt = seed.createdAt
	if seed.deleted {
		row.DeletedAt = gorm.DeletedAt{Time: seed.createdAt, Valid: true}
	}
	require.NoError(t, e.db.Create(row).Error)
	return row.ID
}

// token seeds one token: used and softDeleted describe what happened to it, and
// expiresIn is relative to the purge's clock.
func (e *formPurgeRepoEnv) token(t *testing.T, submissionID uint, expiresIn time.Duration, used, softDeleted bool) uint {
	t.Helper()
	e.seq++
	row := &models.FormConfirmationToken{
		SubmissionID: submissionID,
		TokenHash:    fmt.Sprintf("hash-%060d", e.seq),
		ExpiresAt:    e.now.Add(expiresIn),
	}
	if used {
		usedAt := e.now.Add(-time.Hour)
		row.UsedAt = &usedAt
	}
	if softDeleted {
		row.DeletedAt = gorm.DeletedAt{Time: e.now.Add(-time.Hour), Valid: true}
	}
	require.NoError(t, e.db.Create(row).Error)
	return row.ID
}

// purge walks both statuses, one transaction per chunk, the way a sweep does.
func (e *formPurgeRepoEnv) purge(t *testing.T) (submissions, tokens int64) {
	t.Helper()
	repo := NewFormRepository(e.db)
	for _, status := range []models.FormSubmissionStatus{models.FormSubmissionSpam, models.FormSubmissionPending} {
		var afterID uint
		for {
			var chunk FormSubmissionPurgeChunk
			require.NoError(t, e.db.Transaction(func(tx *gorm.DB) error {
				var err error
				chunk, err = repo.WithTx(tx).PurgeSubmissionChunk(FormSubmissionPurgeQuery{
					Status: status, Cutoff: e.cutoff, Now: e.now, AfterID: afterID, Limit: formPurgeRepoLimit,
				})
				return err
			}))
			submissions += chunk.Submissions
			tokens += chunk.Tokens
			if chunk.Selected < formPurgeRepoLimit {
				break
			}
			afterID = chunk.LastID
		}
	}
	return submissions, tokens
}

func (e *formPurgeRepoEnv) unscopedSubmissions(t *testing.T, ids []uint) int64 {
	t.Helper()
	var count int64
	require.NoError(t, e.db.Unscoped().Model(&models.FormSubmission{}).Where("id IN ?", ids).Count(&count).Error)
	return count
}

func (e *formPurgeRepoEnv) unscopedTokens(t *testing.T, ids []uint) int64 {
	t.Helper()
	var count int64
	require.NoError(t, e.db.Unscoped().Model(&models.FormConfirmationToken{}).Where("id IN ?", ids).Count(&count).Error)
	return count
}

// dump renders every stored column of both tables, Unscoped, so a comparison
// catches any write to a kept row, updated_at and deleted_at included.
func (e *formPurgeRepoEnv) dump(t *testing.T) string {
	t.Helper()
	var submissions []models.FormSubmission
	require.NoError(t, e.db.Unscoped().Order("id").Find(&submissions).Error)
	var tokens []models.FormConfirmationToken
	require.NoError(t, e.db.Unscoped().Order("id").Find(&tokens).Error)

	var b strings.Builder
	stamp := func(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
	optional := func(value *time.Time) string {
		if value == nil {
			return "null"
		}
		return stamp(*value)
	}
	for _, s := range submissions {
		lead := "null"
		if s.LeadID != nil {
			lead = fmt.Sprint(*s.LeadID)
		}
		fmt.Fprintf(&b, "submission %d form=%d data=%q email=%q status=%s reason=%q lead=%s ip=%q ua=%q ref=%q confirmed=%s created=%s updated=%s deleted=%v/%s\n",
			s.ID, s.FormID, s.DataJSON, s.Email, s.Status, s.SpamReason, lead, s.IPAddress, s.UserAgent, s.Referrer,
			optional(s.ConfirmedAt), stamp(s.CreatedAt), stamp(s.UpdatedAt), s.DeletedAt.Valid, stamp(s.DeletedAt.Time))
	}
	for _, token := range tokens {
		fmt.Fprintf(&b, "token %d submission=%d hash=%s expires=%s used=%s created=%s updated=%s deleted=%v/%s\n",
			token.ID, token.SubmissionID, token.TokenHash, stamp(token.ExpiresAt), optional(token.UsedAt),
			stamp(token.CreatedAt), stamp(token.UpdatedAt), token.DeletedAt.Valid, stamp(token.DeletedAt.Time))
	}
	return b.String()
}

func TestFormSubmissionPurgeRemovesEligibleSQLite(t *testing.T) {
	runFormSubmissionPurgeRemovesEligible(t, false)
}

func TestFormSubmissionPurgeRemovesEligibleMySQL(t *testing.T) {
	runFormSubmissionPurgeRemovesEligible(t, true)
}

func TestFormSubmissionPurgeKeepsIneligibleSQLite(t *testing.T) {
	runFormSubmissionPurgeKeepsIneligible(t, false)
}

func TestFormSubmissionPurgeKeepsIneligibleMySQL(t *testing.T) {
	runFormSubmissionPurgeKeepsIneligible(t, true)
}

func runFormSubmissionPurgeRemovesEligible(t *testing.T, useMySQL bool) {
	e := newFormPurgeRepoEnv(t, useMySQL)

	deletedForm := makeForm(t, e.db, "Deleted form", "purge-deleted-form-id", models.FormStatusPublished)
	require.NoError(t, e.db.Delete(deletedForm).Error)

	var submissions, tokens []uint
	eligible := func(seed formPurgeSeed) uint {
		id := e.submission(t, seed)
		submissions = append(submissions, id)
		return id
	}
	spam := formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.old}
	pending := formPurgeSeed{status: models.FormSubmissionPending, createdAt: e.old}

	// Spam rows, whatever they hold. A spam row cannot be confirmed, so even an
	// unused, unexpired token does not protect it.
	eligible(spam)
	spamWithToken := eligible(spam)
	tokens = append(tokens, e.token(t, spamWithToken, time.Hour, false, false))
	eligible(formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.old, deleted: true})
	eligible(formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.old, formID: deletedForm.ID})

	// One second before the cutoff is already too old.
	eligible(formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.cutoff.Add(-time.Second)})

	// Pending rows with no token, only spent or only expired tokens.
	eligible(pending)
	spent := eligible(pending)
	tokens = append(tokens, e.token(t, spent, time.Hour, true, false))
	expired := eligible(pending)
	tokens = append(tokens, e.token(t, expired, -time.Hour, false, false))
	both := eligible(pending)
	tokens = append(tokens,
		e.token(t, both, time.Hour, true, false),
		e.token(t, both, -time.Hour, false, false))

	// A soft-deleted token is not spendable and is removed with its row.
	softToken := eligible(pending)
	tokens = append(tokens, e.token(t, softToken, time.Hour, false, true))

	// Pending rows already soft-deleted, and rows of a soft-deleted form.
	softRow := eligible(formPurgeSeed{status: models.FormSubmissionPending, createdAt: e.old, deleted: true})
	tokens = append(tokens, e.token(t, softRow, -time.Hour, false, false))
	eligible(formPurgeSeed{status: models.FormSubmissionPending, createdAt: e.old, formID: deletedForm.ID})

	removed, removedTokens := e.purge(t)

	assert.EqualValues(t, len(submissions), removed)
	assert.EqualValues(t, len(tokens), removedTokens)
	assert.Zero(t, e.unscopedSubmissions(t, submissions), "an Unscoped count finds none of the rows")
	assert.Zero(t, e.unscopedTokens(t, tokens), "an Unscoped count finds none of the tokens")

	again, againTokens := e.purge(t)
	assert.Zero(t, again)
	assert.Zero(t, againTokens)
}

func runFormSubmissionPurgeKeepsIneligible(t *testing.T, useMySQL bool) {
	e := newFormPurgeRepoEnv(t, useMySQL)
	lead := uint(4242)
	ancient := e.cutoff.Add(-3650 * 24 * time.Hour)

	// Received and confirmed rows of any age, with and without tokens.
	for _, status := range []models.FormSubmissionStatus{models.FormSubmissionReceived, models.FormSubmissionConfirmed} {
		plain := e.submission(t, formPurgeSeed{status: status, createdAt: ancient})
		e.token(t, plain, -time.Hour, true, false)
		e.submission(t, formPurgeSeed{status: status, createdAt: ancient, deleted: true})
		e.submission(t, formPurgeSeed{status: status, createdAt: ancient, leadID: &lead})
	}

	// Spam or pending rows linked to a lead: lead erasure owns those.
	for _, status := range []models.FormSubmissionStatus{models.FormSubmissionSpam, models.FormSubmissionPending} {
		linked := e.submission(t, formPurgeSeed{status: status, createdAt: ancient, leadID: &lead})
		e.token(t, linked, -time.Hour, false, false)

		// Rows created at or after the cutoff.
		atCutoff := e.submission(t, formPurgeSeed{status: status, createdAt: e.cutoff})
		e.token(t, atCutoff, -time.Hour, false, false)
		e.submission(t, formPurgeSeed{status: status, createdAt: e.cutoff.Add(time.Second)})
		e.submission(t, formPurgeSeed{status: status, createdAt: e.now.UTC()})
	}

	// Old pending rows that still hold a spendable token, even beside dead ones.
	spendable := e.submission(t, formPurgeSeed{status: models.FormSubmissionPending, createdAt: ancient})
	e.token(t, spendable, time.Hour, false, false)
	mixed := e.submission(t, formPurgeSeed{status: models.FormSubmissionPending, createdAt: ancient})
	e.token(t, mixed, -time.Hour, false, false)
	e.token(t, mixed, time.Hour, true, false)
	e.token(t, mixed, time.Hour, false, false)
	softDeletedSpendable := e.submission(t, formPurgeSeed{status: models.FormSubmissionPending, createdAt: ancient, deleted: true})
	e.token(t, softDeletedSpendable, time.Hour, false, false)

	before := e.dump(t)
	require.NotEmpty(t, before)

	// Nothing above is eligible, so the kept rows are all that is stored.
	removed, removedTokens := e.purge(t)
	assert.Zero(t, removed)
	assert.Zero(t, removedTokens)
	assert.Equal(t, before, e.dump(t), "a purge with nothing to remove leaves every row unchanged")

	// With eligible neighbours the purge runs its deletes and still leaves the
	// kept rows alone: this is what makes the comparison above non-vacuous.
	doomedSpam := e.submission(t, formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.old})
	doomedPending := e.submission(t, formPurgeSeed{status: models.FormSubmissionPending, createdAt: e.old})
	doomedToken := e.token(t, doomedPending, -time.Hour, false, false)

	removed, removedTokens = e.purge(t)
	assert.EqualValues(t, 2, removed)
	assert.EqualValues(t, 1, removedTokens)
	assert.Zero(t, e.unscopedSubmissions(t, []uint{doomedSpam, doomedPending}))
	assert.Zero(t, e.unscopedTokens(t, []uint{doomedToken}))
	assert.Equal(t, before, e.dump(t), "the kept rows and their tokens are byte-for-byte unchanged")
}

// A chunk outside a transaction would release its locks statement by statement
// and could not undo a failed delete, so it is refused before anything runs.
func TestFormSubmissionPurgeChunkRefusedOutsideATransaction(t *testing.T) {
	e := newFormPurgeRepoEnv(t, false)
	id := e.submission(t, formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.old})

	_, err := NewFormRepository(e.db).PurgeSubmissionChunk(FormSubmissionPurgeQuery{
		Status: models.FormSubmissionSpam, Cutoff: e.cutoff, Now: e.now, Limit: formPurgeRepoLimit,
	})
	require.ErrorIs(t, err, errFormPurgeOutsideTransaction)
	assert.EqualValues(t, 1, e.unscopedSubmissions(t, []uint{id}))
}

// The walk selects at most the limit and moves forward from AfterID.
func TestFormSubmissionPurgeChunkHonoursLimitAndCursor(t *testing.T) {
	e := newFormPurgeRepoEnv(t, false)
	var ids []uint
	for i := 0; i < 3; i++ {
		ids = append(ids, e.submission(t, formPurgeSeed{status: models.FormSubmissionSpam, createdAt: e.old}))
	}
	repo := NewFormRepository(e.db)
	run := func(afterID uint, limit int) FormSubmissionPurgeChunk {
		var chunk FormSubmissionPurgeChunk
		require.NoError(t, e.db.Transaction(func(tx *gorm.DB) error {
			var err error
			chunk, err = repo.WithTx(tx).PurgeSubmissionChunk(FormSubmissionPurgeQuery{
				Status: models.FormSubmissionSpam, Cutoff: e.cutoff, Now: e.now, AfterID: afterID, Limit: limit,
			})
			return err
		}))
		return chunk
	}

	first := run(0, 2)
	assert.Equal(t, 2, first.Selected)
	assert.Equal(t, ids[1], first.LastID)
	assert.EqualValues(t, 2, first.Submissions)

	second := run(first.LastID, 2)
	assert.Equal(t, 1, second.Selected)
	assert.Equal(t, ids[2], second.LastID)
	assert.EqualValues(t, 1, second.Submissions)

	assert.Zero(t, e.unscopedSubmissions(t, ids))
}
