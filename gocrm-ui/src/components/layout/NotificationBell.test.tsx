import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@/test/test-utils';
import { focusManager } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { NotificationBell } from './NotificationBell';
import { notificationsApi } from '@/api/endpoints';
import { createMockNotification, createMockUser } from '@/test/factories';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints/notifications', () => ({
  notificationsApi: {
    list: vi.fn(),
    unreadCount: vi.fn(),
    markRead: vi.fn(),
    markAllRead: vi.fn(),
  },
}));

const mockShowSuccess = vi.fn();
const mockShowError = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: mockShowSuccess, showError: mockShowError }),
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (user: User | null) => ({
  user,
  isLoading: false,
  isAuthenticated: user !== null,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

// Every test runs under the application's own cache defaults (five-minute
// staleTime, no focus refetch): the bell must override them itself.
const renderBell = () => render(withClient(createProductionQueryClient(), <NotificationBell />));

const unread = createMockNotification({
  id: 11,
  title: 'Call Jane back',
  body: 'Lead #5 asked for a call',
  entity_type: 'lead',
  entity_id: 5,
});
const read = createMockNotification({
  id: 12,
  title: 'Deal moved',
  body: 'Deal #9 reached proposal',
  entity_type: 'deal',
  entity_id: 9,
  read_at: '2026-10-28T10:00:00Z',
});

const openMenu = async () => {
  fireEvent.click(await screen.findByTestId('notification-bell'));
  return screen.findByRole('menu', { name: 'Notifications' });
};

describe('NotificationBell', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'admin' })));
    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(3);
    vi.mocked(notificationsApi.list).mockResolvedValue({ notifications: [unread, read], total: 2 });
    vi.mocked(notificationsApi.markRead).mockResolvedValue({ ...unread, read_at: '2026-10-29T09:00:00Z' });
    vi.mocked(notificationsApi.markAllRead).mockResolvedValue(3);
  });

  afterEach(() => {
    // Back to the document-driven focus state and real timers.
    focusManager.setFocused(undefined);
    vi.useRealTimers();
  });

  it('shows the unread count badge for admin, sales and support', async () => {
    for (const role of ['admin', 'sales', 'support'] as const) {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));
      const view = renderBell();

      const bell = await screen.findByTestId('notification-bell');
      await waitFor(() => expect(bell).toHaveAttribute('data-unread', '3'));
      expect(bell).toHaveAccessibleName('Notifications (3 unread)');
      expect(within(bell).getByText('3')).toBeInTheDocument();
      view.unmount();
    }
    expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(3);
    // The menu list is only fetched once the menu opens.
    expect(notificationsApi.list).not.toHaveBeenCalled();
  });

  it('renders no bell and requests nothing for the customer role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'customer' })));
    const { container } = renderBell();

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(screen.queryByTestId('notification-bell')).not.toBeInTheDocument();
    expect(container).toBeEmptyDOMElement();
    expect(notificationsApi.unreadCount).not.toHaveBeenCalled();
    expect(notificationsApi.list).not.toHaveBeenCalled();
  });

  it('renders no bell and requests nothing when logged out', async () => {
    mockUseAuth.mockReturnValue(authState(null));
    const { container } = renderBell();

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(screen.queryByTestId('notification-bell')).not.toBeInTheDocument();
    expect(container).toBeEmptyDOMElement();
    expect(notificationsApi.unreadCount).not.toHaveBeenCalled();
    expect(notificationsApi.list).not.toHaveBeenCalled();
  });

  it('opens the latest 10 notifications and marks one read on click', async () => {
    renderBell();
    await waitFor(() => expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(1));

    const menu = await openMenu();
    expect(notificationsApi.list).toHaveBeenCalledWith({ limit: 10 });
    const items = await within(menu).findAllByTestId('bell-notification');
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveAttribute('data-read', 'false');
    expect(items[1]).toHaveAttribute('data-read', 'true');
    expect(within(items[0]).getByText('Call Jane back')).toBeInTheDocument();
    expect(within(items[0]).getByText('Lead #5 asked for a call')).toBeInTheDocument();

    fireEvent.click(items[0]);

    await waitFor(() => expect(notificationsApi.markRead).toHaveBeenCalledWith(11));
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/leads/5'));
    // The invalidation refetches the count the badge shows.
    await waitFor(() => expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.queryByRole('menu', { name: 'Notifications' })).not.toBeInTheDocument(),
    );
  });

  it('marks all read from the menu', async () => {
    renderBell();
    const menu = await openMenu();
    await within(menu).findAllByTestId('bell-notification');
    const markAll = within(menu).getByRole('menuitem', { name: 'Mark all read' });
    await waitFor(() => expect(markAll).not.toHaveAttribute('aria-disabled', 'true'));

    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(0);
    fireEvent.click(markAll);

    await waitFor(() => expect(notificationsApi.markAllRead).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mockShowSuccess).toHaveBeenCalledWith('Marked 3 notifications read'));
    await waitFor(() =>
      expect(screen.getByTestId('notification-bell')).toHaveAttribute('data-unread', '0'),
    );
  });

  it('refetches the unread count at 60,000 ms and not at 59,999 ms', async () => {
    // No shouldAdvanceTime: the real 20 ms ticker would move the clock at a
    // random phase and make a one-millisecond boundary nondeterministic. The
    // clock moves only when the test advances it.
    vi.useFakeTimers();
    renderBell();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('notification-bell')).toHaveAttribute('data-unread', '3');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(59_999);
    });
    expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(2);
  });

  it('refetches the unread count when the window regains focus', async () => {
    renderBell();
    await waitFor(() =>
      expect(screen.getByTestId('notification-bell')).toHaveAttribute('data-unread', '3'),
    );
    expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(1);

    act(() => {
      focusManager.setFocused(false);
    });
    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(4);
    act(() => {
      focusManager.setFocused(true);
    });

    await waitFor(() => expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.getByTestId('notification-bell')).toHaveAttribute('data-unread', '4'),
    );
  });

  it('fetches the latest notifications again when the menu is reopened within five minutes', async () => {
    const a = createMockNotification({ id: 21, title: 'Notification A' });
    const b = createMockNotification({ id: 22, title: 'Notification B' });
    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(1);
    vi.mocked(notificationsApi.list).mockResolvedValue({ notifications: [a], total: 1 });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderBell();

    let menu = await openMenu();
    expect(await within(menu).findByText('Notification A')).toBeInTheDocument();
    fireEvent.keyDown(menu, { key: 'Escape' });
    await waitFor(() =>
      expect(screen.queryByRole('menu', { name: 'Notifications' })).not.toBeInTheDocument(),
    );

    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(2);
    vi.mocked(notificationsApi.list).mockResolvedValue({ notifications: [b, a], total: 2 });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    await waitFor(() =>
      expect(screen.getByTestId('notification-bell')).toHaveAttribute('data-unread', '2'),
    );

    menu = await openMenu();
    expect(await within(menu).findByText('Notification B')).toBeInTheDocument();
    expect(within(menu).getByText('Notification A')).toBeInTheDocument();
    expect(notificationsApi.list).toHaveBeenCalledTimes(2);
  });

  it('fetches the list again while the menu is open when the polled count changes', async () => {
    const a = createMockNotification({ id: 21, title: 'Notification A' });
    const b = createMockNotification({ id: 22, title: 'Notification B' });
    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(1);
    vi.mocked(notificationsApi.list).mockResolvedValue({ notifications: [a], total: 1 });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderBell();

    const menu = await openMenu();
    expect(await within(menu).findByText('Notification A')).toBeInTheDocument();
    expect(notificationsApi.list).toHaveBeenCalledTimes(1);

    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(2);
    vi.mocked(notificationsApi.list).mockResolvedValue({ notifications: [b, a], total: 2 });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });

    expect(await within(menu).findByText('Notification B')).toBeInTheDocument();
    expect(notificationsApi.list).toHaveBeenCalledTimes(2);
  });

  it('shows the server message and a Retry action when the menu list fails, not the empty state', async () => {
    vi.mocked(notificationsApi.list).mockRejectedValue(
      Object.assign(new Error('Request failed with status code 500'), {
        response: { status: 500, data: { code: 'INTERNAL_ERROR', message: 'Database unavailable' } },
      }),
    );
    renderBell();
    const menu = await openMenu();

    expect(await within(menu).findByText('Database unavailable')).toBeInTheDocument();
    expect(within(menu).queryByText('No notifications')).not.toBeInTheDocument();
    expect(within(menu).queryAllByTestId('bell-notification')).toHaveLength(0);

    vi.mocked(notificationsApi.list).mockResolvedValue({ notifications: [unread], total: 1 });
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Retry' }));

    expect(await within(menu).findByText('Call Jane back')).toBeInTheDocument();
    expect(within(menu).queryByText('Database unavailable')).not.toBeInTheDocument();
    expect(notificationsApi.list).toHaveBeenCalledTimes(2);
  });

  it('shows no badge while the unread count has never loaded and keeps the last known count when a refetch fails', async () => {
    // Refetches are driven by window focus here: real timers keep React
    // Query's error-then-success notification path intact.
    vi.mocked(notificationsApi.unreadCount).mockRejectedValueOnce(new Error('Network Error'));
    renderBell();
    await waitFor(() => expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(1));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    const bell = screen.getByTestId('notification-bell');
    // No count yet: no "0 unread" claim, no badge.
    expect(bell).not.toHaveAttribute('data-unread');
    expect(bell).toHaveAccessibleName('Notifications');
    expect(within(bell).queryByText('0')).not.toBeInTheDocument();

    // The next refetch succeeds.
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => expect(bell).toHaveAttribute('data-unread', '3'));
    expect(bell).toHaveAccessibleName('Notifications (3 unread)');

    // A failed refetch keeps the last known count instead of dropping to zero.
    vi.mocked(notificationsApi.unreadCount).mockRejectedValueOnce(new Error('Network Error'));
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    await waitFor(() => expect(notificationsApi.unreadCount).toHaveBeenCalledTimes(3));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(bell).toHaveAttribute('data-unread', '3');
    expect(within(bell).getByText('3')).toBeInTheDocument();
  });

  it('links to the notifications page', async () => {
    renderBell();
    const menu = await openMenu();

    const viewAll = within(menu).getByRole('menuitem', { name: 'View all' });
    expect(viewAll).toHaveAttribute('href', '/notifications');
    fireEvent.click(viewAll);
    await waitFor(() =>
      expect(screen.queryByRole('menu', { name: 'Notifications' })).not.toBeInTheDocument(),
    );
  });
});
