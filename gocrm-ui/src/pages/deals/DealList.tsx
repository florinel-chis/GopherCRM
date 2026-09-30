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
  FormControl,
  FormControlLabel,
  InputLabel,
  MenuItem,
  Select,
  Switch,
} from '@mui/material';
import { Add as AddIcon, Search as SearchIcon } from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { dealsApi, type DealFilters, type DealSortColumn } from '@/api/endpoints';
import { DEAL_STAGES, isClosedDealStage, type Deal, type DealStage } from '@/types';
import { DealStageChip } from './DealStageChip';
import { formatCalendarDate, formatDealAmount, isPastCalendarDate } from './dealFormat';

// Columns the API accepts in sort_by (its allowlist); the others render
// without a sort header so a click can never produce a rejected request.
const SORTABLE_COLUMNS = new Set<string>([
  'title',
  'stage',
  'amount_cents',
  'probability',
  'expected_close_date',
  'created_at',
]);

interface ListState {
  page: number;
  limit: number;
  search: string;
  stage: DealStage | '';
  openOnly: boolean;
  sort_by?: DealSortColumn;
  sort_order?: 'asc' | 'desc';
}

// The company when linked, otherwise the customer, otherwise the lead.
const dealAccountName = (deal: Deal): string => {
  if (deal.company) {
    return deal.company.name;
  }
  if (deal.customer) {
    return deal.customer.company_name || deal.customer.contact_name || deal.customer.email;
  }
  if (deal.lead) {
    return deal.lead.company_name || deal.lead.contact_name || deal.lead.email;
  }
  return '—';
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  // Deals are admin and sales only on the API; the route guard keeps the
  // other roles out, and only admin deletes.
  const canWrite = user?.role === 'admin' || user?.role === 'sales';
  const canDelete = user?.role === 'admin';

  const [state, setState] = useState<ListState>({
    page: 1,
    limit: 10,
    search: '',
    stage: '',
    openOnly: false,
  });
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; deal?: Deal }>({ open: false });

  const filters: DealFilters = useMemo(
    () => ({
      offset: (state.page - 1) * state.limit,
      limit: state.limit,
      search: state.search || undefined,
      stage: state.stage || undefined,
      open: state.openOnly || undefined,
      sort_by: state.sort_by,
      sort_order: state.sort_order,
    }),
    [state]
  );

  const { data, isLoading } = useQuery({
    queryKey: ['deals', filters],
    queryFn: () => dealsApi.getDeals(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => dealsApi.deleteDeal(id),
    onSuccess: () => {
      showSuccess('Deal deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['deals'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete deal');
    },
  });

  const columns: Column<Deal>[] = useMemo(
    () => [
      { id: 'title', label: 'Title', minWidth: 200 },
      {
        id: 'account',
        label: 'Company / Customer',
        minWidth: 160,
        sortable: false,
        format: (_value: unknown, row: Deal) => dealAccountName(row),
      },
      {
        id: 'stage',
        label: 'Stage',
        minWidth: 120,
        format: (value: DealStage) => <DealStageChip stage={value} />,
      },
      {
        id: 'amount_cents',
        label: 'Amount',
        minWidth: 120,
        align: 'right',
        format: (value: number, row: Deal) => formatDealAmount(value, row.currency),
      },
      {
        id: 'probability',
        label: 'Probability',
        minWidth: 100,
        align: 'right',
        format: (value: number) => `${value}%`,
      },
      {
        id: 'expected_close_date',
        label: 'Expected close',
        minWidth: 130,
        format: (value: string | null, row: Deal) => {
          const pastDue = !isClosedDealStage(row.stage) && isPastCalendarDate(value);
          return (
            <Typography
              component="span"
              variant="body2"
              color={pastDue ? 'error' : 'inherit'}
              data-testid={pastDue ? 'deal-past-due' : undefined}
            >
              {formatCalendarDate(value)}
            </Typography>
          );
        },
      },
      {
        id: 'owner',
        label: 'Owner',
        minWidth: 140,
        sortable: false,
        format: (_value: unknown, row: Deal) =>
          row.owner ? `${row.owner.first_name} ${row.owner.last_name}` : '—',
      },
    ],
    []
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    if (!SORTABLE_COLUMNS.has(field)) {
      return;
    }
    setState((prev) => ({ ...prev, sort_by: field as DealSortColumn, sort_order: order, page: 1 }));
  }, []);

  const handleSearch = useCallback((value: string) => {
    setState((prev) => ({ ...prev, search: value, page: 1 }));
  }, []);

  const handleStageFilter = useCallback((value: string) => {
    setState((prev) => ({ ...prev, stage: value as DealStage | '', page: 1 }));
  }, []);

  const handleOpenOnly = useCallback((checked: boolean) => {
    setState((prev) => ({ ...prev, openOnly: checked, page: 1 }));
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
        <Typography variant="h4">Deals</Typography>
        {canWrite && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/deals/new')}>
            New Deal
          </Button>
        )}
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center" flexWrap="wrap">
          <TextField
            size="small"
            placeholder="Search deals..."
            value={state.search}
            onChange={(e) => handleSearch(e.target.value)}
            inputProps={{ 'aria-label': 'Search deals' }}
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
            <InputLabel id="deal-stage-filter-label">Stage</InputLabel>
            <Select
              labelId="deal-stage-filter-label"
              id="deal-stage-filter"
              value={state.stage}
              onChange={(e) => handleStageFilter(e.target.value)}
              label="Stage"
              displayEmpty
              renderValue={(value: DealStage | '') =>
                value === '' ? 'All stages' : DEAL_STAGES.find((s) => s.value === value)?.label
              }
            >
              <MenuItem value="">All stages</MenuItem>
              {DEAL_STAGES.map((stage) => (
                <MenuItem key={stage.value} value={stage.value}>
                  {stage.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControlLabel
            control={
              <Switch
                checked={state.openOnly}
                onChange={(e) => handleOpenOnly(e.target.checked)}
                inputProps={{ 'aria-label': 'Open only' }}
              />
            }
            label="Open only"
          />
        </Box>
      </Paper>

      <DataTable
        columns={columns}
        data={data?.deals || []}
        totalCount={data?.total || 0}
        page={state.page - 1}
        rowsPerPage={state.limit}
        loading={isLoading}
        onSort={handleSort}
        sortBy={state.sort_by ?? ''}
        sortOrder={state.sort_order ?? 'asc'}
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(deal) => navigate(`/deals/${deal.id}`)}
        onEdit={canWrite ? (deal) => navigate(`/deals/${deal.id}/edit`) : undefined}
        onDelete={canDelete ? (deal) => setDeleteDialog({ open: true, deal }) : undefined}
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Deal"
        message={`Are you sure you want to delete the deal "${deleteDialog.deal?.title}"? Its stage history is kept. This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.deal) {
            deleteMutation.mutate(deleteDialog.deal.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};
