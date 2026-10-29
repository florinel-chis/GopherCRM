import React, { useEffect, useMemo, useState } from 'react';
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { Autocomplete, CircularProgress, Link, TextField } from '@mui/material';
import { automationApi, type AutomationRuleFilters } from '@/api/endpoints';
import type { AutomationRule } from '@/types';
import { getAPIError } from '@/utils/apiError';
import { automationKeys } from './automationQueries';

// The rules API has no name search: page through it by name (100 per page,
// at most ten pages) and filter locally. Anything beyond that, and deleted
// rules, which the list never returns, are reachable through the Rule ID field.
const PAGE_SIZE = 100;
const MAX_PAGES = 10;

const pageFilters = (page: number): AutomationRuleFilters => ({
  offset: page * PAGE_SIZE,
  limit: PAGE_SIZE,
  sort_by: 'name',
  sort_order: 'asc',
});

const ruleLabel = (rule: AutomationRule): string => `${rule.name} (#${rule.id})`;

export interface RuleFilterAutocompleteProps {
  /** The selected rule id, shared with the numeric Rule ID filter. */
  value: number | undefined;
  onChange: (ruleId: number | undefined) => void;
  /** Reports every rule loaded so far, so the run list can show names. */
  onRulesLoaded?: (rules: AutomationRule[]) => void;
}

/**
 * Admin-only rule picker for the run list. Nothing is requested until the
 * list is first opened: sales never mounts it, so it never calls
 * GET /automation/rules, which is admin-only.
 */
export const RuleFilterAutocomplete: React.FC<RuleFilterAutocompleteProps> = ({
  value,
  onChange,
  onRulesLoaded,
}) => {
  const queryClient = useQueryClient();
  const [opened, setOpened] = useState(false);

  const firstPage = useQuery({
    queryKey: automationKeys.ruleList(pageFilters(0)),
    queryFn: () => automationApi.listRules(pageFilters(0)),
    enabled: opened,
  });

  const pageCount = firstPage.data
    ? Math.min(MAX_PAGES, Math.max(1, Math.ceil(firstPage.data.total / PAGE_SIZE)))
    : 0;

  const laterPages = useQueries({
    queries: Array.from({ length: Math.max(0, pageCount - 1) }, (_, index) => {
      const filters = pageFilters(index + 1);
      return {
        queryKey: automationKeys.ruleList(filters),
        queryFn: () => automationApi.listRules(filters),
        enabled: opened,
      };
    }),
    // Structurally shared, so the array keeps its identity until a page changes.
    combine: (results) => ({
      rules: results.map((result) => result.data?.rules),
      loading: results.some((result) => result.isLoading),
      error: results.find((result) => result.isError)?.error ?? null,
    }),
  });

  const firstRules = firstPage.data?.rules;
  const laterRules = laterPages.rules;
  const rules = useMemo(() => {
    const all = [...(firstRules ?? []), ...laterRules.flatMap((page) => page ?? [])];
    const seen = new Set<number>();
    // A rule renamed between two page requests could appear twice.
    return all.filter((rule) => (seen.has(rule.id) ? false : (seen.add(rule.id), true)));
  }, [firstRules, laterRules]);

  useEffect(() => {
    if (rules.length > 0) {
      onRulesLoaded?.(rules);
    }
  }, [rules, onRulesLoaded]);

  const selected = value === undefined ? null : (rules.find((rule) => rule.id === value) ?? null);
  const loading = opened && (firstPage.isLoading || laterPages.loading);
  const loadError = firstPage.error ?? laterPages.error;
  const errorText = loadError
    ? `Rules could not be loaded: ${getAPIError(loadError)?.message ?? 'please try again'}`
    : undefined;
  // Refetches the failed rule pages. They may be disabled (the error was
  // cached before this mount and the list has not been opened), so this
  // enables them and refetches by key rather than only active queries.
  const retry = () => {
    setOpened(true);
    void queryClient.refetchQueries({
      queryKey: automationKeys.rules,
      predicate: (query) => query.state.status === 'error',
    });
  };

  return (
    <Autocomplete
      size="small"
      sx={{ minWidth: 240 }}
      options={rules}
      value={selected}
      loading={loading}
      onOpen={() => setOpened(true)}
      onChange={(_, rule) => onChange(rule ? rule.id : undefined)}
      getOptionLabel={ruleLabel}
      isOptionEqualToValue={(option, current) => option.id === current.id}
      noOptionsText={errorText ?? 'No rules'}
      renderInput={(params) => (
        <TextField
          {...params}
          label="Rule"
          error={Boolean(loadError)}
          helperText={
            loadError ? (
              <>
                {errorText}{' '}
                <Link
                  component="button"
                  type="button"
                  variant="caption"
                  aria-label="Retry loading rules"
                  onClick={retry}
                >
                  Retry
                </Link>
              </>
            ) : undefined
          }
          InputProps={{
            ...params.InputProps,
            endAdornment: (
              <>
                {loading ? <CircularProgress color="inherit" size={18} /> : null}
                {params.InputProps.endAdornment}
              </>
            ),
          }}
        />
      )}
    />
  );
};
