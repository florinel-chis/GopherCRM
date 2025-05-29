import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Chip,
  IconButton,
  Menu,
  MenuItem,
  FormControl,
  InputLabel,
  Select,
  TextField,
  InputAdornment,
  ToggleButton,
  ToggleButtonGroup,
  Card,
  CardContent,
  Stack,
} from '@mui/material';
import {
  Add as AddIcon,
  Search as SearchIcon,
  MoreVert as MoreVertIcon,
  ViewList as ListIcon,
  CalendarMonth as CalendarIcon,
  CheckCircle as CompleteIcon,
} from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { tasksApi, type TaskFilters } from '@/api/endpoints';
import type { Task } from '@/types';
import { format, startOfMonth, endOfMonth, eachDayOfInterval, isSameDay, parseISO } from 'date-fns';

const statusOptions = [
  { value: '', label: 'All Statuses' },
  { value: 'pending', label: 'Pending' },
  { value: 'in_progress', label: 'In Progress' },
  { value: 'completed', label: 'Completed' },
  { value: 'cancelled', label: 'Cancelled' },
];

const priorityOptions = [
  { value: '', label: 'All Priorities' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
];

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
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  
  const [viewMode, setViewMode] = useState<'list' | 'calendar'>('list');
  const [filters, setFilters] = useState<TaskFilters>({
    page: 1,
    limit: 10,
    status: '',
    priority: '',
    search: '',
  });
  
  const [deleteDialog, setDeleteDialog] = useState<{
    open: boolean;
    task?: Task;
  }>({ open: false });
  
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);
  const [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const [selectedMonth, setSelectedMonth] = useState(new Date());

  const { data, isLoading } = useQuery({
    queryKey: ['tasks', filters],
    queryFn: () => tasksApi.getTasks(filters),
  });

  const deleteMutation = useMutation({
    mutationFn: tasksApi.deleteTask,
    onSuccess: () => {
      showSuccess('Task deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      setDeleteDialog({ open: false });
    },
    onError: () => {
      showError('Failed to delete task');
    },
  });

  const completeMutation = useMutation({
    mutationFn: (id: number) => tasksApi.updateTask(id, { status: 'completed' }),
    onSuccess: () => {
      showSuccess('Task marked as completed');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
    },
    onError: () => {
      showError('Failed to complete task');
    },
  });

  const columns: Column<Task>[] = [
    {
      id: 'title',
      label: 'Title',
      minWidth: 250,
    },
    {
      id: 'status',
      label: 'Status',
      minWidth: 100,
      format: (value: Task['status']) => (
        <Chip
          label={value.split('_').map(s => s.charAt(0).toUpperCase() + s.slice(1)).join(' ')}
          color={getStatusColor(value)}
          size="small"
        />
      ),
    },
    {
      id: 'priority',
      label: 'Priority',
      minWidth: 90,
      format: (value: Task['priority']) => (
        <Chip
          label={value.charAt(0).toUpperCase() + value.slice(1)}
          color={getPriorityColor(value)}
          size="small"
        />
      ),
    },
    {
      id: 'assignee',
      label: 'Assigned To',
      minWidth: 120,
      format: (value: any) => {
        if (value) {
          return `${value.first_name} ${value.last_name}`;
        }
        return 'Unassigned';
      },
    },
    {
      id: 'due_date',
      label: 'Due Date',
      minWidth: 100,
      format: (value: string) => format(new Date(value), 'MMM dd, yyyy'),
    },
    {
      id: 'created_at',
      label: 'Created',
      minWidth: 100,
      format: (value: string) => format(new Date(value), 'MMM dd, yyyy'),
    },
  ];

  const handleMenuOpen = (event: React.MouseEvent<HTMLElement>, task: Task) => {
    setAnchorEl(event.currentTarget);
    setSelectedTask(task);
  };

  const handleMenuClose = () => {
    setAnchorEl(null);
    setSelectedTask(null);
  };

  const handleDelete = () => {
    if (selectedTask) {
      setDeleteDialog({ open: true, task: selectedTask });
      handleMenuClose();
    }
  };

  const handleComplete = () => {
    if (selectedTask && selectedTask.status !== 'completed') {
      completeMutation.mutate(selectedTask.id);
      handleMenuClose();
    }
  };

  const handleSearch = (value: string) => {
    setFilters({ ...filters, search: value, page: 1 });
  };

  const handleStatusChange = (status: string) => {
    setFilters({ ...filters, status, page: 1 });
  };

  const handlePriorityChange = (priority: string) => {
    setFilters({ ...filters, priority, page: 1 });
  };

  const handlePageChange = (page: number) => {
    setFilters({ ...filters, page: page + 1 });
  };

  const handleRowsPerPageChange = (rowsPerPage: number) => {
    setFilters({ ...filters, limit: rowsPerPage, page: 1 });
  };

  const renderCalendarView = () => {
    const monthStart = startOfMonth(selectedMonth);
    const monthEnd = endOfMonth(selectedMonth);
    const days = eachDayOfInterval({ start: monthStart, end: monthEnd });
    const tasks = data?.data || [];

    const getTasksForDay = (day: Date) => {
      return tasks.filter(task => {
        const dueDate = parseISO(task.due_date);
        return isSameDay(dueDate, day);
      });
    };

    return (
      <Paper sx={{ p: 2 }}>
        <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
          <Typography variant="h6">
            {format(selectedMonth, 'MMMM yyyy')}
          </Typography>
          <Box>
            <Button onClick={() => setSelectedMonth(new Date(selectedMonth.getFullYear(), selectedMonth.getMonth() - 1))}>
              Previous
            </Button>
            <Button onClick={() => setSelectedMonth(new Date())}>Today</Button>
            <Button onClick={() => setSelectedMonth(new Date(selectedMonth.getFullYear(), selectedMonth.getMonth() + 1))}>
              Next
            </Button>
          </Box>
        </Box>
        <Box display="grid" gridTemplateColumns="repeat(7, 1fr)" gap={1}>
          {['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].map(day => (
            <Typography key={day} variant="subtitle2" align="center" sx={{ fontWeight: 'bold' }}>
              {day}
            </Typography>
          ))}
          {days.map(day => {
            const dayTasks = getTasksForDay(day);
            return (
              <Card key={day.toISOString()} variant="outlined" sx={{ minHeight: 100 }}>
                <CardContent sx={{ p: 1 }}>
                  <Typography variant="caption" color="text.secondary">
                    {format(day, 'd')}
                  </Typography>
                  <Stack spacing={0.5} mt={1}>
                    {dayTasks.slice(0, 3).map(task => (
                      <Box
                        key={task.id}
                        sx={{
                          fontSize: '0.75rem',
                          p: 0.5,
                          bgcolor: 'primary.light',
                          borderRadius: 1,
                          cursor: 'pointer',
                          '&:hover': { bgcolor: 'primary.main', color: 'white' },
                        }}
                        onClick={() => navigate(`/tasks/${task.id}`)}
                      >
                        {task.title}
                      </Box>
                    ))}
                    {dayTasks.length > 3 && (
                      <Typography variant="caption" color="text.secondary">
                        +{dayTasks.length - 3} more
                      </Typography>
                    )}
                  </Stack>
                </CardContent>
              </Card>
            );
          })}
        </Box>
      </Paper>
    );
  };

  if (isLoading && !data) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Tasks</Typography>
        <Box display="flex" gap={2} alignItems="center">
          <ToggleButtonGroup
            value={viewMode}
            exclusive
            onChange={(_, newMode) => newMode && setViewMode(newMode)}
            size="small"
          >
            <ToggleButton value="list">
              <ListIcon />
            </ToggleButton>
            <ToggleButton value="calendar">
              <CalendarIcon />
            </ToggleButton>
          </ToggleButtonGroup>
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => navigate('/tasks/new')}
          >
            Create Task
          </Button>
        </Box>
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center" flexWrap="wrap">
          <TextField
            size="small"
            placeholder="Search tasks..."
            value={filters.search}
            onChange={(e) => handleSearch(e.target.value)}
            InputProps={{
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon />
                </InputAdornment>
              ),
            }}
            sx={{ minWidth: 300 }}
          />
          
          <FormControl size="small" sx={{ minWidth: 150 }}>
            <InputLabel>Status</InputLabel>
            <Select
              value={filters.status || ''}
              onChange={(e) => handleStatusChange(e.target.value)}
              label="Status"
            >
              {statusOptions.map((option) => (
                <MenuItem key={option.value} value={option.value}>
                  {option.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl size="small" sx={{ minWidth: 150 }}>
            <InputLabel>Priority</InputLabel>
            <Select
              value={filters.priority || ''}
              onChange={(e) => handlePriorityChange(e.target.value)}
              label="Priority"
            >
              {priorityOptions.map((option) => (
                <MenuItem key={option.value} value={option.value}>
                  {option.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
        </Box>
      </Paper>

      {viewMode === 'list' ? (
        <DataTable
          columns={columns}
          data={data?.data || []}
          totalCount={data?.total || 0}
          page={(filters.page || 1) - 1}
          rowsPerPage={filters.limit || 10}
          loading={isLoading}
          onPageChange={handlePageChange}
          onRowsPerPageChange={handleRowsPerPageChange}
          onRowClick={(task) => navigate(`/tasks/${task.id}`)}
          onEdit={(task) => navigate(`/tasks/${task.id}/edit`)}
          onDelete={(task) => setDeleteDialog({ open: true, task })}
          actions={
            <>
              <IconButton
                size="small"
                onClick={(e) => selectedTask && handleMenuOpen(e, selectedTask)}
              >
                <MoreVertIcon />
              </IconButton>
              <Menu
                anchorEl={anchorEl}
                open={Boolean(anchorEl)}
                onClose={handleMenuClose}
              >
                <MenuItem onClick={() => selectedTask && navigate(`/tasks/${selectedTask.id}`)}>
                  View Details
                </MenuItem>
                <MenuItem onClick={() => selectedTask && navigate(`/tasks/${selectedTask.id}/edit`)}>
                  Edit
                </MenuItem>
                {selectedTask?.status !== 'completed' && (
                  <MenuItem onClick={handleComplete}>
                    <CompleteIcon fontSize="small" sx={{ mr: 1 }} />
                    Mark as Complete
                  </MenuItem>
                )}
                <MenuItem onClick={handleDelete}>
                  Delete
                </MenuItem>
              </Menu>
            </>
          }
        />
      ) : (
        renderCalendarView()
      )}

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete Task"
        message={`Are you sure you want to delete the task "${deleteDialog.task?.title}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (deleteDialog.task) {
            deleteMutation.mutate(deleteDialog.task.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};