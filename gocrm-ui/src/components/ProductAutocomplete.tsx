import React, { useCallback } from 'react';
import { productsApi } from '@/api/endpoints';
import type { Product } from '@/types';
import { RecordAutocomplete } from './RecordAutocomplete';

export interface ProductAutocompleteProps {
  value: number | null;
  onChange: (productId: number | null, product: Product | null) => void;
  /** Only products priced in this currency are offered: a line must share the quote's. */
  currency: string;
  label?: string;
  helperText?: string;
  error?: boolean;
  disabled?: boolean;
}

const SEARCH_LIMIT = 20;

// "Widget (SKU-001)", or the bare name without a SKU. Not exported: this file
// exports a component and the fast-refresh lint rule wants nothing else.
const productOptionLabel = (product: Product): string =>
  product.sku ? `${product.name} (${product.sku})` : product.name;

/**
 * Picker over GET /products?is_active=true&currency=&search=&limit=20; the
 * value is a product id. Inactive products are never offered, because the
 * API refuses them on a line.
 */
export const ProductAutocomplete: React.FC<ProductAutocompleteProps> = ({
  value,
  onChange,
  currency,
  label = 'Product',
  helperText,
  error,
  disabled,
}) => {
  const searchProducts = useCallback(
    async (term: string): Promise<Product[]> => {
      const result = await productsApi.getProducts({
        search: term || undefined,
        is_active: true,
        currency,
        limit: SEARCH_LIMIT,
      });
      return result.products;
    },
    [currency]
  );

  return (
    <RecordAutocomplete<Product>
      testId="product-autocomplete"
      value={value}
      onChange={onChange}
      label={label}
      queryKeys={['product', `products:${currency}`]}
      search={searchProducts}
      lookup={productsApi.getProduct}
      getOptionLabel={productOptionLabel}
      noOptionsText="No matching active products in this currency"
      emptySearchText="Type to search products"
      helperText={helperText}
      error={error}
      disabled={disabled}
    />
  );
};
