import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { Component as TicketDetail } from './TicketDetail';
import { ticketsApi } from '@/api/endpoints';
import { createMockTicket, createMockUser } from '@/test/factories';
import type { User } from '@/types';

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

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const ASSIGNEE_ID = 7;

const renderDetail = () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/tickets/1']}>
        <Routes>
          <Route path="/tickets/:id" element={<TicketDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

const queryEditButton = () => screen.queryByRole('button', { name: /^edit$/i });
const queryDeleteButton = () => screen.queryByTestId('DeleteIcon');

describe('TicketDetail action gating', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(ticketsApi.getTicket).mockResolvedValue(
      createMockTicket({
        id: 1,
        subject: 'Login issue',
        assigned_to_id: ASSIGNEE_ID,
        assigned_to: createMockUser({ id: ASSIGNEE_ID, role: 'support' }),
      }),
    );
  });

  it('shows Edit and Delete for an admin', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    expect(queryEditButton()).toBeInTheDocument();
    expect(queryDeleteButton()).toBeInTheDocument();
  });

  it('shows Edit but not Delete for the support user the ticket is assigned to', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: ASSIGNEE_ID, role: 'support' })));

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    expect(queryEditButton()).toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
  });

  it('shows neither Edit nor Delete for a support user the ticket is not assigned to', async () => {
    mockUseAuth.mockReturnValue(
      authState(createMockUser({ id: ASSIGNEE_ID + 1, role: 'support' })),
    );

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    expect(queryEditButton()).not.toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
  });

  it('shows neither Edit nor Delete for a sales user', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    expect(queryEditButton()).not.toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
  });

  it('shows neither Edit nor Delete for a customer user', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'customer' })));

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    expect(queryEditButton()).not.toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
  });

  it('shows neither Edit nor Delete for a support user when the ticket is unassigned', async () => {
    vi.mocked(ticketsApi.getTicket).mockResolvedValue(
      createMockTicket({
        id: 1,
        subject: 'Login issue',
        assigned_to_id: undefined,
        assigned_to: undefined,
      }),
    );
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: ASSIGNEE_ID, role: 'support' })));

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    expect(queryEditButton()).not.toBeInTheDocument();
    expect(queryDeleteButton()).not.toBeInTheDocument();
  });
});

describe('TicketDetail comments', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders comments and displays Internal Note chip for internal comments', async () => {
    vi.mocked(ticketsApi.getTicket).mockResolvedValue(
      createMockTicket({
        id: 1,
        subject: 'Login issue',
        assigned_to_id: ASSIGNEE_ID,
        comments: [
          {
            id: 101,
            ticket_id: 1,
            user_id: 1,
            content: 'Public comment here',
            is_internal: false,
            created_at: '2026-10-17T12:00:00Z',
            updated_at: '2026-10-17T12:00:00Z',
            user: createMockUser({ id: 1, first_name: 'Alice', last_name: 'Smith' }),
          },
          {
            id: 102,
            ticket_id: 1,
            user_id: ASSIGNEE_ID,
            content: 'Internal staff note',
            is_internal: true,
            created_at: '2026-10-17T12:05:00Z',
            updated_at: '2026-10-17T12:05:00Z',
            user: createMockUser({ id: ASSIGNEE_ID, first_name: 'Bob', last_name: 'Support' }),
          },
        ],
      }),
    );
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));

    renderDetail();

    expect(await screen.findByText('Public comment here')).toBeInTheDocument();
    expect(screen.getByText('Internal staff note')).toBeInTheDocument();
    expect(screen.getByText('Internal Note')).toBeInTheDocument();
  });

  it('allows an assigned support user to submit an internal comment', async () => {
    vi.mocked(ticketsApi.getTicket).mockResolvedValue(
      createMockTicket({
        id: 1,
        subject: 'Login issue',
        assigned_to_id: ASSIGNEE_ID,
      }),
    );
    vi.mocked(ticketsApi.addComment).mockResolvedValue({
      id: 201,
      ticket_id: 1,
      user_id: ASSIGNEE_ID,
      content: 'Investigating in backend logs',
      is_internal: true,
      created_at: '2026-10-17T12:10:00Z',
    });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: ASSIGNEE_ID, role: 'support' })));

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    const commentInput = screen.getByPlaceholderText('Add a comment...');
    fireEvent.change(commentInput, { target: { value: 'Investigating in backend logs' } });

    const internalCheckbox = screen.getByLabelText(/internal note/i);
    fireEvent.click(internalCheckbox);

    const form = commentInput.closest('form');
    expect(form).not.toBeNull();
    if (form) {
      fireEvent.submit(form);
    }

    await waitFor(() => {
      expect(ticketsApi.addComment).toHaveBeenCalledWith(1, {
        content: 'Investigating in backend logs',
        is_internal: true,
      });
    });
  });

  it('hides the Add Comment form for customer or sales users', async () => {
    vi.mocked(ticketsApi.getTicket).mockResolvedValue(
      createMockTicket({
        id: 1,
        subject: 'Login issue',
        assigned_to_id: ASSIGNEE_ID,
      }),
    );
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'customer' })));

    renderDetail();

    expect(await screen.findByText('Login issue')).toBeInTheDocument();
    expect(screen.queryByPlaceholderText('Add a comment...')).not.toBeInTheDocument();
  });
});
