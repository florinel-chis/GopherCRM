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
  Alert,
} from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { FormTextField, FormSelect, FormSwitch } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { usersApi, type CreateUserData, type UpdateUserData } from '@/api/endpoints';
import { useAuth } from '@/contexts/AuthContext';

const createUserSchema = z.object({
  username: z.string().min(3, 'Username must be at least 3 characters').max(50),
  email: z.string().email('Invalid email address'),
  password: z.string().min(8, 'Password must be at least 8 characters'),
  confirmPassword: z.string(),
  first_name: z.string().min(1, 'First name is required'),
  last_name: z.string().min(1, 'Last name is required'),
  role: z.enum(['admin', 'sales', 'support', 'customer']),
}).refine((data) => data.password === data.confirmPassword, {
  message: "Passwords don't match",
  path: ['confirmPassword'],
});

const updateUserSchema = z.object({
  email: z.string().email('Invalid email address'),
  first_name: z.string().min(1, 'First name is required'),
  last_name: z.string().min(1, 'Last name is required'),
  role: z.enum(['admin', 'sales', 'support', 'customer']),
  is_active: z.boolean(),
});

type CreateUserFormData = z.infer<typeof createUserSchema>;
type UpdateUserFormData = z.infer<typeof updateUserSchema>;

const roleOptions = [
  { value: 'admin', label: 'Admin' },
  { value: 'sales', label: 'Sales' },
  { value: 'support', label: 'Support' },
  { value: 'customer', label: 'Customer' },
];

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { user: currentUser } = useAuth();
  const isEditMode = !!id;

  const createMethods = useForm<CreateUserFormData>({
    resolver: zodResolver(createUserSchema),
    defaultValues: {
      username: '',
      email: '',
      password: '',
      confirmPassword: '',
      first_name: '',
      last_name: '',
      role: 'sales',
    },
  });

  const updateMethods = useForm<UpdateUserFormData>({
    resolver: zodResolver(updateUserSchema),
    defaultValues: {
      email: '',
      first_name: '',
      last_name: '',
      role: 'sales',
      is_active: true,
    },
  });

  const methods = isEditMode ? updateMethods : createMethods as any;

  const { data: user, isLoading } = useQuery({
    queryKey: ['user', id],
    queryFn: () => usersApi.getUser(Number(id)),
    enabled: isEditMode,
  });

  const createMutation = useMutation({
    mutationFn: (data: CreateUserData) => usersApi.createUser(data),
    onSuccess: () => {
      showSuccess('User created successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
      navigate('/users');
    },
    onError: (error: any) => {
      showError(error.response?.data?.message || 'Failed to create user');
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: UpdateUserData }) =>
      usersApi.updateUser(id, data),
    onSuccess: () => {
      showSuccess('User updated successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
      queryClient.invalidateQueries({ queryKey: ['user', id] });
      navigate('/users');
    },
    onError: (error: any) => {
      showError(error.response?.data?.message || 'Failed to update user');
    },
  });

  useEffect(() => {
    if (user && isEditMode) {
      updateMethods.reset({
        email: user.email,
        first_name: user.first_name,
        last_name: user.last_name,
        role: user.role,
        is_active: user.is_active,
      });
    }
  }, [user, updateMethods, isEditMode]);

  const onSubmit = (data: any) => {
    if (isEditMode) {
      const updateData: UpdateUserData = {
        email: data.email,
        first_name: data.first_name,
        last_name: data.last_name,
        role: data.role,
        is_active: data.is_active,
      };
      updateMutation.mutate({ id: Number(id), data: updateData });
    } else {
      const createData: CreateUserData = {
        username: data.username,
        email: data.email,
        password: data.password,
        first_name: data.first_name,
        last_name: data.last_name,
        role: data.role,
      };
      createMutation.mutate(createData);
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  const isAdmin = currentUser?.role === 'admin';
  const canEditRole = isAdmin;
  const canEditActiveStatus = isAdmin && user?.id !== currentUser?.id;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">
          {isEditMode ? 'Edit User' : 'Create New User'}
        </Typography>
      </Box>

      {!isAdmin && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          Only administrators can create or edit users.
        </Alert>
      )}

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)}>
            <Stack spacing={3}>
              <Typography variant="h6">Account Information</Typography>
              
              {!isEditMode && (
                <FormTextField
                  name="username"
                  label="Username"
                  required
                  disabled={!isAdmin}
                />
              )}

              <FormTextField
                name="email"
                label="Email"
                type="email"
                required
                disabled={!isAdmin}
              />

              {!isEditMode && (
                <>
                  <FormTextField
                    name="password"
                    label="Password"
                    type="password"
                    required
                    disabled={!isAdmin}
                  />
                  <FormTextField
                    name="confirmPassword"
                    label="Confirm Password"
                    type="password"
                    required
                    disabled={!isAdmin}
                  />
                </>
              )}

              <Divider />

              <Typography variant="h6">Personal Information</Typography>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <FormTextField
                  name="first_name"
                  label="First Name"
                  required
                  disabled={!isAdmin}
                />
                <FormTextField
                  name="last_name"
                  label="Last Name"
                  required
                  disabled={!isAdmin}
                />
              </Box>

              <Divider />

              <Typography variant="h6">Role & Permissions</Typography>

              <FormSelect
                name="role"
                label="Role"
                options={roleOptions}
                required
                disabled={!canEditRole}
              />

              {isEditMode && (
                <FormSwitch
                  name="is_active"
                  label="Active Account"
                  disabled={!canEditActiveStatus}
                />
              )}

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  startIcon={<CancelIcon />}
                  onClick={() => navigate('/users')}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={!isAdmin || createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} User
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};