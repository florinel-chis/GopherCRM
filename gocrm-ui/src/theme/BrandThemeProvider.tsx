import React, { useContext, useMemo } from 'react';
import { ThemeProvider } from '@mui/material/styles';
import { ConfigurationContext } from '@/contexts/configuration-context';
import { createAppTheme } from './index';
import { resolvePrimaryColor } from './primaryColor';

interface BrandThemeProviderProps {
  children: React.ReactNode;
}

/**
 * Applies the stored `ui.theme.primary_color` to the light theme. It reads the
 * configuration context directly and tolerates its absence, so outside a
 * ConfigurationProvider, while signed out, or for an unsupported value the
 * default theme (#1976d2) is used. A refreshed configuration re-renders it.
 */
export const BrandThemeProvider: React.FC<BrandThemeProviderProps> = ({ children }) => {
  const configuration = useContext(ConfigurationContext);
  const colour = resolvePrimaryColor(configuration?.getPrimaryColor());
  const brandTheme = useMemo(() => createAppTheme(colour), [colour]);
  return <ThemeProvider theme={brandTheme}>{children}</ThemeProvider>;
};
