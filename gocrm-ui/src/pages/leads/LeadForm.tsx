import React, { useEffect } from 'react';
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
} from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { FormTextField, FormSelect } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { useAuth } from '@/hooks/useAuth';
import { leadsApi, type CreateLeadData, type UpdateLeadData } from '@/api/endpoints';

const leadSchema = z.object({
  company_name: z.string().min(1, 'Company name is required'),
  contact_name: z.string().min(1, 'Contact name is required'),
  email: z.string().email('Invalid email address'),
  phone: z.string().min(1, 'Phone number is required'),
  status: z.enum(['new', 'contacted', 'qualified', 'converted', 'lost']),
  source: z.string().min(1, 'Lead source is required'),
  notes: z.string().optional(),
});

type LeadFormData = z.infer<typeof leadSchema>;

const statusOptions = [
  { value: 'new', label: 'New' },
  { value: 'contacted', label: 'Contacted' },
  { value: 'qualified', label: 'Qualified' },
  { value: 'converted', label: 'Converted' },
  { value: 'lost', label: 'Lost' },
];

const sourceOptions = [
  { value: 'website', label: 'Website' },
  { value: 'referral', label: 'Referral' },
  { value: 'cold_call', label: 'Cold Call' },
  { value: 'advertisement', label: 'Advertisement' },
  { value: 'social_media', label: 'Social Media' },
  { value: 'email_campaign', label: 'Email Campaign' },
  { value: 'trade_show', label: 'Trade Show' },
  { value: 'other', label: 'Other' },
];

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { user } = useAuth();
  const isEditMode = !!id;

  const methods = useForm<LeadFormData>({
    resolver: zodResolver(leadSchema),
    defaultValues: {
      company_name: '',
      contact_name: '',
      email: '',
      phone: '',
      status: 'new',
      source: '',
      notes: '',
    },
  });

  const { data: lead, isLoading } = useQuery({
    queryKey: ['lead', id],
    queryFn: () => leadsApi.getLead(Number(id)),
    enabled: isEditMode,
  });

  const createMutation = useMutation({
    mutationFn: (data: CreateLeadData) => leadsApi.createLead(data),
    onSuccess: () => {
      showSuccess('Lead created successfully');
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      navigate('/leads');
    },
    onError: (error: any) => {
      // Handle validation errors
      if (error.response?.data?.details) {
        const validationErrors = error.response.data.details;
        // Map backend field names to frontend field names
        const fieldMapping: Record<string, string> = {
          'FirstName': 'contact_name',
          'LastName': 'contact_name',
          'Email': 'email',
          'Phone': 'phone',
          'Company': 'company_name',
        };
        
        Object.entries(validationErrors).forEach(([field, message]) => {
          const frontendField = fieldMapping[field] || field.toLowerCase();
          methods.setError(frontendField as any, {
            type: 'server',
            message: String(message),
          });
        });
        
        showError('Please fix the validation errors');
      } else {
        showError(error.response?.data?.message || 'Failed to create lead');
      }
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: UpdateLeadData }) =>
      leadsApi.updateLead(id, data),
    onSuccess: () => {
      showSuccess('Lead updated successfully');
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      queryClient.invalidateQueries({ queryKey: ['lead', id] });
      navigate('/leads');
    },
    onError: (error: any) => {
      // Handle validation errors
      if (error.response?.data?.details) {
        const validationErrors = error.response.data.details;
        // Map backend field names to frontend field names
        const fieldMapping: Record<string, string> = {
          'FirstName': 'contact_name',
          'LastName': 'contact_name',
          'Email': 'email',
          'Phone': 'phone',
          'Company': 'company_name',
        };
        
        Object.entries(validationErrors).forEach(([field, message]) => {
          const frontendField = fieldMapping[field] || field.toLowerCase();
          methods.setError(frontendField as any, {
            type: 'server',
            message: String(message),
          });
        });
        
        showError('Please fix the validation errors');
      } else {
        showError(error.response?.data?.message || 'Failed to update lead');
      }
    },
  });

  useEffect(() => {
    if (lead) {
      methods.reset({
        company_name: lead.company_name,
        contact_name: lead.contact_name,
        email: lead.email,
        phone: lead.phone,
        status: lead.status,
        source: lead.source,
        notes: lead.notes || '',
      });
    }
  }, [lead, methods]);

  const onSubmit = (data: LeadFormData) => {
    if (isEditMode) {
      updateMutation.mutate({ id: Number(id), data });
    } else {
      // For admin users, include the owner_id (assign to themselves by default)
      const createData: CreateLeadData = {
        ...data,
        ...(user?.role === 'admin' && { owner_id: user.id }),
      };
      createMutation.mutate(createData);
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">
          {isEditMode ? 'Edit Lead' : 'Create New Lead'}
        </Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)}>
            <Stack spacing={3}>
              <Typography variant="h6">Contact Information</Typography>
              
              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="company_name"
                  label="Company Name"
                  required
                />
                <FormTextField
                  name="contact_name"
                  label="Contact Name"
                  required
                />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="email"
                  label="Email"
                  type="email"
                  required
                />
                <FormTextField
                  name="phone"
                  label="Phone"
                  required
                />
              </Box>

              <Divider />

              <Typography variant="h6">Lead Details</Typography>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormSelect
                  name="status"
                  label="Status"
                  options={statusOptions}
                  required
                />
                <FormSelect
                  name="source"
                  label="Lead Source"
                  options={sourceOptions}
                  required
                />
              </Box>

              <FormTextField
                name="notes"
                label="Notes"
                multiline
                rows={4}
              />

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  startIcon={<CancelIcon />}
                  onClick={() => navigate('/leads')}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} Lead
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};