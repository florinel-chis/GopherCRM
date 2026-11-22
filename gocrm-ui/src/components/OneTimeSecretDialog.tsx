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

const COPY_FAILED = 'Could not copy to the clipboard. Select and copy it manually.';

interface OneTimeSecretDialogProps {
  // The dialog title, also its accessible name.
  title: string;
  // The label of the read-only field that holds the value.
  label: string;
  // The aria-label of the copy button.
  copyLabel: string;
  // The notice shown above the value.
  warning: React.ReactNode;
  // The success snackbar text after a copy. It never contains the value.
  copiedMessage: string;
  // The value of the create response, or null while the dialog is closed.
  // The caller holds it in component state only and drops it in onClose.
  secret: string | null;
  onClose: () => void;
  // The data-testid of the read-only input; the title id is `${testId}-title`.
  testId: string;
}

/**
 * Shows a value the API returns exactly once (a new API key, SCIM token or
 * webhook signing secret), so this dialog is the single place it is ever
 * rendered. It closes only through Done: a click outside it or Escape leaves
 * it open. It writes nothing to browser storage or the query cache.
 */
export const OneTimeSecretDialog: React.FC<OneTimeSecretDialogProps> = ({
  title,
  label,
  copyLabel,
  warning,
  copiedMessage,
  secret,
  onClose,
  testId,
}) => {
  const { showSuccess, showError } = useSnackbar();

  const copySecret = async () => {
    if (secret === null) {
      return;
    }
    if (typeof navigator.clipboard?.writeText !== 'function') {
      showError(COPY_FAILED);
      return;
    }
    try {
      await navigator.clipboard.writeText(secret);
      showSuccess(copiedMessage);
    } catch {
      showError(COPY_FAILED);
    }
  };

  const titleId = `${testId}-title`;

  return (
    // No onClose on the Dialog: backdrop clicks and Escape are ignored, so
    // only Done drops the value.
    <Dialog open={secret !== null} fullWidth maxWidth="sm" aria-labelledby={titleId}>
      <DialogTitle id={titleId}>{title}</DialogTitle>
      <DialogContent>
        <Alert severity="warning" sx={{ mb: 2 }}>
          {warning}
        </Alert>
        <TextField
          label={label}
          value={secret ?? ''}
          fullWidth
          margin="dense"
          autoComplete="off"
          slotProps={{
            htmlInput: { readOnly: true, 'data-testid': testId },
            input: {
              sx: { fontFamily: 'monospace' },
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton aria-label={copyLabel} onClick={copySecret} edge="end">
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
