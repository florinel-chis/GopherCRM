package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type notificationRepository struct {
	db *gorm.DB
}

func NewNotificationRepository(db *gorm.DB) NotificationRepository {
	return &notificationRepository{db: db}
}

func (r *notificationRepository) WithTx(tx *gorm.DB) NotificationRepository {
	return &notificationRepository{db: tx}
}

func (r *notificationRepository) Create(notification *models.Notification) error {
	return r.db.Omit("User").Create(notification).Error
}

func (r *notificationRepository) GetByID(id uint) (*models.Notification, error) {
	var notification models.Notification
	if err := r.db.First(&notification, id).Error; err != nil {
		return nil, err
	}
	return &notification, nil
}

// ListByUser returns one page of the user's notifications, newest first,
// plus the total of the same set. The user predicate is part of the query, so
// nobody else's row can take a place on the page.
func (r *notificationRepository) ListByUser(userID uint, unreadOnly bool, offset, limit int) ([]models.Notification, int64, error) {
	scope := func(db *gorm.DB) *gorm.DB {
		db = db.Where("user_id = ?", userID)
		if unreadOnly {
			db = db.Where("read_at IS NULL")
		}
		return db
	}
	var total int64
	if err := scope(r.db.Model(&models.Notification{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	notifications := []models.Notification{}
	err := scope(r.db).Order("created_at DESC").Order("id DESC").Offset(offset).Limit(limit).Find(&notifications).Error
	if err != nil {
		return nil, 0, err
	}
	return notifications, total, nil
}

func (r *notificationRepository) CountUnread(userID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Notification{}).Where("user_id = ? AND read_at IS NULL", userID).Count(&count).Error
	return count, err
}

// MarkRead stamps read_at on the user's own unread notification and reports
// the affected rows; a row of another user or one already read matches
// nothing.
func (r *notificationRepository) MarkRead(id, userID uint, now time.Time) (int64, error) {
	stamp := now.UTC()
	result := r.db.Model(&models.Notification{}).
		Where("id = ? AND user_id = ? AND read_at IS NULL", id, userID).
		Updates(map[string]interface{}{"read_at": &stamp, "updated_at": stamp})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *notificationRepository) MarkAllRead(userID uint, now time.Time) (int64, error) {
	stamp := now.UTC()
	result := r.db.Model(&models.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Updates(map[string]interface{}{"read_at": &stamp, "updated_at": stamp})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
