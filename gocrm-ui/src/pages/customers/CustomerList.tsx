import React, { useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  TextField,
  InputAdornment,
} from '@mui/material';
import {
  Add as AddIcon,
  Search as SearchIcon,
  ConfirmationNumber as TicketIcon,
} from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { RowActionsMenu } from '@/components/RowActionsMenu';
import { useAuth } from '@/hooks/useAuth';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { customersApi, type CustomerFilters } from '@/api/endpoints';
import type { Customer } from '@/types';
import { formatDate } from '@/utils/date';

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { user } = useAuth();

  // POST /customers is admin and sales only; POST /tickets admin and support
  // only. The routes refuse the forms to everyone else, so do not offer them.
  const canCreateCustomer = user?.role === 'admin' || user?.role === 'sales';
  const canCreateTicket = user?.role === 'admin' || user?.role === 'support';
  
  const [filters, setFilters] = useState<CustomerFilters>({
    page: 1,
    limit: 10,
    search: '',
  });
  
  const [deleteDialog, setDeleteDialog] = useState<{
    open: boolean;
    customer?: Customer;
  }>({ open: false });
  
  // Sorted column as the DataTable column id (filters carry the mapped backend
  // field). Owned here because a sort click switches the query key and unmounts
  // the table through the loading branch below.
  const [sort, setSort] = useState<{ column: string; order: 'asc' | 'desc' }>({
    column: '',
    order: 'asc',
  });

  const { data, isLoading } = useQuery({
    queryKey: ['customers', filters],
    queryFn: () => customersApi.getCustomers(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => customersApi.deleteCustomer(id),
    onSuccess: () => {
      showSuccess('Customer deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete customer');
    },
  });

  const columns: Column<Customer>[] = useMemo(() => [
    {
      id: 'company_name',
      label: 'Company',
      minWidth: 200,
    },
    {
      id: 'contact_name',
      label: 'Primary Contact',
      minWidth: 150,
    },
    {
      id: 'email',
      label: 'Email',
      minWidth: 200,
    },
    {
      id: 'phone',
      label: 'Phone',
      minWidth: 150,
    },
    {
      id: 'total_revenue',
      label: 'Total Revenue',
      minWidth: 120,
      // Not in the customers sort allowlist (internal/utils/sort.go): the API
      // rejects the column, so offering the header would fail the list request.
      sortable: false,
      format: (value: number) => `$${value.toLocaleString()}`,
    },
    {
      id: 'created_at',
      label: 'Customer Since',
      minWidth: 120,
      format: (value: string) => formatDate(value, 'MMM dd, yyyy'),
    },
  ], []);

  // View, edit and delete are the row's own icon buttons; the overflow menu
  // carries only what they do not, opening a ticket for the customer. Each
  // menu closes over its own row.
  const renderRowActions = (customer: Customer) => (
    <RowActionsMenu
      label={`More actions for ${customer.company_name || customer.contact_name}`}
      actions={
        canCreateTicket
          ? [
              {
                label: 'Create Ticket',
                icon: <TicketIcon fontSize="small" />,
                onClick: () => navigate(`/tickets/new?customer_id=${customer.id}`),
              },
            ]
          : []
      }
    />
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    const fieldMap: Record<string, string> = {
      company_name: 'company',
      contact_name: 'first_name',
      email: 'email',
      phone: 'phone',
      created_at: 'created_at',
    };
    const sortBy = fieldMap[field] || field;
    setSort({ column: field, order });
    setFilters(prev => ({ ...prev, sort_by: sortBy, sort_order: order, page: 1 }));
  }, []);

  const handleSearch = useCallback((value: string) => {
    setFilters(prev => ({ ...prev, search: value, page: 1 }));
  }, []);

  const handlePageChange = useCallback((page: number) => {
    setFilters(prev => ({ ...prev, page: page + 1 }));
  }, []);

  const handleRowsPerPageChange = useCallback((rowsPerPage: number) => {
    setFilters(prev => ({ ...prev, limit: rowsPerPage, page: 1 }));
  }, []);

  if (isLoading && !data) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Customers</Typography>
        {canCreateCustomer && (
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => navigate('/customers/new')}
          >
            Add Customer
          </Button>
        )}
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center">
          <TextField
            size="small"
            placeholder="Search customers..."
            value={filters.search}
            onChange={(e) => handleSearch(e.target.value)}
            InputProps={{
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon />
                </InputAdornment>
              ),
            }}
            sx={{ minWidth: 300 }}
          />
        </Box>
      </Paper>

      <DataTable
        columns={columns}
        data={data?.data || []}
        totalCount={data?.total || 0}
        page={(filters.page || 1) - 1}
        rowsPerPage={filters.limit || 10}
        loading={isLoading}
        onSort={handleSort}
        sortBy={sort.column}
        sortOrder={sort.order}
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(customer) => navigate(`/customers/${customer.id}`)}
        onEdit={(customer) => navigate(`/customers/${customer.id}/edit`)}
        onDelete={(customer) => setDeleteDialog({ open: true, customer })}
        rowActions={renderRowActions}
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Customer"
        message={`Are you sure you want to delete the customer "${deleteDialog.customer?.company_name}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.customer) {
            deleteMutation.mutate(deleteDialog.customer.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};