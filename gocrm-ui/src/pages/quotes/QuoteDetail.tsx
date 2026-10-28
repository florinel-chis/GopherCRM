import React, { useState } from 'react';
import { useParams, useNavigate, Link as RouterLink } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Button,
  Divider,
  IconButton,
  Link,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  Edit as EditIcon,
  Delete as DeleteIcon,
  Business as CustomerIcon,
  Handshake as DealIcon,
  Person as PersonIcon,
  CalendarToday as CalendarIcon,
  Email as EmailIcon,
  Print as PrintIcon,
  History as HistoryIcon,
  Notes as NotesIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { quotesApi } from '@/api/endpoints';
import { canEmailQuote, isDraftQuote, type Quote, type QuoteLine } from '@/types';
import { formatDate } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';
import { formatCalendarDate } from '@/pages/deals/dealFormat';
import { QuoteStatusChip } from './QuoteStatusChip';
import { QuoteStatusActions } from './QuoteStatusActions';
import { QuoteHistory } from './QuoteHistory';
import { QuoteLineDialog } from './QuoteLineDialog';
import { QuoteEditDialog } from './QuoteEditDialog';
import { QuoteEmailDialog } from './QuoteEmailDialog';
import { formatDiscountPercent, formatMoney, quoteCustomerName, quoteOwnerName } from './quoteFormat';

interface FieldProps {
  icon: React.ReactNode;
  label: string;
  children: React.ReactNode;
}

