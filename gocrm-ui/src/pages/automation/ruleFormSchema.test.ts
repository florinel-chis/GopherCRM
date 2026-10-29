import { describe, it, expect } from 'vitest';
import type { AutomationStepType } from '@/types';
import { createMockAutomationRule } from '@/test/factories';
import {
  emptyFormValues,
  emptyStep,
  fromRule,
  mapServerFieldErrors,
  parseStepError,
  serverFieldErrorsOf,
  ruleFormSchema,
  toRulePayload,
  type RuleFormValues,
  type StepFormValues,
} from './ruleFormSchema';

const step = (type: AutomationStepType, overrides: Partial<StepFormValues> = {}): StepFormValues => ({
  ...emptyStep(type),
  ...overrides,
});

const notify = (title = 'Heads up') => step('notify', { title });

const form = (overrides: Partial<RuleFormValues> = {}): RuleFormValues => ({
  ...emptyFormValues(),
  name: 'Rule',
  steps: [notify()],
  ...overrides,
});

// Every message the schema reports, keyed by its dotted path.
const issues = (values: RuleFormValues): Record<string, string[]> => {
  const result = ruleFormSchema.safeParse(values);
  if (result.success) return {};
  const out: Record<string, string[]> = {};
  for (const issue of result.error.issues) {
    const path = issue.path.join('.');
    (out[path] ??= []).push(issue.message);
  }
  return out;
};

