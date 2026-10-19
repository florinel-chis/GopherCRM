package service

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// LeadViewer is the caller a lead-visibility decision is made for.
type LeadViewer struct {
	UserID uint
	Role   models.UserRole
}

// CanViewLead is the one lead-visibility policy. It decides every association
// that can carry a lead (tasks and deals), when the link is written and when
// it is read back: an admin sees every lead, a sales user only the leads it
// owns, and every other role (support, customer, or none) no lead at all.
// A nil lead, one that is not loaded or no longer exists, is visible to
// admins only.
func CanViewLead(viewer LeadViewer, lead *models.Lead) bool {
	switch viewer.Role {
	case models.RoleAdmin:
		return true
	case models.RoleSales:
		return lead != nil && viewer.UserID != 0 && lead.OwnerID == viewer.UserID
	default:
		return false
	}
}

// RedactTaskLead removes a lead the viewer may not see from a task about to
// be returned. Both the preloaded lead and lead_id go, so neither the lead's
// data nor the fact that the task points at it leaks. The decision is made
// on the preloaded lead and re-made on every read, so a link written before
// the lead changed owner, or before the task was reassigned, stops showing as
// soon as the policy says so. A lead_id without its lead (not preloaded, or a
// preloaded lead that is not the linked one) is hidden from everyone but
// admins.
func RedactTaskLead(viewer LeadViewer, task *models.Task) {
	if task == nil || (task.LeadID == nil && task.Lead == nil) {
		return
	}
	if CanViewLead(viewer, linkedLead(task.LeadID, task.Lead)) {
		return
	}
	task.LeadID, task.Lead = nil, nil
}

// RedactTaskLeads applies RedactTaskLead to every task of a page.
func RedactTaskLeads(viewer LeadViewer, tasks []models.Task) {
	for i := range tasks {
		RedactTaskLead(viewer, &tasks[i])
	}
}

// RedactDealLead is RedactTaskLead for a deal.
func RedactDealLead(viewer LeadViewer, deal *models.Deal) {
	if deal == nil || (deal.LeadID == nil && deal.Lead == nil) {
		return
	}
	if CanViewLead(viewer, linkedLead(deal.LeadID, deal.Lead)) {
		return
	}
	deal.LeadID, deal.Lead = nil, nil
}

// RedactDealLeads applies RedactDealLead to every deal of a page.
func RedactDealLeads(viewer LeadViewer, deals []models.Deal) {
	for i := range deals {
		RedactDealLead(viewer, &deals[i])
	}
}

// linkedLead is the preloaded lead when it is the one the id points at, and
// nil otherwise, so a stale copy can never vouch for a different link.
func linkedLead(leadID *uint, lead *models.Lead) *models.Lead {
	if lead == nil || leadID == nil || *leadID != lead.ID {
		return nil
	}
	return lead
}

// checkLeadLink is the write side of CanViewLead: leadID must name a live lead
// that the acting user may see. The actor's role is read from the user
// repository rather than trusted from the caller. A lead that does not exist
// and one the actor may not see both get ErrLeadNotFound, so a write cannot be
// used to probe for leads hidden from the caller. The lead is returned for
// the caller to attach to its response.
func checkLeadLink(leadRepo repository.LeadRepository, userRepo repository.UserRepository, actorID, leadID uint) (*models.Lead, error) {
	lead, err := leadRepo.GetByID(leadID)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("unknown lead_id %d: %w", leadID, apperrors.ErrLeadNotFound)
		}
		return nil, err
	}

	viewer, err := leadViewerOf(userRepo, actorID)
	if err != nil {
		return nil, err
	}
	if !CanViewLead(viewer, lead) {
		return nil, fmt.Errorf("unknown lead_id %d: %w", leadID, apperrors.ErrLeadNotFound)
	}
	return lead, nil
}

// leadViewerOf loads the acting user. No actor (id 0) and an unknown one are
// a viewer without a role, which sees no lead.
func leadViewerOf(userRepo repository.UserRepository, actorID uint) (LeadViewer, error) {
	if actorID == 0 {
		return LeadViewer{}, nil
	}
	actor, err := userRepo.GetByID(actorID)
	if err != nil {
		if isNotFound(err) {
			return LeadViewer{}, nil
		}
		return LeadViewer{}, err
	}
	return LeadViewer{UserID: actor.ID, Role: actor.Role}, nil
}
