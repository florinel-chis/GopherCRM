import React, { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useForm, FormProvider, type FieldErrors } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import {
  useQuery,
  useMutation,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Stack,
  Divider,
  Autocomplete,
  createFilterOptions,
  TextField as MuiTextField,
} from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { FormTextField, FormSelect, FormDatePicker } from '@/components/form';
import { LabelChip } from '@/components/LabelChip';
import { nextPaletteColor } from '@/components/labelColors';
import { Loading } from '@/components/Loading';
import { DealAutocomplete } from '@/components/DealAutocomplete';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import {
  labelsApi,
  tasksApi,
  usersApi,
  type CreateTaskData,
  type UpdateTaskData,
} from '@/api/endpoints';
import type { Label, User } from '@/types';
import { AxiosError } from 'axios';
import { addDays } from 'date-fns';

const taskSchema = z.object({
  title: z.string().min(1, 'Title is required').max(200),
  description: z.string().optional(),
  status: z.enum(['pending', 'in_progress', 'completed', 'cancelled']),
  priority: z.enum(['low', 'medium', 'high']),
  due_date: z.date(),
  assigned_to: z.number().optional(),
  label_ids: z.array(z.number()).optional(),
});

// Sentinel id for the synthetic 'Add "xyz"' entry the Autocomplete offers when
// the typed name matches no existing label. Real ids are always positive.
const CREATE_LABEL_OPTION_ID = -1;

type LabelOption = Label & { inputValue?: string };

const filterLabelOptions = createFilterOptions<LabelOption>();

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

