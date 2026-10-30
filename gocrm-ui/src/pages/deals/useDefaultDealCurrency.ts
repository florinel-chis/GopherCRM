import { useUIConfigurationsQuery } from '@/hooks/useUIConfigurations';

export const DEFAULT_DEAL_CURRENCY = 'EUR';
const DEFAULT_CURRENCY_KEY = 'deals.default_currency';

/**
 * The configured `deals.default_currency` (from the UI-safe configuration
 * list, the one authenticated query the whole app shares), upper-cased; EUR while it loads
 * or when it is absent or malformed.
 */
export const useDefaultDealCurrency = (enabled = true): string => {
  const { data } = useUIConfigurationsQuery(enabled);
  const configured = data?.find((config) => config.key === DEFAULT_CURRENCY_KEY)?.value;
  if (typeof configured === 'string' && /^[A-Za-z]{3}$/.test(configured.trim())) {
    return configured.trim().toUpperCase();
  }
  return DEFAULT_DEAL_CURRENCY;
};
