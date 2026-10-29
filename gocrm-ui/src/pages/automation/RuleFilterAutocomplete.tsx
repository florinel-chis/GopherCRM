import React, { useEffect, useMemo, useState } from 'react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { Autocomplete, CircularProgress, TextField } from '@mui/material';
import { automationApi, type AutomationRuleFilters } from '@/api/endpoints';
import type { AutomationRule } from '@/types';
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
      noOptionsText={firstPage.isError ? 'Rules could not be loaded' : 'No rules'}
      renderInput={(params) => (
        <TextField
          {...params}
          label="Rule"
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
