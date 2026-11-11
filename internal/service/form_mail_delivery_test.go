package service

// Regression roots for the form mail delivery records (#216), on real
// repositories: a one-connection in-memory SQLite database and a disposable
// schema on TEST_MYSQL_DSN. The MySQL roots skip without the variable and fail
// when it names an unreachable server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// deliveryMailer is a non-logging fake: it records every message, fails per
// recipient or globally, can stall, and can run a hook inside Send.
type deliveryMailer struct {
	mu     sync.Mutex
	sent   []sentMail
	err    error
	errFor map[string]error
	delay  time.Duration
	onSend func(to string)
	calls  atomic.Int64
}

func (m *deliveryMailer) SendPasswordReset(string, string, string) error { return nil }

func (m *deliveryMailer) Send(to, subject, body string) error {
	m.calls.Add(1)
	m.mu.Lock()
	m.sent = append(m.sent, sentMail{To: to, Subject: subject, Body: body})
	err := m.err
	if specific, ok := m.errFor[to]; ok {
		err = specific
	}
	hook, delay := m.onSend, m.delay
	m.mu.Unlock()
	if hook != nil {
		hook(to)
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	return err
}

func (m *deliveryMailer) set(fn func(m *deliveryMailer)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m)
}

// clear restores a mailer that delivers everything at once.
func (m *deliveryMailer) clear() {
	m.set(func(m *deliveryMailer) {
		m.err, m.errFor, m.delay, m.onSend = nil, nil, 0, nil
	})
}

func (m *deliveryMailer) reset() {
	m.set(func(m *deliveryMailer) { m.sent = nil })
}

func (m *deliveryMailer) to(address string) []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sentMail
	for _, msg := range m.sent {
		if msg.To == address {
			out = append(out, msg)
		}
	}
	return out
}

func (m *deliveryMailer) all() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]sentMail, len(m.sent))
	copy(out, m.sent)
	return out
}

// deliveryClock is the injectable service clock.
type deliveryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *deliveryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *deliveryClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func newDeliveryFixture(t *testing.T, db *gorm.DB, opts ...FormServiceOption) (*formFixture, *deliveryMailer) {
	t.Helper()
	f := newFormFixtureOn(t, config.FormsConfig{PublicBaseURL: formTestPublicBase}, db)
	m := &deliveryMailer{}
	f.wireDeliveries(m, opts...)
	return f, m
}

// wireDeliveries rebuilds the service around m; an untyped nil means no mailer.
func (f *formFixture) wireDeliveries(m mailer.Mailer, opts ...FormServiceOption) {
	f.service = NewFormService(f.repo, f.leadRepo, f.userRepo, m,
		utils.NewTransactionManager(f.db), f.cfg, formTestAPIPrefix, opts...)
}

// deliveryForm publishes the contact form with two notification addresses and
// a follow-up mail.
func (f *formFixture) deliveryForm(t *testing.T, optIn bool) *models.Form {
	t.Helper()
	form := f.newForm()
	form.DoubleOptIn = optIn
	form.FollowUpSubject = "Your guide"
	form.FollowUpBody = "Download it here: {content_link}"
	form.ContentURL = "https://files.example/guide.pdf"
	return f.publish(t, form)
}

func submissionFrom(email string) *PublicSubmissionRequest {
	req := validSubmission()
	req.Values["email"] = email
	return req
}

func latestSubmission(t *testing.T, db *gorm.DB, formID uint) *models.FormSubmission {
	t.Helper()
	var submission models.FormSubmission
	require.NoError(t, db.Unscoped().Where("form_id = ?", formID).Order("id desc").First(&submission).Error)
	return &submission
}

func deliveriesOf(t *testing.T, db *gorm.DB, submissionID uint) []models.FormMailDelivery {
	t.Helper()
	var rows []models.FormMailDelivery
	require.NoError(t, db.Unscoped().Where("submission_id = ?", submissionID).Order("id").Find(&rows).Error)
	return rows
}

func deliveryByID(t *testing.T, db *gorm.DB, id uint) models.FormMailDelivery {
	t.Helper()
	var row models.FormMailDelivery
	require.NoError(t, db.Unscoped().First(&row, id).Error)
	return row
}

func deliveryKinds(rows []models.FormMailDelivery) []string {
	kinds := make([]string, 0, len(rows))
	for _, row := range rows {
		kinds = append(kinds, row.Kind)
	}
	return kinds
}

func countDeliveries(t *testing.T, db *gorm.DB, query string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Unscoped().Model(&models.FormMailDelivery{}).Where(query, args...).Count(&count).Error)
	return count
}

// assertNeverSending checks the row ended with an outcome, never in flight.
func assertNeverSending(t *testing.T, rows []models.FormMailDelivery) {
	t.Helper()
	for _, row := range rows {
		assert.NotEqualf(t, models.FormMailStatusSending, row.Status, "delivery %d was left sending", row.ID)
		assert.NotEqualf(t, models.FormMailStatusPending, row.Status, "delivery %d was left pending", row.ID)
	}
}

// cancelOnClaimRepo cancels the request context the moment the first
// post-commit statement, the claim, is issued: right after the commit.
type cancelOnClaimRepo struct {
	repository.FormRepository
	cancel context.CancelFunc
}

func (r *cancelOnClaimRepo) WithTx(tx *gorm.DB) repository.FormRepository {
	return &cancelOnClaimRepo{FormRepository: r.FormRepository.WithTx(tx), cancel: r.cancel}
}

func (r *cancelOnClaimRepo) ClaimPendingDelivery(id uint, now time.Time) error {
	r.cancel()
	return r.FormRepository.ClaimPendingDelivery(id, now)
}

// deliveryFailingLeadRepo makes every lead write fail, so the submission
// transaction rolls back.
type deliveryFailingLeadRepo struct {
	repository.LeadRepository
}

func (r deliveryFailingLeadRepo) WithTx(tx *gorm.DB) repository.LeadRepository {
	return deliveryFailingLeadRepo{LeadRepository: r.LeadRepository.WithTx(tx)}
}

