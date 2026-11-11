package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// ErrTicketMailDeliveryNotPending is returned by FinishPending when the row is
// gone or already carries an outcome. It is a plain sentinel: it wraps neither
// gorm.ErrRecordNotFound nor an application error.
var ErrTicketMailDeliveryNotPending = errors.New("ticket mail delivery is not pending")

type ticketMailDeliveryRepository struct {
	db *gorm.DB
}

// NewTicketMailDeliveryRepository returns the ticket comment mail delivery
// repository (#219).
func NewTicketMailDeliveryRepository(db *gorm.DB) TicketMailDeliveryRepository {
	return &ticketMailDeliveryRepository{db: db}
}

func (r *ticketMailDeliveryRepository) WithTx(tx *gorm.DB) TicketMailDeliveryRepository {
	return &ticketMailDeliveryRepository{db: tx}
}

func (r *ticketMailDeliveryRepository) Create(ctx context.Context, delivery *models.TicketMailDelivery) error {
	return r.db.WithContext(ctx).Create(delivery).Error
}

// FinishPending is a conditional UPDATE by primary key whose WHERE clause
// carries status = 'pending'. It always changes the status column, so MySQL's
// changed-rows count equals the matched count.
func (r *ticketMailDeliveryRepository) FinishPending(ctx context.Context, id uint, outcome TicketMailDeliveryOutcome) error {
	switch outcome.Status {
	case models.TicketMailStatusSent, models.TicketMailStatusFailed, models.TicketMailStatusLogOnly, models.TicketMailStatusSkipped:
	default:
		return fmt.Errorf("ticket mail delivery cannot be finished as %q", outcome.Status)
	}
	var sentAt interface{}
	if outcome.SentAt != nil {
		sentAt = outcome.SentAt.UTC()
	}
	result := r.db.WithContext(ctx).Model(&models.TicketMailDelivery{}).
		Where("id = ? AND status = ?", id, models.TicketMailStatusPending).
		Updates(map[string]interface{}{
			"status":     outcome.Status,
			"error_code": outcome.ErrorCode,
			"sent_at":    sentAt,
			"updated_at": outcome.Now.UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return ErrTicketMailDeliveryNotPending
	default:
		return fmt.Errorf("ticket mail delivery stamp affected %d rows", result.RowsAffected)
	}
}

func (r *ticketMailDeliveryRepository) GetByCommentID(ctx context.Context, commentID uint) (*models.TicketMailDelivery, error) {
	var delivery models.TicketMailDelivery
	if err := r.db.WithContext(ctx).Where("comment_id = ?", commentID).Take(&delivery).Error; err != nil {
		return nil, err
	}
	return &delivery, nil
}

// purgeTicketMailDeliveriesOfTicket hard-deletes every delivery of one ticket
// inside the caller's transaction. A schema without the table (a fixture that
// migrates a subset) is skipped by a migrator probe; when the table exists a
// failure is returned. The error names the table and the ticket id only.
func purgeTicketMailDeliveriesOfTicket(tx *gorm.DB, ticketID uint) error {
	if !tx.Migrator().HasTable(&models.TicketMailDelivery{}) {
		return nil
	}
	if err := tx.Where("ticket_id = ?", ticketID).Delete(&models.TicketMailDelivery{}).Error; err != nil {
		return fmt.Errorf("purging ticket_mail_deliveries of ticket %d: %w", ticketID, err)
	}
	return nil
}
