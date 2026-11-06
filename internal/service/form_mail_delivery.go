package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Form mail delivery records (#216).
//
// Every form mail — the double-opt-in confirmation, the follow-up and one
// notification per NotifyEmails address — gets a form_mail_deliveries row,
// written pending in the submission or confirmation transaction, so a
// submission that rolls back (or is filed as spam) leaves no row. After the
// commit each row is claimed (pending → sending) and, once Mailer.Send has
// returned, stamped sent, failed, log_only or skipped. The claim and the stamp
// each run on their own context.WithoutCancel(ctx) with a fresh timeout created
// at that statement, so neither a cancelled request nor a slow relay can leave
// a row in sending; Send itself is bounded only by the mailer. A delivery
// failure is recorded and logged and never changes the public answer.
//
// The rows hold no address and no message content; a notification keeps an
// HMAC of its recipient so a retry can find the address again among the form's
// current NotifyEmails. Admin and sales retry a failed, log_only or stuck row
// through RetryDelivery: one WithTransactionAndRetry transaction ending in a
// conditional claim, then one send after the commit.

// FormMailStuckAfter is how long a row may stay pending or sending before it
// is reported stuck and becomes retryable.
const FormMailStuckAfter = 15 * time.Minute

const (
	// formMailStampTimeout bounds each post-commit claim and stamp by default.
	formMailStampTimeout = 5 * time.Second
	// formMailRetryRetries is how many times a retry transaction that lost a
	// deadlock or a lock wait is replayed.
	formMailRetryRetries = 3
	// formMailRecipientDomain separates the recipient HMAC from the token
	// hashes keyed with the same secret.
	formMailRecipientDomain = "form-mail-recipient\x00"
)

// ErrFormDeliveryNotRetryable is apperrors.ErrFormDeliveryNotRetryable, named
// here for the service's callers.
var ErrFormDeliveryNotRetryable = apperrors.ErrFormDeliveryNotRetryable

var formMailStatuses = map[string]bool{
	models.FormMailStatusPending: true,
	models.FormMailStatusSending: true,
	models.FormMailStatusSent:    true,
	models.FormMailStatusFailed:  true,
	models.FormMailStatusLogOnly: true,
	models.FormMailStatusSkipped: true,
}

var formMailKinds = map[string]bool{
	models.FormMailKindConfirmation: true,
	models.FormMailKindFollowUp:     true,
	models.FormMailKindNotification: true,
}

// FormMailDeliveryFilter narrows the attention list. Zero values do not
// filter; an empty Status selects failed, log_only and stuck rows.
type FormMailDeliveryFilter struct {
	FormID uint
	Status string
	Kind   string
}

// WithFormMailStampTimeout replaces the 5-second bound of each post-commit
// claim and stamp for this service instance.
func WithFormMailStampTimeout(d time.Duration) FormServiceOption {
	return func(s *formService) { s.mailStampTimeout = d }
}

// WithFormMailClock replaces the clock the delivery records are stamped and
// judged stuck with, for this service instance.
func WithFormMailClock(now func() time.Time) FormServiceOption {
	return func(s *formService) { s.mailClock = now }
}

func (s *formService) mailNow() time.Time {
	if s.mailClock != nil {
		return s.mailClock()
	}
	return time.Now()
}

func (s *formService) stampTimeout() time.Duration {
	if s.mailStampTimeout > 0 {
		return s.mailStampTimeout
	}
	return formMailStampTimeout
}

// formMailJob is one recorded delivery and the message it stands for. The
// message lives only in memory, between the commit and the send.
type formMailJob struct {
	delivery *models.FormMailDelivery
	to       string
	subject  string
	body     string
}

// ---------------------------------------------------------------------------
// Recording (inside the submission or confirmation transaction)
// ---------------------------------------------------------------------------

// recordAcceptedMail writes the pending rows of an accepted submission — one
// notification per non-blank NotifyEmails address, then the follow-up when the
// form has a follow-up subject — on the transaction-bound repository.
func (s *formService) recordAcceptedMail(repo repository.FormRepository, form *models.Form, submission *models.FormSubmission) ([]formMailJob, error) {
	var jobs []formMailJob
	if len(form.NotifyEmails) > 0 {
		subject, body := renderFormNotification(form, submission)
		for _, recipient := range form.NotifyEmails {
			if strings.TrimSpace(recipient) == "" {
				continue
			}
			job, err := s.recordMail(repo, form, submission, models.FormMailKindNotification, s.recipientHash(recipient), recipient, subject, body)
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
		}
	}
	if strings.TrimSpace(form.FollowUpSubject) != "" {
		subject, body := renderFormFollowUp(form)
		job, err := s.recordMail(repo, form, submission, models.FormMailKindFollowUp, "", submission.Email, subject, body)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// recordConfirmationMail writes the pending confirmation row of a double
// opt-in submission.
func (s *formService) recordConfirmationMail(repo repository.FormRepository, form *models.Form, submission *models.FormSubmission, rawToken string) ([]formMailJob, error) {
	subject, body := renderFormConfirmation(form, s.confirmationLink(rawToken))
	job, err := s.recordMail(repo, form, submission, models.FormMailKindConfirmation, "", submission.Email, subject, body)
	if err != nil {
		return nil, err
	}
	return []formMailJob{job}, nil
}

func (s *formService) recordMail(repo repository.FormRepository, form *models.Form, submission *models.FormSubmission, kind, recipientHash, to, subject, body string) (formMailJob, error) {
	// Written from the service clock in UTC, the zone every later stamp and the
	// stuck threshold use.
	now := s.mailNow().UTC()
	delivery := &models.FormMailDelivery{
		CreatedAt:     now,
		UpdatedAt:     now,
		SubmissionID:  submission.ID,
		FormID:        form.ID,
		Kind:          kind,
		Status:        models.FormMailStatusPending,
		RecipientHash: recipientHash,
	}
	if err := repo.CreateDelivery(delivery); err != nil {
		return formMailJob{}, err
	}
	return formMailJob{delivery: delivery, to: to, subject: subject, body: body}, nil
}

// recipientHash identifies a notification address without storing it.
func (s *formService) recipientHash(address string) string {
	return hashOpaqueToken(formMailRecipientDomain+strings.ToLower(strings.TrimSpace(address)), s.tokenSecret)
}

// ---------------------------------------------------------------------------
// Delivery (after the commit)
// ---------------------------------------------------------------------------

// deliverMail claims and sends every recorded job in order. Nothing it does is
// reported to the caller: the submission is stored and the outcome is on the
// row.
func (s *formService) deliverMail(ctx context.Context, jobs []formMailJob) {
	for i := range jobs {
		job := &jobs[i]
		if err := s.claimPendingDelivery(ctx, job.delivery); err != nil {
			continue
		}
		_ = s.sendAndStamp(ctx, job)
	}
}

// withMailStamp runs one post-commit statement on its own context: detached
// from the request, so a cancelled request cannot strand the row, and bounded
// by a timeout created here, so a held connection or lock cannot hang it.
func (s *formService) withMailStamp(ctx context.Context, fn func(repo repository.FormRepository) error) error {
	stampCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.stampTimeout())
	defer cancel()
	return fn(s.repo.WithTx(s.txManager.DB(stampCtx)))
}

func (s *formService) claimPendingDelivery(ctx context.Context, delivery *models.FormMailDelivery) error {
	now := s.mailNow()
	err := s.withMailStamp(ctx, func(repo repository.FormRepository) error {
		return repo.ClaimPendingDelivery(delivery.ID, now)
	})
	if err != nil {
		// Left pending: it becomes stuck, and with that retryable, after
		// FormMailStuckAfter. Nothing is sent without a claim.
		formMailLogger(delivery).WithError(utils.LogError(err)).
			Warn("Form mail delivery could not be claimed; it was not sent")
		return err
	}
	markClaimed(delivery, now)
	return nil
}

func markClaimed(delivery *models.FormMailDelivery, now time.Time) {
	stamp := now.UTC()
	delivery.Status = models.FormMailStatusSending
	delivery.Attempts++
	delivery.LastAttemptAt = &stamp
	delivery.UpdatedAt = stamp
}

// sendAndStamp sends one claimed job and records the outcome. It returns the
// send's error (nil for sent, log_only and skipped), whatever the stamp did.
func (s *formService) sendAndStamp(ctx context.Context, job *formMailJob) error {
	delivery := job.delivery
	var (
		outcome repository.FormMailDeliveryOutcome
		sendErr error
	)
	switch {
	case s.mailer == nil:
		outcome.Status, outcome.ErrorCode = models.FormMailStatusSkipped, models.FormMailErrorNoMailer
	case strings.TrimSpace(job.to) == "":
		outcome.Status, outcome.ErrorCode = models.FormMailStatusSkipped, models.FormMailErrorNoRecipient
	default:
		sendErr = s.mailer.Send(job.to, job.subject, job.body)
		switch {
		case sendErr != nil:
			outcome.Status, outcome.ErrorCode = models.FormMailStatusFailed, formMailErrorCode(sendErr)
		case s.mailerLogsOnly():
			outcome.Status = models.FormMailStatusLogOnly
		default:
			outcome.Status = models.FormMailStatusSent
		}
	}
	outcome.Now = s.mailNow()
	if outcome.Status == models.FormMailStatusSent {
		sentAt := outcome.Now.UTC()
		outcome.SentAt = &sentAt
	}

	stampErr := s.withMailStamp(ctx, func(repo repository.FormRepository) error {
		return repo.FinishDelivery(delivery.ID, outcome)
	})
	delivery.Status = outcome.Status
	delivery.ErrorCode = outcome.ErrorCode
	delivery.SentAt = outcome.SentAt
	delivery.UpdatedAt = outcome.Now.UTC()

	logger := formMailLogger(delivery)
	if sendErr != nil {
		logger.WithError(utils.LogError(sendErr)).Warn("Form mail delivery failed")
	} else {
		logger.Info("Form mail delivery recorded")
	}
	if stampErr != nil {
		if apperrors.IsNotFound(stampErr) {
			// Purged or erased between the claim and the stamp: there is no
			// row left to record the outcome on.
			logger.Warn("Form mail delivery row vanished before its outcome was recorded")
		} else {
			logger.WithError(utils.LogError(stampErr)).Warn("Form mail delivery outcome could not be recorded")
		}
	}
	return sendErr
}

func (s *formService) mailerLogsOnly() bool {
	reporter, ok := s.mailer.(mailer.LogOnlyReporter)
	return ok && reporter.LogsOnly()
}

// formMailErrorCode classifies a Send failure without keeping its text.
func formMailErrorCode(err error) string {
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return models.FormMailErrorTimeout
	case errors.Is(err, mailer.ErrInvalidAddress), errors.Is(err, mailer.ErrInvalidHeader):
		return models.FormMailErrorInvalidRecipient
	default:
		return models.FormMailErrorMailFailed
	}
}

// formMailLogger carries ids, the kind, the status and the error code only:
// never the address, the subject, the body, the link or the recipient hash.
func formMailLogger(delivery *models.FormMailDelivery) *logrus.Entry {
	return utils.Logger.WithFields(map[string]interface{}{
		"form_id":       delivery.FormID,
		"submission_id": delivery.SubmissionID,
		"delivery_id":   delivery.ID,
		"mail":          delivery.Kind,
		"status":        delivery.Status,
		"error_code":    delivery.ErrorCode,
	})
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (s *formService) confirmationLink(rawToken string) string {
	return s.confirmURL + "?token=" + url.QueryEscape(rawToken)
}

// renderFormConfirmation builds the double-opt-in mail around the link.
func renderFormConfirmation(form *models.Form, link string) (string, string) {
	subject := form.ConfirmationSubject
	if strings.TrimSpace(subject) == "" {
		subject = formDefaultConfirmationSubject
	}
	body := form.ConfirmationBody
	if strings.TrimSpace(body) == "" {
		body = formDefaultConfirmationBody
	}
	// A body an admin wrote without the placeholder would be a dead end, so the
	// link is appended rather than dropped.
	if !strings.Contains(body, formConfirmationLinkPlaceholder) {
		body = strings.TrimRight(body, "\n") + "\n\n" + formConfirmationLinkPlaceholder + "\n"
	}
	return subject, strings.ReplaceAll(body, formConfirmationLinkPlaceholder, link)
}

// renderFormFollowUp builds the post-submission mail, which is what carries a
// gated-content link. The caller checks that the form has a follow-up subject:
// an empty subject is how a form says it has no follow-up.
func renderFormFollowUp(form *models.Form) (string, string) {
	body := form.FollowUpBody
	if strings.TrimSpace(body) == "" {
		body = formDefaultFollowUpBody
	}
	return form.FollowUpSubject, strings.ReplaceAll(body, formContentLinkPlaceholder, form.ContentURL)
}

// renderFormNotification builds the staff notification, one copy per address.
func renderFormNotification(form *models.Form, submission *models.FormSubmission) (string, string) {
	var b strings.Builder
	b.WriteString("A new submission arrived through the form \"")
	b.WriteString(form.Name)
	b.WriteString("\".\n\n")
	for _, line := range submissionLines(form, submission, false) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\nSubmission ID: ")
	fmt.Fprintf(&b, "%d\n", submission.ID)
	return "New submission: " + form.Name, b.String()
}

// ---------------------------------------------------------------------------
// Staff reads
// ---------------------------------------------------------------------------

// ListDeliveries returns one page of the attention list, newest first.
func (s *formService) ListDeliveries(filter FormMailDeliveryFilter, offset, limit int) ([]models.FormMailDelivery, int64, error) {
	fieldErrors := FieldErrors{}
	if filter.Status != "" && !formMailStatuses[filter.Status] {
		fieldErrors["status"] = fmt.Sprintf("unknown delivery status %q", filter.Status)
	}
	if filter.Kind != "" && !formMailKinds[filter.Kind] {
		fieldErrors["kind"] = fmt.Sprintf("unknown delivery kind %q", filter.Kind)
	}
	if len(fieldErrors) > 0 {
		return nil, 0, fieldErrors
	}

	now := s.mailNow()
	rows, total, err := s.repo.ListAttentionDeliveries(repository.FormMailDeliveryQuery{
		FormID:      filter.FormID,
		Kind:        filter.Kind,
		Status:      filter.Status,
		StuckBefore: now.Add(-FormMailStuckAfter),
		Offset:      offset,
		Limit:       limit,
	})
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].Evaluate(now, FormMailStuckAfter)
	}
	return rows, total, nil
}

