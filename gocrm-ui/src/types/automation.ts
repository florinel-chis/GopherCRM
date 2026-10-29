// Follow-up automation (issue #164). Mirrors internal/models/automation.go
// and internal/models/notification.go; every field the screens read exists
// in the Go JSON.

export const AUTOMATION_ENTITY_TYPES = ['lead', 'deal'] as const;
export type AutomationEntityType = (typeof AUTOMATION_ENTITY_TYPES)[number];

export type AutomationLeadTrigger =
  | 'lead_created'
  | 'lead_updated'
  | 'lead_status_changed'
  | 'schedule';
export type AutomationDealTrigger =
  | 'deal_created'
  | 'deal_updated'
  | 'deal_stage_changed'
  | 'schedule';
export type AutomationTriggerType = AutomationLeadTrigger | AutomationDealTrigger;

// automation_service.go:35-44
export const AUTOMATION_TRIGGERS: Readonly<
  Record<AutomationEntityType, readonly AutomationTriggerType[]>
> = {
  lead: ['lead_created', 'lead_updated', 'lead_status_changed', 'schedule'],
  deal: ['deal_created', 'deal_updated', 'deal_stage_changed', 'schedule'],
};
export const isChangeTrigger = (t: AutomationTriggerType): boolean =>
  t === 'lead_status_changed' || t === 'deal_stage_changed';

// Lead statuses the API accepts in triggers, filters and conditions
// (bulk_status_service.go:89-97). NOT the frontend Lead['status'] union,
// which also carries 'lost'.
export const AUTOMATION_LEAD_STATUSES = [
  'new',
  'contacted',
  'qualified',
  'unqualified',
  'converted',
] as const;
// Deal stages: reuse DEAL_STAGE_VALUES from '@/types'.

export const AUTOMATION_STEP_TYPES = [
  'wait',
  'condition',
  'create_task',
  'assign_owner',
  'send_email',
  'notify',
] as const;
export type AutomationStepType = (typeof AUTOMATION_STEP_TYPES)[number];

export const AUTOMATION_RUN_STATUSES = [
  'pending',
  'running',
  'waiting',
  'completed',
  'failed',
  'cancelled',
] as const;
export type AutomationRunStatus = (typeof AUTOMATION_RUN_STATUSES)[number];
export const isActiveRunStatus = (s: AutomationRunStatus): boolean =>
  s === 'pending' || s === 'running' || s === 'waiting';

export type AutomationCancelReason =
  | 'rule_changed'
  | 'rule_disabled'
  | 'rule_deleted'
  | 'manual'
  | 'record_deleted';
export type AutomationOutcome = 'succeeded' | 'skipped' | 'failed' | 'sending' | 'waiting';
export type AutomationErrorCode =
  | 'recipient_unavailable'
  | 'mail_failed'
  | 'email_unconfirmed'
  | 'step_failed';
export type AutomationRecipient = 'owner' | 'user';
export type AutomationElse = 'stop' | 'jump';
export type AutomationTaskPriority = 'low' | 'medium' | 'high';

// Limits (models/automation.go:109-118, automation_service.go:272)
export const AUTOMATION_MAX_STEPS = 20;
export const AUTOMATION_MAX_WAIT_MINUTES = 90 * 24 * 60; // 129600, per wait and in total
export const AUTOMATION_RULE_NAME_MAX = 200; // runes
export const AUTOMATION_TEXT_MAX = 255; // runes, no \r or \n (titles, subject)
export const AUTOMATION_BODY_MAX_BYTES = 65535; // UTF-8 bytes (description, body)
export const AUTOMATION_DUE_IN_DAYS_MAX = 365;

// Condition allowlists (automation_condition.go:46-72)
export type AutomationFieldKind = 'string' | 'number' | 'money';
export const AUTOMATION_CONDITION_FIELDS: Readonly<
  Record<AutomationEntityType, Readonly<Record<string, AutomationFieldKind>>>
> = {
  lead: {
    status: 'string',
    source: 'string',
    classification: 'string',
    company: 'string',
    owner_id: 'number',
  },
  deal: {
    stage: 'string',
    source: 'string',
    currency: 'string',
    amount_cents: 'money',
    probability: 'number',
    owner_id: 'number',
  },
};
export const AUTOMATION_STRING_OPERATORS = [
  'eq',
  'neq',
  'in',
  'not_in',
  'contains',
  'is_empty',
  'is_not_empty',
] as const;
export const AUTOMATION_NUMBER_OPERATORS = ['eq', 'neq', 'gt', 'gte', 'lt', 'lte'] as const;
export type AutomationOperator =
  | (typeof AUTOMATION_STRING_OPERATORS)[number]
  | (typeof AUTOMATION_NUMBER_OPERATORS)[number];

// Placeholder allowlists (automation_condition.go:250-287); syntax {{ name }}
// matched by /\{\{\s*([A-Za-z0-9_.]*)\s*\}\}/g.
export const AUTOMATION_PLACEHOLDER_PATTERN = /\{\{\s*([A-Za-z0-9_.]*)\s*\}\}/g;
export const AUTOMATION_PLACEHOLDERS: Readonly<Record<AutomationEntityType, readonly string[]>> = {
  lead: [
    'lead.id',
    'lead.first_name',
    'lead.last_name',
    'lead.email',
    'lead.phone',
    'lead.company',
    'lead.status',
    'lead.source',
    'owner.first_name',
    'owner.last_name',
    'owner.email',
  ],
  deal: [
    'deal.id',
    'deal.title',
    'deal.stage',
    'deal.amount',
    'deal.currency',
    'deal.source',
    'owner.first_name',
    'owner.last_name',
    'owner.email',
  ],
};
// create_task title/description only (narrower, erasure-safe)
export const AUTOMATION_TASK_PLACEHOLDERS: Readonly<
  Record<AutomationEntityType, readonly string[]>
