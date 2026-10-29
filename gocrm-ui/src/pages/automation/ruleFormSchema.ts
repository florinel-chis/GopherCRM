import * as z from 'zod';
import type { AutomationRuleInput, AutomationStepInput } from '@/api/endpoints/automation';
import {
  AUTOMATION_BODY_MAX_BYTES,
  AUTOMATION_CONDITION_FIELDS,
  AUTOMATION_DUE_IN_DAYS_MAX,
  AUTOMATION_LEAD_STATUSES,
  AUTOMATION_MAX_STEPS,
  AUTOMATION_MAX_WAIT_MINUTES,
  AUTOMATION_NUMBER_OPERATORS,
  AUTOMATION_PLACEHOLDER_PATTERN,
  AUTOMATION_PLACEHOLDERS,
  AUTOMATION_RULE_NAME_MAX,
  AUTOMATION_STEP_TYPES,
  AUTOMATION_STRING_OPERATORS,
  AUTOMATION_TASK_PLACEHOLDERS,
  AUTOMATION_TEXT_MAX,
  AUTOMATION_TRIGGERS,
  DEAL_STAGE_VALUES,
  isChangeTrigger,
  type AutomationEntityType,
  type AutomationFieldKind,
  type AutomationOperator,
  type AutomationRule,
  type AutomationStep,
  type AutomationStepConfig,
  type AutomationStepType,
  type AutomationTrigger,
} from '@/types';
import { centsToMoneyText, parseConditionNumber, parseMoneyToCents } from './conditionValues';

// The rule editor's form model, its validation (mirroring the server's
// validateRule so a refusal shows on the field before the request) and the
// conversion to and from the API shape.

export const WAIT_UNITS = ['minutes', 'hours', 'days'] as const;
export type WaitUnit = (typeof WAIT_UNITS)[number];
const MINUTES_PER_UNIT: Record<WaitUnit, number> = { minutes: 1, hours: 60, days: 1440 };

export const WAIT_UNIT_LABELS: Record<WaitUnit, string> = {
  minutes: 'Minutes',
  hours: 'Hours',
  days: 'Days',
};

export const CONDITION_FIELD_LABELS: Record<string, string> = {
  status: 'Status',
  stage: 'Stage',
  source: 'Source',
  classification: 'Classification',
  company: 'Company',
  currency: 'Currency',
  amount_cents: 'Amount',
  probability: 'Probability',
  owner_id: 'Owner ID',
};

const LIST_OPERATORS = new Set<string>(['in', 'not_in']);
const VALUELESS_OPERATORS = new Set<string>(['is_empty', 'is_not_empty']);

export const isListOperator = (operator: string): boolean => LIST_OPERATORS.has(operator);
export const isValuelessOperator = (operator: string): boolean => VALUELESS_OPERATORS.has(operator);

export const conditionFieldKind = (
  entity: AutomationEntityType,
  field: string,
): AutomationFieldKind | undefined =>
  Object.prototype.hasOwnProperty.call(AUTOMATION_CONDITION_FIELDS[entity], field)
    ? AUTOMATION_CONDITION_FIELDS[entity][field]
    : undefined;

export const operatorsFor = (kind: AutomationFieldKind | undefined): readonly AutomationOperator[] => {
  if (!kind) return [];
  return kind === 'string' ? AUTOMATION_STRING_OPERATORS : AUTOMATION_NUMBER_OPERATORS;
};

/** Lead statuses (lead rules) or deal stages (deal rules) the API accepts. */
export const entityValues = (entity: AutomationEntityType): readonly string[] =>
  entity === 'lead' ? AUTOMATION_LEAD_STATUSES : DEAL_STAGE_VALUES;

export const placeholderHelp = (names: readonly string[]): string =>
  `Placeholders: ${names.map((name) => `{{${name}}}`).join(', ')}`;

const TRIGGER_TYPES = [
  'lead_created',
  'lead_updated',
  'lead_status_changed',
  'deal_created',
  'deal_updated',
  'deal_stage_changed',
  'schedule',
] as const;

// Number inputs hold NaN while empty; the refinements below report it.
const numeric = z.union([z.number(), z.nan()]);

