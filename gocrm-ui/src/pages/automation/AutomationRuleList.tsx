import React, { useCallback, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Box, Button, IconButton, Switch, Tooltip, Typography } from '@mui/material';
import { Add as AddIcon, Delete as DeleteIcon, Science as DryRunIcon } from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { automationApi, type AutomationRuleFilters, type AutomationRuleSortColumn } from '@/api/endpoints';
import type { AutomationEntityType, AutomationRule } from '@/types';
import { formatDate } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';
import { AUTOMATION_DATETIME, describeTrigger } from './automationFormat';
import { afterRuleChanged, afterRuleDeleted, afterRuleEnabled, automationKeys } from './automationQueries';
import { DryRunDialog } from './DryRunDialog';

// Columns in the API's sort allowlist; Tools never sorts.
const SORTABLE_COLUMNS = new Set<string>(['name', 'entity_type', 'trigger_type', 'enabled', 'version', 'updated_at']);

const ENTITY_LABELS: Record<AutomationEntityType, string> = { lead: 'Lead', deal: 'Deal' };

interface ListState {
  page: number;
  limit: number;
  sort_by?: AutomationRuleSortColumn;
  sort_order?: 'asc' | 'desc';
}

const stop = (event: React.SyntheticEvent) => event.stopPropagation();

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();

  // Rules are admin-only on the API; the route guard keeps the other roles out.
  const [state, setState] = useState<ListState>({ page: 1, limit: 10 });
  const [disabling, setDisabling] = useState<AutomationRule | null>(null);
  const [deleting, setDeleting] = useState<AutomationRule | null>(null);
  const [dryRunRule, setDryRunRule] = useState<AutomationRule | null>(null);

  const filters: AutomationRuleFilters = useMemo(
    () => ({
      offset: (state.page - 1) * state.limit,
      limit: state.limit,
      sort_by: state.sort_by,
      sort_order: state.sort_order,
    }),
    [state],
  );

  const { data, isLoading } = useQuery({
    queryKey: automationKeys.ruleList(filters),
    queryFn: () => automationApi.listRules(filters),
  });

  const reportError = useCallback(
    (error: unknown, fallback: string) => showError(getAPIError(error)?.message || fallback),
    [showError],
  );

  const enableMutation = useMutation({
    mutationFn: (id: number) => automationApi.enableRule(id),
    onSuccess: async (rule) => {
      await afterRuleEnabled(queryClient, rule);
      showSuccess('Rule enabled');
    },
    onError: (error) => reportError(error, 'Failed to enable the rule'),
  });

  const disableMutation = useMutation({
    mutationFn: (id: number) => automationApi.disableRule(id),
    onSuccess: async (rule) => {
      await afterRuleChanged(queryClient, rule);
      showSuccess('Rule disabled');
    },
    onError: (error) => reportError(error, 'Failed to disable the rule'),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => automationApi.deleteRule(id),
    onSuccess: async (_result, id) => {
      await afterRuleDeleted(queryClient, id);
      showSuccess('Rule deleted');
    },
    onError: (error) => reportError(error, 'Failed to delete the rule'),
  });

  const { mutate: enableRule } = enableMutation;

  const columns: Column<AutomationRule>[] = useMemo(
    () => [
      { id: 'name', label: 'Name', minWidth: 200 },
      {
        id: 'entity_type',
        label: 'Entity',
        minWidth: 90,
        format: (value: AutomationEntityType) => ENTITY_LABELS[value] ?? value,
      },
      {
        id: 'trigger_type',
        label: 'Trigger',
        minWidth: 220,
        format: (_value: unknown, row: AutomationRule) => describeTrigger(row),
      },
      {
        id: 'enabled',
        label: 'Enabled',
        minWidth: 90,
        format: (_value: unknown, row: AutomationRule) => (
          <Switch
            checked={row.enabled}
            onClick={stop}
            onChange={(_event, checked) => {
              if (checked) {
                enableRule(row.id);
              } else {
                setDisabling(row);
              }
            }}
            slotProps={{ input: { 'aria-label': `${row.name} enabled` } }}
          />
        ),
      },
      { id: 'version', label: 'Version', minWidth: 80 },
      {
        id: 'updated_at',
        label: 'Updated',
        minWidth: 160,
        format: (value: string) => formatDate(value, AUTOMATION_DATETIME),
      },
      {
        id: 'tools',
        label: 'Tools',
        sortable: false,
        format: (_value: unknown, row: AutomationRule) => (
          <Box sx={{ display: 'flex', gap: 0.5 }}>
            <Tooltip title="Dry run">
              <IconButton
                size="small"
                aria-label="Dry run"
                onClick={(event) => {
                  event.stopPropagation();
                  setDryRunRule(row);
                }}
              >
                <DryRunIcon fontSize="small" />
              </IconButton>
            </Tooltip>
            <Tooltip title="Delete rule">
              <IconButton
                size="small"
                aria-label="Delete rule"
                onClick={(event) => {
                  event.stopPropagation();
                  setDeleting(row);
                }}
              >
                <DeleteIcon fontSize="small" />
              </IconButton>
            </Tooltip>
          </Box>
        ),
      },
    ],
    [enableRule],
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    if (!SORTABLE_COLUMNS.has(field)) {
      return;
    }
    setState((prev) => ({ ...prev, sort_by: field as AutomationRuleSortColumn, sort_order: order, page: 1 }));
  }, []);

  const handlePageChange = useCallback((page: number) => {
    setState((prev) => ({ ...prev, page: page + 1 }));
  }, []);

  const handleRowsPerPageChange = useCallback((rowsPerPage: number) => {
    setState((prev) => ({ ...prev, limit: rowsPerPage, page: 1 }));
  }, []);

  if (isLoading && !data) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Automation rules</Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/automation/rules/new')}>
          New rule
        </Button>
      </Box>

      <DataTable
        columns={columns}
        data={data?.rules || []}
        totalCount={data?.total || 0}
        page={state.page - 1}
        rowsPerPage={state.limit}
        loading={isLoading}
        onSort={handleSort}
        sortBy={state.sort_by ?? ''}
        sortOrder={state.sort_order ?? 'asc'}
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(rule) => navigate(`/automation/rules/${rule.id}/edit`)}
      />

      <ConfirmDialog
        open={disabling !== null}
        title="Disable rule"
        message={`Disabling "${disabling?.name ?? ''}" cancels its pending, waiting and running runs.`}
        confirmText="Disable"
        onConfirm={() => {
          if (disabling) disableMutation.mutate(disabling.id);
          setDisabling(null);
        }}
        onCancel={() => setDisabling(null)}
      />

      <ConfirmDialog
        open={deleting !== null}
        title="Delete rule"
        message={`Deleting "${deleting?.name ?? ''}" cancels its pending, waiting and running runs. Runs already recorded stay.`}
        confirmText="Delete"
        severity="error"
        onConfirm={() => {
          if (deleting) deleteMutation.mutate(deleting.id);
          setDeleting(null);
        }}
        onCancel={() => setDeleting(null)}
      />

      {dryRunRule ? <DryRunDialog rule={dryRunRule} open onClose={() => setDryRunRule(null)} /> : null}
    </Box>
  );
};
