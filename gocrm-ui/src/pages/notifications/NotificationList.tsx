import React, { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Chip,
  IconButton,
  Link,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material';
import { DoneAll as DoneAllIcon, MarkEmailRead as MarkReadIcon } from '@mui/icons-material';
import { notificationsApi, type NotificationFilters } from '@/api/endpoints';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { afterNotificationsRead, notificationKeys } from '@/pages/automation/automationQueries';
import { AUTOMATION_DATETIME } from '@/pages/automation/automationFormat';
import { formatDate, formatDistance } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';
import { notificationTarget } from './notificationTarget';

const ROWS_PER_PAGE_OPTIONS = [10, 25, 50];

type ReadFilter = 'all' | 'unread';

/**
 * The caller's own notifications (admin, sales, support), newest first. The
 * API has no sort, so the table has none either. Marking read invalidates
 * every notification query, so the app bar's bell follows.
 */
export const Component: React.FC = () => {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const [paging, setPaging] = useState({ page: 0, limit: 10 });
  const [readFilter, setReadFilter] = useState<ReadFilter>('all');

  const filters: NotificationFilters = {
    offset: paging.page * paging.limit,
    limit: paging.limit,
    unread: readFilter === 'unread' ? true : undefined,
  };

  const { data, isLoading, isError, error, isFetching, refetch } = useQuery({
    queryKey: notificationKeys.list(filters),
    queryFn: () => notificationsApi.list(filters),
    staleTime: 0,
    // Keep the table (and its pagination) on screen while the next page loads.
    placeholderData: keepPreviousData,
  });

  const markReadMutation = useMutation({
    mutationFn: (id: number) => notificationsApi.markRead(id),
    onSuccess: () => afterNotificationsRead(queryClient),
    onError: (err) => showError(getAPIError(err)?.message || 'Failed to mark the notification read'),
  });

  const markAllMutation = useMutation({
    mutationFn: () => notificationsApi.markAllRead(),
    onSuccess: async (marked) => {
      showSuccess(`Marked ${marked} notifications read`);
      await afterNotificationsRead(queryClient);
    },
    onError: (err) => showError(getAPIError(err)?.message || 'Failed to mark notifications read'),
  });

  const notifications = data?.notifications ?? [];

  return (
    <Box>
      <Box
        display="flex"
        justifyContent="space-between"
        alignItems="center"
        flexWrap="wrap"
        gap={2}
        mb={3}
      >
        <Typography variant="h4" component="h1">
          Notifications
        </Typography>
        <Box display="flex" alignItems="center" gap={2} flexWrap="wrap">
          <ToggleButtonGroup
            exclusive
            size="small"
            aria-label="Filter"
            value={readFilter}
            onChange={(_, value: ReadFilter | null) => {
              if (value) {
                setReadFilter(value);
                setPaging((prev) => ({ ...prev, page: 0 }));
              }
            }}
          >
            <ToggleButton value="all">All</ToggleButton>
            <ToggleButton value="unread">Unread</ToggleButton>
          </ToggleButtonGroup>
          <Button
            variant="outlined"
            startIcon={<DoneAllIcon />}
            disabled={markAllMutation.isPending}
            onClick={() => markAllMutation.mutate()}
          >
            Mark all read
          </Button>
        </Box>
      </Box>

      {isError && (
        // Shown alone when nothing has loaded yet, and above the retained
        // rows when a refetch fails after a successful load.
        <Alert
          severity="error"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" disabled={isFetching} onClick={() => void refetch()}>
              Retry
            </Button>
          }
        >
          {getAPIError(error)?.message || 'Failed to load notifications'}
        </Alert>
      )}
      {isLoading ? (
        <Loading />
      ) : !data ? null : (
        <Paper>
          <TableContainer>
            <Table size="small" aria-label="Notifications">
              <TableHead>
                <TableRow>
                  <TableCell>Notification</TableCell>
                  <TableCell>Received</TableCell>
                  <TableCell>Status</TableCell>
                  <TableCell align="right">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {notifications.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={4}>
                      <Typography color="text.secondary">
                        {readFilter === 'unread' ? 'No unread notifications' : 'No notifications'}
                      </Typography>
                    </TableCell>
                  </TableRow>
                ) : (
                  notifications.map((n) => {
                    const target = notificationTarget(n, user?.role);
                    return (
                      <TableRow key={n.id} hover data-testid="notification-row" data-read={String(!!n.read_at)}>
                        <TableCell>
                          <Typography variant="body2" sx={{ fontWeight: n.read_at ? 400 : 700 }}>
                            {n.title}
                          </Typography>
                          <Typography variant="body2" color="text.secondary">
                            {n.body}
                          </Typography>
                        </TableCell>
                        <TableCell sx={{ whiteSpace: 'nowrap' }}>
                          <Typography variant="body2">
                            {formatDate(n.created_at, AUTOMATION_DATETIME)}
                          </Typography>
                          <Typography variant="caption" color="text.secondary">
                            {formatDistance(n.created_at)}
                          </Typography>
                        </TableCell>
                        <TableCell>
                          <Chip
                            size="small"
                            label={n.read_at ? 'Read' : 'Unread'}
                            color={n.read_at ? 'default' : 'primary'}
                            variant={n.read_at ? 'outlined' : 'filled'}
                          />
                        </TableCell>
                        <TableCell align="right" sx={{ whiteSpace: 'nowrap' }}>
                          {!n.read_at && (
                            <Tooltip title="Mark read">
                              <span>
                                <IconButton
                                  size="small"
                                  aria-label="Mark read"
                                  disabled={markReadMutation.isPending}
                                  onClick={() => markReadMutation.mutate(n.id)}
                                >
                                  <MarkReadIcon fontSize="small" />
                                </IconButton>
                              </span>
                            </Tooltip>
                          )}
                          {target && (
                            <Link component={RouterLink} to={target} sx={{ ml: 1 }}>
                              Open
                            </Link>
                          )}
                        </TableCell>
                      </TableRow>
                    );
                  })
                )}
              </TableBody>
            </Table>
          </TableContainer>
          <TablePagination
            component="div"
            count={data?.total ?? 0}
            page={paging.page}
            rowsPerPage={paging.limit}
            rowsPerPageOptions={ROWS_PER_PAGE_OPTIONS}
            onPageChange={(_, page) => setPaging((prev) => ({ ...prev, page }))}
            onRowsPerPageChange={(event) =>
              setPaging({ page: 0, limit: parseInt(event.target.value, 10) })
            }
          />
        </Paper>
      )}
    </Box>
  );
};
