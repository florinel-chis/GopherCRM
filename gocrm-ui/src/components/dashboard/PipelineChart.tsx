import React from 'react';
import { Link as RouterLink } from 'react-router-dom';
import {
  Box,
  Link,
  Paper,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { useTheme } from '@mui/material/styles';
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import { dealStageLabel, isClosedDealStage, type PipelineStage } from '@/types';
import { formatDealAmount } from '@/pages/deals/dealFormat';

export interface PipelineChartProps {
  stages: PipelineStage[] | undefined;
  /** The currency the bars are drawn in; other currencies go in a table. */
  defaultCurrency: string;
  isLoading: boolean;
}

const amountIn = (stage: PipelineStage, currency: string): number =>
  stage.totals.find((total) => total.currency === currency)?.amount_cents ?? 0;

/**
 * "Pipeline by stage": the open stages as bars in the default currency.
 * Amounts are never converted or summed across currencies, so every other
 * currency present is listed in a small table under the chart.
 */
export const PipelineChart: React.FC<PipelineChartProps> = ({ stages, defaultCurrency, isLoading }) => {
  const theme = useTheme();
  const openStages = (stages ?? []).filter((stage) => !isClosedDealStage(stage.stage));
  const openCount = openStages.reduce((sum, stage) => sum + stage.count, 0);
  const otherCurrencies = Array.from(
    new Set(
      openStages.flatMap((stage) =>
        stage.totals.map((total) => total.currency).filter((currency) => currency !== defaultCurrency)
      )
    )
  ).sort();
  const defaultTotal = openStages.reduce((sum, stage) => sum + amountIn(stage, defaultCurrency), 0);
  const chartData = openStages.map((stage) => ({
    name: dealStageLabel(stage.stage),
    amount: amountIn(stage, defaultCurrency) / 100,
  }));

  return (
    <Paper sx={{ p: 2, height: '100%' }} component="section" aria-labelledby="pipeline-chart-heading">
      <Box display="flex" justifyContent="space-between" alignItems="baseline" mb={1}>
        <Typography variant="h6" id="pipeline-chart-heading">
          Pipeline by stage
        </Typography>
        <Link component={RouterLink} to="/deals/board" variant="body2">
          Open the board
        </Link>
      </Box>
      {isLoading ? (
        <Skeleton variant="rectangular" height={240} />
      ) : openCount === 0 ? (
        <Typography color="text.secondary">No open deals yet</Typography>
      ) : (
        <>
          <Typography variant="body2" color="text.secondary" data-testid="pipeline-default-total">
            Open pipeline in {defaultCurrency}: {formatDealAmount(defaultTotal, defaultCurrency)}
          </Typography>
          <ResponsiveContainer width="100%" height={240}>
            <BarChart data={chartData} margin={{ top: 16, right: 16, bottom: 0, left: 8 }}>
              <CartesianGrid strokeDasharray="3 3" />
              <XAxis dataKey="name" />
              <YAxis
                tickFormatter={(value: number) =>
                  new Intl.NumberFormat(undefined, { notation: 'compact' }).format(value)
                }
              />
              <Tooltip
                formatter={(value) => [
                  formatDealAmount(Math.round(Number(value) * 100), defaultCurrency),
                  defaultCurrency,
                ]}
              />
              <Bar dataKey="amount" name={defaultCurrency} fill={theme.palette.primary.main} />
            </BarChart>
          </ResponsiveContainer>
          {otherCurrencies.length > 0 && (
            <Box mt={1}>
              <Typography variant="subtitle2" id="pipeline-other-currencies-heading">
                Other currencies
              </Typography>
              <Table size="small" aria-labelledby="pipeline-other-currencies-heading">
                <TableHead>
                  <TableRow>
                    <TableCell>Currency</TableCell>
                    {openStages.map((stage) => (
                      <TableCell key={stage.stage} align="right">
                        {dealStageLabel(stage.stage)}
                      </TableCell>
                    ))}
                  </TableRow>
                </TableHead>
                <TableBody>
                  {otherCurrencies.map((currency) => (
                    <TableRow key={currency}>
                      <TableCell component="th" scope="row">
                        {currency}
                      </TableCell>
                      {openStages.map((stage) => (
                        <TableCell key={stage.stage} align="right">
                          {formatDealAmount(amountIn(stage, currency), currency)}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </Box>
          )}
        </>
      )}
    </Paper>
  );
};