func (r deliveryFailingLeadRepo) Create(*models.Lead) error {
	return errors.New("injected lead insert fault")
}

func (r deliveryFailingLeadRepo) Update(*models.Lead) error {
	return errors.New("injected lead update fault")
}

// countingDeliveryRepo counts delivery reads.
type countingDeliveryRepo struct {
	repository.FormRepository
	reads *atomic.Int64
}

func (r *countingDeliveryRepo) WithTx(tx *gorm.DB) repository.FormRepository {
	return &countingDeliveryRepo{FormRepository: r.FormRepository.WithTx(tx), reads: r.reads}
}

func (r *countingDeliveryRepo) GetDeliveryByID(id uint) (*models.FormMailDelivery, error) {
	r.reads.Add(1)
	return r.FormRepository.GetDeliveryByID(id)
}

// ------------------------------------------------------------------ AC-1 ---

func TestFormMailDeliveryRecordedSQLite(t *testing.T) {
	runFormMailDeliveryRecorded(t, openFormTestSQLite(t))
}

func TestFormMailDeliveryRecordedMySQL(t *testing.T) {
	runFormMailDeliveryRecorded(t, openFormTestMySQL(t, 3))
}

func runFormMailDeliveryRecorded(t *testing.T, db *gorm.DB) {
	f, m := newDeliveryFixture(t, db)
	ctx := context.Background()

	t.Run("a received submission records a notification per address and a follow-up, all sent", func(t *testing.T) {
		m.clear()
		form := f.deliveryForm(t, false)
		outcome, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
		assert.Equal(t, submitOutcome(form), outcome, "the public answer is unchanged")

		rows := deliveriesOf(t, db, latestSubmission(t, db, form.ID).ID)
		require.Len(t, rows, 3)
		assert.Equal(t, []string{models.FormMailKindNotification, models.FormMailKindNotification, models.FormMailKindFollowUp}, deliveryKinds(rows))
		for _, row := range rows {
			assert.Equal(t, models.FormMailStatusSent, row.Status)
			assert.Equal(t, 1, row.Attempts)
			assert.NotNil(t, row.SentAt)
			assert.NotNil(t, row.LastAttemptAt)
			assert.Empty(t, row.ErrorCode)
			assert.Equal(t, form.ID, row.FormID)
		}
		assert.NotEmpty(t, rows[0].RecipientHash)
		assert.NotEqual(t, rows[0].RecipientHash, rows[1].RecipientHash, "one row per address")
		assert.Empty(t, rows[2].RecipientHash, "the submitter's mail stores no hash")
		assert.Len(t, m.to("sales@example.com"), 1)
		assert.Len(t, m.to("support@example.com"), 1)
		assert.Len(t, m.to("ada@example.com"), 1)
	})

	t.Run("a pending submission records the confirmation and the confirmation records the rest", func(t *testing.T) {
		m.clear()
		m.reset()
		form := f.deliveryForm(t, true)
		outcome, err := f.service.SubmitPublic(ctx, form.PublicID, submissionFrom("optin@example.com"), submissionMeta())
		require.NoError(t, err)
		assert.Equal(t, submitOutcome(form), outcome)

		submission := latestSubmission(t, db, form.ID)
		rows := deliveriesOf(t, db, submission.ID)
		require.Len(t, rows, 1)
		assert.Equal(t, models.FormMailKindConfirmation, rows[0].Kind)
		assert.Equal(t, models.FormMailStatusSent, rows[0].Status)

		mails := m.to("optin@example.com")
		require.Len(t, mails, 1)
		require.NoError(t, f.service.ConfirmSubmission(ctx, tokenFromLink(t, mails[0].Body)))
		rows = deliveriesOf(t, db, submission.ID)
		assert.Equal(t, []string{models.FormMailKindConfirmation, models.FormMailKindNotification, models.FormMailKindNotification, models.FormMailKindFollowUp}, deliveryKinds(rows))
		for _, row := range rows {
			assert.Equal(t, models.FormMailStatusSent, row.Status)
		}
	})

	t.Run("a Send error is recorded failed with its code", func(t *testing.T) {
		m.clear()
		m.set(func(m *deliveryMailer) {
			m.errFor = map[string]error{
				"sales@example.com":   fmt.Errorf("dial relay: %w", os.ErrDeadlineExceeded),
				"support@example.com": fmt.Errorf("recipient: %w", mailer.ErrInvalidAddress),
				"ada@example.com":     errors.New("relay said no"),
			}
		})
		form := f.deliveryForm(t, false)
		outcome, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err, "a delivery failure never fails the submission")
		assert.Equal(t, submitOutcome(form), outcome)

		rows := deliveriesOf(t, db, latestSubmission(t, db, form.ID).ID)
		require.Len(t, rows, 3)
		codes := []string{models.FormMailErrorTimeout, models.FormMailErrorInvalidRecipient, models.FormMailErrorMailFailed}
		for i, row := range rows {
			assert.Equal(t, models.FormMailStatusFailed, row.Status)
			assert.Equal(t, codes[i], row.ErrorCode)
			assert.Equal(t, 1, row.Attempts)
			assert.Nil(t, row.SentAt)
		}

		m.set(func(m *deliveryMailer) {
			m.errFor = map[string]error{"sales@example.com": fmt.Errorf("subject: %w", mailer.ErrInvalidHeader)}
		})
		_, err = f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
		rows = deliveriesOf(t, db, latestSubmission(t, db, form.ID).ID)
		assert.Equal(t, models.FormMailErrorInvalidRecipient, rows[0].ErrorCode)
		assert.Equal(t, models.FormMailStatusSent, rows[1].Status)
	})

	t.Run("no mailer, or no recipient, is skipped", func(t *testing.T) {
		m.clear()
		form := f.deliveryForm(t, false)
		f.wireDeliveries(nil)
		outcome, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
		f.wireDeliveries(m)
		require.NoError(t, err)
		assert.Equal(t, submitOutcome(form), outcome)
		for _, row := range deliveriesOf(t, db, latestSubmission(t, db, form.ID).ID) {
			assert.Equal(t, models.FormMailStatusSkipped, row.Status)
			assert.Equal(t, models.FormMailErrorNoMailer, row.ErrorCode)
		}

		optional := &models.Form{
			Name:   "Optional address",
			Status: models.FormStatusPublished,
			Fields: []models.FormFieldDef{
				{Name: "email", Label: "Email", Type: models.FormFieldEmail},
				{Name: "message", Label: "Message", Type: models.FormFieldTextarea, Required: true},
			},
			NotifyEmails:    []string{"sales@example.com"},
			FollowUpSubject: "Your guide",
		}
		f.publish(t, optional)
		_, err = f.service.SubmitPublic(ctx, optional.PublicID, &PublicSubmissionRequest{
			Values:    map[string]string{"email": "", "message": "No address given"},
			Challenge: challengeAged(30 * time.Second),
		}, submissionMeta())
		require.NoError(t, err)
		rows := deliveriesOf(t, db, latestSubmission(t, db, optional.ID).ID)
		require.Len(t, rows, 2)
		assert.Equal(t, models.FormMailStatusSent, rows[0].Status)
		assert.Equal(t, models.FormMailKindFollowUp, rows[1].Kind)
		assert.Equal(t, models.FormMailStatusSkipped, rows[1].Status)
		assert.Equal(t, models.FormMailErrorNoRecipient, rows[1].ErrorCode)
	})

	t.Run("a Send outlasting the bound, with the request cancelled after the commit, still ends stamped", func(t *testing.T) {
		const bound = 100 * time.Millisecond
		m.clear()
		m.set(func(m *deliveryMailer) {
			m.delay = 3 * bound
			m.errFor = map[string]error{"support@example.com": fmt.Errorf("write: %w", os.ErrDeadlineExceeded)}
		})
		defer m.clear()
		form := f.deliveryForm(t, false)

		reqCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		f.service = NewFormService(&cancelOnClaimRepo{FormRepository: f.repo, cancel: cancel}, f.leadRepo, f.userRepo, m,
			utils.NewTransactionManager(f.db), f.cfg, formTestAPIPrefix, WithFormMailStampTimeout(bound))
		defer f.wireDeliveries(m)

		outcome, err := f.service.SubmitPublic(reqCtx, form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
		assert.Equal(t, submitOutcome(form), outcome)
		assert.ErrorIs(t, reqCtx.Err(), context.Canceled, "the request was cancelled right after the commit")

		rows := deliveriesOf(t, db, latestSubmission(t, db, form.ID).ID)
		require.Len(t, rows, 3)
		assertNeverSending(t, rows)
		assert.Equal(t, models.FormMailStatusSent, rows[0].Status)
		assert.Equal(t, models.FormMailStatusFailed, rows[1].Status)
		assert.Equal(t, models.FormMailErrorTimeout, rows[1].ErrorCode)
		assert.Equal(t, models.FormMailStatusSent, rows[2].Status)
	})

	t.Run("spam and a rolled-back submission leave no row", func(t *testing.T) {
		m.clear()
		form := f.deliveryForm(t, false)
		spam := validSubmission()
		spam.Honeypot = "filled by a bot"
		outcome, err := f.service.SubmitPublic(ctx, form.PublicID, spam, submissionMeta())
		require.NoError(t, err)
		assert.Equal(t, submitOutcome(form), outcome, "spam is answered like a genuine submission")
		spamRow := latestSubmission(t, db, form.ID)
		require.Equal(t, models.FormSubmissionSpam, spamRow.Status)
		assert.Zero(t, countDeliveries(t, db, "submission_id = ?", spamRow.ID))

		rollback := f.deliveryForm(t, false)
		f.wire(f.repo, deliveryFailingLeadRepo{LeadRepository: f.leadRepo}, m)
		defer f.wireDeliveries(m)
		_, err = f.service.SubmitPublic(ctx, rollback.PublicID, submissionFrom("rollback@example.com"), submissionMeta())
		require.Error(t, err)
		assert.Zero(t, countDeliveries(t, db, "form_id = ?", rollback.ID), "the rolled-back transaction wrote no row")
		var submissions int64
		require.NoError(t, db.Unscoped().Model(&models.FormSubmission{}).Where("form_id = ?", rollback.ID).Count(&submissions).Error)
		assert.Zero(t, submissions)
	})
}

