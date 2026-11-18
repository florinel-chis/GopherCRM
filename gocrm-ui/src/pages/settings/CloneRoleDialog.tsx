import React, { useEffect, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  TextField,
} from '@mui/material';
import { rolesApi, type Role } from '@/api/endpoints';
import { useSnackbar } from '@/hooks/useSnackbar';
import { getAPIError } from '@/utils/apiError';

interface CloneRoleDialogProps {
  /** The role to copy; the dialog is closed while it is null. */
  source: Role | null;
  onClose: () => void;
  /** Called with the new custom role once the API created it. */
  onCloned: (created: Role) => void;
}

/**
 * Asks for the name of the copy and posts it to POST /roles/{id}/clone. A
 * name another role already has is reported on the field and leaves the
 * dialog open; any other refusal goes to the snackbar.
 */
export const CloneRoleDialog: React.FC<CloneRoleDialogProps> = ({ source, onClose, onCloned }) => {
  const queryClient = useQueryClient();
  const { showError } = useSnackbar();
  const [name, setName] = useState('');
  const [nameError, setNameError] = useState('');

  useEffect(() => {
    if (source) {
      setName('');
      setNameError('');
    }
  }, [source]);

  const cloneMutation = useMutation({
    mutationFn: ({ id, cloneName }: { id: number; cloneName: string }) =>
      rolesApi.cloneRole(id, { name: cloneName }),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['roles'] });
      onCloned(created);
    },
    onError: (error: unknown) => {
      const apiError = getAPIError(error);
      if (apiError?.code === 'ROLE_NAME_TAKEN') {
        setNameError('Another role already has this name');
        return;
      }
      showError(apiError?.message || 'Failed to clone the role');
    },
  });

  const trimmed = name.trim();

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!source || !trimmed || cloneMutation.isPending) return;
    cloneMutation.mutate({ id: source.id, cloneName: trimmed });
  };

  return (
    <Dialog
      open={source !== null}
      onClose={onClose}
      fullWidth
      maxWidth="xs"
      aria-labelledby="clone-role-title"
    >
      <form onSubmit={submit} noValidate>
        <DialogTitle id="clone-role-title">Clone role</DialogTitle>
        <DialogContent>
          <TextField
            autoFocus
            fullWidth
            required
            margin="dense"
            label="Name of the new role"
            value={name}
            onChange={(event) => {
              setName(event.target.value);
              setNameError('');
            }}
            error={!!nameError}
            helperText={nameError || (source ? `A copy of ${source.name} with the same permissions` : ' ')}
            slotProps={{ htmlInput: { maxLength: 100 } }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="contained" disabled={!trimmed || cloneMutation.isPending}>
            Clone
          </Button>
        </DialogActions>
      </form>
    </Dialog>
  );
};
