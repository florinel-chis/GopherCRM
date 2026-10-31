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
//
// @Description One step of an automation rule body.
type AutomationStepRequest struct {
	// What the step does; see models.AutomationStepConfig for the fields each type takes. An unknown type is refused (invalid_value).
	StepType string `json:"step_type" enums:"wait,condition,create_task,assign_owner,send_email,notify" example:"create_task"`
	// The step configuration; only the fields of the step type apply, and a field set where it does not apply may be refused (not_applicable).
	Config models.AutomationStepConfig `json:"config"`
}

// AutomationRuleRequest is the body of POST /automation/rules and
// PUT /automation/rules/:id. Steps are numbered 1..n in the given order. Every
// rule limit (20 steps, 90 days of waits, a forward else_position, known
// placeholders, valid trigger and step configs) is checked by the service and
// answered with 400 INVALID_INPUT, so nothing here carries a binding tag. The
// validate tags only mark the service-enforced required fields and step count
// in the API specification: gin validates binding tags, never validate tags.
//
// @Description Body of POST /automation/rules and PUT /automation/rules/{id}, and the rule of a draft dry run. Validated by the service (400 INVALID_INPUT or INVALID_REFERENCE with error.details.fields), not by binding rules.
type AutomationRuleRequest struct {
	// Rule name. Required: trimmed, 1 to 200 characters (refused with required or too_long).
	Name string `json:"name" validate:"required" maxLength:"200" example:"Welcome new web leads"`
	// Optional description, at most 65535 bytes.
	Description string `json:"description" example:"Creates a call-back task for every lead from the website."`
	// Kind of record the rule runs on. Required; any other value is refused (invalid_value) and stops the remaining checks.
	EntityType string `json:"entity_type" validate:"required" enums:"lead,deal" example:"lead"`
	// What starts a run. Required. Lead rules take lead_created, lead_updated, lead_status_changed or schedule; deal rules deal_created, deal_updated, deal_stage_changed or schedule (anything else is invalid_value and skips the trigger checks).
	TriggerType string `json:"trigger_type" validate:"required" enums:"lead_created,lead_updated,lead_status_changed,deal_created,deal_updated,deal_stage_changed,schedule" example:"lead_created"`
	// Trigger configuration: from and to for a status or stage change, hour_utc, filters and stale_days for a schedule.
	Trigger models.AutomationTrigger `json:"trigger"`
	// Whether the rule starts runs. On create, absent or null means false; on update, absent or null keeps the stored value. Ignored by the draft dry run.
	Enabled *bool `json:"enabled" example:"true"`
	// The ordered steps, 1 to 20; they are numbered 1..n in this order. Missing or more than 20 skips the per-step checks.
	Steps []AutomationStepRequest `json:"steps" validate:"required,min=1,max=20"`
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

// AutomationRuleErrorDetails is error.details of a refused rule body: every
// refused field with its path, code and message. The paths index the steps
// array from zero (steps[2].config.title is the third step's title).
//
// @Description error.details of a refused automation rule body.
type AutomationRuleErrorDetails struct {
	// Every refused field, in the order the checks ran; each field appears once, with its first refusal.
	Fields []service.RuleFieldError `json:"fields"`
}

// AutomationDraftDryRunRequest is the body of POST /automation/rules/dry-run:
// an unsaved rule body (the create body; enabled is ignored) and the record
// to simulate it against.
//
// @Description Body of POST /automation/rules/dry-run: an unsaved rule and the record to simulate it against.
type AutomationDraftDryRunRequest struct {
	// The unsaved rule body, as for POST /automation/rules (enabled is ignored).
	Rule AutomationRuleRequest `json:"rule" binding:"required"`
	// Kind of the record to simulate against; must match rule.entity_type (400 INVALID_INPUT otherwise).
	EntityType string `json:"entity_type" binding:"required,oneof=lead deal" example:"lead"`
	// ID of the live lead or deal to simulate against (404 when it does not exist).
	EntityID uint `json:"entity_id" binding:"required,min=1" example:"12"`
}

// AutomationDryRunRequest is the body of POST /automation/rules/:id/dry-run.
//
// @Description Body of POST /automation/rules/{id}/dry-run: the record to simulate the saved rule against.
type AutomationDryRunRequest struct {
	// Kind of the record to simulate against; must match the rule's entity_type (400 INVALID_INPUT otherwise).
	EntityType string `json:"entity_type" binding:"required,oneof=lead deal" example:"lead"`
	// ID of the live lead or deal to simulate against (404 when it does not exist).
	EntityID uint `json:"entity_id" binding:"required,min=1" example:"12"`
}

// CreateRule godoc
// @Summary Create an automation rule
// @Description Create a follow-up automation rule with its ordered steps. Roles: admin only. entity_type is lead or deal; trigger_type is lead_created, lead_updated, lead_status_changed, deal_created, deal_updated, deal_stage_changed or schedule; trigger carries from/to for a status or stage change and hour_utc, filters and stale_days for a schedule. Steps are wait, condition, create_task, assign_owner, send_email and notify, numbered 1..n in the given order. Validation (nothing is written when it fails): name required, at most 200 characters; description at most 65535 bytes; an invalid trigger or step config, more than 20 steps, more than 90 days of waits, a backward or out-of-range else_position or an unknown placeholder is 400 INVALID_INPUT; an unknown, inactive or customer user id is 400 INVALID_REFERENCE when it is the first refusal. Every refused field is listed in error.details.fields as {field, code, message}: field is a path into the body (name, trigger.hour_utc, steps[i].config.title with i counted from zero), code one of required, too_long, line_break, invalid_value, out_of_range, not_applicable, unknown_placeholder, too_many, unknown_reference; error.message and error.code are those of the first refused field. An unknown entity_type stops the checks, an unknown trigger_type skips the trigger checks, and no steps or more than 20 skips the step checks. Defaults are stored: recipient owner, else stop. A new rule is version 1, records the caller as created_by_id, and is disabled unless enabled is true. Runs are started by the background worker, which matches each event against the rules that are enabled when it dispatches the event.
// @Tags automation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body AutomationRuleRequest true "The rule"
// @Success 201 {object} utils.APIResponse{data=models.AutomationRule} "Rule created with its steps"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError{details=AutomationRuleErrorDetails}} "INVALID_INPUT - the rule body was refused (error.details.fields lists every refused field; error.message and error.code are those of the first); INVALID_REFERENCE - the same, when the first refusal is an unknown, deactivated or customer user_id; BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description One page of live (not deleted) rules with their steps. Roles: admin only. Filters: entity_type and enabled; an unknown entity_type or a non-boolean enabled is 400. sort_by must be one of the listed columns (anything else is 400). An absent sort_by sorts by created_at descending and then ignores sort_order; with a sort_by, sort_order is asc or desc and anything else means desc. Ties are broken by id in the same direction. Pagination: page (1-based) overrides offset when greater than 0; limit defaults to 20 and is clamped to 100; the checks run before any query, sort first.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); overrides offset when greater than 0"
// @Param offset query int false "Number of items to skip" default(0)
// @Param limit query int false "Page size; non-positive values fall back to 20, larger values are clamped to 100" default(20)
// @Param entity_type query string false "Only rules of this entity type" Enums(lead, deal)
// @Param enabled query bool false "Only enabled (true) or disabled (false) rules; any value strconv.ParseBool accepts (1, t, TRUE, 0, f, ...)"
// @Param sort_by query string false "Sort column; created_at when absent, anything outside the list is 400" Enums(id, name, entity_type, trigger_type, enabled, version, created_at, updated_at)
// @Param sort_order query string false "Sort direction, honoured only with sort_by; anything else means desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AutomationRule,meta=utils.APIMeta} "One page of rules; meta carries page, per_page, total and total_pages"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - sort_by is not an allowed column (message Invalid sort column), entity_type is not lead or deal, or enabled is not a boolean"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description One live rule with its steps in position order. Roles: admin only. A deleted rule is 404.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "The rule"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live rule with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description Replace the rule wholesale with the same body as the create. Roles: admin only. The body is validated first (the same 400 answers and error.details.fields as the create, before the rule is looked up), then in one transaction the rule row is locked, the version incremented, the steps replaced (new step ids) and the rule's pending, waiting and running runs cancelled with cancel_reason rule_changed. enabled absent keeps the stored switch as read under the row lock, so a disable committed meanwhile is not undone. created_by_id is kept. Any failure leaves the rule, its steps and its runs untouched.
// @Tags automation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Param request body AutomationRuleRequest true "The complete new rule"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "Rule updated with its new version and steps"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError{details=AutomationRuleErrorDetails}} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here); INVALID_INPUT - the rule body was refused (error.details.fields lists every refused field; error.message and error.code are those of the first); INVALID_REFERENCE - the same, when the first refusal is an unknown, deactivated or customer user_id; BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live rule with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
	updated, err := h.automationService.UpdateRule(id, rule, req.Enabled)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, updated)
	utils.RespondSuccess(c, http.StatusOK, updated)
}

