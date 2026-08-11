import React, { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  ToggleButton,
  ToggleButtonGroup,
  Typography,
} from '@mui/material';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { aeoApi } from '@/api/endpoints/aeo';
import type { AEOCitationsReport } from '@/api/endpoints/aeo';

const RANGE_OPTIONS = [7, 30, 90] as const;

const BRAND_COLOR = '#1976d2';
const COMPETITOR_COLOR = '#90a4ae';

const formatPercent = (value: number | undefined | null) => `${(value ?? 0).toFixed(1)}%`;

interface MetricTileProps {
  title: string;
  value: string;
  caption?: string;
}

const MetricTile: React.FC<MetricTileProps> = ({ title, value, caption }) => (
  <Card sx={{ flex: '1 1 200px', minWidth: 200 }}>
    <CardContent>
      <Typography variant="body2" color="text.secondary" gutterBottom>
        {title}
      </Typography>
      <Typography variant="h5" component="div">
        {value}
      </Typography>
      {caption && (
        <Typography variant="caption" color="text.secondary">
          {caption}
        </Typography>
      )}
    </CardContent>
  </Card>
);

interface CompanyBarChartProps {
  testId: string;
  data: Array<{ company: string; is_brand: boolean; value: number }>;
  label: string;
  color: string;
}

// Both comparisons share a shape: one bar per company, the brand's bar
// coloured differently so it reads against the competitor set at a glance.
const CompanyBarChart: React.FC<CompanyBarChartProps> = ({ testId, data, label, color }) => (
  <Box data-testid={testId}>
    <ResponsiveContainer width="100%" height={300}>
      <BarChart data={data}>
        <CartesianGrid strokeDasharray="3 3" />
        <XAxis dataKey="company" tick={{ fontSize: 12 }} />
        <YAxis domain={[0, 100]} unit="%" tick={{ fontSize: 12 }} />
        <Tooltip formatter={(value) => `${Number(value).toFixed(1)}%`} />
        <Bar dataKey="value" name={label} fill={color}>
          {data.map((entry) => (
            <Cell
              key={entry.company}
              fill={entry.is_brand ? BRAND_COLOR : COMPETITOR_COLOR}
            />
          ))}
        </Bar>
      </BarChart>
    </ResponsiveContainer>
  </Box>
);

