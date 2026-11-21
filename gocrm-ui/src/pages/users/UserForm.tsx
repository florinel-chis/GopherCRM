import React, { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useForm, FormProvider } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { Box, Paper, Typography, Button, Stack, Divider, Alert } from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { FormTextField, FormSelect, FormSwitch, type SelectOption } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import {
  rolesApi,
  usersApi,
  type CreateUserData,
  type Role,
  type UpdateUserData,
} from '@/api/endpoints';
import { useAuth } from '@/hooks/useAuth';
import { usePermissions } from '@/hooks/usePermissions';
import { getAPIError } from '@/utils/apiError';
import type { User } from '@/types';

const createUserSchema = z
  .object({
    email: z.string().email('Invalid email address'),
    password: z.string().min(8, 'Password must be at least 8 characters'),
    confirmPassword: z.string(),
    first_name: z.string().min(1, 'First name is required'),
    last_name: z.string().min(1, 'Last name is required'),
    role: z.string().min(1, 'Role is required'),
  })
  .refine((data) => data.password === data.confirmPassword, {
    message: "Passwords don't match",
    path: ['confirmPassword'],
  });

const updateUserSchema = z.object({
  email: z.string().email('Invalid email address'),
  first_name: z.string().min(1, 'First name is required'),
  last_name: z.string().min(1, 'Last name is required'),
  role: z.string().min(1, 'Role is required'),
  is_active: z.boolean(),
  // Asked for, and sent, only when the signed-in user changes its own email.
  current_password: z.string().optional(),
});

type CreateUserFormData = z.infer<typeof createUserSchema>;
type UpdateUserFormData = z.infer<typeof updateUserSchema>;

// The role picker holds one string per role: the system key (admin, sales,
// support, customer) for a system role, custom:<id> for a custom one.
const CUSTOM_PREFIX = 'custom:';
const DEFAULT_NEW_ROLE = 'sales';

type RoleFields = Pick<CreateUserData, 'role' | 'role_id'>;

const pickerValueOf = (item: Role): string =>
  item.is_system && item.system_key ? item.system_key : `${CUSTOM_PREFIX}${item.id}`;

/** What the API takes for a picker value: role_id travels with role custom and with nothing else. */
const roleFieldsOf = (value: string): RoleFields =>
  value.startsWith(CUSTOM_PREFIX)
    ? { role: 'custom', role_id: Number(value.slice(CUSTOM_PREFIX.length)) }
    : { role: value as Exclude<User['role'], 'custom'> };

/**
 * The stored role of the account under edit (never of the signed-in user):
 * its picker value, and whether it is a customer login, which the API never
 * lets onto a custom role (409 CUSTOMER_LOGIN_ROLE).
 */
const storedRoleOf = (account: User): { value: string; isCustomerLogin: boolean } => {
  const { role: stored, role_id: storedId } = account;
  if (stored === 'custom') {
    // A custom account without a role id has no value: the form then refuses
    // to submit instead of sending role custom alone.
    return { value: storedId ? `${CUSTOM_PREFIX}${storedId}` : '', isCustomerLogin: false };
  }
  return { value: stored, isCustomerLogin: stored === 'customer' };
};

const fallbackLabel = (value: string): string =>
  value.startsWith(CUSTOM_PREFIX)
    ? `Role #${value.slice(CUSTOM_PREFIX.length)}`
    : value.charAt(0).toUpperCase() + value.slice(1);

/**
 * The options of the picker. A customer login is offered the system roles
 * only. The stored role is always among them, also when it lies past the
 * page GET /roles returned, so an unrelated edit sends it back unchanged.
 */
const roleOptionsFor = (roles: Role[], account: User | undefined): SelectOption[] => {
  const stored = account ? storedRoleOf(account) : undefined;
  const options = roles
    .filter((item) => item.is_system || !stored?.isCustomerLogin)
    .map((item) => ({ value: pickerValueOf(item), label: item.name }));
  if (stored?.value && !options.some((option) => option.value === stored.value)) {
    options.push({ value: stored.value, label: fallbackLabel(stored.value) });
  }
  return options;
};