// EnableRule godoc
// @Summary Enable an automation rule
// @Description Switch the rule on and return it. Roles: admin only. Idempotent; the version is not changed. The background worker matches events against the rules enabled at dispatch time, so events recorded shortly before but not yet dispatched can also start runs.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "The rule, enabled"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live rule with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /automation/rules/{id}/enable [post]
func (h *AutomationHandler) EnableRule(c *gin.Context) {
	h.setEnabled(c, true)
}

// DisableRule godoc
// @Summary Disable an automation rule
// @Description Switch the rule off and cancel its pending, waiting and running runs with cancel_reason rule_disabled, in one transaction, and return the rule. Roles: admin only. The version is not changed; disabling a disabled rule still cancels any active run.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRule} "The rule, disabled"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live rule with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description Soft-delete the rule and cancel its pending, waiting and running runs with cancel_reason rule_deleted, in one transaction. Roles: admin only. Runs and their logs stay readable; they report the rule's name with rule_deleted true.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Success 204 "Rule deleted; no body"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live rule with this id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description Simulate a saved rule against one live record of its entity type and report each step's would-be outcome: the outcome, the simulated time, the resolved user ids, the task due date and link, the owner from/to and the branch taken. Roles: admin only. Waits advance the simulated clock without pausing, an earlier simulated assign feeds later conditions and recipients, and steps skipped by a condition branch are not listed. The rule is simulated whether or not it is enabled. Nothing is written, sent or notified. Order of checks: id, body binding, rule lookup, entity type match, record lookup.
// @Tags automation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Rule ID"
// @Param request body AutomationDryRunRequest true "The record to simulate against"
// @Success 200 {object} utils.APIResponse{data=models.AutomationDryRunReport} "Per-step report"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST when the id is not an unsigned integer (0 is not rejected here) or the body is not valid JSON; VALIDATION_ERROR when entity_type is missing or not lead/deal or entity_id is missing or 0 (error.details maps EntityType or EntityID to a message); INVALID_INPUT when entity_type differs from the rule's"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live rule with this id, or no live record with this entity_id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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

