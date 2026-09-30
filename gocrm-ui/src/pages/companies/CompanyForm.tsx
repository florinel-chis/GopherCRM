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
import { AxiosError } from 'axios';
import { FormTextField, FormSelect } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { companiesApi, usersApi, type CreateCompanyData } from '@/api/endpoints';
import { COMPANY_EMPLOYEE_RANGES, type User } from '@/types';

// Bounds mirror the API's column sizes (name 200, domain/website/address 255,
// industry/city/state/country 100, phone 50, postal_code 20).
const companySchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, 'Name is required')
    .max(200, 'Name must be 200 characters or fewer'),
  domain: z.string().trim().max(255, 'Domain must be 255 characters or fewer'),
  website: z
    .string()
    .trim()
    .max(255, 'Website must be 255 characters or fewer')
    .refine((value) => value === '' || /^https?:\/\/\S+$/i.test(value), {
      message: 'Website must be an http(s) URL',
    }),
  industry: z.string().trim().max(100, 'Industry must be 100 characters or fewer'),
  employee_range: z.enum(['', ...COMPANY_EMPLOYEE_RANGES]),
  phone: z.string().trim().max(50, 'Phone must be 50 characters or fewer'),
  address: z.string().trim().max(255, 'Address must be 255 characters or fewer'),
  city: z.string().trim().max(100, 'City must be 100 characters or fewer'),
  state: z.string().trim().max(100, 'State must be 100 characters or fewer'),
  country: z.string().trim().max(100, 'Country must be 100 characters or fewer'),
  postal_code: z.string().trim().max(20, 'Postal code must be 20 characters or fewer'),
  notes: z.string(),
});

type CompanyFormData = z.infer<typeof companySchema>;

// What the axios client leaves in `error.response.data`: the `error` object of
// the API envelope ({code, message, details}), not the envelope itself.
interface ApiErrorPayload {
  code?: string;
  message?: string;
}

const employeeRangeOptions = [
  { value: '', label: 'Not specified' },
  ...COMPANY_EMPLOYEE_RANGES.map((range) => ({ value: range, label: range })),
];