// withDeliveries attaches each submission's delivery rows, in id order, and
// passes a failed read through.
func (s *formService) withDeliveries(list []models.FormSubmission, total int64, err error) ([]models.FormSubmission, int64, error) {
	if err != nil {
		return nil, 0, err
	}
	ids := make([]uint, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	rows, err := s.repo.ListDeliveriesForSubmissions(ids)
	if err != nil {
		return nil, 0, err
	}
	now := s.mailNow()
	bySubmission := make(map[uint][]models.FormMailDelivery, len(list))
	for _, row := range rows {
		row.Evaluate(now, FormMailStuckAfter)
		bySubmission[row.SubmissionID] = append(bySubmission[row.SubmissionID], row)
	}
	for i := range list {
		list[i].Deliveries = bySubmission[list[i].ID]
		if list[i].Deliveries == nil {
			list[i].Deliveries = []models.FormMailDelivery{}
		}
	}
	return list, total, nil
}

func (s *formService) withSubmissionDeliveries(submission *models.FormSubmission) (*models.FormSubmission, error) {
	list, _, err := s.withDeliveries([]models.FormSubmission{*submission}, 1, nil)
	if err != nil {
		return nil, err
	}
	return &list[0], nil
}

// ---------------------------------------------------------------------------
// Retry
// ---------------------------------------------------------------------------

// RetryDelivery re-sends one failed, log_only or stuck delivery. See the
// FormService interface for the contract.
func (s *formService) RetryDelivery(ctx context.Context, id uint) (*models.FormMailDelivery, error) {
	if utils.InTransaction(ctx) {
		return nil, utils.ErrTransactionRootRequired
	}
	// Like every delivery line, the retry's carry ids, the kind, the status
	// and the error code only.
	logger := utils.Logger.WithField("delivery_id", id)

	var job formMailJob
	err := s.txManager.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		job, err = s.prepareRetry(s.repo.WithTx(tx), id)
		return err
	}, formMailRetryRetries)
	if err != nil {
		switch {
		case errors.Is(err, ErrFormDeliveryNotRetryable):
			logger.WithError(utils.LogError(err)).Info("Form mail retry refused")
		case apperrors.IsNotFound(err):
			logger.WithError(utils.LogError(err)).Warn("Form mail delivery not found")
		default:
			logger.WithError(utils.LogError(err)).Error("Form mail retry failed")
		}
		return nil, err
	}

	sendErr := s.sendAndStamp(ctx, &job)
	job.delivery.Evaluate(s.mailNow(), FormMailStuckAfter)
	if sendErr != nil {
		// The transport's text may quote the address; only the category is
		// returned.
		return job.delivery, fmt.Errorf("form mail delivery %d failed (%s): %w", id, job.delivery.ErrorCode, apperrors.ErrMailDelivery)
	}
	return job.delivery, nil
}