// ------------------------------------------------------------------ AC-2 ---

func TestFormMailDeliveryLogOnlyRecorded(t *testing.T) {
	db := openFormTestSQLite(t)
	f, m := newDeliveryFixture(t, db)
	ctx := context.Background()
	f.wireDeliveries(mailer.NewLogMailer())

	form := f.deliveryForm(t, false)
	_, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	rows := deliveriesOf(t, db, latestSubmission(t, db, form.ID).ID)
	require.Len(t, rows, 3)
	for _, row := range rows {
		assert.Equal(t, models.FormMailStatusLogOnly, row.Status, "the LogMailer delivers nothing")
		assert.Nil(t, row.SentAt)
		assert.Empty(t, row.ErrorCode)
		assert.Equal(t, 1, row.Attempts)
	}

	optIn := f.deliveryForm(t, true)
	_, err = f.service.SubmitPublic(ctx, optIn.PublicID, submissionFrom("logonly@example.com"), submissionMeta())
	require.NoError(t, err)
	confirmation := deliveriesOf(t, db, latestSubmission(t, db, optIn.ID).ID)
	require.Len(t, confirmation, 1)
	assert.Equal(t, models.FormMailStatusLogOnly, confirmation[0].Status)
	assert.Nil(t, confirmation[0].SentAt)

	// A log_only row is retryable; through the LogMailer it stays log_only.
	retried, err := f.service.RetryDelivery(ctx, rows[0].ID)
	require.NoError(t, err)
	assert.Equal(t, models.FormMailStatusLogOnly, retried.Status)
	assert.Nil(t, retried.SentAt)
	stored := deliveryByID(t, db, rows[0].ID)
	assert.Equal(t, models.FormMailStatusLogOnly, stored.Status)
	assert.Nil(t, stored.SentAt)
	assert.Equal(t, 2, stored.Attempts)

	// Once a real transport is wired the same row goes out.
	f.wireDeliveries(m)
	retried, err = f.service.RetryDelivery(ctx, rows[0].ID)
	require.NoError(t, err)
	assert.Equal(t, models.FormMailStatusSent, retried.Status)
	assert.NotNil(t, retried.SentAt)
	assert.Len(t, m.to("sales@example.com"), 1)
}

