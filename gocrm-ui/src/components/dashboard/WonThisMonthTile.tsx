import React from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { Avatar, Box, Card, CardContent, Link, Skeleton, Typography } from '@mui/material';
import { EmojiEvents as TrophyIcon } from '@mui/icons-material';
import type { WonThisMonth } from '@/types';
import { formatDealAmount } from '@/pages/deals/dealFormat';

export interface WonThisMonthTileProps {
  wonThisMonth: WonThisMonth | undefined;
  isLoading: boolean;
  /** The pipeline request failed: say so rather than show zero. */
  isError?: boolean;
}

/**
 * Deals won in the current calendar month (UTC, as the API buckets them):
 * the count and one total per currency, never summed across currencies.
 */
export const WonThisMonthTile: React.FC<WonThisMonthTileProps> = ({
  wonThisMonth,
  isLoading,
  isError = false,
}) => {
  if (isLoading) {
    return <Skeleton variant="rectangular" height={140} />;
  }
  const count = wonThisMonth?.count ?? 0;
  const totals = wonThisMonth?.totals ?? [];

  return (
    <Card component="section" aria-labelledby="won-this-month-heading" sx={{ height: '100%' }}>
      <CardContent>
        <Box display="flex" alignItems="flex-start" justifyContent="space-between">
          <Box>
            <Typography color="textSecondary" gutterBottom variant="body2" id="won-this-month-heading">
              Won this month
            </Typography>
            {isError ? (
              <Typography variant="body2" color="error" role="alert">
                Pipeline could not be loaded
              </Typography>
            ) : (
              <Typography variant="h4" component="div" data-testid="won-this-month-count">
                {count}
              </Typography>
            )}
            {isError ? null : count === 0 ? (
              <Typography variant="body2" color="text.secondary">
                No deals won yet this month
              </Typography>
            ) : (
              totals.map((total) => (
                <Typography
                  key={total.currency}
                  variant="body2"
                  data-testid="won-this-month-total"
                  data-currency={total.currency}
                >
                  {formatDealAmount(total.amount_cents, total.currency)}
                </Typography>
              ))
            )}
          </Box>
          <Avatar sx={{ bgcolor: 'success.main', width: 56, height: 56 }}>
            <TrophyIcon />
          </Avatar>
        </Box>
        <Link component={RouterLink} to="/deals/board" variant="body2" sx={{ display: 'inline-block', mt: 1 }}>
          View the board
        </Link>
      </CardContent>
    </Card>
  );
};
