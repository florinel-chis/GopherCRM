import { describe, it, expect } from 'vitest';
import {
  AUTOMATION_CONDITION_FIELDS,
  AUTOMATION_ENTITY_TYPES,
  AUTOMATION_PLACEHOLDERS,
  AUTOMATION_TASK_PLACEHOLDERS,
  AUTOMATION_TICKET_PRIORITIES,
  AUTOMATION_TICKET_STATUSES,
  AUTOMATION_TRIGGERS,
  isChangeTrigger,
  type AutomationStepType,
} from '@/types';
import { createMockAutomationRule } from '@/test/factories';
import {
  conditionSuggestions,
  emptyFormValues,
  emptyStep,
  entityValues,
  entityValueWord,
  fromRule,
  mapServerFieldErrors,
  ruleFormSchema,
  toRulePayload,
  type RuleFormValues,
  type StepFormValues,
} from './ruleFormSchema';

const step = (type: AutomationStepType, overrides: Partial<StepFormValues> = {}): StepFormValues => ({
  ...emptyStep(type),
  ...overrides,
});

const notify = (title = 'Ticket {{ticket.number}}') => step('notify', { title });

const ticketForm = (overrides: Partial<RuleFormValues> = {}): RuleFormValues => ({
  ...emptyFormValues(),
  name: 'Ticket rule',
  entity_type: 'ticket',
  trigger_type: 'ticket_created',
  steps: [notify()],
  ...overrides,
});

