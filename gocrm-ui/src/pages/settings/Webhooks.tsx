import React, { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Chip,
  IconButton,
  Paper,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  Tabs,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  DeleteOutline as DeleteOutlineIcon,
  Edit as EditIcon,
} from '@mui/icons-material';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { usePagedList } from '@/hooks/usePagedList';
import { useSnackbar } from '@/hooks/useSnackbar';
import {
  webhooksApi,
  type WebhookEndpoint,
  type WebhookEndpointListParams,
} from '@/api/endpoints';
import { formatDate } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';
import { WebhookDeliveryLog } from './WebhookDeliveryLog';
import { WebhookEndpointDialog } from './WebhookEndpointDialog';
import { OneTimeSecretDialog } from '@/components/OneTimeSecretDialog';
import { webhookKeys } from './webhookQueries';

type TabKey = 'endpoints' | 'deliveries';

// The form dialog: closed, registering a new endpoint or editing one.
type FormTarget = { mode: 'create' } | { mode: 'edit'; endpoint: WebhookEndpoint } | null;

const EndpointsTab: React.FC = () => {
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();

  const [formTarget, setFormTarget] = useState<FormTarget>(null);
  const [deleteTarget, setDeleteTarget] = useState<WebhookEndpoint | null>(null);
  // The signing secret of the endpoint just registered. It exists in this
  // state only, for as long as the dialog showing it is open: it is never
  // written to the query cache or to browser storage.
  const [secret, setSecret] = useState<string | null>(null);

  // Newest first, so an endpoint just registered is on the first page.
  const paramsFor = (pageWindow: { offset: number; limit: number }): WebhookEndpointListParams => ({
    ...pageWindow,
    sort_by: 'id',
    sort_order: 'desc',
  });

  const {
    rows,
    total,
    isLoading,
    isError,
    error,
    displayPage,
    rowsPerPage,
    onPageChange,
    onRowsPerPageChange,
    resetPage,
  } = usePagedList({
    queryKey: (pageWindow) => webhookKeys.endpoints(paramsFor(pageWindow)),
    queryFn: (pageWindow) => webhooksApi.listEndpoints(paramsFor(pageWindow)),
  });

  const refresh = () => queryClient.invalidateQueries({ queryKey: webhookKeys.all });

  const toggleMutation = useMutation({
    // Only the flag is sent: the URL and the events keep their stored values.
    mutationFn: (endpoint: WebhookEndpoint) =>
      webhooksApi.updateEndpoint(endpoint.id, { is_active: !endpoint.is_active }),
    onSuccess: (updated) => {
      showSuccess(updated.is_active ? 'Endpoint activated' : 'Endpoint deactivated');
      void refresh();
    },
    onError: (failure) => {
      showError(getAPIError(failure)?.message ?? 'The endpoint could not be updated.');
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => webhooksApi.deleteEndpoint(id),
    onSuccess: () => {
      showSuccess('Endpoint deleted');
      setDeleteTarget(null);
      void refresh();
    },
    onError: (failure) => {
      showError(getAPIError(failure)?.message ?? 'The endpoint could not be deleted.');
      setDeleteTarget(null);
    },
  });

  const closeSecret = () => setSecret(null);

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" gap={2} mb={2}>
        <Typography variant="body2" color="text.secondary">
          Each active endpoint receives a signed POST for every event it subscribes to.
        </Typography>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={() => setFormTarget({ mode: 'create' })}
          sx={{ flexShrink: 0 }}
        >
          Add endpoint
        </Button>
      </Box>

      <Paper>
        {isLoading ? (
          <Loading />
        ) : isError ? (
          <Alert severity="error" sx={{ m: 2 }}>
            {getAPIError(error)?.message ?? 'The webhook endpoints could not be loaded.'}
          </Alert>
        ) : (
          <>
            <TableContainer>
              <Table aria-label="Webhook endpoints">
                <TableHead>
                  <TableRow>
                    <TableCell>URL</TableCell>
                    <TableCell>Events</TableCell>
                    <TableCell>Status</TableCell>
                    <TableCell>Created</TableCell>
                    <TableCell align="right">Actions</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {/* The empty state is for an empty list only, not for the one
                      render before the corrected page arrives. */}
                  {rows.length === 0 && total === 0 ? (
                    <TableRow>
                      <TableCell colSpan={5} align="center" sx={{ py: 4 }}>
                        <Typography variant="body2" color="text.secondary">
                          No webhook endpoints yet.
                        </Typography>
                      </TableCell>
                    </TableRow>
                  ) : (
                    rows.map((endpoint) => (
                      <TableRow key={endpoint.id} hover>
                        <TableCell sx={{ wordBreak: 'break-all' }}>{endpoint.url}</TableCell>
                        <TableCell>
                          <Box display="flex" gap={0.5} flexWrap="wrap">
                            {endpoint.event_types.map((eventType) => (
                              <Chip key={eventType} size="small" variant="outlined" label={eventType} />
                            ))}
                          </Box>
                        </TableCell>
                        <TableCell>
                          <Chip
                            size="small"
                            label={endpoint.is_active ? 'Active' : 'Inactive'}
                            color={endpoint.is_active ? 'success' : 'default'}
                            variant={endpoint.is_active ? 'filled' : 'outlined'}
                          />
                        </TableCell>
                        <TableCell>{formatDate(endpoint.created_at, 'PP')}</TableCell>
                        <TableCell align="right" sx={{ whiteSpace: 'nowrap' }}>
                          <Button
                            size="small"
                            onClick={() => toggleMutation.mutate(endpoint)}
                            disabled={toggleMutation.isPending}
                            aria-label={`${endpoint.is_active ? 'Deactivate' : 'Activate'} ${endpoint.url}`}
                          >
                            {endpoint.is_active ? 'Deactivate' : 'Activate'}
                          </Button>
                          <Tooltip title="Edit">
                            <IconButton
                              aria-label={`Edit ${endpoint.url}`}
                              onClick={() => setFormTarget({ mode: 'edit', endpoint })}
                            >
                              <EditIcon />
                            </IconButton>
                          </Tooltip>
                          <Tooltip title="Delete">
                            <IconButton
                              aria-label={`Delete ${endpoint.url}`}
                              color="error"
                              onClick={() => setDeleteTarget(endpoint)}
                            >
                              <DeleteOutlineIcon />
                            </IconButton>
                          </Tooltip>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </TableContainer>
            <TablePagination
              component="div"
              rowsPerPageOptions={[10, 20, 50, 100]}
              count={total}
              page={displayPage}
              rowsPerPage={rowsPerPage}
              onPageChange={onPageChange}
              onRowsPerPageChange={onRowsPerPageChange}
            />
          </>
        )}
      </Paper>

      <WebhookEndpointDialog
        open={formTarget !== null}
        endpoint={formTarget?.mode === 'edit' ? formTarget.endpoint : null}
        onClose={() => setFormTarget(null)}
        onCreated={(created) => {
          setFormTarget(null);
          // Only the secret is kept, and the list is fetched again instead of
          // being patched with the answer that carries it.
          setSecret(created.secret);
          resetPage();
          void refresh();
        }}
        onUpdated={() => {
          setFormTarget(null);
          showSuccess('Endpoint updated');
          void refresh();
        }}
      />

      <OneTimeSecretDialog
        title="Signing secret"
        label="Signing secret"
        copyLabel="Copy signing secret"
        copiedMessage="Signing secret copied to clipboard"
        testId="webhook-secret"
        secret={secret}
        onClose={closeSecret}
        warning={
          <>
            Copy this secret now. It cannot be shown again: it is stored encrypted and no other
            screen or request returns it. If it is lost, delete the endpoint and register it again.
          </>
        }
      />

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Delete webhook endpoint"
        message={`Delete the endpoint ${deleteTarget?.url ?? ''}? Its delivery log is removed with it, and deliveries still waiting are never sent. This cannot be undone.`}
        confirmText="Delete"
        severity="error"
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
        onCancel={() => setDeleteTarget(null)}
      />
    </Box>
  );
};

export const Webhooks: React.FC = () => {
  const [tab, setTab] = useState<TabKey>('endpoints');

  return (
    <Box>
      <Typography variant="h4" sx={{ mb: 1 }}>
        Webhooks
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Outbound webhooks notify other systems when a lead, a customer or a ticket is created or
        updated.
      </Typography>

      <Tabs
        value={tab}
        onChange={(_event, value: TabKey) => setTab(value)}
        aria-label="Webhook sections"
        sx={{ mb: 2, borderBottom: 1, borderColor: 'divider' }}
      >
        <Tab label="Endpoints" value="endpoints" />
        <Tab label="Delivery log" value="deliveries" />
      </Tabs>

      {tab === 'endpoints' ? <EndpointsTab /> : <WebhookDeliveryLog />}
    </Box>
  );
};

export function Component() {
  return <Webhooks />;
}

Component.displayName = 'Webhooks';
