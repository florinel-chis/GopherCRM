import React, { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Alert, Box, Button, Typography } from '@mui/material';
import { useSnackbar } from '@/hooks/useSnackbar';
import { quotesApi } from '@/api/endpoints';
import { quoteTransitionsFrom, type Quote, type QuoteStatus } from '@/types';
import { getAPIError } from '@/utils/apiError';

export interface QuoteStatusActionsProps {
  quote: Quote;
  disabled?: boolean;
  /** Called with the quote the API returned after a successful move. */
  onChanged?: (quote: Quote) => void;
}

interface ActionSpec {
  label: string;
  color: 'primary' | 'success' | 'error' | 'warning' | 'inherit';
  variant: 'contained' | 'outlined';
}

// One button per allowed target status (models.quoteTransitions); the labels
// are what the acceptance checks and the e2e spec click.
const ACTIONS: Record<QuoteStatus, ActionSpec> = {
  sent: { label: 'Mark as sent', color: 'primary', variant: 'contained' },
  accepted: { label: 'Accept', color: 'success', variant: 'contained' },
  rejected: { label: 'Reject', color: 'error', variant: 'outlined' },
  expired: { label: 'Mark expired', color: 'warning', variant: 'outlined' },
  draft: { label: 'Revise', color: 'inherit', variant: 'outlined' },
};

/**
 * The status buttons of a quote: exactly the transitions valid from its
 * current status. Each posts to /quotes/:id/status, stores the returned quote
 * in the cache and marks the history stale. A 400 INVALID_STATUS_TRANSITION
 * (a lost race) or a 409 CONFLICT (no lines, past valid_until) shows the
 * server's message.
 */
export const QuoteStatusActions: React.FC<QuoteStatusActionsProps> = ({ quote, disabled = false, onChanged }) => {
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const [error, setError] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: (status: QuoteStatus) => quotesApi.changeStatus(quote.id, status),
    onSuccess: (updated) => {
      setError(null);
      showSuccess('Quote status updated');
      queryClient.setQueryData(['quote', quote.id], updated);
      queryClient.invalidateQueries({ queryKey: ['quote', quote.id, 'history'] });
      queryClient.invalidateQueries({ queryKey: ['quotes'] });
      queryClient.invalidateQueries({ queryKey: ['deal'] });
      onChanged?.(updated);
    },
    onError: (failure) => {
      const message = getAPIError(failure)?.message || 'Failed to change the quote status';
      setError(message);
      showError(message);
    },
  });

  const transitions = quoteTransitionsFrom(quote.status);

  return (
    <Box data-testid="quote-status-actions">
      {transitions.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          No further status changes
        </Typography>
      ) : (
        <Box display="flex" gap={1} flexWrap="wrap">
          {transitions.map((target) => {
            const spec = ACTIONS[target];
            return (
              <Button
                key={target}
                variant={spec.variant}
                color={spec.color}
                disabled={disabled || mutation.isPending}
                onClick={() => {
                  setError(null);
                  mutation.mutate(target);
                }}
              >
                {spec.label}
              </Button>
            );
          })}
        </Box>
      )}
      {error && (
        <Alert severity="error" sx={{ mt: 1 }} onClose={() => setError(null)} data-testid="quote-status-error">
          {error}
        </Alert>
      )}
    </Box>
  );
};
