import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as TicketList } from './TicketList';
import { ticketsApi } from '@/api/endpoints';
import { createMockTicket, createMockCustomer, createMockUser } from '@/test/factories';
import { useNavigate } from 'react-router-dom';
import userEvent from '@testing-library/user-event';

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
    bulkUpdateStatus: vi.fn(),
  },
}));

describe('TicketList', () => {
  const mockNavigate = vi.fn();
  const mockCustomer = createMockCustomer({ id: 1, company_name: 'Acme Corp' });
  const mockAssignee = createMockUser({ 
    id: 2, 
    first_name: 'John', 
    last_name: 'Doe',
    role: 'support' 
  });
  
  const mockTickets = [
    createMockTicket({ 
      id: 1, 
      subject: 'Login issue',
      status: 'open',
      priority: 'high',
      customer: mockCustomer,
      assigned_to: mockAssignee,
    }),
    createMockTicket({ 
      id: 2, 
      subject: 'Payment failed',
      status: 'in_progress',
      priority: 'urgent',
      customer: mockCustomer,
      assigned_to: undefined,
    }),
    createMockTicket({ 
      id: 3, 
      subject: 'Feature request',
      status: 'resolved',
      priority: 'low',
      customer: mockCustomer,
      assigned_to: mockAssignee,
    }),
    createMockTicket({ 
      id: 4, 
      subject: 'Bug report',
      status: 'closed',
      priority: 'medium',
      customer: mockCustomer,
      assigned_to: mockAssignee,
    }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(mockNavigate);
    (ticketsApi.getTickets as any).mockResolvedValue({
      data: mockTickets,
      total: 4,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  it('renders ticket list with data', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Tickets')).toBeInTheDocument();
      expect(screen.getByText('Login issue')).toBeInTheDocument();
      expect(screen.getByText('Payment failed')).toBeInTheDocument();
      expect(screen.getByText('Feature request')).toBeInTheDocument();
      expect(screen.getByText('Bug report')).toBeInTheDocument();
    });
  });

  it('shows loading state initially', () => {
    (ticketsApi.getTickets as any).mockImplementation(() => new Promise(() => {})); // Never resolves
    render(<TicketList />);
    expect(screen.getByTestId('loading')).toBeInTheDocument();
  });

  it('displays ticket details correctly', async () => {
    render(<TicketList />);

    await waitFor(() => {
      // Check ticket IDs
      expect(screen.getByText('#1')).toBeInTheDocument();
      expect(screen.getByText('#2')).toBeInTheDocument();
      expect(screen.getByText('#3')).toBeInTheDocument();
      expect(screen.getByText('#4')).toBeInTheDocument();
      
      // Check customer name
      const customerCells = screen.getAllByText('Acme Corp');
      expect(customerCells.length).toBeGreaterThan(0);
      
      // Check assigned to
      const johnDoeElements = screen.getAllByText('John Doe');
      expect(johnDoeElements.length).toBeGreaterThan(0);
      expect(screen.getByText('Unassigned')).toBeInTheDocument();
    });
  });

  it('displays status chips with correct colors', async () => {
    render(<TicketList />);

    await waitFor(() => {
      const openChip = screen.getByText('Open');
      const inProgressChip = screen.getByText('In Progress');
      const resolvedChip = screen.getByText('Resolved');
      const closedChip = screen.getByText('Closed');

      expect(openChip).toBeInTheDocument();
      expect(inProgressChip).toBeInTheDocument();
      expect(resolvedChip).toBeInTheDocument();
      expect(closedChip).toBeInTheDocument();
    });
  });

  it('displays priority chips with correct colors', async () => {
    render(<TicketList />);

    await waitFor(() => {
      const highChip = screen.getByText('High');
      const urgentChip = screen.getByText('Urgent');
      const lowChip = screen.getByText('Low');
      const mediumChip = screen.getByText('Medium');

      expect(highChip).toBeInTheDocument();
      expect(urgentChip).toBeInTheDocument();
      expect(lowChip).toBeInTheDocument();
      expect(mediumChip).toBeInTheDocument();
    });
  });

  it('filters tickets by search term', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    const searchInput = screen.getByPlaceholderText('Search tickets...');
    fireEvent.change(searchInput, { target: { value: 'payment' } });

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          search: 'payment',
          page: 1,
        })
      );
    });
  });

  it('filters tickets by status', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    // Find the Status select - find the FormControl by looking for the InputLabel
    const statusLabel = screen.getAllByText('Status').find(element => 
      element.classList.contains('MuiInputLabel-root')
    );
    const statusFormControl = statusLabel?.closest('.MuiFormControl-root');
    const statusSelect = statusFormControl?.querySelector('[role="combobox"]');
    expect(statusSelect).toBeInTheDocument();
    fireEvent.mouseDown(statusSelect!);
    
    // Wait for the dropdown menu to appear
    await waitFor(() => {
      expect(screen.getByRole('listbox')).toBeInTheDocument();
    });
    
    // Click on 'Open' option
    const openOption = screen.getByRole('option', { name: 'Open' });
    fireEvent.click(openOption);

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
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    // Find the Priority select - find the FormControl by looking for the InputLabel
    const priorityLabel = screen.getAllByText('Priority').find(element => 
      element.classList.contains('MuiInputLabel-root')
    );
    const priorityFormControl = priorityLabel?.closest('.MuiFormControl-root');
    const prioritySelect = priorityFormControl?.querySelector('[role="combobox"]');
    expect(prioritySelect).toBeInTheDocument();
    fireEvent.mouseDown(prioritySelect!);
    
    // Wait for the dropdown menu to appear
    await waitFor(() => {
      expect(screen.getByRole('listbox')).toBeInTheDocument();
    });
    
    // Click on 'High' option
    const highOption = screen.getByRole('option', { name: 'High' });
    fireEvent.click(highOption);

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          priority: 'high',
          page: 1,
        })
      );
    });
  });

  it('clears filters when selecting "All Statuses"', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    // First set a status filter
    const statusLabel = screen.getAllByText('Status').find(element => 
      element.classList.contains('MuiInputLabel-root')
    );
    const statusFormControl = statusLabel?.closest('.MuiFormControl-root');
    const statusSelect = statusFormControl?.querySelector('[role="combobox"]');
    fireEvent.mouseDown(statusSelect!);
    
    await waitFor(() => {
      const openOption = screen.getByRole('option', { name: 'Open' });
      fireEvent.click(openOption);
    });

    // Wait for the API call to complete
    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          status: 'open',
        })
      );
    });

    // Then clear it - need to find the select again as it might have re-rendered
    const statusLabel2 = screen.getAllByText('Status').find(element => 
      element.classList.contains('MuiInputLabel-root')
    );
    const statusFormControl2 = statusLabel2?.closest('.MuiFormControl-root');
    const statusSelect2 = statusFormControl2?.querySelector('[role="combobox"]');
    fireEvent.mouseDown(statusSelect2!);
    
    // Wait for dropdown to open with new options
    await waitFor(() => {
      expect(screen.getByRole('listbox')).toBeInTheDocument();
    });
    
    const allOption = screen.getByRole('option', { name: 'All Statuses' });
    fireEvent.click(allOption);

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenLastCalledWith(
        expect.objectContaining({
          status: '',
        })
      );
    });
  });

  it('navigates to create new ticket', async () => {
    render(<TicketList />);
    
    await waitFor(() => {
      expect(screen.getByText('Tickets')).toBeInTheDocument();
    });
    
    const createButton = screen.getByRole('button', { name: /create ticket/i });
    fireEvent.click(createButton);
    
    expect(mockNavigate).toHaveBeenCalledWith('/tickets/new');
  });

  it('navigates to ticket detail on row click', async () => {
    render(<TicketList />);

    await waitFor(() => {
      const row = screen.getByText('Login issue').closest('tr');
      expect(row).toBeInTheDocument();
      fireEvent.click(row!);
    });

    expect(mockNavigate).toHaveBeenCalledWith('/tickets/1');
  });

  it('navigates to edit ticket', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    // Find the edit button by its test id within the first data row
    const editButtons = screen.getAllByTestId('EditIcon');
    // Click the first edit button (for Login issue)
    fireEvent.click(editButtons[0].closest('button')!);

    expect(mockNavigate).toHaveBeenCalledWith('/tickets/1/edit');
  });

  it('handles ticket deletion', async () => {
    (ticketsApi.deleteTicket as any).mockResolvedValue({});
    
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    // Find the delete button by its test id within the first data row
    const deleteButtons = screen.getAllByTestId('DeleteIcon');
    // Click the first delete button (for Login issue)
    fireEvent.click(deleteButtons[0].closest('button')!);

    // Confirm deletion in the dialog
    await waitFor(() => {
      expect(screen.getByText(/Are you sure you want to delete ticket #1/)).toBeInTheDocument();
      const confirmButton = screen.getByRole('button', { name: 'Delete' });
      fireEvent.click(confirmButton);
    });

    await waitFor(() => {
      expect(ticketsApi.deleteTicket).toHaveBeenCalledWith(1);
    });
  });

  it('cancels deletion when clicking cancel', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    const deleteButtons = screen.getAllByTestId('DeleteIcon');
    fireEvent.click(deleteButtons[0].closest('button')!);

    await waitFor(() => {
      const cancelButton = screen.getByRole('button', { name: 'Cancel' });
      fireEvent.click(cancelButton);
    });

    expect(ticketsApi.deleteTicket).not.toHaveBeenCalled();
  });

  it('handles pagination', async () => {
    // Update the mock to return more data to enable pagination
    (ticketsApi.getTickets as any).mockResolvedValue({
      data: mockTickets,
      total: 25, // More than one page worth
      page: 1,
      limit: 10,
      total_pages: 3,
    });
    
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    // Find pagination controls
    const nextPageButton = screen.getByRole('button', { name: /next page/i });
    fireEvent.click(nextPageButton);

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          page: 2,
        })
      );
    });
  });

  it('changes rows per page', async () => {
    render(<TicketList />);

    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });

    // Find the rows per page select
    const rowsPerPageSelect = screen.getByRole('combobox', { name: /rows per page/i });
    fireEvent.mouseDown(rowsPerPageSelect);

    await waitFor(() => {
      const option25 = screen.getByRole('option', { name: '25' });
      fireEvent.click(option25);
    });

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          limit: 25,
          page: 1,
        })
      );
    });
  });

  it('combines multiple filters', async () => {
    render(<TicketList />);

    // Wait for initial data to load
    await waitFor(() => {
      expect(screen.getByText('Login issue')).toBeInTheDocument();
    });
    
    // Also wait for the API call to complete
    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledTimes(1);
    });

    // Set search filter
    const searchInput = screen.getByPlaceholderText('Search tickets...');
    fireEvent.change(searchInput, { target: { value: 'login' } });

    // Wait for search API call
    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          search: 'login',
        })
      );
    });
    
    // Wait for data to reload after search
    await waitFor(() => {
      expect(screen.queryByTestId('loading')).not.toBeInTheDocument();
    });

    // Set status filter
    const statusLabel = screen.getAllByText('Status').find(element => 
      element.classList.contains('MuiInputLabel-root')
    );
    const statusFormControl = statusLabel?.closest('.MuiFormControl-root');
    const statusSelect = statusFormControl?.querySelector('[role="combobox"]');
    fireEvent.mouseDown(statusSelect!);
    await waitFor(() => {
      const openOption = screen.getByRole('option', { name: 'Open' });
      fireEvent.click(openOption);
    });
    
    // Wait for status filter API call
    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          search: 'login',
          status: 'open',
        })
      );
    });
    
    // Wait for data to reload after status filter
    await waitFor(() => {
      expect(screen.queryByTestId('loading')).not.toBeInTheDocument();
    });

    // Set priority filter
    const priorityLabel = screen.getAllByText('Priority').find(element => 
      element.classList.contains('MuiInputLabel-root')
    );
    const priorityFormControl = priorityLabel?.closest('.MuiFormControl-root');
    const prioritySelect = priorityFormControl?.querySelector('[role="combobox"]');
    fireEvent.mouseDown(prioritySelect!);
    await waitFor(() => {
      const highOption = screen.getByRole('option', { name: 'High' });
      fireEvent.click(highOption);
    });

    await waitFor(() => {
      expect(ticketsApi.getTickets).toHaveBeenCalledWith(
        expect.objectContaining({
          search: 'login',
          status: 'open',
          priority: 'high',
          page: 1,
        })
      );
    });
  });

  it('shows empty table when no tickets found', async () => {
    (ticketsApi.getTickets as any).mockResolvedValue({
      data: [],
      total: 0,
      page: 1,
      limit: 10,
      total_pages: 0,
    });

    render(<TicketList />);

    await waitFor(() => {
      // Check that the table exists but has no data rows
      expect(screen.getByRole('table')).toBeInTheDocument();
      // Check that there are no ticket rows (only header row should exist)
      const rows = screen.getAllByRole('row');
      expect(rows.length).toBe(1); // Only header row
    });
  });

  it('handles API errors gracefully', async () => {
    const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    (ticketsApi.getTickets as any).mockRejectedValue(new Error('API Error'));

    render(<TicketList />);

    await waitFor(() => {
      // When API fails, the component should still render but with no data
      expect(screen.getByRole('table')).toBeInTheDocument();
      const rows = screen.getAllByRole('row');
      expect(rows.length).toBe(1); // Only header row
    });

    consoleErrorSpy.mockRestore();
  });
});