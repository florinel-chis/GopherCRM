package handler

import (
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// AutomationHandler serves the follow-up automation API: the admin-only
// rule CRUD and dry run, and the run history.
type AutomationHandler struct {
	automationService service.AutomationService
}

func NewAutomationHandler(automationService service.AutomationService) *AutomationHandler {
	return &AutomationHandler{automationService: automationService}
}

// AutomationStepRequest is one step of a rule body. The config fields that
// apply depend on step_type; see models.AutomationStepConfig.
type AutomationStepRequest struct {
	StepType string                      `json:"step_type"`
	Config   models.AutomationStepConfig `json:"config"`
}

// AutomationRuleRequest is the body of POST /automation/rules and
// PUT /automation/rules/:id. Steps are numbered 1..n in the given order. Every
// rule limit (20 steps, 90 days of waits, a forward else_position, known
// placeholders, valid trigger and step configs) is checked by the service and
// answered with 400 INVALID_INPUT, so nothing here carries a binding tag.
type AutomationRuleRequest struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	EntityType  string                   `json:"entity_type"`
	TriggerType string                   `json:"trigger_type"`
	Trigger     models.AutomationTrigger `json:"trigger"`
	Enabled     *bool                    `json:"enabled"`
	Steps       []AutomationStepRequest  `json:"steps"`
}

// toModel turns the request into the rule the service validates.
func (req *AutomationRuleRequest) toModel() *models.AutomationRule {
	rule := &models.AutomationRule{
		Name:        req.Name,
		Description: req.Description,
		EntityType:  req.EntityType,
		TriggerType: req.TriggerType,
		Trigger:     req.Trigger,
		Steps:       make([]models.AutomationStep, 0, len(req.Steps)),
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	for i, step := range req.Steps {
		rule.Steps = append(rule.Steps, models.AutomationStep{Position: i + 1, StepType: step.StepType, Config: step.Config})
	}
	return rule
}

// AutomationDryRunRequest is the body of POST /automation/rules/:id/dry-run.
type AutomationDryRunRequest struct {
	EntityType string `json:"entity_type" binding:"required,oneof=lead deal"`
	EntityID   uint   `json:"entity_id" binding:"required,min=1"`
}

// CreateRule godoc
// @Summary Create an automation rule
// @Description Create a follow-up automation rule with its ordered steps (admin only). entity_type is lead or deal; trigger_type is lead_created, lead_updated, lead_status_changed, deal_created, deal_updated, deal_stage_changed or schedule; trigger carries from/to for a status or stage change and hour_utc, filters and stale_days for a schedule. Steps are wait, condition, create_task, assign_owner, send_email and notify, numbered 1..n in the given order. An invalid trigger or step config, more than 20 steps, more than 90 days of waits, a backward or out-of-range else_position or an unknown placeholder is 400 INVALID_INPUT and writes no row; an unknown, inactive or non-staff user id is 400 INVALID_REFERENCE. A new rule is disabled unless enabled is true.
// @Tags automation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body AutomationRuleRequest true "Rule"
// @Success 201 {object} utils.APIResponse{data=models.AutomationRule} "Rule created with its steps"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid rule (INVALID_INPUT) or unknown user (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "Too many requests - rate limit exceeded"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules [post]
func (h *AutomationHandler) CreateRule(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.CreateRule")

	var req AutomationRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	rule := req.toModel()
	if err := h.automationService.CreateRule(rule, c.GetUint("user_id")); err != nil {
		h.respondError(c, logger, err)
		return
	}
	if created, err := h.automationService.GetRule(rule.ID); err == nil {
		rule = created
	}
	utils.LogHandlerResponse(logger, http.StatusCreated, rule)
	utils.RespondSuccess(c, http.StatusCreated, rule)
}

// ListRules godoc
// @Summary List automation rules
// @Description One page of rules with their steps (admin only). Filters: entity_type, enabled. sort_by must be one of the listed columns (anything else is 400) and defaults to created_at; sort_order defaults to desc. Pagination goes through the shared parser: limit=0 is 20, the cap is 100.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param entity_type query string false "Only rules of this entity type" Enums(lead, deal)
// @Param enabled query bool false "Only enabled (true) or disabled (false) rules"
// @Param sort_by query string false "Sort column" Enums(id, name, entity_type, trigger_type, enabled, version, created_at, updated_at) default(created_at)
// @Param sort_order query string false "Sort direction" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AutomationRule,meta=utils.APIMeta} "Rules retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "sort_by is not an allowed column or a filter is malformed"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules [get]
func (h *AutomationHandler) ListRules(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.ListRules")

	offset, limit, sortBy, sortOrder, ok := listParams(c, "automation_rules")
	if !ok {
		return
	}
	filter := repository.AutomationRuleListFilter{EntityType: c.Query("entity_type"), SortBy: sortBy, SortOrder: sortOrder}
	if filter.EntityType != "" && filter.EntityType != models.AutomationEntityLead && filter.EntityType != models.AutomationEntityDeal {
		utils.RespondBadRequest(c, "entity_type must be lead or deal")
		return
	}
	if raw := c.Query("enabled"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			utils.RespondBadRequest(c, "enabled must be true or false")
			return
		}
		filter.Enabled = &enabled
	}

	rules, total, err := h.automationService.ListRules(offset, limit, filter)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, rules)
	utils.RespondSuccessWithMeta(c, http.StatusOK, rules, pageMeta(c, offset, limit, total))
}

