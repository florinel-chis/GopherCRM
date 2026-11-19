import React, { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Chip,
  FormControl,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  Typography,
} from '@mui/material';
import { Loading } from '@/components/Loading';
import {
  webhooksApi,
  WEBHOOK_DELIVERY_STATUSES,
  WEBHOOK_EVENT_TYPES,
  type WebhookDeliveryListParams,
  type WebhookDeliveryStatus,
  type WebhookEventType,
} from '@/api/endpoints';
import { formatDate } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';
import { webhookKeys } from './webhookQueries';

// The endpoint filter offers one page of endpoints: the API caps a page at 100.
const ENDPOINT_OPTIONS_LIMIT = 100;

const STATUS_COLORS: Record<WebhookDeliveryStatus, 'default' | 'info' | 'success' | 'error'> = {
  pending: 'default',
  sending: 'info',
  succeeded: 'success',
  failed: 'error',
};

interface Filters {
  endpointId: number | '';
  status: WebhookDeliveryStatus | '';
  eventType: WebhookEventType | '';
}

/**
 * The delivery log: one row per event and endpoint, in the order the API
 * returns (newest first). Only fields of the API are shown; a delivery holds
 * no URL, secret or payload.
 */
export const WebhookDeliveryLog: React.FC = () => {
  const [filters, setFilters] = useState<Filters>({ endpointId: '', status: '', eventType: '' });
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(20);

  // A changed filter always starts again from the first page.
  const changeFilters = (patch: Partial<Filters>) => {
    setFilters((prev) => ({ ...prev, ...patch }));
    setPage(0);
  };

  const params: WebhookDeliveryListParams = {
    endpoint_id: filters.endpointId === '' ? undefined : filters.endpointId,
    status: filters.status === '' ? undefined : filters.status,
    event_type: filters.eventType === '' ? undefined : filters.eventType,
    offset: page * rowsPerPage,
    limit: rowsPerPage,
  };

  const { data, isLoading, isError, error } = useQuery({
    queryKey: webhookKeys.deliveries(params),
    queryFn: () => webhooksApi.listDeliveries(params),
    // A log is read for what happened just now: a page or filter shown before
    // is fetched again instead of being answered from the cache.
    staleTime: 0,
  });

  const endpointOptions = useQuery({
    queryKey: webhookKeys.endpointOptions(),
    queryFn: () =>
      webhooksApi.listEndpoints({
        offset: 0,
        limit: ENDPOINT_OPTIONS_LIMIT,
        sort_by: 'id',
        sort_order: 'desc',
      }),
  });

  const rows = data?.rows ?? [];

  return (
    <Box>
      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} flexWrap="wrap">
          <FormControl size="small" sx={{ minWidth: 260 }}>
            <InputLabel id="webhook-delivery-endpoint-label" shrink>
              Endpoint
            </InputLabel>
            <Select<number | ''>
              labelId="webhook-delivery-endpoint-label"
              id="webhook-delivery-endpoint"
              value={filters.endpointId}
              onChange={(event) => {
                const value = event.target.value;
                changeFilters({ endpointId: value === '' ? '' : Number(value) });
              }}
              label="Endpoint"
              displayEmpty
              notched
            >
              <MenuItem value="">All endpoints</MenuItem>
              {(endpointOptions.data?.rows ?? []).map((endpoint) => (
                <MenuItem key={endpoint.id} value={endpoint.id}>
                  #{endpoint.id} {endpoint.url}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl size="small" sx={{ minWidth: 170 }}>
            <InputLabel id="webhook-delivery-status-label" shrink>
              Status
            </InputLabel>
            <Select
              labelId="webhook-delivery-status-label"
              id="webhook-delivery-status"
              value={filters.status}
              onChange={(event) =>
                changeFilters({ status: event.target.value as WebhookDeliveryStatus | '' })
              }
              label="Status"
              displayEmpty
              notched
            >
              <MenuItem value="">All statuses</MenuItem>
              {WEBHOOK_DELIVERY_STATUSES.map((status) => (
                <MenuItem key={status} value={status}>
                  {status}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl size="small" sx={{ minWidth: 200 }}>
            <InputLabel id="webhook-delivery-event-label" shrink>
              Event type
            </InputLabel>
            <Select
              labelId="webhook-delivery-event-label"
              id="webhook-delivery-event"
              value={filters.eventType}
              onChange={(event) =>
                changeFilters({ eventType: event.target.value as WebhookEventType | '' })
              }
              label="Event type"
              displayEmpty
              notched
            >
              <MenuItem value="">All event types</MenuItem>
              {WEBHOOK_EVENT_TYPES.map((eventType) => (
                <MenuItem key={eventType} value={eventType}>
                  {eventType}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
        </Box>
      </Paper>

      <Paper>
        {isLoading ? (
          <Loading />
        ) : isError ? (
          <Alert severity="error" sx={{ m: 2 }}>
            {getAPIError(error)?.message ?? 'The delivery log could not be loaded.'}
          </Alert>
        ) : (
          <>
            <TableContainer>
              <Table size="small" aria-label="Webhook deliveries">
                <TableHead>
                  <TableRow>
                    <TableCell>Event type</TableCell>
                    <TableCell>Record ID</TableCell>
                    <TableCell>Status</TableCell>
                    <TableCell align="right">Attempts</TableCell>
                    <TableCell>Last attempt</TableCell>
                    <TableCell align="right">Last response code</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {rows.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={6} align="center" sx={{ py: 4 }}>
                        <Typography variant="body2" color="text.secondary">
                          No deliveries match these filters.
                        </Typography>
                      </TableCell>
                    </TableRow>
                  ) : (
                    rows.map((delivery) => (
                      <TableRow key={delivery.id} hover data-testid="webhook-delivery-row">
                        <TableCell>{delivery.event_type}</TableCell>
                        <TableCell>{delivery.record_id}</TableCell>
                        <TableCell>
                          <Chip
                            size="small"
                            label={delivery.status}
                            color={STATUS_COLORS[delivery.status] ?? 'default'}
                            variant="outlined"
                          />
                        </TableCell>
                        <TableCell align="right">{delivery.attempts}</TableCell>
                        <TableCell>{formatDate(delivery.last_attempt_at, 'PPp')}</TableCell>
                        <TableCell align="right">{delivery.last_http_status ?? '-'}</TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </TableContainer>
            <TablePagination
              component="div"
              rowsPerPageOptions={[10, 20, 50, 100]}
              count={data?.total ?? 0}
              page={page}
              rowsPerPage={rowsPerPage}
              onPageChange={(_event, next) => setPage(next)}
              onRowsPerPageChange={(event) => {
                setRowsPerPage(Number(event.target.value));
                setPage(0);
              }}
            />
          </>
        )}
      </Paper>
    </Box>
  );
};
