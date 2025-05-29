import React, { useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Chip,
  Divider,
  Stack,
  IconButton,
  Avatar,
  List,
  ListItem,
  ListItemIcon,
  ListItemText,
  Tab,
  Tabs,
} from '@mui/material';
import {
  Edit as EditIcon,
  Delete as DeleteIcon,
  Person as PersonIcon,
  Email as EmailIcon,
  CalendarToday as CalendarIcon,
  Work as WorkIcon,
  Block as BlockIcon,
  CheckCircle as ActiveIcon,
  Assignment as TaskIcon,
  ConfirmationNumber as TicketIcon,
  TrendingUp as TrendingUpIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useSnackbar } from '@/hooks/useSnackbar';
import { usersApi, tasksApi, ticketsApi } from '@/api/endpoints';
import type { User } from '@/types';
import { format } from 'date-fns';
import { useAuth } from '@/contexts/AuthContext';

interface TabPanelProps {
  children?: React.ReactNode;
  index: number;
  value: number;
}

function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;
  return (
    <div
      role="tabpanel"
      hidden={value !== index}
      id={`tabpanel-${index}`}
      aria-labelledby={`tab-${index}`}
      {...other}
    >
      {value === index && <Box sx={{ py: 3 }}>{children}</Box>}
    </div>
  );
}

