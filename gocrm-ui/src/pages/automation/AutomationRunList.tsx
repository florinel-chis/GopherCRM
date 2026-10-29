import React, { useCallback, useMemo, useState } from 'react';
import { useNavigate, useSearchParams, Link as RouterLink } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  FormControl,
  InputLabel,
  Link,
  MenuItem,
  Paper,
  Select,
  TextField,
  Typography,
} from '@mui/material';
import { DataTable, type Column } from '@/components/DataTable';
import { automationApi, type AutomationRunFilters, type AutomationRunSortColumn } from '@/api/endpoints';
import { useAuth } from '@/hooks/useAuth';
import {
  AUTOMATION_ENTITY_TYPES,
  AUTOMATION_RUN_STATUSES,
  type AutomationEntityType,
  type AutomationRule,
  type AutomationRun,
  type AutomationRunStatus,
} from '@/types';
import { formatDate } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';
import { AUTOMATION_DATETIME, entityLabel, entityPath, runStatusLabel } from './automationFormat';
import { automationKeys } from './automationQueries';
import { RunStatusChip } from './AutomationChips';
import { RuleFilterAutocomplete } from './RuleFilterAutocomplete';

// Columns the API accepts in sort_by (its allowlist); the others render
// without a sort header so a click can never produce a rejected request.
const SORTABLE_COLUMNS = new Set<string>(['id', 'status', 'resume_at', 'created_at', 'updated_at']);

const ENTITY_LABELS: Record<AutomationEntityType, string> = { lead: 'Lead', deal: 'Deal' };

// The filters live in the URL, so a link (the nav entry, a record section's
// "View all") always shows exactly what it names, even while mounted.
interface UrlFilters {
  status: AutomationRunStatus | '';
  rule_id?: number;
  entity_type: AutomationEntityType | '';
  entity_id?: number;
}

// Paging and sorting are local; the page goes back to 1 whenever the URL
// filters change.
interface PagingState {
  filterKey: string;
  page: number;
  limit: number;
  sort_by?: AutomationRunSortColumn;
  sort_order?: 'asc' | 'desc';
}

const parsePositiveInt = (text: string | null): number | undefined => {
  if (text === null || !/^\d+$/.test(text.trim())) {
    return undefined;
  }
  const value = Number(text.trim());
  return Number.isSafeInteger(value) && value > 0 ? value : undefined;
};

const isRunStatus = (value: string | null): value is AutomationRunStatus =>
  (AUTOMATION_RUN_STATUSES as readonly string[]).includes(value ?? '');

const isEntityType = (value: string | null): value is AutomationEntityType =>
  (AUTOMATION_ENTITY_TYPES as readonly string[]).includes(value ?? '');

const filtersFromParams = (params: URLSearchParams): UrlFilters => {
  const status = params.get('status');
  const entityType = params.get('entity_type');
  const entity_type = isEntityType(entityType) ? entityType : '';
  return {
    status: isRunStatus(status) ? status : '',
    rule_id: parsePositiveInt(params.get('rule_id')),
    entity_type,
    // A record id without its type would match leads and deals alike.
    entity_id: entity_type ? parsePositiveInt(params.get('entity_id')) : undefined,
  };
};

const filterKeyOf = (f: UrlFilters): string =>
  [f.status, f.rule_id ?? '', f.entity_type, f.entity_id ?? ''].join('|');

