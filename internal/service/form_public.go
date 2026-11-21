package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ---------------------------------------------------------------------------
// Public rendering
// ---------------------------------------------------------------------------

// PublicDefinition returns what a visitor's browser needs to render the form.
// An unknown key, an unpublished form and an origin the form does not allow are
// all reported as not-found: the public surface never explains why a form is
// unavailable.
func (s *formService) PublicDefinition(publicID, origin string) (*PublicFormDefinition, error) {
	form, err := s.publishedForm(s.repo, publicID)
	if err != nil {
		return nil, err
	}

	if !originAllowed(form.AllowedDomains, origin) {
		utils.Logger.WithField("form_id", form.ID).
			Info("Form definition requested from a disallowed origin")
		return nil, fmt.Errorf("form %q not found: %w", publicID, apperrors.ErrNotFound)
	}

	// Definitions saved before the column limits existed may still declare a
	// wider max_length; publish the limit the validator enforces.
	fields := make([]models.FormFieldDef, len(form.Fields))
	for i, field := range form.Fields {
		field.MaxLength = effectiveMaxLength(field)
		fields[i] = field
	}

	definition := &PublicFormDefinition{
		Name:          form.Name,
		PublicID:      form.PublicID,
		Fields:        fields,
		ConsentText:   form.ConsentText,
		SubmitAction:  form.SubmitAction,
		Challenge:     forms.NewChallenge([]byte(s.tokenSecret), time.Now()),
		HoneypotField: formHoneypotField,
	}
	if definition.SubmitAction == "" {
		definition.SubmitAction = models.FormSubmitActionMessage
	}
	if form.CaptchaEnabled && s.cfg.RecaptchaActive() {
		definition.RecaptchaSiteKey = s.cfg.RecaptchaSiteKey
	}
	return definition, nil
}

func (s *formService) publishedForm(repo repository.FormRepository, publicID string) (*models.Form, error) {
	form, err := repo.GetByPublicID(publicID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("form %q not found: %w", publicID, apperrors.ErrNotFound)
		}
		return nil, err
	}
	if form.Status != models.FormStatusPublished {
		return nil, fmt.Errorf("form %q not found: %w", publicID, apperrors.ErrNotFound)
	}
	return form, nil
}

// ---------------------------------------------------------------------------
// Submission pipeline
// ---------------------------------------------------------------------------

// SubmitPublic runs a public submission through validation and every spam
// layer, then stores it and triggers whatever the form is configured to do.
//
// The layers run in a fixed order — validation, challenge, origin, honeypot,
// captcha — and the first one that trips decides the recorded reason. Only
// validation is reported to the client; every spam layer produces the same
// success-shaped outcome a genuine submission gets, with the submission stored
// as spam so an admin can see what the filters caught.
//
// A submission is a transaction root: it mails after its own commit, so a ctx
// that already carries a transaction is refused with
// utils.ErrTransactionRootRequired before anything is read or written.
func (s *formService) SubmitPublic(ctx context.Context, publicID string, req *PublicSubmissionRequest, meta SubmissionMeta) (*SubmitOutcome, error) {
	if utils.InTransaction(ctx) {
		return nil, utils.ErrTransactionRootRequired
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "form"), "FormService", "SubmitPublic")

	// Every statement of a submission is bound to the visitor's request: a
	// request that is cancelled or times out stops waiting for a connection,
	// abandons the captcha call and stores nothing.
	form, err := s.publishedForm(s.repo.WithTx(s.txManager.DB(ctx)), publicID)
	if err != nil {
		return nil, err
	}

	logger = logger.WithField("form_id", form.ID)
	values, err := s.validateValues(form, req)
	if err != nil {
		return nil, err
	}
	// The handler refuses a malformed key before calling; this guards every
	// other caller (#213). The value is never echoed or logged.
	if req.IdempotencyKey != nil && !ValidFormIdempotencyKey(*req.IdempotencyKey) {
		return nil, FieldErrors{"idempotency_key": FormIdempotencyKeyRule}
	}

	spamReason, err := s.checkChallenge(req.Challenge)
	if err != nil {
		return nil, err
	}
	if spamReason != "" {
		return s.recordSpam(ctx, form, values, meta, req, spamReason)
	}

	if !originAllowed(form.AllowedDomains, meta.Origin) {
		return s.recordSpam(ctx, form, values, meta, req, models.FormSpamReasonDomain)
	}

	if strings.TrimSpace(req.Honeypot) != "" {
		return s.recordSpam(ctx, form, values, meta, req, models.FormSpamReasonHoneypot)
	}

	passed, err := s.checkCaptcha(ctx, form, req, meta)
	if err != nil {
		return nil, err
	}
	if !passed {
		return s.recordSpam(ctx, form, values, meta, req, models.FormSpamReasonCaptcha)
	}

	submission := s.newSubmission(form, values, meta, req)
	if form.DoubleOptIn {
		submission.Status = models.FormSubmissionPending
		if err := s.storePendingSubmission(ctx, form, submission); err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, err
		}
	} else {
		submission.Status = models.FormSubmissionReceived
		if err := s.storeReceivedSubmission(ctx, form, submission); err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, err
		}
	}

	logger.WithField("submission_id", submission.ID).
		WithField("status", submission.Status).
		Info("Form submission accepted")
	return submitOutcome(form), nil
}