func formDeliveryNotFound(id uint) error {
	return fmt.Errorf("form mail delivery %d not found: %w", id, apperrors.ErrNotFound)
}

func formDeliveryNotRetryable(id uint, reason string) error {
	return fmt.Errorf("form mail delivery %d: %s: %w", id, reason, ErrFormDeliveryNotRetryable)
}

// prepareRetry runs inside the retry transaction and ends with the claim. For
// a follow-up or a notification the order is: the delivery, the submission
// locked by primary key, the form, the claim.
func (s *formService) prepareRetry(repo repository.FormRepository, id uint) (formMailJob, error) {
	delivery, err := repo.GetDeliveryByID(id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return formMailJob{}, formDeliveryNotFound(id)
		}
		return formMailJob{}, err
	}
	now := s.mailNow()
	delivery.Evaluate(now, FormMailStuckAfter)
	if !delivery.Retryable {
		return formMailJob{}, formDeliveryNotRetryable(id, "status "+delivery.Status)
	}
	if delivery.Kind == models.FormMailKindConfirmation {
		return s.prepareConfirmationRetry(repo, delivery, now)
	}

	submission, err := repo.LockSubmissionByID(delivery.SubmissionID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return formMailJob{}, formDeliveryNotFound(id)
		}
		return formMailJob{}, err
	}
	form, err := s.retryForm(repo, id, submission.FormID)
	if err != nil {
		return formMailJob{}, err
	}

	job := formMailJob{delivery: delivery}
	switch delivery.Kind {
	case models.FormMailKindFollowUp:
		if strings.TrimSpace(form.FollowUpSubject) == "" {
			return formMailJob{}, formDeliveryNotRetryable(id, "the form no longer has a follow-up")
		}
		if submission.Status != models.FormSubmissionReceived && submission.Status != models.FormSubmissionConfirmed {
			return formMailJob{}, formDeliveryNotRetryable(id, "submission status "+string(submission.Status))
		}
		job.to = submission.Email
		job.subject, job.body = renderFormFollowUp(form)
	case models.FormMailKindNotification:
		recipient, ok := s.notifyRecipient(form, delivery.RecipientHash)
		if !ok {
			return formMailJob{}, formDeliveryNotRetryable(id, "the recipient is no longer a notification address of the form")
		}
		job.to = recipient
		job.subject, job.body = renderFormNotification(form, submission)
	default:
		return formMailJob{}, formDeliveryNotRetryable(id, "unknown kind")
	}

	if err := s.claimForRetry(repo, delivery, now); err != nil {
		return formMailJob{}, err
	}
	return job, nil
}