const idText = (value: number | undefined): string => (value === undefined ? '' : String(value));

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [searchParams, setSearchParams] = useSearchParams();

  // Admin and sales only (route guard); the API narrows sales to the leads and
  // deals it owns, inside the query.
  const urlFilters = useMemo(() => filtersFromParams(searchParams), [searchParams]);
  const filterKey = filterKeyOf(urlFilters);
  const [paging, setPaging] = useState<PagingState>({ filterKey, page: 1, limit: 10 });
  const [texts, setTexts] = useState({ filterKey, ruleId: idText(urlFilters.rule_id), recordId: idText(urlFilters.entity_id) });
  const [ruleNames, setRuleNames] = useState<ReadonlyMap<number, string>>(new Map());

  // A URL change made elsewhere (a link, back/forward) resets the page and
  // the two id fields during render, before anything is requested.
  if (paging.filterKey !== filterKey) {
    setPaging((prev) => ({ ...prev, filterKey, page: 1 }));
  }
  if (texts.filterKey !== filterKey) {
    setTexts({ filterKey, ruleId: idText(urlFilters.rule_id), recordId: idText(urlFilters.entity_id) });
  }
  const page = paging.filterKey === filterKey ? paging.page : 1;
  const ruleIdText = texts.ruleId;
  const recordIdText = texts.recordId;
  const setRuleIdText = (ruleId: string) => setTexts((prev) => ({ ...prev, ruleId }));
  const setRecordIdText = (recordId: string) => setTexts((prev) => ({ ...prev, recordId }));

  const filters: AutomationRunFilters = useMemo(
    () => ({
      offset: (page - 1) * paging.limit,
      limit: paging.limit,
      status: urlFilters.status || undefined,
      rule_id: urlFilters.rule_id,
      entity_type: urlFilters.entity_type || undefined,
      entity_id: urlFilters.entity_type ? urlFilters.entity_id : undefined,
      sort_by: paging.sort_by,
      sort_order: paging.sort_order,
    }),
    [page, paging.limit, paging.sort_by, paging.sort_order, urlFilters]
  );

  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey: automationKeys.runList(filters),
    queryFn: () => automationApi.listRuns(filters),
  });

  // Every filter change is written to the URL (the single source of the
  // filters); the page then goes back to 1 through the filter key.
  const updateFilters = (patch: Partial<UrlFilters>) => {
    const next: UrlFilters = { ...urlFilters, ...patch };
    if (!next.entity_type) {
      next.entity_id = undefined;
    }
    const params = new URLSearchParams(searchParams);
    const write = (key: string, value: string | number | undefined) => {
      if (value === undefined || value === '') {
        params.delete(key);
      } else {
        params.set(key, String(value));
      }
    };
    write('status', next.status);
    write('rule_id', next.rule_id);
    write('entity_type', next.entity_type);
    write('entity_id', next.entity_id);
    setSearchParams(params, { replace: true });
  };

  const applyRuleId = () => {
    const ruleId = parsePositiveInt(ruleIdText);
    if (ruleId !== urlFilters.rule_id) {
      updateFilters({ rule_id: ruleId });
    }
  };

  const applyRecordId = () => {
    const entityId = parsePositiveInt(recordIdText);
    if (entityId !== urlFilters.entity_id) {
      updateFilters({ entity_id: entityId });
    }
  };

  const handleRulesLoaded = useCallback((rules: AutomationRule[]) => {
    setRuleNames(new Map(rules.map((rule) => [rule.id, rule.name])));
  }, []);

  const columns: Column<AutomationRun>[] = useMemo(
    () => [
      {
        id: 'id',
        label: 'Run',
        minWidth: 80,
        // A named link per row: the accessible way to open a run.
        format: (_value: unknown, row: AutomationRun) => (
          <Link
            component={RouterLink}
            to={`/automation/runs/${row.id}`}
            aria-label={`Run #${row.id}`}
            onClick={(event: React.MouseEvent) => event.stopPropagation()}
          >
            #{row.id}
          </Link>
        ),
      },
      {
        id: 'status',
        label: 'Status',
        minWidth: 110,
        format: (value: AutomationRunStatus) => <RunStatusChip status={value} />,
      },
      {
        id: 'rule_id',
        label: 'Rule',
        minWidth: 180,
        sortable: false,
        // Runs never carry the rule; names come only from the rules an
        // admin's picker has already loaded.
        format: (_value: unknown, row: AutomationRun) =>
          `${row.rule?.name ?? ruleNames.get(row.rule_id) ?? `Rule #${row.rule_id}`} · v${row.rule_version}`,
      },
      {
        id: 'entity_id',
        label: 'Record',
        minWidth: 110,
        sortable: false,
        format: (_value: unknown, row: AutomationRun) => (
          <Link
            component={RouterLink}
            to={entityPath(row.entity_type, row.entity_id)}
            onClick={(event: React.MouseEvent) => event.stopPropagation()}
          >
            {entityLabel(row.entity_type, row.entity_id)}
          </Link>
        ),
      },
      {
        id: 'resume_at',
        label: 'Resumes',
        minWidth: 150,
        format: (value: string | undefined) => formatDate(value, AUTOMATION_DATETIME),
      },
      {
        id: 'created_at',
        label: 'Created',
        minWidth: 150,
        format: (value: string) => formatDate(value, AUTOMATION_DATETIME),
      },
      {
        id: 'updated_at',
        label: 'Updated',
        minWidth: 150,
        format: (value: string) => formatDate(value, AUTOMATION_DATETIME),
      },
    ],
    [ruleNames]
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    if (!SORTABLE_COLUMNS.has(field)) {
      return;
    }
    setPaging((prev) => ({ ...prev, sort_by: field as AutomationRunSortColumn, sort_order: order, page: 1 }));
  }, []);

  const handlePageChange = useCallback((page: number) => {
    setPaging((prev) => ({ ...prev, page: page + 1 }));
  }, []);

  const handleRowsPerPageChange = useCallback((rowsPerPage: number) => {
    setPaging((prev) => ({ ...prev, limit: rowsPerPage, page: 1 }));
  }, []);

  const onEnter = (apply: () => void) => (event: React.KeyboardEvent) => {
    if (event.key === 'Enter') {
      apply();
    }
  };

  const ruleIdInvalid = ruleIdText.trim() !== '' && parsePositiveInt(ruleIdText) === undefined;
  const recordIdInvalid = recordIdText.trim() !== '' && parsePositiveInt(recordIdText) === undefined;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Automation runs</Typography>
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="flex-start" flexWrap="wrap">
          <FormControl size="small" sx={{ minWidth: 170 }}>
            <InputLabel id="run-status-filter-label" shrink>
              Status
            </InputLabel>
            <Select
              labelId="run-status-filter-label"
              id="run-status-filter"
              value={urlFilters.status}
              onChange={(event) => updateFilters({ status: event.target.value as AutomationRunStatus | '' })}
              label="Status"
              displayEmpty
              notched
              renderValue={(value: AutomationRunStatus | '') => (value === '' ? 'All statuses' : runStatusLabel(value))}
            >
              <MenuItem value="">All statuses</MenuItem>
              {AUTOMATION_RUN_STATUSES.map((status) => (
                <MenuItem key={status} value={status}>
                  {runStatusLabel(status)}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          {isAdmin && (
            <RuleFilterAutocomplete
              value={urlFilters.rule_id}
              onChange={(ruleId) => {
                setRuleIdText(idText(ruleId));
                updateFilters({ rule_id: ruleId });
              }}
              onRulesLoaded={handleRulesLoaded}
            />
          )}

          {/* Every role may filter by id: it is applied inside the sales
              scope, and it reaches deleted rules the picker cannot list. */}
          <TextField
            size="small"
            label="Rule ID"
            type="number"
            value={ruleIdText}
            onChange={(event) => setRuleIdText(event.target.value)}
            onBlur={applyRuleId}
            onKeyDown={onEnter(applyRuleId)}
            error={ruleIdInvalid}
            helperText={ruleIdInvalid ? 'A positive whole number' : undefined}
            sx={{ width: 140 }}
            slotProps={{ htmlInput: { min: 1, step: 1 } }}
          />

          <FormControl size="small" sx={{ minWidth: 140 }}>
            <InputLabel id="run-entity-filter-label" shrink>
              Entity
            </InputLabel>
            <Select
              labelId="run-entity-filter-label"
              id="run-entity-filter"
              value={urlFilters.entity_type}
              onChange={(event) => {
                const entityType = event.target.value as AutomationEntityType | '';
                if (!entityType) {
                  setRecordIdText('');
                }
                updateFilters({ entity_type: entityType });
              }}
              label="Entity"
              displayEmpty
              notched
              renderValue={(value: AutomationEntityType | '') => (value === '' ? 'All' : ENTITY_LABELS[value])}
            >
              <MenuItem value="">All</MenuItem>
              {AUTOMATION_ENTITY_TYPES.map((type) => (
                <MenuItem key={type} value={type}>
                  {ENTITY_LABELS[type]}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <TextField
            size="small"
            label="Record ID"
            type="number"
            value={recordIdText}
            disabled={!urlFilters.entity_type}
            onChange={(event) => setRecordIdText(event.target.value)}
            onBlur={applyRecordId}
            onKeyDown={onEnter(applyRecordId)}
            error={recordIdInvalid}
            helperText={recordIdInvalid ? 'A positive whole number' : undefined}
            sx={{ width: 140 }}
            slotProps={{ htmlInput: { min: 1, step: 1 } }}
          />
        </Box>
      </Paper>

      {isError ? (
        <Alert
          severity="error"
          action={
            <Button color="inherit" size="small" onClick={() => void refetch()} disabled={isFetching}>
              Retry
            </Button>
          }
        >
          {getAPIError(error)?.message ?? 'The automation runs could not be loaded.'}
        </Alert>
      ) : (
        <>
          <DataTable
            columns={columns}
            data={data?.runs ?? []}
            totalCount={data?.total ?? 0}
            page={page - 1}
            rowsPerPage={paging.limit}
            loading={isLoading}
            onSort={handleSort}
            sortBy={paging.sort_by ?? ''}
            sortOrder={paging.sort_order ?? 'asc'}
            onPageChange={handlePageChange}
            onRowsPerPageChange={handleRowsPerPageChange}
            onRowClick={(run) => navigate(`/automation/runs/${run.id}`)}
          />
          {!isLoading && data?.runs.length === 0 && (
            <Typography color="text.secondary" sx={{ mt: 2 }}>
              No runs match these filters.
            </Typography>
          )}
        </>
      )}
    </Box>
  );
};
