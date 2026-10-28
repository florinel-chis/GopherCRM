import React, { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
} from '@mui/material';
import { quotesApi } from '@/api/endpoints';
import type { Quote } from '@/types';
import { formatDate } from '@/utils/date';
import { getAPIError, getHTTPStatus } from '@/utils/apiError';
import { quoteCustomerName } from './quoteFormat';

export interface QuoteEmailDialogProps {
  open: boolean;
  quote: Quote;
  onClose: () => void;
  /** Called with the stamped quote after a successful send. */
  onEmailed?: (quote: Quote) => void;
}

type Outcome = { kind: 'success'; emailedAt: string | null } | { kind: 'error'; message: string };

// 409 is a refusal with the quote unchanged (not sent or accepted, no live
// customer, or revised while the mail was in flight); 502 means the mailer
// failed and nothing was stamped either. The two read differently on purpose.
const describeFailure = (error: unknown): string => {
  const status = getHTTPStatus(error);
  const message = getAPIError(error)?.message;
  if (status === 409) {
    return `The quote was not emailed: ${message || 'it is not ready to be sent'}.`;
  }
  if (status === 502) {
    return 'The quote email could not be delivered. The quote was not changed; try again later.';
  }
  return message || 'Failed to email the quote';
};

interface QuoteEmailFormProps extends Omit<QuoteEmailDialogProps, 'open'> {
  queryKey: readonly unknown[];
}

// The dialog's content. MUI unmounts it while the dialog is closed, so every
// opening starts without an outcome left over from the previous one.
const QuoteEmailForm: React.FC<QuoteEmailFormProps> = ({ quote, onClose, onEmailed, queryKey }) => {
  const queryClient = useQueryClient();
  const [outcome, setOutcome] = useState<Outcome | null>(null);

  const mutation = useMutation({
    mutationFn: () => quotesApi.emailQuote(quote.id),
    onSuccess: (emailed) => {
      setOutcome({ kind: 'success', emailedAt: emailed.last_emailed_at });
      queryClient.setQueryData(queryKey, emailed);
      onEmailed?.(emailed);
    },
    onError: (error) => {
      setOutcome({ kind: 'error', message: describeFailure(error) });
    },
  });

  const recipient = quoteCustomerName(quote.customer);
  const company = quote.customer?.company;
  const sent = outcome?.kind === 'success';

  return (
    <>
      <DialogTitle id="quote-email-dialog-title">Email quote {quote.number}</DialogTitle>
      <DialogContent>
        {quote.customer ? (
          <DialogContentText>
            The quote is sent as a plain-text email to{' '}
            <strong data-testid="quote-email-recipient">
              {recipient}
              {company ? ` (${company})` : ''}
            </strong>{' '}
            at the customer&apos;s current email address. The address is read from the customer record and cannot
            be changed here.
          </DialogContentText>
        ) : (
          <DialogContentText data-testid="quote-email-no-recipient">
            This quote has no customer, so there is nobody to email it to. Link a customer first.
          </DialogContentText>
        )}
        {outcome?.kind === 'success' && (
          <Alert severity="success" sx={{ mt: 2 }} data-testid="quote-email-success">
            Emailed on {formatDate(outcome.emailedAt, 'MMM dd, yyyy HH:mm')}
          </Alert>
        )}
        {outcome?.kind === 'error' && (
          <Alert severity="error" sx={{ mt: 2 }} data-testid="quote-email-error">
            {outcome.message}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={mutation.isPending}>
          {sent ? 'Close' : 'Cancel'}
        </Button>
        {!sent && (
          <Button
            variant="contained"
            onClick={() => mutation.mutate()}
            disabled={mutation.isPending || !quote.customer}
          >
            Send email
          </Button>
        )}
      </DialogActions>
    </>
  );
};

/**
 * Confirms the email of a sent or accepted quote. The recipient is the linked
 * customer, named but never editable: the API reads the current address
 * itself and the UI never carries it.
 */
export const QuoteEmailDialog: React.FC<QuoteEmailDialogProps> = ({ open, quote, onClose, onEmailed }) => (
  <Dialog open={open} onClose={onClose} aria-labelledby="quote-email-dialog-title" fullWidth maxWidth="sm">
    <QuoteEmailForm quote={quote} onClose={onClose} onEmailed={onEmailed} queryKey={['quote', quote.id]} />
  </Dialog>
);