const stepSchema = z.object({
  // Stable id from newStepKey(); jump targets refer to it.
  key: z.string(),
  step_type: z.enum(AUTOMATION_STEP_TYPES),
  wait_amount: numeric,
  wait_unit: z.enum(WAIT_UNITS),
  field: z.string(),
  operator: z.string(),
  value_text: z.string(),
  value_list: z.array(z.string()),
  // The stored value and the text it was rendered as: re-sent unchanged
  // while the visible value is not edited.
  original_value: z.union([z.string(), z.number(), z.array(z.string())]).optional(),
  original_text: z.string().optional(),
  currency: z.string(),
  else: z.enum(['stop', 'jump']),
  else_target: z.string(),
  recipient: z.enum(['owner', 'user']),
  user_id: z.number(),
  title: z.string(),
  description: z.string(),
  priority: z.enum(['', 'low', 'medium', 'high']),
  due_in_days: numeric,
  subject: z.string(),
  body: z.string(),
});

export type StepFormValues = z.infer<typeof stepSchema>;

const codePoints = (value: string): number => [...value].length;
const utf8Bytes = (value: string): number => new TextEncoder().encode(value).length;

const firstUnknownPlaceholder = (text: string, allowed: readonly string[]): string | undefined => {
  for (const match of text.matchAll(AUTOMATION_PLACEHOLDER_PATTERN)) {
    if (!allowed.includes(match[1])) return match[1];
  }
  return undefined;
};

const sameList = (a: readonly string[], b: readonly string[]): boolean =>
  a.length === b.length && a.every((item, index) => item === b[index]);

/** True while the condition still shows the value it was loaded with. */
export const isUntouchedValue = (step: StepFormValues): boolean => {
  const original = step.original_value;
  if (original === undefined) return false;
  if (Array.isArray(original)) {
    return isListOperator(step.operator) && sameList(step.value_list, original);
  }
  return !isListOperator(step.operator) && step.value_text === step.original_text;
};

const waitMinutes = (step: StepFormValues): number =>
  step.wait_amount * MINUTES_PER_UNIT[step.wait_unit];