// The refusals of the role rules, each with its own sentence in the form.
const ROLE_RULE_MESSAGES: Record<string, string> = {
  LAST_ADMIN: 'The last active administrator cannot be given another role or be deactivated.',
  CUSTOMER_LOGIN_ROLE: 'A customer login cannot be given a custom role.',
  ROLE_ESCALATION: 'You cannot edit an account or give a role whose permissions are above your own.',
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { user: currentUser, refreshUser } = useAuth();
  const { can } = usePermissions();
  const isEditMode = !!id;
  // The role has its own key on the API; without it the picker is not shown
  // and the roles are not read.
  const canEditRole = can('users.assign_role');
  const [roleRuleError, setRoleRuleError] = useState('');
  // The stored account is copied into the form once, when both it and the
  // roles are there; until then nothing can be submitted.
  const [formReady, setFormReady] = useState(false);

  const createMethods = useForm<CreateUserFormData>({
    resolver: zodResolver(createUserSchema),
    defaultValues: {
      email: '',
      password: '',
      confirmPassword: '',
      first_name: '',
      last_name: '',
      role: canEditRole ? DEFAULT_NEW_ROLE : 'customer',
    },
  });

  const updateMethods = useForm<UpdateUserFormData>({
    resolver: zodResolver(updateUserSchema),
    defaultValues: {
      email: '',
      first_name: '',
      last_name: '',
      role: '',
      is_active: true,
      current_password: '',
    },
  });

  const {
    data: user,
    isLoading,
    isError: userFailed,
  } = useQuery({
    queryKey: ['user', id],
    queryFn: () => usersApi.getUser(Number(id)),
    enabled: isEditMode,
  });

  const rolesQuery = useQuery({
    queryKey: ['roles'],
    queryFn: rolesApi.getRoles,
    enabled: canEditRole,
  });
  const roles = rolesQuery.data;
  const rolesLoaded = !canEditRole || roles !== undefined;

  const roleOptions = useMemo(() => roleOptionsFor(roles ?? [], user), [roles, user]);

  const onSaveError = (error: unknown, fallback: string) => {
    const apiError = getAPIError(error);
    const ruleMessage = apiError?.code ? ROLE_RULE_MESSAGES[apiError.code] : undefined;
    if (ruleMessage) {
      setRoleRuleError(ruleMessage);
      return;
    }
    showError(apiError?.message || fallback);
  };

  const createMutation = useMutation({
    mutationFn: (data: CreateUserData) => usersApi.createUser(data),
    onSuccess: () => {
      showSuccess('User created successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
      navigate('/users');
    },
    onError: (error: unknown) => onSaveError(error, 'Failed to create user'),
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: UpdateUserData }) =>
      usersApi.updateUser(id, data),
    onSuccess: () => {
      showSuccess('User updated successfully');
      queryClient.invalidateQueries({ queryKey: ['users'] });
      queryClient.invalidateQueries({ queryKey: ['user', id] });
      // Saving your own account can change your role: reload the signed-in user.
      if (Number(id) === currentUser?.id) void refreshUser();
      navigate('/users');
    },
    onError: (error: unknown) => onSaveError(error, 'Failed to update user'),
  });

  useEffect(() => {
    if (!isEditMode || formReady || !user || !rolesLoaded) return;
    updateMethods.reset({
      email: user.email,
      first_name: user.first_name,
      last_name: user.last_name,
      role: storedRoleOf(user).value,
      is_active: user.is_active,
      current_password: '',
    });
    setFormReady(true);
  }, [user, rolesLoaded, formReady, updateMethods, isEditMode]);

  // The API changes the signed-in user's own email only with its current
  // password; an edit of another account, or one that leaves the email as it
  // was loaded, does not ask for it.
  const editedEmail = updateMethods.watch('email');
  const needsCurrentPassword =
    isEditMode && formReady && !!user && user.id === currentUser?.id && editedEmail !== user.email;

  // A password typed for an email change does not outlive it: when the email
  // is back at the loaded value the field goes and so does what it held.
  useEffect(() => {
    if (!needsCurrentPassword) {
      updateMethods.setValue('current_password', '');
      updateMethods.clearErrors('current_password');
    }
  }, [needsCurrentPassword, updateMethods]);

  const onCreateSubmit = (data: CreateUserFormData) => {
    setRoleRuleError('');
    const createData: CreateUserData = {
      email: data.email,
      password: data.password,
      first_name: data.first_name,
      last_name: data.last_name,
      // Without users.assign_role the API lets the caller create a customer only.
      ...(canEditRole ? roleFieldsOf(data.role) : { role: 'customer' as const }),
    };
    createMutation.mutate(createData);
  };

  const onUpdateSubmit = (data: UpdateUserFormData) => {
    setRoleRuleError('');
    if (needsCurrentPassword && !data.current_password) {
      updateMethods.setError('current_password', {
        type: 'required',
        message: 'Enter your current password to change your email',
      });
      return;
    }
    const updateData: UpdateUserData = {
      email: data.email,
      first_name: data.first_name,
      last_name: data.last_name,
      // An untouched picker sends the stored role back as it is.
      ...(canEditRole ? roleFieldsOf(data.role) : {}),
      is_active: data.is_active,
      ...(needsCurrentPassword ? { current_password: data.current_password } : {}),
    };
    updateMutation.mutate({ id: Number(id), data: updateData });
  };

  const rolesFailed = canEditRole && rolesQuery.isError;
  if (rolesFailed || (isEditMode && userFailed)) {
    return (
      <Box>
        <Alert severity="error" sx={{ mb: 2 }}>
          {rolesFailed
            ? 'The roles could not be loaded, so the form cannot be shown.'
            : 'The user could not be loaded.'}
        </Alert>
        <Button variant="outlined" onClick={() => navigate('/users')}>
          Back to users
        </Button>
      </Box>
    );
  }

  // New: the picker needs its options before it can show the default. Edit:
  // the form holds the stored account, role included, before it is shown.
  if (isLoading || !rolesLoaded || (isEditMode && !formReady)) {
    return <Loading />;
  }

  // The fields save through PUT /users/:id (edit) or POST /users (new); the
  // role and the active switch have their own keys on the API.
  const canWrite = can(isEditMode ? 'users.update' : 'users.create');
  const canEditActiveStatus = can('users.activate') && user?.id !== currentUser?.id;

  const fields = (
    <Stack spacing={3}>
      <Typography variant="h6">Account Information</Typography>

      <FormTextField name="email" label="Email" type="email" required disabled={!canWrite} />

      {needsCurrentPassword && (
        <FormTextField
          name="current_password"
          label="Current password"
          type="password"
          autoComplete="current-password"
          required
          disabled={!canWrite}
          helperText="Changing your own email needs your current password"
        />
      )}

      {!isEditMode && (
        <>
          <FormTextField
            name="password"
            label="Password"
            type="password"
            required
            disabled={!canWrite}
          />
          <FormTextField
            name="confirmPassword"
            label="Confirm Password"
            type="password"
            required
            disabled={!canWrite}
          />
        </>
      )}

      <Divider />

      <Typography variant="h6">Personal Information</Typography>

      <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
        <FormTextField name="first_name" label="First Name" required disabled={!canWrite} />
        <FormTextField name="last_name" label="Last Name" required disabled={!canWrite} />
      </Box>

      <Divider />

      <Typography variant="h6">Role & Permissions</Typography>

      {canEditRole && <FormSelect name="role" label="Role" options={roleOptions} required />}
      {!canEditRole && !isEditMode && (
        <Typography variant="body2" color="text.secondary">
          New account is created as a customer
        </Typography>
      )}
      {roleRuleError && <Alert severity="error">{roleRuleError}</Alert>}

      {isEditMode && (
        <FormSwitch name="is_active" label="Active Account" disabled={!canEditActiveStatus} />
      )}

      <Box display="flex" gap={2} justifyContent="flex-end">
        <Button variant="outlined" startIcon={<CancelIcon />} onClick={() => navigate('/users')}>
          Cancel
        </Button>
        <Button
          type="submit"
          variant="contained"
          startIcon={<SaveIcon />}
          disabled={!canWrite || createMutation.isPending || updateMutation.isPending}
        >
          {isEditMode ? 'Update' : 'Create'} User
        </Button>
      </Box>
    </Stack>
  );

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">{isEditMode ? 'Edit User' : 'Create New User'}</Typography>
      </Box>

      {!canWrite && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          Only administrators can create or edit users.
        </Alert>
      )}

      <Paper sx={{ p: 3 }}>
        {isEditMode ? (
          <FormProvider {...updateMethods}>
            <form onSubmit={updateMethods.handleSubmit(onUpdateSubmit)}>{fields}</form>
          </FormProvider>
        ) : (
          <FormProvider {...createMethods}>
            <form onSubmit={createMethods.handleSubmit(onCreateSubmit)}>{fields}</form>
          </FormProvider>
        )}
      </Paper>
    </Box>
  );
};
