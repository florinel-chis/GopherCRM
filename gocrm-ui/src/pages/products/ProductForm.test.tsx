import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as ProductForm } from './ProductForm';
import { configurationsApi, productsApi } from '@/api/endpoints';
import type { Configuration } from '@/api/endpoints';
import { createMockProduct } from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  productsApi: {
    getProduct: vi.fn(),
    createProduct: vi.fn(),
    updateProduct: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn(),
  },
}));

const showError = vi.fn();
const showSuccess = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
}));

const currencyConfig = (value: string): Configuration => ({
  id: 1,
  key: 'deals.default_currency',
  value,
  type: 'string',
  category: 'deals',
  description: '',
  default_value: 'EUR',
  is_system: false,
  is_read_only: false,
  valid_values: '',
  created_at: '',
  updated_at: '',
});

const name = () => screen.getByLabelText(/^Name/);
const sku = () => screen.getByLabelText(/^SKU/);
const price = () => screen.getByLabelText(/^Price/);
const currency = () => screen.getByLabelText(/^Currency/);
const submit = (label: RegExp) => fireEvent.click(screen.getByRole('button', { name: label }));

describe('ProductForm', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({});
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([currencyConfig('EUR')]);
  });

  it('submits unit_price_cents 1250 for a price of 12.50 EUR', async () => {
    vi.mocked(productsApi.createProduct).mockResolvedValue(createMockProduct({ id: 5 }));
    render(<ProductForm />);

    await waitFor(() => expect(currency()).toHaveValue('EUR'));
    fireEvent.change(name(), { target: { value: 'Widget' } });
    fireEvent.change(sku(), { target: { value: 'SKU-001' } });
    fireEvent.change(price(), { target: { value: '12.50' } });
    submit(/Create Product/);

    await waitFor(() =>
      expect(productsApi.createProduct).toHaveBeenCalledWith({
        name: 'Widget',
        sku: 'SKU-001',
        description: '',
        unit_price_cents: 1250,
        currency: 'EUR',
        is_active: true,
      })
    );
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/products'));
    expect(showSuccess).toHaveBeenCalledWith('Product created successfully');
  });

  it('rejects an empty name and a 201-character name without submitting', async () => {
    render(<ProductForm />);

    fireEvent.change(price(), { target: { value: '1' } });
    submit(/Create Product/);
    expect(await screen.findByText('Name is required')).toBeInTheDocument();
    expect(productsApi.createProduct).not.toHaveBeenCalled();

    fireEvent.change(name(), { target: { value: 'x'.repeat(201) } });
    submit(/Create Product/);
    expect(await screen.findByText('Name must be 200 characters or fewer')).toBeInTheDocument();
    expect(productsApi.createProduct).not.toHaveBeenCalled();
  });

  it('validates the SKU length, the price format and the currency code', async () => {
    render(<ProductForm />);

    fireEvent.change(name(), { target: { value: 'Widget' } });
    fireEvent.change(sku(), { target: { value: 's'.repeat(65) } });
    fireEvent.change(price(), { target: { value: '12,5' } });
    fireEvent.change(currency(), { target: { value: 'EU' } });
    submit(/Create Product/);

    expect(await screen.findByText('SKU must be 64 characters or fewer')).toBeInTheDocument();
    expect(screen.getByText('Price must be a non-negative number with at most two decimals')).toBeInTheDocument();
    expect(screen.getByText('Currency must be a 3-letter ISO code')).toBeInTheDocument();
    expect(productsApi.createProduct).not.toHaveBeenCalled();
  });

  it('takes the configured default currency until the user edits it', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([currencyConfig('ron')]);
    render(<ProductForm />);

    await waitFor(() => expect(currency()).toHaveValue('RON'));
    fireEvent.change(currency(), { target: { value: 'usd' } });
    expect(currency()).toHaveValue('USD');
  });

  it('accepts a free product at 0 and sends the active switch as given', async () => {
    vi.mocked(productsApi.createProduct).mockResolvedValue(createMockProduct({ id: 6 }));
    render(<ProductForm />);

    await waitFor(() => expect(currency()).toHaveValue('EUR'));
    fireEvent.change(name(), { target: { value: 'Free sample' } });
    fireEvent.change(price(), { target: { value: '0' } });
    fireEvent.click(screen.getByLabelText('Active'));
    submit(/Create Product/);

    await waitFor(() =>
      expect(productsApi.createProduct).toHaveBeenCalledWith(
        expect.objectContaining({ unit_price_cents: 0, is_active: false })
      )
    );
  });

  it('loads a product for editing and sends the update in cents', async () => {
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    vi.mocked(productsApi.getProduct).mockResolvedValue(
      createMockProduct({ id: 5, name: 'Gadget', sku: 'G-1', unit_price_cents: 99900, currency: 'USD' })
    );
    vi.mocked(productsApi.updateProduct).mockResolvedValue(createMockProduct({ id: 5 }));
    render(<ProductForm />);

    expect(await screen.findByRole('heading', { name: 'Edit Product' })).toBeInTheDocument();
    await waitFor(() => expect(name()).toHaveValue('Gadget'));
    expect(price()).toHaveValue('999.00');
    expect(currency()).toHaveValue('USD');

    fireEvent.change(price(), { target: { value: '1000' } });
    submit(/Update Product/);

    await waitFor(() =>
      expect(productsApi.updateProduct).toHaveBeenCalledWith(
        5,
        expect.objectContaining({ name: 'Gadget', sku: 'G-1', unit_price_cents: 100000, currency: 'USD' })
      )
    );
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/products'));
  });

  it('shows the server message when the API refuses the product', async () => {
    const rejection = Object.assign(new Error('bad request'), {
      response: { status: 400, data: { code: 'VALIDATION_ERROR', message: 'sku is too long (at most 64 characters)' } },
    });
    vi.mocked(productsApi.createProduct).mockRejectedValue(rejection);
    render(<ProductForm />);

    await waitFor(() => expect(currency()).toHaveValue('EUR'));
    fireEvent.change(name(), { target: { value: 'Widget' } });
    fireEvent.change(price(), { target: { value: '1' } });
    submit(/Create Product/);

    await waitFor(() => expect(showError).toHaveBeenCalledWith('sku is too long (at most 64 characters)'));
  });
});