> = {
  lead: ['lead.id', 'lead.status', 'lead.source'],
  deal: ['deal.id', 'deal.title', 'deal.stage', 'deal.amount', 'deal.currency', 'deal.source'],
};

// models.AutomationScheduleFilter (automation.go:122-125)
export interface AutomationScheduleFilter {
  status?: string; // lead rules only
  stage?: string; // deal rules only
}

// models.AutomationTrigger (automation.go:132-138). hour_utc has NO omitempty:
// always present on reads (0 for non-schedule rules).
export interface AutomationTrigger {
  from?: string;
  to?: string;
  hour_utc: number;
  filters?: AutomationScheduleFilter;
  stale_days?: number;
}

// models.AutomationStepConfig (automation.go:142-169); every key omitempty.
export interface AutomationStepConfig {
  duration_minutes?: number; // wait
  field?: string; // condition
  operator?: AutomationOperator;
  value?: string | number | string[] | null; // raw JSON: string for eq/neq/contains ("" allowed), string[] for in/not_in (empty items allowed), number for numeric fields, absent or null for is_empty/is_not_empty (the endpoint transform deletes null, 1.4)
  currency?: string; // amount_cents only, /^[A-Z]{3}$/
  else?: AutomationElse; // server normalises '' to 'stop'
  else_position?: number; // only with else 'jump'
  recipient?: AutomationRecipient; // create_task, send_email, notify (server normalises '' to 'owner'); assign_owner is stored as 'user'
  user_id?: number;
  title?: string; // create_task, notify
  description?: string; // create_task
  priority?: '' | AutomationTaskPriority;
  due_in_days?: number;
  subject?: string; // send_email
  body?: string; // send_email, notify
}

// models.AutomationStep (automation.go:222-231)
export interface AutomationStep {
  id: number;
  rule_id: number;
  position: number;
  step_type: AutomationStepType;
  created_at: string;
  config: AutomationStepConfig;
}

// models.AutomationRule (automation.go:177-196) incl. BaseModel (base.go:9-14)
export interface AutomationRule {
  id: number;
  created_at: string;
  updated_at: string;
  deleted_at?: string | null;
  name: string;
  description: string;
  entity_type: AutomationEntityType;
  trigger_type: AutomationTriggerType;
  enabled: boolean;
  version: number;
  schedule_pass_on?: string;
  schedule_cursor: number;
  schedule_completed_on?: string;
  created_by_id?: number;
  trigger: AutomationTrigger;
  steps: AutomationStep[]; // never null on reads; transform defensively to []
}

// models.AutomationRunStep (automation.go:314-325). details holds ids and
// statuses only; known keys listed in AUTOMATION_DETAIL_KEYS below.
export interface AutomationRunStep {
  id: number;
  run_id: number;
  position: number;
  step_type: AutomationStepType;
  outcome: AutomationOutcome;
  error_code?: AutomationErrorCode;
  executed_at: string;
  details: Record<string, unknown>;
}

// models.AutomationRun (automation.go:275-298)
export interface AutomationRun {
  id: number;
  created_at: string;
  updated_at: string;
  rule_id: number;
  // The rule's current name, also for a deleted rule (rule_deleted); empty
  // when the rule row no longer exists. Runs never carry the rule itself.
  rule_name: string;
  rule_deleted: boolean;
  rule_version: number;
  entity_type: AutomationEntityType;
  entity_id: number;
  event_id?: number;
  dedupe_key: string;
  status: AutomationRunStatus;
  next_position: number;
  attempts: number;
  resume_at?: string;
  claimed_at?: string;
  finished_at?: string;
  cancel_reason?: AutomationCancelReason;
  error_code?: AutomationErrorCode;
  steps?: AutomationRunStep[]; // on GET /automation/runs/:id and POST /automation/runs/:id/cancel; absent on list rows
}

// models.AutomationDryRunStep / Report (automation.go:353-371)
export interface AutomationDryRunStep {
  position: number;
  step_type: AutomationStepType;
  outcome: AutomationOutcome; // dry run produces succeeded | skipped | waiting
  error_code?: AutomationErrorCode;
  simulated_at: string;
  details: Record<string, unknown>;
}
// A draft report (POST /automation/rules/dry-run) simulates unsaved form
// values: draft is true and rule_id and rule_version are 0.
export interface AutomationDryRunReport {
  rule_id: number;
  rule_version: number;
  draft: boolean;
  entity_type: AutomationEntityType;
  entity_id: number;
  simulated_start: string;
  simulated_end: string;
  steps: AutomationDryRunStep[];
}

// models.Notification (notification.go:9-22)
export interface Notification {
  id: number;
  created_at: string;
  updated_at: string;
  user_id: number;
  title: string;
  body: string;
  entity_type: AutomationEntityType; // set by the notify step; typed as the two entity types
  entity_id: number;
  run_id?: number;
  read_at?: string;
}
