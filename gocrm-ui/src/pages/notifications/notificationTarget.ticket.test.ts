import { describe, it, expect } from 'vitest';
import { notificationTarget } from './notificationTarget';
import { createMockNotification } from '@/test/factories';

describe('notificationTarget ticket notifications', () => {
  it('a ticket notification links to its ticket', () => {
    const ticket = createMockNotification({ entity_type: 'ticket', entity_id: 42 });
    for (const role of ['admin', 'sales', 'support'] as const) {
      expect(notificationTarget(ticket, role)).toBe('/tickets/42');
    }
    expect(notificationTarget(ticket, 'customer')).toBeNull();
    expect(notificationTarget(ticket, undefined)).toBeNull();
    expect(notificationTarget(createMockNotification({ entity_type: 'ticket', entity_id: 0 }), 'admin')).toBeNull();

    // Lead and deal targets are unchanged.
    const lead = createMockNotification({ entity_type: 'lead', entity_id: 5 });
    const deal = createMockNotification({ entity_type: 'deal', entity_id: 9 });
    for (const role of ['admin', 'sales'] as const) {
      expect(notificationTarget(lead, role)).toBe('/leads/5');
      expect(notificationTarget(deal, role)).toBe('/deals/9');
    }
    expect(notificationTarget(lead, 'support')).toBeNull();
    expect(notificationTarget(deal, 'support')).toBeNull();
  });
});
