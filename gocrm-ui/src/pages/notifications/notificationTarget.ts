import type { Notification } from '@/types';

// Where a notification leads: the page of its ticket, lead or deal, for a
// caller that may read that kind of record (the API would answer 403 to
// anyone else). Without the key there is no link; a click then only marks
// the notification read.
export const notificationTarget = (
  n: Pick<Notification, 'entity_type' | 'entity_id'>,
  can: (key: string) => boolean,
): string | null => {
  if (!Number.isInteger(n.entity_id) || n.entity_id <= 0) return null;
  if (n.entity_type === 'ticket') return can('tickets.view') ? `/tickets/${n.entity_id}` : null;
  if (n.entity_type === 'lead') return can('leads.view') ? `/leads/${n.entity_id}` : null;
  if (n.entity_type === 'deal') return can('deals.view') ? `/deals/${n.entity_id}` : null;
  return null;
};
