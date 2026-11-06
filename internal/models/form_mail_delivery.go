package models

import (
	"time"

	"gorm.io/gorm"
)

// The three mails a form sends: the double-opt-in link to the submitter, the
// follow-up to the submitter when the form defines a follow-up subject, and one
// staff notification per NotifyEmails address. The constants are untyped
// strings so the API docs gate collects them as a closed set by prefix.
const (
	FormMailKindConfirmation = "confirmation"
	FormMailKindFollowUp     = "follow_up"
	FormMailKindNotification = "notification"
)

// Delivery states. A row is written pending in the submission or confirmation
// transaction, claimed (sending) after the commit and stamped with the outcome:
// sent means the relay accepted the message (there is no bounce or provider
// confirmation), log_only that the development LogMailer (no SMTP_HOST) logged
// it instead of delivering it, and skipped that nothing was attempted because
// no mailer is wired or there is no recipient address.
const (
	FormMailStatusPending = "pending"
	FormMailStatusSending = "sending"
	FormMailStatusSent    = "sent"
	FormMailStatusFailed  = "failed"
	FormMailStatusLogOnly = "log_only"
	FormMailStatusSkipped = "skipped"
)

// Error codes stamped on a failed or skipped delivery. They never carry the
// transport's own text, which may quote the address.
const (
	FormMailErrorMailFailed       = "mail_failed"
	FormMailErrorTimeout          = "timeout"
	FormMailErrorInvalidRecipient = "invalid_recipient"
	FormMailErrorNoMailer         = "no_mailer"
	FormMailErrorNoRecipient      = "no_recipient"
)

// FormMailDelivery records the outcome of one form mail: the confirmation, the
// follow-up or one notification. It stores no address and no message content;
// a notification row keeps only an HMAC of its recipient (never serialised) so
// a retry can find the address again in the form's current NotifyEmails.
//
// The rows are hard-deleted with their submission by the retention purge and
// by the erasure of the lead the submission is linked to.
type FormMailDelivery struct {
	// Unique identifier, assigned by the server. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"17" readonly:"true"`
	// When the row was written (RFC 3339), in the submission or confirmation
	// transaction. Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-11-06T09:30:00Z" readonly:"true"`
	// When the row last changed state (RFC 3339). A pending or sending row
	// whose updated_at is more than 15 minutes old is stuck. Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-11-06T09:30:01Z" readonly:"true"`
	// Never serialised; the rows are hard-deleted, never soft-deleted.
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Submission the mail belongs to.
	SubmissionID uint `gorm:"not null;index" json:"submission_id" example:"42" readonly:"true"`
	// Form the submission came through.
	FormID uint `gorm:"not null;index" json:"form_id" example:"4" readonly:"true"`
	// Which mail: confirmation (double opt-in link), follow_up (to the
	// submitter) or notification (one per notify_emails address).
	Kind string `gorm:"not null;type:varchar(20)" json:"kind" enums:"confirmation,follow_up,notification" example:"notification" readonly:"true"`
	// Delivery state: pending (written with the submission), sending (claimed),
	// sent (accepted by the relay), failed, log_only (logged by the development
	// mailer, nothing delivered) or skipped (no mailer or no recipient).
	Status string `gorm:"not null;type:varchar(20);index" json:"status" enums:"pending,sending,sent,failed,log_only,skipped" example:"failed" readonly:"true"`
	// Why a delivery failed or was skipped: mail_failed, timeout,
	// invalid_recipient, no_mailer or no_recipient. Empty otherwise.
	ErrorCode string `gorm:"type:varchar(50)" json:"error_code" enums:",mail_failed,timeout,invalid_recipient,no_mailer,no_recipient" example:"mail_failed" readonly:"true"`
	// HMAC-SHA256 of the normalised notification recipient; empty for the
	// submitter's mails. Never serialised.
	RecipientHash string `gorm:"type:varchar(64)" json:"-"`
	// Number of claims (first send and every retry).
	Attempts int `gorm:"not null;default:0" json:"attempts" example:"1" readonly:"true"`
	// When the last claim was made (RFC 3339); null before the first.
	LastAttemptAt *time.Time `json:"last_attempt_at" example:"2026-11-06T09:30:01Z" extensions:"x-nullable" readonly:"true"`
	// When the relay accepted the message (RFC 3339); null unless status is
	// sent.
	SentAt *time.Time `json:"sent_at" example:"2026-11-06T09:30:01Z" extensions:"x-nullable" readonly:"true"`

	// Computed on read: a pending or sending row last updated more than 15
	// minutes ago.
	Stuck bool `gorm:"-" json:"stuck" example:"false" readonly:"true"`
	// Computed on read: the status allows a retry (failed, log_only or stuck).
	// A retry may still be refused (409) or not found (404) for reasons that
	// depend on the submission, the form or its notify_emails.
	Retryable bool `gorm:"-" json:"retryable" example:"true" readonly:"true"`
}

// Evaluate fills Stuck and Retryable for the clock now: a pending or sending
// row is stuck once its updated_at lies more than stuckAfter before now.
func (d *FormMailDelivery) Evaluate(now time.Time, stuckAfter time.Duration) {
	d.Stuck = (d.Status == FormMailStatusPending || d.Status == FormMailStatusSending) &&
		d.UpdatedAt.Before(now.Add(-stuckAfter))
	d.Retryable = d.Status == FormMailStatusFailed || d.Status == FormMailStatusLogOnly || d.Stuck
}
