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

interface WebhookSecretDialogProps {
  // The secret of the create response, or null while the dialog is closed.
  // The caller holds it in component state only and drops it in onClose.
  secret: string | null;
  onClose: () => void;
}

/**
 * Shows the signing secret of a newly registered endpoint. The API returns it
 * once, in the create response (the API-only rotate-secret route returns a new
 * one once, with no screen control), so this dialog is the single place it is
 * ever rendered.
 */
export const WebhookSecretDialog: React.FC<WebhookSecretDialogProps> = ({ secret, onClose }) => {
  const { showSuccess, showError } = useSnackbar();

  const copySecret = async () => {
    if (!secret) {
      return;
    }
    try {
      await navigator.clipboard.writeText(secret);
      showSuccess('Signing secret copied to clipboard');
    } catch {
      showError('Could not copy the secret. Select and copy it manually.');
    }
  };

  return (
    <Dialog
      open={secret !== null}
      onClose={onClose}
      fullWidth
      maxWidth="sm"
      aria-labelledby="webhook-secret-title"
    >
      <DialogTitle id="webhook-secret-title">Signing secret</DialogTitle>
      <DialogContent>
        <Alert severity="warning" sx={{ mb: 2 }}>
          Copy this secret now. It cannot be shown again: it is stored encrypted and no other
          screen or request returns it. If it is lost, delete the endpoint and register it again.
        </Alert>
        <TextField
          label="Signing secret"
          value={secret ?? ''}
          fullWidth
          margin="dense"
          autoComplete="off"
          slotProps={{
            htmlInput: { readOnly: true, 'data-testid': 'webhook-secret' },
            input: {
              sx: { fontFamily: 'monospace' },
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton aria-label="Copy signing secret" onClick={copySecret} edge="end">
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