// ------------------------------------------------------------------ AC-4 ---

func TestFormMailDeliveryRetrySQLite(t *testing.T) {
	runFormMailDeliveryRetry(t, openFormTestSQLite(t))
}

func TestFormMailDeliveryRetryMySQL(t *testing.T) {
	runFormMailDeliveryRetry(t, openFormTestMySQL(t, 4))
}

func runFormMailDeliveryRetry(t *testing.T, db *gorm.DB) {
	f, m := newDeliveryFixture(t, db)
	ctx := context.Background()
	form := f.deliveryForm(t, false)

	// failedSubmission stores a submission whose three mails all failed.
	failedSubmission := func(t *testing.T, form *models.Form) (*models.FormSubmission, []models.FormMailDelivery) {
		t.Helper()
		m.clear()
		m.set(func(m *deliveryMailer) { m.err = errors.New("relay unavailable") })
		_, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
		m.clear()
		require.NoError(t, err)
		submission := latestSubmission(t, db, form.ID)
		rows := deliveriesOf(t, db, submission.ID)
		require.Len(t, rows, 3)
		for _, row := range rows {
			require.Equal(t, models.FormMailStatusFailed, row.Status)
		}
		return submission, rows
	}
	assertUnchanged := func(t *testing.T, before models.FormMailDelivery) {
		t.Helper()
		after := deliveryByID(t, db, before.ID)
		assert.Equal(t, before.Status, after.Status)
		assert.Equal(t, before.Attempts, after.Attempts)
		assert.Equal(t, before.ErrorCode, after.ErrorCode)
		assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "nothing was written")
	}
	setColumn := func(t *testing.T, model interface{}, id uint, column string, value interface{}) {
		t.Helper()
		require.NoError(t, db.Model(model).Where("id = ?", id).UpdateColumn(column, value).Error)
	}

	submission, rows := failedSubmission(t, form)
	sales, support, followUp := rows[0], rows[1], rows[2]

	t.Run("a context carrying a transaction is refused before any read", func(t *testing.T) {
		var reads atomic.Int64
		svc := NewFormService(&countingDeliveryRepo{FormRepository: f.repo, reads: &reads}, f.leadRepo, f.userRepo, m,
			utils.NewTransactionManager(db), f.cfg, formTestAPIPrefix)
		require.NoError(t, utils.NewTransactionManager(db).WithTransaction(ctx, func(txCtx context.Context) error {
			_, err := svc.RetryDelivery(txCtx, sales.ID)
			assert.ErrorIs(t, err, utils.ErrTransactionRootRequired)
			return nil
		}))
		assert.Zero(t, reads.Load())
		assertUnchanged(t, sales)
	})

	t.Run("a send failure stamps failed and matches ErrMailDelivery", func(t *testing.T) {
		m.set(func(m *deliveryMailer) { m.err = errors.New("relay still unavailable") })
		defer m.clear()
		delivery, err := f.service.RetryDelivery(ctx, sales.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.ErrMailDelivery)
		require.NotNil(t, delivery)
		assert.Equal(t, models.FormMailStatusFailed, delivery.Status)
		stored := deliveryByID(t, db, sales.ID)
		assert.Equal(t, models.FormMailStatusFailed, stored.Status)
		assert.Equal(t, models.FormMailErrorMailFailed, stored.ErrorCode)
		assert.Equal(t, 2, stored.Attempts)
	})

	t.Run("a notification retry sends once to its address, rendered from the current form", func(t *testing.T) {
		m.reset()
		before := m.calls.Load()
		require.NoError(t, db.Model(&models.Form{}).Where("id = ?", form.ID).UpdateColumn("name", "Renamed form").Error)
		delivery, err := f.service.RetryDelivery(ctx, sales.ID)
		require.NoError(t, err)
		assert.Equal(t, models.FormMailStatusSent, delivery.Status)
		assert.Equal(t, 3, delivery.Attempts)
		assert.NotNil(t, delivery.SentAt)
		assert.False(t, delivery.Retryable)
		assert.EqualValues(t, 1, m.calls.Load()-before)
		mails := m.to("sales@example.com")
		require.Len(t, mails, 1)
		assert.Equal(t, "New submission: Renamed form", mails[0].Subject)
		assert.Contains(t, mails[0].Body, fmt.Sprintf("Submission ID: %d", submission.ID))
		stored := deliveryByID(t, db, sales.ID)
		assert.Equal(t, models.FormMailStatusSent, stored.Status)
		assert.Empty(t, stored.ErrorCode)
	})

	t.Run("refusals write nothing and send nothing", func(t *testing.T) {
		before := m.calls.Load()
		refused := func(t *testing.T, row models.FormMailDelivery) {
			t.Helper()
			snapshot := deliveryByID(t, db, row.ID)
			_, err := f.service.RetryDelivery(ctx, row.ID)
			assert.ErrorIs(t, err, ErrFormDeliveryNotRetryable)
			assertUnchanged(t, snapshot)
		}

		// A sent row.
		refused(t, sales)

		// A skipped row, and pending and sending rows that are not stuck.
		for _, status := range []string{models.FormMailStatusSkipped, models.FormMailStatusPending, models.FormMailStatusSending} {
			row := &models.FormMailDelivery{
				CreatedAt:    time.Now().UTC(),
				UpdatedAt:    time.Now().UTC(),
				SubmissionID: submission.ID,
				FormID:       form.ID,
				Kind:         models.FormMailKindFollowUp,
				Status:       status,
			}
			require.NoError(t, f.repo.CreateDelivery(row))
			refused(t, *row)
		}

		// A follow-up whose form no longer has a follow-up subject.
		setColumn(t, &models.Form{}, form.ID, "follow_up_subject", "")
		refused(t, followUp)
		setColumn(t, &models.Form{}, form.ID, "follow_up_subject", "Your guide")

		// A follow-up whose submission is neither received nor confirmed.
		setColumn(t, &models.FormSubmission{}, submission.ID, "status", models.FormSubmissionPending)
		refused(t, followUp)
		setColumn(t, &models.FormSubmission{}, submission.ID, "status", models.FormSubmissionReceived)

		// A notification whose address left notify_emails.
		setColumn(t, &models.Form{}, form.ID, "notify_emails", `["sales@example.com","new@example.com"]`)
		refused(t, support)
		setColumn(t, &models.Form{}, form.ID, "notify_emails", `["sales@example.com","support@example.com"]`)

		// A delivery of a soft-deleted form.
		deleted := f.deliveryForm(t, false)
		_, deletedRows := failedSubmission(t, deleted)
		before = m.calls.Load()
		require.NoError(t, f.service.Delete(deleted.ID))
		refused(t, deletedRows[0])
		refused(t, deletedRows[2])

		assert.EqualValues(t, before, m.calls.Load(), "no refusal reached the mailer")
	})

	t.Run("an unknown delivery and a vanished submission are not found", func(t *testing.T) {
		_, err := f.service.RetryDelivery(ctx, 999999)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)

		gone, goneRows := failedSubmission(t, form)
		require.NoError(t, db.Unscoped().Delete(&models.FormSubmission{}, gone.ID).Error)
		_, err = f.service.RetryDelivery(ctx, goneRows[0].ID)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		assertUnchanged(t, goneRows[0])
	})

	t.Run("the follow-up and the remaining notification go out", func(t *testing.T) {
		delivery, err := f.service.RetryDelivery(ctx, followUp.ID)
		require.NoError(t, err)
		assert.Equal(t, models.FormMailStatusSent, delivery.Status)
		mails := m.to("ada@example.com")
		require.NotEmpty(t, mails)
		assert.Equal(t, "Your guide", mails[len(mails)-1].Subject)
		assert.Equal(t, "Download it here: https://files.example/guide.pdf", mails[len(mails)-1].Body)
	})

	t.Run("two concurrent retries of one row send once", func(t *testing.T) {
		before := m.calls.Load()
		release := make(chan struct{})
		m.set(func(m *deliveryMailer) {
			m.onSend = func(string) { <-release }
		})
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				_, err := f.service.RetryDelivery(ctx, support.ID)
				results <- err
			}()
		}
		// The winner is parked inside Send, so the first answer is the loser's.
		var first error
		select {
		case first = <-results:
		case <-time.After(confirmationTimeout):
			close(release)
			t.Fatal("neither retry returned while the winner was sending")
		}
		close(release)
		var second error
		select {
		case second = <-results:
		case <-time.After(confirmationTimeout):
			t.Fatal("the winner never returned")
		}
		m.clear()
		assert.ErrorIs(t, first, ErrFormDeliveryNotRetryable, "the loser is refused")
		assert.NoError(t, second, "the winner sends")
		assert.EqualValues(t, 1, m.calls.Load()-before, "Mailer.Send ran exactly once")
		assert.Equal(t, models.FormMailStatusSent, deliveryByID(t, db, support.ID).Status)
	})

	t.Run("a Send outlasting a short bound still ends sent or failed/timeout", func(t *testing.T) {
		const bound = 100 * time.Millisecond
		f.wireDeliveries(m, WithFormMailStampTimeout(bound))
		defer f.wireDeliveries(m)
		_, slow := failedSubmission(t, form)

		m.set(func(m *deliveryMailer) { m.delay = 3 * bound })
		delivery, err := f.service.RetryDelivery(ctx, slow[0].ID)
		require.NoError(t, err)
		assert.Equal(t, models.FormMailStatusSent, delivery.Status)

		m.set(func(m *deliveryMailer) { m.err = fmt.Errorf("read reply: %w", os.ErrDeadlineExceeded) })
		_, err = f.service.RetryDelivery(ctx, slow[1].ID)
		m.clear()
		assert.ErrorIs(t, err, apperrors.ErrMailDelivery)
		stored := deliveryByID(t, db, slow[1].ID)
		assert.Equal(t, models.FormMailStatusFailed, stored.Status)
		assert.Equal(t, models.FormMailErrorTimeout, stored.ErrorCode)
		assertNeverSending(t, []models.FormMailDelivery{deliveryByID(t, db, slow[0].ID), stored})
	})

	t.Run("a row purged during the send is logged and the send's outcome returned", func(t *testing.T) {
		_, vanishing := failedSubmission(t, form)
		target := vanishing[0]
		_, hook := capturePrivacyLogs(t)
		m.set(func(m *deliveryMailer) {
			m.onSend = func(string) {
				require.NoError(t, db.Unscoped().Delete(&models.FormMailDelivery{}, target.ID).Error)
			}
		})
		delivery, err := f.service.RetryDelivery(ctx, target.ID)
		m.clear()
		require.NoError(t, err, "the send succeeded, and that is what is returned")
		assert.Equal(t, models.FormMailStatusSent, delivery.Status)
		assert.Zero(t, countDeliveries(t, db, "id = ?", target.ID))

		var found bool
		for _, entry := range hook.AllEntries() {
			if entry.Message == "Form mail delivery row vanished before its outcome was recorded" {
				found = true
				assert.Equal(t, "warning", entry.Level.String())
				assert.Equal(t, target.ID, entry.Data["delivery_id"])
				assert.Equal(t, target.SubmissionID, entry.Data["submission_id"])
				assert.Equal(t, target.FormID, entry.Data["form_id"])
			}
		}
		assert.True(t, found, "a Warn line with the ids is logged")
	})
}

