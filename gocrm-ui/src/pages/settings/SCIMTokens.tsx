import React, { useState } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import { Add as AddIcon, DeleteOutline as DeleteOutlineIcon } from '@mui/icons-material';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { scimTokensApi, type SCIMToken } from '@/api/endpoints/scimTokens';
import { formatDate } from '@/utils/date';
import { apiErrorMessage } from '@/utils/apiError';
import { SCIMTokenSecretDialog } from './SCIMTokenSecretDialog';

const SCIM_TOKENS_KEY = ['scim-tokens'] as const;

const createTokenSchema = z.object({
  // The API trims the name and accepts 1 to 100 characters.
  name: z
    .string()
    .trim()
    .min(1, 'Name is required')
    .max(100, 'Name must be at most 100 characters'),
});

type CreateTokenFormData = z.infer<typeof createTokenSchema>;

export const SCIMTokens: React.FC = () => {
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();

  const [createOpen, setCreateOpen] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  // The plaintext token exists only in the create response. It is held here,
  // in component state, until the reveal dialog is dismissed; it never goes
  // through a mutation (whose result the mutation cache would keep) or the
  // query cache.
  const [createdToken, setCreatedToken] = useState<string | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<SCIMToken | null>(null);

  const { data, isLoading, isError, error: listError } = useQuery({
    queryKey: SCIM_TOKENS_KEY,
    queryFn: () => scimTokensApi.list(),
  });

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<CreateTokenFormData>({
    resolver: zodResolver(createTokenSchema),
    defaultValues: { name: '' },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: number) => scimTokensApi.revoke(id),
    onSuccess: () => {
      showSuccess('SCIM token revoked');
      queryClient.invalidateQueries({ queryKey: SCIM_TOKENS_KEY });
      setRevokeTarget(null);
    },
    onError: (error) => {
      showError(apiErrorMessage(error, 'Failed to revoke the SCIM token'));
      setRevokeTarget(null);
    },
  });

  const openCreate = () => {
    setCreateError(null);
    reset({ name: '' });
    setCreateOpen(true);
  };

  const onCreate = async (form: CreateTokenFormData) => {
    setCreateError(null);
    try {
      const created = await scimTokensApi.create(form.name);
      setCreateOpen(false);
      setCreatedToken(created.token);
    } catch (error) {
      setCreateError(
        apiErrorMessage(error, 'Failed to create the SCIM token', { details: true }),
      );
    }
  };

  const closeCreated = () => {
    setCreatedToken(null);
    queryClient.invalidateQueries({ queryKey: SCIM_TOKENS_KEY });
  };

  const tokens = data ?? [];

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 1 }}>
        <Typography variant="h4">SCIM tokens</Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={openCreate}>
          Create token
        </Button>
      </Box>

      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        An identity provider sends one of these tokens as <code>Authorization: Bearer</code> on
        <code> /scim/v2</code> to provision users. A token is shown once, when it is created; a
        revoked token is refused at once and stays listed.
      </Typography>

      <Paper>
        {isLoading ? (
          <Loading />
        ) : isError ? (
          <Alert severity="error" sx={{ m: 2 }}>
            {apiErrorMessage(listError, 'Failed to load the SCIM tokens. Please try again.')}
          </Alert>
        ) : (
          <TableContainer>
            <Table>
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>Created</TableCell>
                  <TableCell>Last used</TableCell>
                  <TableCell>Revoked</TableCell>
                  <TableCell align="right">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {tokens.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} align="center" sx={{ py: 4 }}>
                      <Typography variant="body2" color="text.secondary">
                        There are no SCIM tokens yet.
                      </Typography>
                    </TableCell>
                  </TableRow>
                ) : (
                  tokens.map((token) => {
                    const revoked = token.revoked_at !== null;
                    return (
                      <TableRow
                        key={token.id}
                        hover
                        data-testid="scim-token-row"
                        sx={revoked ? { opacity: 0.55 } : undefined}
                      >
                        <TableCell>{token.name}</TableCell>
                        <TableCell>{formatDate(token.created_at, 'PPp')}</TableCell>
                        <TableCell>{formatDate(token.last_used_at, 'PPp')}</TableCell>
                        <TableCell>{formatDate(token.revoked_at, 'PPp')}</TableCell>
                        <TableCell align="right">
                          <Tooltip title={revoked ? 'Already revoked' : 'Revoke'}>
                            <span>
                              <IconButton
                                aria-label={`Revoke ${token.name}`}
                                onClick={() => setRevokeTarget(token)}
                                disabled={revoked}
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
        aria-labelledby="create-scim-token-title"
      >
        <Box component="form" onSubmit={handleSubmit(onCreate)} noValidate>
          <DialogTitle id="create-scim-token-title">Create SCIM token</DialogTitle>
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
              helperText={
                errors.name?.message || 'The identity provider that will use it, e.g. "Entra ID".'
              }
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

      <SCIMTokenSecretDialog token={createdToken} onClose={closeCreated} />

      <ConfirmDialog
        open={!!revokeTarget}
        title="Revoke SCIM token"
        message={`Revoke "${revokeTarget?.name ?? ''}"? The identity provider using it will be refused at once. This cannot be undone.`}
        confirmText="Revoke"
        severity="error"
        onConfirm={() => revokeTarget && revokeMutation.mutate(revokeTarget.id)}
        onCancel={() => setRevokeTarget(null)}
      />
    </Box>
  );
};

export function Component() {
  return <SCIMTokens />;
}

Component.displayName = 'SCIMTokens';
