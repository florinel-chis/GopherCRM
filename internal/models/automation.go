package models

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Follow-up automation (issue #164): admin-defined rules that start on a lead
// or deal event, or on a daily UTC schedule, and run ordered steps. Every
// write to a lead or a deal records an outbox event in its own transaction;
// a worker turns events into persisted runs and executes each step behind a
// locking read of the record and a conditional advance of the run row.
//
// None of these tables holds personal data: events carry ids, statuses and
// origins; run logs carry ids, outcomes and error codes. The only free text
// is what an admin typed into a rule (step titles, bodies) and the rendered
// title and body of an in-app notification addressed to a staff user.

// Entity types a rule can target.
const (
	AutomationEntityLead = "lead"
	AutomationEntityDeal = "deal"
)

// Event types. The trigger type of an event rule is one of these; the
// schedule trigger is AutomationTriggerSchedule.
const (
	AutomationEventLeadCreated       = "lead_created"
	AutomationEventLeadUpdated       = "lead_updated"
	AutomationEventLeadStatusChanged = "lead_status_changed"
	AutomationEventDealCreated       = "deal_created"
	AutomationEventDealUpdated       = "deal_updated"
	AutomationEventDealStageChanged  = "deal_stage_changed"
	AutomationTriggerSchedule        = "schedule"
)

// Event origins. Automation-originated writes (an assign_owner step) record
// their event with AutomationOriginAutomation, which the dispatcher claims
// but never turns into a run: that is what keeps two rules from triggering
// each other in a loop.
const (
	AutomationOriginUser       = "user"
	AutomationOriginForm       = "form"
	AutomationOriginBulk       = "bulk"
	AutomationOriginConversion = "conversion"
	AutomationOriginAutomation = "automation"
)

// Step types.
const (
	AutomationStepWait        = "wait"
	AutomationStepCondition   = "condition"
	AutomationStepCreateTask  = "create_task"
	AutomationStepAssignOwner = "assign_owner"
	AutomationStepSendEmail   = "send_email"
	AutomationStepNotify      = "notify"
)

// Run statuses.
const (
	AutomationRunPending   = "pending"
	AutomationRunRunning   = "running"
	AutomationRunWaiting   = "waiting"
	AutomationRunCompleted = "completed"
	AutomationRunFailed    = "failed"
	AutomationRunCancelled = "cancelled"
)

// Cancel reasons.
const (
	AutomationCancelRuleChanged   = "rule_changed"
	AutomationCancelRuleDisabled  = "rule_disabled"
	AutomationCancelRuleDeleted   = "rule_deleted"
	AutomationCancelManual        = "manual"
	AutomationCancelRecordDeleted = "record_deleted"
)

// Run-step outcomes and error codes.
const (
	AutomationOutcomeSucceeded = "succeeded"
	AutomationOutcomeSkipped   = "skipped"
	AutomationOutcomeFailed    = "failed"
	AutomationOutcomeSending   = "sending"
	AutomationOutcomeWaiting   = "waiting"

	AutomationErrorRecipientUnavailable = "recipient_unavailable"
	AutomationErrorMailFailed           = "mail_failed"
	AutomationErrorEmailUnconfirmed     = "email_unconfirmed"
	AutomationErrorStepFailed           = "step_failed"
)

// Recipient kinds of a task assignee, an email recipient, a notification
// recipient: the record's current owner, or one named staff user.
const (
	AutomationRecipientOwner = "owner"
	AutomationRecipientUser  = "user"
)

// Condition branches when the condition is false.
const (
	AutomationElseStop = "stop"
	AutomationElseJump = "jump"
)

// Limits a rule is validated against.
const (
	AutomationMaxSteps           = 20
	AutomationMaxWaitMinutes     = 90 * 24 * 60
	AutomationRuleNameMaxLength  = 200
	AutomationRuleDescMaxBytes   = 65535
	AutomationStepTextMaxLength  = 255
	AutomationStepBodyMaxBytes   = 65535
	AutomationScheduleBatchSize  = 500
	AutomationDedupeKeyMaxLength = 100
)

// AutomationScheduleFilter narrows the records a schedule rule visits on each
// pass. An empty field means no filter on it.
//
// @Description Narrows the records a schedule rule visits on each daily pass; an empty field is no filter.
type AutomationScheduleFilter struct {
	// Lead rules only: visit only leads in this status. Omitted or empty means any status; set on a deal rule it is refused (invalid_value).
	Status string `json:"status,omitempty" enums:"new,contacted,qualified,unqualified,converted" example:"contacted"`
	// Deal rules only: visit only deals in this stage. Omitted or empty means any stage; set on a lead rule it is refused (invalid_value).
	Stage string `json:"stage,omitempty" enums:"qualification,proposal,negotiation,won,lost" example:"proposal"`
}

// AutomationTrigger is the decoded trigger configuration of a rule. From and
// To narrow a status or stage change (empty means any). HourUTC, Filters and
// StaleDays belong to the schedule trigger: one pass per UTC day, starting at
// the first worker tick at or after hour_utc, over the live records matching
// the filters whose updated_at is older than stale_days (0 means any age).
//
// @Description Trigger configuration of an automation rule. from and to apply to status or stage change triggers; hour_utc, filters and stale_days to the schedule trigger.
type AutomationTrigger struct {
	// Only for lead_status_changed and deal_stage_changed: start only when the previous value was this lead status (lead rules) or deal stage (deal rules). Omitted or empty means any; on any other trigger type it is refused (not_applicable).
	From string `json:"from,omitempty" enums:"new,contacted,qualified,unqualified,converted,qualification,proposal,negotiation,won,lost" example:"new"`
	// Only for lead_status_changed and deal_stage_changed: start only when the new value is this lead status (lead rules) or deal stage (deal rules). Omitted or empty means any; on any other trigger type it is refused (not_applicable).
	To string `json:"to,omitempty" enums:"new,contacted,qualified,unqualified,converted,qualification,proposal,negotiation,won,lost" example:"qualified"`
	// Schedule trigger only: the UTC hour (0-23) from which the daily pass may start; it starts at the first worker tick at or after that hour. Always present in responses (0 for event triggers); a non-zero value on an event trigger is refused (not_applicable).
	HourUTC int `json:"hour_utc" minimum:"0" maximum:"23" example:"8"`
	// Schedule trigger only: narrows the records each pass visits. Refused (not_applicable) on an event trigger.
	Filters *AutomationScheduleFilter `json:"filters,omitempty"`
	// Schedule trigger only: visit only records whose updated_at is more than this many days old; 0 or omitted means any age. Must not be negative; refused (not_applicable) on an event trigger.
	StaleDays int `json:"stale_days,omitempty" minimum:"0" example:"14"`
}

// AutomationStepConfig is the decoded configuration of one step. Which
// fields apply depends on the step type; the service validates each type.
//
// @Description Configuration of one automation step. Which fields apply depends on step_type: wait (duration_minutes), condition (field, operator, value, currency, else, else_position), create_task (title, description, priority, due_in_days, recipient, user_id), assign_owner (user_id), send_email (subject, body, recipient, user_id), notify (title, body, recipient, user_id).
type AutomationStepConfig struct {
	// wait: how long the run pauses, in minutes, 1 to 129600 (90 days). The waits of one rule may add up to at most 90 days.
	DurationMinutes int64 `json:"duration_minutes,omitempty" minimum:"1" maximum:"129600" example:"1440"`

	// condition: the record field compared. Lead rules: status, source, classification, company (compared as strings) and owner_id (a number). Deal rules: stage, source, currency (strings), amount_cents (money, needs currency) and probability, owner_id (numbers). Names, addresses and notes are never comparable.
	Field string `json:"field,omitempty" enums:"status,source,classification,company,owner_id,stage,currency,amount_cents,probability" example:"status"`
	// condition: the comparison. String fields take eq, neq, in, not_in, contains (case-insensitive substring), is_empty and is_not_empty; number and money fields take eq, neq, gt, gte, lt and lte.
	Operator string `json:"operator,omitempty" enums:"eq,neq,in,not_in,contains,is_empty,is_not_empty,gt,gte,lt,lte" example:"eq"`
	// condition: the value compared against, as raw JSON whose type depends on the operator: a string for eq/neq/contains on a string field, a non-empty array of strings for in/not_in, a number for every operator on a number or money field (amount_cents in minor units), and no value (omitted or null) for is_empty/is_not_empty. The schema therefore names no type; an object is always refused.
	Value json.RawMessage `json:"value,omitempty"`
	// condition on amount_cents only: the ISO currency (three upper-case letters) the amount is in. Required for amount_cents and refused on any other field; a deal in another currency makes the condition false, amounts in different currencies are never compared.
	Currency string `json:"currency,omitempty" example:"EUR"`
	// condition: what happens when the condition is false. stop ends the run; jump continues at else_position. Empty is stored as stop.
	Else string `json:"else,omitempty" enums:"stop,jump" example:"jump"`
	// condition with else jump only: the 1-based position to continue at, which must come after the condition step and be at most the step count. Refused (not_applicable) with else stop.
	ElsePosition int `json:"else_position,omitempty" minimum:"2" maximum:"20" example:"4"`

	// create_task, send_email, notify: who receives the task, the email or the notification. owner is the record's owner at execution time; user is the staff user named by user_id. Empty is stored as owner. assign_owner always stores user.
	Recipient string `json:"recipient,omitempty" enums:"owner,user" example:"owner"`
	// The staff user (active, not a customer) named by recipient user, and the new owner of an assign_owner step, where it is required. Refused (not_applicable) with recipient owner. An unknown, deactivated or customer user is refused with unknown_reference (400 INVALID_REFERENCE when it is the first refusal).
	UserID uint `json:"user_id,omitempty" example:"7"`

	// create_task and notify (required): the title template, at most 255 characters and no line break. Placeholders are written as {{ name }}. A notify title may use lead.id, lead.first_name, lead.last_name, lead.email, lead.phone, lead.company, lead.status, lead.source (lead rules) or deal.id, deal.title, deal.stage, deal.amount, deal.currency, deal.source (deal rules), plus owner.first_name, owner.last_name, owner.email; a task title only lead.id, lead.status, lead.source (lead rules) or the deal placeholders (deal rules).
	Title string `json:"title,omitempty" maxLength:"255" example:"Follow up with lead {{ lead.id }}"`
	// create_task only: the task description template, at most 65535 bytes, with the task placeholders of title.
	Description string `json:"description,omitempty" example:"Lead status is {{ lead.status }}."`
	// create_task only: the new task's priority; empty means medium.
	Priority string `json:"priority,omitempty" enums:"low,medium,high" example:"high"`
	// create_task only: the new task is due this many days after the step runs, 0 to 365.
	DueInDays int `json:"due_in_days,omitempty" minimum:"0" maximum:"365" example:"2"`

	// send_email only (required): the subject template, at most 255 characters and no line break, with the notify placeholders.
	Subject string `json:"subject,omitempty" maxLength:"255" example:"Lead {{ lead.first_name }} needs attention"`
	// send_email (required) and notify (optional): the body template, at most 65535 bytes, with the notify placeholders. An email is sent as plain text to the recipient's address.
	Body string `json:"body,omitempty" example:"Please contact {{ lead.first_name }} {{ lead.last_name }} today."`
}

// AutomationRule is one automation rule with its ordered steps.
//
// version is incremented by every update, and every run records the version
// it was started from. The three schedule_* columns are the persisted cursor
// of the schedule trigger: the UTC day of the pass in progress, the last
// record id handed out in that pass, and the day whose pass finished.
//
// @Description An admin-defined follow-up rule: a lead or deal trigger (or a daily UTC schedule) and up to 20 ordered steps.
type AutomationRule struct {
	BaseModel
	// Rule name, trimmed, 1 to 200 characters.
	Name string `gorm:"not null;type:varchar(200)" json:"name" maxLength:"200" example:"Welcome new web leads"`
	// Free-text description for admins, at most 65535 bytes; an empty string when none.
	Description string `gorm:"type:text" json:"description" example:"Creates a call-back task for every lead from the website."`
	// Kind of record the rule runs on.
	EntityType string `gorm:"not null;type:varchar(20)" json:"entity_type" enums:"lead,deal" example:"lead"`
	// What starts a run. Lead rules take lead_created, lead_updated, lead_status_changed or schedule; deal rules take deal_created, deal_updated, deal_stage_changed or schedule. Writes made by an automation step never start a run.
	TriggerType   string `gorm:"not null;type:varchar(40)" json:"trigger_type" enums:"lead_created,lead_updated,lead_status_changed,deal_created,deal_updated,deal_stage_changed,schedule" example:"lead_created"`
	TriggerConfig string `gorm:"column:trigger_config;type:text" json:"-"`
	// Whether the rule starts new runs. A new rule is disabled unless the create body sends true; a PUT without enabled keeps the stored value.
	Enabled bool `gorm:"not null;default:false" json:"enabled" example:"true"`
	// Starts at 1 and is incremented by every PUT /automation/rules/{id} (enabling or disabling does not change it); every run records the version it started from. Read-only.
	Version int `gorm:"not null;default:1" json:"version" example:"3" readonly:"true"`

	// Schedule rules: the UTC day (YYYY-MM-DD) of the pass in progress; omitted when none. Server-maintained, read-only.
	SchedulePassOn *string `gorm:"type:varchar(10)" json:"schedule_pass_on,omitempty" example:"2026-10-31" readonly:"true"`
	// Schedule rules: the last record id handed out in the pass in progress; 0 otherwise. Server-maintained, read-only.
	ScheduleCursor uint `gorm:"not null;default:0" json:"schedule_cursor" example:"0" readonly:"true"`
	// Schedule rules: the UTC day (YYYY-MM-DD) whose pass last finished; omitted when none. Server-maintained, read-only.
	ScheduleCompletedOn *string `gorm:"type:varchar(10)" json:"schedule_completed_on,omitempty" example:"2026-10-30" readonly:"true"`

	// ID of the admin who created the rule; omitted when unknown. Read-only.
	CreatedByID *uint `json:"created_by_id,omitempty" example:"1" readonly:"true"`
	CreatedBy   *User `gorm:"foreignKey:CreatedByID" json:"-"`

	// The trigger configuration; which fields apply depends on trigger_type.
	Trigger AutomationTrigger `gorm:"-" json:"trigger"`
	// The ordered steps, by position (1 to 20 of them). Replaced wholesale, with new ids, by every PUT.
	Steps []AutomationStep `gorm:"foreignKey:RuleID" json:"steps"`
}

func (AutomationRule) TableName() string { return "automation_rules" }

// BeforeSave serializes the decoded trigger into its TEXT column.
func (r *AutomationRule) BeforeSave(tx *gorm.DB) error {
	encoded, err := json.Marshal(r.Trigger)
	if err != nil {
		return fmt.Errorf("automation rule trigger: %w", err)
	}
	r.TriggerConfig = string(encoded)
	return nil
}

// AfterFind restores the decoded trigger from its TEXT column. An empty or
// unreadable column decodes to the zero trigger.
func (r *AutomationRule) AfterFind(tx *gorm.DB) error {
	r.Trigger = AutomationTrigger{}
	if r.TriggerConfig != "" {
		_ = json.Unmarshal([]byte(r.TriggerConfig), &r.Trigger)
	}
	return nil
}

// AutomationStep is one ordered step of a rule. Steps are replaced wholesale
// by every rule update, so they carry no soft delete.
//
// @Description One ordered step of an automation rule.
type AutomationStep struct {
	// Unique identifier of the step row. Read-only; a PUT of the rule replaces the steps with new ids.
	ID uint `gorm:"primarykey" json:"id" example:"301" readonly:"true"`
	// ID of the rule the step belongs to. Read-only.
	RuleID uint `gorm:"not null;index" json:"rule_id" example:"12" readonly:"true"`
	// 1-based position of the step in the rule, the order of the steps array in the request. Read-only.
	Position int `gorm:"not null" json:"position" example:"1" readonly:"true"`
	// What the step does: wait pauses the run, condition tests the record and stops or jumps when false, create_task creates a task, assign_owner changes the record's owner, send_email emails a staff user, notify creates an in-app notification.
	StepType  string `gorm:"not null;type:varchar(20)" json:"step_type" enums:"wait,condition,create_task,assign_owner,send_email,notify" example:"create_task"`
	ConfigRaw string `gorm:"column:config;type:text" json:"-"`
	// When the step row was written (RFC 3339). Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-10-30T08:00:00Z" readonly:"true"`

	// The step configuration as stored, with defaults filled in (recipient owner, else stop); which fields apply depends on step_type.
	Config AutomationStepConfig `gorm:"-" json:"config"`
}

func (AutomationStep) TableName() string { return "automation_steps" }

func (s *AutomationStep) BeforeSave(tx *gorm.DB) error {
	encoded, err := json.Marshal(s.Config)
	if err != nil {
		return fmt.Errorf("automation step config: %w", err)
	}
	s.ConfigRaw = string(encoded)
	return nil
}

func (s *AutomationStep) AfterFind(tx *gorm.DB) error {
	s.Config = AutomationStepConfig{}
	if s.ConfigRaw != "" {
		_ = json.Unmarshal([]byte(s.ConfigRaw), &s.Config)
	}
	return nil
}

// AutomationEvent is the outbox row a lead or deal write records in its own
// transaction. It carries ids, the event type, the previous and new value of
// a changed status or stage, and the origin: never a name, an address or a
// note. processed_at is set by the dispatcher's conditional claim.
type AutomationEvent struct {
	ID          uint       `gorm:"primarykey" json:"id"`
	EntityType  string     `gorm:"not null;type:varchar(20);index:idx_automation_events_entity" json:"entity_type"`
	EntityID    uint       `gorm:"not null;index:idx_automation_events_entity" json:"entity_id"`
	EventType   string     `gorm:"not null;type:varchar(40)" json:"event_type"`
	FromValue   string     `gorm:"type:varchar(50)" json:"from_value"`
	ToValue     string     `gorm:"type:varchar(50)" json:"to_value"`
	Origin      string     `gorm:"not null;type:varchar(20)" json:"origin"`
	ActorID     *uint      `json:"actor_id,omitempty"`
	CreatedAt   time.Time  `gorm:"not null" json:"created_at"`
	ProcessedAt *time.Time `gorm:"index" json:"processed_at,omitempty"`
}

func (AutomationEvent) TableName() string { return "automation_events" }

// AutomationRun is one execution of a rule against one record. dedupe_key is
// unique: evt:<event_id>:rule:<rule_id> for event runs and
// sch:<rule_id>:<YYYY-MM-DD>:<entity_id> for schedule runs, so a repeated
// dispatch or a repeated schedule tick cannot start a second run.
//
// @Description One execution of an automation rule against one lead or deal. It never carries the rule's trigger or steps, only the rule's name.
type AutomationRun struct {
	// Unique identifier of the run. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"88" readonly:"true"`
	// When the run was started (RFC 3339). Read-only.
	CreatedAt time.Time `json:"created_at" example:"2026-10-30T08:00:00Z" readonly:"true"`
	// When the run row was last written (RFC 3339). Read-only.
	UpdatedAt time.Time `json:"updated_at" example:"2026-10-30T08:00:05Z" readonly:"true"`

	// ID of the rule the run executes. Read-only.
	RuleID uint `gorm:"not null;index" json:"rule_id" example:"12" readonly:"true"`
	// Rule is never serialised: runs are readable by sales users, rules only
	// by admins, and a rule carries its trigger and step texts. The run API
	// exposes the rule's name alone, through RuleName and RuleDeleted.
	Rule *AutomationRule `gorm:"foreignKey:RuleID" json:"-"`
	// The rule's current name (also for a soft-deleted rule; empty when the rule row is gone), filled in by the run reads; it has no column. Read-only.
	RuleName string `gorm:"-" json:"rule_name" example:"Welcome new web leads" readonly:"true"`
	// True when the rule has been deleted since (or is gone). Read-only.
	RuleDeleted bool `gorm:"-" json:"rule_deleted" example:"false" readonly:"true"`
	// The rule version the run was started from. Read-only.
	RuleVersion int `gorm:"not null" json:"rule_version" example:"3" readonly:"true"`
	// Kind of record the run acts on. Read-only.
	EntityType string `gorm:"not null;type:varchar(20);index:idx_automation_runs_entity" json:"entity_type" enums:"lead,deal" example:"lead" readonly:"true"`
	// ID of the lead or deal the run acts on. Read-only.
	EntityID uint `gorm:"not null;index:idx_automation_runs_entity" json:"entity_id" example:"12" readonly:"true"`
	// ID of the lead or deal event that started the run; omitted for a schedule run. Read-only.
	EventID *uint `json:"event_id,omitempty" example:"1042" readonly:"true"`
	// Unique key that stops a run being started twice: evt:<event_id>:rule:<rule_id> for an event run, sch:<rule_id>:<YYYY-MM-DD>:<entity_id> for a schedule run. Read-only.
	DedupeKey string `gorm:"not null;type:varchar(100);uniqueIndex" json:"dedupe_key" example:"evt:1042:rule:12" readonly:"true"`

	// Lifecycle status: pending (queued), running (an executor holds it), waiting (paused by a wait step until resume_at), and the final completed, failed and cancelled. Read-only.
	Status string `gorm:"not null;type:varchar(20);default:'pending';index" json:"status" enums:"pending,running,waiting,completed,failed,cancelled" example:"waiting" readonly:"true"`
	// 1-based position of the next step to execute. Read-only.
	NextPosition int `gorm:"not null;default:1" json:"next_position" example:"2" readonly:"true"`
	// Step attempts that failed or were released so far (never reset). An attempt that fails with attempts already at 2 fails the run with error_code step_failed. Read-only.
	Attempts int `gorm:"not null;default:0" json:"attempts" example:"0" readonly:"true"`
	// When a waiting run resumes (RFC 3339); omitted when not set. Read-only.
	ResumeAt *time.Time `gorm:"index" json:"resume_at,omitempty" example:"2026-10-31T08:00:00Z" readonly:"true"`
	// When an executor last claimed the run (RFC 3339); omitted when not set. Read-only.
	ClaimedAt *time.Time `json:"claimed_at,omitempty" example:"2026-10-30T08:00:01Z" readonly:"true"`
	// When the run reached completed, failed or cancelled (RFC 3339); omitted while it is active. Read-only.
	FinishedAt *time.Time `json:"finished_at,omitempty" example:"2026-10-30T08:00:05Z" readonly:"true"`
	// Why a cancelled run was cancelled: the rule was changed, disabled or deleted, an admin cancelled it (manual), or the record was deleted. Omitted unless the run is cancelled. Read-only.
	CancelReason string `gorm:"type:varchar(30)" json:"cancel_reason,omitempty" enums:"rule_changed,rule_disabled,rule_deleted,manual,record_deleted" example:"manual" readonly:"true"`
	// Why a failed run failed: step_failed (a step kept failing) or email_unconfirmed (an email may or may not have been sent and is never retried). Omitted unless the run failed. Read-only.
	ErrorCode string `gorm:"type:varchar(40)" json:"error_code,omitempty" enums:"step_failed,email_unconfirmed" example:"step_failed" readonly:"true"`

	// The step log, by position. Only returned by GET /automation/runs/{id} and POST /automation/runs/{id}/cancel; omitted from the list. Read-only.
	Steps []AutomationRunStep `gorm:"foreignKey:RunID" json:"steps,omitempty" readonly:"true"`
}

func (AutomationRun) TableName() string { return "automation_runs" }

// IsFinished reports whether the run can no longer be cancelled or resumed.
func (r *AutomationRun) IsFinished() bool {
	switch r.Status {
	case AutomationRunCompleted, AutomationRunFailed, AutomationRunCancelled:
		return true
	}
	return false
}

// AutomationRunStep is the log row of one executed step. (run_id, position)
// is unique, which is what stops a second executor from recording a second
// "sending" row for an email step. details holds ids and statuses only.
//
// @Description The log row of one executed step of a run. It holds ids, outcomes and error codes, never personal data.
type AutomationRunStep struct {
	// Unique identifier of the log row. Read-only.
	ID uint `gorm:"primarykey" json:"id" example:"501" readonly:"true"`
	// ID of the run. Read-only.
	RunID uint `gorm:"not null;uniqueIndex:idx_automation_run_steps_run_position" json:"run_id" example:"88" readonly:"true"`
	// 1-based position of the executed step in the rule version the run started from. Read-only.
	Position int `gorm:"not null;uniqueIndex:idx_automation_run_steps_run_position" json:"position" example:"1" readonly:"true"`
	// Type of the executed step. Read-only.
	StepType string `gorm:"not null;type:varchar(20)" json:"step_type" enums:"wait,condition,create_task,assign_owner,send_email,notify" example:"create_task" readonly:"true"`
	// Result: succeeded, skipped (the recipient was unavailable), failed (the email could not be delivered, or was left unconfirmed), sending (an email is being handed to the mail relay) or waiting (a wait step paused the run). Read-only.
	Outcome string `gorm:"not null;type:varchar(20)" json:"outcome" enums:"succeeded,skipped,failed,sending,waiting" example:"succeeded" readonly:"true"`
	// Why the step was skipped or failed: recipient_unavailable (no active staff user to receive or own it), mail_failed (delivery failed; the run continues) or email_unconfirmed. Omitted otherwise. Read-only.
	ErrorCode string `gorm:"type:varchar(40)" json:"error_code,omitempty" enums:"recipient_unavailable,mail_failed,email_unconfirmed" example:"recipient_unavailable" readonly:"true"`
	DetailRaw string `gorm:"column:detail;type:text" json:"-"`
	// When the step was executed (RFC 3339). Read-only.
	ExecutedAt time.Time `gorm:"not null" json:"executed_at" example:"2026-10-30T08:00:02Z" readonly:"true"`

	// Ids and statuses the step touched, never names or addresses. Keys by step type: wait resume_at; condition result, branch (next, jump or stop) and next_position for a jump; create_task assignee_id, due_date, lead_id or customer_id and task_id; assign_owner from_owner_id and to_owner_id; send_email and notify recipient_id, plus notification_id for notify. Always an object, possibly empty. Read-only.
	Details map[string]interface{} `gorm:"-" json:"details" readonly:"true"`
}

func (AutomationRunStep) TableName() string { return "automation_run_steps" }

func (s *AutomationRunStep) BeforeSave(tx *gorm.DB) error {
	if s.Details == nil {
		s.DetailRaw = "{}"
		return nil
	}
	encoded, err := json.Marshal(s.Details)
	if err != nil {
		return fmt.Errorf("automation run step detail: %w", err)
	}
	s.DetailRaw = string(encoded)
	return nil
}

func (s *AutomationRunStep) AfterFind(tx *gorm.DB) error {
	s.Details = map[string]interface{}{}
	if s.DetailRaw != "" {
		_ = json.Unmarshal([]byte(s.DetailRaw), &s.Details)
	}
	return nil
}

// AutomationDryRunStep is one line of a dry-run report: what the step would
// do against the chosen record, at the simulated time, with the ids it would
// touch. Nothing in it is written anywhere.
//
// @Description One line of a dry-run report: what a step would do, at the simulated time. Nothing in it was written.
type AutomationDryRunStep struct {
	// 1-based position of the step. Steps skipped by a condition branch do not appear.
	Position int `json:"position" example:"1"`
	// Type of the step.
	StepType string `json:"step_type" enums:"wait,condition,create_task,assign_owner,send_email,notify" example:"create_task"`
	// Would-be result: succeeded, waiting (a wait step; the simulated clock moves on), or skipped when no active staff recipient or owner is available.
	Outcome string `json:"outcome" enums:"succeeded,waiting,skipped" example:"succeeded"`
	// recipient_unavailable when the step would be skipped; omitted otherwise.
	ErrorCode string `json:"error_code,omitempty" enums:"recipient_unavailable" example:"recipient_unavailable"`
	// The simulated time the step would run at (RFC 3339, UTC): the dry run's start plus every earlier wait.
	SimulatedAt time.Time `json:"simulated_at" example:"2026-10-31T08:00:00Z"`
	// What the step would touch, by step type: wait resume_at; condition result, branch (next, jump or stop) and next_position for a jump; create_task assignee_id, due_date and lead_id or customer_id; assign_owner from_owner_id and to_owner_id; send_email and notify recipient_id. Always an object, possibly empty.
	Details map[string]interface{} `json:"details"`
}

// AutomationDryRunReport is the answer of POST /automation/rules/:id/dry-run
// and of POST /automation/rules/dry-run. A draft report (the second route)
// simulates an unsaved rule body: Draft is true and RuleID and RuleVersion
// are 0.
//
// @Description The simulated outcome of an automation rule (or an unsaved draft) against one record. Nothing is written, sent or notified.
type AutomationDryRunReport struct {
	// ID of the simulated rule; 0 for a draft.
	RuleID uint `json:"rule_id" example:"12"`
	// Version of the simulated rule; 0 for a draft.
	RuleVersion int `json:"rule_version" example:"3"`
	// True when an unsaved rule body was simulated (POST /automation/rules/dry-run).
	Draft bool `json:"draft" example:"false"`
	// Kind of the record simulated against.
	EntityType string `json:"entity_type" enums:"lead,deal" example:"lead"`
	// ID of the record simulated against.
	EntityID uint `json:"entity_id" example:"12"`
	// The simulated start (RFC 3339, UTC): the time of the request.
	SimulatedStart time.Time `json:"simulated_start" example:"2026-10-31T08:00:00Z"`
	// The simulated end (RFC 3339, UTC): the start plus every wait that was reached.
	SimulatedEnd time.Time `json:"simulated_end" example:"2026-11-02T08:00:00Z"`
	// The steps that would run, in execution order; always an array, possibly empty.
	Steps []AutomationDryRunStep `json:"steps"`
}
