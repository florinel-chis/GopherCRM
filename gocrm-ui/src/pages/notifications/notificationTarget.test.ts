import { describe, it, expect } from 'vitest';
import { notificationTarget } from './notificationTarget';
import { createMockNotification } from '@/test/factories';

describe('notificationTarget', () => {
  it('maps lead and deal notifications to their detail pages for admin and sales', () => {
    const lead = createMockNotification({ entity_type: 'lead', entity_id: 5 });
    const deal = createMockNotification({ entity_type: 'deal', entity_id: 9 });

    for (const role of ['admin', 'sales'] as const) {
      expect(notificationTarget(lead, role)).toBe('/leads/5');
      expect(notificationTarget(deal, role)).toBe('/deals/9');
    }
  });

  it('returns null for support and customer', () => {
    const lead = createMockNotification({ entity_type: 'lead', entity_id: 5 });
    const deal = createMockNotification({ entity_type: 'deal', entity_id: 9 });

    for (const role of ['support', 'customer'] as const) {
      expect(notificationTarget(lead, role)).toBeNull();
      expect(notificationTarget(deal, role)).toBeNull();
    }
    expect(notificationTarget(lead, undefined)).toBeNull();
  });
});
