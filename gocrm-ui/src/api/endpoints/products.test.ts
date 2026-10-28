import { describe, it, expect, vi, beforeEach } from 'vitest';
import { productsApi } from './products';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendProduct = {
  id: 3,
  name: 'Widget',
  sku: 'SKU-001',
  description: 'A standard widget',
  unit_price_cents: 1250,
  currency: 'EUR',
  is_active: true,
  created_at: '2026-10-26T08:00:00Z',
  updated_at: '2026-10-26T08:00:00Z',
};

describe('productsApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists products with the filters as params and reads the total from meta', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [backendProduct],
      meta: { total: 25, page: 2, per_page: 10, total_pages: 3 },
    });

    const result = await productsApi.getProducts({
      offset: 10,
      limit: 10,
      search: 'widget',
      is_active: true,
      currency: 'EUR',
      sort_by: 'unit_price_cents',
      sort_order: 'asc',
    });

    expect(api.get).toHaveBeenCalledWith('/products', {
      params: {
        offset: 10,
        limit: 10,
        search: 'widget',
        is_active: true,
        currency: 'EUR',
        sort_by: 'unit_price_cents',
        sort_order: 'asc',
      },
    });
    expect(result.products).toHaveLength(1);
    expect(result.products[0].name).toBe('Widget');
    expect(result.total).toBe(25);
  });

  it('falls back to the page length when the response carries no meta', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendProduct, { ...backendProduct, id: 4 }] });

    const result = await productsApi.getProducts();

    expect(result.total).toBe(2);
  });

  it('returns an empty list when the payload is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(productsApi.getProducts()).resolves.toEqual({ products: [], total: 0 });
  });

  it('fetches one product and normalises absent optional fields', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: { id: 3, name: 'Bare', unit_price_cents: 0, currency: 'EUR', is_active: true },
    });

    const product = await productsApi.getProduct(3);

    expect(api.get).toHaveBeenCalledWith('/products/3');
    expect(product.sku).toBe('');
    expect(product.description).toBe('');
    expect(product.unit_price_cents).toBe(0);
  });

  it('creates a product with the body in cents as given', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: backendProduct });

    const created = await productsApi.createProduct({
      name: 'Widget',
      sku: 'SKU-001',
      unit_price_cents: 1250,
      currency: 'EUR',
      is_active: true,
    });

    expect(api.post).toHaveBeenCalledWith('/products', {
      name: 'Widget',
      sku: 'SKU-001',
      unit_price_cents: 1250,
      currency: 'EUR',
      is_active: true,
    });
    expect(created.id).toBe(3);
  });

  it('updates a product at /products/{id} with PUT', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...backendProduct, is_active: false } });

    const updated = await productsApi.updateProduct(3, { is_active: false });

    expect(api.put).toHaveBeenCalledWith('/products/3', { is_active: false });
    expect(updated.is_active).toBe(false);
  });

  it('deletes a product at /products/{id}', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    await productsApi.deleteProduct(3);

    expect(api.delete).toHaveBeenCalledWith('/products/3');
  });

  it('propagates a VALIDATION_ERROR rejection to the caller', async () => {
    const rejection = Object.assign(new Error('bad request'), {
      response: { status: 400, data: { code: 'VALIDATION_ERROR', message: 'name is required', details: null } },
    });
    vi.mocked(api.post).mockRejectedValue(rejection);

    await expect(productsApi.createProduct({ name: '', unit_price_cents: 0 })).rejects.toBe(rejection);
  });
});