// GetRule godoc
// @Summary Get an automation rule
// @Description The rule with its steps in position order (admin only).
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "Rule retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid rule ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Rule not found"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules/{id} [get]
func (h *AutomationHandler) GetRule(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.GetRule")
	id, ok := pathID(c, "Invalid rule ID")
	if !ok {
		return
	}
	rule, err := h.automationService.GetRule(id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, rule)
	utils.RespondSuccess(c, http.StatusOK, rule)
}

// UpdateRule godoc
// @Summary Update an automation rule
// @Description Replace the rule wholesale (admin only): the same body as the create. In one transaction the version is incremented, the steps are replaced and the rule's pending, waiting and running runs are cancelled with cancel_reason rule_changed. enabled absent keeps the stored switch. The same 400 answers as the create apply and leave the rule untouched.
// @Tags automation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Param request body AutomationRuleRequest true "Rule"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "Rule updated with its new version and steps"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid rule (INVALID_INPUT) or unknown user (INVALID_REFERENCE)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Rule not found"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules/{id} [put]
func (h *AutomationHandler) UpdateRule(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.UpdateRule")
	id, ok := pathID(c, "Invalid rule ID")
	if !ok {
		return
	}
	var req AutomationRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	rule := req.toModel()
	if req.Enabled == nil {
		stored, err := h.automationService.GetRule(id)
		if err != nil {
			h.respondError(c, logger, err)
			return
		}
		rule.Enabled = stored.Enabled
	}
	updated, err := h.automationService.UpdateRule(id, rule)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, updated)
	utils.RespondSuccess(c, http.StatusOK, updated)
}

// EnableRule godoc
// @Summary Enable an automation rule
// @Description Switch the rule on (admin only). Returns the rule.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "Rule enabled"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid rule ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Rule not found"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules/{id}/enable [post]
func (h *AutomationHandler) EnableRule(c *gin.Context) {
	h.setEnabled(c, true)
}

// DisableRule godoc
// @Summary Disable an automation rule
// @Description Switch the rule off (admin only) and cancel its pending, waiting and running runs with cancel_reason rule_disabled, in one transaction. Returns the rule.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "Rule disabled"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid rule ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Rule not found"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules/{id}/disable [post]
func (h *AutomationHandler) DisableRule(c *gin.Context) {
	h.setEnabled(c, false)
}