const Field: React.FC<FieldProps> = ({ icon, label, children }) => (
  <Box display="flex" alignItems="center" gap={1}>
    {icon}
    <Box>
      <Typography variant="caption" color="text.secondary">
        {label}
      </Typography>
      <Typography component="div">{children}</Typography>
    </Box>
  </Box>
);

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  const quoteId = Number(id);
  const isAdmin = user?.role === 'admin';

  const [editOpen, setEditOpen] = useState(false);
  const [lineDialog, setLineDialog] = useState<{ open: boolean; line: QuoteLine | null }>({ open: false, line: null });
  const [deleteLine, setDeleteLine] = useState<QuoteLine | null>(null);
  const [emailOpen, setEmailOpen] = useState(false);
  const [deleteDialog, setDeleteDialog] = useState(false);

  const { data: quote, isLoading, isError } = useQuery({
    queryKey: ['quote', quoteId],
    queryFn: () => quotesApi.getQuote(quoteId),
    enabled: !!id,
  });

  const { data: history = [], isLoading: historyLoading } = useQuery({
    queryKey: ['quote', quoteId, 'history'],
    queryFn: () => quotesApi.getHistory(quoteId),
    enabled: !!id,
  });

  const storeQuote = (updated: Quote) => {
    queryClient.setQueryData(['quote', quoteId], updated);
    queryClient.invalidateQueries({ queryKey: ['quotes'] });
  };

  const deleteLineMutation = useMutation({
    mutationFn: (lineId: number) => quotesApi.deleteLine(quoteId, lineId),
    onSuccess: (updated) => {
      showSuccess('Line removed');
      storeQuote(updated);
      setDeleteLine(null);
    },
    onError: (error) => {
      showError(getAPIError(error)?.message || 'Failed to remove the line');
      setDeleteLine(null);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: () => quotesApi.deleteQuote(quoteId),
    onSuccess: () => {
      showSuccess('Quote deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['quotes'] });
      queryClient.invalidateQueries({ queryKey: ['deal'] });
      navigate('/quotes');
    },
    onError: (error) => {
      showError(getAPIError(error)?.message || 'Failed to delete quote');
      setDeleteDialog(false);
    },
  });

  if (isError) {
    return (
      <Box>
        <Typography variant="h5" gutterBottom>
          Quote not found
        </Typography>
        <Button variant="outlined" onClick={() => navigate('/quotes')}>
          Back to quotes
        </Button>
      </Box>
    );
  }

  if (isLoading || !quote) {
    return <Loading />;
  }

  const draft = isDraftQuote(quote.status);
  // Admin deletes any quote; a sales owner only its own drafts (the API
  // answers 409 otherwise).
  const canDelete = isAdmin || draft;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="flex-start" mb={3} flexWrap="wrap" gap={2}>
        <Box>
          <Box display="flex" alignItems="center" gap={2}>
            <Typography variant="h4" data-testid="quote-number">
              {quote.number}
            </Typography>
            <QuoteStatusChip status={quote.status} size="medium" />
          </Box>
          <Typography variant="h6" component="p" color="text.secondary" data-testid="quote-title">
            {quote.title}
          </Typography>
        </Box>
        <Box display="flex" gap={1} alignItems="center" flexWrap="wrap">
          {draft && (
            <Button variant="outlined" startIcon={<EditIcon />} onClick={() => setEditOpen(true)}>
              Edit
            </Button>
          )}
          <Button
            variant="outlined"
            startIcon={<PrintIcon />}
            component={RouterLink}
            to={`/quotes/${quote.id}/print`}
          >
            Print
          </Button>
          {canEmailQuote(quote.status) && (
            <Button variant="outlined" startIcon={<EmailIcon />} onClick={() => setEmailOpen(true)}>
              Email quote
            </Button>
          )}
          {canDelete && (
            <IconButton color="error" aria-label="Delete quote" onClick={() => setDeleteDialog(true)}>
              <DeleteIcon />
            </IconButton>
          )}
        </Box>
      </Box>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Stack spacing={3}>
          <Box>
            <Typography variant="h6" gutterBottom>
              Quote Information
            </Typography>
            <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
              <Field icon={<CustomerIcon color="action" />} label="Customer">
                {quote.customer ? (
                  <Link component={RouterLink} to={`/customers/${quote.customer.id}`} data-testid="quote-customer">
                    {quoteCustomerName(quote.customer)}
                    {quote.customer.company ? ` (${quote.customer.company})` : ''}
                  </Link>
                ) : (
                  'No customer'
                )}
              </Field>
              <Field icon={<DealIcon color="action" />} label="Deal">
                {quote.deal ? (
                  <Link component={RouterLink} to={`/deals/${quote.deal.id}`} data-testid="quote-deal">
                    {quote.deal.title}
                  </Link>
                ) : (
                  'Not linked'
                )}
              </Field>
              <Field icon={<PersonIcon color="action" />} label="Owner">
                {quoteOwnerName(quote.owner, quote.owner_id)}
              </Field>
              <Field icon={<CalendarIcon color="action" />} label="Valid until">
                <span data-testid="quote-valid-until">{formatCalendarDate(quote.valid_until)}</span>
              </Field>
              {quote.sent_at && (
                <Field icon={<CalendarIcon color="action" />} label="Sent">
                  {formatDate(quote.sent_at, 'MMM dd, yyyy HH:mm')}
                </Field>
              )}
              {quote.closed_at && (
                <Field icon={<CalendarIcon color="action" />} label="Closed">
                  {formatDate(quote.closed_at, 'MMM dd, yyyy HH:mm')}
                </Field>
              )}
              {quote.last_emailed_at && (
                <Field icon={<EmailIcon color="action" />} label="Last emailed">
                  <span data-testid="quote-last-emailed">{formatDate(quote.last_emailed_at, 'MMM dd, yyyy HH:mm')}</span>
                </Field>
              )}
              <Field icon={<CalendarIcon color="action" />} label="Created">
                {formatDate(quote.created_at, 'MMM dd, yyyy HH:mm')}
              </Field>
            </Box>
          </Box>

          <Divider />

          <Box>
            <Typography variant="subtitle2" color="text.secondary" gutterBottom>
              Status
            </Typography>
            <QuoteStatusActions quote={quote} />
          </Box>

          {quote.notes && (
            <>
              <Divider />
              <Box>
                <Box display="flex" alignItems="center" gap={1} mb={1}>
                  <NotesIcon color="action" />
                  <Typography variant="h6">Notes</Typography>
                </Box>
                <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>
                  {quote.notes}
                </Typography>
              </Box>
            </>
          )}
        </Stack>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }} component="section" aria-labelledby="quote-lines-heading">
        <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
          <Typography variant="h6" id="quote-lines-heading">
            Lines ({quote.lines.length})
          </Typography>
          {draft && (
            <Button
              variant="outlined"
              size="small"
              startIcon={<AddIcon />}
              onClick={() => setLineDialog({ open: true, line: null })}
            >
              Add line
            </Button>
          )}
        </Box>
        {quote.lines.length > 0 ? (
          <Table size="small" aria-label="Quote lines">
            <TableHead>
              <TableRow>
                <TableCell>Description</TableCell>
                <TableCell align="right">Qty</TableCell>
                <TableCell align="right">Unit price</TableCell>
                <TableCell align="right">Discount</TableCell>
                <TableCell align="right">Line total</TableCell>
                {draft && <TableCell align="right">Actions</TableCell>}
              </TableRow>
            </TableHead>
            <TableBody>
              {quote.lines.map((line) => (
                <TableRow key={line.id} hover data-testid="quote-line-row">
                  <TableCell>{line.description}</TableCell>
                  <TableCell align="right">{line.quantity}</TableCell>
                  <TableCell align="right">{formatMoney(line.unit_price_cents, quote.currency)}</TableCell>
                  <TableCell align="right">{formatDiscountPercent(line.discount_bp)}</TableCell>
                  <TableCell align="right">{formatMoney(line.line_total_cents, quote.currency)}</TableCell>
                  {draft && (
                    <TableCell align="right">
                      <IconButton
                        size="small"
                        aria-label={`Edit line ${line.position}`}
                        onClick={() => setLineDialog({ open: true, line })}
                      >
                        <EditIcon fontSize="small" />
                      </IconButton>
                      <IconButton
                        size="small"
                        aria-label={`Delete line ${line.position}`}
                        onClick={() => setDeleteLine(line)}
                      >
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : (
          <Typography color="text.secondary">
            {draft ? 'No lines yet; a quote needs at least one line before it can be sent' : 'No lines'}
          </Typography>
        )}

        <Box display="flex" justifyContent="flex-end" mt={2}>
          <Table size="small" sx={{ width: 'auto', minWidth: 280 }} aria-label="Totals">
            <TableBody>
              <TableRow>
                <TableCell>Subtotal</TableCell>
                <TableCell align="right" data-testid="quote-subtotal">
                  {formatMoney(quote.subtotal_cents, quote.currency)}
                </TableCell>
              </TableRow>
              <TableRow>
                <TableCell>Discount</TableCell>
                <TableCell align="right" data-testid="quote-discount">
                  {formatMoney(quote.discount_cents, quote.currency)}
                </TableCell>
              </TableRow>
              <TableRow>
                <TableCell sx={{ fontWeight: 'bold' }}>Total</TableCell>
                <TableCell align="right" sx={{ fontWeight: 'bold' }} data-testid="quote-total">
                  {formatMoney(quote.total_cents, quote.currency)}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </Box>
      </Paper>

      <Paper sx={{ p: 3 }} component="section" aria-labelledby="quote-history-heading">
        <Box display="flex" alignItems="center" gap={1} mb={1}>
          <HistoryIcon color="action" />
          <Typography variant="h6" id="quote-history-heading">
            History
          </Typography>
        </Box>
        <QuoteHistory history={history} isLoading={historyLoading} />
      </Paper>

      {draft && (
        <>
          <QuoteEditDialog
            open={editOpen}
            quote={quote}
            onClose={() => setEditOpen(false)}
            onSaved={(updated) => {
              showSuccess('Quote updated');
              storeQuote(updated);
              setEditOpen(false);
            }}
          />
          {/*
            Closing keeps the line: the dialog stays mounted through its exit
            transition and would otherwise retitle itself "Add line" while the
            page behind it is still aria-hidden. Opening always sets both.
          */}
          <QuoteLineDialog
            open={lineDialog.open}
            quote={quote}
            line={lineDialog.line}
            onClose={() => setLineDialog((prev) => ({ ...prev, open: false }))}
            onSaved={(updated) => {
              showSuccess(lineDialog.line ? 'Line updated' : 'Line added');
              storeQuote(updated);
              setLineDialog((prev) => ({ ...prev, open: false }));
            }}
          />
          <ConfirmDialog
            open={deleteLine !== null}
            title="Remove line"
            message={`Remove "${deleteLine?.description}" from the quote? The totals are recomputed.`}
            severity="warning"
            confirmText="Remove"
            onConfirm={() => {
              if (deleteLine) {
                deleteLineMutation.mutate(deleteLine.id);
              }
            }}
            onCancel={() => setDeleteLine(null)}
          />
        </>
      )}

      {canEmailQuote(quote.status) && (
        <QuoteEmailDialog
          open={emailOpen}
          quote={quote}
          onClose={() => setEmailOpen(false)}
          onEmailed={storeQuote}
        />
      )}

      <ConfirmDialog
        open={deleteDialog}
        title="Delete Quote"
        message={`Are you sure you want to delete ${quote.number} "${quote.title}"? Its lines and history are kept. This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};