const form = (overrides: Partial<RuleFormValues> = {}): RuleFormValues => ({
  ...emptyFormValues(),
  name: 'Rule',
  steps: [step('notify', { title: 'Heads up' })],
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
  describe('ticket rules', () => {
    it('ticket allowlists equal the API sets', () => {
      // internal/service/automation_validation.go: entity types and ticket triggers.
      expect(AUTOMATION_ENTITY_TYPES).toContain('ticket');
      expect(AUTOMATION_TRIGGERS.ticket).toEqual([
        'ticket_created',
        'ticket_updated',
        'ticket_status_changed',
        'schedule',
      ]);
      expect(isChangeTrigger('ticket_status_changed')).toBe(true);
      expect(isChangeTrigger('ticket_created')).toBe(false);
      expect(isChangeTrigger('ticket_updated')).toBe(false);

      // internal/service/automation_condition.go: condition fields and placeholders.
      expect(AUTOMATION_CONDITION_FIELDS.ticket).toEqual({
        status: 'string',
        priority: 'string',
        owner_id: 'number',
        customer_id: 'number',
      });
      expect(AUTOMATION_PLACEHOLDERS.ticket).toEqual([
        'ticket.id',
        'ticket.number',
        'ticket.status',
        'ticket.priority',
        'owner.first_name',
        'owner.last_name',
        'owner.email',
      ]);
      expect(AUTOMATION_TASK_PLACEHOLDERS.ticket).toEqual([
        'ticket.id',
        'ticket.number',
        'ticket.status',
        'ticket.priority',
      ]);

      // internal/models/ticket.go: TicketStatus and TicketPriority.
      expect(AUTOMATION_TICKET_STATUSES).toEqual(['open', 'in_progress', 'resolved', 'closed']);
      expect(AUTOMATION_TICKET_PRIORITIES).toEqual(['low', 'medium', 'high', 'urgent']);
      expect(entityValues('ticket')).toEqual(['open', 'in_progress', 'resolved', 'closed']);
      expect(entityValueWord('ticket')).toBe('status');
      expect(conditionSuggestions('ticket', 'status')).toEqual(['open', 'in_progress', 'resolved', 'closed']);
      expect(conditionSuggestions('ticket', 'priority')).toEqual(['low', 'medium', 'high', 'urgent']);
      expect(conditionSuggestions('ticket', 'owner_id')).toBeNull();

      // The form schema accepts a ticket rule on each ticket trigger.
      for (const trigger_type of AUTOMATION_TRIGGERS.ticket) {
        expect(issues(ticketForm({ trigger_type }))).toEqual({});
      }
    });

    it('a ticket schedule filter is sent as filters.ticket_status', () => {
      const values = ticketForm({ trigger_type: 'schedule', hour_utc: 8, filter_value: 'in_progress' });
      expect(issues(values)).toEqual({});
      const payload = toRulePayload(values, 'create');
      expect(payload.entity_type).toBe('ticket');
      expect(payload.trigger).toEqual({ hour_utc: 8, filters: { ticket_status: 'in_progress' } });
      expect(payload.trigger.filters).toEqual({ ticket_status: 'in_progress' });
      expect(payload.trigger.filters).not.toHaveProperty('status');
      expect(payload.trigger.filters).not.toHaveProperty('stage');

      // A status change carries the ticket statuses as from/to.
      const change = toRulePayload(
        ticketForm({ trigger_type: 'ticket_status_changed', from: 'open', to: 'resolved' }),
        'create',
      );
      expect(change.trigger).toEqual({ from: 'open', to: 'resolved' });
    });

    it('a ticket rule refuses deal stages, lead placeholders and ticket text placeholders', () => {
      // A deal stage or a lead status as from / to / filter.
      expect(issues(ticketForm({ trigger_type: 'ticket_status_changed', from: 'proposal', to: 'contacted' }))).toEqual({
        from: ['Unknown value'],
        to: ['Unknown value'],
      });
      expect(issues(ticketForm({ trigger_type: 'schedule', filter_value: 'qualification' }))).toEqual({
        filter_value: ['Unknown value'],
      });
      expect(issues(ticketForm({ trigger_type: 'schedule', filter_value: 'new' }))).toEqual({
        filter_value: ['Unknown value'],
      });

      // Lead placeholders and the ticket's free text in a notify title.
      expect(issues(ticketForm({ steps: [notify('Hi {{lead.email}}')] }))).toEqual({
        'steps.0.title': ['Unknown placeholder {{lead.email}}'],
      });
      expect(issues(ticketForm({ steps: [notify('About {{ticket.title}}')] }))).toEqual({
        'steps.0.title': ['Unknown placeholder {{ticket.title}}'],
      });

      // A personal placeholder in a task title.
      expect(issues(ticketForm({ steps: [step('create_task', { title: 'Call {{owner.email}}' })] }))).toEqual({
        'steps.0.title': ['Unknown placeholder {{owner.email}}'],
      });

      // Condition fields outside the ticket allowlist.
      for (const field of ['title', 'stage']) {
        expect(issues(ticketForm({ steps: [step('condition', { field, operator: 'eq', value_text: 'x' })] }))).toEqual({
          'steps.0.field': ['Pick a field'],
        });
      }

      // The same rule with ticket values passes.
      expect(
        issues(ticketForm({ trigger_type: 'ticket_status_changed', from: 'open', to: 'resolved' })),
      ).toEqual({});
      expect(issues(ticketForm({ trigger_type: 'schedule', filter_value: 'in_progress' }))).toEqual({});
      expect(
        issues(
          ticketForm({
            steps: [
              step('condition', { field: 'priority', operator: 'eq', value_text: 'urgent' }),
              step('condition', { field: 'customer_id', operator: 'eq', value_text: '7' }),
              step('create_task', { title: 'Follow up {{ticket.number}} ({{ticket.priority}})' }),
              notify('{{ticket.number}} is {{ticket.status}} for {{owner.first_name}} {{owner.email}}'),
            ],
          }),
        ),
      ).toEqual({});
    });

    it('fromRule reads filters.ticket_status back into the filter', () => {
      const rule = createMockAutomationRule({
        entity_type: 'ticket',
        trigger_type: 'schedule',
        trigger: { hour_utc: 7, stale_days: 2, filters: { ticket_status: 'resolved' } },
        steps: [
          {
            id: 1,
            rule_id: 1,
            position: 1,
            step_type: 'notify',
            created_at: '2026-11-13T08:00:00Z',
            config: { title: 'Resolved {{ticket.number}}', recipient: 'owner' },
          },
        ],
      });
      const values = fromRule(rule);
      expect(values.entity_type).toBe('ticket');
      expect(values.trigger_type).toBe('schedule');
      expect(values.filter_value).toBe('resolved');
      expect(values.hour_utc).toBe(7);
      expect(values.stale_days).toBe(2);
      expect(issues(values)).toEqual({});

      const payload = toRulePayload(values, 'edit');
      expect(payload.trigger).toEqual({ hour_utc: 7, stale_days: 2, filters: { ticket_status: 'resolved' } });
      expect(payload.steps).toEqual([
        { step_type: 'notify', config: { title: 'Resolved {{ticket.number}}', recipient: 'owner' } },
      ]);
    });

    it('a server error on trigger.filters.ticket_status maps to the filter field', () => {
      const values = ticketForm({ trigger_type: 'schedule', filter_value: 'in_progress' });
      const mapped = mapServerFieldErrors(
        [
          {
            field: 'trigger.filters.ticket_status',
            code: 'invalid_value',
            message: 'filters.ticket_status "in_progress" is not a ticket status',
          },
          { field: 'entity_type', code: 'invalid_value', message: 'entity_type must be lead, deal or ticket' },
        ],
        values,
        structuredClone(values),
      );
      expect(mapped.fields).toEqual([
        { name: 'filter_value', message: 'filters.ticket_status "in_progress" is not a ticket status' },
      ]);
      expect(mapped.unmapped).toEqual([{ text: 'entity_type must be lead, deal or ticket' }]);
    });
  });

  describe('lead and deal rules unchanged', () => {
    it('lead and deal schedule filters are still sent as status and stage', () => {
      const lead = form({ trigger_type: 'schedule', hour_utc: 8, filter_value: 'contacted' });
      expect(issues(lead)).toEqual({});
      expect(toRulePayload(lead, 'create').trigger).toEqual({ hour_utc: 8, filters: { status: 'contacted' } });

      const deal = form({ entity_type: 'deal', trigger_type: 'schedule', hour_utc: 8, filter_value: 'proposal' });
      expect(issues(deal)).toEqual({});
      expect(toRulePayload(deal, 'create').trigger).toEqual({ hour_utc: 8, filters: { stage: 'proposal' } });

      const leadChange = form({ trigger_type: 'lead_status_changed', from: 'new', to: 'qualified' });
      expect(issues(leadChange)).toEqual({});
      expect(toRulePayload(leadChange, 'create').trigger).toEqual({ from: 'new', to: 'qualified' });

      const dealChange = form({
        entity_type: 'deal',
        trigger_type: 'deal_stage_changed',
        from: 'qualification',
        to: 'proposal',
      });
      expect(issues(dealChange)).toEqual({});
      expect(toRulePayload(dealChange, 'create').trigger).toEqual({ from: 'qualification', to: 'proposal' });

      // Ticket statuses are not lead statuses or deal stages.
      expect(issues(form({ trigger_type: 'lead_status_changed', to: 'resolved' }))).toEqual({ to: ['Unknown value'] });
      expect(issues(form({ entity_type: 'deal', trigger_type: 'schedule', filter_value: 'open' }))).toEqual({
        filter_value: ['Unknown value'],
      });
      expect(entityValueWord('lead')).toBe('status');
      expect(entityValueWord('deal')).toBe('stage');
    });

    it('fromRule still reads lead status and deal stage filters', () => {
      const lead = fromRule(
        createMockAutomationRule({ trigger_type: 'schedule', trigger: { hour_utc: 6, filters: { status: 'contacted' } } }),
      );
      expect(lead.entity_type).toBe('lead');
      expect(lead.filter_value).toBe('contacted');
      expect(toRulePayload(lead, 'edit').trigger).toEqual({ hour_utc: 6, filters: { status: 'contacted' } });

      const deal = fromRule(
        createMockAutomationRule({
          entity_type: 'deal',
          trigger_type: 'schedule',
          trigger: { hour_utc: 6, filters: { stage: 'proposal' } },
          steps: [],
        }),
      );
      expect(deal.entity_type).toBe('deal');
      expect(deal.filter_value).toBe('proposal');
      expect(toRulePayload(deal, 'edit').trigger).toEqual({ hour_utc: 6, filters: { stage: 'proposal' } });
    });
  });
});
