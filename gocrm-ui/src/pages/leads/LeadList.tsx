import { getAPIError } from '@/utils/apiError';
import React, { useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Alert,
  Button,
  Chip,
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
  PersonAdd as ConvertIcon,
} from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { RowActionsMenu } from '@/components/RowActionsMenu';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { ConvertLeadDialog, type ConvertLeadData } from '@/components/ConvertLeadDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { useConfiguration } from '@/hooks/useConfiguration';
import { leadsApi, type LeadFilters } from '@/api/endpoints';
import type { Lead } from '@/types';
import { formatDate } from '@/utils/date';

const statusOptions = [
  { value: '', label: 'All Statuses' },
  { value: 'new', label: 'New' },
  { value: 'contacted', label: 'Contacted' },
  { value: 'qualified', label: 'Qualified' },
  { value: 'converted', label: 'Converted' },
  { value: 'lost', label: 'Lost' },
];

const classificationOptions = [
  { value: '', label: 'All Classifications' },
  { value: 'hot_lead', label: 'Hot Lead' },
  { value: 'lead', label: 'Lead' },
  { value: 'unclassified', label: 'Unclassified' },
  { value: 'spam', label: 'Spam' },
  { value: 'test', label: 'Test' },
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

const getClassificationColor = (classification: string) => {
  switch (classification) {
    case 'hot_lead':
      return 'error';
    case 'lead':
      return 'success';
    case 'spam':
      return 'default';
    case 'test':
      return 'warning';
    case 'unclassified':
    default:
      return 'info';
  }
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { getLeadConversion, refreshConfigurations } = useConfiguration();
  const conversion = getLeadConversion();
  const conversionSettings = conversion.status === 'ready' ? conversion.settings : null;
  
  const [filters, setFilters] = useState<LeadFilters>({
    page: 1,
    limit: 10,
    status: '',
    classification: '',
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
  
  // The sorted column is tracked as the DataTable column id (filters carry the
  // mapped backend field). It has to live here, not inside DataTable: a sort
  // click switches the query key, which sends this page down its loading branch
  // and unmounts the table.
  const [sort, setSort] = useState<{ column: string; order: 'asc' | 'desc' }>({
    column: '',
    order: 'asc',
  });

  const { data, isLoading } = useQuery({
    queryKey: ['leads', filters],
    queryFn: () => leadsApi.getLeads(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => leadsApi.deleteLead(id),
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
    onError: (error: unknown) => {
      // The dialog stays open and shows the server's reason (on the notes
      // field when the notes were refused). A settings refusal means this
      // page followed stale settings; any other refusal (already converted,
      // no email) may mean a stale row.
      const apiError = getAPIError(error);
      if (apiError?.code === 'VALIDATION_ERROR') {
        void refreshConfigurations();
      }
      if (apiError?.details?.field !== 'notes') {
        queryClient.invalidateQueries({ queryKey: ['leads'] });
      }
    },
  });

  const { reset: resetConvertMutation } = convertMutation;

  const columns: Column<Lead>[] = useMemo(() => [
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
      id: 'classification',
      label: 'Classification',
      minWidth: 130,
      format: (value: string) => (
        <Chip
          label={value ? value.replace('_', ' ').charAt(0).toUpperCase() + value.replace('_', ' ').slice(1) : 'Unclassified'}
          color={getClassificationColor(value || 'unclassified')}
          size="small"
          variant="outlined"
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
      format: (value: string) => formatDate(value, 'MMM dd, yyyy'),
    },
  ], []);

  // View, edit and delete are the row's own icon buttons; the overflow menu
  // carries only what they do not, conversion, and only on leads whose status
  // allows it. Each menu closes over its own row.
  // While the conversion rules are unknown (loading, or failed to load) no
  // status is guessed: every unconverted lead shows the action disabled.
  const conversionActions = (lead: Lead) => {
    if (conversionSettings) {
      return conversionSettings.allowedStatuses.includes(lead.status)
        ? [
            {
              label: 'Convert to Customer',
              icon: <ConvertIcon fontSize="small" />,
              onClick: () => {
                resetConvertMutation();
                setConvertDialog({ open: true, lead });
              },
            },
          ]
        : [];
    }
    return lead.status === 'converted'
      ? []
      : [
          {
            label:
              conversion.status === 'loading'
                ? 'Convert to Customer (loading settings…)'
                : 'Convert to Customer (settings unavailable)',
            icon: <ConvertIcon fontSize="small" />,
            onClick: () => {},
            disabled: true,
          },
        ];
  };
  const renderRowActions = (lead: Lead) => (
    <RowActionsMenu
      label={`More actions for ${lead.company_name || lead.contact_name}`}
      actions={conversionActions(lead)}
    />
  );

  const handleConvertConfirm = useCallback((data: ConvertLeadData) => {
    if (convertDialog.lead) {
      convertMutation.mutate({ id: convertDialog.lead.id, data });
    }
  }, [convertDialog.lead, convertMutation]);

  const closeConvertDialog = useCallback(() => {
    setConvertDialog({ open: false });
    resetConvertMutation();
  }, [resetConvertMutation]);

  const handleSearch = useCallback((value: string) => {
    setFilters(prev => ({ ...prev, search: value, page: 1 }));
  }, []);

  const handleStatusChange = useCallback((status: string) => {
    setFilters(prev => ({ ...prev, status, page: 1 }));
  }, []);

  const handleClassificationChange = useCallback((classification: string) => {
    setFilters(prev => ({ ...prev, classification, page: 1 }));
  }, []);

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    // Map frontend column IDs to backend field names
    const fieldMap: Record<string, string> = {
      company_name: 'company',
      contact_name: 'first_name',
      email: 'email',
      phone: 'phone',
      status: 'status',
      classification: 'classification',
      source: 'source',
      created_at: 'created_at',
    };
    const sortBy = fieldMap[field] || field;
    setSort({ column: field, order });
    setFilters(prev => ({ ...prev, sort_by: sortBy, sort_order: order, page: 1 }));
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
        <Typography variant="h4">Leads</Typography>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={() => navigate('/leads/new')}
        >
          Add Lead
        </Button>
      </Box>

      {conversion.status === 'unavailable' && (
        <Alert
          severity="warning"
          sx={{ mb: 2 }}
          data-testid="conversion-settings-unavailable"
          action={
            <Button color="inherit" size="small" onClick={() => void refreshConfigurations()}>
              Retry
            </Button>
          }
        >
          Could not load the lead conversion settings, so leads cannot be converted from here yet.
        </Alert>
      )}

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

          <FormControl size="small" sx={{ minWidth: 170 }}>
            <InputLabel>Classification</InputLabel>
            <Select
              value={filters.classification || ''}
              onChange={(e) => handleClassificationChange(e.target.value)}
              label="Classification"
            >
              {classificationOptions.map((option) => (
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
        onSort={handleSort}
        sortBy={sort.column}
        sortOrder={sort.order}
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(lead) => navigate(`/leads/${lead.id}`)}
        onEdit={(lead) => navigate(`/leads/${lead.id}/edit`)}
        onDelete={(lead) => setDeleteDialog({ open: true, lead })}
        rowActions={renderRowActions}
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
        onClose={closeConvertDialog}
        onConfirm={handleConvertConfirm}
        isLoading={convertMutation.isPending}
        requireNotes={conversionSettings?.requireNotes}
        autoAssignOwner={conversionSettings?.autoAssignOwner}
        settingsStatus={conversion.status}
        onRetrySettings={() => void refreshConfigurations()}
        error={convertMutation.error}
      />
    </Box>
  );
};