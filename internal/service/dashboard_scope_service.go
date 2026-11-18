package service

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/repository"
)

// DashboardScopeService serves the dashboard aggregates of a caller whose
// permission reaches only its own records (#347): the leads it owns, the
// tickets and tasks assigned to it. The system-wide aggregates stay on the
// lead, ticket and task services.
type DashboardScopeService interface {
	LeadCountByOwner(ownerID uint) (int64, error)
	LeadStatusCountsByOwner(ownerID uint) (map[string]int64, error)
	LeadConversionTimestampsByOwner(ownerID uint, since time.Time) ([]time.Time, error)
	OpenTicketCountByAssignee(assigneeID uint) (int64, error)
	TicketPriorityCountsByAssignee(assigneeID uint) (map[string]int64, error)
	PendingTaskCountByAssignee(assigneeID uint) (int64, error)
	TaskStatusCountsByAssignee(assigneeID uint) (map[string]int64, error)
}

type dashboardScopeService struct {
	repo repository.DashboardScopeRepository
}

func NewDashboardScopeService(repo repository.DashboardScopeRepository) DashboardScopeService {
	return &dashboardScopeService{repo: repo}
}

func (s *dashboardScopeService) LeadCountByOwner(ownerID uint) (int64, error) {
	return s.repo.CountLeadsByOwner(ownerID)
}

func (s *dashboardScopeService) LeadStatusCountsByOwner(ownerID uint) (map[string]int64, error) {
	return s.repo.CountLeadsByStatusForOwner(ownerID)
}

func (s *dashboardScopeService) LeadConversionTimestampsByOwner(ownerID uint, since time.Time) ([]time.Time, error) {
	return s.repo.LeadConversionTimestampsSinceForOwner(ownerID, since)
}

func (s *dashboardScopeService) OpenTicketCountByAssignee(assigneeID uint) (int64, error) {
	return s.repo.CountOpenTicketsByAssignee(assigneeID)
}

func (s *dashboardScopeService) TicketPriorityCountsByAssignee(assigneeID uint) (map[string]int64, error) {
	return s.repo.CountTicketsByPriorityForAssignee(assigneeID)
}

func (s *dashboardScopeService) PendingTaskCountByAssignee(assigneeID uint) (int64, error) {
	return s.repo.CountPendingTasksByAssignee(assigneeID)
}

func (s *dashboardScopeService) TaskStatusCountsByAssignee(assigneeID uint) (map[string]int64, error) {
	return s.repo.CountTasksByStatusForAssignee(assigneeID)
}
