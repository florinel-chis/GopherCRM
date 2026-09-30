import React, { useState } from 'react';
import { Link as RouterLink, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Avatar,
  Box,
  Button,
  ButtonBase,
  CircularProgress,
  IconButton,
  Link,
  Menu,
  MenuItem,
  Paper,
  Skeleton,
  Stack,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  ExpandLess as ExpandLessIcon,
  ExpandMore as ExpandMoreIcon,
  MoreVert as MoreVertIcon,
} from '@mui/icons-material';
import { AxiosError } from 'axios';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { dealsApi, type ChangeDealStageData, type DealFilters } from '@/api/endpoints';
import {
  DEAL_STAGES,
  dealStageLabel,
  isClosedDealStage,
  type Deal,
  type DealStage,
  type PipelineStage,
} from '@/types';
import { dealStagePaletteColor } from './dealStageColors';
import { dealAccountName, formatCalendarDate, formatDealAmount, isPastCalendarDate } from './dealFormat';
import { DealViewToggle } from './DealViewToggle';
import { LostReasonDialog } from './LostReasonDialog';
import { invalidateDealQueries } from './dealQueries';

// The API caps a page at 100; a column shows the first page and links to the
// list for the rest.
const BOARD_COLUMN_LIMIT = 100;

// Open stages show the deals closing soonest first; closed stages the most
// recently closed first.
const boardColumnFilters = (stage: DealStage): DealFilters =>
  isClosedDealStage(stage)
    ? { stage, limit: BOARD_COLUMN_LIMIT, sort_by: 'closed_at', sort_order: 'desc' }
    : { stage, limit: BOARD_COLUMN_LIMIT, sort_by: 'expected_close_date', sort_order: 'asc' };

const serverMessage = (error: unknown): string | undefined => {
  if (error instanceof AxiosError) {
    const message = (error.response?.data as { message?: unknown } | undefined)?.message;
    return typeof message === 'string' && message !== '' ? message : undefined;
  }
  return undefined;
};

const ownerInitials = (deal: Deal): string => {
  const owner = deal.owner;
  if (!owner) {
    return '?';
  }
  const initials = `${owner.first_name?.charAt(0) ?? ''}${owner.last_name?.charAt(0) ?? ''}`.toUpperCase();
  return initials || owner.email?.charAt(0).toUpperCase() || '?';
};

const ownerName = (deal: Deal): string =>
  deal.owner ? `${deal.owner.first_name} ${deal.owner.last_name}`.trim() || deal.owner.email : `User #${deal.owner_id}`;

const dealCountText = (count: number): string => (count === 1 ? '1 deal' : `${count} deals`);

interface DealCardProps {
  deal: Deal;
  disabled: boolean;
  onMove: (deal: Deal, stage: DealStage) => void;
}

const DealCard: React.FC<DealCardProps> = ({ deal, disabled, onMove }) => {
  const navigate = useNavigate();
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const menuId = `deal-actions-${deal.id}`;
  const pastDue = !isClosedDealStage(deal.stage) && isPastCalendarDate(deal.expected_close_date);
  const account = dealAccountName(deal);

  const move = (stage: DealStage) => {
    setAnchorEl(null);
    onMove(deal, stage);
  };

  return (
    <Paper
      variant="outlined"
      data-testid="deal-board-card"
      data-deal-id={deal.id}
      sx={(theme) => ({
        p: 1.5,
        borderLeft: `4px solid ${dealStagePaletteColor(theme, deal.stage)}`,
      })}
    >
      <Box display="flex" alignItems="flex-start" gap={1}>
        <Box flex={1} minWidth={0}>
          <Link
            component={RouterLink}
            to={`/deals/${deal.id}`}
            underline="hover"
            color="inherit"
            variant="subtitle2"
            sx={{ display: 'block', wordBreak: 'break-word' }}
          >
            {deal.title}
          </Link>
          {account !== '—' && (
            <Typography variant="body2" color="text.secondary" noWrap>
              {account}
            </Typography>
          )}
        </Box>
        <IconButton
          size="small"
          aria-label={`Deal actions for ${deal.title}`}
          aria-controls={anchorEl ? menuId : undefined}
          aria-haspopup="menu"
          aria-expanded={anchorEl ? 'true' : undefined}
          disabled={disabled}
          onClick={(event) => setAnchorEl(event.currentTarget)}
        >
          <MoreVertIcon fontSize="small" />
        </IconButton>
        <Menu id={menuId} anchorEl={anchorEl} open={anchorEl !== null} onClose={() => setAnchorEl(null)}>
          <MenuItem
            onClick={() => {
              setAnchorEl(null);
              navigate(`/deals/${deal.id}`);
            }}
          >
            Open
          </MenuItem>
          {DEAL_STAGES.filter((stage) => stage.value !== deal.stage).map((stage) => (
            <MenuItem key={stage.value} onClick={() => move(stage.value)}>
              Move to {stage.label}
            </MenuItem>
          ))}
        </Menu>
      </Box>
      <Box display="flex" alignItems="center" justifyContent="space-between" mt={1} gap={1}>
        <Typography variant="body2" fontWeight={500} data-testid="deal-board-card-amount">
          {formatDealAmount(deal.amount_cents, deal.currency)}
        </Typography>
        <Box display="flex" alignItems="center" gap={1}>
          <Typography
            variant="caption"
            color={pastDue ? 'error' : 'text.secondary'}
            data-testid={pastDue ? 'deal-past-due' : undefined}
          >
            {deal.expected_close_date ? formatCalendarDate(deal.expected_close_date, 'MMM dd') : ''}
          </Typography>
          <Avatar
            role="img"
            aria-label={`Owner ${ownerName(deal)}`}
            title={ownerName(deal)}
            sx={{ width: 24, height: 24, fontSize: 11 }}
          >
            {ownerInitials(deal)}
          </Avatar>
        </Box>
      </Box>
    </Paper>
  );
};

