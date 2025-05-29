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
  MoreVert as MoreVertIcon,
  Block as BlockIcon,
  CheckCircle as ActiveIcon,
} from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { usersApi, type UserFilters } from '@/api/endpoints';
import type { User } from '@/types';
import { format } from 'date-fns';
import { useAuth } from '@/contexts/AuthContext';

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
  
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);
  const [selectedUser, setSelectedUser] = useState<User | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['users', filters],
    queryFn: () => usersApi.getUsers(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: usersApi.deleteUser,
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
    mutationFn: usersApi.activateUser,
    onSuccess: () => {
      showSuccess('User activated successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
    },
    onError: () => {
      showError('Failed to activate user');
    },
  });

  const deactivateMutation = useMutation({
    mutationFn: usersApi.deactivateUser,
    onSuccess: () => {
      showSuccess('User deactivated successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
    },
    onError: () => {
      showError('Failed to deactivate user');
    },
  });

  const columns: Column<User>[] = [
    {
      id: 'username',
      label: 'Username',
      minWidth: 150,
    },
    {
      id: 'name',
      label: 'Name',
      minWidth: 200,
      format: (_value: any, row: User) => `${row.first_name} ${row.last_name}`,
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
      format: (value: string) => format(new Date(value), 'MMM dd, yyyy'),
    },
  ];

  const handleMenuOpen = (event: React.MouseEvent<HTMLElement>, user: User) => {
    setAnchorEl(event.currentTarget);
    setSelectedUser(user);
  };

  const handleMenuClose = () => {
    setAnchorEl(null);
    setSelectedUser(null);
  };

  const handleDelete = () => {
    if (selectedUser) {
      setDeleteDialog({ open: true, user: selectedUser });
      handleMenuClose();
    }
  };

  const handleActivationToggle = () => {
    if (selectedUser) {
      if (selectedUser.is_active) {
        deactivateMutation.mutate(selectedUser.id);
      } else {
        activateMutation.mutate(selectedUser.id);
      }
      handleMenuClose();
    }
  };

  const handleSearch = (value: string) => {
    setFilters({ ...filters, search: value, page: 1 });
  };

  const handleRoleChange = (role: string) => {
    setFilters({ ...filters, role, page: 1 });
  };

  const handleStatusChange = (_event: React.MouseEvent<HTMLElement>, value: string | null) => {
    if (value === null) return;
    setFilters({ 
      ...filters, 
      is_active: value === 'all' ? undefined : value === 'active',
      page: 1 
    });
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
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(user) => navigate(`/users/${user.id}`)}
        onEdit={isAdmin ? (user) => navigate(`/users/${user.id}/edit`) : undefined}
        onDelete={isAdmin ? (user) => setDeleteDialog({ open: true, user }) : undefined}
        actions={
          isAdmin ? (
            <>
              <IconButton
                size="small"
                onClick={(e) => selectedUser && handleMenuOpen(e, selectedUser)}
              >
                <MoreVertIcon />
              </IconButton>
              <Menu
                anchorEl={anchorEl}
                open={Boolean(anchorEl)}
                onClose={handleMenuClose}
              >
                <MenuItem onClick={() => selectedUser && navigate(`/users/${selectedUser.id}`)}>
                  View Details
                </MenuItem>
                <MenuItem onClick={() => selectedUser && navigate(`/users/${selectedUser.id}/edit`)}>
                  Edit
                </MenuItem>
                <MenuItem onClick={handleActivationToggle}>
                  {selectedUser?.is_active ? 'Deactivate' : 'Activate'}
                </MenuItem>
                <MenuItem onClick={handleDelete} disabled={selectedUser?.id === currentUser?.id}>
                  Delete
                </MenuItem>
              </Menu>
            </>
          ) : undefined
        }
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete User"
        message={`Are you sure you want to delete the user "${deleteDialog.user?.username}"? This action cannot be undone.`}
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