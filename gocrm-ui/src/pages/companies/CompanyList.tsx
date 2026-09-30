import React, { useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { Box, Paper, Typography, Button, TextField, InputAdornment } from '@mui/material';
import { Add as AddIcon, Search as SearchIcon } from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { companiesApi, type CompanyFilters } from '@/api/endpoints';
import type { Company } from '@/types';
import { formatDate } from '@/utils/date';

// Columns the API accepts in sort_by (its allowlist); the others render
// without a sort header so a click can never produce a rejected request.
const SORTABLE_COLUMNS = new Set<string>(['name', 'domain', 'industry', 'created_at', 'updated_at']);

interface ListState {
  page: number;
  limit: number;
  search: string;
  sort_by?: CompanyFilters['sort_by'];
  sort_order?: 'asc' | 'desc';
}

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  // Mirrors the API policy: admin and sales write, only admin deletes;
  // support reads.
  const canWrite = user?.role === 'admin' || user?.role === 'sales';
  const canDelete = user?.role === 'admin';

  const [state, setState] = useState<ListState>({ page: 1, limit: 10, search: '' });
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; company?: Company }>({
    open: false,
  });

  const filters: CompanyFilters = useMemo(
    () => ({
      offset: (state.page - 1) * state.limit,
      limit: state.limit,
      search: state.search || undefined,
      sort_by: state.sort_by,
      sort_order: state.sort_order,
    }),
    [state]
  );

  const { data, isLoading } = useQuery({
    queryKey: ['companies', filters],
    queryFn: () => companiesApi.getCompanies(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => companiesApi.deleteCompany(id),
    onSuccess: () => {
      showSuccess('Company deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['companies'] });
      // Linked leads and customers lose their company_id server-side.
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      queryClient.invalidateQueries({ queryKey: ['customer'] });
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      queryClient.invalidateQueries({ queryKey: ['lead'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete company');
    },
  });

  const columns: Column<Company>[] = useMemo(
    () => [
      { id: 'name', label: 'Name', minWidth: 200 },
      { id: 'domain', label: 'Domain', minWidth: 160 },
      { id: 'industry', label: 'Industry', minWidth: 140 },
      { id: 'city', label: 'City', minWidth: 120, sortable: false },
      {
        id: 'owner',
        label: 'Owner',
        minWidth: 140,
        sortable: false,
        format: (_value: unknown, row: Company) =>
          row.owner ? `${row.owner.first_name} ${row.owner.last_name}` : '—',
      },
      {
        id: 'created_at',
        label: 'Created',
        minWidth: 120,
        format: (value: string) => formatDate(value, 'MMM dd, yyyy'),
      },
    ],
    []
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    if (!SORTABLE_COLUMNS.has(field)) {
      return;
    }
    setState((prev) => ({
      ...prev,
      sort_by: field as CompanyFilters['sort_by'],
      sort_order: order,
      page: 1,
    }));
  }, []);

  const handleSearch = useCallback((value: string) => {
    setState((prev) => ({ ...prev, search: value, page: 1 }));
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
        <Typography variant="h4">Companies</Typography>
        {canWrite && (
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => navigate('/companies/new')}
          >
            New Company
          </Button>
        )}
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center">
          <TextField
            size="small"
            placeholder="Search companies..."
            value={state.search}
            onChange={(e) => handleSearch(e.target.value)}
            inputProps={{ 'aria-label': 'Search companies' }}
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
        data={data?.companies || []}
        totalCount={data?.total || 0}
        page={state.page - 1}
        rowsPerPage={state.limit}
        loading={isLoading}
        onSort={handleSort}
        sortBy={state.sort_by ?? ''}
        sortOrder={state.sort_order ?? 'asc'}
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(company) => navigate(`/companies/${company.id}`)}
        onEdit={canWrite ? (company) => navigate(`/companies/${company.id}/edit`) : undefined}
        onDelete={canDelete ? (company) => setDeleteDialog({ open: true, company }) : undefined}
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Company"
        message={`Are you sure you want to delete the company "${deleteDialog.company?.name}"? Its leads and customers keep their records but lose the link. This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.company) {
            deleteMutation.mutate(deleteDialog.company.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};
