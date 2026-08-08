import React, { useEffect, useState } from 'react';
import { useForm, FormProvider } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
} from '@mui/icons-material';
import { AxiosError } from 'axios';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { LabelChip } from '@/components/LabelChip';
import {
  DEFAULT_LABEL_COLOR,
  LABEL_COLOR_PALETTE,
  contrastingTextColor,
  nextPaletteColor,
} from '@/components/labelColors';
import { FormTextField } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { labelsApi, type CreateLabelData } from '@/api/endpoints';
import type { Label } from '@/types';

const labelSchema = z.object({
  name: z.string().trim().min(1, 'Name is required').max(50, 'Name must be 50 characters or fewer'),
  color: z
    .string()
    .regex(/^#[0-9a-fA-F]{6}$/, 'Color must be a hex value such as #1F77B4'),
});

type LabelFormData = z.infer<typeof labelSchema>;

export const Component: React.FC = () => {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  // Mirrors the API policy: admin, sales and support may create and edit
  // labels; only an admin may delete one. Customers are read-only.
  const canManage = user?.role === 'admin' || user?.role === 'sales' || user?.role === 'support';
  const canDelete = user?.role === 'admin';

  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<Label | null>(null);
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; label?: Label }>({
    open: false,
  });

  const { data: labels, isLoading } = useQuery({
    queryKey: ['labels'],
    queryFn: () => labelsApi.getLabels(),
  });

  const methods = useForm<LabelFormData>({
    resolver: zodResolver(labelSchema),
    defaultValues: { name: '', color: DEFAULT_LABEL_COLOR },
  });
  const selectedColor = methods.watch('color');

  useEffect(() => {
    if (!editorOpen) {
      return;
    }
    methods.reset({
      name: editing?.name ?? '',
      color: editing?.color ?? nextPaletteColor((labels || []).map((label) => label.color)),
    });
    // `labels` is intentionally excluded: refetching the list mid-edit must not
    // reset the colour the user just picked.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editorOpen, editing, methods]);

  // A duplicate name comes back as 409; anything else is reported generically.
  const reportMutationError = (error: unknown, fallback: string) => {
    if (error instanceof AxiosError && error.response?.status === 409) {
      showError('A label with that name already exists');
      return;
    }
    showError(fallback);
  };

  const saveMutation = useMutation({
    mutationFn: (data: CreateLabelData) =>
      editing ? labelsApi.updateLabel(editing.id, data) : labelsApi.createLabel(data),
    onSuccess: (label) => {
      showSuccess(editing ? `Label "${label.name}" updated` : `Label "${label.name}" created`);
      queryClient.invalidateQueries({ queryKey: ['labels'] });
      // Tasks carry their labels inline, so a rename or recolor changes them.
      // Both task keys have to go: ['tasks'] is the list and ['task', id] the
      // detail/edit cache, and prefix matching treats them as unrelated.
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      queryClient.invalidateQueries({ queryKey: ['task'] });
      setEditorOpen(false);
      setEditing(null);
    },
    onError: (error) => {
      reportMutationError(error, editing ? 'Failed to update label' : 'Failed to create label');
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => labelsApi.deleteLabel(id),
    onSuccess: () => {
      showSuccess('Label deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['labels'] });
      // A deleted label is detached from every task, so both the list and the
      // per-task detail caches are stale.
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      queryClient.invalidateQueries({ queryKey: ['task'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete label');
    },
  });

  const openCreate = () => {
    setEditing(null);
    setEditorOpen(true);
  };

  const openEdit = (label: Label) => {
    setEditing(label);
    setEditorOpen(true);
  };

  const closeEditor = () => {
    setEditorOpen(false);
    setEditing(null);
  };

  const onSubmit = (data: LabelFormData) => {
    saveMutation.mutate({ name: data.name.trim(), color: data.color });
  };

  if (isLoading) {
    return <Loading />;
  }

  const rows = labels || [];
  const pendingDelete = deleteDialog.label;
  const pendingDeleteCount = pendingDelete?.task_count ?? 0;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Labels</Typography>
        {canManage && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={openCreate}>
            Create Label
          </Button>
        )}
      </Box>

      <Paper>
        <TableContainer>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Color</TableCell>
                <TableCell align="right">Tasks</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4}>
                    <Typography variant="body2" color="text.secondary">
                      No labels yet.
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((label) => (
                  <TableRow key={label.id} hover>
                    <TableCell>
                      <LabelChip label={label} />
                    </TableCell>
                    <TableCell>
                      <Box display="flex" alignItems="center" gap={1}>
                        <Box
                          data-testid={`label-swatch-${label.id}`}
                          sx={{
                            width: 20,
                            height: 20,
                            borderRadius: '4px',
                            border: '1px solid',
                            borderColor: 'divider',
                            backgroundColor: label.color,
                          }}
                        />
                        <Typography variant="body2">{label.color.toUpperCase()}</Typography>
                      </Box>
                    </TableCell>
                    <TableCell align="right">{label.task_count ?? 0}</TableCell>
                    <TableCell align="right">
                      <Box display="flex" gap={1} justifyContent="flex-end">
                        {canManage && (
                          <Tooltip title="Edit label">
                            <IconButton size="small" onClick={() => openEdit(label)}>
                              <EditIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                        {canDelete && (
                          <Tooltip title="Delete label">
                            <IconButton
                              size="small"
                              onClick={() => setDeleteDialog({ open: true, label })}
                            >
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Box>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </TableContainer>
      </Paper>

      <Dialog open={editorOpen} onClose={closeEditor} maxWidth="xs" fullWidth>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)}>
            <DialogTitle>{editing ? 'Edit Label' : 'Create Label'}</DialogTitle>
            <DialogContent>
              <Stack spacing={3} mt={1}>
                <FormTextField name="name" label="Name" required autoFocus />

                <Box>
                  <Typography variant="subtitle2" gutterBottom>
                    Color
                  </Typography>
                  <Box display="flex" gap={1} flexWrap="wrap" mb={2}>
                    {LABEL_COLOR_PALETTE.map((color) => {
                      const isSelected = color.toUpperCase() === (selectedColor || '').toUpperCase();
                      return (
                        <Box
                          key={color}
                          component="button"
                          type="button"
                          aria-label={`Use color ${color}`}
                          aria-pressed={isSelected}
                          onClick={() =>
                            methods.setValue('color', color, { shouldValidate: true })
                          }
                          sx={{
                            width: 32,
                            height: 32,
                            padding: 0,
                            cursor: 'pointer',
                            borderRadius: '50%',
                            backgroundColor: color,
                            border: '2px solid',
                            borderColor: isSelected ? contrastingTextColor(color) : 'transparent',
                            outline: isSelected ? '2px solid' : 'none',
                            outlineColor: 'primary.main',
                          }}
                        />
                      );
                    })}
                  </Box>
                  <FormTextField
                    name="color"
                    label="Hex color"
                    required
                    helperText="Any #RRGGBB value; the swatches above are suggestions"
                  />
                </Box>

                <Box>
                  <Typography variant="subtitle2" gutterBottom>
                    Preview
                  </Typography>
                  <LabelChip
                    label={{
                      id: editing?.id ?? 0,
                      name: methods.watch('name') || 'Label',
                      color: /^#[0-9a-fA-F]{6}$/.test(selectedColor || '')
                        ? selectedColor
                        : DEFAULT_LABEL_COLOR,
                    }}
                  />
                </Box>
              </Stack>
            </DialogContent>
            <DialogActions>
              <Button onClick={closeEditor} color="inherit">
                Cancel
              </Button>
              <Button type="submit" variant="contained" disabled={saveMutation.isPending}>
                {editing ? 'Save' : 'Create'}
              </Button>
            </DialogActions>
          </form>
        </FormProvider>
      </Dialog>

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Label"
        message={
          pendingDelete
            ? `Delete the label "${pendingDelete.name}"? It will be removed from ${pendingDeleteCount} ${
                pendingDeleteCount === 1 ? 'task' : 'tasks'
              }. This action cannot be undone.`
            : ''
        }
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (pendingDelete) {
            deleteMutation.mutate(pendingDelete.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};

Component.displayName = 'LabelList';
