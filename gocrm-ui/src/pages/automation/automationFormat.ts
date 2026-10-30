import { formatDate } from '@/utils/date';
import type {
  AutomationCancelReason,
  AutomationEntityType,
  AutomationErrorCode,
  AutomationOperator,
  AutomationOutcome,
  AutomationRule,
  AutomationRun,
  AutomationRunStatus,
  AutomationStepType,
  AutomationTriggerType,
} from '@/types';

/**
 * The rule a run belongs to: its name as the run reports it, "Rule #id"
 * when the rule row is gone, and "(deleted)" for a deleted rule.
 */
export const runRuleLabel = (run: Pick<AutomationRun, 'rule_id' | 'rule_name' | 'rule_deleted'>): string => {
  const name = run.rule_name?.trim() ? run.rule_name : `Rule #${run.rule_id}`;
  return run.rule_deleted ? `${name} (deleted)` : name;
};

// Date-time pattern for run, step and notification timestamps (local time).
export const AUTOMATION_DATETIME = 'MMM d, yyyy HH:mm';
const AUTOMATION_DATE = 'MMM d, yyyy';

const TRIGGER_LABELS: Record<AutomationTriggerType, string> = {
  lead_created: 'Lead created',
  lead_updated: 'Lead updated',
  lead_status_changed: 'Lead status changed',
  deal_created: 'Deal created',
  deal_updated: 'Deal updated',
  deal_stage_changed: 'Deal stage changed',
  schedule: 'Schedule',
};

const STEP_TYPE_LABELS: Record<AutomationStepType, string> = {
  wait: 'Wait',
  condition: 'Condition',
  create_task: 'Create task',
  assign_owner: 'Assign owner',
  send_email: 'Send email',
  notify: 'Notify',
};

const RUN_STATUS_LABELS: Record<AutomationRunStatus, string> = {
  pending: 'Pending',
  running: 'Running',
  waiting: 'Waiting',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
};

const OUTCOME_LABELS: Record<AutomationOutcome, string> = {
  succeeded: 'Succeeded',
  skipped: 'Skipped',
  failed: 'Failed',
  sending: 'Sending',
  waiting: 'Waiting',
};

const OPERATOR_LABELS: Record<AutomationOperator, string> = {
  eq: 'equals',
  neq: 'does not equal',
  in: 'is one of',
  not_in: 'is not one of',
  contains: 'contains',
  is_empty: 'is empty',
  is_not_empty: 'is not empty',
  gt: '>',
  gte: '≥',
  lt: '<',
  lte: '≤',
};

const CANCEL_REASON_LABELS: Record<AutomationCancelReason, string> = {
  rule_changed: 'Rule changed',
  rule_disabled: 'Rule disabled',
  rule_deleted: 'Rule deleted',
  manual: 'Cancelled manually',
  record_deleted: 'Record deleted',
};

const ERROR_CODE_LABELS: Record<AutomationErrorCode, string> = {
  recipient_unavailable: 'Recipient unavailable',
  mail_failed: 'Mail failed',
  email_unconfirmed: 'Email delivery unconfirmed',
  step_failed: 'Step failed',
};

const ENTITY_LABELS: Record<AutomationEntityType, string> = { lead: 'Lead', deal: 'Deal' };
const ENTITY_PATHS: Record<AutomationEntityType, string> = { lead: '/leads', deal: '/deals' };

// Unknown values (a newer server) fall back to the raw string.
export const triggerTypeLabel = (t: AutomationTriggerType): string => TRIGGER_LABELS[t] ?? t;
export const stepTypeLabel = (s: AutomationStepType): string => STEP_TYPE_LABELS[s] ?? s;
export const runStatusLabel = (s: AutomationRunStatus): string => RUN_STATUS_LABELS[s] ?? s;
export const outcomeLabel = (o: AutomationOutcome): string => OUTCOME_LABELS[o] ?? o;
export const operatorLabel = (o: AutomationOperator): string => OPERATOR_LABELS[o] ?? o;
export const cancelReasonLabel = (r: AutomationCancelReason): string =>
  CANCEL_REASON_LABELS[r] ?? r;
export const errorCodeLabel = (c: AutomationErrorCode): string => ERROR_CODE_LABELS[c] ?? c;

