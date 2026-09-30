import type { QueryClient } from '@tanstack/react-query';

/**
 * Marks stale every query that reads deals after a create, update, stage
 * change or delete: the lists and board columns and the board's pipeline
 * (all under ['deals'], the pipeline as ['deals', 'pipeline']) and the
 * dashboard's pipeline widgets (['dashboard', 'pipeline']).
 */
export const invalidateDealQueries = (queryClient: QueryClient): void => {
  queryClient.invalidateQueries({ queryKey: ['deals'] });
  queryClient.invalidateQueries({ queryKey: ['dashboard', 'pipeline'] });
};
