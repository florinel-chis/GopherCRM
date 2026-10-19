package handler

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/gin-gonic/gin"
)

// leadViewer is the authenticated caller as the lead-visibility policy
// (service.CanViewLead) sees it. Every task and deal a handler returns goes
// through service.RedactTaskLead(s) / service.RedactDealLead(s) with it, so a
// lead link the caller may not see is never part of a response.
func leadViewer(c *gin.Context) service.LeadViewer {
	return service.LeadViewer{
		UserID: c.GetUint("user_id"),
		Role:   models.UserRole(c.GetString("user_role")),
	}
}