const getRoleColor = (role: User['role']) => {
  switch (role) {
    case 'admin':
      return 'error';
    case 'sales':
      return 'primary';
    case 'support':
      return 'warning';
    case 'customer':
      return 'default';
    default:
      return 'default';
  }
};

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { user: currentUser } = useAuth();
  
  const [tabValue, setTabValue] = useState(0);
  const [deleteDialog, setDeleteDialog] = useState(false);

  const { data: user, isLoading } = useQuery({
    queryKey: ['user', id],
    queryFn: () => usersApi.getUser(Number(id)),
    enabled: !!id,
  });

  const { data: tasksData } = useQuery({
    queryKey: ['tasks', { assigned_to: id }],
    queryFn: () => tasksApi.getTasks({ assigned_to: Number(id) }),
    enabled: !!id,
  });

  const { data: ticketsData } = useQuery({
    queryKey: ['tickets', { assigned_to: id }],
    queryFn: () => ticketsApi.getTickets({ assigned_to: Number(id) }),
    enabled: !!id,
  });

  const deleteMutation = useMutation({
    mutationFn: () => usersApi.deleteUser(Number(id)),
    onSuccess: () => {
      showSuccess('User deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
      navigate('/users');
    },
    onError: () => {
      showError('Failed to delete user');
    },
  });

  const activateMutation = useMutation({
    mutationFn: () => usersApi.activateUser(Number(id)),
    onSuccess: () => {
      showSuccess('User activated successfully');
      queryClient.invalidateQueries({ queryKey: ['user', id] });
    },
    onError: () => {
      showError('Failed to activate user');
    },
  });

  const deactivateMutation = useMutation({
    mutationFn: () => usersApi.deactivateUser(Number(id)),
    onSuccess: () => {
      showSuccess('User deactivated successfully');
      queryClient.invalidateQueries({ queryKey: ['user', id] });
    },
    onError: () => {
      showError('Failed to deactivate user');
    },
  });

  if (isLoading || !user) {
    return <Loading />;
  }

  const isAdmin = currentUser?.role === 'admin';
  const isOwnProfile = currentUser?.id === user.id;
  const canEdit = isAdmin || isOwnProfile;
  const canDelete = isAdmin && !isOwnProfile;
  const canToggleStatus = isAdmin && !isOwnProfile;

  const openTasks = tasksData?.data.filter(t => t.status !== 'completed' && t.status !== 'cancelled').length || 0;
  const openTickets = ticketsData?.data.filter(t => t.status === 'open' || t.status === 'in_progress').length || 0;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Box display="flex" alignItems="center" gap={2}>
          <Avatar sx={{ width: 56, height: 56 }}>
            {user.first_name.charAt(0)}{user.last_name.charAt(0)}
          </Avatar>
          <Box>
            <Typography variant="h4">
              {user.first_name} {user.last_name}
            </Typography>
            <Box display="flex" gap={1} mt={1}>
              <Chip
                label={user.role.charAt(0).toUpperCase() + user.role.slice(1)}
                color={getRoleColor(user.role)}
                size="small"
              />
              <Chip
                icon={user.is_active ? <ActiveIcon /> : <BlockIcon />}
                label={user.is_active ? 'Active' : 'Inactive'}
                color={user.is_active ? 'success' : 'default'}
                size="small"
              />
            </Box>
          </Box>
        </Box>
        <Box display="flex" gap={1}>
          {canToggleStatus && (
            <Button
              variant="outlined"
              onClick={() => user.is_active ? deactivateMutation.mutate() : activateMutation.mutate()}
              startIcon={user.is_active ? <BlockIcon /> : <ActiveIcon />}
            >
              {user.is_active ? 'Deactivate' : 'Activate'}
            </Button>
          )}
          {canEdit && (
            <Button
              variant="outlined"
              startIcon={<EditIcon />}
              onClick={() => navigate(`/users/${id}/edit`)}
            >
              Edit
            </Button>
          )}
          {canDelete && (
            <IconButton
              color="error"
              onClick={() => setDeleteDialog(true)}
            >
              <DeleteIcon />
            </IconButton>
          )}
        </Box>
      </Box>

      <Paper>
        <Tabs
          value={tabValue}
          onChange={(_, newValue) => setTabValue(newValue)}
          sx={{ borderBottom: 1, borderColor: 'divider' }}
        >
          <Tab label="Overview" />
          <Tab label="Activity" />
          {(isAdmin || isOwnProfile) && <Tab label="Statistics" />}
        </Tabs>

        <TabPanel value={tabValue} index={0}>
          <Box sx={{ p: 3 }}>
            <Stack spacing={3}>
              <Box>
                <Typography variant="h6" gutterBottom>
                  Contact Information
                </Typography>
                <List>
                  <ListItem sx={{ px: 0 }}>
                    <ListItemIcon>
                      <PersonIcon />
                    </ListItemIcon>
                    <ListItemText
                      primary="Username"
                      secondary={user.username}
                    />
                  </ListItem>
                  <ListItem sx={{ px: 0 }}>
                    <ListItemIcon>
                      <EmailIcon />
                    </ListItemIcon>
                    <ListItemText
                      primary="Email"
                      secondary={
                        <a href={`mailto:${user.email}`}>{user.email}</a>
                      }
                    />
                  </ListItem>
                </List>
              </Box>

              <Divider />

              <Box>
                <Typography variant="h6" gutterBottom>
                  Account Information
                </Typography>
                <List>
                  <ListItem sx={{ px: 0 }}>
                    <ListItemIcon>
                      <WorkIcon />
                    </ListItemIcon>
                    <ListItemText
                      primary="Role"
                      secondary={user.role.charAt(0).toUpperCase() + user.role.slice(1)}
                    />
                  </ListItem>
                  <ListItem sx={{ px: 0 }}>
                    <ListItemIcon>
                      <CalendarIcon />
                    </ListItemIcon>
                    <ListItemText
                      primary="Member Since"
                      secondary={format(new Date(user.created_at), 'MMMM dd, yyyy')}
                    />
                  </ListItem>
                  <ListItem sx={{ px: 0 }}>
                    <ListItemIcon>
                      <CalendarIcon />
                    </ListItemIcon>
                    <ListItemText
                      primary="Last Updated"
                      secondary={format(new Date(user.updated_at), 'MMMM dd, yyyy HH:mm')}
                    />
                  </ListItem>
                </List>
              </Box>
            </Stack>
          </Box>
        </TabPanel>

        <TabPanel value={tabValue} index={1}>
          <Box sx={{ p: 3 }}>
            <Stack spacing={3}>
              <Box>
                <Typography variant="h6" gutterBottom>
                  Current Assignments
                </Typography>
                <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                  <Paper variant="outlined" sx={{ p: 2 }}>
                    <Box display="flex" alignItems="center" gap={1} mb={1}>
                      <TaskIcon color="primary" />
                      <Typography variant="subtitle1">Open Tasks</Typography>
                    </Box>
                    <Typography variant="h4">{openTasks}</Typography>
                  </Paper>
                  <Paper variant="outlined" sx={{ p: 2 }}>
                    <Box display="flex" alignItems="center" gap={1} mb={1}>
                      <TicketIcon color="warning" />
                      <Typography variant="subtitle1">Open Tickets</Typography>
                    </Box>
                    <Typography variant="h4">{openTickets}</Typography>
                  </Paper>
                </Box>
              </Box>

              {tasksData && tasksData.data.length > 0 && (
                <>
                  <Divider />
                  <Box>
                    <Typography variant="h6" gutterBottom>
                      Recent Tasks
                    </Typography>
                    <List>
                      {tasksData.data.slice(0, 5).map((task) => (
                        <ListItem
                          key={task.id}
                          sx={{ px: 0, cursor: 'pointer' }}
                          onClick={() => navigate(`/tasks/${task.id}`)}
                        >
                          <ListItemText
                            primary={task.title}
                            secondary={
                              <>
                                {task.status} • {task.priority} priority • Due {format(new Date(task.due_date), 'MMM dd, yyyy')}
                              </>
                            }
                          />
                        </ListItem>
                      ))}
                    </List>
                  </Box>
                </>
              )}
            </Stack>
          </Box>
        </TabPanel>

        {(isAdmin || isOwnProfile) && (
          <TabPanel value={tabValue} index={2}>
            <Box sx={{ p: 3 }}>
              <Typography variant="h6" gutterBottom>
                Performance Overview
              </Typography>
              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: 'repeat(3, 1fr)' }} gap={2}>
                <Paper variant="outlined" sx={{ p: 2 }}>
                  <Box display="flex" alignItems="center" gap={1} mb={1}>
                    <TaskIcon color="action" />
                    <Typography variant="subtitle2">Total Tasks</Typography>
                  </Box>
                  <Typography variant="h5">{tasksData?.total || 0}</Typography>
                </Paper>
                <Paper variant="outlined" sx={{ p: 2 }}>
                  <Box display="flex" alignItems="center" gap={1} mb={1}>
                    <TicketIcon color="action" />
                    <Typography variant="subtitle2">Total Tickets</Typography>
                  </Box>
                  <Typography variant="h5">{ticketsData?.total || 0}</Typography>
                </Paper>
                <Paper variant="outlined" sx={{ p: 2 }}>
                  <Box display="flex" alignItems="center" gap={1} mb={1}>
                    <TrendingUpIcon color="action" />
                    <Typography variant="subtitle2">Completion Rate</Typography>
                  </Box>
                  <Typography variant="h5">
                    {tasksData?.total
                      ? Math.round(
                          ((tasksData.data.filter(t => t.status === 'completed').length) /
                            tasksData.total) *
                            100
                        )
                      : 0}%
                  </Typography>
                </Paper>
              </Box>
            </Box>
          </TabPanel>
        )}
      </Paper>

      <ConfirmDialog
        open={deleteDialog}
        title="Delete User"
        message={`Are you sure you want to delete "${user.username}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};