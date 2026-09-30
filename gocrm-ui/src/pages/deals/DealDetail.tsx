import React, { useState } from 'react';
import { useParams, useNavigate, Link as RouterLink } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Divider,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  IconButton,
  Link,
  TextField,
} from '@mui/material';
import {
  Edit as EditIcon,
  Delete as DeleteIcon,
  Apartment as CompanyIcon,
  Business as CustomerIcon,
  ContactPhone as LeadIcon,
  Person as PersonIcon,
  CalendarToday as CalendarIcon,
  Paid as MoneyIcon,
  Percent as PercentIcon,
  Source as SourceIcon,
  EventBusy as ClosedIcon,
  Notes as NotesIcon,
  History as HistoryIcon,
  ReportProblem as LostIcon,
} from '@mui/icons-material';
import { AxiosError } from 'axios';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { dealsApi, type ChangeDealStageData } from '@/api/endpoints';
import { DEAL_STAGES, isClosedDealStage, type DealStage } from '@/types';
import { formatDate } from '@/utils/date';
import { DealStageChip } from './DealStageChip';
import { DealHistory } from './DealHistory';
import { formatCalendarDate, formatDealAmount, isPastCalendarDate } from './dealFormat';

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

const serverMessage = (error: unknown): string | undefined => {
  if (error instanceof AxiosError) {
    const message = (error.response?.data as { message?: unknown } | undefined)?.message;
    return typeof message === 'string' && message !== '' ? message : undefined;
  }
  return undefined;
};

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  const dealId = Number(id);
  const canWrite = user?.role === 'admin' || user?.role === 'sales';
  const canDelete = user?.role === 'admin';

  const [deleteDialog, setDeleteDialog] = useState(false);
  const [lostDialog, setLostDialog] = useState(false);
  const [lostReason, setLostReason] = useState('');
  const [lostReasonError, setLostReasonError] = useState<string | null>(null);

  const { data: deal, isLoading, isError } = useQuery({
    queryKey: ['deal', id],
    queryFn: () => dealsApi.getDeal(dealId),
    enabled: !!id,
  });

  const { data: history = [], isLoading: historyLoading } = useQuery({
    queryKey: ['deal', id, 'history'],
    queryFn: () => dealsApi.getDealHistory(dealId),
    enabled: !!id,
  });

  const stageMutation = useMutation({
    mutationFn: (data: ChangeDealStageData) => dealsApi.changeStage(dealId, data),
    onSuccess: (updated) => {
      showSuccess('Stage updated');
      queryClient.setQueryData(['deal', id], updated);
      queryClient.invalidateQueries({ queryKey: ['deal', id, 'history'] });
      queryClient.invalidateQueries({ queryKey: ['deals'] });
      setLostDialog(false);
      setLostReason('');
      setLostReasonError(null);
    },
    onError: (error) => {
      showError(serverMessage(error) ?? 'Failed to update the stage');
    },
  });

  const deleteMutation = useMutation({
    mutationFn: () => dealsApi.deleteDeal(dealId),
    onSuccess: () => {
      showSuccess('Deal deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['deals'] });
      navigate('/deals');
    },
    onError: () => {
      showError('Failed to delete deal');
    },
  });

  const handleStageSelect = (nextStage: DealStage) => {
    if (!deal || nextStage === deal.stage) {
      return;
    }
    if (nextStage === 'lost') {
      setLostReason(deal.lost_reason || '');
      setLostReasonError(null);
      setLostDialog(true);
      return;
    }
    stageMutation.mutate({ stage: nextStage });
  };

  const confirmLost = () => {
    const reason = lostReason.trim();
    if (reason === '') {
      setLostReasonError('A reason is required to mark the deal as lost');
      return;
    }
    if (reason.length > 255) {
      setLostReasonError('Lost reason must be 255 characters or fewer');
      return;
    }
    stageMutation.mutate({ stage: 'lost', lost_reason: reason });
  };

  if (isError) {
    return (
      <Box>
        <Typography variant="h5" gutterBottom>
          Deal not found
        </Typography>
        <Button variant="outlined" onClick={() => navigate('/deals')}>
          Back to deals
        </Button>
      </Box>
    );
  }

  if (isLoading || !deal) {
    return <Loading />;
  }

  const closed = isClosedDealStage(deal.stage);
  const pastDue = !closed && isPastCalendarDate(deal.expected_close_date);

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3} flexWrap="wrap" gap={2}>
        <Box display="flex" alignItems="center" gap={2}>
          <Typography variant="h4">{deal.title}</Typography>
          <DealStageChip stage={deal.stage} size="medium" />
        </Box>
        <Box display="flex" gap={1} alignItems="center">
          {canWrite && (
            <FormControl size="small" sx={{ minWidth: 170 }}>
              <InputLabel id="deal-stage-label">Stage</InputLabel>
              <Select
                labelId="deal-stage-label"
                id="deal-stage-select"
                value={deal.stage}
                label="Stage"
                disabled={stageMutation.isPending}
                onChange={(event) => handleStageSelect(event.target.value as DealStage)}
              >
                {DEAL_STAGES.map((stage) => (
                  <MenuItem key={stage.value} value={stage.value}>
                    {stage.label}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          )}
          {canWrite && (
            <Button variant="outlined" startIcon={<EditIcon />} onClick={() => navigate(`/deals/${id}/edit`)}>
              Edit
            </Button>
          )}
          {canDelete && (
            <IconButton color="error" aria-label="Delete deal" onClick={() => setDeleteDialog(true)}>
              <DeleteIcon />
            </IconButton>
          )}
        </Box>
      </Box>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Stack spacing={3}>
          <Box>
            <Typography variant="h6" gutterBottom>
              Deal Information
            </Typography>
            <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
              <Field icon={<MoneyIcon color="action" />} label="Amount">
                <span data-testid="deal-amount">{formatDealAmount(deal.amount_cents, deal.currency)}</span>
              </Field>
              <Field icon={<PercentIcon color="action" />} label="Probability">
                {deal.probability}%
              </Field>
              <Field icon={<CalendarIcon color={pastDue ? 'error' : 'action'} />} label="Expected close">
                <Typography
                  component="span"
                  color={pastDue ? 'error' : 'inherit'}
                  data-testid={pastDue ? 'deal-past-due' : undefined}
                >
                  {formatCalendarDate(deal.expected_close_date)}
                  {pastDue && ' (past due)'}
                </Typography>
              </Field>
              <Field icon={<PersonIcon color="action" />} label="Owner">
                {deal.owner ? `${deal.owner.first_name} ${deal.owner.last_name}` : `User #${deal.owner_id}`}
              </Field>
              <Field icon={<CompanyIcon color="action" />} label="Company">
                {deal.company ? (
                  <Link component={RouterLink} to={`/companies/${deal.company.id}`}>
                    {deal.company.name}
                  </Link>
                ) : (
                  'Not linked'
                )}
              </Field>
              <Field icon={<CustomerIcon color="action" />} label="Customer">
                {deal.customer ? (
                  <Link component={RouterLink} to={`/customers/${deal.customer.id}`}>
                    {deal.customer.contact_name || deal.customer.email}
                  </Link>
                ) : (
                  'Not linked'
                )}
              </Field>
              <Field icon={<LeadIcon color="action" />} label="Lead">
                {deal.lead ? (
                  <Link component={RouterLink} to={`/leads/${deal.lead.id}`}>
                    {deal.lead.contact_name || deal.lead.email}
                  </Link>
                ) : (
                  'Not linked'
                )}
              </Field>
              <Field icon={<SourceIcon color="action" />} label="Source">
                {deal.source || 'Not specified'}
              </Field>
              {closed && (
                <Field icon={<ClosedIcon color="action" />} label="Closed at">
                  <span data-testid="deal-closed-at">{formatDate(deal.closed_at, 'MMM dd, yyyy HH:mm')}</span>
                </Field>
              )}
              {deal.stage === 'lost' && (
                <Field icon={<LostIcon color="error" />} label="Lost reason">
                  <span data-testid="deal-lost-reason">{deal.lost_reason || 'Not given'}</span>
                </Field>
              )}
              <Field icon={<CalendarIcon color="action" />} label="Created">
                {formatDate(deal.created_at, 'MMM dd, yyyy HH:mm')}
              </Field>
              <Field icon={<CalendarIcon color="action" />} label="Last Updated">
                {formatDate(deal.updated_at, 'MMM dd, yyyy HH:mm')}
              </Field>
            </Box>
          </Box>

          {deal.notes && (
            <>
              <Divider />
              <Box>
                <Box display="flex" alignItems="center" gap={1} mb={1}>
                  <NotesIcon color="action" />
                  <Typography variant="h6">Notes</Typography>
                </Box>
                <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>
                  {deal.notes}
                </Typography>
              </Box>
            </>
          )}
        </Stack>
      </Paper>

      <Paper sx={{ p: 3 }} component="section" aria-labelledby="deal-history-heading">
        <Box display="flex" alignItems="center" gap={1} mb={1}>
          <HistoryIcon color="action" />
          <Typography variant="h6" id="deal-history-heading">
            History
          </Typography>
        </Box>
        <DealHistory history={history} isLoading={historyLoading} />
      </Paper>

      <Dialog
        open={lostDialog}
        onClose={() => setLostDialog(false)}
        aria-labelledby="lost-dialog-title"
        fullWidth
        maxWidth="sm"
      >
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
            value={lostReason}
            onChange={(event) => {
              setLostReason(event.target.value);
              setLostReasonError(null);
            }}
            error={lostReasonError !== null}
            helperText={lostReasonError ?? 'Up to 255 characters'}
            inputProps={{ maxLength: 255 }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setLostDialog(false)} disabled={stageMutation.isPending}>
            Cancel
          </Button>
          <Button
            variant="contained"
            color="error"
            onClick={confirmLost}
            disabled={stageMutation.isPending}
          >
            Mark as lost
          </Button>
        </DialogActions>
      </Dialog>

      <ConfirmDialog
        open={deleteDialog}
        title="Delete Deal"
        message={`Are you sure you want to delete "${deal.title}"? Its stage history is kept. This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};
