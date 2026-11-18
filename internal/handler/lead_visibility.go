package handler

import (
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/gin-gonic/gin"
)

// leadViewer is the authenticated caller as the lead-visibility policy
// (service.CanViewLead) sees it. Every task and deal a handler returns goes
// through service.RedactTaskLead(s) / service.RedactDealLead(s) with it, so a
// lead link the caller may not see is never part of a response. The viewer is
// built from leads.view by the permission context: scope all sees every lead,
// scope own the caller's own, and a caller without the key none.
func leadViewer(c *gin.Context) service.LeadViewer {
	return viewerFor(c, permissions.LeadsView)
}