const emptyValues: CompanyFormData = {
  name: '',
  domain: '',
  website: '',
  industry: '',
  employee_range: '',
  phone: '',
  address: '',
  city: '',
  state: '',
  country: '',
  postal_code: '',
  notes: '',
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();
  const isEditMode = !!id;

  // Only an admin may hand a company to another account manager; the API
  // gives sales its own id regardless of what is sent.
  const isAdmin = user?.role === 'admin';
  const [owner, setOwner] = useState<User | null>(null);
  // Set once the admin changes the picker. An untouched owner is not sent, so
  // an update keeps whatever the company has; a cleared one is sent as 0,
  // which is how the API removes an owner.
  const [ownerTouched, setOwnerTouched] = useState(false);

  const methods = useForm<CompanyFormData>({
    resolver: zodResolver(companySchema),
    defaultValues: emptyValues,
  });

  const { data: company, isLoading } = useQuery({
    queryKey: ['company', id],
    queryFn: () => companiesApi.getCompany(Number(id)),
    enabled: isEditMode,
  });

  const { data: usersData } = useQuery({
    queryKey: ['users', { is_active: true }],
    queryFn: () => usersApi.getUsers({ is_active: true }),
    enabled: isAdmin,
  });

  useEffect(() => {
    if (company) {
      methods.reset({
        name: company.name,
        domain: company.domain || '',
        website: company.website || '',
        industry: company.industry || '',
        employee_range: company.employee_range || '',
        phone: company.phone || '',
        address: company.address || '',
        city: company.city || '',
        state: company.state || '',
        country: company.country || '',
        postal_code: company.postal_code || '',
        notes: company.notes || '',
      });
      setOwner(company.owner ?? null);
    }
  }, [company, methods]);

  // A 409 names the domain conflict; the server's message goes on the field
  // that caused it. Anything else is reported through the snackbar.
  const reportError = (error: unknown, fallback: string) => {
    if (error instanceof AxiosError) {
      const payload = error.response?.data as ApiErrorPayload | undefined;
      const serverMessage =
        typeof payload?.message === 'string' && payload.message !== '' ? payload.message : undefined;
      if (error.response?.status === 409) {
        const message = serverMessage || 'A company with this domain already exists';
        methods.setError('domain', { type: 'server', message });
        showError(message);
        return;
      }
      if (serverMessage) {
        showError(serverMessage);
        return;
      }
    }
    showError(fallback);
  };

  // Sales never sends owner_id: the API assigns the company to the caller.
  // An admin sends the picked owner, 0 to clear one on update, and nothing
  // when the picker was left alone (create: unowned; update: unchanged).
  const ownerPatch = (): Pick<CreateCompanyData, 'owner_id'> => {
    if (!isAdmin || !ownerTouched) {
      return {};
    }
    if (owner) {
      return { owner_id: owner.id };
    }
    return isEditMode ? { owner_id: 0 } : {};
  };

  const createMutation = useMutation({
    mutationFn: (data: CreateCompanyData) => companiesApi.createCompany(data),
    onSuccess: (created) => {
      showSuccess('Company created successfully');
      queryClient.invalidateQueries({ queryKey: ['companies'] });
      navigate(`/companies/${created.id}`);
    },
    onError: (error) => reportError(error, 'Failed to create company'),
  });

  const updateMutation = useMutation({
    mutationFn: (data: CreateCompanyData) => companiesApi.updateCompany(Number(id), data),
    onSuccess: () => {
      showSuccess('Company updated successfully');
      queryClient.invalidateQueries({ queryKey: ['companies'] });
      queryClient.invalidateQueries({ queryKey: ['company', id] });
      navigate(`/companies/${id}`);
    },
    onError: (error) => reportError(error, 'Failed to update company'),
  });

  const onSubmit = (data: CompanyFormData) => {
    const payload: CreateCompanyData = {
      ...data,
      ...ownerPatch(),
    };
    if (isEditMode) {
      updateMutation.mutate(payload);
    } else {
      createMutation.mutate(payload);
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  const users = usersData?.data || [];

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">{isEditMode ? 'Edit Company' : 'Create New Company'}</Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)} noValidate>
            <Stack spacing={3}>
              <Typography variant="h6">Company</Typography>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField name="name" label="Name" required />
                <FormTextField
                  name="domain"
                  label="Domain"
                  placeholder="example.com"
                  helperText="Letters, digits, dots and hyphens; unique among companies; the scheme and www. are stripped"
                />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField name="website" label="Website" placeholder="https://example.com" />
                <FormTextField name="industry" label="Industry" />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormSelect
                  name="employee_range"
                  label="Employees"
                  options={employeeRangeOptions}
                  displayEmpty
                />
                <FormTextField name="phone" label="Phone" />
              </Box>

              {isAdmin && (
                <Autocomplete
                  value={owner}
                  onChange={(_, newValue) => {
                    setOwner(newValue);
                    setOwnerTouched(true);
                  }}
                  options={users}
                  getOptionLabel={(option) => `${option.first_name} ${option.last_name}`}
                  isOptionEqualToValue={(option, current) => option.id === current.id}
                  renderInput={(params) => (
                    <MuiTextField
                      {...params}
                      label="Owner"
                      helperText={
                        isEditMode
                          ? 'Account manager; clear to remove the owner'
                          : 'Account manager; leave empty to keep the company unowned'
                      }
                    />
                  )}
                />
              )}

              <Divider />

              <Typography variant="h6">Address</Typography>

              <FormTextField name="address" label="Street Address" />

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField name="city" label="City" />
                <FormTextField name="state" label="State/Province" />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField name="country" label="Country" />
                <FormTextField name="postal_code" label="Postal Code" />
              </Box>

              <Divider />

              <Typography variant="h6">Additional Information</Typography>

              <FormTextField name="notes" label="Notes" multiline rows={4} />

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  startIcon={<CancelIcon />}
                  onClick={() => navigate(isEditMode ? `/companies/${id}` : '/companies')}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} Company
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};