// Every cached deal task list (DealTasksSection's ['deal', <id>, 'tasks',
// paging]) goes stale when a task is saved: its link, title or status may
// have moved, and the save does not say which deal it left.
const invalidateDealTaskLists = (queryClient: QueryClient) =>
  queryClient.invalidateQueries({
    predicate: (query) => query.queryKey[0] === 'deal' && query.queryKey[2] === 'tasks',
  });

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();
  const isEditMode = !!id;

  // POST /labels is limited to admin, sales and support, exactly as on the
  // label management page. Offering the inline "Add ..." option to a customer
  // would only ever produce a 403, so it is not offered at all.
  const canCreateLabels =
    user?.role === 'admin' || user?.role === 'sales' || user?.role === 'support';

  // Only admin and sales may list deals (GET /deals), so only they get the
  // picker; support and customer never send deal_id and leave the link alone.
  const canLinkDeal = user?.role === 'admin' || user?.role === 'sales';

  const [selectedAssignee, setSelectedAssignee] = useState<User | null>(null);
  const [selectedLabels, setSelectedLabels] = useState<Label[]>([]);
  const [dealId, setDealId] = useState<number | null>(null);
  // Set only by the picker's onChange. An untouched picker sends no deal_id on
  // an update, so a pre-filled link, or a deal_id the API returned without a
  // summary (an empty picker), is never rewritten or cleared by accident.
  const [dealTouched, setDealTouched] = useState(false);

  const methods = useForm<TaskFormData>({
    resolver: zodResolver(taskSchema),
    defaultValues: {
      title: '',
      description: '',
      status: 'pending',
      priority: 'medium',
      due_date: addDays(new Date(), 1), // Default to tomorrow
      assigned_to: undefined,
      label_ids: [],
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

  const { data: labels } = useQuery({
    queryKey: ['labels'],
    queryFn: () => labelsApi.getLabels(),
  });

  const createMutation = useMutation({
    mutationFn: (data: CreateTaskData) => tasksApi.createTask(data),
    onSuccess: () => {
      showSuccess('Task created successfully');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      // Label task counts move whenever a task's label set changes.
      queryClient.invalidateQueries({ queryKey: ['labels'] });
      invalidateDealTaskLists(queryClient);
      navigate('/tasks');
    },
    onError: (error) => {
      console.error('Failed to create task:', error);
      showError('Failed to create task');
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: UpdateTaskData }) =>
      tasksApi.updateTask(id, data),
    onSuccess: async (_updated, variables) => {
      showSuccess('Task updated successfully');
      queryClient.invalidateQueries({ queryKey: ['tasks'] });
      queryClient.invalidateQueries({ queryKey: ['labels'] });
      invalidateDealTaskLists(queryClient);
      // The PUT body is not trusted for the deal link: a clear of a link the
      // caller cannot see answers 200 and keeps it. Re-read the task and show
      // what the API holds, so a refused clear never looks cleared.
      try {
        await queryClient.fetchQuery({
          queryKey: ['task', id],
          queryFn: () => tasksApi.getTask(variables.id),
          staleTime: 0,
          retry: false,
        });
      } catch {
        // Never fill the entry from the PUT body; mark it stale so TaskDetail
        // fetches it again and shows its own loading or error state.
        await queryClient.invalidateQueries({ queryKey: ['task', id], refetchType: 'none' });
      }
      navigate(`/tasks/${variables.id}`);
    },
    onError: (error) => {
      console.error('Failed to update task:', error);
      showError('Failed to update task');
    },
  });

  const appendLabel = (label: Label) => {
    if (selectedLabels.some((selected) => selected.id === label.id)) {
      return;
    }
    const next = [...selectedLabels, label];
    setSelectedLabels(next);
    methods.setValue(
      'label_ids',
      next.map((entry) => entry.id)
    );
  };

  // Inline label creation from the Autocomplete: the colour is handed out from
  // the preset palette so the user never has to pick one mid-flow.
  const createLabelMutation = useMutation({
    mutationFn: (name: string) =>
      labelsApi.createLabel({
        name,
        color: nextPaletteColor((labels || []).map((existing) => existing.color)),
      }),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['labels'] });
      appendLabel(created);
      showSuccess(`Label "${created.name}" created`);
    },
    onError: async (error, attemptedName) => {
      // A 409 means the label exists server-side but was missing from the
      // cached list (created elsewhere within the staleTime window). Recover
      // by fetching the fresh list and selecting the real label instead of
      // dead-ending on an error.
      if (error instanceof AxiosError && error.response?.status === 409) {
        try {
          const fresh = await queryClient.fetchQuery({
            queryKey: ['labels'],
            queryFn: () => labelsApi.getLabels(),
            staleTime: 0,
          });
          const existing = fresh.find(
            (label) => label.name.toLowerCase() === attemptedName.trim().toLowerCase()
          );
          if (existing) {
            appendLabel(existing);
            showSuccess(`Label "${existing.name}" already existed and was selected`);
            return;
          }
        } catch {
          // Refetch failed; fall through to the generic report.
        }
      }
      console.error('Failed to create label:', error);
      showError('Failed to create label');
    },
  });

  useEffect(() => {
    if (task) {
      methods.reset({
        title: task.title,
        description: task.description || '',
        status: task.status,
        priority: task.priority,
        due_date: task.due_date ? new Date(task.due_date) : undefined,
        assigned_to: task.assigned_to,
        label_ids: (task.labels || []).map((label) => label.id),
      });
      if (task.assignee) {
        setSelectedAssignee(task.assignee);
      }
      setSelectedLabels(task.labels || []);
      // Pre-filled from the summary only; a deal_id without a summary (a
      // deal the caller cannot see, or a deleted one) leaves the picker empty.
      setDealId(task.deal?.id ?? null);
      setDealTouched(false);
    }
  }, [task, methods]);

  const handleLabelsChange = (selection: LabelOption[]) => {
    const pendingCreate = selection.find(
      (option) => option.id === CREATE_LABEL_OPTION_ID && option.inputValue
    );
    if (pendingCreate?.inputValue) {
      // Do not add the synthetic option to the selection; the real label is
      // appended once the API returns it. Roles that cannot create labels are
      // never offered the option, and it is dropped here as well so it can
      // never reach the selection as a phantom id.
      if (canCreateLabels) {
        createLabelMutation.mutate(pendingCreate.inputValue.trim());
      }
      return;
    }
    setSelectedLabels(selection);
    methods.setValue(
      'label_ids',
      selection.map((label) => label.id)
    );
  };

  const onSubmit = (data: TaskFormData) => {
    const submitData = {
      ...data,
      due_date: data.due_date.toISOString(),
      // Ensure description is never undefined for API consistency
      description: data.description || '',
      label_ids: selectedLabels.map((label) => label.id),
    };

    if (isEditMode) {
      const updateData: UpdateTaskData = { ...submitData };
      // Only a touched picker sends deal_id: an id replaces the link, 0 clears it.
      if (canLinkDeal && dealTouched) {
        updateData.deal_id = dealId ?? 0;
      }
      updateMutation.mutate({ id: Number(id), data: updateData });
    } else {
      const createData: CreateTaskData = { ...submitData };
      if (canLinkDeal && dealId !== null) {
        createData.deal_id = dealId;
      }
      createMutation.mutate(createData);
    }
  };

  const onError = (errors: FieldErrors<TaskFormData>) => {
    console.error('Form validation errors:', errors);
  };

  if (taskLoading) {
    return <Loading />;
  }

  const users = usersData?.data || [];
  const labelOptions: LabelOption[] = labels || [];

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">
          {isEditMode ? 'Edit Task' : 'Create New Task'}
        </Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit, onError)}>
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

              {canLinkDeal && (
                <DealAutocomplete
                  openOnly={false}
                  testId="task-deal-autocomplete"
                  value={dealId}
                  initialDeal={task?.deal ?? null}
                  onChange={(nextId) => {
                    setDealId(nextId);
                    setDealTouched(true);
                  }}
                  label="Deal"
                  helperText="Optional. Closed deals can be linked too."
                />
              )}

              <Autocomplete
                multiple
                value={selectedLabels as LabelOption[]}
                onChange={(_, newValue) => handleLabelsChange(newValue)}
                options={labelOptions}
                disabled={createLabelMutation.isPending}
                isOptionEqualToValue={(option, value) => option.id === value.id}
                getOptionLabel={(option) => option.inputValue ?? option.name}
                filterOptions={(options, params) => {
                  const filtered = filterLabelOptions(options, params);
                  if (!canCreateLabels) {
                    return filtered;
                  }
                  const typed = params.inputValue.trim();
                  const alreadyExists = options.some(
                    (option) => option.name.toLowerCase() === typed.toLowerCase()
                  );
                  if (typed !== '' && !alreadyExists) {
                    filtered.push({
                      id: CREATE_LABEL_OPTION_ID,
                      name: `Add "${typed}"`,
                      color: nextPaletteColor((labels || []).map((label) => label.color)),
                      inputValue: typed,
                    });
                  }
                  return filtered;
                }}
                renderOption={(props, option) => {
                  const { key, ...optionProps } = props;
                  return (
                    <li key={key} {...optionProps}>
                      {option.id === CREATE_LABEL_OPTION_ID ? (
                        option.name
                      ) : (
                        <LabelChip label={option} />
                      )}
                    </li>
                  );
                }}
                renderValue={(value, getItemProps) =>
                  value.map((option, index) => {
                    const { key, ...itemProps } = getItemProps({ index });
                    return <LabelChip key={key} label={option} {...itemProps} />;
                  })
                }
                renderInput={(params) => (
                  <MuiTextField
                    {...params}
                    label="Labels"
                    placeholder="Add a label"
                    helperText={
                      canCreateLabels
                        ? 'Type a new name to create a label on the fly'
                        : 'Pick from the existing labels'
                    }
                  />
                )}
              />

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