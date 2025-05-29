import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Chip,
  IconButton,
  Menu,
  MenuItem,
  TextField,
  InputAdornment,
} from '@mui/material';
import {
  Add as AddIcon,
  Search as SearchIcon,
  MoreVert as MoreVertIcon,
} from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { customersApi, type CustomerFilters } from '@/api/endpoints';
import type { Customer } from '@/types';
import { format } from 'date-fns';

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  
  const [filters, setFilters] = useState<CustomerFilters>({
    page: 1,
    limit: 10,
    search: '',
  });
  
  const [deleteDialog, setDeleteDialog] = useState<{
    open: boolean;
    customer?: Customer;
  }>({ open: false });
  
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);
  const [selectedCustomer, setSelectedCustomer] = useState<Customer | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['customers', filters],
    queryFn: () => customersApi.getCustomers(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: customersApi.deleteCustomer,
    onSuccess: () => {
      showSuccess('Customer deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete customer');
    },
  });

  const columns: Column<Customer>[] = [
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
      format: (value: number) => `$${value.toLocaleString()}`,
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
        />
      ),
    },
    {
      id: 'created_at',
      label: 'Customer Since',
      minWidth: 120,
      format: (value: string) => format(new Date(value), 'MMM dd, yyyy'),
    },
  ];

  const handleMenuOpen = (event: React.MouseEvent<HTMLElement>, customer: Customer) => {
    setAnchorEl(event.currentTarget);
    setSelectedCustomer(customer);
  };

  const handleMenuClose = () => {
    setAnchorEl(null);
    setSelectedCustomer(null);
  };

  const handleDelete = () => {
    if (selectedCustomer) {
      setDeleteDialog({ open: true, customer: selectedCustomer });
      handleMenuClose();
    }
  };

  const handleSearch = (value: string) => {
    setFilters({ ...filters, search: value, page: 1 });
  };

  const handlePageChange = (page: number) => {
    setFilters({ ...filters, page: page + 1 });
  };

  const handleRowsPerPageChange = (rowsPerPage: number) => {
    setFilters({ ...filters, limit: rowsPerPage, page: 1 });
  };

  if (isLoading && !data) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Customers</Typography>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={() => navigate('/customers/new')}
        >
          Add Customer
        </Button>
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
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(customer) => navigate(`/customers/${customer.id}`)}
        onEdit={(customer) => navigate(`/customers/${customer.id}/edit`)}
        onDelete={(customer) => setDeleteDialog({ open: true, customer })}
        actions={
          <>
            <IconButton
              size="small"
              onClick={(e) => selectedCustomer && handleMenuOpen(e, selectedCustomer)}
            >
              <MoreVertIcon />
            </IconButton>
            <Menu
              anchorEl={anchorEl}
              open={Boolean(anchorEl)}
              onClose={handleMenuClose}
            >
              <MenuItem onClick={() => selectedCustomer && navigate(`/customers/${selectedCustomer.id}`)}>
                View Details
              </MenuItem>
              <MenuItem onClick={() => selectedCustomer && navigate(`/customers/${selectedCustomer.id}/edit`)}>
                Edit
              </MenuItem>
              <MenuItem onClick={() => selectedCustomer && navigate(`/tickets/new?customer_id=${selectedCustomer.id}`)}>
                Create Ticket
              </MenuItem>
              <MenuItem onClick={handleDelete}>Delete</MenuItem>
            </Menu>
          </>
        }
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