export const entityLabel = (type: AutomationEntityType, id: number): string =>
  `${ENTITY_LABELS[type] ?? type} #${id}`;
export const entityPath = (type: AutomationEntityType, id: number): string =>
  `${ENTITY_PATHS[type] ?? `/${type}s`}/${id}`;

const pad2 = (n: number): string => String(n).padStart(2, '0');

// "Lead status changed → qualified", "Lead status changed: new → any",
// "Schedule, daily at 08:00 UTC, status contacted, not updated for 7 days".
export const describeTrigger = (rule: Pick<AutomationRule, 'trigger_type' | 'trigger'>): string => {
  const label = triggerTypeLabel(rule.trigger_type);
  const trigger = rule.trigger ?? { hour_utc: 0 };
  if (rule.trigger_type === 'schedule') {
    const parts = [`${label}, daily at ${pad2(trigger.hour_utc ?? 0)}:00 UTC`];
    if (trigger.filters?.status) parts.push(`status ${trigger.filters.status}`);
    if (trigger.filters?.stage) parts.push(`stage ${trigger.filters.stage}`);
    if (trigger.stale_days && trigger.stale_days > 0) {
      parts.push(
        `not updated for ${trigger.stale_days} ${trigger.stale_days === 1 ? 'day' : 'days'}`,
      );
    }
    return parts.join(', ');
  }
  if (trigger.from) {
    return `${label}: ${trigger.from} → ${trigger.to || 'any'}`;
  }
  if (trigger.to) {
    return `${label} → ${trigger.to}`;
  }
  return label;
};

export interface StepDetail {
  key: string;
  label: string;
  value: string;
  href?: string;
}

const BRANCH_LABELS: Record<string, string> = { next: 'Continue', jump: 'Jump', stop: 'Stop' };

const userRef = (value: unknown): string => `User #${String(value)}`;

type DetailFormatter = {
  label: string;
  value: (v: unknown) => string;
  href?: (v: unknown) => string;
};

const DETAIL_FORMATTERS = new Map<string, DetailFormatter>(
  Object.entries({
    resume_at: { label: 'Resumes', value: (v) => formatDate(String(v), AUTOMATION_DATETIME) },
    result: { label: 'Condition', value: (v) => String(v) },
    branch: { label: 'Branch', value: (v) => BRANCH_LABELS[String(v)] ?? String(v) },
    next_position: { label: 'Next step', value: (v) => String(v) },
    assignee_id: { label: 'Assignee', value: userRef },
    due_date: { label: 'Due', value: (v) => formatDate(String(v), AUTOMATION_DATE) },
    lead_id: {
      label: 'Lead',
      value: (v) => `Lead #${String(v)}`,
      href: (v) => `/leads/${String(v)}`,
    },
    customer_id: {
      label: 'Customer',
      value: (v) => `Customer #${String(v)}`,
      href: (v) => `/customers/${String(v)}`,
    },
    task_id: {
      label: 'Task',
      value: (v) => `Task #${String(v)}`,
      href: (v) => `/tasks/${String(v)}`,
    },
    from_owner_id: { label: 'Owner from', value: userRef },
    to_owner_id: { label: 'Owner to', value: userRef },
    recipient_id: { label: 'Recipient', value: userRef },
    notification_id: { label: 'Notification', value: (v) => String(v) },
  } satisfies Record<string, DetailFormatter>),
);

// Known keys first in a fixed reading order, then unknown keys as received.
const DETAIL_ORDER = [...DETAIL_FORMATTERS.keys()];

export const formatStepDetails = (
  details: Record<string, unknown> | null | undefined,
): StepDetail[] => {
  if (!details) return [];
  const keys = Object.keys(details);
  const known = DETAIL_ORDER.filter((key) => keys.includes(key));
  const unknown = keys.filter((key) => !DETAIL_FORMATTERS.has(key));
  return [...known, ...unknown].map((key) => {
    const value = details[key];
    const formatter = DETAIL_FORMATTERS.get(key);
    if (!formatter) {
      return { key, label: key, value: String(value) };
    }
    const detail: StepDetail = { key, label: formatter.label, value: formatter.value(value) };
    if (formatter.href) detail.href = formatter.href(value);
    return detail;
  });
};
