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
  Stack,
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
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  PolarAngleAxis,
  RadialBar,
  RadialBarChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { aeoApi } from '@/api/endpoints/aeo';
import type { AEODashboard as AEODashboardData } from '@/api/endpoints/aeo';

const RANGE_OPTIONS = [7, 30, 90] as const;

// Distinct hues for the per-provider and per-competitor series. Long enough
// for the six engines plus the twenty competitors the profile allows; the
// index wraps rather than repeating adjacent colours.
const SERIES_COLORS = [
  '#1976d2',
  '#2e7d32',
  '#ed6c02',
  '#9c27b0',
  '#0288d1',
  '#d32f2f',
  '#5d4037',
  '#00838f',
  '#7b1fa2',
  '#558b2f',
];

const OVERALL_COLOR = '#212121';
const BRAND_COLOR = '#1976d2';

const seriesColor = (index: number) => SERIES_COLORS[index % SERIES_COLORS.length];

// Every rate the API returns is already 0..100 with one decimal; formatting
// here only guards against nulls in partially populated payloads.
const formatPercent = (value: number | undefined | null) => `${(value ?? 0).toFixed(1)}%`;

// "YYYY-MM-DD" → "MM-DD"; the year is constant across a 90-day window.
const shortDay = (day: string) => (day.length === 10 ? day.slice(5) : day);

const formatTimestamp = (value?: string) =>
  value ? new Date(value).toLocaleString() : '—';

interface MetricTileProps {
  title: string;
  value: string;
  caption?: string;
}

