import React, { useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { parseISO } from 'date-fns';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Container,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { quoteResponseApi, type QuotePublicView, type QuoteResponseDecision } from '@/api/endpoints';
import { getAPIError, getHTTPStatus } from '@/utils/apiError';
import { formatDate } from '@/utils/date';
import { formatDiscountPercent, formatMoney, formatQuantity } from './quoteFormat';

export const QUOTE_LINK_INVALID_MESSAGE =
  'This link is no longer valid. It may already have been used or it has expired.';
export const QUOTE_LOAD_FAILED_MESSAGE = 'The quote could not be loaded. Please try again later.';
export const QUOTE_RESPONSE_FAILED_MESSAGE = 'Your answer could not be recorded. Please try again.';

type ViewState =
  | { kind: 'loading' }
  | { kind: 'invalid' }
  | { kind: 'unavailable' }
  | { kind: 'ready'; quote: QuotePublicView }
  | { kind: 'answered'; number: string; decision: QuoteResponseDecision };

const DECISION_VERB: Record<QuoteResponseDecision, string> = { accept: 'Accept', decline: 'Decline' };
const DECISION_DONE: Record<QuoteResponseDecision, string> = { accept: 'accepted', decline: 'declined' };

/**
 * The public page behind the link of a quote email: it shows the quote and
 * records the client's answer, once. It is reached without a session, renders
 * outside the application shell and never shows or logs the token.
 */
export const QuoteResponse: React.FC = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  // The token is read from the link once, on the first render, and kept in
  // component state only (never in web storage). The effect below then drops
  // it from the address bar with a replace navigation, so the history entry no
  // longer carries it; a reload therefore shows the invalid-link message and
  // the client opens the mailed link again.
  const [token] = useState(() => searchParams.get('token') ?? '');

  const [state, setState] = useState<ViewState>(token ? { kind: 'loading' } : { kind: 'invalid' });
  const [pending, setPending] = useState<QuoteResponseDecision | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  // The link is read once per mount: both routes share one strict rate
  // bucket, so there is no retry and no refetch. The ref also keeps a
  // development double-mount from sending the request twice.
  const requestedFor = useRef<string | null>(null);
  // Set synchronously, so a second click cannot send a second decision
  // before the disabled state has rendered: the second POST would be refused
  // as a used link and hide the answer that was recorded.
  const inFlight = useRef(false);

  useEffect(() => {
    if (searchParams.has('token')) {
      setSearchParams({}, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  useEffect(() => {
    if (!token || requestedFor.current === token) {
      return;
    }
    requestedFor.current = token;
    quoteResponseApi
      .getQuoteResponse(token)
      .then((quote) => setState({ kind: 'ready', quote }))
      .catch((error: unknown) => {
        setState({ kind: getHTTPStatus(error) === 400 ? 'invalid' : 'unavailable' });
      });
  }, [token]);

  const confirm = async (quote: QuotePublicView, decision: QuoteResponseDecision) => {
    if (inFlight.current) {
      return;
    }
    inFlight.current = true;
    setSubmitting(true);
    setSubmitError(null);
    try {
      await quoteResponseApi.respond(token, decision);
      setState({ kind: 'answered', number: quote.number, decision });
    } catch (error: unknown) {
      if (getHTTPStatus(error) === 400 && getAPIError(error)?.code === 'BAD_REQUEST') {
        setState({ kind: 'invalid' });
      } else {
        setSubmitError(QUOTE_RESPONSE_FAILED_MESSAGE);
      }
    } finally {
      inFlight.current = false;
      setSubmitting(false);
      setPending(null);
    }
  };

  return (
    <Container component="main" maxWidth="md">
      <Box sx={{ marginTop: 8, marginBottom: 8 }}>
        <Paper elevation={3} sx={{ padding: 4, width: '100%' }}>
          {state.kind === 'loading' && (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}>
              <CircularProgress aria-label="Loading quote" />
            </Box>
          )}

          {state.kind === 'invalid' && <Alert severity="warning">{QUOTE_LINK_INVALID_MESSAGE}</Alert>}

          {state.kind === 'unavailable' && <Alert severity="error">{QUOTE_LOAD_FAILED_MESSAGE}</Alert>}

          {state.kind === 'answered' && (
            <Alert severity="success">
              You have {DECISION_DONE[state.decision]} quote {state.number}.
            </Alert>
          )}

          {state.kind === 'ready' && (
            <>
              <Typography component="h1" variant="h5" gutterBottom>
                Quote {state.quote.number}
              </Typography>
              {state.quote.valid_until && (
                <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                  Valid until{' '}
                  <span data-testid="quote-response-valid-until">
                    {formatDate(parseISO(state.quote.valid_until), 'MMM dd, yyyy')}
                  </span>
                </Typography>
              )}

              {submitError && (
                <Alert severity="error" sx={{ mb: 2 }}>
                  {submitError}
                </Alert>
              )}

              <TableContainer sx={{ mb: 2 }}>
                <Table size="small" aria-label="Quote lines">
                  <TableHead>
                    <TableRow>
                      <TableCell>Description</TableCell>
                      <TableCell align="right">Quantity</TableCell>
                      <TableCell align="right">Unit price</TableCell>
                      <TableCell align="right">Discount</TableCell>
                      <TableCell align="right">Tax rate</TableCell>
                      <TableCell align="right">Total</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {state.quote.lines.map((line, index) => (
                      <TableRow key={index} data-testid="quote-response-line">
                        <TableCell>{line.description}</TableCell>
                        <TableCell align="right">{formatQuantity(line.quantity)}</TableCell>
                        <TableCell align="right">
                          {formatMoney(line.unit_price_cents, state.quote.currency)}
                        </TableCell>
                        <TableCell align="right">{formatDiscountPercent(line.discount_bp)}</TableCell>
                        <TableCell align="right">{formatDiscountPercent(line.tax_rate_bp)}</TableCell>
                        <TableCell align="right">
                          {formatMoney(line.line_total_cents, state.quote.currency)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </TableContainer>

              <Table size="small" aria-label="Quote totals" sx={{ maxWidth: 360, ml: 'auto', mb: 3 }}>
                <TableBody>
                  {(
                    [
                      ['Subtotal', 'subtotal', state.quote.subtotal_cents],
                      ['Discount', 'discount', state.quote.discount_cents],
                      ['Net', 'net', state.quote.total_cents],
                      ['Tax', 'tax', state.quote.tax_cents],
                      ['Gross', 'gross', state.quote.gross_total_cents],
                    ] as const
                  ).map(([label, key, cents]) => (
                    <TableRow key={key}>
                      <TableCell component="th" scope="row">
                        {label}
                      </TableCell>
                      <TableCell align="right" data-testid={`quote-response-${key}`}>
                        {formatMoney(cents, state.quote.currency)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>

              <Stack direction="row" spacing={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  color="error"
                  disabled={submitting}
                  onClick={() => setPending('decline')}
                >
                  Decline
                </Button>
                <Button variant="contained" disabled={submitting} onClick={() => setPending('accept')}>
                  Accept
                </Button>
              </Stack>

              <Dialog
                open={pending !== null}
                onClose={() => {
                  if (!submitting) setPending(null);
                }}
                aria-labelledby="quote-response-confirm-title"
              >
                {pending !== null && (
                  <>
                    <DialogTitle id="quote-response-confirm-title">
                      {DECISION_VERB[pending]} quote {state.quote.number}?
                    </DialogTitle>
                    <DialogContent>
                      <DialogContentText>
                        You are about to {pending} quote {state.quote.number}. Your answer cannot be
                        changed afterwards.
                      </DialogContentText>
                    </DialogContent>
                    <DialogActions>
                      <Button onClick={() => setPending(null)} disabled={submitting}>
                        Cancel
                      </Button>
                      <Button
                        variant="contained"
                        disabled={submitting}
                        onClick={() => void confirm(state.quote, pending)}
                      >
                        Confirm
                      </Button>
                    </DialogActions>
                  </>
                )}
              </Dialog>
            </>
          )}
        </Paper>
      </Box>
    </Container>
  );
};

export const Component = QuoteResponse;
