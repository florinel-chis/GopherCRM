package service

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// LeadExportParams are the list parameters a GET /leads/export request
// carried, after the handler dropped an unknown sort_by and coerced a bad
// sort_order. Which of them apply depends on the caller (ExportForActor).
type LeadExportParams struct {
	Search         string
	Classification string
	SortBy         string
	SortOrder      string
}

// ExportForActor returns exactly the leads the actor may list, unpaginated, for
// the CSV export (#234). actor.Scope is the caller's leads.export scope, and
// the rows follow the lead list's (LeadHandler.List):
//
//   - scope all (admin on the system matrix) gets every live lead matching the list's combined filters: a
//     non-empty search and an exact classification are ANDed, and the rows
//     follow the sort when one is given, else ascending id order;
//   - scope own (sales) gets only the actor's own leads in ascending id
//     order, and every parameter it sent is ignored, as the list ignores them
//     for that scope;
//   - a caller without the key (support, customer, unknown) and an own-scope
//     actor without an id get an error matching apperrors.ErrForbidden,
//     without a read.
//
// The rows come from one ordered statement (LeadRepository.ListAllForExport).
// The log lines carry the operation, the scope, the user id, the row count and
// presence flags only: never a lead's data, the search term or the raw sort.
func (s *leadService) ExportForActor(actor LeadViewer, params LeadExportParams) ([]models.Lead, error) {
	logger := utils.Logger.WithFields(logrus.Fields{
		"operation":          "LeadService.ExportForActor",
		"scope":              string(actor.Scope),
		"user_id":            actor.UserID,
		"has_search":         params.Search != "",
		"has_classification": params.Classification != "",
		"has_sort":           params.SortBy != "",
	})

	var filter repository.LeadExportFilter
	switch {
	case actor.Scope == models.PermissionScopeAll:
		filter.Search = params.Search
		filter.Classification = models.LeadClassification(params.Classification)
		if params.SortBy != "" {
			filter.SortBy = params.SortBy
			filter.SortOrder = params.SortOrder
		}
	case actor.Scope == models.PermissionScopeOwn && actor.UserID != 0:
		ownerID := actor.UserID
		filter.OwnerID = &ownerID
	default:
		logger.Warn("Lead export refused for this scope")
		return nil, fmt.Errorf("lead export refused: %w", apperrors.ErrForbidden)
	}

	leads, err := s.leadRepo.ListAllForExport(filter)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to read leads for export")
		return nil, err
	}

	logger.WithField("count", len(leads)).Info("Leads read for export")
	return leads, nil
}