interface BoardColumnProps {
  stage: DealStage;
  summary: PipelineStage | undefined;
  summaryLoading: boolean;
  summaryError: boolean;
  expanded: boolean;
  onToggle?: () => void;
  movingDealId: number | null;
  onMove: (deal: Deal, stage: DealStage) => void;
}

const BoardColumn: React.FC<BoardColumnProps> = ({
  stage,
  summary,
  summaryLoading,
  summaryError,
  expanded,
  onToggle,
  movingDealId,
  onMove,
}) => {
  const closed = isClosedDealStage(stage);
  const labelId = `deal-board-${stage}-label`;
  const bodyId = `deal-board-${stage}-body`;
  const filters = boardColumnFilters(stage);

  const { data, isLoading, isError } = useQuery({
    queryKey: ['deals', filters],
    queryFn: () => dealsApi.getDeals(filters),
    enabled: expanded,
  });

  const deals = data?.deals ?? [];
  const more = data ? Math.max(data.total - deals.length, 0) : 0;

  const label = (
    <Typography component="span" variant="subtitle1" fontWeight={600} id={labelId}>
      {dealStageLabel(stage)}
    </Typography>
  );

  return (
    <Paper
      component="section"
      aria-labelledby={labelId}
      data-testid={`deal-board-column-${stage}`}
      sx={(theme) => ({
        flex: '0 0 280px',
        display: 'flex',
        flexDirection: 'column',
        bgcolor: 'grey.50',
        borderTop: `4px solid ${dealStagePaletteColor(theme, stage)}`,
      })}
    >
      <Box p={1.5} data-testid="deal-board-column-header">
        <Typography component="h2" variant="subtitle1" sx={{ m: 0 }}>
          {closed ? (
            <ButtonBase
              onClick={onToggle}
              aria-expanded={expanded}
              aria-controls={bodyId}
              sx={{ width: '100%', justifyContent: 'space-between', borderRadius: 1, px: 0.5, py: 0.25 }}
            >
              {label}
              {expanded ? <ExpandLessIcon fontSize="small" /> : <ExpandMoreIcon fontSize="small" />}
            </ButtonBase>
          ) : (
            label
          )}
        </Typography>
        {summaryLoading ? (
          <Skeleton width={80} />
        ) : summaryError ? null : (
          <>
            <Typography variant="body2" color="text.secondary" data-testid="deal-board-count">
              {dealCountText(summary?.count ?? 0)}
            </Typography>
            {(summary?.totals ?? []).map((total) => (
              <Box key={total.currency} data-testid="deal-board-total" data-currency={total.currency}>
                <Typography variant="body2" fontWeight={500}>
                  {formatDealAmount(total.amount_cents, total.currency)}
                </Typography>
                {!closed && (
                  <Typography variant="caption" color="text.secondary" component="div">
                    Weighted {formatDealAmount(total.weighted_cents, total.currency)}
                  </Typography>
                )}
              </Box>
            ))}
          </>
        )}
      </Box>

      <Box id={bodyId} px={1.5} pb={1.5} hidden={!expanded}>
        {expanded && (
          <Stack spacing={1}>
            {isLoading && (
              <Box display="flex" justifyContent="center" py={2}>
                <CircularProgress size={24} aria-label={`Loading ${dealStageLabel(stage)} deals`} />
              </Box>
            )}
            {isError && (
              <Typography variant="body2" color="error">
                Could not load these deals
              </Typography>
            )}
            {data && deals.length === 0 && (
              <Typography variant="body2" color="text.secondary">
                No deals
              </Typography>
            )}
            {deals.map((deal) => (
              <DealCard key={deal.id} deal={deal} disabled={movingDealId === deal.id} onMove={onMove} />
            ))}
            {more > 0 && (
              <Link component={RouterLink} to={`/deals?stage=${stage}`} variant="body2">
                +{more} more in the list
              </Link>
            )}
          </Stack>
        )}
      </Box>
    </Paper>
  );
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();
  const canWrite = user?.role === 'admin' || user?.role === 'sales';

  // Won and lost start collapsed: their headers still show count and totals.
  const [expandedClosed, setExpandedClosed] = useState<Record<'won' | 'lost', boolean>>({
    won: false,
    lost: false,
  });
  const [lostTarget, setLostTarget] = useState<Deal | null>(null);

  // Under the ['deals'] prefix so every deal mutation elsewhere refreshes it.
  const {
    data: pipeline,
    isLoading: pipelineLoading,
    isError: pipelineError,
  } = useQuery({
    queryKey: ['deals', 'pipeline'],
    queryFn: () => dealsApi.getPipeline(),
  });

  const moveMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: ChangeDealStageData }) => dealsApi.changeStage(id, data),
    onSuccess: (updated) => {
      showSuccess(`Moved "${updated.title}" to ${dealStageLabel(updated.stage)}`);
      setLostTarget(null);
      // The card leaves one column and joins another, both headers change,
      // and the dashboard widgets and the detail page read the same deals.
      invalidateDealQueries(queryClient);
      queryClient.invalidateQueries({ queryKey: ['deal'] });
    },
    onError: (error) => {
      showError(serverMessage(error) ?? 'Failed to move the deal');
    },
  });

  const handleMove = (deal: Deal, stage: DealStage) => {
    if (stage === deal.stage) {
      return;
    }
    if (stage === 'lost') {
      setLostTarget(deal);
      return;
    }
    moveMutation.mutate({ id: deal.id, data: { stage } });
  };

  const movingDealId = moveMutation.isPending ? (moveMutation.variables?.id ?? null) : null;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3} flexWrap="wrap" gap={2}>
        <Typography variant="h4">Deal pipeline</Typography>
        <Box display="flex" gap={2} alignItems="center">
          <DealViewToggle value="board" />
          {canWrite && (
            <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/deals/new')}>
              New Deal
            </Button>
          )}
        </Box>
      </Box>

      {pipelineError && (
        <Typography color="error" role="alert" mb={2}>
          Pipeline could not be loaded
        </Typography>
      )}

      <Box display="flex" gap={2} alignItems="flex-start" sx={{ overflowX: 'auto', pb: 1 }}>
        {DEAL_STAGES.map(({ value: stage }) => {
          const closed = stage === 'won' || stage === 'lost';
          return (
            <BoardColumn
              key={stage}
              stage={stage}
              summary={pipeline?.stages.find((candidate) => candidate.stage === stage)}
              summaryLoading={pipelineLoading}
              summaryError={pipelineError}
              expanded={closed ? expandedClosed[stage] : true}
              onToggle={
                closed
                  ? () => setExpandedClosed((previous) => ({ ...previous, [stage]: !previous[stage] }))
                  : undefined
              }
              movingDealId={movingDealId}
              onMove={handleMove}
            />
          );
        })}
      </Box>

      <LostReasonDialog
        open={lostTarget !== null}
        initialReason={lostTarget?.lost_reason ?? ''}
        pending={moveMutation.isPending}
        onCancel={() => setLostTarget(null)}
        onConfirm={(reason) => {
          if (lostTarget) {
            moveMutation.mutate({ id: lostTarget.id, data: { stage: 'lost', lost_reason: reason } });
          }
        }}
      />
    </Box>
  );
};