const MetricTile: React.FC<MetricTileProps> = ({ title, value, caption }) => (
  <Card sx={{ flex: '1 1 180px', minWidth: 180 }}>
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

interface TimelineRow {
  day: string;
  label: string;
  [series: string]: string | number;
}

// Turns the sparse per-day maps the API returns into dense rows so recharts
// draws a continuous line: a day with no answers for a provider is a 0, not
// a gap.
const buildRows = (
  points: Array<{ day: string; values: Record<string, number> | undefined }>,
  keys: string[],
  extra?: (index: number) => Record<string, number>
): TimelineRow[] =>
  points.map((point, index) => {
    const row: TimelineRow = { day: point.day, label: shortDay(point.day) };
    keys.forEach((key) => {
      row[key] = point.values?.[key] ?? 0;
    });
    Object.entries(extra?.(index) ?? {}).forEach(([key, value]) => {
      row[key] = value;
    });
    return row;
  });

export const Component: React.FC = () => {
  const [days, setDays] = useState<number>(30);

  const { data, isLoading, isError } = useQuery({
    queryKey: ['aeo', 'dashboard', days],
    queryFn: () => aeoApi.getDashboard(days),
  });

  const dashboard: AEODashboardData | undefined = data;

  // Provider keys come from both the summary and the timeline: a provider
  // that answered only on one day still deserves a line.
  const providerKeys = useMemo(() => {
    const keys = new Set<string>();
    (dashboard?.by_provider ?? []).forEach((entry) => keys.add(entry.provider));
    (dashboard?.timeline ?? []).forEach((point) =>
      Object.keys(point.by_provider ?? {}).forEach((key) => keys.add(key))
    );
    return Array.from(keys).sort();
  }, [dashboard]);

  const companyKeys = useMemo(() => {
    const keys = new Set<string>();
    (dashboard?.share_of_voice ?? []).forEach((entry) => keys.add(entry.company));
    (dashboard?.competitor_timeline ?? []).forEach((point) =>
      Object.keys(point.by_company ?? {}).forEach((key) => keys.add(key))
    );
    return Array.from(keys).sort();
  }, [dashboard]);

  const providerRows = useMemo(() => {
    const timeline = dashboard?.timeline ?? [];
    return buildRows(
      timeline.map((point) => ({ day: point.day, values: point.by_provider })),
      providerKeys,
      (index) => ({ overall: timeline[index]?.overall ?? 0 })
    );
  }, [dashboard, providerKeys]);

  const competitorRows = useMemo(
    () =>
      buildRows(
        (dashboard?.competitor_timeline ?? []).map((point) => ({
          day: point.day,
          values: point.by_company,
        })),
        companyKeys
      ),
    [dashboard, companyKeys]
  );

  const gaugeData = useMemo(
    () => [{ name: 'Visibility', value: dashboard?.visibility ?? 0, fill: BRAND_COLOR }],
    [dashboard]
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
      <Typography variant="h4">AEO Dashboard</Typography>
      {rangeSelector}
    </Box>
  );

  if (isLoading) {
    return (
      <Box>
        {header}
        <Box display="flex" justifyContent="center" py={6}>
          <CircularProgress aria-label="Loading AEO dashboard" />
        </Box>
      </Box>
    );
  }

  if (isError || !dashboard) {
    return (
      <Box>
        {header}
        <Alert severity="error">Failed to load the AEO dashboard. Please try again.</Alert>
      </Box>
    );
  }

  if (dashboard.total_answers === 0) {
    return (
      <Box>
        {header}
        <Paper sx={{ p: 4 }}>
          <Typography variant="h6" gutterBottom>
            No AEO data yet
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Add tracked prompts and start a run to see brand visibility across the answer
            engines.
          </Typography>
        </Paper>
      </Box>
    );
  }

  const answeredShare =
    dashboard.total_answers > 0
      ? (dashboard.brand_mentions / dashboard.total_answers) * 100
      : 0;

  return (
    <Box>
      {header}

      <Typography variant="body2" color="text.secondary" mb={2}>
        {dashboard.from} to {dashboard.to} ({dashboard.days} days) · last run{' '}
        {formatTimestamp(dashboard.last_run_at)}
      </Typography>

      <Box display="flex" flexWrap="wrap" gap={3} mb={3}>
        <Paper sx={{ p: 2, flex: '1 1 320px', minWidth: 300 }}>
          <Typography variant="h6" gutterBottom>
            Brand visibility
          </Typography>
          <Box position="relative" data-testid="aeo-visibility-gauge">
            <ResponsiveContainer width="100%" height={300}>
              <RadialBarChart
                data={gaugeData}
                innerRadius="72%"
                outerRadius="100%"
                startAngle={90}
                endAngle={-270}
              >
                <PolarAngleAxis type="number" domain={[0, 100]} angleAxisId={0} tick={false} />
                <RadialBar background dataKey="value" angleAxisId={0} cornerRadius={8} />
              </RadialBarChart>
            </ResponsiveContainer>
            <Box
              position="absolute"
              top={0}
              left={0}
              right={0}
              bottom={0}
              display="flex"
              flexDirection="column"
              alignItems="center"
              justifyContent="center"
              sx={{ pointerEvents: 'none' }}
            >
              <Typography variant="h3" component="div">
                {formatPercent(dashboard.visibility)}
              </Typography>
              <Typography variant="body2" color="text.secondary">
                of answers mention the brand
              </Typography>
            </Box>
          </Box>
        </Paper>

        <Box
          sx={{ flex: '2 1 420px', minWidth: 300, display: 'flex', flexWrap: 'wrap', gap: 2 }}
        >
          <MetricTile
            title="Answers analysed"
            value={String(dashboard.total_answers)}
            caption={`${dashboard.failed_answers} failed`}
          />
          <MetricTile
            title="Brand mentions"
            value={String(dashboard.brand_mentions)}
            caption={`${formatPercent(answeredShare)} of answers`}
          />
          <MetricTile title="Engines" value={String(dashboard.by_provider.length)} />
          {dashboard.by_provider.map((provider) => (
            <MetricTile
              key={provider.provider}
              title={provider.provider}
              value={formatPercent(provider.visibility)}
              caption={`${provider.mentions}/${provider.answers} answers`}
            />
          ))}
        </Box>
      </Box>

      <Paper sx={{ p: 2, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Visibility over time
        </Typography>
        <Box data-testid="aeo-provider-timeline">
          <ResponsiveContainer width="100%" height={300}>
            <LineChart data={providerRows}>
              <CartesianGrid strokeDasharray="3 3" />
              <XAxis dataKey="label" tick={{ fontSize: 12 }} />
              <YAxis domain={[0, 100]} unit="%" tick={{ fontSize: 12 }} />
              <Tooltip formatter={(value) => `${Number(value).toFixed(1)}%`} />
              <Legend />
              <Line
                type="monotone"
                dataKey="overall"
                name="Overall"
                stroke={OVERALL_COLOR}
                strokeWidth={2}
                dot={false}
              />
              {providerKeys.map((provider, index) => (
                <Line
                  key={provider}
                  type="monotone"
                  dataKey={provider}
                  name={provider}
                  stroke={seriesColor(index)}
                  strokeWidth={1.5}
                  dot={false}
                />
              ))}
            </LineChart>
          </ResponsiveContainer>
        </Box>
      </Paper>

      <Paper sx={{ mb: 3 }}>
        <Box p={2} pb={0}>
          <Typography variant="h6" gutterBottom>
            Share of voice
          </Typography>
        </Box>
        <TableContainer data-testid="aeo-share-of-voice">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Company</TableCell>
                <TableCell align="right">Mentions</TableCell>
                <TableCell align="right">Share</TableCell>
                <TableCell align="right">Visibility</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {dashboard.share_of_voice.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4}>
                    <Typography variant="body2" color="text.secondary">
                      No mentions recorded in this range.
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                dashboard.share_of_voice.map((entry) => (
                  <TableRow key={entry.company} hover>
                    <TableCell>
                      <Stack direction="row" spacing={1} alignItems="center">
                        <Typography variant="body2">{entry.company}</Typography>
                        {entry.is_brand && (
                          <Chip size="small" color="primary" label="Your brand" />
                        )}
                      </Stack>
                    </TableCell>
                    <TableCell align="right">{entry.mentions}</TableCell>
                    <TableCell align="right">{formatPercent(entry.share)}</TableCell>
                    <TableCell align="right">{formatPercent(entry.visibility)}</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </TableContainer>
      </Paper>

      <Paper sx={{ p: 2 }}>
        <Typography variant="h6" gutterBottom>
          Competitor visibility over time
        </Typography>
        {companyKeys.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            No competitors are configured yet.
          </Typography>
        ) : (
          <Box data-testid="aeo-competitor-timeline">
            <ResponsiveContainer width="100%" height={300}>
              <LineChart data={competitorRows}>
                <CartesianGrid strokeDasharray="3 3" />
                <XAxis dataKey="label" tick={{ fontSize: 12 }} />
                <YAxis domain={[0, 100]} unit="%" tick={{ fontSize: 12 }} />
                <Tooltip formatter={(value) => `${Number(value).toFixed(1)}%`} />
                <Legend />
                {companyKeys.map((company, index) => (
                  <Line
                    key={company}
                    type="monotone"
                    dataKey={company}
                    name={company}
                    stroke={seriesColor(index)}
                    strokeWidth={1.5}
                    dot={false}
                  />
                ))}
              </LineChart>
            </ResponsiveContainer>
          </Box>
        )}
      </Paper>
    </Box>
  );
};

Component.displayName = 'AEODashboard';
