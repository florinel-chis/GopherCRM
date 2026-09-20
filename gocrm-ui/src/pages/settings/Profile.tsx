import React, { useState } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import {
  Box,
  Paper,
  Typography,
  Chip,
  Divider,
  Grid,
  TextField,
  Button,
  Alert,
  InputAdornment,
  IconButton,
} from '@mui/material';
import { Visibility, VisibilityOff } from '@mui/icons-material';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { Loading } from '@/components/Loading';
import { authApi } from '@/api/endpoints';
import { formatDate } from '@/utils/date';
import type { User } from '@/types';

// Mirrors the policy enforced by internal/utils/password.go and the sign-up
// form in pages/auth/Register.tsx. Keep the three in sync.
const changePasswordSchema = z.object({
  current_password: z.string().min(1, 'Current password is required'),
  new_password: z.string()
    .min(10, 'Password must be at least 10 characters')
    .regex(/[A-Z]/, 'Password must contain at least one uppercase letter')
    .regex(/[a-z]/, 'Password must contain at least one lowercase letter')
    .regex(/[0-9]/, 'Password must contain at least one number')
    .regex(/[^A-Za-z0-9]/, 'Password must contain at least one special character'),
  confirm_password: z.string(),
}).refine((data) => data.new_password === data.confirm_password, {
  message: "Passwords don't match",
  path: ['confirm_password'],
});

type ChangePasswordFormData = z.infer<typeof changePasswordSchema>;

const ROLE_COLORS: Record<User['role'], 'error' | 'primary' | 'info' | 'default'> = {
  admin: 'error',
  sales: 'primary',
  support: 'info',
  customer: 'default',
};

/**
 * Pulls the backend's own message out of an axios error. The API answers with
 * `{success:false, error:{code,message}}` and the client's error interceptor
 * replaces `response.data` with that inner `error` object — so the message
 * normally sits at `data.message`. The nested form is kept as a fallback for
 * responses the interceptor did not touch.
 */
const serverMessage = (error: unknown, fallback: string): string => {
  const data = (error as {
    response?: { data?: { message?: string; error?: { message?: string } } };
  })?.response?.data;
  return data?.message || data?.error?.message || fallback;
};

export const Profile: React.FC = () => {
  const { user, isLoading } = useAuth();
  const { showSuccess } = useSnackbar();
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [showCurrent, setShowCurrent] = useState(false);
  const [showNew, setShowNew] = useState(false);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<ChangePasswordFormData>({
    resolver: zodResolver(changePasswordSchema),
    defaultValues: {
      current_password: '',
      new_password: '',
      confirm_password: '',
    },
  });

  const onSubmit = async (data: ChangePasswordFormData) => {
    setError(null);
    setSuccess(null);
    try {
      await authApi.changePassword(data.current_password, data.new_password);
      // The backend revokes every refresh token but cannot revoke the JWT that
      // is already in this tab, so the session stays alive on purpose.
      const message =
        'Password changed. Your other sessions have been signed out; this one stays active.';
      setSuccess(message);
      showSuccess('Password changed successfully');
      reset();
    } catch (err) {
      setError(serverMessage(err, 'Password change failed. Please try again.'));
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  if (!user) {
    return (
      <Box sx={{ p: 3 }}>
        <Alert severity="error">Your profile could not be loaded.</Alert>
      </Box>
    );
  }

  return (
    <Box sx={{ p: 3 }}>
      <Typography variant="h4" gutterBottom>
        Profile
      </Typography>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Account details
        </Typography>
        <Divider sx={{ mb: 2 }} />
        <Grid container spacing={2}>
          <Grid size={{ xs: 12, sm: 6 }}>
            <Typography variant="body2" color="text.secondary">
              Name
            </Typography>
            <Typography variant="body1">
              {`${user.first_name} ${user.last_name}`.trim() || '—'}
            </Typography>
          </Grid>
          <Grid size={{ xs: 12, sm: 6 }}>
            <Typography variant="body2" color="text.secondary">
              Email
            </Typography>
            <Typography variant="body1">{user.email}</Typography>
          </Grid>
          <Grid size={{ xs: 12, sm: 6 }}>
            <Typography variant="body2" color="text.secondary">
              Role
            </Typography>
            <Chip
              label={user.role}
              color={ROLE_COLORS[user.role] ?? 'default'}
              size="small"
              sx={{ mt: 0.5, textTransform: 'capitalize' }}
            />
          </Grid>
          <Grid size={{ xs: 12, sm: 6 }}>
            <Typography variant="body2" color="text.secondary">
              Member since
            </Typography>
            <Typography variant="body1">{formatDate(user.created_at, 'PP')}</Typography>
          </Grid>
        </Grid>
      </Paper>

      <Paper sx={{ p: 3, maxWidth: 560 }}>
        <Typography variant="h6" gutterBottom>
          Change password
        </Typography>
        <Divider sx={{ mb: 2 }} />

        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}
        {success && (
          <Alert severity="success" sx={{ mb: 2 }}>
            {success}
          </Alert>
        )}

        <Box component="form" onSubmit={handleSubmit(onSubmit)} noValidate>
          <TextField
            {...register('current_password')}
            label="Current password"
            type={showCurrent ? 'text' : 'password'}
            fullWidth
            margin="normal"
            autoComplete="current-password"
            error={!!errors.current_password}
            helperText={errors.current_password?.message}
            InputProps={{
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton
                    aria-label="toggle current password visibility"
                    onClick={() => setShowCurrent((visible) => !visible)}
                    edge="end"
                  >
                    {showCurrent ? <VisibilityOff /> : <Visibility />}
                  </IconButton>
                </InputAdornment>
              ),
            }}
          />
          <TextField
            {...register('new_password')}
            label="New password"
            type={showNew ? 'text' : 'password'}
            fullWidth
            margin="normal"
            autoComplete="new-password"
            error={!!errors.new_password}
            helperText={
              errors.new_password?.message ||
              'At least 10 characters with upper and lower case, a number and a special character.'
            }
            InputProps={{
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton
                    aria-label="toggle new password visibility"
                    onClick={() => setShowNew((visible) => !visible)}
                    edge="end"
                  >
                    {showNew ? <VisibilityOff /> : <Visibility />}
                  </IconButton>
                </InputAdornment>
              ),
            }}
          />
          <TextField
            {...register('confirm_password')}
            label="Confirm new password"
            type="password"
            fullWidth
            margin="normal"
            autoComplete="new-password"
            error={!!errors.confirm_password}
            helperText={errors.confirm_password?.message}
          />

          <Button
            type="submit"
            variant="contained"
            disabled={isSubmitting}
            sx={{ mt: 2 }}
          >
            {isSubmitting ? 'Changing password...' : 'Change password'}
          </Button>
        </Box>
      </Paper>
    </Box>
  );
};

export function Component() {
  return <Profile />;
}

Component.displayName = 'Profile';
