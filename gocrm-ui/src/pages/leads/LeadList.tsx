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
import { ConvertLeadDialog, type ConvertLeadData } from '@/components/ConvertLeadDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { useConfiguration } from '@/contexts/ConfigurationContext';
import { leadsApi, type LeadFilters } from '@/api/endpoints';
import type { Lead } from '@/types';
import { format } from 'date-fns';

const statusOptions = [
  { value: '', label: 'All Statuses' },
  { value: 'new', label: 'New' },
  { value: 'contacted', label: 'Contacted' },
  { value: 'qualified', label: 'Qualified' },
  { value: 'converted', label: 'Converted' },
  { value: 'lost', label: 'Lost' },
];

const getStatusColor = (status: Lead['status']) => {
  switch (status) {
    case 'new':
      return 'info';
    case 'contacted':
      return 'primary';
    case 'qualified':
      return 'warning';
    case 'converted':
      return 'success';
    case 'lost':
      return 'error';
    default:
      return 'default';
  }
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { getLeadConversionStatuses } = useConfiguration();
  
  const [filters, setFilters] = useState<LeadFilters>({
    page: 1,
    limit: 10,
    status: '',
    search: '',
  });
  
  const [deleteDialog, setDeleteDialog] = useState<{
    open: boolean;
    lead?: Lead;
  }>({ open: false });

  const [convertDialog, setConvertDialog] = useState<{
    open: boolean;
    lead?: Lead;
  }>({ open: false });
  
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);
  const [selectedLead, setSelectedLead] = useState<Lead | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['leads', filters],
    queryFn: () => leadsApi.getLeads(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: leadsApi.deleteLead,
    onSuccess: () => {
      showSuccess('Lead deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete lead');
    },
  });

  const convertMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: ConvertLeadData }) => leadsApi.convertLead(id, data),
    onSuccess: (data) => {
      showSuccess('Lead converted to customer successfully');
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      setConvertDialog({ open: false });
      navigate(`/customers/${data.customer_id}`);
    },
    onError: () => {
      showError('Failed to convert lead');
    },
  });

  const columns: Column<Lead>[] = [
    {
      id: 'company_name',
      label: 'Company',
      minWidth: 200,
    },
    {
      id: 'contact_name',
      label: 'Contact',
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
      id: 'status',
      label: 'Status',
      minWidth: 120,
      format: (value: Lead['status']) => (
        <Chip
          label={value.charAt(0).toUpperCase() + value.slice(1)}
          color={getStatusColor(value)}
          size="small"
        />
      ),
    },
    {
      id: 'source',
      label: 'Source',
      minWidth: 120,
    },
    {
      id: 'created_at',
      label: 'Created',
      minWidth: 120,
      format: (value: string) => format(new Date(value), 'MMM dd, yyyy'),
    },
  ];

  const handleMenuOpen = (event: React.MouseEvent<HTMLElement>, lead: Lead) => {
    setAnchorEl(event.currentTarget);
    setSelectedLead(lead);
  };

  const handleMenuClose = () => {
    setAnchorEl(null);
    setSelectedLead(null);
  };

  const handleDelete = () => {
    if (selectedLead) {
      setDeleteDialog({ open: true, lead: selectedLead });
      handleMenuClose();
    }
  };

  const handleConvert = () => {
    if (selectedLead) {
      setConvertDialog({ open: true, lead: selectedLead });
      handleMenuClose();
    }
  };

  const handleConvertConfirm = (data: ConvertLeadData) => {
    if (convertDialog.lead) {
      convertMutation.mutate({ id: convertDialog.lead.id, data });
    }
  };

  const handleSearch = (value: string) => {
    setFilters({ ...filters, search: value, page: 1 });
  };

  const handleStatusChange = (status: string) => {
    setFilters({ ...filters, status, page: 1 });
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
        <Typography variant="h4">Leads</Typography>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={() => navigate('/leads/new')}
        >
          Add Lead
        </Button>
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center">
          <TextField
            size="small"
            placeholder="Search leads..."
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
        onRowClick={(lead) => navigate(`/leads/${lead.id}`)}
        onEdit={(lead) => navigate(`/leads/${lead.id}/edit`)}
        onDelete={(lead) => setDeleteDialog({ open: true, lead })}
        actions={
          <>
            <IconButton
              size="small"
              onClick={(e) => selectedLead && handleMenuOpen(e, selectedLead)}
            >
              <MoreVertIcon />
            </IconButton>
            <Menu
              anchorEl={anchorEl}
              open={Boolean(anchorEl)}
              onClose={handleMenuClose}
            >
              <MenuItem onClick={() => selectedLead && navigate(`/leads/${selectedLead.id}`)}>
                View Details
              </MenuItem>
              <MenuItem onClick={() => selectedLead && navigate(`/leads/${selectedLead.id}/edit`)}>
                Edit
              </MenuItem>
              {getLeadConversionStatuses().includes(selectedLead?.status || '') && (
                <MenuItem onClick={handleConvert}>Convert to Customer</MenuItem>
              )}
              <MenuItem onClick={handleDelete}>Delete</MenuItem>
            </Menu>
          </>
        }
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Lead"
        message={`Are you sure you want to delete the lead "${deleteDialog.lead?.company_name}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.lead) {
            deleteMutation.mutate(deleteDialog.lead.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />

      <ConvertLeadDialog
        open={convertDialog.open}
        lead={convertDialog.lead || null}
        onClose={() => setConvertDialog({ open: false })}
        onConfirm={handleConvertConfirm}
        isLoading={convertMutation.isPending}
      />
    </Box>
  );
};