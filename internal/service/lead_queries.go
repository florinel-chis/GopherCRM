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