export const ruleFormSchema = z
  .object({
    name: z.string(),
    description: z.string(),
    entity_type: z.enum(['lead', 'deal']),
    trigger_type: z.enum(TRIGGER_TYPES),
    from: z.string(),
    to: z.string(),
    hour_utc: numeric,
    stale_days: numeric,
    filter_value: z.string(),
    enabled: z.boolean(),
    steps: z.array(stepSchema),
  })
  .superRefine((values, ctx) => {
    const issue = (path: (string | number)[], message: string) =>
      ctx.addIssue({ code: z.ZodIssueCode.custom, path, message });
    const entity = values.entity_type;

    if (!values.name.trim()) {
      issue(['name'], 'Name is required');
    } else if (codePoints(values.name) > AUTOMATION_RULE_NAME_MAX) {
      issue(['name'], 'At most 200 characters');
    }
    if (utf8Bytes(values.description) > AUTOMATION_BODY_MAX_BYTES) {
      issue(['description'], 'At most 65535 bytes');
    }

    if (!AUTOMATION_TRIGGERS[entity].includes(values.trigger_type)) {
      issue(['trigger_type'], 'Pick a trigger for this entity type');
    } else if (isChangeTrigger(values.trigger_type)) {
      for (const end of ['from', 'to'] as const) {
        if (values[end] && !entityValues(entity).includes(values[end])) {
          issue([end], 'Unknown value');
        }
      }
    } else if (values.trigger_type === 'schedule') {
      if (!Number.isInteger(values.hour_utc) || values.hour_utc < 0 || values.hour_utc > 23) {
        issue(['hour_utc'], 'Hour must be between 0 and 23');
      }
      if (!Number.isInteger(values.stale_days) || values.stale_days < 0) {
        issue(['stale_days'], 'Must be zero or more');
      }
      if (values.filter_value && !entityValues(entity).includes(values.filter_value)) {
        issue(['filter_value'], 'Unknown value');
      }
    }

    const steps = values.steps;
    if (steps.length < 1) {
      issue(['steps'], 'Add at least one step');
    } else if (steps.length > AUTOMATION_MAX_STEPS) {
      issue(['steps'], 'A rule has at most 20 steps');
    }

    let totalWait = 0;
    steps.forEach((step, index) => {
      const at = (field: keyof StepFormValues) => ['steps', index, field];

      const line = (field: 'title' | 'subject', required: string, allowed: readonly string[]) => {
        const value = step[field];
        if (!value.trim()) {
          issue(at(field), required);
        } else if (codePoints(value) > AUTOMATION_TEXT_MAX) {
          issue(at(field), 'At most 255 characters');
        } else if (/[\r\n]/.test(value)) {
          issue(at(field), 'No line breaks');
        } else {
          const unknown = firstUnknownPlaceholder(value, allowed);
          if (unknown !== undefined) issue(at(field), `Unknown placeholder {{${unknown}}}`);
        }
      };
      const text = (field: 'description' | 'body', allowed: readonly string[], required?: string) => {
        const value = step[field];
        if (required && !value.trim()) {
          issue(at(field), required);
        } else if (utf8Bytes(value) > AUTOMATION_BODY_MAX_BYTES) {
          issue(at(field), 'At most 65535 bytes');
        } else {
          const unknown = firstUnknownPlaceholder(value, allowed);
          if (unknown !== undefined) issue(at(field), `Unknown placeholder {{${unknown}}}`);
        }
      };
      const recipient = () => {
        if (step.recipient === 'user' && !(step.user_id > 0)) issue(at('user_id'), 'Pick a user');
      };

      switch (step.step_type) {
        case 'wait': {
          if (!Number.isInteger(step.wait_amount) || step.wait_amount <= 0) {
            issue(at('wait_amount'), 'Enter a positive duration');
          } else if (waitMinutes(step) > AUTOMATION_MAX_WAIT_MINUTES) {
            issue(at('wait_amount'), 'A wait is at most 90 days');
          } else {
            totalWait += waitMinutes(step);
          }
          break;
        }
        case 'condition': {
          const kind = conditionFieldKind(entity, step.field);
          if (!kind) {
            issue(at('field'), 'Pick a field');
          } else if (!operatorsFor(kind).includes(step.operator as AutomationOperator)) {
            issue(at('operator'), 'Pick an operator');
          } else if (isListOperator(step.operator)) {
            if (step.value_list.length < 1) issue(at('value_list'), 'Add at least one value');
          } else if (!isValuelessOperator(step.operator) && !isUntouchedValue(step)) {
            if (kind === 'number' && parseConditionNumber(step.value_text) === null) {
              issue(at('value_text'), 'Enter a number like 10 or 12.5');
            }
            if (kind === 'money' && parseMoneyToCents(step.value_text) === null) {
              issue(at('value_text'), 'Enter an amount like 1500 or -12.50 (at most two decimals)');
            }
          }
          if (kind === 'money' && !/^[A-Z]{3}$/.test(step.currency)) {
            issue(at('currency'), 'Three-letter currency, e.g. EUR');
          }
          if (step.else === 'jump') {
            const target = steps.findIndex((candidate) => candidate.key === step.else_target);
            if (index === steps.length - 1) {
              issue(at('else_target'), 'No later step to jump to');
            } else if (target < 0) {
              issue(at('else_target'), 'The jump target was removed; pick another step');
            } else if (target <= index) {
              issue(at('else_target'), 'Jump to a later step');
            }
          }
          break;
        }
        case 'create_task': {
          const allowed = AUTOMATION_TASK_PLACEHOLDERS[entity];
          line('title', 'Title is required', allowed);
          text('description', allowed);
          if (
            !Number.isInteger(step.due_in_days) ||
            step.due_in_days < 0 ||
            step.due_in_days > AUTOMATION_DUE_IN_DAYS_MAX
          ) {
            issue(at('due_in_days'), 'Between 0 and 365');
          }
          recipient();
          break;
        }
        case 'assign_owner':
          if (!(step.user_id > 0)) issue(at('user_id'), 'Pick a user');
          break;
        case 'send_email': {
          const allowed = AUTOMATION_PLACEHOLDERS[entity];
          line('subject', 'Subject is required', allowed);
          text('body', allowed, 'Body is required');
          recipient();
          break;
        }
        case 'notify': {
          const allowed = AUTOMATION_PLACEHOLDERS[entity];
          line('title', 'Title is required', allowed);
          text('body', allowed);
          recipient();
          break;
        }
      }
    });
    if (totalWait > AUTOMATION_MAX_WAIT_MINUTES) {
      issue(['steps'], 'The waits add up to more than 90 days');
    }
  });

