import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, requireElement } from '@/test/test-utils';
import { Component as CustomerList } from './CustomerList';
import { customersApi } from '@/api/endpoints';
import { createMockCustomer } from '@/test/factories';
import { useNavigate } from 'react-router-dom';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
  };
});

// "Add Customer" is offered to admin and sales only.
vi.mock('@/hooks/useAuth', async () => {
  const { createMockUser } = await import('@/test/factories');
  const user = createMockUser({ id: 1, role: 'admin' });
  return {
    useAuth: () => ({
      user,
      isLoading: false,
      isAuthenticated: true,
      login: vi.fn(),
      register: vi.fn(),
      logout: vi.fn(),
      refreshUser: vi.fn(),
    }),
  };
});

vi.mock('@/api/endpoints', () => ({
  customersApi: {
    getCustomers: vi.fn(),
    deleteCustomer: vi.fn(),
  },
}));

describe('CustomerList', () => {
  const mockNavigate = vi.fn();
  const mockCustomers = [
    createMockCustomer({ id: 1, company_name: 'Customer A' }),
    createMockCustomer({ id: 2, company_name: 'Customer B' }),
    createMockCustomer({ id: 3, company_name: 'Customer C' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(customersApi.getCustomers).mockResolvedValue({
      data: mockCustomers,
      total: 3,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  it('renders customer list with data', async () => {
    render(<CustomerList />);

    await waitFor(() => {
      expect(screen.getByText('Customers')).toBeInTheDocument();
      expect(screen.getByText('Customer A')).toBeInTheDocument();
      expect(screen.getByText('Customer B')).toBeInTheDocument();
      expect(screen.getByText('Customer C')).toBeInTheDocument();
    });
  });

  // Customers have no activation concept on the API; the column used to show
  // an invented "Active" for every row.
  it('has no status column', async () => {
    render(<CustomerList />);

    await screen.findByText('Customer A');
    expect(screen.queryByRole('columnheader', { name: 'Status' })).not.toBeInTheDocument();
    expect(screen.queryByText('Active')).not.toBeInTheDocument();
    expect(screen.queryByText('Inactive')).not.toBeInTheDocument();
  });

  it('navigates to create new customer', async () => {
    render(<CustomerList />);
    
    await waitFor(() => {
      expect(screen.getByText('Customers')).toBeInTheDocument();
    });
    
    const addButton = screen.getByRole('button', { name: /add customer/i });
    fireEvent.click(addButton);
    
    expect(mockNavigate).toHaveBeenCalledWith('/customers/new');
  });

  it('filters customers by search term', async () => {
    render(<CustomerList />);

    await waitFor(() => {
      expect(screen.getByText('Customer A')).toBeInTheDocument();
    });

    const searchInput = screen.getByPlaceholderText('Search customers...');
    fireEvent.change(searchInput, { target: { value: 'test search' } });

    await waitFor(() => {
      expect(customersApi.getCustomers).toHaveBeenCalledWith(
        expect.objectContaining({
          search: 'test search',
          page: 1,
        })
      );
    });
  });

  it('navigates to customer detail on row click', async () => {
    render(<CustomerList />);

    await waitFor(() => {
      const row = screen.getByText('Customer A').closest('tr');
      expect(row).toBeInTheDocument();
      fireEvent.click(requireElement(row));
    });

    expect(mockNavigate).toHaveBeenCalledWith('/customers/1');
  });

  it('handles customer deletion', async () => {
    vi.mocked(customersApi.deleteCustomer).mockResolvedValue(undefined);
    
    render(<CustomerList />);

    await waitFor(() => {
      expect(screen.getByText('Customer A')).toBeInTheDocument();
    });

    // Find and click delete button for first customer
    // The delete button is rendered by DataTable as an IconButton with Delete icon
    const rows = screen.getAllByRole('row');
    // Skip the header row and find the delete button in the first data row
    const firstDataRow = rows[1];
    const deleteButton = firstDataRow.querySelector('button svg[data-testid="DeleteIcon"]')?.parentElement;
    fireEvent.click(requireElement(deleteButton));

    // Confirm deletion
    await waitFor(() => {
      const confirmButton = screen.getByRole('button', { name: 'Delete' });
      fireEvent.click(confirmButton);
    });

    await waitFor(() => {
      expect(customersApi.deleteCustomer).toHaveBeenCalledWith(1);
    });
  });

  // The API returns no revenue; the column used to show a fabricated $0 for
  // every customer (#186).
  it('has no total revenue column or revenue sort', async () => {
    render(<CustomerList />);

    await screen.findByText('Customer A');
    const headers = screen.getAllByRole('columnheader').map((h) => h.textContent?.trim());
    expect(headers).toEqual([
      'Company',
      'Primary Contact',
      'Email',
      'Phone',
      'Customer Since',
      'Actions',
    ]);
    expect(
      screen.queryByRole('columnheader', { name: /total revenue/i }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /revenue/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/total revenue/i)).not.toBeInTheDocument();
  });
});