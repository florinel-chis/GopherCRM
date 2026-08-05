import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Dashboard } from './Dashboard';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const mockGetStats = vi.fn();

vi.mock('@/api/endpoints', () => ({
  dashboardApi: {
    getStats: () => mockGetStats(),
    getRecentActivities: vi.fn(),
    getSalesPerformance: vi.fn(),
    getUpcomingTasks: vi.fn(),
  },
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

const renderDashboard = () => {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    </QueryClientProvider>
  );
};

describe('Dashboard', () => {
  beforeEach(() => {
    mockUseAuth.mockReset();
    mockGetStats.mockReset();
    mockGetStats.mockResolvedValue({
      total_leads: 10,
      total_customers: 4,
      open_tickets: 3,
      pending_tasks: 7,
      conversion_rate: 40,
    });
  });

  it.each(['admin', 'sales', 'support'] as const)(
    'fetches and renders the stats cards for the %s role',
    async (role) => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));

      renderDashboard();

      expect(await screen.findByText('Total Leads')).toBeInTheDocument();
      expect(screen.getByText('Total Customers')).toBeInTheDocument();
      expect(screen.getByText('Open Tickets')).toBeInTheDocument();
      expect(screen.getByText('Pending Tasks')).toBeInTheDocument();
      expect(screen.getByText('Conversion Rate')).toBeInTheDocument();
      await waitFor(() => expect(mockGetStats).toHaveBeenCalled());
    }
  );

  it('does not fetch or render stats for the customer role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'customer' })));

    renderDashboard();

    // The welcome heading still renders.
    expect(screen.getByText('Dashboard')).toBeInTheDocument();

    // No stats cards and no request to the forbidden endpoint.
    expect(screen.queryByText('Total Leads')).not.toBeInTheDocument();
    expect(screen.queryByText('Total Customers')).not.toBeInTheDocument();
    expect(screen.queryByText('Conversion Rate')).not.toBeInTheDocument();
    await waitFor(() => expect(mockGetStats).not.toHaveBeenCalled());
  });
});
