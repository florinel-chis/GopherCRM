import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { Component as CustomerList } from './CustomerList';
import { customersApi, type CustomerFilters } from '@/api/endpoints';
import { createMockCustomer, createMockUser } from '@/test/factories';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  customersApi: {
    getCustomers: vi.fn(),
    deleteCustomer: vi.fn(),
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

// The per-row overflow menu carries the one customer action the row icons do
// not: opening a ticket for that customer. POST /tickets is admin and support
// only, and POST /customers admin and sales only, so both affordances follow
// the caller's role.
describe('CustomerList row actions', () => {
  const mockNavigate = vi.fn();
  const customers = [
    createMockCustomer({ id: 1, company_name: 'Alpha Customers' }),
    createMockCustomer({ id: 2, company_name: 'Bravo Customers' }),
    createMockCustomer({ id: 3, company_name: 'Charlie Customers' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(customersApi.getCustomers).mockImplementation(async (filters?: CustomerFilters) => {
      const data = filters?.sort_by ? [...customers].reverse() : customers;
      return { data, total: data.length, page: 1, limit: 10, total_pages: 1 };
    });
  });

  it('opens a ticket for the chosen customer after the table was re-sorted', async () => {
    signedInAs('admin');
    render(<CustomerList />);
    await screen.findByText('Alpha Customers');

    fireEvent.click(screen.getByRole('button', { name: 'Company' }));
    await waitFor(() =>
      expect(screen.getAllByRole('row')[1]).toHaveTextContent('Charlie Customers'),
    );

    fireEvent.click(screen.getByRole('button', { name: 'More actions for Bravo Customers' }));
    const menu = await screen.findByRole('menu', { name: 'More actions for Bravo Customers' });
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Create Ticket' }));

    expect(mockNavigate).toHaveBeenCalledWith('/tickets/new?customer_id=2');
    // The menu item must not fall through to the row's own click.
    expect(mockNavigate).not.toHaveBeenCalledWith('/customers/2');
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument());
  });

  it.each(['admin', 'support'] as const)(
    'offers Create Ticket to %s on every row',
    async (role) => {
      signedInAs(role);
      render(<CustomerList />);
      await screen.findByText('Alpha Customers');

      expect(screen.getAllByRole('button', { name: /^More actions for / })).toHaveLength(3);
    },
  );

  it.each(['sales', 'customer'] as const)('shows no overflow menu to %s', async (role) => {
    signedInAs(role);
    render(<CustomerList />);
    await screen.findByText('Alpha Customers');

    expect(screen.queryByRole('button', { name: /^More actions for / })).not.toBeInTheDocument();
    expect(screen.queryByTestId('MoreVertIcon')).not.toBeInTheDocument();
  });

  it.each([
    ['admin', true],
    ['sales', true],
    ['support', false],
    ['customer', false],
  ] as const)('shows Add Customer to %s: %s', async (role, visible) => {
    signedInAs(role);
    render(<CustomerList />);
    await screen.findByText('Alpha Customers');

    const button = screen.queryByRole('button', { name: 'Add Customer' });
    if (visible) {
      expect(button).toBeInTheDocument();
    } else {
      expect(button).not.toBeInTheDocument();
    }
  });
});