// DryRunDraft godoc
// @Summary Dry-run an unsaved automation rule
// @Description Simulate an unsaved rule body against one live record: the rule editor's current values, for a new rule or for unsaved edits of an existing one. Roles: admin only. The body goes through the same normalisation and validation as the create (400 INVALID_INPUT or INVALID_REFERENCE with error.details.fields), then is simulated like POST /automation/rules/{id}/dry-run. Nothing is saved, no transaction is started and nothing is written, sent or notified; a saved rule's version and steps are untouched. The report has draft true and rule_id and rule_version 0. enabled in the rule body is ignored.
// @Tags automation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param request body AutomationDraftDryRunRequest true "The unsaved rule and the record to simulate against"
// @Success 200 {object} utils.APIResponse{data=models.AutomationDryRunReport} "Per-step report of the draft"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError{details=AutomationRuleErrorDetails}} "VALIDATION_ERROR when entity_type is missing or not lead/deal or entity_id is missing or 0 (error.details maps EntityType or EntityID to a message; a missing rule object is not a binding error, it fails the rule checks instead); INVALID_INPUT - the rule body was refused (error.details.fields lists every refused field; error.message and error.code are those of the first); INVALID_REFERENCE - the same, when the first refusal is an unknown, deactivated or customer user_id; BAD_REQUEST (message Invalid request format) when the body is not valid JSON or a value has the wrong JSON type; INVALID_INPUT without details when entity_type differs from rule.entity_type"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no live record with this entity_id"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
// @Router /automation/rules/dry-run [post]
func (h *AutomationHandler) DryRunDraft(c *gin.Context) {
	logger := utils.LogHandlerStart(c, "AutomationHandler.DryRunDraft")
	var req AutomationDraftDryRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(err).SetType(gin.ErrorTypeBind)
		return
	}
	report, err := h.automationService.DryRunDraft(req.Rule.toModel(), req.EntityType, req.EntityID)
	if err != nil {
		h.respondError(c, logger, err)
		return
	}
	utils.LogHandlerResponse(logger, http.StatusOK, report)
	utils.RespondSuccess(c, http.StatusOK, report)
}

