import { describe, it, expect } from 'vitest';
import {
  AUTOMATION_DATETIME,
  cancelReasonLabel,
  describeTrigger,
  entityLabel,
  entityPath,
  errorCodeLabel,
  formatStepDetails,
  operatorLabel,
  outcomeLabel,
  runRuleLabel,
  runStatusLabel,
  stepTypeLabel,
  triggerTypeLabel,
} from './automationFormat';
import { formatDate } from '@/utils/date';
import {
  AUTOMATION_NUMBER_OPERATORS,
  AUTOMATION_RUN_STATUSES,
  AUTOMATION_STEP_TYPES,
  AUTOMATION_STRING_OPERATORS,
  AUTOMATION_TRIGGERS,
} from '@/types';
import type { AutomationOutcome } from '@/types';
import { createMockAutomationRule } from '@/test/factories';

describe('automationFormat', () => {
  it('labels every trigger, step type, run status and outcome', () => {
    expect(AUTOMATION_TRIGGERS.lead.map(triggerTypeLabel)).toEqual([
      'Lead created',
      'Lead updated',
      'Lead status changed',
      'Schedule',
    ]);
    expect(AUTOMATION_TRIGGERS.deal.map(triggerTypeLabel)).toEqual([
      'Deal created',
      'Deal updated',
      'Deal stage changed',
      'Schedule',
    ]);
    expect(AUTOMATION_STEP_TYPES.map(stepTypeLabel)).toEqual([
      'Wait',
      'Condition',
      'Create task',
      'Assign owner',
      'Send email',
      'Notify',
    ]);
    expect(AUTOMATION_RUN_STATUSES.map(runStatusLabel)).toEqual([
      'Pending',
      'Running',
      'Waiting',
      'Completed',
      'Failed',
      'Cancelled',
    ]);
    const outcomes: AutomationOutcome[] = ['succeeded', 'skipped', 'failed', 'sending', 'waiting'];
    expect(outcomes.map(outcomeLabel)).toEqual([
      'Succeeded',
      'Skipped',
      'Failed',
      'Sending',
      'Waiting',
    ]);
    expect(
      [...new Set([...AUTOMATION_STRING_OPERATORS, ...AUTOMATION_NUMBER_OPERATORS])].map(
        operatorLabel,
      ),
    ).toEqual([
      'equals',
      'does not equal',
      'is one of',
      'is not one of',
      'contains',
      'is empty',
      'is not empty',
      '>',
      '≥',
      '<',
      '≤',
    ]);
    expect(cancelReasonLabel('rule_changed')).toBe('Rule changed');
    expect(cancelReasonLabel('rule_disabled')).toBe('Rule disabled');
    expect(cancelReasonLabel('rule_deleted')).toBe('Rule deleted');
    expect(cancelReasonLabel('manual')).toBe('Cancelled manually');
    expect(cancelReasonLabel('record_deleted')).toBe('Record deleted');
    expect(errorCodeLabel('recipient_unavailable')).toBe('Recipient unavailable');
    expect(errorCodeLabel('mail_failed')).toBe('Mail failed');
    expect(errorCodeLabel('email_unconfirmed')).toBe('Email delivery unconfirmed');
    expect(errorCodeLabel('step_failed')).toBe('Step failed');
    expect(entityLabel('lead', 5)).toBe('Lead #5');
    expect(entityLabel('deal', 9)).toBe('Deal #9');
    expect(entityPath('lead', 5)).toBe('/leads/5');
    expect(entityPath('deal', 9)).toBe('/deals/9');
  });

  it('describes a trigger with its from/to filter and a schedule with its UTC hour', () => {
    expect(describeTrigger(createMockAutomationRule())).toBe('Lead created');
    expect(
      describeTrigger(
        createMockAutomationRule({
          trigger_type: 'lead_status_changed',
          trigger: { hour_utc: 0, to: 'qualified' },
        }),
      ),
    ).toBe('Lead status changed → qualified');
    expect(
      describeTrigger(
        createMockAutomationRule({
          trigger_type: 'lead_status_changed',
          trigger: { hour_utc: 0, from: 'new' },
        }),
      ),
    ).toBe('Lead status changed: new → any');
    expect(
      describeTrigger(
        createMockAutomationRule({
          entity_type: 'deal',
          trigger_type: 'deal_stage_changed',
          trigger: { hour_utc: 0, from: 'proposal', to: 'won' },
        }),
      ),
    ).toBe('Deal stage changed: proposal → won');
    expect(
      describeTrigger(
        createMockAutomationRule({ trigger_type: 'schedule', trigger: { hour_utc: 8 } }),
      ),
    ).toBe('Schedule, daily at 08:00 UTC');
    expect(
      describeTrigger(
        createMockAutomationRule({
          trigger_type: 'schedule',
          trigger: { hour_utc: 17, filters: { status: 'contacted' }, stale_days: 7 },
        }),
      ),
    ).toBe('Schedule, daily at 17:00 UTC, status contacted, not updated for 7 days');
    expect(
      describeTrigger(
        createMockAutomationRule({
          entity_type: 'deal',
          trigger_type: 'schedule',
          trigger: { hour_utc: 0, filters: { stage: 'proposal' }, stale_days: 1 },
        }),
      ),
    ).toBe('Schedule, daily at 00:00 UTC, stage proposal, not updated for 1 day');
  });

  it('formats known detail keys and passes unknown keys through', () => {
    const details = formatStepDetails({
      resume_at: '2026-11-01T08:00:00Z',
      result: false,
      branch: 'jump',
      next_position: 4,
      assignee_id: 3,
      due_date: '2026-11-03T08:00:00Z',
      lead_id: 5,
      customer_id: 6,
      task_id: 7,
      from_owner_id: 2,
      to_owner_id: 3,
      recipient_id: 3,
      notification_id: 11,
      mystery: 'x',
    });

    expect(details).toEqual([
      {
        key: 'resume_at',
        label: 'Resumes',
        value: formatDate('2026-11-01T08:00:00Z', AUTOMATION_DATETIME),
      },
      { key: 'result', label: 'Condition', value: 'false' },
      { key: 'branch', label: 'Branch', value: 'Jump' },
      { key: 'next_position', label: 'Next step', value: '4' },
      { key: 'assignee_id', label: 'Assignee', value: 'User #3' },
      { key: 'due_date', label: 'Due', value: formatDate('2026-11-03T08:00:00Z', 'MMM d, yyyy') },
      { key: 'lead_id', label: 'Lead', value: 'Lead #5', href: '/leads/5' },
      { key: 'customer_id', label: 'Customer', value: 'Customer #6', href: '/customers/6' },
      { key: 'task_id', label: 'Task', value: 'Task #7', href: '/tasks/7' },
      { key: 'from_owner_id', label: 'Owner from', value: 'User #2' },
      { key: 'to_owner_id', label: 'Owner to', value: 'User #3' },
      { key: 'recipient_id', label: 'Recipient', value: 'User #3' },
      { key: 'notification_id', label: 'Notification', value: '11' },
      { key: 'mystery', label: 'mystery', value: 'x' },
    ]);
    expect(formatStepDetails({ branch: 'next', result: true })).toEqual([
      { key: 'result', label: 'Condition', value: 'true' },
      { key: 'branch', label: 'Branch', value: 'Continue' },
    ]);
    expect(formatStepDetails({ branch: 'stop' })[0].value).toBe('Stop');
    expect(formatStepDetails({})).toEqual([]);
    expect(formatStepDetails(null)).toEqual([]);
  });

  it('runRuleLabel falls back to Rule #id and flags deleted rules', () => {
    expect(runRuleLabel({ rule_id: 3, rule_name: 'Welcome call', rule_deleted: false })).toBe('Welcome call');
    expect(runRuleLabel({ rule_id: 3, rule_name: 'Welcome call', rule_deleted: true })).toBe('Welcome call (deleted)');
    expect(runRuleLabel({ rule_id: 3, rule_name: '', rule_deleted: true })).toBe('Rule #3 (deleted)');
    expect(runRuleLabel({ rule_id: 3, rule_name: '  ', rule_deleted: false })).toBe('Rule #3');
  });
});