func (h *AutomationHandler) setEnabled(c *gin.Context, enabled bool) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.SetEnabled")
	id, ok := pathID(c, "Invalid rule ID")
	if !ok {
		return
	}
	rule, err := h.automationService.SetEnabled(id, enabled)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, rule)
	utils.RespondSuccess(c, http.StatusOK, rule)
}

// DeleteRule godoc
// @Summary Delete an automation rule
// @Description Soft-delete the rule (admin only) and cancel its pending, waiting and running runs with cancel_reason rule_deleted, in one transaction. Runs and their logs stay.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 204 "Rule deleted"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid rule ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Rule not found"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules/{id} [delete]
func (h *AutomationHandler) DeleteRule(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.DeleteRule")
	id, ok := pathID(c, "Invalid rule ID")
	if !ok {
		return
	}
	if err := h.automationService.DeleteRule(id); err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusNoContent, nil)
	c.Status(http.StatusNoContent)
}

// DryRun godoc
// @Summary Dry-run an automation rule
// @Description Simulate the rule against one record of its entity type (admin only) and report each step's would-be outcome: the outcome, the simulated time, the resolved user ids, the task due date and link, the owner from/to and the branch taken. Waits advance the simulated clock without pausing and an earlier simulated assign feeds later conditions. Nothing is written, sent or notified. A record of the wrong entity type is 400 INVALID_INPUT; a missing rule or record is 404.
// @Tags automation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Param request body AutomationDryRunRequest true "The record to simulate against"
// @Success 200 {object} utils.APIResponse{data=models.AutomationDryRunReport} "Per-step report"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid body, or a record of the wrong entity type (INVALID_INPUT)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Rule or record not found"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/rules/{id}/dry-run [post]
func (h *AutomationHandler) DryRun(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.DryRun")
	id, ok := pathID(c, "Invalid rule ID")
	if !ok {
		return
	}
	var req AutomationDryRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	report, err := h.automationService.DryRun(id, req.EntityType, req.EntityID)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, report)
	utils.RespondSuccess(c, http.StatusOK, report)
}

// ListRuns godoc
// @Summary List automation runs
// @Description One page of runs (admin and sales). The role scope is applied inside the query: an admin sees every run, a sales user only runs whose lead or deal it currently owns, so meta.total counts that set. Filters: status, rule_id, entity_type, entity_id. sort_by is limited to id, created_at, updated_at, status and resume_at (anything else is 400). Support and customer users get 403.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); when supplied it overrides offset"
// @Param offset query int false "Result offset" default(0)
// @Param limit query int false "Page size, capped at 100" default(20)
// @Param status query string false "Only runs in this status" Enums(pending, running, waiting, completed, failed, cancelled)
// @Param rule_id query int false "Only runs of this rule"
// @Param entity_type query string false "Only runs on this entity type" Enums(lead, deal)
// @Param entity_id query int false "Only runs on this record (with entity_type)"
// @Param sort_by query string false "Sort column" Enums(id, created_at, updated_at, status, resume_at) default(created_at)
// @Param sort_order query string false "Sort direction" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AutomationRun,meta=utils.APIMeta} "Runs retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "sort_by is not an allowed column or a filter is malformed"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/runs [get]
func (h *AutomationHandler) ListRuns(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.ListRuns")

	offset, limit, sortBy, sortOrder, ok := listParams(c, "automation_runs")
	if !ok {
		return
	}
	filter := repository.AutomationRunListFilter{Status: c.Query("status"), EntityType: c.Query("entity_type"), SortBy: sortBy, SortOrder: sortOrder}
	switch filter.Status {
	case "", models.AutomationRunPending, models.AutomationRunRunning, models.AutomationRunWaiting,
		models.AutomationRunCompleted, models.AutomationRunFailed, models.AutomationRunCancelled:
	default:
		utils.RespondBadRequest(c, "unknown run status")
		return
	}
	if filter.EntityType != "" && filter.EntityType != models.AutomationEntityLead && filter.EntityType != models.AutomationEntityDeal {
		utils.RespondBadRequest(c, "entity_type must be lead or deal")
		return
	}
	if filter.RuleID, ok = queryID(c, "rule_id"); !ok {
		return
	}
	if filter.EntityID, ok = queryID(c, "entity_id"); !ok {
		return
	}

	runs, total, err := h.automationService.ListRuns(leadViewer(c), offset, limit, filter)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, runs)
	utils.RespondSuccessWithMeta(c, http.StatusOK, runs, pageMeta(c, offset, limit, total))
}