// ------------------------------------------------------------------ AC-5 ---

func TestFormMailDeliveryConfirmationRetrySQLite(t *testing.T) {
	runFormMailDeliveryConfirmationRetry(t, openFormTestSQLite(t))
}

func TestFormMailDeliveryConfirmationRetryMySQL(t *testing.T) {
	runFormMailDeliveryConfirmationRetry(t, openFormTestMySQL(t, 3))
}

func runFormMailDeliveryConfirmationRetry(t *testing.T, db *gorm.DB) {
	f, m := newDeliveryFixture(t, db)
	ctx := context.Background()
	form := f.deliveryForm(t, true)

	// submitPending stores a double-opt-in submission and returns it, its
	// confirmation row and the token its mail carried.
	submitPending := func(t *testing.T, email string, fail bool) (*models.FormSubmission, models.FormMailDelivery, string) {
		t.Helper()
		m.clear()
		m.reset()
		if fail {
			m.set(func(m *deliveryMailer) { m.err = errors.New("relay unavailable") })
		}
		_, err := f.service.SubmitPublic(ctx, form.PublicID, submissionFrom(email), submissionMeta())
		m.clear()
		require.NoError(t, err)
		submission := latestSubmission(t, db, form.ID)
		rows := deliveriesOf(t, db, submission.ID)
		require.Len(t, rows, 1)
		require.Equal(t, models.FormMailKindConfirmation, rows[0].Kind)
		mails := m.to(email)
		require.Len(t, mails, 1)
		return submission, rows[0], tokenFromLink(t, mails[0].Body)
	}
	tokensOf := func(t *testing.T, submissionID uint) []models.FormConfirmationToken {
		t.Helper()
		var tokens []models.FormConfirmationToken
		require.NoError(t, db.Unscoped().Where("submission_id = ?", submissionID).Order("id").Find(&tokens).Error)
		return tokens
	}
	type tokenState struct {
		ID      uint
		Hash    string
		Used    bool
		Expires int64
	}
	allTokens := func(t *testing.T) []tokenState {
		t.Helper()
		var tokens []models.FormConfirmationToken
		require.NoError(t, db.Unscoped().Order("id").Find(&tokens).Error)
		out := make([]tokenState, 0, len(tokens))
		for _, token := range tokens {
			out = append(out, tokenState{ID: token.ID, Hash: token.TokenHash, Used: token.UsedAt != nil, Expires: token.ExpiresAt.Unix()})
		}
		return out
	}
	refused := func(t *testing.T, row models.FormMailDelivery) {
		t.Helper()
		tokensBefore := allTokens(t)
		calls := m.calls.Load()
		_, err := f.service.RetryDelivery(ctx, row.ID)
		assert.ErrorIs(t, err, ErrFormDeliveryNotRetryable)
		assert.Equal(t, tokensBefore, allTokens(t), "the rollback leaves every token as it was")
		assert.Equal(t, calls, m.calls.Load(), "nothing is mailed")
		assert.Equal(t, row.Status, deliveryByID(t, db, row.ID).Status)
	}

	t.Run("a retry re-arms the single token in place and mails a link that confirms", func(t *testing.T) {
		submission, row, oldToken := submitPending(t, "retry@example.com", true)
		require.Equal(t, models.FormMailStatusFailed, row.Status)
		before := tokensOf(t, submission.ID)
		require.Len(t, before, 1)
		totalBefore := len(allTokens(t))

		m.reset()
		delivery, err := f.service.RetryDelivery(ctx, row.ID)
		require.NoError(t, err)
		assert.Equal(t, models.FormMailStatusSent, delivery.Status)
		assert.Equal(t, 2, delivery.Attempts)
		mails := m.to("retry@example.com")
		require.Len(t, mails, 1)
		newToken := tokenFromLink(t, mails[0].Body)
		assert.NotEqual(t, oldToken, newToken)

		after := tokensOf(t, submission.ID)
		require.Len(t, after, 1, "no token row is inserted")
		assert.Equal(t, before[0].ID, after[0].ID, "the same row is re-armed")
		assert.NotEqual(t, before[0].TokenHash, after[0].TokenHash)
		assert.Nil(t, after[0].UsedAt)
		assert.WithinDuration(t, time.Now().Add(formConfirmationTTL), after[0].ExpiresAt, time.Minute)
		assert.Len(t, allTokens(t), totalBefore)

		assert.ErrorIs(t, f.service.ConfirmSubmission(ctx, oldToken), ErrInvalidConfirmationToken, "the old link is dead")
		require.NoError(t, f.service.ConfirmSubmission(ctx, newToken), "the new link confirms")
		stored, err := f.service.GetSubmission(submission.ID)
		require.NoError(t, err)
		assert.Equal(t, models.FormSubmissionConfirmed, stored.Status)
	})

	t.Run("a confirmed submission is refused", func(t *testing.T) {
		_, row, token := submitPending(t, "confirmed@example.com", true)
		require.NoError(t, f.service.ConfirmSubmission(ctx, token))
		refused(t, row)
	})

	t.Run("a submission now linked to a lead is refused", func(t *testing.T) {
		submission, row, _ := submitPending(t, "linked@example.com", true)
		lead := &models.Lead{FirstName: "Linked", LastName: "Lead", Email: "linked-lead@example.com", OwnerID: f.owner.ID}
		require.NoError(t, db.Create(lead).Error)
		require.NoError(t, db.Model(&models.FormSubmission{}).Where("id = ?", submission.ID).UpdateColumn("lead_id", lead.ID).Error)
		refused(t, row)
	})

	t.Run("a newer non-spam submission of the same address is refused", func(t *testing.T) {
		_, rowA, _ := submitPending(t, "again@example.com", true)
		submissionB, _, tokenB := submitPending(t, "again@example.com", false)
		require.NoError(t, f.service.ConfirmSubmission(ctx, tokenB))
		beforeB := tokensOf(t, submissionB.ID)

		refused(t, rowA)

		storedB, err := f.service.GetSubmission(submissionB.ID)
		require.NoError(t, err)
		assert.Equal(t, models.FormSubmissionConfirmed, storedB.Status, "B is unchanged")
		afterB := tokensOf(t, submissionB.ID)
		require.Len(t, afterB, len(beforeB))
		assert.Equal(t, beforeB[0].TokenHash, afterB[0].TokenHash)
		assert.Equal(t, beforeB[0].UsedAt != nil, afterB[0].UsedAt != nil)
	})

	t.Run("a submission without exactly one token row is refused", func(t *testing.T) {
		two, rowTwo, _ := submitPending(t, "two-tokens@example.com", true)
		require.NoError(t, db.Create(&models.FormConfirmationToken{
			SubmissionID: two.ID,
			TokenHash:    strings.Repeat("b", 64),
			ExpiresAt:    time.Now().Add(time.Hour),
		}).Error)
		refused(t, rowTwo)

		none, rowNone, _ := submitPending(t, "no-token@example.com", true)
		require.NoError(t, db.Unscoped().Where("submission_id = ?", none.ID).Delete(&models.FormConfirmationToken{}).Error)
		refused(t, rowNone)
		assert.Empty(t, tokensOf(t, none.ID), "nothing was inserted")
	})

	t.Run("an expired pending submission before the purge cutoff is retried, kept and confirmed", func(t *testing.T) {
		submission, row, _ := submitPending(t, "late@example.com", true)
		old := time.Now().UTC().Add(-30 * 24 * time.Hour)
		require.NoError(t, db.Model(&models.FormSubmission{}).Where("id = ?", submission.ID).UpdateColumn("created_at", old).Error)
		require.NoError(t, db.Model(&models.FormConfirmationToken{}).Where("submission_id = ?", submission.ID).
			UpdateColumn("expires_at", time.Now().Add(-time.Hour)).Error)

		m.reset()
		_, err := f.service.RetryDelivery(ctx, row.ID)
		require.NoError(t, err)
		mails := m.to("late@example.com")
		require.Len(t, mails, 1)
		newToken := tokenFromLink(t, mails[0].Body)

		cutoff := time.Now().UTC().Add(-72 * time.Hour)
		var chunk repository.FormSubmissionPurgeChunk
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			var err error
			chunk, err = f.repo.WithTx(tx).PurgeSubmissionChunk(repository.FormSubmissionPurgeQuery{
				Status: models.FormSubmissionPending, Cutoff: cutoff, Now: time.Now(), Limit: 500,
			})
			return err
		}))
		assert.Zero(t, chunk.Submissions, "the re-armed token keeps the submission out of the purge")
		assert.Len(t, deliveriesOf(t, db, submission.ID), 1)

		require.NoError(t, f.service.ConfirmSubmission(ctx, newToken))
		stored, err := f.service.GetSubmission(submission.ID)
		require.NoError(t, err)
		assert.Equal(t, models.FormSubmissionConfirmed, stored.Status)
		assert.Len(t, tokensOf(t, submission.ID), 1, "still exactly one token row")
	})
}

