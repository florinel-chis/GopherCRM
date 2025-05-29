import React, { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useForm, FormProvider } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Stack,
  Divider,
  Autocomplete,
  TextField as MuiTextField,
} from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { FormTextField, FormSelect, FormDatePicker } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { tasksApi, usersApi, type CreateTaskData, type UpdateTaskData } from '@/api/endpoints';
import type { User } from '@/types';
import { addDays } from 'date-fns';

const taskSchema = z.object({
  title: z.string().min(1, 'Title is required').max(200),
  description: z.string().optional(),
  status: z.enum(['pending', 'in_progress', 'completed', 'cancelled']),
  priority: z.enum(['low', 'medium', 'high']),
  due_date: z.date(),
  assigned_to: z.number().optional(),
});

type TaskFormData = z.infer<typeof taskSchema>;

const statusOptions = [
  { value: 'pending', label: 'Pending' },
  { value: 'in_progress', label: 'In Progress' },
  { value: 'completed', label: 'Completed' },
  { value: 'cancelled', label: 'Cancelled' },
];

const priorityOptions = [
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
];

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const isEditMode = !!id;
  
  const [selectedAssignee, setSelectedAssignee] = useState<User | null>(null);

  const methods = useForm<TaskFormData>({
    resolver: zodResolver(taskSchema),
    defaultValues: {
      title: '',
      description: '',
      status: 'pending',
      priority: 'medium',
      due_date: addDays(new Date(), 1), // Default to tomorrow
      assigned_to: undefined,
    },
  });

  const { data: task, isLoading: taskLoading } = useQuery({
    queryKey: ['task', id],
    queryFn: () => tasksApi.getTask(Number(id)),
    enabled: isEditMode,
  });

  const { data: usersData } = useQuery({
    queryKey: ['users', { is_active: true }],
    queryFn: () => usersApi.getUsers({ is_active: true }),
  });

  const createMutation = useMutation({
    mutationFn: (data: CreateTaskData) => tasksApi.createTask(data),
    onSuccess: () => {
      showSuccess('Task created successfully');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      navigate('/tasks');
    },
    onError: () => {
      showError('Failed to create task');
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: UpdateTaskData }) =>
      tasksApi.updateTask(id, data),
    onSuccess: () => {
      showSuccess('Task updated successfully');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      queryClient.invalidateQueries({ queryKey: ['task', id] });
      navigate('/tasks');
    },
    onError: () => {
      showError('Failed to update task');
    },
  });

  useEffect(() => {
    if (task) {
      methods.reset({
        title: task.title,
        description: task.description || '',
        status: task.status,
        priority: task.priority,
        due_date: new Date(task.due_date),
        assigned_to: task.assigned_to,
      });
      if (task.assignee) {
        setSelectedAssignee(task.assignee);
      }
    }
  }, [task, methods]);

  const onSubmit = (data: TaskFormData) => {
    const submitData = {
      ...data,
      due_date: data.due_date.toISOString(),
    };

    if (isEditMode) {
      updateMutation.mutate({ id: Number(id), data: submitData });
    } else {
      createMutation.mutate(submitData as CreateTaskData);
    }
  };

  if (taskLoading) {
    return <Loading />;
  }

  const users = usersData?.data || [];

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">
          {isEditMode ? 'Edit Task' : 'Create New Task'}
        </Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)}>
            <Stack spacing={3}>
              <Typography variant="h6">Task Information</Typography>
              
              <FormTextField
                name="title"
                label="Title"
                required
                fullWidth
              />

              <FormTextField
                name="description"
                label="Description"
                multiline
                rows={4}
                fullWidth
              />

              <Divider />

              <Typography variant="h6">Task Details</Typography>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormSelect
                  name="status"
                  label="Status"
                  options={statusOptions}
                  required
                />
                <FormSelect
                  name="priority"
                  label="Priority"
                  options={priorityOptions}
                  required
                />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormDatePicker
                  name="due_date"
                  label="Due Date"
                  required
                />
                <Autocomplete
                  value={selectedAssignee}
                  onChange={(_, newValue) => {
                    setSelectedAssignee(newValue);
                    methods.setValue('assigned_to', newValue?.id || undefined);
                  }}
                  options={users}
                  getOptionLabel={(option) => `${option.first_name} ${option.last_name}`}
                  renderInput={(params) => (
                    <MuiTextField
                      {...params}
                      label="Assign To (Optional)"
                    />
                  )}
                />
              </Box>

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  startIcon={<CancelIcon />}
                  onClick={() => navigate('/tasks')}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} Task
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};