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
type AutomationScheduleFilter struct {
	Status string `json:"status,omitempty"`
	Stage  string `json:"stage,omitempty"`
}

// AutomationTrigger is the decoded trigger configuration of a rule. From and
// To narrow a status or stage change (empty means any). HourUTC, Filters and
// StaleDays belong to the schedule trigger: one pass per UTC day, starting at
// the first worker tick at or after hour_utc, over the live records matching
// the filters whose updated_at is older than stale_days (0 means any age).
type AutomationTrigger struct {
	From      string                    `json:"from,omitempty"`
	To        string                    `json:"to,omitempty"`
	HourUTC   int                       `json:"hour_utc"`
	Filters   *AutomationScheduleFilter `json:"filters,omitempty"`
	StaleDays int                       `json:"stale_days,omitempty"`
}

// AutomationStepConfig is the decoded configuration of one step. Which
// fields apply depends on the step type; the service validates each type.
type AutomationStepConfig struct {
	// wait
	DurationMinutes int64 `json:"duration_minutes,omitempty"`

	// condition
	Field        string          `json:"field,omitempty"`
	Operator     string          `json:"operator,omitempty"`
	Value        json.RawMessage `json:"value,omitempty" swaggertype:"object"`
	Currency     string          `json:"currency,omitempty"`
	Else         string          `json:"else,omitempty"`
	ElsePosition int             `json:"else_position,omitempty"`

	// create_task assignee, send_email and notify recipient: "owner" (the
	// record's owner at execution time) or "user" with UserID. assign_owner
	// always names UserID.
	Recipient string `json:"recipient,omitempty"`
	UserID    uint   `json:"user_id,omitempty"`

	// create_task and notify
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Priority    string `json:"priority,omitempty"`
	DueInDays   int    `json:"due_in_days,omitempty"`

	// send_email and notify
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
}

// AutomationRule is one automation rule with its ordered steps.
//
// version is incremented by every update, and every run records the version
// it was started from. The three schedule_* columns are the persisted cursor
// of the schedule trigger: the UTC day of the pass in progress, the last
// record id handed out in that pass, and the day whose pass finished.
type AutomationRule struct {
	BaseModel
	Name          string `gorm:"not null;type:varchar(200)" json:"name"`
	Description   string `gorm:"type:text" json:"description"`
	EntityType    string `gorm:"not null;type:varchar(20)" json:"entity_type"`
	TriggerType   string `gorm:"not null;type:varchar(40)" json:"trigger_type"`
	TriggerConfig string `gorm:"column:trigger_config;type:text" json:"-"`
	Enabled       bool   `gorm:"not null;default:false" json:"enabled"`
	Version       int    `gorm:"not null;default:1" json:"version"`

	SchedulePassOn      *string `gorm:"type:varchar(10)" json:"schedule_pass_on,omitempty"`
	ScheduleCursor      uint    `gorm:"not null;default:0" json:"schedule_cursor"`
	ScheduleCompletedOn *string `gorm:"type:varchar(10)" json:"schedule_completed_on,omitempty"`

	CreatedByID *uint `json:"created_by_id,omitempty"`
	CreatedBy   *User `gorm:"foreignKey:CreatedByID" json:"-"`

	Trigger AutomationTrigger `gorm:"-" json:"trigger"`
	Steps   []AutomationStep  `gorm:"foreignKey:RuleID" json:"steps"`
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
type AutomationStep struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	RuleID    uint      `gorm:"not null;index" json:"rule_id"`
	Position  int       `gorm:"not null" json:"position"`
	StepType  string    `gorm:"not null;type:varchar(20)" json:"step_type"`
	ConfigRaw string    `gorm:"column:config;type:text" json:"-"`
	CreatedAt time.Time `json:"created_at"`

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
type AutomationRun struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	RuleID      uint            `gorm:"not null;index" json:"rule_id"`
	Rule        *AutomationRule `gorm:"foreignKey:RuleID" json:"rule,omitempty"`
	RuleVersion int             `gorm:"not null" json:"rule_version"`
	EntityType  string          `gorm:"not null;type:varchar(20);index:idx_automation_runs_entity" json:"entity_type"`
	EntityID    uint            `gorm:"not null;index:idx_automation_runs_entity" json:"entity_id"`
	EventID     *uint           `json:"event_id,omitempty"`
	DedupeKey   string          `gorm:"not null;type:varchar(100);uniqueIndex" json:"dedupe_key"`

	Status       string     `gorm:"not null;type:varchar(20);default:'pending';index" json:"status"`
	NextPosition int        `gorm:"not null;default:1" json:"next_position"`
	Attempts     int        `gorm:"not null;default:0" json:"attempts"`
	ResumeAt     *time.Time `gorm:"index" json:"resume_at,omitempty"`
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CancelReason string     `gorm:"type:varchar(30)" json:"cancel_reason,omitempty"`
	ErrorCode    string     `gorm:"type:varchar(40)" json:"error_code,omitempty"`

	Steps []AutomationRunStep `gorm:"foreignKey:RunID" json:"steps,omitempty"`
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
type AutomationRunStep struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	RunID      uint      `gorm:"not null;uniqueIndex:idx_automation_run_steps_run_position" json:"run_id"`
	Position   int       `gorm:"not null;uniqueIndex:idx_automation_run_steps_run_position" json:"position"`
	StepType   string    `gorm:"not null;type:varchar(20)" json:"step_type"`
	Outcome    string    `gorm:"not null;type:varchar(20)" json:"outcome"`
	ErrorCode  string    `gorm:"type:varchar(40)" json:"error_code,omitempty"`
	DetailRaw  string    `gorm:"column:detail;type:text" json:"-"`
	ExecutedAt time.Time `gorm:"not null" json:"executed_at"`

	Details map[string]interface{} `gorm:"-" json:"details"`
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
type AutomationDryRunStep struct {
	Position    int                    `json:"position"`
	StepType    string                 `json:"step_type"`
	Outcome     string                 `json:"outcome"`
	ErrorCode   string                 `json:"error_code,omitempty"`
	SimulatedAt time.Time              `json:"simulated_at"`
	Details     map[string]interface{} `json:"details"`
}

// AutomationDryRunReport is the answer of POST /automation/rules/:id/dry-run.
type AutomationDryRunReport struct {
	RuleID         uint                   `json:"rule_id"`
	RuleVersion    int                    `json:"rule_version"`
	EntityType     string                 `json:"entity_type"`
	EntityID       uint                   `json:"entity_id"`
	SimulatedStart time.Time              `json:"simulated_start"`
	SimulatedEnd   time.Time              `json:"simulated_end"`
	Steps          []AutomationDryRunStep `json:"steps"`
}