// ListRuns godoc
// @Summary List automation runs
// @Description One page of runs, without their step logs. Roles: admin sees every run; sales only runs whose lead or deal it currently owns (applied inside the query, so meta.total counts that set; ownership is read at request time, so it follows reassignments); support and customers get 403 from the route guard. Filters: status, rule_id, entity_type, entity_id (each validated, 400 when malformed). sort_by is limited to the listed columns (anything else is 400). An absent sort_by sorts by created_at descending and then ignores sort_order; with a sort_by, sort_order is asc or desc and anything else means desc. Ties are broken by id in the same direction. Pagination: page (1-based) overrides offset when greater than 0; limit defaults to 20 and is clamped to 100; the checks run before any query, sort first. Each run carries rule_name, the rule's current name (also for a deleted rule, then with rule_deleted true), and never the rule's trigger or steps; a sales user therefore sees the names of the rules that ran on its records.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param page query int false "Page number (1-based); overrides offset when greater than 0"
// @Param offset query int false "Number of items to skip" default(0)
// @Param limit query int false "Page size; non-positive values fall back to 20, larger values are clamped to 100" default(20)
// @Param status query string false "Only runs in this status" Enums(pending, running, waiting, completed, failed, cancelled)
// @Param rule_id query int false "Only runs of this rule (a positive integer)"
// @Param entity_type query string false "Only runs on this entity type" Enums(lead, deal)
// @Param entity_id query int false "Only runs on records with this id (a positive integer; combine with entity_type to name one record)"
// @Param sort_by query string false "Sort column; created_at when absent, anything outside the list is 400" Enums(id, created_at, updated_at, status, resume_at)
// @Param sort_order query string false "Sort direction, honoured only with sort_by; anything else means desc" Enums(asc, desc) default(desc)
// @Success 200 {object} utils.APIResponse{data=[]models.AutomationRun,meta=utils.APIMeta} "One page of runs; meta carries page, per_page, total and total_pages"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - sort_by is not an allowed column (message Invalid sort column), status or entity_type is unknown, or rule_id or entity_id is not a positive integer"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin or sales role required"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description One run with its step log in position order, with rule_name and rule_deleted as in the list and never the rule's trigger or steps. Roles: admin any run; sales only runs on a lead or deal it currently owns (any other run is 404, indistinguishable from a missing one); support and customers get 403 from the route guard.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Run ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRun} "The run with steps"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin or sales role required"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no run with this id, or (sales) a run outside the caller's records"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
// @Description Cancel a pending, waiting or running run with cancel_reason manual and return it with its step log. Roles: admin only (sales gets 403 from the route guard). A running run performs no further step: its next conditional advance affects zero rows. A completed, failed or cancelled run, or one that finishes between the read and the cancel, is 409.
// @Tags automation
// @Produce json
// @Security BearerAuth
// @Security ApiKeyAuth
// @Param id path int true "Run ID"
// @Success 200 {object} utils.APIResponse{data=models.AutomationRun} "The run, cancelled"
// @Failure 400 {object} utils.APIResponse{error=utils.APIError} "BAD_REQUEST - the id is not an unsigned integer (0 is not rejected here)"
// @Failure 401 {object} utils.APIResponse{error=utils.APIError} "UNAUTHORIZED - missing or malformed Authorization header, or invalid, expired or revoked credentials"
// @Failure 403 {object} utils.APIResponse{error=utils.APIError} "FORBIDDEN - admin role required (route guard)"
// @Failure 404 {object} utils.APIResponse{error=utils.APIError} "NOT_FOUND - no run with this id"
// @Failure 409 {object} utils.APIResponse{error=utils.APIError} "INVALID_STATUS_TRANSITION - the run is already finished"
// @Failure 429 {object} utils.APIResponse{error=utils.APIError} "TOO_MANY_REQUESTS - per-IP rate limit for authenticated traffic exceeded; error.details.retry_after is 60s"
// @Failure 500 {object} utils.APIResponse{error=utils.APIError} "INTERNAL_ERROR - unexpected failure; details are logged, never returned"
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
	var ruleErr *service.RuleValidationError
	switch {
	case errors.As(err, &ruleErr):
		// Every refused field goes into error.details.fields; the message and
		// the code are those of the first refusal, as before field errors.
		code := apperrors.CodeInvalidInput
		if service.IsReferenceRefusal(ruleErr) {
			code = apperrors.CodeInvalidReference
		}
		logger.WithError(utils.LogError(err)).Warn("Automation rule rejected")
		utils.RespondError(c, http.StatusBadRequest, code, err.Error(), AutomationRuleErrorDetails{Fields: ruleErr.Fields})
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
