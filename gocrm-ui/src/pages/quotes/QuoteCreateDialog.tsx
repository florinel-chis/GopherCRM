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
import { quotesApi, type CreateQuoteData } from '@/api/endpoints';
import type { Customer, Deal, Quote } from '@/types';
import { DealAutocomplete } from '@/components/DealAutocomplete';
import { CustomerAutocomplete } from '@/components/CustomerAutocomplete';
import { useDefaultDealCurrency } from '@/pages/deals/useDefaultDealCurrency';
import { getAPIError } from '@/utils/apiError';

export interface QuoteCreateDialogProps {
  open: boolean;
  onClose: () => void;
  /** The deal the dialog starts on (DealDetail's "New quote"); its customer is prefilled too. */
  initialDeal?: Deal | null;
  /** Called with the created draft; the caller navigates to it. */
  onCreated: (quote: Quote) => void;
}

const TITLE_MAX_LENGTH = 200;
const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;

interface FieldErrors {
  title?: string;
  links?: string;
  currency?: string;
  valid_until?: string;
}

type QuoteCreateFormProps = Omit<QuoteCreateDialogProps, 'open' | 'initialDeal'> & { initialDeal: Deal | null };

// The dialog's content. MUI unmounts it while the dialog is closed, so every
// opening starts from the initial deal with no errors left over.
const QuoteCreateForm: React.FC<QuoteCreateFormProps> = ({ initialDeal, onClose, onCreated }) => {
  const queryClient = useQueryClient();
  const defaultCurrency = useDefaultDealCurrency();

  const [title, setTitle] = useState('');
  const [dealId, setDealId] = useState<number | null>(initialDeal?.id ?? null);
  const [pickedDeal, setPickedDeal] = useState<Deal | null>(initialDeal);
  const [customerId, setCustomerId] = useState<number | null>(initialDeal?.customer?.id ?? null);
  const [customer, setCustomer] = useState<Customer | null>(initialDeal?.customer ?? null);
  // The currency follows the picked deal, else the configured default, until
  // the user edits it (`currency` is null until then).
  const [currency, setCurrency] = useState<string | null>(null);
  const [validUntil, setValidUntil] = useState('');
  const [notes, setNotes] = useState('');
  const [errors, setErrors] = useState<FieldErrors>({});
  const [serverError, setServerError] = useState<string | null>(null);

  const effectiveCurrency = currency ?? pickedDeal?.currency ?? defaultCurrency;

  const mutation = useMutation({
    mutationFn: (data: CreateQuoteData) => quotesApi.createQuote(data),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ['quotes'] });
      queryClient.invalidateQueries({ queryKey: ['deal'] });
      onCreated(created);
    },
    onError: (error) => {
      setServerError(getAPIError(error)?.message || 'Failed to create the quote');
    },
  });

  const validate = (): FieldErrors => {
    const next: FieldErrors = {};
    const trimmedTitle = title.trim();
    if (trimmedTitle === '') {
      next.title = 'Title is required';
    } else if (trimmedTitle.length > TITLE_MAX_LENGTH) {
      next.title = `Title must be ${TITLE_MAX_LENGTH} characters or fewer`;
    }
    if (dealId === null && customerId === null) {
      next.links = 'Pick a deal or a customer';
    }
    if (!/^[A-Z]{3}$/.test(effectiveCurrency)) {
      next.currency = 'Currency must be a 3-letter ISO code';
    }
    if (validUntil !== '' && !DATE_PATTERN.test(validUntil)) {
      next.valid_until = 'Valid until must be a date';
    }
    return next;
  };

  const submit = () => {
    const next = validate();
    setErrors(next);
    if (Object.keys(next).length > 0) {
      return;
    }
    setServerError(null);
    const payload: CreateQuoteData = {
      title: title.trim(),
      currency: effectiveCurrency,
    };
    if (dealId !== null) {
      payload.deal_id = dealId;
    }
    if (customerId !== null) {
      payload.customer_id = customerId;
    }
    if (validUntil !== '') {
      payload.valid_until = validUntil;
    }
    if (notes.trim() !== '') {
      payload.notes = notes;
    }
    mutation.mutate(payload);
  };

  return (
    <>
      <DialogTitle id="quote-create-dialog-title">New quote</DialogTitle>
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
              setErrors((prev) => ({ ...prev, title: undefined }));
            }}
            error={!!errors.title}
            helperText={errors.title}
            inputProps={{ maxLength: TITLE_MAX_LENGTH, name: 'title' }}
          />
          <DealAutocomplete
            value={dealId}
            initialDeal={initialDeal}
            onChange={(nextId, deal) => {
              setDealId(nextId);
              setPickedDeal(deal);
              setErrors((prev) => ({ ...prev, links: undefined }));
              // The quote is addressed to the deal's customer; a deal without
              // one leaves whatever customer is picked.
              if (deal?.customer) {
                setCustomerId(deal.customer.id);
                setCustomer(deal.customer);
              }
            }}
            error={!!errors.links}
            helperText={errors.links ?? 'Open deals only; the quote is addressed to the deal’s customer'}
          />
          <CustomerAutocomplete
            value={customerId}
            initialCustomer={customer}
            onChange={(nextId, record) => {
              setCustomerId(nextId);
              setCustomer(record);
              setErrors((prev) => ({ ...prev, links: undefined }));
            }}
            error={!!errors.links}
            helperText={errors.links ?? 'Required without a deal; must be the deal’s customer when both are set'}
          />
          <TextField
            label="Currency"
            value={effectiveCurrency}
            onChange={(event) => {
              setCurrency(event.target.value.toUpperCase());
              setErrors((prev) => ({ ...prev, currency: undefined }));
            }}
            inputProps={{ maxLength: 3, style: { textTransform: 'uppercase' }, name: 'currency' }}
            error={!!errors.currency}
            helperText={errors.currency ?? 'ISO 4217 code; fixed once the quote has lines'}
            fullWidth
          />
          <TextField
            label="Valid until"
            type="date"
            value={validUntil}
            onChange={(event) => {
              setValidUntil(event.target.value);
              setErrors((prev) => ({ ...prev, valid_until: undefined }));
            }}
            InputLabelProps={{ shrink: true }}
            inputProps={{ name: 'valid_until' }}
            error={!!errors.valid_until}
            helperText={errors.valid_until ?? 'Optional; acceptance is refused after this date'}
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
            <Alert severity="error" data-testid="quote-create-error">
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
          Create
        </Button>
      </DialogActions>
    </>
  );
};

/**
 * Starts a draft quote from the quotes list or from a deal's Quotes section.
 * The deal picker is prefilled from DealDetail; at least one of deal and
 * customer is required, as on the API.
 */
export const QuoteCreateDialog: React.FC<QuoteCreateDialogProps> = ({ open, onClose, initialDeal = null, onCreated }) => (
  <Dialog open={open} onClose={onClose} aria-labelledby="quote-create-dialog-title" fullWidth maxWidth="sm">
    <QuoteCreateForm initialDeal={initialDeal} onClose={onClose} onCreated={onCreated} />
  </Dialog>
);