// ------------------------------------------------------------------ AC-6 ---

func TestFormMailDeliveryStuckRows(t *testing.T) {
	db := openFormTestSQLite(t)
	clock := &deliveryClock{now: time.Now()}
	f, _ := newDeliveryFixture(t, db, WithFormMailClock(clock.Now))
	ctx := context.Background()
	form := f.deliveryForm(t, false)

	_, err := f.service.SubmitPublic(ctx, form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	submission := latestSubmission(t, db, form.ID)
	rows := deliveriesOf(t, db, submission.ID)
	require.Len(t, rows, 3)

	base := clock.Now()
	left := map[uint]string{rows[0].ID: models.FormMailStatusPending, rows[1].ID: models.FormMailStatusSending}
	for id, status := range left {
		require.NoError(t, db.Model(&models.FormMailDelivery{}).Where("id = ?", id).
			UpdateColumns(map[string]interface{}{"status": status, "updated_at": base.UTC()}).Error)
	}

	check := func(t *testing.T, stuck bool) {
		t.Helper()
		stored, err := f.service.GetSubmission(submission.ID)
		require.NoError(t, err)
		require.Len(t, stored.Deliveries, 3)
		for _, delivery := range stored.Deliveries {
			if _, ok := left[delivery.ID]; ok {
				assert.Equalf(t, stuck, delivery.Stuck, "stuck of %s", delivery.Status)
				assert.Equalf(t, stuck, delivery.Retryable, "retryable of %s", delivery.Status)
			} else {
				assert.False(t, delivery.Stuck)
				assert.False(t, delivery.Retryable, "a sent row is not retryable")
			}
		}

		list, total, err := f.service.ListDeliveries(FormMailDeliveryFilter{FormID: form.ID}, 0, 100)
		require.NoError(t, err)
		listed := map[uint]bool{}
		for _, delivery := range list {
			listed[delivery.ID] = true
			assert.True(t, delivery.Stuck)
			assert.True(t, delivery.Retryable)
		}
		for id := range left {
			assert.Equal(t, stuck, listed[id], "the default attention filter lists a stuck row")
		}
		if stuck {
			assert.EqualValues(t, 2, total)
		} else {
			assert.Zero(t, total)
		}

		pending, _, err := f.service.ListDeliveries(FormMailDeliveryFilter{FormID: form.ID, Status: models.FormMailStatusPending}, 0, 100)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, stuck, pending[0].Stuck)
	}

	for _, elapsed := range []time.Duration{14 * time.Minute, FormMailStuckAfter} {
		clock.set(base.Add(elapsed))
		check(t, false)
		for id, status := range left {
			_, err := f.service.RetryDelivery(ctx, id)
			assert.ErrorIsf(t, err, ErrFormDeliveryNotRetryable, "a %s row is refused after %s", status, elapsed)
			assert.Equal(t, status, deliveryByID(t, db, id).Status)
		}
	}

	clock.set(base.Add(FormMailStuckAfter + time.Minute))
	check(t, true)
	for id := range left {
		delivery, err := f.service.RetryDelivery(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, models.FormMailStatusSent, delivery.Status)
		assert.False(t, delivery.Stuck)
	}
}