export type RuleFormValues = z.infer<typeof ruleFormSchema>;

let stepCounter = 0;
/** step-1, step-2, … : stable for the life of the page, deterministic in tests. */
export const newStepKey = (): string => {
  stepCounter += 1;
  return `step-${stepCounter}`;
};

export const emptyStep = (type: AutomationStepType): StepFormValues => ({
  key: newStepKey(),
  step_type: type,
  wait_amount: 1,
  wait_unit: 'days',
  field: '',
  operator: '',
  value_text: '',
  value_list: [''],
  currency: '',
  else: 'stop',
  else_target: '',
  recipient: 'owner',
  user_id: 0,
  title: '',
  description: '',
  priority: '',
  due_in_days: 0,
  subject: '',
  body: '',
});

export const emptyFormValues = (): RuleFormValues => ({
  name: '',
  description: '',
  entity_type: 'lead',
  trigger_type: 'lead_created',
  from: '',
  to: '',
  hour_utc: 9,
  stale_days: 0,
  filter_value: '',
  enabled: false,
  steps: [],
});

const conditionValue = (
  step: StepFormValues,
  kind: AutomationFieldKind | undefined,
): AutomationStepConfig['value'] | undefined => {
  if (isValuelessOperator(step.operator)) return undefined;
  if (isUntouchedValue(step)) return step.original_value;
  if (isListOperator(step.operator)) return [...step.value_list];
  if (kind === 'number') return parseConditionNumber(step.value_text) ?? undefined;
  if (kind === 'money') return parseMoneyToCents(step.value_text) ?? undefined;
  return step.value_text;
};

const recipientConfig = (step: StepFormValues): Pick<AutomationStepConfig, 'recipient' | 'user_id'> =>
  step.recipient === 'user' ? { recipient: 'user', user_id: step.user_id } : { recipient: 'owner' };

const stepToInput = (
  step: StepFormValues,
  steps: StepFormValues[],
  entity: AutomationEntityType,
): AutomationStepInput => {
  switch (step.step_type) {
    case 'wait':
      return { step_type: 'wait', config: { duration_minutes: waitMinutes(step) } };
    case 'condition': {
      const kind = conditionFieldKind(entity, step.field);
      const config: AutomationStepConfig = {
        field: step.field,
        operator: step.operator as AutomationOperator,
      };
      const value = conditionValue(step, kind);
      if (value !== undefined) config.value = value;
      if (kind === 'money') config.currency = step.currency;
      config.else = step.else;
      if (step.else === 'jump') {
        config.else_position = steps.findIndex((candidate) => candidate.key === step.else_target) + 1;
      }
      return { step_type: 'condition', config };
    }
    case 'create_task': {
      const config: AutomationStepConfig = { title: step.title };
      if (step.description) config.description = step.description;
      if (step.priority) config.priority = step.priority;
      config.due_in_days = step.due_in_days;
      return { step_type: 'create_task', config: { ...config, ...recipientConfig(step) } };
    }
    case 'assign_owner':
      return { step_type: 'assign_owner', config: { user_id: step.user_id } };
    case 'send_email':
      return {
        step_type: 'send_email',
        config: { subject: step.subject, body: step.body, ...recipientConfig(step) },
      };
    case 'notify': {
      const config: AutomationStepConfig = { title: step.title };
      if (step.body) config.body = step.body;
      return { step_type: 'notify', config: { ...config, ...recipientConfig(step) } };
    }
  }
  return { step_type: step.step_type, config: {} };
};

const triggerToInput = (values: RuleFormValues): Partial<AutomationTrigger> => {
  if (isChangeTrigger(values.trigger_type)) {
    const trigger: Partial<AutomationTrigger> = {};
    if (values.from) trigger.from = values.from;
    if (values.to) trigger.to = values.to;
    return trigger;
  }
  if (values.trigger_type === 'schedule') {
    const trigger: Partial<AutomationTrigger> = { hour_utc: values.hour_utc };
    if (values.stale_days > 0) trigger.stale_days = values.stale_days;
    if (values.filter_value) {
      trigger.filters =
        values.entity_type === 'lead' ? { status: values.filter_value } : { stage: values.filter_value };
    }
    return trigger;
  }
  return {};
};

/**
 * The POST/PUT body. `enabled` is sent on create only: on edit the list's
 * switch owns it, and the server keeps the stored switch when it is absent.
 */
