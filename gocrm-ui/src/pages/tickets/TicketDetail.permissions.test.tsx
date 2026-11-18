import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { Component as TicketDetail } from './TicketDetail';
import { ticketsApi } from '@/api/endpoints';
import { createMockTicket, createMockUser } from '@/test/factories';
import { customPermissions } from '@/test/permissions';
import type { Permission } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
    showInfo: vi.fn(),
    showWarning: vi.fn(),
  }),
}));

vi.mock('@/api/endpoints', () => ({
  ticketsApi: {
    getTicket: vi.fn(),
    deleteTicket: vi.fn(),
    addComment: vi.fn(),
  },
}));

// The caller: a custom role whose name ('customer') never offered a ticket
// write before the conversion, so only the permission list can show a button.
const CALLER_ID = 7;

const signedInWith = (permissions: Permission[]) =>
  mockUseAuth.mockReturnValue({
    user: createMockUser({ id: CALLER_ID, role: 'customer' }),
    permissions,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

const ticketAssignedTo = (assigneeId: number) =>
  vi.mocked(ticketsApi.getTicket).mockResolvedValue(
    createMockTicket({
      id: 1,
      subject: 'Login issue',
      assigned_to_id: assigneeId,
      assigned_to: createMockUser({ id: assigneeId, role: 'support' }),
    }),
  );

const renderDetail = async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/tickets/1']}>
        <Routes>
          <Route path="/tickets/:id" element={<TicketDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  expect(await screen.findByText('Login issue')).toBeInTheDocument();
  return view;
};

const queryEditButton = () => screen.queryByRole('button', { name: /^edit$/i });
const queryDeleteButton = () => screen.queryByTestId('DeleteIcon');

describe('TicketDetail buttons', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('follow tickets.update scope and assignment', async () => {
    const UPDATE_ALL = customPermissions('tickets.view', 'tickets.update');
    const UPDATE_OWN = customPermissions('tickets.view', ['tickets.update', 'own']);

    // Scope all: Edit on a ticket assigned to someone else.
    signedInWith(UPDATE_ALL);
    ticketAssignedTo(CALLER_ID + 1);
    let view = await renderDetail();
    expect(queryEditButton()).toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
    view.unmount();

    // Scope own, same permission list twice: the assignee alone decides.
    signedInWith(UPDATE_OWN);
    ticketAssignedTo(CALLER_ID);
    view = await renderDetail();
    expect(queryEditButton()).toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
    view.unmount();

    signedInWith(UPDATE_OWN);
    ticketAssignedTo(CALLER_ID + 1);
    view = await renderDetail();
    expect(queryEditButton()).not.toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
    view.unmount();

    // The read key alone offers no write control.
    signedInWith(customPermissions('tickets.view'));
    ticketAssignedTo(CALLER_ID);
    view = await renderDetail();
    expect(queryEditButton()).not.toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
    view.unmount();

    // Delete follows tickets.delete only, not tickets.update.
    signedInWith(customPermissions('tickets.view', 'tickets.delete'));
    ticketAssignedTo(CALLER_ID + 1);
    view = await renderDetail();
    expect(queryDeleteButton()).toBeInTheDocument();
    expect(queryEditButton()).not.toBeInTheDocument();
    view.unmount();
  });
});