// ------------------------------------------------------------------ AC-7 ---

func TestFormMailDeliveryLogsOmitPersonalData(t *testing.T) {
	db := openFormTestSQLite(t)
	f, m := newDeliveryFixture(t, db)
	ctx := context.Background()

	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })

	received := f.deliveryForm(t, false)
	m.set(func(m *deliveryMailer) {
		m.errFor = map[string]error{
			// A relay that quotes the address in its reply.
			"support@example.com": errors.New(`550 5.1.1 "support@example.com": recipient rejected`),
			"ada@example.com":     fmt.Errorf("data: %w", os.ErrDeadlineExceeded),
		}
	})
	_, err := f.service.SubmitPublic(ctx, received.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	receivedRows := deliveriesOf(t, db, latestSubmission(t, db, received.ID).ID)
	m.clear()

	optIn := f.deliveryForm(t, true)
	m.set(func(m *deliveryMailer) { m.err = errors.New("relay refused the message") })
	_, err = f.service.SubmitPublic(ctx, optIn.PublicID, submissionFrom("visitor@example.com"), submissionMeta())
	require.NoError(t, err)
	optInSubmission := latestSubmission(t, db, optIn.ID)
	confirmation := deliveriesOf(t, db, optInSubmission.ID)[0]
	failedLink := m.to("visitor@example.com")[0].Body
	m.clear()

	// Retries: a refused one, a failing one and two successful ones (the
	// confirmation's mails a fresh link, which is then confirmed).
	_, err = f.service.RetryDelivery(ctx, receivedRows[0].ID)
	require.ErrorIs(t, err, ErrFormDeliveryNotRetryable)
	m.set(func(m *deliveryMailer) { m.err = errors.New(`relay rejected "support@example.com"`) })
	_, err = f.service.RetryDelivery(ctx, receivedRows[1].ID)
	require.ErrorIs(t, err, apperrors.ErrMailDelivery)
	m.clear()
	_, err = f.service.RetryDelivery(ctx, receivedRows[2].ID)
	require.NoError(t, err)
	m.reset()
	_, err = f.service.RetryDelivery(ctx, confirmation.ID)
	require.NoError(t, err)
	freshLink := m.to("visitor@example.com")[0].Body
	freshToken := tokenFromLink(t, freshLink)
	require.NoError(t, f.service.ConfirmSubmission(ctx, freshToken))

	var hashes []string
	for _, row := range append(receivedRows, deliveriesOf(t, db, optInSubmission.ID)...) {
		if row.RecipientHash != "" {
			hashes = append(hashes, row.RecipientHash)
		}
	}
	require.NotEmpty(t, hashes)

	forbidden := append([]string{
		"ada@example.com", "sales@example.com", "support@example.com", "visitor@example.com",
		"New submission:", "Your guide", formDefaultConfirmationSubject,
		"A new submission arrived", "Download it here", "Please call me back.",
		formTestConfirmURL, "token=", tokenFromLink(t, failedLink), freshToken,
	}, hashes...)
	text := output.String()
	for _, needle := range forbidden {
		assert.NotContainsf(t, text, needle, "the logs carry %q", needle)
	}

	allowed := map[string]bool{
		"level": true, "msg": true, "time": true,
		"form_id": true, "submission_id": true, "delivery_id": true,
		"mail": true, "status": true, "error_code": true, "error": true,
	}
	deliveryLines, failureLines := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if _, ok := entry["delivery_id"]; !ok {
			continue
		}
		deliveryLines++
		for key := range entry {
			assert.Truef(t, allowed[key], "a delivery line carries %q: %s", key, line)
		}
		if entry["msg"] == "Form mail delivery failed" {
			failureLines++
			assert.NotEmpty(t, entry["error_code"])
			assert.NotEmpty(t, entry["error"], "the sanitised error is logged")
		}
	}
	assert.GreaterOrEqual(t, deliveryLines, 10, "submission, confirmation and retry all log their deliveries")
	assert.GreaterOrEqual(t, failureLines, 4)
	assert.Contains(t, text, "[redacted]", "a quoted relay reply is cut by utils.LogError")
}

