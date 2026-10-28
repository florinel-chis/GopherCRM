package service

import (
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type notificationService struct {
	repo repository.NotificationRepository
	now  func() time.Time
}

func NewNotificationService(repo repository.NotificationRepository) NotificationService {
	return &notificationService{repo: repo, now: time.Now}
}

func (s *notificationService) List(userID uint, unreadOnly bool, offset, limit int) ([]models.Notification, int64, error) {
	notifications, total, err := s.repo.ListByUser(userID, unreadOnly, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	if notifications == nil {
		notifications = []models.Notification{}
	}
	return notifications, total, nil
}

func (s *notificationService) UnreadCount(userID uint) (int64, error) {
	return s.repo.CountUnread(userID)
}

// MarkRead stamps the user's own notification; one that does not exist or
// belongs to somebody else is apperrors.ErrNotificationNotFound. An already
// read notification is returned as it is.
func (s *notificationService) MarkRead(userID, id uint) (*models.Notification, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{"user_id": userID, "notification_id": id}), "NotificationService", "MarkRead")
	notification, err := s.repo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("notification %d not found: %w", id, apperrors.ErrNotificationNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	if notification.UserID != userID {
		return nil, fmt.Errorf("notification %d not found: %w", id, apperrors.ErrNotificationNotFound)
	}
	if notification.ReadAt == nil {
		if _, err := s.repo.MarkRead(id, userID, s.now()); err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, err
		}
	}
	return s.repo.GetByID(id)
}

func (s *notificationService) MarkAllRead(userID uint) (int64, error) {
	return s.repo.MarkAllRead(userID, s.now())
}