export const toRulePayload = (values: RuleFormValues, mode: 'create' | 'edit'): AutomationRuleInput => {
  const payload: AutomationRuleInput = {
    name: values.name.trim(),
    description: values.description,
    entity_type: values.entity_type,
    trigger_type: values.trigger_type,
    trigger: triggerToInput(values),
    steps: values.steps.map((step, _index, steps) => stepToInput(step, steps, values.entity_type)),
  };
  if (mode === 'create') payload.enabled = values.enabled;
  return payload;
};

const waitFromMinutes = (minutes: number): Pick<StepFormValues, 'wait_amount' | 'wait_unit'> => {
  if (minutes > 0 && minutes % 1440 === 0) return { wait_amount: minutes / 1440, wait_unit: 'days' };
  if (minutes > 0 && minutes % 60 === 0) return { wait_amount: minutes / 60, wait_unit: 'hours' };
  return { wait_amount: minutes, wait_unit: 'minutes' };
};

const conditionFromConfig = (
  config: AutomationStepConfig,
  entity: AutomationEntityType,
): Partial<StepFormValues> => {
  const value = config.value;
  const kind = conditionFieldKind(entity, config.field ?? '');
  if (Array.isArray(value)) {
    return { value_list: [...value], original_value: [...value] };
  }
  if (typeof value === 'number') {
    let text = String(value);
    if (kind === 'money') text = Number.isInteger(value) ? centsToMoneyText(value) : String(value / 100);
    return { value_text: text, original_value: value, original_text: text };
  }
  if (typeof value === 'string') {
    return { value_text: value, original_value: value, original_text: value };
  }
  return {};
};

const stepFromRule = (step: AutomationStep, entity: AutomationEntityType): StepFormValues => {
  const config = step.config ?? {};
  const base = emptyStep(step.step_type);
  switch (step.step_type) {
    case 'wait':
      return { ...base, ...waitFromMinutes(config.duration_minutes ?? 0) };
    case 'condition':
      return {
        ...base,
        field: config.field ?? '',
        operator: config.operator ?? '',
        currency: config.currency ?? '',
        else: config.else === 'jump' ? 'jump' : 'stop',
        ...conditionFromConfig(config, entity),
      };
    default:
      return {
        ...base,
        recipient: step.step_type === 'assign_owner' ? 'user' : config.recipient === 'user' ? 'user' : 'owner',
        user_id: config.user_id ?? 0,
        title: config.title ?? '',
        description: config.description ?? '',
        priority: config.priority ?? '',
        due_in_days: config.due_in_days ?? 0,
        subject: config.subject ?? '',
        body: config.body ?? '',
      };
  }
};

/** The form values of a stored rule, with fresh step keys. */
export const fromRule = (rule: AutomationRule): RuleFormValues => {
  const ordered = [...rule.steps].sort((a, b) => a.position - b.position);
  const steps = ordered.map((step) => stepFromRule(step, rule.entity_type));
  ordered.forEach((step, index) => {
    const position = step.config?.else_position;
    if (step.step_type === 'condition' && step.config?.else === 'jump' && position) {
      steps[index].else_target = steps[position - 1]?.key ?? '';
    }
  });
  const trigger = rule.trigger ?? { hour_utc: 0 };
  const schedule = rule.trigger_type === 'schedule';
  return {
    name: rule.name,
    description: rule.description ?? '',
    entity_type: rule.entity_type,
    trigger_type: rule.trigger_type,
    from: trigger.from ?? '',
    to: trigger.to ?? '',
    hour_utc: schedule ? trigger.hour_utc ?? 0 : 9,
    stale_days: trigger.stale_days ?? 0,
    filter_value: trigger.filters?.status ?? trigger.filters?.stage ?? '',
    enabled: rule.enabled,
    steps,
  };
};

const STEP_ERROR_PATTERN = /^step (\d+): (.*?)(?:: validation failed)?$/s;
const SENTINEL_SUFFIX = /: (?:validation failed|assignee not found)$/;

/** Splits a server validation message into the step it names and its text. */
export const parseStepError = (message: string): { step?: number; text: string } => {
  const match = STEP_ERROR_PATTERN.exec(message);
  if (match) return { step: Number(match[1]), text: match[2] };
  return { text: message.replace(SENTINEL_SUFFIX, '') };
};
