import React, { useState } from 'react';
import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  TextField,
} from '@mui/material';

export const LOST_REASON_MAX_LENGTH = 255;

export interface LostReasonDialogProps {
  open: boolean;
  /** The reason the field starts with each time the dialog opens. */
  initialReason?: string;
  /** Disables the buttons while the stage change is in flight. */
  pending?: boolean;
  onCancel: () => void;
  /** Called with the trimmed reason once it passes validation. */
  onConfirm: (reason: string) => void;
}

interface LostReasonFormProps extends Omit<LostReasonDialogProps, 'open'> {
  initialReason: string;
  pending: boolean;
}

// The dialog's content. MUI unmounts it while the dialog is closed, so every
// opening starts from `initialReason` with no error left over.
const LostReasonForm: React.FC<LostReasonFormProps> = ({ initialReason, pending, onCancel, onConfirm }) => {
  const [reason, setReason] = useState(initialReason);
  const [error, setError] = useState<string | null>(null);

  const confirm = () => {
    const trimmed = reason.trim();
    if (trimmed === '') {
      setError('A reason is required to mark the deal as lost');
      return;
    }
    if (trimmed.length > LOST_REASON_MAX_LENGTH) {
      setError(`Lost reason must be ${LOST_REASON_MAX_LENGTH} characters or fewer`);
      return;
    }
    onConfirm(trimmed);
  };

  return (
    <>
      <DialogTitle id="lost-dialog-title">Mark deal as lost</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ mb: 2 }}>
          The deal is closed with probability 0 and the reason is kept with it.
        </DialogContentText>
        <TextField
          autoFocus
          fullWidth
          required
          multiline
          minRows={2}
          label="Lost reason"
          value={reason}
          onChange={(event) => {
            setReason(event.target.value);
            setError(null);
          }}
          error={error !== null}
          helperText={error ?? `Up to ${LOST_REASON_MAX_LENGTH} characters`}
          inputProps={{ maxLength: LOST_REASON_MAX_LENGTH }}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel} disabled={pending}>
          Cancel
        </Button>
        <Button variant="contained" color="error" onClick={confirm} disabled={pending}>
          Mark as lost
        </Button>
      </DialogActions>
    </>
  );
};

/**
 * Asks for the reason before a deal moves to `lost`. Used by the detail page's
 * stage selector and by the board's "Move to Lost" action.
 */
export const LostReasonDialog: React.FC<LostReasonDialogProps> = ({
  open,
  initialReason = '',
  pending = false,
  onCancel,
  onConfirm,
}) => (
  <Dialog open={open} onClose={onCancel} aria-labelledby="lost-dialog-title" fullWidth maxWidth="sm">
    <LostReasonForm
      initialReason={initialReason}
      pending={pending}
      onCancel={onCancel}
      onConfirm={onConfirm}
    />
  </Dialog>
);
