import { useQuery } from '@tanstack/react-query';
import { configurationsApi } from '@/api/endpoints';

export const DEFAULT_DEAL_CURRENCY = 'EUR';
const DEFAULT_CURRENCY_KEY = 'deals.default_currency';

/**
 * The configured `deals.default_currency` (from the UI-safe configuration
 * list, the same query the deal form reads), upper-cased; EUR while it loads
 * or when it is absent or malformed.
 */
export const useDefaultDealCurrency = (enabled = true): string => {
  const { data } = useQuery({
    queryKey: ['configurations', 'ui'],
    queryFn: () => configurationsApi.getUIConfigurations(),
    enabled,
  });
  const configured = data?.find((config) => config.key === DEFAULT_CURRENCY_KEY)?.value;
  if (typeof configured === 'string' && /^[A-Za-z]{3}$/.test(configured.trim())) {
    return configured.trim().toUpperCase();
  }
  return DEFAULT_DEAL_CURRENCY;
};
