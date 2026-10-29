import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import type { Product } from '@/types';

// GET /products takes offset/limit, a substring search over name and SKU,
// the is_active and currency filters and a sort column from the API
// allowlist.
export type ProductSortColumn =
  | 'id'
  | 'name'
  | 'sku'
  | 'unit_price_cents'
  | 'currency'
  | 'is_active'
  | 'created_at'
  | 'updated_at';

export interface ProductFilters {
  offset?: number;
  limit?: number;
  search?: string;
  is_active?: boolean;
  currency?: string;
  sort_by?: ProductSortColumn;
  sort_order?: 'asc' | 'desc';
}

export interface ProductListResult {
  products: Product[];
  total: number;
}

// The body of POST /products. The price is integer minor units; an omitted
// currency takes deals.default_currency and an omitted is_active is true.
export interface CreateProductData {
  name: string;
  sku?: string;
  description?: string;
  unit_price_cents: number;
  currency?: string;
  is_active?: boolean;
}

// PUT /products/:id is a partial update: every absent field keeps its value.
export type UpdateProductData = Partial<CreateProductData>;

// The optional columns may be absent on the wire.
export const transformProductFromBackend = (input: unknown): Product => {
  const raw = input as Partial<Product> & { id: number };
  return {
    id: raw.id,
    name: raw.name ?? '',
    sku: raw.sku ?? '',
    description: raw.description ?? '',
    unit_price_cents: raw.unit_price_cents ?? 0,
    currency: raw.currency ?? '',
    is_active: raw.is_active ?? false,
    created_at: raw.created_at ?? '',
    updated_at: raw.updated_at ?? '',
  };
};

export const productsApi = {
  // The array is the envelope payload; the pagination totals only exist in
  // `meta`, which the client interceptor re-attaches to the response.
  getProducts: async (filters?: ProductFilters): Promise<ProductListResult> => {
    const response = (await api.get<unknown[]>('/products', {
      params: filters,
    })) as ApiResponseWithMeta<unknown[]>;
    const rows = Array.isArray(response.data) ? response.data : [];
    const products = rows.map(transformProductFromBackend);
    return { products, total: response.meta?.total ?? products.length };
  },

  getProduct: async (id: number): Promise<Product> => {
    const response = await api.get<unknown>(`/products/${id}`);
    return transformProductFromBackend(response.data);
  },

  // Admin only; 400 VALIDATION_ERROR names the field in its message.
  createProduct: async (data: CreateProductData): Promise<Product> => {
    const response = await api.post<unknown>('/products', data);
    return transformProductFromBackend(response.data);
  },

  updateProduct: async (id: number, data: UpdateProductData): Promise<Product> => {
    const response = await api.put<unknown>(`/products/${id}`, data);
    return transformProductFromBackend(response.data);
  },

  // Admin only; a soft delete, existing quote lines keep their copied values.
  deleteProduct: async (id: number): Promise<void> => {
    await api.delete(`/products/${id}`);
  },
};
