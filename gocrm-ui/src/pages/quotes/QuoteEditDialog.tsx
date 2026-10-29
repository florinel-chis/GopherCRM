import React, { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
} from '@mui/material';
import { quotesApi, type UpdateQuoteData } from '@/api/endpoints';
import type { Quote } from '@/types';
import { getAPIError } from '@/utils/apiError';

export interface QuoteEditDialogProps {
  open: boolean;
  quote: Quote;
  onClose: () => void;
  onSaved: (quote: Quote) => void;
}

const TITLE_MAX_LENGTH = 200;
const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;

type QuoteEditFormProps = Omit<QuoteEditDialogProps, 'open'>;

// The dialog's content; unmounted while closed, so it restarts from the quote.
const QuoteEditForm: React.FC<QuoteEditFormProps> = ({ quote, onClose, onSaved }) => {
  const queryClient = useQueryClient();
  const [title, setTitle] = useState(quote.title);
  const [validUntil, setValidUntil] = useState(quote.valid_until ?? '');
  const [notes, setNotes] = useState(quote.notes);
  const [titleError, setTitleError] = useState<string | null>(null);
  const [dateError, setDateError] = useState<string | null>(null);
  const [serverError, setServerError] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: (data: UpdateQuoteData) => quotesApi.updateQuote(quote.id, data),
    onSuccess: (updated) => {
      queryClient.setQueryData(['quote', quote.id], updated);
      queryClient.invalidateQueries({ queryKey: ['quotes'] });
      onSaved(updated);
    },
    onError: (error) => {
      setServerError(getAPIError(error)?.message || 'Failed to update the quote');
    },
  });

  const submit = () => {
    const trimmed = title.trim();
    let valid = true;
    if (trimmed === '') {
      setTitleError('Title is required');
      valid = false;
    } else if (trimmed.length > TITLE_MAX_LENGTH) {
      setTitleError(`Title must be ${TITLE_MAX_LENGTH} characters or fewer`);
      valid = false;
    }
    if (validUntil !== '' && !DATE_PATTERN.test(validUntil)) {
      setDateError('Valid until must be a date');
      valid = false;
    }
    if (!valid) {
      return;
    }
    setServerError(null);
    mutation.mutate({ title: trimmed, valid_until: validUntil === '' ? null : validUntil, notes });
  };

  return (
    <>
      <DialogTitle id="quote-edit-dialog-title">Edit quote</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ mt: 1 }}>
          <TextField
            autoFocus
            required
            fullWidth
            label="Title"
            value={title}
            onChange={(event) => {
              setTitle(event.target.value);
              setTitleError(null);
            }}
            error={titleError !== null}
            helperText={titleError}
            inputProps={{ maxLength: TITLE_MAX_LENGTH, name: 'title' }}
          />
          <TextField
            label="Valid until"
            type="date"
            value={validUntil}
            onChange={(event) => {
              setValidUntil(event.target.value);
              setDateError(null);
            }}
            InputLabelProps={{ shrink: true }}
            inputProps={{ name: 'valid_until' }}
            error={dateError !== null}
            helperText={dateError ?? 'Clear the date to remove it'}
            fullWidth
          />
          <TextField
            label="Notes"
            multiline
            rows={3}
            value={notes}
            onChange={(event) => setNotes(event.target.value)}
            inputProps={{ name: 'notes' }}
            fullWidth
          />
          {serverError && (
            <Alert severity="error" data-testid="quote-edit-error">
              {serverError}
            </Alert>
          )}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={mutation.isPending}>
          Cancel
        </Button>
        <Button variant="contained" onClick={submit} disabled={mutation.isPending}>
          Save
        </Button>
      </DialogActions>
    </>
  );
};

/** Edits the title, validity date and notes of a draft quote. */
export const QuoteEditDialog: React.FC<QuoteEditDialogProps> = ({ open, quote, onClose, onSaved }) => (
  <Dialog open={open} onClose={onClose} aria-labelledby="quote-edit-dialog-title" fullWidth maxWidth="sm">
    <QuoteEditForm quote={quote} onClose={onClose} onSaved={onSaved} />
  </Dialog>
);
