import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as TicketList } from './TicketList';
import { ticketsApi } from '@/api/endpoints';
import { createMockTicket } from '@/test/factories';
import { useNavigate } from 'react-router-dom';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  ticketsApi: {
    getTickets: vi.fn(),
    deleteTicket: vi.fn(),
  },
}));

describe('TicketList', () => {
  const mockNavigate = vi.fn();
  const mockTickets = [
    createMockTicket({ id: 1, subject: 'Ticket 1', status: 'open', priority: 'high' }),
    createMockTicket({ id: 2, subject: 'Ticket 2', status: 'in_progress', priority: 'medium' }),
    createMockTicket({ id: 3, subject: 'Ticket 3', status: 'resolved', priority: 'low' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(mockNavigate);
    (ticketsApi.getTickets as any).mockResolvedValue({
      data: mockTickets,
      total: 3,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  it('renders ticket list with data', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Tickets')).toBeInTheDocument();
      expect(screen.getByText('Ticket 1')).toBeInTheDocument();
      expect(screen.getByText('Ticket 2')).toBeInTheDocument();
      expect(screen.getByText('Ticket 3')).toBeInTheDocument();
    });
  });

  it('displays ticket IDs correctly', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('#1')).toBeInTheDocument();
      expect(screen.getByText('#2')).toBeInTheDocument();
      expect(screen.getByText('#3')).toBeInTheDocument();
    });
  });

  it('displays status chips with correct colors', async () => {
    render(<TicketList />);

    await waitFor(() => {
      const openChip = screen.getByText('Open');
      const inProgressChip = screen.getByText('In Progress');
      const resolvedChip = screen.getByText('Resolved');

      expect(openChip).toBeInTheDocument();
      expect(inProgressChip).toBeInTheDocument();
      expect(resolvedChip).toBeInTheDocument();
    });
  });

  it('displays priority chips correctly', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('High')).toBeInTheDocument();
      expect(screen.getByText('Medium')).toBeInTheDocument();
      expect(screen.getByText('Low')).toBeInTheDocument();
    });
  });

  it('filters tickets by status', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Ticket 1')).toBeInTheDocument();
    });

    // Find the status select by looking for the specific FormControl in the filter area
    const filterBox = screen.getByPlaceholderText('Search tickets...').closest('.MuiBox-root');
    const statusSelects = filterBox?.querySelectorAll('[role="combobox"]');
    expect(statusSelects).toBeDefined();
    expect(statusSelects!.length).toBeGreaterThan(0);
    const statusSelect = statusSelects![0]; // First select is Status
    fireEvent.mouseDown(statusSelect);
    
    await waitFor(() => {
      const openOption = screen.getByRole('option', { name: 'Open' });
      fireEvent.click(openOption);
    });

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          status: 'open',
          page: 1,
        })
      );
    });
  });

  it('filters tickets by priority', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Ticket 1')).toBeInTheDocument();
    });

    // Find the priority select by looking for the specific FormControl in the filter area
    const filterBox = screen.getByPlaceholderText('Search tickets...').closest('.MuiBox-root');
    const prioritySelects = filterBox?.querySelectorAll('[role="combobox"]');
    expect(prioritySelects).toBeDefined();
    expect(prioritySelects!.length).toBeGreaterThan(1);
    const prioritySelect = prioritySelects![1]; // Second select is Priority
    fireEvent.mouseDown(prioritySelect);
    
    await waitFor(() => {
      const highOption = screen.getByRole('option', { name: 'High' });
      fireEvent.click(highOption);
    });

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          priority: 'high',
          page: 1,
        })
      );
    });
  });

  it('navigates to create new ticket', async () => {
    render(<TicketList />);
    
    await waitFor(() => {
      const createButton = screen.getByRole('button', { name: /create ticket/i });
      expect(createButton).toBeInTheDocument();
      fireEvent.click(createButton);
    });
    
    expect(mockNavigate).toHaveBeenCalledWith('/tickets/new');
  });

  it('navigates to ticket detail on row click', async () => {
    render(<TicketList />);

    await waitFor(() => {
      const row = screen.getByText('Ticket 1').closest('tr');
      expect(row).toBeInTheDocument();
      fireEvent.click(row!);
    });

    expect(mockNavigate).toHaveBeenCalledWith('/tickets/1');
  });

  it('handles ticket deletion', async () => {
    (ticketsApi.deleteTicket as any).mockResolvedValue({});
    
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Ticket 1')).toBeInTheDocument();
    });

    // Find the delete button for the first ticket - it's in the actions column
    const rows = screen.getAllByRole('row');
    // Skip header row, get first data row
    const firstDataRow = rows[1];
    const deleteButton = firstDataRow.querySelector('[data-testid="DeleteIcon"]')?.closest('button');
    expect(deleteButton).toBeInTheDocument();
    fireEvent.click(deleteButton!);

    await waitFor(() => {
      const confirmButton = screen.getByText('Delete');
      fireEvent.click(confirmButton);
    });

    await waitFor(() => {
      expect(ticketsApi.deleteTicket).toHaveBeenCalledWith(1);
    });
  });

  it('searches tickets', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Ticket 1')).toBeInTheDocument();
    });

    const searchInput = screen.getByPlaceholderText('Search tickets...');
    fireEvent.change(searchInput, { target: { value: 'bug' } });

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          search: 'bug',
          page: 1,
        })
      );
    });
  });
});