// ----------------------------------------------------------------- AC-10 ---

func TestFormMailDeliveryErasedWithLeadSQLite(t *testing.T) {
	runFormMailDeliveryErasedWithLead(t, openFormTestSQLite(t))
}

func TestFormMailDeliveryErasedWithLeadMySQL(t *testing.T) {
	runFormMailDeliveryErasedWithLead(t, openFormTestMySQL(t, 3))
}

func runFormMailDeliveryErasedWithLead(t *testing.T, db *gorm.DB) {
	f, m := newDeliveryFixture(t, db)
	ctx := context.Background()
	form := f.deliveryForm(t, false)

	m.set(func(m *deliveryMailer) { m.err = errors.New("relay unavailable") })
	_, err := f.service.SubmitPublic(ctx, form.PublicID, submissionFrom("erased@example.com"), submissionMeta())
	require.NoError(t, err)
	erased := latestSubmission(t, db, form.ID)
	_, err = f.service.SubmitPublic(ctx, form.PublicID, submissionFrom("kept@example.com"), submissionMeta())
	require.NoError(t, err)
	kept := latestSubmission(t, db, form.ID)
	m.clear()

	erasedRows := deliveriesOf(t, db, erased.ID)
	keptRows := deliveriesOf(t, db, kept.ID)
	require.Len(t, erasedRows, 3)
	require.Len(t, keptRows, 3)
	require.NotNil(t, erased.LeadID)
	require.NotNil(t, kept.LeadID)
	require.NotEqual(t, *erased.LeadID, *kept.LeadID)

	require.NoError(t, f.leadRepo.Delete(*erased.LeadID))

	assert.Zero(t, countDeliveries(t, db, "submission_id = ?", erased.ID), "hard-deleted in the erasure")
	for _, row := range erasedRows {
		_, err := f.service.RetryDelivery(ctx, row.ID)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
	}
	list, _, err := f.service.ListDeliveries(FormMailDeliveryFilter{FormID: form.ID}, 0, 100)
	require.NoError(t, err)
	listed := map[uint]bool{}
	for _, row := range list {
		listed[row.ID] = true
	}
	for _, row := range erasedRows {
		assert.False(t, listed[row.ID], "the list no longer has delivery %d", row.ID)
	}
	for _, row := range keptRows {
		assert.True(t, listed[row.ID], "another submission's delivery %d survives", row.ID)
	}
	stored, err := f.service.GetSubmission(erased.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Deliveries)
	assert.Len(t, deliveriesOf(t, db, kept.ID), 3)
}
