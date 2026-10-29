import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { MainLayout } from './MainLayout';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();
vi.mock('@/components/layout/NotificationBell', () => ({
  NotificationBell: () => <div data-testid="notification-bell" />,
}));

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

// The permanent drawer is the navigation a desktop viewer sees; the temporary
// one is kept mounted for mobile and would double every entry. Entries are
// looked up by text: jsdom applies the drawer's `xs` display rule and ignores
// the `sm` media query, so role queries would report the entries hidden.
const navigation = () => within(screen.getByTestId('app-navigation'));

describe('MainLayout', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('shows the Automation group for admin and sales and hides it for support and customer', async () => {
    for (const role of ['admin', 'sales'] as const) {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));
      const view = render(<MainLayout />);

      await userEvent.click(navigation().getByText('Automation'));
      expect(navigation().getByText('Runs')).toBeInTheDocument();
      // Rules are admin-only on the API.
      if (role === 'admin') {
        expect(navigation().getByText('Rules')).toBeInTheDocument();
      } else {
        expect(navigation().queryByText('Rules')).not.toBeInTheDocument();
      }
      view.unmount();
    }

    for (const role of ['support', 'customer'] as const) {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));
      const view = render(<MainLayout />);

      expect(navigation().queryByText('Automation')).not.toBeInTheDocument();
      view.unmount();
    }
  });

  it('renders the notification bell slot in the app bar', () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'admin' })));
    render(<MainLayout />);

    expect(screen.getByTestId('notification-bell')).toBeInTheDocument();
  });

  it('shows the Products and Quotes navigation entries for admin and sales and hides them for support and customer', () => {
    for (const role of ['admin', 'sales'] as const) {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));
      const view = render(<MainLayout />);

      expect(navigation().getByText('Products')).toBeInTheDocument();
      expect(navigation().getByText('Quotes')).toBeInTheDocument();
      // The entries sit with the other sales items, next to Deals.
      expect(navigation().getByText('Deals')).toBeInTheDocument();
      view.unmount();
    }

    for (const role of ['support', 'customer'] as const) {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));
      const view = render(<MainLayout />);

      expect(navigation().queryByText('Products')).not.toBeInTheDocument();
      expect(navigation().queryByText('Quotes')).not.toBeInTheDocument();
      // The shell itself still renders for them.
      expect(navigation().getByText('Dashboard')).toBeInTheDocument();
      expect(navigation().getByText('Tasks')).toBeInTheDocument();
      view.unmount();
    }
  });

  it('keeps the existing role-gated entries as they were', () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'support' })));
    render(<MainLayout />);

    expect(navigation().queryByText('Deals')).not.toBeInTheDocument();
    expect(navigation().getByText('Tickets')).toBeInTheDocument();
    expect(navigation().getByText('Companies')).toBeInTheDocument();
    expect(navigation().queryByText('Users')).not.toBeInTheDocument();
  });
});
