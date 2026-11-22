package service

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func (s *leadService) GetCount() (int64, error) {
	return s.leadRepo.Count()
}

func (s *leadService) GetCountByClassification(classification models.LeadClassification) (int64, error) {
	return s.leadRepo.CountByClassification(classification)
}

// GetStatusCounts returns the live lead count per status, for the dashboard's
// status distribution chart. Statuses with no rows are simply missing from the
// map — the handler owns the label set the chart must always show.
func (s *leadService) GetStatusCounts() (map[string]int64, error) {
	return s.leadRepo.CountByStatus()
}

// GetRecent returns the newest leads across every owner.
func (s *leadService) GetRecent(limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecent(nil, limit)
}

// GetRecentByOwner returns the newest leads owned by one user. Sales users only
// ever see their own leads, so the dashboard widget narrows through here.
func (s *leadService) GetRecentByOwner(ownerID uint, limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecent(&ownerID, limit)
}

// GetRecentlyConverted returns the leads most recently marked converted across
// every owner, for the admin activity feed.
func (s *leadService) GetRecentlyConverted(limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecentlyConverted(nil, limit)
}

// GetRecentlyConvertedByOwner returns the leads most recently marked converted
// that one user owns. The sales activity feed narrows through here.
func (s *leadService) GetRecentlyConvertedByOwner(ownerID uint, limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecentlyConverted(&ownerID, limit)
}

// GetConversionTimestamps returns the conversion time of every lead converted
// at or after `since`, oldest first, for the sales-performance chart. The
// caller buckets them in Go; see the repository for why not in SQL.
func (s *leadService) GetConversionTimestamps(since time.Time) ([]time.Time, error) {
	return s.leadRepo.ConversionTimestampsSince(since)
}