// recordSpam stores a rejected submission and answers with the outcome a
// genuine submission would have produced. No mail is sent and no lead is
// created — the row exists purely so an admin can audit the filters.
func (s *formService) recordSpam(ctx context.Context, form *models.Form, values map[string]string, meta SubmissionMeta, req *PublicSubmissionRequest, reason string) (*SubmitOutcome, error) {
	submission := s.newSubmission(form, values, meta, req)
	submission.Status = models.FormSubmissionSpam
	submission.SpamReason = reason
	// Spam never opens a ticket, so it keeps no key (#213).
	submission.IdempotencyKey = ""

	if err := s.repo.WithTx(s.txManager.DB(ctx)).CreateSubmission(submission); err != nil {
		return nil, err
	}

	utils.Logger.WithField("form_id", form.ID).WithField("spam_reason", reason).
		Info("Form submission rejected by a protection layer")
	return submitOutcome(form), nil
}

func (s *formService) newSubmission(form *models.Form, values map[string]string, meta SubmissionMeta, req *PublicSubmissionRequest) *models.FormSubmission {
	submission := &models.FormSubmission{
		FormID:    form.ID,
		Data:      values,
		Email:     values[models.FormFieldEmail],
		IPAddress: truncate(meta.IP, 45),
		UserAgent: truncate(meta.UserAgent, 255),
		Referrer:  truncate(req.PageURL, 512),
	}
	// Only a ticket form keeps the client key (#213): the pending row carries
	// it to the confirmation. Every other form ignores it.
	if form.CreateTicket && req.IdempotencyKey != nil {
		submission.IdempotencyKey = *req.IdempotencyKey
	}
	return submission
}

// storeReceivedSubmission persists a final submission together with the lead it
// feeds, then sends whatever mail the form configures. The lead and the
// submission's link to it are written in one transaction so a submission can
// never point at a lead that was rolled back; so are the pending rows of its
// mail (#216), which are claimed, sent and stamped after the commit.
func (s *formService) storeReceivedSubmission(ctx context.Context, form *models.Form, submission *models.FormSubmission) error {
	var jobs []formMailJob
	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		txFormRepo := s.repo.WithTx(tx)

		// A received submission is final on arrival: its attribution is
		// committed with the row (#220).
		applyFormAttribution(submission)
		if err := txFormRepo.CreateSubmission(submission); err != nil {
			return err
		}
		if form.CreateLead {
			if err := s.applySubmissionLead(ctx, s.leadRepo.WithTx(tx), automationTx(s.automation, tx), s.webhooks.Bind(tx), form, submission); err != nil {
				return err
			}
			if err := txFormRepo.UpdateSubmission(submission); err != nil {
				return err
			}
		}
		var err error
		jobs, err = s.recordAcceptedMail(txFormRepo, form, submission)
		return err
	})
	if err != nil {
		return err
	}

	s.deliverMail(ctx, jobs)
	return nil
}

// storePendingSubmission persists a submission awaiting confirmation and mints
// its single-use token. Prior outstanding tokens for the same form and address
// are spent first, so re-submitting leaves exactly one working link. No lead is
// created and no notification is sent until the address is confirmed.
//
// Spending the old tokens can deadlock with the retention purge removing an
// older pending row of the same address, and InnoDB may pick this transaction as
// the victim. It is replayed so the visitor neither loses the new row nor gets a
// server error. Each attempt inserts its row afresh, because the rollback
// discarded the id GORM copied onto the struct, and mail goes out once, after
// the attempt that committed.
func (s *formService) storePendingSubmission(ctx context.Context, form *models.Form, submission *models.FormSubmission) error {
	var jobs []formMailJob

	err := s.txManager.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		txFormRepo := s.repo.WithTx(tx)

		submission.ID = 0
		submission.CreatedAt = time.Time{}
		submission.UpdatedAt = time.Time{}
		if err := txFormRepo.CreateSubmission(submission); err != nil {
			return err
		}
		if err := txFormRepo.InvalidatePendingTokens(form.ID, submission.Email); err != nil {
			return err
		}

		raw, err := generateOpaqueToken()
		if err != nil {
			return err
		}
		token := &models.FormConfirmationToken{
			SubmissionID: submission.ID,
			TokenHash:    hashOpaqueToken(raw, s.tokenSecret),
			ExpiresAt:    time.Now().Add(formConfirmationTTL),
		}
		if err := txFormRepo.CreateConfirmationToken(token); err != nil {
			return err
		}

		jobs, err = s.recordConfirmationMail(txFormRepo, form, submission, raw)
		return err
	}, formPendingSubmissionRetries)
	if err != nil {
		return err
	}

	s.deliverMail(ctx, jobs)
	return nil
}

// submitOutcome describes what the renderer should do after a submission. It
// is deliberately derived from the form alone: spam and genuine submissions
// must be answered identically.
func submitOutcome(form *models.Form) *SubmitOutcome {
	outcome := &SubmitOutcome{
		Action:              form.SubmitAction,
		PendingConfirmation: form.DoubleOptIn,
	}
	if outcome.Action == "" {
		outcome.Action = models.FormSubmitActionMessage
	}

	if outcome.Action == models.FormSubmitActionRedirect {
		outcome.RedirectURL = form.RedirectURL
		return outcome
	}

	switch {
	case strings.TrimSpace(form.ThankYouMessage) != "":
		outcome.Message = form.ThankYouMessage
	case form.DoubleOptIn:
		// Without a word about the mail that is on its way, a visitor of an
		// opt-in form would believe they were done.
		outcome.Message = formDefaultPendingMessage
	default:
		outcome.Message = formDefaultThankYouMessage
	}
	return outcome
}

// Mail is rendered, recorded and delivered in form_mail_delivery.go (#216).