// prepareConfirmationRetry re-arms the submission's single token in place and
// claims the delivery. The order matters: the plain reads, the spend of every
// outstanding token of the form and address (token rows first, the order a
// confirmation and the purge use), the submission locked by primary key and
// re-checked, the newer-submission check, the re-arm, the claim. Any refusal
// returns an error and the rollback restores every token.
func (s *formService) prepareConfirmationRetry(repo repository.FormRepository, delivery *models.FormMailDelivery, now time.Time) (formMailJob, error) {
	id := delivery.ID
	submission, err := repo.GetSubmissionByID(delivery.SubmissionID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return formMailJob{}, formDeliveryNotFound(id)
		}
		return formMailJob{}, err
	}
	form, err := s.retryForm(repo, id, submission.FormID)
	if err != nil {
		return formMailJob{}, err
	}
	if submission.Status != models.FormSubmissionPending || submission.LeadID != nil {
		return formMailJob{}, formDeliveryNotRetryable(id, "the submission is no longer pending")
	}

	if err := repo.InvalidatePendingTokens(form.ID, submission.Email); err != nil {
		return formMailJob{}, err
	}

	locked, err := repo.LockSubmissionByID(submission.ID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return formMailJob{}, formDeliveryNotFound(id)
		}
		return formMailJob{}, err
	}
	if locked.Status != models.FormSubmissionPending || locked.LeadID != nil {
		return formMailJob{}, formDeliveryNotRetryable(id, "the submission is no longer pending")
	}

	newer, err := repo.HasNewerSubmission(form.ID, locked.Email, locked.ID)
	if err != nil {
		return formMailJob{}, err
	}
	if newer {
		return formMailJob{}, formDeliveryNotRetryable(id, "a newer submission of the same address exists")
	}

	tokenIDs, err := repo.ListConfirmationTokenIDs(locked.ID)
	if err != nil {
		return formMailJob{}, err
	}
	if len(tokenIDs) != 1 {
		return formMailJob{}, formDeliveryNotRetryable(id, fmt.Sprintf("the submission has %d confirmation tokens", len(tokenIDs)))
	}
	raw, err := generateOpaqueToken()
	if err != nil {
		return formMailJob{}, err
	}
	// expires_at is written from the process clock, the clock the confirmation
	// and the purge compare it with.
	if err := repo.ReArmConfirmationToken(tokenIDs[0], hashOpaqueToken(raw, s.tokenSecret), time.Now().Add(formConfirmationTTL)); err != nil {
		if apperrors.IsNotFound(err) {
			return formMailJob{}, formDeliveryNotRetryable(id, "the confirmation token could not be re-armed")
		}
		return formMailJob{}, err
	}

	if err := s.claimForRetry(repo, delivery, now); err != nil {
		return formMailJob{}, err
	}
	subject, body := renderFormConfirmation(form, s.confirmationLink(raw))
	return formMailJob{delivery: delivery, to: locked.Email, subject: subject, body: body}, nil
}

