import React from 'react';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  InputAdornment,
  TextField,
} from '@mui/material';
import { ContentCopy as ContentCopyIcon } from '@mui/icons-material';
import { useSnackbar } from '@/hooks/useSnackbar';

interface SCIMTokenSecretDialogProps {
  // The token of the create response, or null while the dialog is closed.
  // The caller holds it in component state only and drops it in onClose.
  token: string | null;
  onClose: () => void;
}

/**
 * Shows a newly created SCIM provider token. The API returns it in the create
 * response only and stores just its HMAC, so this dialog is the single place
 * it is ever rendered.
 */
export const SCIMTokenSecretDialog: React.FC<SCIMTokenSecretDialogProps> = ({ token, onClose }) => {
  const { showSuccess, showError } = useSnackbar();

  const copyToken = async () => {
    if (!token) {
      return;
    }
    try {
      await navigator.clipboard.writeText(token);
      showSuccess('SCIM token copied to clipboard');
    } catch {
      showError('Could not copy the token. Select and copy it manually.');
    }
  };

  return (
    <Dialog
      open={token !== null}
      onClose={onClose}
      fullWidth
      maxWidth="sm"
      aria-labelledby="scim-token-secret-title"
    >
      <DialogTitle id="scim-token-secret-title">Your new SCIM token</DialogTitle>
      <DialogContent>
        <Alert severity="warning" sx={{ mb: 2 }}>
          Copy this token now. It is shown only once and cannot be retrieved again: only a hash is
          stored. If it is lost, revoke it and create a new one.
        </Alert>
        <TextField
          label="SCIM token"
          value={token ?? ''}
          fullWidth
          margin="dense"
          autoComplete="off"
          slotProps={{
            htmlInput: { readOnly: true, 'data-testid': 'scim-token-value' },
            input: {
              sx: { fontFamily: 'monospace' },
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton aria-label="Copy SCIM token" onClick={copyToken} edge="end">
                    <ContentCopyIcon />
                  </IconButton>
                </InputAdornment>
              ),
            },
          }}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} variant="contained">
          Done
        </Button>
      </DialogActions>
    </Dialog>
  );
};
