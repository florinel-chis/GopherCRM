package service

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// CanViewDeal is the one deal-visibility policy behind a task's deal link, when
// the link is written and when it is read back: an admin sees every deal, a
// sales user only the deals it owns, and every other role (support, customer,
// or none) no deal at all. A nil deal, one that is not loaded or no longer
// exists, is visible to admins only.
func CanViewDeal(viewer LeadViewer, deal *models.Deal) bool {
	switch viewer.Role {
	case models.RoleAdmin:
		return true
	case models.RoleSales:
		return deal != nil && viewer.UserID != 0 && deal.OwnerID == viewer.UserID
	default:
		return false
	}
}

// RedactTaskDeal removes a deal the viewer may not see from a task about to be
// returned. Both the preloaded deal and deal_id go, so neither the deal's data
// nor the fact that the task points at it leaks. The decision is made on the
// preloaded deal and re-made on every read, so a link written before the deal
// changed owner stops showing as soon as the policy says so. A deal_id without
// its deal (not preloaded, soft-deleted, or a preloaded deal that is not the
// linked one) is hidden from everyone but admins.
func RedactTaskDeal(viewer LeadViewer, task *models.Task) {
	if task == nil || (task.DealID == nil && task.Deal == nil) {
		return
	}
	if CanViewDeal(viewer, linkedDeal(task.DealID, task.Deal)) {
		return
	}
	task.DealID, task.Deal = nil, nil
}

// RedactTaskDeals applies RedactTaskDeal to every task of a page.
func RedactTaskDeals(viewer LeadViewer, tasks []models.Task) {
	for i := range tasks {
		RedactTaskDeal(viewer, &tasks[i])
	}
}

// linkedDeal is the preloaded deal when it is the one the id points at, and nil
// otherwise, so a stale copy can never vouch for a different link.
func linkedDeal(dealID *uint, deal *models.Deal) *models.Deal {
	if deal == nil || dealID == nil || *dealID != deal.ID {
		return nil
	}
	return deal
}

// checkDealLink is the write side of CanViewDeal: dealID must name a live deal
// that the acting user may see, closed deals included. The actor's role is read
// from the user repository rather than trusted from the caller. A deal that
// does not exist and one the actor may not see both get ErrDealNotFound with
// the same message, so a write cannot be used to probe for hidden deals. Only a
// not-found lookup is mapped: any other failure is returned as it is, so an
// outage answers 500 and not 404. The deal is returned for the caller to attach
// to its response.
func checkDealLink(dealRepo repository.DealRepository, userRepo repository.UserRepository, actorID, dealID uint) (*models.Deal, error) {
	deal, err := dealRepo.GetByID(dealID)
	if err != nil {
		if isNotFound(err) {
			return nil, unknownDealError(dealID)
		}
		return nil, err
	}

	viewer, err := leadViewerOf(userRepo, actorID)
	if err != nil {
		return nil, err
	}
	if !CanViewDeal(viewer, deal) {
		return nil, unknownDealError(dealID)
	}
	return deal, nil
}

func unknownDealError(dealID uint) error {
	return fmt.Errorf("unknown deal_id %d: %w", dealID, apperrors.ErrDealNotFound)
}
