import { describe, it, expect } from 'vitest';
import {
  describeTrigger,
  entityLabel,
  entityPath,
  entityTypeLabel,
  ticketNumber,
  ticketStatusLabel,
  triggerTypeLabel,
} from './automationFormat';

describe('automationFormat ticket labels', () => {
  it('ticketNumber formats ids like models.TicketNumber', () => {
    // models.TicketNumber: fmt.Sprintf("T-%06d", id)
    expect(ticketNumber(42)).toBe('T-000042');
    expect(ticketNumber(1)).toBe('T-000001');
    expect(ticketNumber(123456)).toBe('T-123456');
    expect(ticketNumber(1234567)).toBe('T-1234567');
  });

  it('ticket triggers, entity labels, paths and schedule filters are described', () => {
    expect(triggerTypeLabel('ticket_created')).toBe('Ticket created');
    expect(triggerTypeLabel('ticket_updated')).toBe('Ticket updated');
    expect(triggerTypeLabel('ticket_status_changed')).toBe('Ticket status changed');

    expect(entityTypeLabel('ticket')).toBe('Ticket');
    expect(entityLabel('ticket', 5)).toBe('Ticket #5');
    expect(entityPath('ticket', 5)).toBe('/tickets/5');
    expect(ticketStatusLabel('in_progress')).toBe('In progress');

    expect(
      describeTrigger({
        trigger_type: 'schedule',
        trigger: { hour_utc: 8, filters: { ticket_status: 'in_progress' }, stale_days: 3 },
      }),
    ).toBe('Schedule, daily at 08:00 UTC, status in_progress, not updated for 3 days');
    expect(
      describeTrigger({ trigger_type: 'ticket_status_changed', trigger: { hour_utc: 0, from: 'open', to: 'resolved' } }),
    ).toBe('Ticket status changed: open → resolved');

    // Lead and deal labels are unchanged.
    expect(entityLabel('lead', 1)).toBe('Lead #1');
    expect(entityPath('deal', 2)).toBe('/deals/2');
    expect(
      describeTrigger({ trigger_type: 'schedule', trigger: { hour_utc: 9, filters: { stage: 'proposal' } } }),
    ).toBe('Schedule, daily at 09:00 UTC, stage proposal');
  });
});
