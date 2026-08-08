import React, { useState, useCallback, useMemo } from 'react';
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
  Autocomplete,
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
import { LabelChip } from '@/components/LabelChip';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { labelsApi, tasksApi, type TaskFilters } from '@/api/endpoints';
import type { Label, Task } from '@/types';
import { format, startOfMonth, endOfMonth, eachDayOfInterval, isSameDay, parseISO } from 'date-fns';

// Chips beyond this many collapse into a "+N" indicator so the column keeps a
// predictable width.
const MAX_VISIBLE_LABEL_CHIPS = 3;

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

  const { data: labels } = useQuery({
    queryKey: ['labels'],
    queryFn: () => labelsApi.getLabels(),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => tasksApi.deleteTask(id),
    onSuccess: () => {
      showSuccess('Task deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      // Deleting a task detaches it from its labels, so the counts move.
      queryClient.invalidateQueries({ queryKey: ['labels'] });
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

  // The filter is stored as an id; resolve it back to the label so the active
  // filter can be shown as a chip.
  const activeLabel = (labels || []).find(label => label.id === filters.label_id) ?? null;

  const handleLabelFilterChange = useCallback((label: Label | null) => {
    // The server applies label_id INSTEAD of search — the two never intersect —
    // so applying a label filter drops the search term rather than leaving a
    // populated box claiming a narrowing that is not happening. The box itself
    // is disabled below for as long as the label filter is active.
    setFilters(prev => ({
      ...prev,
      label_id: label?.id,
      search: label ? '' : prev.search,
      page: 1,
    }));
  }, []);

  const columns: Column<Task>[] = useMemo(() => [
    {
      id: 'title',
      label: 'Title',
      minWidth: 250,
    },
    {
      id: 'labels',
      label: 'Labels',
      minWidth: 180,
      sortable: false,
      format: (value: Task['labels']) => {
        const taskLabels = value || [];
        if (taskLabels.length === 0) {
          return null;
        }
        const visible = taskLabels.slice(0, MAX_VISIBLE_LABEL_CHIPS);
        const hidden = taskLabels.length - visible.length;
        return (
          <Box display="flex" gap={0.5} flexWrap="wrap" alignItems="center">
            {visible.map((label) => (
              <LabelChip
                key={label.id}
                label={label}
                clickable
                onClick={(event) => {
                  // The row itself navigates to the task; a chip click is a
                  // filter action, not a drill-down.
                  event.stopPropagation();
                  handleLabelFilterChange(label);
                }}
              />
            ))}
            {hidden > 0 && (
              <Typography variant="caption" color="text.secondary">
                +{hidden}
              </Typography>
            )}
          </Box>
        );
      },
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
  ], [handleLabelFilterChange]);

  const handleMenuOpen = useCallback((event: React.MouseEvent<HTMLElement>, task: Task) => {
    setAnchorEl(event.currentTarget);
    setSelectedTask(task);
  }, []);

  const handleMenuClose = useCallback(() => {
    setAnchorEl(null);
    setSelectedTask(null);
  }, []);

  const handleDelete = useCallback(() => {
    if (selectedTask) {
      setDeleteDialog({ open: true, task: selectedTask });
      handleMenuClose();
    }
  }, [selectedTask, handleMenuClose]);

  const handleComplete = useCallback(() => {
    if (selectedTask && selectedTask.status !== 'completed') {
      completeMutation.mutate(selectedTask.id);
      handleMenuClose();
    }
  }, [selectedTask, completeMutation, handleMenuClose]);

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    const fieldMap: Record<string, string> = {
      title: 'title',
      status: 'status',
      priority: 'priority',
      due_date: 'due_date',
      created_at: 'created_at',
    };
    const sortBy = fieldMap[field] || field;
    setFilters(prev => ({ ...prev, sort_by: sortBy, sort_order: order, page: 1 }));
  }, []);

  const handleSearch = useCallback((value: string) => {
    setFilters(prev => ({ ...prev, search: value, page: 1 }));
  }, []);

  const handleStatusChange = useCallback((status: string) => {
    setFilters(prev => ({ ...prev, status, page: 1 }));
  }, []);

  const handlePriorityChange = useCallback((priority: string) => {
    setFilters(prev => ({ ...prev, priority, page: 1 }));
  }, []);

  const handlePageChange = useCallback((page: number) => {
    setFilters(prev => ({ ...prev, page: page + 1 }));
  }, []);

  const handleRowsPerPageChange = useCallback((rowsPerPage: number) => {
    setFilters(prev => ({ ...prev, limit: rowsPerPage, page: 1 }));
  }, []);

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
            disabled={Boolean(activeLabel)}
            helperText={activeLabel ? 'Clear the label filter to search' : undefined}
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

          <Autocomplete
            size="small"
            sx={{ minWidth: 220 }}
            value={activeLabel}
            onChange={(_, newValue) => handleLabelFilterChange(newValue)}
            options={labels || []}
            isOptionEqualToValue={(option, value) => option.id === value.id}
            getOptionLabel={(option) => option.name}
            renderOption={(props, option) => {
              const { key, ...optionProps } = props;
              return (
                <li key={key} {...optionProps}>
                  <LabelChip label={option} />
                </li>
              );
            }}
            renderInput={(params) => <TextField {...params} label="Label" />}
          />

          {activeLabel && (
            <Box display="flex" gap={1} alignItems="center">
              <Typography variant="body2" color="text.secondary">
                Filtered by
              </Typography>
              <LabelChip
                label={activeLabel}
                onDelete={() => handleLabelFilterChange(null)}
                data-testid="active-label-filter"
              />
            </Box>
          )}
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
          onSort={handleSort}
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