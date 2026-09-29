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
} from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { FormTextField, FormSwitch } from '@/components/form';
import { CompanyAutocomplete } from '@/components/CompanyAutocomplete';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { customersApi, type CreateCustomerData, type UpdateCustomerData } from '@/api/endpoints';
import type { Company } from '@/types';

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

interface ServerErrorPayload {
  message?: string;
  details?: Record<string, unknown>;
}

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const [searchParams] = useSearchParams();
  const isEditMode = !!id;

  // "New Customer" on a company page arrives as /customers/new?company_id=N.
  const prefilledCompanyId = (() => {
    const raw = searchParams.get('company_id');
    const parsed = raw ? Number(raw) : NaN;
    return Number.isInteger(parsed) && parsed > 0 ? parsed : null;
  })();

  // The curated link to a Company record, kept next to the free-text
  // `company_name`. `originalCompanyId` remembers the link at load time:
  // clearing it on edit must send `company_id: 0`, while an untouched, unset
  // link is omitted.
  const [linkedCompanyId, setLinkedCompanyId] = useState<number | null>(
    isEditMode ? null : prefilledCompanyId
  );
  const [linkedCompany, setLinkedCompany] = useState<Company | null>(null);
  const [originalCompanyId, setOriginalCompanyId] = useState<number | null>(null);
  const [companyError, setCompanyError] = useState<string | null>(null);

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

  // An unknown or deleted company_id comes back as 400 INVALID_REFERENCE with
  // the field named in `details`; that message belongs on the company picker.
  // Returns the server's top-level message, if any, for the snackbar.
  const reportCompanyReference = (error: unknown): string | undefined => {
    const payload = (error as { response?: { data?: ServerErrorPayload } })?.response?.data;
    const reference = payload?.details?.company_id ?? payload?.details?.CompanyID;
    if (reference) {
      setCompanyError(String(reference));
    }
    return payload?.message;
  };

  const createMutation = useMutation({
    mutationFn: (data: CreateCustomerData) => customersApi.createCustomer(data),
    onSuccess: () => {
      showSuccess('Customer created successfully');
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      navigate('/customers');
    },
    onError: (error: unknown) => {
      showError(reportCompanyReference(error) || 'Failed to create customer');
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
    onError: (error: unknown) => {
      showError(reportCompanyReference(error) || 'Failed to update customer');
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
      const currentCompanyId = customer.company_id ?? null;
      setLinkedCompanyId(currentCompanyId);
      setLinkedCompany(customer.company_record ?? null);
      setOriginalCompanyId(currentCompanyId);
    }
  }, [customer, methods]);

  const handleCompanyChange = (companyId: number | null, company: Company | null) => {
    setLinkedCompanyId(companyId);
    setLinkedCompany(company);
    setCompanyError(null);
    // Convenience only: seed the free-text company when it is still blank.
    if (company && methods.getValues('company_name').trim() === '') {
      methods.setValue('company_name', company.name, { shouldValidate: true });
    }
  };

  // Chosen → send it; cleared on edit after being linked → 0 clears it on
  // the API; never linked and untouched → omit so the API keeps whatever it has.
  const companyIdPatch = (): Pick<CreateCustomerData, 'company_id'> => {
    if (linkedCompanyId !== null) {
      return { company_id: linkedCompanyId };
    }
    if (isEditMode && originalCompanyId !== null) {
      return { company_id: 0 };
    }
    return {};
  };

  const onSubmit = (data: CustomerFormData) => {
    const submitData = {
      ...data,
      website: data.website || undefined,
      annual_revenue: data.annual_revenue || undefined,
      employee_count: data.employee_count || undefined,
      ...companyIdPatch(),
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
                  label="Company (as entered)"
                  required
                />
                <CompanyAutocomplete
                  value={linkedCompanyId}
                  initialCompany={linkedCompany}
                  onChange={handleCompanyChange}
                  error={companyError !== null}
                  helperText={companyError ?? 'Link this customer to a company record (optional)'}
                />
              </Box>

              <FormTextField
                name="industry"
                label="Industry"
              />

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