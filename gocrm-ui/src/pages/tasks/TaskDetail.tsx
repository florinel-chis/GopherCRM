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
  List,
  ListItem,
  ListItemIcon,
  ListItemText,
} from '@mui/material';
import {
  Edit as EditIcon,
  Delete as DeleteIcon,
  Person as PersonIcon,
  CalendarToday as CalendarIcon,
  Assignment as AssignmentIcon,
  Flag as FlagIcon,
  CheckCircle as CompleteIcon,
  CheckCircle,
  Cancel as CancelIcon,
  Schedule as ScheduleIcon,
  Description as DescriptionIcon,
  History as HistoryIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useSnackbar } from '@/hooks/useSnackbar';
import { tasksApi } from '@/api/endpoints';
import type { Task } from '@/types';
import { format, formatDistanceToNow } from 'date-fns';

const getStatusColor = (status: Task['status']) => {
  switch (status) {
    case 'pending':
      return 'default';
    case 'in_progress':
      return 'warning';
    case 'completed':
      return 'success';
    case 'cancelled':
      return 'error';
    default:
      return 'default';
  }
};

const getPriorityColor = (priority: Task['priority']) => {
  switch (priority) {
    case 'low':
      return 'default';
    case 'medium':
      return 'primary';
    case 'high':
      return 'error';
    default:
      return 'default';
  }
};

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  
  const [deleteDialog, setDeleteDialog] = useState(false);

  const { data: task, isLoading } = useQuery({
    queryKey: ['task', id],
    queryFn: () => tasksApi.getTask(Number(id)),
    enabled: !!id,
  });

  const deleteMutation = useMutation({
    mutationFn: () => tasksApi.deleteTask(Number(id)),
    onSuccess: () => {
      showSuccess('Task deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      navigate('/tasks');
    },
    onError: () => {
      showError('Failed to delete task');
    },
  });

  const updateStatusMutation = useMutation({
    mutationFn: (status: Task['status']) => 
      tasksApi.updateTask(Number(id), { status }),
    onSuccess: () => {
      showSuccess('Task status updated successfully');
      queryClient.invalidateQueries({ queryKey: ['task', id] });
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
    onError: () => {
      showError('Failed to update task status');
    },
  });

  const handleStatusChange = (newStatus: Task['status']) => {
    updateStatusMutation.mutate(newStatus);
  };

  const handleComplete = () => {
    handleStatusChange('completed');
  };

  const handleCancel = () => {
    handleStatusChange('cancelled');
  };

  if (isLoading || !task) {
    return <Loading />;
  }

  const isOverdue = new Date(task.due_date) < new Date() && task.status !== 'completed';

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Box display="flex" alignItems="center" gap={2}>
          <Typography variant="h4">{task.title}</Typography>
          <Chip
            label={task.status.split('_').map(s => s.charAt(0).toUpperCase() + s.slice(1)).join(' ')}
            color={getStatusColor(task.status)}
          />
          <Chip
            label={task.priority.charAt(0).toUpperCase() + task.priority.slice(1)}
            color={getPriorityColor(task.priority)}
            size="small"
          />
          {isOverdue && (
            <Chip
              label="Overdue"
              color="error"
              size="small"
            />
          )}
        </Box>
        <Box display="flex" gap={1}>
          {task.status === 'pending' && (
            <Button
              variant="contained"
              color="warning"
              onClick={() => handleStatusChange('in_progress')}
            >
              Start Task
            </Button>
          )}
          {task.status === 'in_progress' && (
            <Button
              variant="contained"
              color="success"
              startIcon={<CompleteIcon />}
              onClick={handleComplete}
            >
              Complete
            </Button>
          )}
          <Button
            variant="outlined"
            startIcon={<EditIcon />}
            onClick={() => navigate(`/tasks/${id}/edit`)}
          >
            Edit
          </Button>
          <IconButton
            color="error"
            onClick={() => setDeleteDialog(true)}
          >
            <DeleteIcon />
          </IconButton>
        </Box>
      </Box>

      <Paper sx={{ p: 3 }}>
        <Stack spacing={3}>
          <Box>
            <Typography variant="h6" gutterBottom>
              Task Details
            </Typography>
            <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={3}>
              <Box>
                <Box display="flex" alignItems="center" gap={1} mb={2}>
                  <FlagIcon color="action" />
                  <Box>
                    <Typography variant="caption" color="text.secondary">
                      Priority
                    </Typography>
                    <Typography>
                      {task.priority.charAt(0).toUpperCase() + task.priority.slice(1)}
                    </Typography>
                  </Box>
                </Box>
                
                <Box display="flex" alignItems="center" gap={1} mb={2}>
                  <CalendarIcon color="action" />
                  <Box>
                    <Typography variant="caption" color="text.secondary">
                      Due Date
                    </Typography>
                    <Typography color={isOverdue ? 'error' : 'text.primary'}>
                      {format(new Date(task.due_date), 'MMM dd, yyyy')} 
                      ({formatDistanceToNow(new Date(task.due_date), { addSuffix: true })})
                    </Typography>
                  </Box>
                </Box>
                
                <Box display="flex" alignItems="center" gap={1}>
                  <PersonIcon color="action" />
                  <Box>
                    <Typography variant="caption" color="text.secondary">
                      Created By
                    </Typography>
                    <Typography>
                      {task.creator ? `${task.creator.first_name} ${task.creator.last_name}` : 'Unknown'}
                    </Typography>
                  </Box>
                </Box>
              </Box>
              
              <Box>
                <Box display="flex" alignItems="center" gap={1} mb={2}>
                  <AssignmentIcon color="action" />
                  <Box>
                    <Typography variant="caption" color="text.secondary">
                      Assigned To
                    </Typography>
                    <Typography>
                      {task.assignee ? `${task.assignee.first_name} ${task.assignee.last_name}` : 'Unassigned'}
                    </Typography>
                  </Box>
                </Box>
                
                <Box display="flex" alignItems="center" gap={1} mb={2}>
                  <CalendarIcon color="action" />
                  <Box>
                    <Typography variant="caption" color="text.secondary">
                      Created
                    </Typography>
                    <Typography>
                      {format(new Date(task.created_at), 'MMM dd, yyyy HH:mm')}
                    </Typography>
                  </Box>
                </Box>
                
                {task.completed_at && (
                  <Box display="flex" alignItems="center" gap={1}>
                    <CheckCircle color="success" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Completed
                      </Typography>
                      <Typography>
                        {format(new Date(task.completed_at), 'MMM dd, yyyy HH:mm')}
                      </Typography>
                    </Box>
                  </Box>
                )}
              </Box>
            </Box>
          </Box>

          {task.description && (
            <>
              <Divider />
              <Box>
                <Box display="flex" alignItems="center" gap={1} mb={2}>
                  <DescriptionIcon color="action" />
                  <Typography variant="h6">Description</Typography>
                </Box>
                <Typography variant="body1" sx={{ whiteSpace: 'pre-wrap' }}>
                  {task.description}
                </Typography>
              </Box>
            </>
          )}

          <Divider />

          <Box>
            <Box display="flex" alignItems="center" gap={1} mb={2}>
              <HistoryIcon />
              <Typography variant="h6">Status History</Typography>
            </Box>
            <List>
              <ListItem sx={{ px: 0 }}>
                <ListItemIcon>
                  <ScheduleIcon color="action" />
                </ListItemIcon>
                <ListItemText
                  primary="Task created"
                  secondary={format(new Date(task.created_at), 'MMM dd, yyyy HH:mm')}
                />
              </ListItem>
              {task.status === 'in_progress' && (
                <ListItem sx={{ px: 0 }}>
                  <ListItemIcon>
                    <ScheduleIcon color="warning" />
                  </ListItemIcon>
                  <ListItemText
                    primary="Task started"
                    secondary={format(new Date(task.updated_at), 'MMM dd, yyyy HH:mm')}
                  />
                </ListItem>
              )}
              {task.completed_at && (
                <ListItem sx={{ px: 0 }}>
                  <ListItemIcon>
                    <CompleteIcon color="success" />
                  </ListItemIcon>
                  <ListItemText
                    primary="Task completed"
                    secondary={format(new Date(task.completed_at), 'MMM dd, yyyy HH:mm')}
                  />
                </ListItem>
              )}
              {task.status === 'cancelled' && (
                <ListItem sx={{ px: 0 }}>
                  <ListItemIcon>
                    <CancelIcon color="error" />
                  </ListItemIcon>
                  <ListItemText
                    primary="Task cancelled"
                    secondary={format(new Date(task.updated_at), 'MMM dd, yyyy HH:mm')}
                  />
                </ListItem>
              )}
            </List>
          </Box>

          {task.status !== 'completed' && task.status !== 'cancelled' && (
            <>
              <Divider />
              <Box display="flex" gap={2}>
                <Button
                  variant="outlined"
                  color="error"
                  startIcon={<CancelIcon />}
                  onClick={handleCancel}
                >
                  Cancel Task
                </Button>
              </Box>
            </>
          )}
        </Stack>
      </Paper>

      <ConfirmDialog
        open={deleteDialog}
        title="Delete Task"
        message={`Are you sure you want to delete "${task.title}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};