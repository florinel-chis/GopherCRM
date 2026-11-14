import type { Notification, User } from '@/types';

// Where a notification leads. Support cannot open leads or deals (the API
// answers 403) but works tickets, so support gets a link for a ticket
// notification only. Customers never receive notifications, so they get no
// link; a click then only marks the notification read.
export const notificationTarget = (
  n: Pick<Notification, 'entity_type' | 'entity_id'>,
  role: User['role'] | undefined,
): string | null => {
  if (!Number.isInteger(n.entity_id) || n.entity_id <= 0) return null;
  if (n.entity_type === 'ticket') {
    return role === 'admin' || role === 'sales' || role === 'support' ? `/tickets/${n.entity_id}` : null;
  }
  if (role !== 'admin' && role !== 'sales') return null;
  if (n.entity_type === 'lead') return `/leads/${n.entity_id}`;
  if (n.entity_type === 'deal') return `/deals/${n.entity_id}`;
  return null;
};
