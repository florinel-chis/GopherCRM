import React, { useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Chip,
  MenuItem,
  FormControl,
  InputLabel,
  Select,
  TextField,
  InputAdornment,
  ToggleButton,
  ToggleButtonGroup,
} from '@mui/material';
import {
  Add as AddIcon,
  Search as SearchIcon,
  Block as BlockIcon,
  CheckCircle as ActiveIcon,
} from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { RowActionsMenu } from '@/components/RowActionsMenu';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { usersApi, type UserFilters } from '@/api/endpoints';
import type { User } from '@/types';
import { formatDate } from '@/utils/date';
import { useAuth } from '@/hooks/useAuth';

const roleOptions = [
  { value: '', label: 'All Roles' },
  { value: 'admin', label: 'Admin' },
  { value: 'sales', label: 'Sales' },
  { value: 'support', label: 'Support' },
  { value: 'customer', label: 'Customer' },
];

const getRoleColor = (role: User['role']) => {
  switch (role) {
    case 'admin':
      return 'error';
    case 'sales':
      return 'primary';
    case 'support':
      return 'warning';
    case 'customer':
      return 'default';
    default:
      return 'default';
  }
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { user: currentUser } = useAuth();
  
  const [filters, setFilters] = useState<UserFilters>({
    page: 1,
    limit: 10,
    role: '',
    is_active: undefined,
    search: '',
  });
  
  const [deleteDialog, setDeleteDialog] = useState<{
    open: boolean;
    user?: User;
  }>({ open: false });
  
  // Sorted column as the DataTable column id (filters carry the mapped backend
  // field). Owned here because a sort click switches the query key and unmounts
  // the table through the loading branch below.
  const [sort, setSort] = useState<{ column: string; order: 'asc' | 'desc' }>({
    column: '',
    order: 'asc',
  });

  const { data, isLoading } = useQuery({
    queryKey: ['users', filters],
    queryFn: () => usersApi.getUsers(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => usersApi.deleteUser(id),
    onSuccess: () => {
      showSuccess('User deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete user');
    },
  });

  const activateMutation = useMutation({
    mutationFn: (id: number) => usersApi.activateUser(id),
    onSuccess: () => {
      showSuccess('User activated successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
    },
    onError: () => {
      showError('Failed to activate user');
    },
  });

  const deactivateMutation = useMutation({
    mutationFn: (id: number) => usersApi.deactivateUser(id),
    onSuccess: () => {
      showSuccess('User deactivated successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
    },
    onError: () => {
      showError('Failed to deactivate user');
    },
  });

  const columns: Column<User>[] = useMemo(() => [
    {
      id: 'username',
      label: 'Username',
      minWidth: 150,
      // There is no `username` on the User model, so this column renders blank
      // and used to sort by `email` — an indicator on Username while the list
      // reordered by a different column. Nothing to sort by: keep it inert.
      sortable: false,
    },
    {
      id: 'name',
      label: 'Name',
      minWidth: 200,
      // Sorts by `first_name`, the leading component of what is rendered, the
      // same mapping the lead and customer contact columns use.
      format: (_value: unknown, row: User) => `${row.first_name} ${row.last_name}`,
    },
    {
      id: 'email',
      label: 'Email',
      minWidth: 200,
    },
    {
      id: 'role',
      label: 'Role',
      minWidth: 100,
      format: (value: User['role']) => (
        <Chip
          label={value.charAt(0).toUpperCase() + value.slice(1)}
          color={getRoleColor(value)}
          size="small"
        />
      ),
    },
    {
      id: 'is_active',
      label: 'Status',
      minWidth: 100,
      format: (value: boolean) => (
        <Chip
          icon={value ? <ActiveIcon /> : <BlockIcon />}
          label={value ? 'Active' : 'Inactive'}
          color={value ? 'success' : 'default'}
          size="small"
        />
      ),
    },
    {
      id: 'created_at',
      label: 'Created',
      minWidth: 120,
      format: (value: string) => formatDate(value, 'MMM dd, yyyy'),
    },
  ], []);

  // View, edit and delete are the row's own icon buttons; the overflow menu
  // carries only what they do not, activation and deactivation. Each menu
  // closes over its own row. The API refuses to deactivate the caller's own
  // account, so that item is disabled rather than offered.
  const renderRowActions = (user: User) => (
    <RowActionsMenu
      label={`More actions for ${`${user.first_name} ${user.last_name}`.trim() || user.email}`}
      actions={[
        user.is_active
          ? {
              label: 'Deactivate',
              icon: <BlockIcon fontSize="small" />,
              disabled: user.id === currentUser?.id,
              onClick: () => deactivateMutation.mutate(user.id),
            }
          : {
              label: 'Activate',
              icon: <ActiveIcon fontSize="small" />,
              onClick: () => activateMutation.mutate(user.id),
            },
      ]}
    />
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    const fieldMap: Record<string, string> = {
      name: 'first_name',
      email: 'email',
      role: 'role',
      is_active: 'is_active',
      created_at: 'created_at',
    };
    const sortBy = fieldMap[field] || field;
    setSort({ column: field, order });
    setFilters(prev => ({ ...prev, sort_by: sortBy, sort_order: order, page: 1 }));
  }, []);

  const handleSearch = useCallback((value: string) => {
    setFilters(prev => ({ ...prev, search: value, page: 1 }));
  }, []);

  const handleRoleChange = useCallback((role: string) => {
    setFilters(prev => ({ ...prev, role, page: 1 }));
  }, []);

  const handleStatusChange = useCallback((_event: React.MouseEvent<HTMLElement>, value: string | null) => {
    if (value === null) return;
    setFilters(prev => ({
      ...prev,
      is_active: value === 'all' ? undefined : value === 'active',
      page: 1
    }));
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

  const isAdmin = currentUser?.role === 'admin';

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Users</Typography>
        {isAdmin && (
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => navigate('/users/new')}
          >
            Add User
          </Button>
        )}
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center" flexWrap="wrap">
          <TextField
            size="small"
            placeholder="Search users..."
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
          
          <FormControl size="small" sx={{ minWidth: 150 }}>
            <InputLabel>Role</InputLabel>
            <Select
              value={filters.role || ''}
              onChange={(e) => handleRoleChange(e.target.value)}
              label="Role"
            >
              {roleOptions.map((option) => (
                <MenuItem key={option.value} value={option.value}>
                  {option.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <ToggleButtonGroup
            value={filters.is_active === undefined ? 'all' : filters.is_active ? 'active' : 'inactive'}
            exclusive
            onChange={handleStatusChange}
            size="small"
          >
            <ToggleButton value="all">All</ToggleButton>
            <ToggleButton value="active">Active</ToggleButton>
            <ToggleButton value="inactive">Inactive</ToggleButton>
          </ToggleButtonGroup>
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
        onRowClick={(user) => navigate(`/users/${user.id}`)}
        onEdit={isAdmin ? (user) => navigate(`/users/${user.id}/edit`) : undefined}
        onDelete={isAdmin ? (user) => setDeleteDialog({ open: true, user }) : undefined}
        rowActions={isAdmin ? renderRowActions : undefined}
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete User"
        message={`Are you sure you want to delete the user "${deleteDialog.user?.first_name} ${deleteDialog.user?.last_name}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.user) {
            deleteMutation.mutate(deleteDialog.user.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};