export const Component: React.FC = () => {
  const [days, setDays] = useState<number>(30);

  const { data, isLoading, isError } = useQuery({
    queryKey: ['aeo', 'citations', days],
    queryFn: () => aeoApi.getCitations(days),
  });

  const report: AEOCitationsReport | undefined = data;

  const citationRateData = useMemo(
    () =>
      (report?.by_company ?? []).map((entry) => ({
        company: entry.company,
        is_brand: entry.is_brand,
        value: entry.citation_rate,
      })),
    [report]
  );

  const brandMentionRateData = useMemo(
    () =>
      (report?.by_company ?? []).map((entry) => ({
        company: entry.company,
        is_brand: entry.is_brand,
        value: entry.brand_mention_rate,
      })),
    [report]
  );

  const rangeSelector = (
    <ToggleButtonGroup
      size="small"
      exclusive
      value={days}
      aria-label="Date range"
      onChange={(_event, value: number | null) => {
        if (value !== null) {
          setDays(value);
        }
      }}
    >
      {RANGE_OPTIONS.map((option) => (
        <ToggleButton key={option} value={option} aria-label={`Last ${option} days`}>
          {option}d
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );

  const header = (
    <Box display="flex" justifyContent="space-between" alignItems="center" mb={3} gap={2}>
      <Typography variant="h4">AEO Citations</Typography>
      {rangeSelector}
    </Box>
  );

  if (isLoading) {
    return (
      <Box>
        {header}
        <Box display="flex" justifyContent="center" py={6}>
          <CircularProgress aria-label="Loading AEO citations" />
        </Box>
      </Box>
    );
  }

  if (isError || !report) {
    return (
      <Box>
        {header}
        <Alert severity="error">Failed to load the AEO citations report. Please try again.</Alert>
      </Box>
    );
  }

  if (report.total_answers === 0) {
    return (
      <Box>
        {header}
        <Paper sx={{ p: 4 }}>
          <Typography variant="h6" gutterBottom>
            No AEO data yet
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Add tracked prompts and start a run to see which domains the answer engines cite.
          </Typography>
        </Paper>
      </Box>
    );
  }

  const answersWithCitationsShare =
    report.total_answers > 0
      ? (report.answers_with_citations / report.total_answers) * 100
      : 0;

  return (
    <Box>
      {header}

      <Typography variant="body2" color="text.secondary" mb={2}>
        {report.from} to {report.to}
      </Typography>

      <Box display="flex" flexWrap="wrap" gap={2} mb={3}>
        <MetricTile
          title="Owned-domain citation rate"
          value={formatPercent(report.owned_citation_rate)}
          caption="answers citing a domain you own"
        />
        <MetricTile title="Total citations" value={String(report.total_citations)} />
        <MetricTile
          title="Answers with citations"
          value={String(report.answers_with_citations)}
          caption={`${formatPercent(answersWithCitationsShare)} of ${report.total_answers} answers`}
        />
      </Box>

      {report.total_citations === 0 && (
        <Alert severity="info" sx={{ mb: 3 }}>
          No citations were extracted in this range.
        </Alert>
      )}

      <Box display="flex" flexWrap="wrap" gap={3} mb={3}>
        <Paper sx={{ p: 2, flex: '1 1 420px', minWidth: 320 }}>
          <Typography variant="h6" gutterBottom>
            Citation rate by company
          </Typography>
          <Typography variant="body2" color="text.secondary" gutterBottom>
            Share of answers citing each company&apos;s domains.
          </Typography>
          {citationRateData.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              No companies are configured yet.
            </Typography>
          ) : (
            <CompanyBarChart
              testId="aeo-citation-rate-chart"
              data={citationRateData}
              label="Citation rate"
              color={COMPETITOR_COLOR}
            />
          )}
        </Paper>

        <Paper sx={{ p: 2, flex: '1 1 420px', minWidth: 320 }}>
          <Typography variant="h6" gutterBottom>
            Citations with brand mention
          </Typography>
          <Typography variant="body2" color="text.secondary" gutterBottom>
            Share of each company&apos;s citations appearing in answers that mention your brand.
          </Typography>
          {brandMentionRateData.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              No companies are configured yet.
            </Typography>
          ) : (
            <CompanyBarChart
              testId="aeo-brand-mention-rate-chart"
              data={brandMentionRateData}
              label="Brand-mention rate"
              color={COMPETITOR_COLOR}
            />
          )}
        </Paper>
      </Box>

      <Paper>
        <Box p={2} pb={0}>
          <Typography variant="h6" gutterBottom>
            Top cited domains
          </Typography>
        </Box>
        <TableContainer>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Domain</TableCell>
                <TableCell>Company</TableCell>
                <TableCell align="right">Citations</TableCell>
                <TableCell align="right">Citation rate</TableCell>
                <TableCell align="right">With brand mention</TableCell>
                <TableCell align="right">Brand-mention rate</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {report.top_domains.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6}>
                    <Typography variant="body2" color="text.secondary">
                      No cited domains in this range.
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                report.top_domains.map((domain) => (
                  <TableRow key={domain.domain} hover>
                    <TableCell>
                      <Box display="flex" alignItems="center" gap={1}>
                        <Typography variant="body2">{domain.domain}</Typography>
                        {domain.is_owned && (
                          <Chip size="small" color="primary" label="Owned" />
                        )}
                      </Box>
                    </TableCell>
                    <TableCell>{domain.company || '—'}</TableCell>
                    <TableCell align="right">{domain.citations}</TableCell>
                    <TableCell align="right">{formatPercent(domain.citation_rate)}</TableCell>
                    <TableCell align="right">{domain.with_brand_mention}</TableCell>
                    <TableCell align="right">
                      {formatPercent(domain.brand_mention_rate)}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </TableContainer>
      </Paper>
    </Box>
  );
};

Component.displayName = 'AEOCitations';
