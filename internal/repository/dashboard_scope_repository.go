package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// DashboardScopeRepository reads the dashboard aggregates narrowed to one
// owner or assignee (#347). Each query is the system-wide one of the lead,
// ticket or task repository with the owner or assignee added to the WHERE
// clause, so the narrowing happens inside the query, before any count.
type DashboardScopeRepository interface {
	CountLeadsByOwner(ownerID uint) (int64, error)
	CountLeadsByStatusForOwner(ownerID uint) (map[string]int64, error)
	LeadConversionTimestampsSinceForOwner(ownerID uint, since time.Time) ([]time.Time, error)
	CountOpenTicketsByAssignee(assigneeID uint) (int64, error)
	CountTicketsByPriorityForAssignee(assigneeID uint) (map[string]int64, error)
	CountPendingTasksByAssignee(assigneeID uint) (int64, error)
	CountTasksByStatusForAssignee(assigneeID uint) (map[string]int64, error)
}

type dashboardScopeRepository struct {
	db *gorm.DB
}

func NewDashboardScopeRepository(db *gorm.DB) DashboardScopeRepository {
	return &dashboardScopeRepository{db: db}
}

func (r *dashboardScopeRepository) CountLeadsByOwner(ownerID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Lead{}).Where("owner_id = ?", ownerID).Count(&count).Error
	return count, err
}

func (r *dashboardScopeRepository) CountLeadsByStatusForOwner(ownerID uint) (map[string]int64, error) {
	return countGroupedByColumn(r.db.Model(&models.Lead{}).Where("owner_id = ?", ownerID), "status")
}

// LeadConversionTimestampsSinceForOwner is LeadRepository.ConversionTimestampsSince
// for one owner; the caller buckets the timestamps in Go, for the reason
// given there.
func (r *dashboardScopeRepository) LeadConversionTimestampsSinceForOwner(ownerID uint, since time.Time) ([]time.Time, error) {
	timestamps := []time.Time{}
	err := r.db.Model(&models.Lead{}).
		Where("owner_id = ?", ownerID).
		Where("status = ?", models.LeadStatusConverted).
		Where("updated_at >= ?", since).
		Order("updated_at ASC").
		Pluck("updated_at", &timestamps).Error
	return timestamps, err
}

func (r *dashboardScopeRepository) CountOpenTicketsByAssignee(assigneeID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Ticket{}).
		Where("assigned_to_id = ?", assigneeID).
		Where("status IN ?", []string{"open", "in_progress"}).
		Count(&count).Error
	return count, err
}

func (r *dashboardScopeRepository) CountTicketsByPriorityForAssignee(assigneeID uint) (map[string]int64, error) {
	return countGroupedByColumn(r.db.Model(&models.Ticket{}).Where("assigned_to_id = ?", assigneeID), "priority")
}

func (r *dashboardScopeRepository) CountPendingTasksByAssignee(assigneeID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Task{}).
		Where("assigned_to_id = ?", assigneeID).
		Where("status IN ?", []string{"pending", "in_progress"}).
		Count(&count).Error
	return count, err
}

func (r *dashboardScopeRepository) CountTasksByStatusForAssignee(assigneeID uint) (map[string]int64, error) {
	return countGroupedByColumn(r.db.Model(&models.Task{}).Where("assigned_to_id = ?", assigneeID), "status")
}