// GetRun godoc
// @Summary Get an automation run
// @Description The run with its step log (admin and sales). A sales user asking for a run on a record it does not own gets 404. Support and customer users get 403.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Run ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRun} "Run retrieved successfully"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid run ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Run not found, or outside the caller's scope"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/runs/{id} [get]
func (h *AutomationHandler) GetRun(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.GetRun")
	id, ok := pathID(c, "Invalid run ID")
	if !ok {
		return
	}
	run, err := h.automationService.GetRun(leadViewer(c), id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, run)
	utils.RespondSuccess(c, http.StatusOK, run)
}

// CancelRun godoc
// @Summary Cancel an automation run
// @Description Cancel a pending, waiting or running run with cancel_reason manual (admin only). A completed, failed or cancelled run is 409 INVALID_STATUS_TRANSITION. A running run performs no further step: its next conditional advance affects zero rows.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Run ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRun} "Run cancelled"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "Invalid run ID"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "Unauthorized"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "Forbidden - admin role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "Run not found"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "The run is already finished (INVALID_STATUS_TRANSITION)"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "Internal server error"
// @Router /automation/runs/{id}/cancel [post]
func (h *AutomationHandler) CancelRun(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.CancelRun")
	id, ok := pathID(c, "Invalid run ID")
	if !ok {
		return
	}
	run, err := h.automationService.CancelRun(id)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, run)
	utils.RespondSuccess(c, http.StatusOK, run)
}

// respondError maps the automation sentinels onto status codes. Anything
// unclassified is a server error and is not echoed back to the client.
func (h *AutomationHandler) respondError(c *gin.Context, logger *logrus.Entry, err error) {
	switch {
	case errors.Is(err, apperrors.ErrAssigneeNotFound):
		logger.WithError(utils.LogError(err)).Warn("Automation rule names an unknown user")
		utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidReference, err.Error(), nil)
	case errors.Is(err, apperrors.ErrValidation):
		logger.WithError(utils.LogError(err)).Warn("Automation request rejected")
		utils.RespondError(c, http.StatusBadRequest, apperrors.CodeInvalidInput, err.Error(), nil)
	case errors.Is(err, apperrors.ErrAutomationRunFinished):
		logger.WithError(utils.LogError(err)).Warn("Automation run already finished")
		utils.RespondError(c, http.StatusConflict, apperrors.CodeInvalidStatusTransition, err.Error(), nil)
	case errors.Is(err, apperrors.ErrForbidden):
		logger.WithError(utils.LogError(err)).Warn("Automation access refused")
		utils.RespondForbidden(c, "Access denied")
	case errors.Is(err, apperrors.ErrAutomationRuleNotFound),
		errors.Is(err, apperrors.ErrAutomationRunNotFound),
		apperrors.IsNotFound(err):
		// The two automation sentinels are their own identities, not wrapped
		// in ErrNotFound, so the shared helper alone would miss them and a
		// missing rule or run would be answered as a server error.
		logger.WithError(utils.LogError(err)).Warn("Automation resource not found")
		utils.RespondNotFound(c, err.Error())
	default:
		logger.WithError(utils.LogError(err)).Error("Automation request failed")
		utils.RespondInternalError(c)
	}
}
