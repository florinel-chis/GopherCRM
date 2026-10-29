import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { Component as TicketList } from './TicketList';
import { ticketsApi, type TicketFilters } from '@/api/endpoints';
import { createMockTicket, createMockUser } from '@/test/factories';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  ticketsApi: {
    getTickets: vi.fn(),
    deleteTicket: vi.fn(),
  },
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const signedInAs = (role: User['role']) =>
  mockUseAuth.mockReturnValue({
    user: createMockUser({ id: 1, role }),
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

const rowFor = (subject: string) => {
  const row = screen.getByText(subject).closest('tr');
  if (!row) throw new Error(`no row for ${subject}`);
  return row;
};

// The ticket overflow menu only repeated view, edit and delete, which every
// row already offers as icon buttons under the same role rules, so it is gone.
// The row icons must still act on their own row after a re-sort.
describe('TicketList row actions', () => {
  const mockNavigate = vi.fn();
  const tickets = [
    createMockTicket({ id: 11, subject: 'Alpha ticket' }),
    createMockTicket({ id: 12, subject: 'Bravo ticket' }),
    createMockTicket({ id: 13, subject: 'Charlie ticket' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    // A sort reverses the rows and a search narrows them, so the tests act on
    // rows whose position changes.
    vi.mocked(ticketsApi.getTickets).mockImplementation(async (filters?: TicketFilters) => {
      const term = filters?.search?.toLowerCase() ?? '';
      const matching = tickets.filter((t) => t.subject.toLowerCase().includes(term));
      const data = filters?.sort_by ? [...matching].reverse() : matching;
      return { data, total: data.length, page: 1, limit: 10, total_pages: 1 };
    });
    vi.mocked(ticketsApi.deleteTicket).mockResolvedValue(undefined);
  });

  it.each(['admin', 'support', 'sales', 'customer'] as const)(
    'renders no overflow menu for %s',
    async (role) => {
      signedInAs(role);
      render(<TicketList />);
      await screen.findByText('Alpha ticket');

      expect(screen.queryByTestId('MoreVertIcon')).not.toBeInTheDocument();
    },
  );

  it('edits and deletes the chosen ticket after the table was re-sorted', async () => {
    signedInAs('admin');
    render(<TicketList />);
    await screen.findByText('Alpha ticket');

    fireEvent.click(screen.getByRole('button', { name: 'Subject' }));
    await waitFor(() => expect(screen.getAllByRole('row')[1]).toHaveTextContent('Charlie ticket'));

    // Alpha moved from the first row to the last.
    fireEvent.click(within(rowFor('Alpha ticket')).getByTestId('EditIcon'));
    expect(mockNavigate).toHaveBeenCalledWith('/tickets/11/edit');
    expect(mockNavigate).toHaveBeenCalledTimes(1);

    fireEvent.click(within(rowFor('Alpha ticket')).getByTestId('DeleteIcon'));
    const dialog = await screen.findByRole('dialog', { name: 'Delete Ticket' });
    expect(dialog).toHaveTextContent('#11');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(ticketsApi.deleteTicket).toHaveBeenCalled());
    expect(vi.mocked(ticketsApi.deleteTicket).mock.calls[0][0]).toBe(11);
  });

  it('edits the chosen ticket after a search narrowed the table', async () => {
    signedInAs('admin');
    render(<TicketList />);
    await screen.findByText('Alpha ticket');

    fireEvent.change(screen.getByPlaceholderText('Search tickets...'), {
      target: { value: 'Charlie' },
    });
    await screen.findByText('Charlie ticket');
    expect(screen.queryByText('Alpha ticket')).not.toBeInTheDocument();

    fireEvent.click(within(rowFor('Charlie ticket')).getByTestId('EditIcon'));
    expect(mockNavigate).toHaveBeenCalledWith('/tickets/13/edit');
    expect(mockNavigate).toHaveBeenCalledTimes(1);
  });

  it('keeps sales read-only: view only, no edit or delete icons', async () => {
    signedInAs('sales');
    render(<TicketList />);
    await screen.findByText('Alpha ticket');

    const row = rowFor('Alpha ticket');
    expect(within(row).getByTestId('VisibilityIcon')).toBeInTheDocument();
    expect(within(row).queryByTestId('EditIcon')).not.toBeInTheDocument();
    expect(within(row).queryByTestId('DeleteIcon')).not.toBeInTheDocument();
  });
});
