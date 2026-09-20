import React, { useState } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Chip,
  IconButton,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  Alert,
  Tooltip,
  InputAdornment,
} from '@mui/material';
import {
  Add as AddIcon,
  ContentCopy as ContentCopyIcon,
  DeleteOutline as DeleteOutlineIcon,
} from '@mui/icons-material';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { apiKeysApi, type GeneratedAPIKey } from '@/api/endpoints/apikeys';
import type { APIKey } from '@/types';
import { formatDate, DATE_FALLBACK } from '@/utils/date';

const createKeySchema = z.object({
  // The backend binding is `min=3,max=100`; matching it here keeps the failure
  // in the form instead of a round-trip 400.
  name: z.string().min(3, 'Name must be at least 3 characters').max(100, 'Name must be at most 100 characters'),
  expires_on: z.string(),
});

type CreateKeyFormData = z.infer<typeof createKeySchema>;

/**
 * The client's error interceptor replaces `response.data` with the envelope's
 * inner `error` object, so the backend message normally sits at `data.message`;
 * the nested form is the fallback for responses it did not touch.
 */
const serverMessage = (error: unknown, fallback: string): string => {
  const data = (error as {
    response?: { data?: { message?: string; error?: { message?: string } } };
  })?.response?.data;
  return data?.message || data?.error?.message || fallback;
};

/**
 * Turns the date-input value (`yyyy-MM-dd`) into the RFC3339 timestamp the API
 * expects, pinned to the end of that local day — a key chosen to expire "on the
 * 31st" should still work during the 31st, and the backend rejects any instant
 * that is not in the future.
 */
const toExpiryTimestamp = (value: string): string | undefined => {
  if (!value) {
    return undefined;
  }
  const parsed = new Date(`${value}T23:59:59`);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString();
};

const isExpired = (key: APIKey): boolean =>
  !!key.expires_at && new Date(key.expires_at).getTime() <= Date.now();

