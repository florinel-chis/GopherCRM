package models

import (
	"fmt"
	"time"
)

// Ticket comment mail (#219). A staff comment that is not an internal note may
// be emailed to the ticket's customer. Each requested send gets one
// ticket_mail_deliveries row, written pending in the comment's transaction and
// stamped with the outcome after the commit. The constants are untyped strings.
const (
	TicketMailStatusPending = "pending"
	TicketMailStatusSent    = "sent"
	TicketMailStatusFailed  = "failed"
	TicketMailStatusLogOnly = "log_only"
	TicketMailStatusSkipped = "skipped"
)

// Error codes stamped on a failed or skipped ticket comment mail. They never
// carry the transport's own text, which may quote the address.
const (
	TicketMailErrorMailFailed          = "mail_failed"
	TicketMailErrorTimeout             = "timeout"
	TicketMailErrorInvalidRecipient    = "invalid_recipient"
	TicketMailErrorLookupFailed        = "lookup_failed"
	TicketMailErrorNoMailer            = "no_mailer"
	TicketMailErrorNoRecipient         = "no_recipient"
	TicketMailErrorCustomerUnavailable = "customer_unavailable"
)

// TicketNumber renders a ticket id as the number staff and customers see in a
// mail subject: T- followed by the id zero-padded to six digits (T-000042).
func TicketNumber(id uint) string {
	return fmt.Sprintf("T-%06d", id)
}

// TicketMailDelivery records the outcome of emailing one ticket comment to the
// ticket's customer. It stores ids, a status, an error code and timestamps:
// no address, no subject, no title and no comment text. There is no deleted_at
// and no foreign key: the rows are hard-deleted when their ticket is deleted
// and when the ticket's customer is erased.
//
// @Description Outcome of emailing one staff ticket comment to the ticket's customer. Returned only on the 201 of POST /tickets/{id}/comments with email_customer true. Holds ids, a status, an error code and timestamps; never an address or content.
type TicketMailDelivery struct {
	// Unique identifier, assigned by the server. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"17" readonly:"true"`
	// When the row was written (RFC 3339), in the comment's transaction. Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-11-13T09:30:00Z" readonly:"true"`
	// When the row last changed state (RFC 3339): the outcome stamp. Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-11-13T09:30:01Z" readonly:"true"`
	// Ticket the comment belongs to.
	TicketID uint `gorm:"not null;index:idx_ticket_mail_deliveries_ticket_id" json:"ticket_id" example:"42" readonly:"true"`
	// The emailed comment; at most one delivery per comment.
	CommentID uint `gorm:"not null;uniqueIndex:idx_ticket_mail_deliveries_comment_id" json:"comment_id" example:"108" readonly:"true"`
	// The ticket's customer when the comment was written (an id, never an address).
	CustomerID uint `gorm:"not null;index:idx_ticket_mail_deliveries_customer_id" json:"customer_id" example:"3" readonly:"true"`
	// Delivery state: pending (written with the comment; left so only when the outcome could not be recorded), sent (accepted by the relay), failed, log_only (logged by the development mailer, nothing delivered) or skipped (nothing attempted).
	Status string `gorm:"not null;type:varchar(20)" json:"status" enums:"pending,sent,failed,log_only,skipped" example:"sent" readonly:"true"`
	// Why a delivery failed (mail_failed, timeout, invalid_recipient, lookup_failed) or was skipped (no_mailer, no_recipient, customer_unavailable). Empty otherwise.
	ErrorCode string `gorm:"not null;type:varchar(50);default:''" json:"error_code" enums:",mail_failed,timeout,invalid_recipient,lookup_failed,no_mailer,no_recipient,customer_unavailable" example:"" readonly:"true"`
	// When the relay accepted the message (RFC 3339); null unless status is sent.
	SentAt *time.Time `json:"sent_at" example:"2026-11-13T09:30:01Z" extensions:"x-nullable" readonly:"true"`
}
