import { useEffect, useRef, useState } from 'react';
import type { MouseEvent } from 'react';
import { Link as RouterLink, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Badge,
  Box,
  Divider,
  IconButton,
  Menu,
  MenuItem,
  Typography,
} from '@mui/material';
import NotificationsIcon from '@mui/icons-material/Notifications';
import { notificationsApi } from '@/api/endpoints';
import type { Notification } from '@/types';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { afterNotificationsRead, notificationKeys } from '@/pages/automation/automationQueries';
import { notificationTarget } from '@/pages/notifications/notificationTarget';
import { formatDistance } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';

export const UNREAD_COUNT_REFETCH_MS = 60_000;
export const BELL_MENU_LIMIT = 10;

// The app bar's notification bell for staff users (admin, sales, support).
// The app caches for five minutes and never refetches on focus, so both
// queries override those defaults: the count polls every minute and on focus,
// and every opening of the menu fetches the latest ten.
export const NotificationBell = () => {
  const { user, isAuthenticated } = useAuth();
  const isStaff = isAuthenticated && !!user && user.role !== 'customer';
  const role = user?.role;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const open = anchorEl !== null;

  // `count` stays undefined until the first successful fetch, and a failed
  // poll keeps the last known value (React Query keeps data on error): the
  // badge never claims "0 unread" because a request failed.
  const { data: count } = useQuery({
    queryKey: notificationKeys.unreadCount,
    queryFn: notificationsApi.unreadCount,
    enabled: isStaff,
    refetchInterval: UNREAD_COUNT_REFETCH_MS,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    staleTime: 0,
    retry: false,
  });

  const listQuery = useQuery({
    queryKey: notificationKeys.list({ limit: BELL_MENU_LIMIT }),
    queryFn: () => notificationsApi.list({ limit: BELL_MENU_LIMIT }),
    enabled: isStaff && open,
    staleTime: 0,
    refetchOnMount: 'always',
    retry: false,
  });
  const { refetch: refetchList } = listQuery;
  const notifications = listQuery.data?.notifications ?? [];

  // An open menu follows the badge: a new polled count fetches the list again.
  // Opening the menu itself is not a change (the query fetches on mount).
  const lastCount = useRef(count);
  useEffect(() => {
    if (lastCount.current === count) return;
    lastCount.current = count;
    if (open) {
      void refetchList();
    }
  }, [count, open, refetchList]);

  const markReadMutation = useMutation({
    mutationFn: (id: number) => notificationsApi.markRead(id),
    onSuccess: () => afterNotificationsRead(queryClient),
    onError: (error) => showError(getAPIError(error)?.message || 'Failed to mark the notification read'),
  });

  const markAllMutation = useMutation({
    mutationFn: () => notificationsApi.markAllRead(),
    onSuccess: async (marked) => {
      showSuccess(`Marked ${marked} notifications read`);
      await afterNotificationsRead(queryClient);
    },
    onError: (error) => showError(getAPIError(error)?.message || 'Failed to mark notifications read'),
  });

  if (!isStaff) {
    return null;
  }

  const handleOpen = (event: MouseEvent<HTMLElement>) => setAnchorEl(event.currentTarget);
  const handleClose = () => setAnchorEl(null);

  const handleItemClick = (n: Notification) => {
    if (!n.read_at) {
      markReadMutation.mutate(n.id);
    }
    handleClose();
    const target = notificationTarget(n, role);
    if (target) {
      navigate(target);
    }
  };

  return (
    <>
      <IconButton
        color="inherit"
        data-testid="notification-bell"
        data-unread={count}
        aria-label={count === undefined ? 'Notifications' : `Notifications (${count} unread)`}
        aria-haspopup="true"
        aria-controls="notification-menu"
        aria-expanded={open ? 'true' : undefined}
        onClick={handleOpen}
      >
        <Badge badgeContent={count ?? 0} color="error" max={99}>
          <NotificationsIcon />
        </Badge>
      </IconButton>
      <Menu
        id="notification-menu"
        anchorEl={anchorEl}
        open={open}
        onClose={handleClose}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
        transformOrigin={{ vertical: 'top', horizontal: 'right' }}
        slotProps={{
          list: { 'aria-label': 'Notifications' },
          paper: { sx: { width: 360, maxWidth: 'calc(100vw - 32px)' } },
        }}
      >
        {listQuery.isLoading && (
          <MenuItem disabled>
            <Typography variant="body2">Loading…</Typography>
          </MenuItem>
        )}
        {listQuery.isError && [
          <MenuItem key="error" disabled sx={{ whiteSpace: 'normal', opacity: '1 !important' }}>
            <Typography variant="body2" color="error">
              {getAPIError(listQuery.error)?.message || 'Failed to load notifications'}
            </Typography>
          </MenuItem>,
          <MenuItem
            key="retry"
            disabled={listQuery.isFetching}
            onClick={() => void refetchList()}
          >
            Retry
          </MenuItem>,
        ]}
        {listQuery.isSuccess && notifications.length === 0 && (
          <MenuItem disabled>
            <Typography variant="body2">No notifications</Typography>
          </MenuItem>
        )}
        {notifications.map((n) => (
          <MenuItem
            key={n.id}
            data-testid="bell-notification"
            data-read={String(!!n.read_at)}
            onClick={() => handleItemClick(n)}
            sx={{ alignItems: 'flex-start', whiteSpace: 'normal' }}
          >
            <Box sx={{ minWidth: 0, width: '100%' }}>
              <Typography variant="body2" sx={{ fontWeight: n.read_at ? 400 : 700 }} noWrap>
                {n.title}
              </Typography>
              <Typography variant="body2" color="text.secondary" noWrap>
                {n.body}
              </Typography>
              <Typography variant="caption" color="text.secondary">
                {formatDistance(n.created_at)}
              </Typography>
            </Box>
          </MenuItem>
        ))}
        <Divider />
        <MenuItem
          disabled={!count || markAllMutation.isPending}
          onClick={() => markAllMutation.mutate()}
        >
          Mark all read
        </MenuItem>
        <MenuItem component={RouterLink} to="/notifications" onClick={handleClose}>
          View all
        </MenuItem>
      </Menu>
    </>
  );
};
