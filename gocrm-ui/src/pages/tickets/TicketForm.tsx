import React, { useEffect, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
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
import { FormTextField, FormSelect } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { ticketsApi, customersApi, usersApi, type CreateTicketData, type UpdateTicketData } from '@/api/endpoints';
import type { Customer, User } from '@/types';

const ticketSchema = z.object({
  subject: z.string().min(1, 'Subject is required'),
  description: z.string().min(1, 'Description is required'),
  status: z.enum(['open', 'in_progress', 'resolved', 'closed']),
  priority: z.enum(['low', 'medium', 'high', 'urgent']),
  customer_id: z.number().min(1, 'Customer is required'),
  assigned_to_id: z.number().optional(),
});

type TicketFormData = z.infer<typeof ticketSchema>;

const statusOptions = [
  { value: 'open', label: 'Open' },
  { value: 'in_progress', label: 'In Progress' },
  { value: 'resolved', label: 'Resolved' },
  { value: 'closed', label: 'Closed' },
];

const priorityOptions = [
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'urgent', label: 'Urgent' },
];

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const isEditMode = !!id;
  
  const preselectedCustomerId = searchParams.get('customer_id');
  const [selectedCustomer, setSelectedCustomer] = useState<Customer | null>(null);
  const [selectedAgent, setSelectedAgent] = useState<User | null>(null);

  const methods = useForm<TicketFormData>({
    resolver: zodResolver(ticketSchema),
    defaultValues: {
      subject: '',
      description: '',
      status: 'open',
      priority: 'medium',
      customer_id: preselectedCustomerId ? Number(preselectedCustomerId) : 0,
      assigned_to_id: undefined,
    },
  });

  const { data: ticket, isLoading: ticketLoading } = useQuery({
    queryKey: ['ticket', id],
    queryFn: () => ticketsApi.getTicket(Number(id)),
    enabled: isEditMode,
  });

  const { data: customersData } = useQuery({
    queryKey: ['customers', { limit: 100 }],
    queryFn: () => customersApi.getCustomers({ limit: 100 }),
  });

  const { data: usersData } = useQuery({
    queryKey: ['users', { role: 'support', is_active: true }],
    queryFn: () => usersApi.getUsers({ role: 'support', is_active: true }),
  });

  const createMutation = useMutation({
    mutationFn: (data: CreateTicketData) => ticketsApi.createTicket(data),
    onSuccess: () => {
      showSuccess('Ticket created successfully');
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
      navigate('/tickets');
    },
    onError: () => {
      showError('Failed to create ticket');
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: UpdateTicketData }) =>
      ticketsApi.updateTicket(id, data),
    onSuccess: () => {
      showSuccess('Ticket updated successfully');
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
      queryClient.invalidateQueries({ queryKey: ['ticket', id] });
      navigate('/tickets');
    },
    onError: () => {
      showError('Failed to update ticket');
    },
  });

  useEffect(() => {
    if (ticket) {
      methods.reset({
        subject: ticket.subject,
        description: ticket.description,
        status: ticket.status,
        priority: ticket.priority,
        customer_id: ticket.customer_id,
        assigned_to_id: ticket.assigned_to_id || undefined,
      });
      setSelectedCustomer(ticket.customer || null);
      setSelectedAgent(ticket.assigned_to || null);
    }
  }, [ticket, methods]);

  useEffect(() => {
    if (preselectedCustomerId && customersData) {
      const customer = customersData.data.find(c => c.id === Number(preselectedCustomerId));
      if (customer) {
        setSelectedCustomer(customer);
      }
    }
  }, [preselectedCustomerId, customersData]);

  const onSubmit = (data: TicketFormData) => {
    if (isEditMode) {
      updateMutation.mutate({ id: Number(id), data });
    } else {
      createMutation.mutate(data);
    }
  };

  if (ticketLoading) {
    return <Loading />;
  }

  const customers = customersData?.data || [];
  const supportAgents = usersData?.data || [];

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">
          {isEditMode ? 'Edit Ticket' : 'Create New Ticket'}
        </Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)}>
            <Stack spacing={3}>
              <Typography variant="h6">Ticket Information</Typography>
              
              <FormTextField
                name="subject"
                label="Subject"
                required
                fullWidth
              />

              <FormTextField
                name="description"
                label="Description"
                required
                multiline
                rows={6}
                fullWidth
              />

              <Divider />

              <Typography variant="h6">Classification</Typography>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormSelect
                  name="priority"
                  label="Priority"
                  options={priorityOptions}
                  required
                />
                <FormSelect
                  name="status"
                  label="Status"
                  options={statusOptions}
                  required
                />
              </Box>

              <Divider />

              <Typography variant="h6">Assignment</Typography>

              <Autocomplete
                value={selectedCustomer}
                onChange={(_, newValue) => {
                  setSelectedCustomer(newValue);
                  methods.setValue('customer_id', newValue?.id || 0);
                }}
                options={customers}
                getOptionLabel={(option) => option.company_name}
                renderInput={(params) => (
                  <MuiTextField
                    {...params}
                    label="Customer"
                    required
                    error={!!methods.formState.errors.customer_id}
                    helperText={methods.formState.errors.customer_id?.message}
                  />
                )}
                disabled={!!preselectedCustomerId}
              />

              <Autocomplete
                value={selectedAgent}
                onChange={(_, newValue) => {
                  setSelectedAgent(newValue);
                  methods.setValue('assigned_to_id', newValue?.id || undefined);
                }}
                options={supportAgents}
                getOptionLabel={(option) => `${option.first_name} ${option.last_name}`}
                renderInput={(params) => (
                  <MuiTextField
                    {...params}
                    label="Assign to Support Agent (Optional)"
                  />
                )}
              />

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  startIcon={<CancelIcon />}
                  onClick={() => navigate('/tickets')}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} Ticket
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};