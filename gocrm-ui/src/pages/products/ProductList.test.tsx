import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as ProductList } from './ProductList';
import { productsApi } from '@/api/endpoints';
import { createMockProduct, createMockUser } from '@/test/factories';
import { useNavigate } from 'react-router-dom';
import type { User } from '@/types';
import { formatMoney } from '@/pages/quotes/quoteFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  productsApi: {
    getProducts: vi.fn(),
    deleteProduct: vi.fn(),
  },
}));

const showError = vi.fn();
const showSuccess = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
}));

const mockUseAuth = vi.fn();
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

const rowOf = async (text: string) => {
  const cell = await screen.findByText(text);
  return cell.closest('tr') as HTMLElement;
};

describe('ProductList', () => {
  const mockNavigate = vi.fn();
  const widget = createMockProduct({ id: 1, name: 'Widget', sku: 'SKU-001', unit_price_cents: 1250, currency: 'EUR' });
  const gadget = createMockProduct({
    id: 2,
    name: 'Gadget',
    sku: 'SKU-002',
    unit_price_cents: 99900,
    currency: 'USD',
    is_active: false,
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(productsApi.getProducts).mockResolvedValue({ products: [widget, gadget], total: 25 });
  });

  it('renders name, SKU, formatted price and active state from the products response', async () => {
    render(<ProductList />);

    const widgetRow = await rowOf('Widget');
    expect(within(widgetRow).getByText('SKU-001')).toBeInTheDocument();
    expect(within(widgetRow).getByText(formatMoney(1250, 'EUR'))).toBeInTheDocument();
    expect(within(widgetRow).getByTestId('product-active-chip')).toHaveTextContent('Active');

    const gadgetRow = await rowOf('Gadget');
    expect(within(gadgetRow).getByText('SKU-002')).toBeInTheDocument();
    expect(within(gadgetRow).getByText(formatMoney(99900, 'USD'))).toBeInTheDocument();
    expect(within(gadgetRow).getByTestId('product-active-chip')).toHaveTextContent('Inactive');
  });

  it('shows the New, Edit and Delete controls for admin and hides them for sales', async () => {
    const { unmount } = render(<ProductList />);

    expect(await screen.findByRole('button', { name: 'New Product' })).toBeInTheDocument();
    const adminRow = await rowOf('Widget');
    expect(adminRow.querySelector('svg[data-testid="EditIcon"]')).not.toBeNull();
    expect(adminRow.querySelector('svg[data-testid="DeleteIcon"]')).not.toBeNull();
    unmount();

    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
    render(<ProductList />);

    const salesRow = await rowOf('Widget');
    expect(screen.queryByRole('button', { name: 'New Product' })).not.toBeInTheDocument();
    expect(salesRow.querySelector('svg[data-testid="EditIcon"]')).toBeNull();
    expect(salesRow.querySelector('svg[data-testid="DeleteIcon"]')).toBeNull();
  });

  it('refetches with the active filter and server pagination parameters', async () => {
    render(<ProductList />);
    await rowOf('Widget');

    expect(productsApi.getProducts).toHaveBeenCalledWith({
      offset: 0,
      limit: 10,
      search: undefined,
      is_active: undefined,
      sort_by: undefined,
      sort_order: undefined,
    });

    fireEvent.mouseDown(screen.getByRole('combobox', { name: /Active/ }));
    fireEvent.click(await screen.findByRole('option', { name: 'Active only' }));
    await waitFor(() =>
      expect(productsApi.getProducts).toHaveBeenLastCalledWith(expect.objectContaining({ is_active: true, offset: 0 }))
    );

    // The list re-mounts while the new page loads, so the pager is looked up again.
    await rowOf('Widget');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(productsApi.getProducts).toHaveBeenLastCalledWith(
        expect.objectContaining({ is_active: true, offset: 10, limit: 10 })
      )
    );
  });

  it('sorts by an allowlisted column and sends the search text server-side', async () => {
    render(<ProductList />);
    await rowOf('Widget');

    fireEvent.click(screen.getByRole('button', { name: /^Price/ }));
    await waitFor(() =>
      expect(productsApi.getProducts).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort_by: 'unit_price_cents', sort_order: 'asc' })
      )
    );

    await rowOf('Widget');
    fireEvent.change(screen.getByLabelText('Search products'), { target: { value: 'gad' } });
    await waitFor(() =>
      expect(productsApi.getProducts).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'gad', offset: 0 }))
    );
  });

  it('lets an admin create, edit and delete with confirmation', async () => {
    vi.mocked(productsApi.deleteProduct).mockResolvedValue();
    render(<ProductList />);

    fireEvent.click(await screen.findByRole('button', { name: 'New Product' }));
    expect(mockNavigate).toHaveBeenCalledWith('/products/new');

    const row = await rowOf('Widget');
    fireEvent.click(row.querySelector('button svg[data-testid="EditIcon"]')?.parentElement as HTMLElement);
    expect(mockNavigate).toHaveBeenCalledWith('/products/1/edit');

    fireEvent.click(row.querySelector('button svg[data-testid="DeleteIcon"]')?.parentElement as HTMLElement);
    const dialog = await screen.findByRole('dialog', { name: 'Delete Product' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(productsApi.deleteProduct).toHaveBeenCalledWith(1));
    await waitFor(() => expect(showSuccess).toHaveBeenCalledWith('Product deleted successfully'));
  });
});
