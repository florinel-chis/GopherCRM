import React, { useCallback, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Button,
  Chip,
  FormControl,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Typography,
} from '@mui/material';
import { Add as AddIcon } from '@mui/icons-material';
import { formatDate } from '@/utils/date';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { usePermissions } from '@/hooks/usePermissions';
import { useSnackbar } from '@/hooks/useSnackbar';
import { formsApi, type Form, type FormStatus } from '@/api/endpoints/forms';

const statusOptions = [
  { value: '', label: 'All Statuses' },
  { value: 'draft', label: 'Draft' },
  { value: 'published', label: 'Published' },
  { value: 'archived', label: 'Archived' },
];

const statusColors: Record<FormStatus, 'default' | 'success' | 'warning'> = {
  draft: 'default',
  published: 'success',
  archived: 'warning',
};

const statusLabels: Record<FormStatus, string> = {
  draft: 'Draft',
  published: 'Published',
  archived: 'Archived',
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { can } = usePermissions();
  const { showSuccess, showError } = useSnackbar();

  // Mirrors the API: forms.manage creates and edits a form, forms.delete
  // deletes one; a caller with forms.view alone is read-only.
  const canManage = can('forms.manage');
  const canDelete = can('forms.delete');

  const [status, setStatus] = useState('');
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(10);
  const [sort, setSort] = useState<{ sort_by?: string; sort_order?: 'asc' | 'desc' }>({});
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; form?: Form }>({
    open: false,
  });

  const params = useMemo(
    () => ({
      offset: page * rowsPerPage,
      limit: rowsPerPage,
      status: status || undefined,
      ...sort,
    }),
    [page, rowsPerPage, status, sort]
  );

  const { data, isLoading } = useQuery({
    queryKey: ['forms', params],
    queryFn: () => formsApi.list(params),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => formsApi.delete(id),
    onSuccess: () => {
      showSuccess('Form deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['forms'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete form');
    },
  });

  const columns: Column<Form>[] = useMemo(
    () => [
      {
        id: 'name',
        label: 'Name',
        minWidth: 220,
      },
      {
        id: 'status',
        label: 'Status',
        minWidth: 120,
        format: (value: FormStatus) => (
          <Chip label={statusLabels[value] ?? value} color={statusColors[value]} size="small" />
        ),
      },
      {
        id: 'submission_count',
        label: 'Submissions',
        minWidth: 120,
        align: 'right',
        sortable: false,
        format: (value: number | undefined) => value ?? 0,
      },
      {
        id: 'created_at',
        label: 'Created',
        minWidth: 140,
        format: (value: string) => formatDate(value, 'MMM dd, yyyy', ''),
      },
    ],
    []
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    setSort({ sort_by: field, sort_order: order });
    setPage(0);
  }, []);

  const handleRowsPerPageChange = useCallback((value: number) => {
    setRowsPerPage(value);
    setPage(0);
  }, []);

  if (isLoading && !data) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Forms</Typography>
        {canManage && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/forms/new')}>
            Create Form
          </Button>
        )}
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <FormControl size="small" sx={{ minWidth: 180 }}>
          <InputLabel id="form-status-filter-label">Status</InputLabel>
          <Select
            labelId="form-status-filter-label"
            label="Status"
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              setPage(0);
            }}
          >
            {statusOptions.map((option) => (
              <MenuItem key={option.value} value={option.value}>
                {option.label}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
      </Paper>

      <DataTable
        columns={columns}
        data={data?.forms || []}
        totalCount={data?.total || 0}
        page={page}
        rowsPerPage={rowsPerPage}
        loading={isLoading}
        onSort={handleSort}
        sortBy={sort.sort_by ?? ''}
        sortOrder={sort.sort_order ?? 'asc'}
        onPageChange={setPage}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(form) => navigate(`/forms/${form.id}`)}
        onEdit={canManage ? (form) => navigate(`/forms/${form.id}/edit`) : undefined}
        onDelete={canDelete ? (form) => setDeleteDialog({ open: true, form }) : undefined}
      />

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Form"
        message={
          deleteDialog.form
            ? `Delete the form "${deleteDialog.form.name}"? It stops being served immediately. Collected submissions are retained. This action cannot be undone.`
            : ''
        }
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.form) {
            deleteMutation.mutate(deleteDialog.form.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};

Component.displayName = 'FormList';
