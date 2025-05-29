package handler

import (
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

type DashboardHandler struct {
	leadService     service.LeadService
	customerService service.CustomerService
	ticketService   service.TicketService
	taskService     service.TaskService
}

func NewDashboardHandler(
	leadService service.LeadService,
	customerService service.CustomerService,
	ticketService service.TicketService,
	taskService service.TaskService,
) *DashboardHandler {
	return &DashboardHandler{
		leadService:     leadService,
		customerService: customerService,
		ticketService:   ticketService,
		taskService:     taskService,
	}
}

type DashboardStats struct {
	TotalLeads     int64   `json:"total_leads"`
	TotalCustomers int64   `json:"total_customers"`
	OpenTickets    int64   `json:"open_tickets"`
	PendingTasks   int64   `json:"pending_tasks"`
	ConversionRate float64 `json:"conversion_rate"`
}

func (h *DashboardHandler) GetStats(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "DashboardHandler.GetStats")

	// Get total leads
	totalLeads, err := h.leadService.GetCount()
	if err != nil {
		logger.WithError(err).Error("Failed to get leads count")
		utils.RespondInternalError(c)
		return
	}

	// Get total customers
	totalCustomers, err := h.customerService.GetCount()
	if err != nil {
		logger.WithError(err).Error("Failed to get customers count")
		utils.RespondInternalError(c)
		return
	}

	// Get open tickets count
	openTickets, err := h.ticketService.GetOpenCount()
	if err != nil {
		logger.WithError(err).Error("Failed to get open tickets count")
		utils.RespondInternalError(c)
		return
	}

	// Get pending tasks count
	pendingTasks, err := h.taskService.GetPendingCount()
	if err != nil {
		logger.WithError(err).Error("Failed to get pending tasks count")
		utils.RespondInternalError(c)
		return
	}

	// Calculate conversion rate (customers / leads * 100)
	conversionRate := float64(0)
	if totalLeads > 0 {
		conversionRate = (float64(totalCustomers) / float64(totalLeads)) * 100
	}

	stats := DashboardStats{
		TotalLeads:     totalLeads,
		TotalCustomers: totalCustomers,
		OpenTickets:    openTickets,
		PendingTasks:   pendingTasks,
		ConversionRate: conversionRate,
	}

	utils.LogHandlerResponse(logger, http.StatusOK, stats)
	utils.RespondSuccess(c, http.StatusOK, stats)
}