// retryForm reads the delivery's form, soft-deleted or not, and refuses a
// soft-deleted one.
func (s *formService) retryForm(repo repository.FormRepository, id, formID uint) (*models.Form, error) {
	form, err := repo.GetFormIncludingDeleted(formID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, formDeliveryNotFound(id)
		}
		return nil, err
	}
	if form.DeletedAt.Valid {
		return nil, formDeliveryNotRetryable(id, "the form is deleted")
	}
	return form, nil
}

// claimForRetry is the conditional claim that ends every retry transaction.
// Losing it means another retry claimed the row first.
func (s *formService) claimForRetry(repo repository.FormRepository, delivery *models.FormMailDelivery, now time.Time) error {
	if err := repo.ClaimDeliveryForRetry(delivery.ID, now, now.Add(-FormMailStuckAfter)); err != nil {
		if apperrors.IsNotFound(err) {
			return formDeliveryNotRetryable(delivery.ID, "claimed by another retry")
		}
		return err
	}
	markClaimed(delivery, now)
	return nil
}

// notifyRecipient finds the current NotifyEmails address a notification row
// was written for.
func (s *formService) notifyRecipient(form *models.Form, recipientHash string) (string, bool) {
	if recipientHash == "" {
		return "", false
	}
	for _, address := range form.NotifyEmails {
		if strings.TrimSpace(address) == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(s.recipientHash(address)), []byte(recipientHash)) == 1 {
			return address, true
		}
	}
	return "", false
}
