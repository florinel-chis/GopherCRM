import React, { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControl,
  IconButton,
  InputLabel,
  Link,
  MenuItem,
  Paper,
  Select,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  ContentCopy as ContentCopyIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
  OpenInNew as OpenInNewIcon,
} from '@mui/icons-material';
import { format } from 'date-fns';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import {
  formsApi,
  type FormStatus,
  type FormSubmission,
  type FormSubmissionStatus,
} from '@/api/endpoints/forms';

const statusColors: Record<FormStatus, 'default' | 'success' | 'warning'> = {
  draft: 'default',
  published: 'success',
  archived: 'warning',
};

const submissionStatusColors: Record<
  FormSubmissionStatus,
  'info' | 'warning' | 'success' | 'error'
> = {
  received: 'info',
  pending: 'warning',
  confirmed: 'success',
  spam: 'error',
};

const submissionStatusOptions = [
  { value: '', label: 'All Statuses' },
  { value: 'received', label: 'Received' },
  { value: 'pending', label: 'Pending' },
  { value: 'confirmed', label: 'Confirmed' },
  { value: 'spam', label: 'Spam' },
];

// The embed script and the hosted page are served by the backend, so both links
// are built from the API base the UI already talks to.
const apiBaseUrl = (
  import.meta.env.VITE_API_BASE_URL || 'http://localhost:8080/api/v1'
).replace(/\/+$/, '');

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  const canManage = user?.role === 'admin' || user?.role === 'sales';
  const canDelete = user?.role === 'admin';

  const [status, setStatus] = useState('');
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(10);
  const [deleteDialog, setDeleteDialog] = useState(false);
  const [selected, setSelected] = useState<FormSubmission | null>(null);

  const { data: form, isLoading, isError } = useQuery({
    queryKey: ['form', id],
    queryFn: () => formsApi.get(Number(id)),
    enabled: Boolean(id),
  });

  const submissionParams = {
    offset: page * rowsPerPage,
    limit: rowsPerPage,
    status: status || undefined,
  };

  const { data: submissions, isLoading: submissionsLoading } = useQuery({
    queryKey: ['form', id, 'submissions', submissionParams],
    queryFn: () => formsApi.listSubmissions(Number(id), submissionParams),
    enabled: Boolean(id),
  });

  const deleteMutation = useMutation({
    mutationFn: () => formsApi.delete(Number(id)),
    onSuccess: () => {
      showSuccess('Form deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['forms'] });
      navigate('/forms');
    },
    onError: () => {
      showError('Failed to delete form');
    },
  });

  const copy = async (value: string, what: string) => {
    try {
      await navigator.clipboard.writeText(value);
      showSuccess(`${what} copied to the clipboard`);
    } catch {
      showError(`Could not copy the ${what.toLowerCase()}`);
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  if (isError || !form) {
    return <Alert severity="error">Failed to load the form</Alert>;
  }

  const embedSnippet = `<script src="${apiBaseUrl}/forms/public/embed.js" data-form-key="${form.public_id}" async></script>`;
  const hostedUrl = `${apiBaseUrl}/forms/public/${form.public_id}/view`;
  const rows = submissions?.submissions || [];

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Box>
          <Typography variant="h4">{form.name}</Typography>
          {form.description && (
            <Typography variant="body2" color="text.secondary">
              {form.description}
            </Typography>
          )}
        </Box>
        <Box display="flex" gap={1}>
          {canManage && (
            <Button
              variant="outlined"
              startIcon={<EditIcon />}
              onClick={() => navigate(`/forms/${form.id}/edit`)}
            >
              Edit
            </Button>
          )}
          {canDelete && (
            <Button
              variant="outlined"
              color="error"
              startIcon={<DeleteIcon />}
              onClick={() => setDeleteDialog(true)}
            >
              Delete
            </Button>
          )}
        </Box>
      </Box>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Summary
        </Typography>
        <Box
          display="grid"
          gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }}
          gap={2}
          mt={1}
        >
          <Box display="flex" alignItems="center" gap={1}>
            <Typography variant="body2" color="text.secondary">
              Status
            </Typography>
            <Chip label={form.status} color={statusColors[form.status]} size="small" />
          </Box>
          <Typography variant="body2" color="text.secondary">
            Fields: {form.fields?.length ?? 0}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            After submission: {form.submit_action === 'redirect' ? 'redirect' : 'thank-you message'}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Double opt-in: {form.double_opt_in ? 'on' : 'off'}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Creates leads: {form.create_lead ? 'yes' : 'no'}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            reCAPTCHA: {form.captcha_enabled ? 'on' : 'off'}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Created: {form.created_at ? format(new Date(form.created_at), 'MMM dd, yyyy') : '—'}
          </Typography>
          <Box display="flex" alignItems="center" gap={1} flexWrap="wrap">
            <Typography variant="body2" color="text.secondary">
              Notifications:
            </Typography>
            {form.notify_emails?.length ? (
              form.notify_emails.map((email) => <Chip key={email} label={email} size="small" />)
            ) : (
              <Typography variant="body2" color="text.secondary">
                none
              </Typography>
            )}
          </Box>
        </Box>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Embed
        </Typography>
        {form.status !== 'published' && (
          <Alert severity="info" sx={{ mb: 2 }}>
            This form is {form.status}. Publish it before embedding — until then both links answer
            404.
          </Alert>
        )}
        <Typography variant="body2" color="text.secondary" gutterBottom>
          Paste this snippet where the form should appear.
        </Typography>
        <Box display="flex" alignItems="flex-start" gap={1} mb={2}>
          <Box
            component="pre"
            data-testid="embed-snippet"
            sx={{
              flexGrow: 1,
              m: 0,
              p: 2,
              overflowX: 'auto',
              borderRadius: 1,
              backgroundColor: 'action.hover',
              fontSize: '0.8rem',
            }}
          >
            {embedSnippet}
          </Box>
          <Tooltip title="Copy embed snippet">
            <IconButton
              aria-label="Copy embed snippet"
              onClick={() => copy(embedSnippet, 'Embed snippet')}
            >
              <ContentCopyIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Box>

        <Divider sx={{ mb: 2 }} />

        <Typography variant="body2" color="text.secondary" gutterBottom>
          Or share the hosted page.
        </Typography>
        <Box display="flex" alignItems="center" gap={1}>
          <Link href={hostedUrl} target="_blank" rel="noopener" sx={{ wordBreak: 'break-all' }}>
            {hostedUrl}
          </Link>
          <Tooltip title="Copy hosted link">
            <IconButton aria-label="Copy hosted link" onClick={() => copy(hostedUrl, 'Hosted link')}>
              <ContentCopyIcon fontSize="small" />
            </IconButton>
          </Tooltip>
          <Tooltip title="Open hosted page">
            <IconButton
              aria-label="Open hosted page"
              component="a"
              href={hostedUrl}
              target="_blank"
              rel="noopener"
            >
              <OpenInNewIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Box>
      </Paper>

      <Paper sx={{ p: 3 }}>
        <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
          <Typography variant="h6">Submissions</Typography>
          <FormControl size="small" sx={{ minWidth: 180 }}>
            <InputLabel id="submission-status-filter-label">Status</InputLabel>
            <Select
              labelId="submission-status-filter-label"
              label="Status"
              value={status}
              onChange={(event) => {
                setStatus(event.target.value);
                setPage(0);
              }}
            >
              {submissionStatusOptions.map((option) => (
                <MenuItem key={option.value} value={option.value}>
                  {option.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
        </Box>

        <TableContainer>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Received</TableCell>
                <TableCell>Email</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Spam reason</TableCell>
                <TableCell>Lead</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={5}>
                    <Typography variant="body2" color="text.secondary">
                      {submissionsLoading ? 'Loading submissions…' : 'No submissions yet.'}
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((submission) => (
                  <TableRow
                    key={submission.id}
                    hover
                    sx={{ cursor: 'pointer' }}
                    onClick={() => setSelected(submission)}
                  >
                    <TableCell>
                      {submission.created_at
                        ? format(new Date(submission.created_at), 'MMM dd, yyyy HH:mm')
                        : ''}
                    </TableCell>
                    <TableCell>{submission.email}</TableCell>
                    <TableCell>
                      <Chip
                        label={submission.status}
                        color={submissionStatusColors[submission.status]}
                        size="small"
                      />
                    </TableCell>
                    <TableCell>{submission.spam_reason || '—'}</TableCell>
                    <TableCell>
                      {submission.lead_id ? (
                        <Link
                          href={`/leads/${submission.lead_id}`}
                          onClick={(event) => {
                            event.preventDefault();
                            event.stopPropagation();
                            navigate(`/leads/${submission.lead_id}`);
                          }}
                        >
                          #{submission.lead_id}
                        </Link>
                      ) : (
                        '—'
                      )}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </TableContainer>

        <TablePagination
          component="div"
          rowsPerPageOptions={[5, 10, 25, 50]}
          count={submissions?.total || 0}
          page={page}
          rowsPerPage={rowsPerPage}
          onPageChange={(_, nextPage) => setPage(nextPage)}
          onRowsPerPageChange={(event) => {
            setRowsPerPage(parseInt(event.target.value, 10));
            setPage(0);
          }}
        />
      </Paper>

      <Dialog open={Boolean(selected)} onClose={() => setSelected(null)} maxWidth="sm" fullWidth>
        <DialogTitle>Submission #{selected?.id}</DialogTitle>
        <DialogContent dividers>
          <Stack spacing={2}>
            <Box display="flex" gap={1} alignItems="center" flexWrap="wrap">
              {selected && (
                <Chip
                  label={selected.status}
                  color={submissionStatusColors[selected.status]}
                  size="small"
                />
              )}
              {selected?.spam_reason && (
                <Chip label={`Caught by: ${selected.spam_reason}`} size="small" />
              )}
              {selected?.lead_id && (
                <Button size="small" onClick={() => navigate(`/leads/${selected.lead_id}`)}>
                  Open lead #{selected.lead_id}
                </Button>
              )}
            </Box>

            <Box component="dl" sx={{ m: 0 }}>
              {Object.entries(selected?.data || {}).map(([key, value]) => (
                <Box key={key} mb={1}>
                  <Typography component="dt" variant="caption" color="text.secondary">
                    {key}
                  </Typography>
                  <Typography component="dd" variant="body2" sx={{ m: 0 }}>
                    {value || '—'}
                  </Typography>
                </Box>
              ))}
              {Object.keys(selected?.data || {}).length === 0 && (
                <Typography variant="body2" color="text.secondary">
                  No values stored.
                </Typography>
              )}
            </Box>

            <Divider />

            <Typography variant="caption" color="text.secondary">
              Submitted from {selected?.ip_address || 'unknown IP'}
              {selected?.referrer ? ` via ${selected.referrer}` : ''}
              {selected?.confirmed_at
                ? ` — confirmed ${format(new Date(selected.confirmed_at), 'MMM dd, yyyy HH:mm')}`
                : ''}
            </Typography>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setSelected(null)}>Close</Button>
        </DialogActions>
      </Dialog>

      <ConfirmDialog
        open={deleteDialog}
        title="Delete Form"
        message={`Delete the form "${form.name}"? It stops being served immediately. Collected submissions are retained. This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};

Component.displayName = 'FormDetail';
