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
import { ticketsApi, type TicketFilters } from '@/api/endpoints';
import type { Ticket } from '@/types';
import { format } from 'date-fns';

const statusOptions = [
  { value: '', label: 'All Statuses' },
  { value: 'open', label: 'Open' },
  { value: 'in_progress', label: 'In Progress' },
  { value: 'resolved', label: 'Resolved' },
  { value: 'closed', label: 'Closed' },
];

const priorityOptions = [
  { value: '', label: 'All Priorities' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'urgent', label: 'Urgent' },
];

const getStatusColor = (status: Ticket['status']) => {
  switch (status) {
    case 'open':
      return 'info';
    case 'in_progress':
      return 'warning';
    case 'resolved':
      return 'success';
    case 'closed':
      return 'default';
    default:
      return 'default';
  }
};

const getPriorityColor = (priority: Ticket['priority']) => {
  switch (priority) {
    case 'low':
      return 'default';
    case 'medium':
      return 'primary';
    case 'high':
      return 'warning';
    case 'urgent':
      return 'error';
    default:
      return 'default';
  }
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  
  const [filters, setFilters] = useState<TicketFilters>({
    page: 1,
    limit: 10,
    status: '',
    priority: '',
    search: '',
  });
  
  const [deleteDialog, setDeleteDialog] = useState<{
    open: boolean;
    ticket?: Ticket;
  }>({ open: false });
  
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);
  const [selectedTicket, setSelectedTicket] = useState<Ticket | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['tickets', filters],
    queryFn: () => ticketsApi.getTickets(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: ticketsApi.deleteTicket,
    onSuccess: () => {
      showSuccess('Ticket deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete ticket');
    },
  });

  const columns: Column<Ticket>[] = [
    {
      id: 'id',
      label: 'Ticket #',
      minWidth: 80,
      format: (value: number) => `#${value}`,
    },
    {
      id: 'subject',
      label: 'Subject',
      minWidth: 250,
    },
    {
      id: 'customer',
      label: 'Customer',
      minWidth: 150,
      format: (value: any) => value?.company_name || 'N/A',
    },
    {
      id: 'status',
      label: 'Status',
      minWidth: 100,
      format: (value: Ticket['status']) => (
        <Chip
          label={value.split('_').map(s => s.charAt(0).toUpperCase() + s.slice(1)).join(' ')}
          color={getStatusColor(value)}
          size="small"
        />
      ),
    },
    {
      id: 'priority',
      label: 'Priority',
      minWidth: 90,
      format: (value: Ticket['priority']) => (
        <Chip
          label={value.charAt(0).toUpperCase() + value.slice(1)}
          color={getPriorityColor(value)}
          size="small"
        />
      ),
    },
    {
      id: 'assigned_to',
      label: 'Assigned To',
      minWidth: 120,
      format: (value: any) => {
        if (value) {
          return `${value.first_name} ${value.last_name}`;
        }
        return 'Unassigned';
      },
    },
    {
      id: 'created_at',
      label: 'Created',
      minWidth: 100,
      format: (value: string) => format(new Date(value), 'MMM dd, yyyy'),
    },
    {
      id: 'updated_at',
      label: 'Last Updated',
      minWidth: 100,
      format: (value: string) => format(new Date(value), 'MMM dd, yyyy'),
    },
  ];

  const handleMenuOpen = (event: React.MouseEvent<HTMLElement>, ticket: Ticket) => {
    setAnchorEl(event.currentTarget);
    setSelectedTicket(ticket);
  };

  const handleMenuClose = () => {
    setAnchorEl(null);
    setSelectedTicket(null);
  };

  const handleDelete = () => {
    if (selectedTicket) {
      setDeleteDialog({ open: true, ticket: selectedTicket });
      handleMenuClose();
    }
  };

  const handleSearch = (value: string) => {
    setFilters({ ...filters, search: value, page: 1 });
  };

  const handleStatusChange = (status: string) => {
    setFilters({ ...filters, status, page: 1 });
  };

  const handlePriorityChange = (priority: string) => {
    setFilters({ ...filters, priority, page: 1 });
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
        <Typography variant="h4">Tickets</Typography>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={() => navigate('/tickets/new')}
        >
          Create Ticket
        </Button>
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center" flexWrap="wrap">
          <TextField
            size="small"
            placeholder="Search tickets..."
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
            <InputLabel>Status</InputLabel>
            <Select
              value={filters.status || ''}
              onChange={(e) => handleStatusChange(e.target.value)}
              label="Status"
            >
              {statusOptions.map((option) => (
                <MenuItem key={option.value} value={option.value}>
                  {option.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl size="small" sx={{ minWidth: 150 }}>
            <InputLabel>Priority</InputLabel>
            <Select
              value={filters.priority || ''}
              onChange={(e) => handlePriorityChange(e.target.value)}
              label="Priority"
            >
              {priorityOptions.map((option) => (
                <MenuItem key={option.value} value={option.value}>
                  {option.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
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
        onRowClick={(ticket) => navigate(`/tickets/${ticket.id}`)}
        onEdit={(ticket) => navigate(`/tickets/${ticket.id}/edit`)}
        onDelete={(ticket) => setDeleteDialog({ open: true, ticket })}
        actions={
          <>
            <IconButton
              size="small"
              onClick={(e) => selectedTicket && handleMenuOpen(e, selectedTicket)}
            >
              <MoreVertIcon />
            </IconButton>
            <Menu
              anchorEl={anchorEl}
              open={Boolean(anchorEl)}
              onClose={handleMenuClose}
            >
              <MenuItem onClick={() => selectedTicket && navigate(`/tickets/${selectedTicket.id}`)}>
                View Details
              </MenuItem>
              <MenuItem onClick={() => selectedTicket && navigate(`/tickets/${selectedTicket.id}/edit`)}>
                Edit
              </MenuItem>
              <MenuItem onClick={handleDelete}>Delete</MenuItem>
            </Menu>
          </>
        }
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Ticket"
        message={`Are you sure you want to delete ticket #${deleteDialog.ticket?.id}? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.ticket) {
            deleteMutation.mutate(deleteDialog.ticket.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};