import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import type { Notification } from '@/types';

// `unread` narrows the list to unread notifications; it is only sent when true.
export interface NotificationFilters {
  offset?: number;
  limit?: number;
  unread?: boolean;
}

export interface NotificationListResult {
  notifications: Notification[];
  total: number;
}

// Every route answers for the caller's own notifications only (admin, sales,
// support); someone else's id is a 404.
export const notificationsApi = {
  // Newest first.
  list: async (filters?: NotificationFilters): Promise<NotificationListResult> => {
    const { unread, ...rest } = filters ?? {};
    const params = unread ? { ...rest, unread: true } : rest;
    const response = (await api.get<Notification[]>('/notifications', {
      params,
    })) as ApiResponseWithMeta<Notification[]>;
    const notifications = Array.isArray(response.data) ? response.data : [];
    return { notifications, total: response.meta?.total ?? notifications.length };
  },

  unreadCount: async (): Promise<number> => {
    const response = await api.get<{ unread?: number }>('/notifications/unread-count');
    return response.data?.unread ?? 0;
  },

  // Idempotent: an already read notification keeps its read_at.
  markRead: async (id: number): Promise<Notification> => {
    const response = await api.post<Notification>(`/notifications/${id}/read`);
    return response.data;
  },

  markAllRead: async (): Promise<number> => {
    const response = await api.post<{ marked?: number }>('/notifications/read-all');
    return response.data?.marked ?? 0;
  },
};
