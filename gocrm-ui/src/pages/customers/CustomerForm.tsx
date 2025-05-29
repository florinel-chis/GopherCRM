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
import { FormTextField, FormSwitch } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { customersApi, type CreateCustomerData, type UpdateCustomerData } from '@/api/endpoints';

const customerSchema = z.object({
  company_name: z.string().min(1, 'Company name is required'),
  contact_name: z.string().min(1, 'Contact name is required'),
  email: z.string().email('Invalid email address'),
  phone: z.string().min(1, 'Phone number is required'),
  address: z.string().optional(),
  city: z.string().optional(),
  state: z.string().optional(),
  country: z.string().optional(),
  postal_code: z.string().optional(),
  website: z.string().url('Invalid URL').or(z.literal('')).optional(),
  industry: z.string().optional(),
  annual_revenue: z.number().min(0).optional(),
  employee_count: z.number().min(0).optional(),
  notes: z.string().optional(),
  is_active: z.boolean(),
});

type CustomerFormData = z.infer<typeof customerSchema>;

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const isEditMode = !!id;

  const methods = useForm<CustomerFormData>({
    resolver: zodResolver(customerSchema),
    defaultValues: {
      company_name: '',
      contact_name: '',
      email: '',
      phone: '',
      address: '',
      city: '',
      state: '',
      country: '',
      postal_code: '',
      website: '',
      industry: '',
      annual_revenue: 0,
      employee_count: 0,
      notes: '',
      is_active: true,
    },
  });

  const { data: customer, isLoading } = useQuery({
    queryKey: ['customer', id],
    queryFn: () => customersApi.getCustomer(Number(id)),
    enabled: isEditMode,
  });

  const createMutation = useMutation({
    mutationFn: (data: CreateCustomerData) => customersApi.createCustomer(data),
    onSuccess: () => {
      showSuccess('Customer created successfully');
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      navigate('/customers');
    },
    onError: () => {
      showError('Failed to create customer');
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: UpdateCustomerData }) =>
      customersApi.updateCustomer(id, data),
    onSuccess: () => {
      showSuccess('Customer updated successfully');
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      queryClient.invalidateQueries({ queryKey: ['customer', id] });
      navigate('/customers');
    },
    onError: () => {
      showError('Failed to update customer');
    },
  });

  useEffect(() => {
    if (customer) {
      methods.reset({
        company_name: customer.company_name,
        contact_name: customer.contact_name,
        email: customer.email,
        phone: customer.phone,
        address: customer.address || '',
        city: customer.city || '',
        state: customer.state || '',
        country: customer.country || '',
        postal_code: customer.postal_code || '',
        website: customer.website || '',
        industry: customer.industry || '',
        annual_revenue: customer.annual_revenue || 0,
        employee_count: customer.employee_count || 0,
        notes: customer.notes || '',
        is_active: customer.is_active,
      });
    }
  }, [customer, methods]);

  const onSubmit = (data: CustomerFormData) => {
    const submitData = {
      ...data,
      website: data.website || undefined,
      annual_revenue: data.annual_revenue || undefined,
      employee_count: data.employee_count || undefined,
    };

    if (isEditMode) {
      updateMutation.mutate({ id: Number(id), data: submitData });
    } else {
      createMutation.mutate(submitData as CreateCustomerData);
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">
          {isEditMode ? 'Edit Customer' : 'Create New Customer'}
        </Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)}>
            <Stack spacing={3}>
              <Typography variant="h6">Company Information</Typography>
              
              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="company_name"
                  label="Company Name"
                  required
                />
                <FormTextField
                  name="industry"
                  label="Industry"
                />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="website"
                  label="Website"
                  placeholder="https://example.com"
                />
                <FormTextField
                  name="employee_count"
                  label="Number of Employees"
                  type="number"
                />
              </Box>

              <FormTextField
                name="annual_revenue"
                label="Annual Revenue ($)"
                type="number"
              />

              <Divider />

              <Typography variant="h6">Contact Information</Typography>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="contact_name"
                  label="Primary Contact Name"
                  required
                />
                <FormTextField
                  name="email"
                  label="Email"
                  type="email"
                  required
                />
              </Box>

              <FormTextField
                name="phone"
                label="Phone"
                required
              />

              <Divider />

              <Typography variant="h6">Address</Typography>

              <FormTextField
                name="address"
                label="Street Address"
              />

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="city"
                  label="City"
                />
                <FormTextField
                  name="state"
                  label="State/Province"
                />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="country"
                  label="Country"
                />
                <FormTextField
                  name="postal_code"
                  label="Postal Code"
                />
              </Box>

              <Divider />

              <Typography variant="h6">Additional Information</Typography>

              <FormTextField
                name="notes"
                label="Notes"
                multiline
                rows={4}
              />

              <FormSwitch
                name="is_active"
                label="Active Customer"
              />

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  startIcon={<CancelIcon />}
                  onClick={() => navigate('/customers')}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} Customer
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};