export const APIKeys: React.FC = () => {
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();

  const [createOpen, setCreateOpen] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [generated, setGenerated] = useState<GeneratedAPIKey | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<APIKey | null>(null);

  const { data, isLoading, isError, error: listError } = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => apiKeysApi.getAPIKeys(),
  });

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<CreateKeyFormData>({
    resolver: zodResolver(createKeySchema),
    defaultValues: { name: '', expires_on: '' },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: number) => apiKeysApi.revokeAPIKey(id),
    onSuccess: () => {
      showSuccess('API key revoked');
      queryClient.invalidateQueries({ queryKey: ['api-keys'] });
      setRevokeTarget(null);
    },
    onError: (error) => {
      showError(serverMessage(error, 'Failed to revoke API key'));
      setRevokeTarget(null);
    },
  });

  const openCreate = () => {
    setCreateError(null);
    reset({ name: '', expires_on: '' });
    setCreateOpen(true);
  };

  const onCreate = async (form: CreateKeyFormData) => {
    setCreateError(null);
    try {
      const key = await apiKeysApi.createAPIKey({
        name: form.name,
        expires_at: toExpiryTimestamp(form.expires_on),
      });
      setCreateOpen(false);
      // The plaintext key exists only in this response; hold it in state until
      // the operator dismisses the reveal dialog.
      setGenerated(key);
    } catch (error) {
      setCreateError(serverMessage(error, 'Failed to create API key'));
    }
  };

  const closeGenerated = () => {
    setGenerated(null);
    queryClient.invalidateQueries({ queryKey: ['api-keys'] });
  };

  const copyKey = async () => {
    if (!generated) {
      return;
    }
    try {
      await navigator.clipboard?.writeText(generated.key);
      showSuccess('API key copied to clipboard');
    } catch {
      showError('Could not copy the key. Select and copy it manually.');
    }
  };

  const keys = data?.data ?? [];

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 1 }}>
        <Typography variant="h4">API Keys</Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={openCreate}>
          Create API Key
        </Button>
      </Box>

      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        These keys belong to your account. Every role — admin included — sees and creates only its
        own keys; there is no account-wide view. Send a key in the <code>ApiKey</code> header to
        authenticate as yourself.
      </Typography>

      <Paper>
        {isLoading ? (
          <Loading />
        ) : isError ? (
          // A failed GET must not be dressed up as an empty account: "you have
          // no keys" would read as a statement of fact about the keys that do
          // exist, and could send someone off to mint a duplicate.
          <Alert severity="error" sx={{ m: 2 }}>
            {serverMessage(listError, 'Failed to load API keys. Please try again.')}
          </Alert>
        ) : (
          <TableContainer>
            <Table>
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>Prefix</TableCell>
                  <TableCell>Status</TableCell>
                  <TableCell>Created</TableCell>
                  <TableCell>Expires</TableCell>
                  <TableCell>Last used</TableCell>
                  <TableCell align="right">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {keys.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={7} align="center" sx={{ py: 4 }}>
                      <Typography variant="body2" color="text.secondary">
                        You have no API keys yet.
                      </Typography>
                    </TableCell>
                  </TableRow>
                ) : (
                  keys.map((key) => {
                    const expired = isExpired(key);
                    const inactive = !key.is_active || expired;
                    return (
                      <TableRow
                        key={key.id}
                        hover
                        sx={inactive ? { opacity: 0.55 } : undefined}
                      >
                        <TableCell>{key.name}</TableCell>
                        <TableCell>
                          <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                            {key.prefix ? `${key.prefix}…` : DATE_FALLBACK}
                          </Typography>
                        </TableCell>
                        <TableCell>
                          <Chip
                            size="small"
                            label={key.is_active ? (expired ? 'Expired' : 'Active') : 'Inactive'}
                            color={inactive ? 'default' : 'success'}
                            variant={inactive ? 'outlined' : 'filled'}
                          />
                        </TableCell>
                        <TableCell>{formatDate(key.created_at, 'PP')}</TableCell>
                        <TableCell>{formatDate(key.expires_at, 'PP')}</TableCell>
                        <TableCell>{formatDate(key.last_used_at, 'PPp')}</TableCell>
                        <TableCell align="right">
                          <Tooltip title={key.is_active ? 'Revoke' : 'Already revoked'}>
                            <span>
                              <IconButton
                                aria-label={`Revoke ${key.name}`}
                                onClick={() => setRevokeTarget(key)}
                                disabled={!key.is_active}
                                color="error"
                              >
                                <DeleteOutlineIcon />
                              </IconButton>
                            </span>
                          </Tooltip>
                        </TableCell>
                      </TableRow>
                    );
                  })
                )}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </Paper>

      <Dialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        fullWidth
        maxWidth="sm"
        aria-labelledby="create-api-key-title"
      >
        <Box component="form" onSubmit={handleSubmit(onCreate)} noValidate>
          <DialogTitle id="create-api-key-title">Create API Key</DialogTitle>
          <DialogContent>
            {createError && (
              <Alert severity="error" sx={{ mb: 2 }}>
                {createError}
              </Alert>
            )}
            <TextField
              {...register('name')}
              label="Name"
              fullWidth
              margin="normal"
              autoFocus
              error={!!errors.name}
              helperText={errors.name?.message || 'What this key is used for, e.g. "Zapier sync".'}
            />
            <TextField
              {...register('expires_on')}
              label="Expires on"
              type="date"
              fullWidth
              margin="normal"
              error={!!errors.expires_on}
              helperText={errors.expires_on?.message || 'Optional. Leave empty for a key that never expires.'}
              InputLabelProps={{ shrink: true }}
            />
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setCreateOpen(false)} color="inherit">
              Cancel
            </Button>
            <Button type="submit" variant="contained" disabled={isSubmitting}>
              {isSubmitting ? 'Creating...' : 'Create'}
            </Button>
          </DialogActions>
        </Box>
      </Dialog>

      <Dialog
        open={!!generated}
        onClose={closeGenerated}
        fullWidth
        maxWidth="sm"
        aria-labelledby="generated-api-key-title"
      >
        <DialogTitle id="generated-api-key-title">Your new API key</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            Copy this key now — it will never be shown again. Only a hash is stored, so a lost key
            can only be replaced, not recovered.
          </Alert>
          <TextField
            label="API key"
            value={generated?.key ?? ''}
            fullWidth
            autoComplete="off"
            inputProps={{ readOnly: true, 'data-testid': 'generated-api-key' }}
            InputProps={{
              sx: { fontFamily: 'monospace' },
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton aria-label="Copy API key" onClick={copyKey} edge="end">
                    <ContentCopyIcon />
                  </IconButton>
                </InputAdornment>
              ),
            }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={closeGenerated} variant="contained">
            Done
          </Button>
        </DialogActions>
      </Dialog>

      <ConfirmDialog
        open={!!revokeTarget}
        title="Revoke API key"
        message={`Revoke "${revokeTarget?.name ?? ''}"? Any integration still using it will stop authenticating immediately. This cannot be undone.`}
        confirmText="Revoke"
        severity="error"
        onConfirm={() => revokeTarget && revokeMutation.mutate(revokeTarget.id)}
        onCancel={() => setRevokeTarget(null)}
      />
    </Box>
  );
};

export function Component() {
  return <APIKeys />;
}

Component.displayName = 'APIKeys';