describe('ruleFormSchema', () => {
  it('rejects more than 20 steps', () => {
    const twenty = Array.from({ length: 20 }, () => notify());
    expect(issues(form({ steps: twenty }))).toEqual({});
    expect(issues(form({ steps: [...twenty, notify()] }))).toEqual({
      steps: ['A rule has at most 20 steps'],
    });
    expect(issues(form({ steps: [] }))).toEqual({ steps: ['Add at least one step'] });
  });

  it('rejects waits that add up to more than 90 days and accepts exactly 90', () => {
    const wait = (amount: number, unit: StepFormValues['wait_unit']) =>
      step('wait', { wait_amount: amount, wait_unit: unit });

    expect(issues(form({ steps: [wait(60, 'days'), wait(30, 'days'), notify()] }))).toEqual({});
    expect(issues(form({ steps: [wait(60, 'days'), wait(30, 'days'), wait(1, 'minutes')] }))).toEqual({
      steps: ['The waits add up to more than 90 days'],
    });
    expect(issues(form({ steps: [wait(129601, 'minutes')] }))).toEqual({
      'steps.0.wait_amount': ['A wait is at most 90 days'],
    });
    expect(issues(form({ steps: [wait(0, 'hours')] }))).toEqual({
      'steps.0.wait_amount': ['Enter a positive duration'],
    });
    expect(issues(form({ steps: [wait(1.5, 'hours')] }))).toEqual({
      'steps.0.wait_amount': ['Enter a positive duration'],
    });
    expect(issues(form({ steps: [wait(Number.NaN, 'hours')] }))).toEqual({
      'steps.0.wait_amount': ['Enter a positive duration'],
    });
  });

  it('serialises a jump target by key to its current position', () => {
    const target = notify('Target');
    const condition = step('condition', {
      field: 'status',
      operator: 'eq',
      value_text: 'new',
      else: 'jump',
      else_target: target.key,
    });
    const values = form({ steps: [condition, step('wait'), target, notify('Other')] });
    expect(issues(values)).toEqual({});
    expect(toRulePayload(values, 'create').steps[0].config).toEqual({
      field: 'status',
      operator: 'eq',
      value: 'new',
      else: 'jump',
      else_position: 3,
    });

    // Moving the target to the end follows it.
    const moved = { ...values, steps: [condition, step('wait'), notify('Other'), target] };
    expect(toRulePayload(moved, 'create').steps[0].config.else_position).toBe(4);

    // A stop branch sends no position.
    const stop = { ...values, steps: [{ ...condition, else: 'stop' as const }, target] };
    expect(toRulePayload(stop, 'create').steps[0].config).toEqual({
      field: 'status',
      operator: 'eq',
      value: 'new',
      else: 'stop',
    });
  });

  it('rejects a jump target that is missing or not after the condition', () => {
    const first = notify('First');
    const jump = (else_target: string) =>
      step('condition', { field: 'status', operator: 'is_empty', else: 'jump', else_target });

    expect(issues(form({ steps: [first, jump(first.key), notify()] }))).toEqual({
      'steps.1.else_target': ['Jump to a later step'],
    });
    expect(issues(form({ steps: [first, jump('step-gone'), notify()] }))).toEqual({
      'steps.1.else_target': ['The jump target was removed; pick another step'],
    });
    expect(issues(form({ steps: [first, jump('')] }))).toEqual({
      'steps.1.else_target': ['No later step to jump to'],
    });
  });

  it('rejects unknown placeholders and personal placeholders in task text', () => {
    const task = (title: string, description = '') => step('create_task', { title, description });

    expect(issues(form({ steps: [task('Call {{lead.id}} ({{ lead.status }})')] }))).toEqual({});
    // A lead's email is fine in a notification, not in a task.
    expect(issues(form({ steps: [notify('Mail {{lead.email}}')] }))).toEqual({});
    expect(issues(form({ steps: [task('Mail {{lead.email}}')] }))).toEqual({
      'steps.0.title': ['Unknown placeholder {{lead.email}}'],
    });
    expect(issues(form({ steps: [task('Call', 'For {{owner.first_name}} and {{x}}')] }))).toEqual({
      'steps.0.description': ['Unknown placeholder {{owner.first_name}}'],
    });
    // Deal placeholders are unknown on a lead rule.
    expect(
      issues(form({ steps: [step('send_email', { subject: 'Deal {{deal.title}}', body: 'Body' })] })),
    ).toEqual({ 'steps.0.subject': ['Unknown placeholder {{deal.title}}'] });
    expect(issues(form({ steps: [step('send_email', { subject: 'S', body: ' ' })] }))).toEqual({
      'steps.0.body': ['Body is required'],
    });
    expect(issues(form({ steps: [notify('Line\nbreak')] }))).toEqual({
      'steps.0.title': ['No line breaks'],
    });
    expect(issues(form({ steps: [notify('x'.repeat(256))] }))).toEqual({
      'steps.0.title': ['At most 255 characters'],
    });
    // 255 code points of a two-unit character are still within the limit.
    expect(issues(form({ steps: [notify('😀'.repeat(255))] }))).toEqual({});
  });

  it('requires a three-letter currency on amount_cents and refuses it elsewhere', () => {
    const money = (currency: string, value_text = '1500') =>
      step('condition', { field: 'amount_cents', operator: 'gte', value_text, currency });
    const deal = (s: StepFormValues) => form({ entity_type: 'deal', trigger_type: 'deal_created', steps: [s] });

    expect(issues(deal(money('EUR')))).toEqual({});
    expect(issues(deal(money('eur')))).toEqual({ 'steps.0.currency': ['Three-letter currency, e.g. EUR'] });
    expect(issues(deal(money('')))).toEqual({ 'steps.0.currency': ['Three-letter currency, e.g. EUR'] });
    expect(issues(deal(money('EUR', '1e3')))).toEqual({
      'steps.0.value_text': ['Enter an amount like 1500 or -12.50 (at most two decimals)'],
    });
    expect(toRulePayload(deal(money('EUR', '-12.50')), 'create').steps[0].config).toEqual({
      field: 'amount_cents',
      operator: 'gte',
      value: -1250,
      currency: 'EUR',
      else: 'stop',
    });

    // A currency left over from an earlier amount condition is never sent.
    const probability = step('condition', {
      field: 'probability',
      operator: 'gt',
      value_text: '12.5',
      currency: 'EUR',
    });
    expect(issues(deal(probability))).toEqual({});
    expect(toRulePayload(deal(probability), 'create').steps[0].config).toEqual({
      field: 'probability',
      operator: 'gt',
      value: 12.5,
      else: 'stop',
    });
    expect(issues(deal({ ...probability, value_text: '1,5' }))).toEqual({
      'steps.0.value_text': ['Enter a number like 10 or 12.5'],
    });
    // amount_cents is not a lead field.
    expect(issues(form({ steps: [money('EUR')] }))).toEqual({ 'steps.0.field': ['Pick a field'] });
    expect(issues(deal({ ...money('EUR'), operator: 'contains' }))).toEqual({
      'steps.0.operator': ['Pick an operator'],
    });
  });

  it('accepts empty strings in eq values and in-lists', () => {
    const eq = step('condition', { field: 'company', operator: 'eq', value_text: '' });
    const list = step('condition', { field: 'source', operator: 'in', value_list: ['', ' a, b '] });
    const values = form({ steps: [eq, list] });
    expect(issues(values)).toEqual({});
    const payload = toRulePayload(values, 'create');
    expect(payload.steps[0].config.value).toBe('');
    expect(payload.steps[1].config.value).toEqual(['', ' a, b ']);

    expect(issues(form({ steps: [{ ...list, value_list: [] }] }))).toEqual({
      'steps.0.value_list': ['Add at least one value'],
    });
    const empty = step('condition', { field: 'company', operator: 'is_empty', value_text: 'ignored' });
    expect(toRulePayload(form({ steps: [empty] }), 'create').steps[0].config).toEqual({
      field: 'company',
      operator: 'is_empty',
      else: 'stop',
    });
  });

  it('round-trips a rule through fromRule and toRulePayload', () => {
    const rule = createMockAutomationRule({
      id: 9,
      name: 'Deals',
      description: 'Desc',
      entity_type: 'deal',
      trigger_type: 'schedule',
      enabled: true,
      trigger: { hour_utc: 7, stale_days: 3, filters: { stage: 'proposal' } },
      steps: [
        { step_type: 'wait', config: { duration_minutes: 2880 } },
        { step_type: 'wait', config: { duration_minutes: 120 } },
        { step_type: 'wait', config: { duration_minutes: 45 } },
        {
          step_type: 'condition',
          config: { field: 'amount_cents', operator: 'gte', value: 150050.5, currency: 'EUR', else: 'jump', else_position: 7 },
        },
        { step_type: 'condition', config: { field: 'amount_cents', operator: 'lt', value: 1250, currency: 'USD', else: 'stop' } },
        { step_type: 'condition', config: { field: 'source', operator: 'in', value: ['a, b', ' ', ''], else: 'stop' } },
        { step_type: 'condition', config: { field: 'probability', operator: 'gt', value: 12.5, else: 'stop' } },
        {
          step_type: 'create_task',
          config: { title: 'T {{deal.id}}', description: 'D', priority: 'low', due_in_days: 2, recipient: 'user', user_id: 4 },
        },
        { step_type: 'assign_owner', config: { user_id: 5, recipient: 'user' } },
        { step_type: 'send_email', config: { subject: 'S', body: 'B', recipient: 'owner' } },
        { step_type: 'notify', config: { title: 'N', recipient: 'user', user_id: 6 } },
      ].map((s, i) => ({ ...s, id: i + 1, rule_id: 9, position: i + 1, created_at: '' })) as never,
    });

    const values = fromRule(rule);
    expect(values.steps.map((s) => [s.wait_amount, s.wait_unit]).slice(0, 3)).toEqual([
      [2, 'days'],
      [2, 'hours'],
      [45, 'minutes'],
    ]);
    expect(values.steps[4].value_text).toBe('12.50');
    expect(values.steps[3].else_target).toBe(values.steps[6].key);
    expect(new Set(values.steps.map((s) => s.key)).size).toBe(11);
    expect(issues(values)).toEqual({});

    const payload = toRulePayload(values, 'edit');
    expect(payload).toEqual({
      name: 'Deals',
      description: 'Desc',
      entity_type: 'deal',
      trigger_type: 'schedule',
      trigger: { hour_utc: 7, stale_days: 3, filters: { stage: 'proposal' } },
      steps: [
        { step_type: 'wait', config: { duration_minutes: 2880 } },
        { step_type: 'wait', config: { duration_minutes: 120 } },
        { step_type: 'wait', config: { duration_minutes: 45 } },
        { step_type: 'condition', config: { field: 'amount_cents', operator: 'gte', value: 150050.5, currency: 'EUR', else: 'jump', else_position: 7 } },
        { step_type: 'condition', config: { field: 'amount_cents', operator: 'lt', value: 1250, currency: 'USD', else: 'stop' } },
        { step_type: 'condition', config: { field: 'source', operator: 'in', value: ['a, b', ' ', ''], else: 'stop' } },
        { step_type: 'condition', config: { field: 'probability', operator: 'gt', value: 12.5, else: 'stop' } },
        {
          step_type: 'create_task',
          config: { title: 'T {{deal.id}}', description: 'D', priority: 'low', due_in_days: 2, recipient: 'user', user_id: 4 },
        },
        { step_type: 'assign_owner', config: { user_id: 5 } },
        { step_type: 'send_email', config: { subject: 'S', body: 'B', recipient: 'owner' } },
        { step_type: 'notify', config: { title: 'N', recipient: 'user', user_id: 6 } },
      ],
    });
    expect('enabled' in payload).toBe(false);
    expect(toRulePayload(values, 'create').enabled).toBe(true);

    // An edited value is re-parsed instead of re-sent.
    const edited = { ...values, steps: values.steps.map((s, i) => (i === 3 ? { ...s, value_text: '1500.25' } : s)) };
    expect(toRulePayload(edited, 'edit').steps[3].config.value).toBe(150025);

    // Change triggers send only the non-empty ends; created triggers send nothing.
    expect(toRulePayload(form({ trigger_type: 'lead_status_changed', to: 'qualified' }), 'create').trigger).toEqual({
      to: 'qualified',
    });
    expect(toRulePayload(form({ from: 'new', to: 'qualified' }), 'create').trigger).toEqual({});
  });

  it('parses "step N: ..." server messages and strips the sentinel suffix', () => {
    expect(parseStepError('step 3: title is required: validation failed')).toEqual({
      step: 3,
      text: 'title is required',
    });
    expect(parseStepError('step 12: else_position must be after the step and at most 4')).toEqual({
      step: 12,
      text: 'else_position must be after the step and at most 4',
    });
    expect(parseStepError('name is required: validation failed')).toEqual({ text: 'name is required' });
    expect(parseStepError('user 7 is inactive: assignee not found')).toEqual({ text: 'user 7 is inactive' });
    expect(parseStepError('Something else')).toEqual({ text: 'Something else' });
  });

  it('mapServerFieldErrors maps every server path onto its form field', () => {
    const values: RuleFormValues = {
      ...emptyFormValues(),
      name: 'R',
      trigger_type: 'schedule',
      steps: [
        step('wait', { key: 'w', wait_amount: 2, wait_unit: 'hours' }),
        step('condition', { key: 'c1', field: 'source', operator: 'in', value_list: ['a'], else: 'jump', else_target: 'n' }),
        step('condition', { key: 'c2', field: 'status', operator: 'eq', value_text: 'new' }),
        step('create_task', { key: 't', title: 'T' }),
        step('send_email', { key: 'e', subject: 'S', body: 'B', recipient: 'user', user_id: 3 }),
        step('notify', { key: 'n', title: 'N' }),
      ],
    };
    const error = (field: string) => ({ field, code: 'x', message: field });
    const mapped = mapServerFieldErrors(
      [
        'name', 'description', 'trigger_type', 'trigger.from', 'trigger.to', 'trigger.hour_utc', 'trigger.stale_days',
        'trigger.filters.status', 'steps[0].config.duration_minutes', 'steps[1].config.value', 'steps[1].config.else_position',
        'steps[1].config.field', 'steps[1].config.operator', 'steps[2].config.value', 'steps[2].config.currency',
        'steps[2].config.else', 'steps[3].config.title', 'steps[3].config.description', 'steps[3].config.priority',
        'steps[3].config.due_in_days', 'steps[3].config.recipient', 'steps[4].config.subject', 'steps[4].config.body',
        'steps[4].config.user_id',
      ].map(error),
      values,
      values,
    );
    expect(mapped.unmapped).toEqual([]);
    expect(Object.fromEntries(mapped.fields.map((f) => [f.message, f.name]))).toEqual({
      name: 'name',
      description: 'description',
      trigger_type: 'trigger_type',
      'trigger.from': 'from',
      'trigger.to': 'to',
      'trigger.hour_utc': 'hour_utc',
      'trigger.stale_days': 'stale_days',
      'trigger.filters.status': 'filter_value',
      'steps[0].config.duration_minutes': 'steps.0.wait_amount',
      'steps[1].config.value': 'steps.1.value_list',
      'steps[1].config.else_position': 'steps.1.else_target',
      'steps[1].config.field': 'steps.1.field',
      'steps[1].config.operator': 'steps.1.operator',
      'steps[2].config.value': 'steps.2.value_text',
      'steps[2].config.currency': 'steps.2.currency',
      'steps[2].config.else': 'steps.2.else',
      'steps[3].config.title': 'steps.3.title',
      'steps[3].config.description': 'steps.3.description',
      'steps[3].config.priority': 'steps.3.priority',
      'steps[3].config.due_in_days': 'steps.3.due_in_days',
      'steps[3].config.recipient': 'steps.3.recipient',
      'steps[4].config.subject': 'steps.4.subject',
      'steps[4].config.body': 'steps.4.body',
      'steps[4].config.user_id': 'steps.4.user_id',
    });
  });

  it('mapServerFieldErrors sends unknown or out-of-range paths to the form-level list', () => {
    const values: RuleFormValues = { ...emptyFormValues(), steps: [step('notify', { key: 'a', title: 'A' })] };
    const mapped = mapServerFieldErrors(
      [
        { field: 'entity_type', code: 'x', message: 'entity' },
        { field: 'steps', code: 'x', message: 'too many' },
        { field: 'steps[0].step_type', code: 'x', message: 'bad type' },
        { field: 'steps[0].config.template_id', code: 'x', message: 'unknown config' },
        { field: 'steps[5].config.title', code: 'x', message: 'missing step' },
        { field: 'mystery', code: 'x', message: 'mystery' },
      ],
      values,
      values,
    );
    expect(mapped.fields).toEqual([]);
    expect(mapped.unmapped).toEqual([
      { text: 'entity' },
      { text: 'too many' },
      { text: 'bad type', stepKey: 'a' },
      { text: 'unknown config', stepKey: 'a' },
      { text: 'Step 6: missing step' },
      { text: 'mystery' },
    ]);
  });

  it('follows moved steps by key and drops errors of removed, retyped or edited fields', () => {
    const submitted: RuleFormValues = {
      ...emptyFormValues(),
      name: 'Old name',
      steps: [
        step('notify', { key: 'a', title: 'A' }),
        step('notify', { key: 'b', title: 'B' }),
        step('notify', { key: 'c', title: 'C' }),
        step('notify', { key: 'd', title: 'D' }),
      ],
    };
    const current: RuleFormValues = {
      ...submitted,
      name: 'New name',
      steps: [
        step('notify', { key: 'b', title: 'B' }),
        step('notify', { key: 'a', title: 'A edited' }),
        step('create_task', { key: 'd', title: 'D' }),
      ],
    };
    const error = (field: string) => ({ field, code: 'x', message: field });
    const mapped = mapServerFieldErrors(
      ['name', 'steps[0].config.title', 'steps[1].config.title', 'steps[2].config.title', 'steps[3].config.title'].map(error),
      submitted,
      current,
    );
    expect(mapped.fields).toEqual([{ name: 'steps.0.title', message: 'steps[1].config.title' }]);
    expect(mapped.unmapped).toEqual([]);
  });

  it('reads error.details.fields and ignores anything else', () => {
    expect(serverFieldErrorsOf(undefined)).toBeNull();
    expect(serverFieldErrorsOf({ ids: [1] })).toBeNull();
    expect(
      serverFieldErrorsOf({ fields: [{ field: 'name', code: 'required', message: 'm' }, { field: 1 }, 'x'] }),
    ).toEqual([{ field: 'name', code: 'required', message: 'm' }]);
  });

  it('drops a user error whose step no longer sends to a specific user', () => {
    const submitted: RuleFormValues = {
      ...emptyFormValues(),
      steps: [step('notify', { key: 'a', title: 'A', recipient: 'user', user_id: 7 })],
    };
    const current: RuleFormValues = {
      ...submitted,
      steps: [step('notify', { key: 'a', title: 'A', recipient: 'owner', user_id: 7 })],
    };
    const errors = [{ field: 'steps[0].config.user_id', code: 'unknown_reference', message: 'the user is deactivated' }];
    expect(mapServerFieldErrors(errors, submitted, current)).toEqual({ fields: [], unmapped: [] });
    expect(mapServerFieldErrors(errors, submitted, submitted).fields).toEqual([
      { name: 'steps.0.user_id', message: 'the user is deactivated' },
    ]);
  });
});
