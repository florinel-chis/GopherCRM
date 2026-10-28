import React, { useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Chip,
  FormControl,
  InputAdornment,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  TextField,
  Typography,
  Button,
} from '@mui/material';
import { Add as AddIcon, Search as SearchIcon } from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { productsApi, type ProductFilters, type ProductSortColumn } from '@/api/endpoints';
import type { Product } from '@/types';
import { formatMoney } from '@/pages/quotes/quoteFormat';

// Columns the API accepts in sort_by (its allowlist); a click elsewhere can
// never produce a rejected request.
const SORTABLE_COLUMNS = new Set<string>(['name', 'sku', 'unit_price_cents', 'currency', 'is_active', 'created_at']);

type ActiveFilter = '' | 'active' | 'inactive';

interface ListState {
  page: number;
  limit: number;
  search: string;
  active: ActiveFilter;
  sort_by?: ProductSortColumn;
  sort_order?: 'asc' | 'desc';
}

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  // Admin and sales read the catalogue (the route guard keeps the other roles
  // out); only admin writes it.
  const isAdmin = user?.role === 'admin';

  const [state, setState] = useState<ListState>({ page: 1, limit: 10, search: '', active: '' });
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; product?: Product }>({ open: false });

  const filters: ProductFilters = useMemo(
    () => ({
      offset: (state.page - 1) * state.limit,
      limit: state.limit,
      search: state.search || undefined,
      is_active: state.active === '' ? undefined : state.active === 'active',
      sort_by: state.sort_by,
      sort_order: state.sort_order,
    }),
    [state]
  );

  const { data, isLoading } = useQuery({
    queryKey: ['products', filters],
    queryFn: () => productsApi.getProducts(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => productsApi.deleteProduct(id),
    onSuccess: () => {
      showSuccess('Product deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['products'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete product');
    },
  });

  const columns: Column<Product>[] = useMemo(
    () => [
      { id: 'name', label: 'Name', minWidth: 200 },
      { id: 'sku', label: 'SKU', minWidth: 120 },
      {
        id: 'unit_price_cents',
        label: 'Price',
        minWidth: 120,
        align: 'right',
        format: (value: number, row: Product) => formatMoney(value, row.currency),
      },
      {
        id: 'is_active',
        label: 'Status',
        minWidth: 100,
        format: (value: boolean) => (
          <Chip
            label={value ? 'Active' : 'Inactive'}
            color={value ? 'success' : 'default'}
            size="small"
            data-testid="product-active-chip"
          />
        ),
      },
    ],
    []
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    if (!SORTABLE_COLUMNS.has(field)) {
      return;
    }
    setState((prev) => ({ ...prev, sort_by: field as ProductSortColumn, sort_order: order, page: 1 }));
  }, []);

  const handleSearch = useCallback((value: string) => {
    setState((prev) => ({ ...prev, search: value, page: 1 }));
  }, []);

  const handleActiveFilter = useCallback((value: string) => {
    setState((prev) => ({ ...prev, active: value as ActiveFilter, page: 1 }));
  }, []);

  const handlePageChange = useCallback((page: number) => {
    setState((prev) => ({ ...prev, page: page + 1 }));
  }, []);

  const handleRowsPerPageChange = useCallback((rowsPerPage: number) => {
    setState((prev) => ({ ...prev, limit: rowsPerPage, page: 1 }));
  }, []);

  if (isLoading && !data) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Products</Typography>
        {isAdmin && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/products/new')}>
            New Product
          </Button>
        )}
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center" flexWrap="wrap">
          <TextField
            size="small"
            placeholder="Search products..."
            value={state.search}
            onChange={(e) => handleSearch(e.target.value)}
            inputProps={{ 'aria-label': 'Search products' }}
            InputProps={{
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon />
                </InputAdornment>
              ),
            }}
            sx={{ minWidth: 300 }}
          />

          <FormControl size="small" sx={{ minWidth: 170 }}>
            <InputLabel id="product-active-filter-label">Active</InputLabel>
            <Select
              labelId="product-active-filter-label"
              id="product-active-filter"
              value={state.active}
              onChange={(e) => handleActiveFilter(e.target.value)}
              label="Active"
              displayEmpty
              renderValue={(value: ActiveFilter) =>
                value === '' ? 'All products' : value === 'active' ? 'Active only' : 'Inactive only'
              }
            >
              <MenuItem value="">All products</MenuItem>
              <MenuItem value="active">Active only</MenuItem>
              <MenuItem value="inactive">Inactive only</MenuItem>
            </Select>
          </FormControl>
        </Box>
      </Paper>

      <DataTable
        columns={columns}
        data={data?.products || []}
        totalCount={data?.total || 0}
        page={state.page - 1}
        rowsPerPage={state.limit}
        loading={isLoading}
        onSort={handleSort}
        sortBy={state.sort_by ?? ''}
        sortOrder={state.sort_order ?? 'asc'}
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onEdit={isAdmin ? (product) => navigate(`/products/${product.id}/edit`) : undefined}
        onDelete={isAdmin ? (product) => setDeleteDialog({ open: true, product }) : undefined}
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Product"
        message={`Are you sure you want to delete "${deleteDialog.product?.name}"? Quote lines priced from it keep their copied name and price. This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.product) {
            deleteMutation.mutate(deleteDialog.product.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};
