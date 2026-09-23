import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as LeadList } from './LeadList';
import { leadsApi } from '@/api/endpoints';
import { createMockLead } from '@/test/factories';
import { useNavigate } from 'react-router-dom';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  leadsApi: {
    getLeads: vi.fn(),
    deleteLead: vi.fn(),
    convertLead: vi.fn(),
  },
}));

describe('LeadList', () => {
  const mockNavigate = vi.fn();
  const mockLeads = [
    createMockLead({ id: 1, company_name: 'Company A', status: 'new' }),
    createMockLead({ id: 2, company_name: 'Company B', status: 'contacted' }),
    createMockLead({ id: 3, company_name: 'Company C', status: 'qualified' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(mockNavigate);
    (leadsApi.getLeads as any).mockResolvedValue({
      data: mockLeads,
      total: 3,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  it('renders lead list with data', async () => {
    render(<LeadList />);

    await waitFor(() => {
      expect(screen.getByText('Leads')).toBeInTheDocument();
      expect(screen.getByText('Company A')).toBeInTheDocument();
      expect(screen.getByText('Company B')).toBeInTheDocument();
      expect(screen.getByText('Company C')).toBeInTheDocument();
    });
  });

  it('shows loading state initially', () => {
    (leadsApi.getLeads as any).mockImplementation(() => new Promise(() => {})); // Never resolves
    render(<LeadList />);
    expect(screen.getByTestId('loading')).toBeInTheDocument();
  });

  it('navigates to create new lead', async () => {
    render(<LeadList />);
    
    await waitFor(() => {
      expect(screen.getByText('Leads')).toBeInTheDocument();
    });
    
    const addButton = screen.getByRole('button', { name: /add lead/i });
    fireEvent.click(addButton);
    
    expect(mockNavigate).toHaveBeenCalledWith('/leads/new');
  });

  it('filters leads by search term', async () => {
    render(<LeadList />);

    await waitFor(() => {
      expect(screen.getByText('Company A')).toBeInTheDocument();
    });

    const searchInput = screen.getByPlaceholderText('Search leads...');
    fireEvent.change(searchInput, { target: { value: 'test search' } });

    await waitFor(() => {
      expect(leadsApi.getLeads).toHaveBeenCalledWith(
        expect.objectContaining({
          search: 'test search',
          page: 1,
        })
      );
    });
  });

  it('filters leads by status', async () => {
    render(<LeadList />);

    await waitFor(() => {
      expect(screen.getByText('Company A')).toBeInTheDocument();
    });

    // Find the Status select by finding all comboboxes and selecting the first one (status filter)
    const comboboxes = screen.getAllByRole('combobox');
    const statusSelect = comboboxes[0]; // First combobox is the status filter
    fireEvent.mouseDown(statusSelect);
    
    // Wait for the dropdown menu to appear
    await waitFor(() => {
      expect(screen.getByRole('listbox')).toBeInTheDocument();
    });
    
    // Click on 'New' option
    const newOption = screen.getByRole('option', { name: 'New' });
    fireEvent.click(newOption);

    await waitFor(() => {
      expect(leadsApi.getLeads).toHaveBeenCalledWith(
        expect.objectContaining({
          status: 'new',
          page: 1,
        })
      );
    });
  });

  it('navigates to lead detail on row click', async () => {
    render(<LeadList />);

    await waitFor(() => {
      const row = screen.getByText('Company A').closest('tr');
      expect(row).toBeInTheDocument();
      fireEvent.click(row!);
    });

    expect(mockNavigate).toHaveBeenCalledWith('/leads/1');
  });

  it('handles lead deletion', async () => {
    (leadsApi.deleteLead as any).mockResolvedValue({});
    
    render(<LeadList />);

    await waitFor(() => {
      expect(screen.getByText('Company A')).toBeInTheDocument();
    });

    // Find the delete button by its test id within the first data row
    const deleteButtons = screen.getAllByTestId('DeleteIcon');
    // Click the first delete button (for Company A)
    fireEvent.click(deleteButtons[0].closest('button')!);

    // Confirm deletion in the dialog
    await waitFor(() => {
      const confirmButton = screen.getByRole('button', { name: 'Delete' });
      fireEvent.click(confirmButton);
    });

    await waitFor(() => {
      expect(leadsApi.deleteLead).toHaveBeenCalledWith(1);
    });
  });

  // Blocked by a component bug, not a test gap: the overflow menu that offers
  // "Convert" can never open. Its button is passed to DataTable as `actions`,
  // which renders once in the table toolbar with no row context, and its
  // handler is `selectedLead && handleMenuOpen(...)` while selectedLead is only
  // set inside handleMenuOpen, so the guard is always false. Replace this todo
  // with a real test in the change that gives each row its own menu.
  it.todo('converts a qualified lead from the row overflow menu');

  it('handles pagination', async () => {
    // Update the mock to return more data to enable pagination
    (leadsApi.getLeads as any).mockResolvedValue({
      data: mockLeads,
      total: 25, // More than one page worth
      page: 1,
      limit: 10,
      total_pages: 3,
    });
    
    render(<LeadList />);

    await waitFor(() => {
      expect(screen.getByText('Company A')).toBeInTheDocument();
    });

    // Find pagination controls - TablePagination uses specific aria labels
    const nextPageButton = screen.getByRole('button', { name: /next page/i });
    fireEvent.click(nextPageButton);

    await waitFor(() => {
      expect(leadsApi.getLeads).toHaveBeenCalledWith(
        expect.objectContaining({
          page: 2,
        })
      );
    });
  });
});