package service

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func (s *leadService) GetByExternalID(externalID string) (*models.Lead, error) {
	return s.leadRepo.GetByExternalID(externalID)
}

// LeadExternalIDMaxLength is the width of leads.external_id (varchar(255)),
// in characters: MySQL counts varchar in characters, not bytes. The
// GET /leads?external_id= lookup refuses a longer value, which no stored row
// can hold. TestLeadExternalIDMaxLengthMatchesTheColumn fails if the two drift.
const LeadExternalIDMaxLength = 255

// LeadExternalIDLookupLimit caps the rows ListByExternalID reads.
const LeadExternalIDLookupLimit = 100

// ListByExternalID returns the live leads whose external_id equals
// externalID, lowest id first, at most LeadExternalIDLookupLimit of them, with
// the owner loaded the way the admin list paths load it (the linked company
// is not). Visibility is the caller's job (CanViewLead on each row).
func (s *leadService) ListByExternalID(externalID string) ([]models.Lead, error) {
	return s.leadRepo.ListByExternalID(externalID, LeadExternalIDLookupLimit)
}

func (s *leadService) GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, int64, error) {
	// For owner-filtered lists, no need to preload Owner since we already know it
	leads, err := s.leadRepo.GetByOwnerID(ownerID, offset, limit)
	if err != nil {
		return nil, 0, err
	}

	total, err := s.leadRepo.CountByOwnerID(ownerID)
	if err != nil {
		return nil, 0, err
	}

	return leads, total, nil
}

func (s *leadService) GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "GetByClassification")
	switch classification {
	case models.LeadClassificationUnclassified, models.LeadClassificationTest, models.LeadClassificationSpam, models.LeadClassificationLead, models.LeadClassificationHotLead:
		logger = logger.WithField("classification", classification)
	}

	leads, err := s.leadRepo.GetByClassification(classification, offset, limit)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list leads by classification")
		return nil, 0, err
	}

	total, err := s.leadRepo.CountByClassification(classification)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count leads by classification")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed by classification successfully")
	return leads, total, nil
}

func (s *leadService) List(offset, limit int) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "List")

	// For general lists, preload Owner for display purposes
	leads, err := s.leadRepo.ListWithPreloads(offset, limit, "Owner")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list leads")
		return nil, 0, err
	}

	total, err := s.leadRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count leads")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed successfully")
	return leads, total, nil
}

func (s *leadService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "ListSorted")

	leads, err := s.leadRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder, "Owner")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list leads sorted")
		return nil, 0, err
	}

	total, err := s.leadRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count leads")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed sorted successfully")
	return leads, total, nil
}

// ListFiltered lists one page of the leads matching filter, with their owner,
// and the number of leads the same filter matches before pagination. The log
// lines carry ids, offsets and counts only: never the search term or the sort,
// and the status, search and classification only as presence flags.
func (s *leadService) ListFiltered(filter repository.LeadListFilter, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error) {
	fields := map[string]interface{}{
		"offset":                offset,
		"limit":                 limit,
		"status_filter":         filter.Status != "",
		"search_filter":         filter.Search != "",
		"classification_filter": filter.Classification != "",
	}
	if filter.OwnerID != nil {
		fields["owner_id"] = *filter.OwnerID
	}
	logger := utils.LogServiceCall(utils.Logger.WithFields(fields), "LeadService", "ListFiltered")

	leads, err := s.leadRepo.ListByFilter(filter, offset, limit, sortBy, sortOrder, "Owner")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list filtered leads")
		return nil, 0, err
	}

	total, err := s.leadRepo.CountByFilter(filter)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count filtered leads")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Filtered leads listed successfully")
	return leads, total, nil
}

func (s *leadService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "Search")

	leads, err := s.leadRepo.Search(query, offset, limit, sortBy, sortOrder, "Owner")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to search leads")
		return nil, 0, err
	}

	total, err := s.leadRepo.CountSearch(query)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count search results")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Lead search completed")
	return leads, total, nil
}
