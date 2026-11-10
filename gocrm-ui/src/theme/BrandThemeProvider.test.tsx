import { describe, it, expect, vi, beforeEach } from 'vitest';
import React from 'react';
import { act, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createTheme, useTheme } from '@mui/material/styles';
import { BrandThemeProvider } from './BrandThemeProvider';
import { createAppTheme } from './index';
import { ConfigurationProvider } from '@/contexts/ConfigurationContext';
import {
  ConfigurationContext,
  type ConfigurationContextType,
} from '@/contexts/configuration-context';
import { configurationsApi } from '@/api/endpoints';

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({
    user: null,
    isLoading: false,
    isAuthenticated: false,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  }),
}));

vi.mock('@/api/endpoints', () => ({
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

const PaletteProbe: React.FC = () => {
  const { palette } = useTheme();
  return (
    <div>
      <span data-testid="main">{palette.primary.main}</span>
      <span data-testid="light">{palette.primary.light}</span>
      <span data-testid="dark">{palette.primary.dark}</span>
      <span data-testid="contrast">{palette.primary.contrastText}</span>
    </div>
  );
};

const contextValue = (primaryColor: string): ConfigurationContextType => ({
  configurations: [],
  isLoading: false,
  error: null,
  getLeadConversion: () => ({ status: 'unavailable' }),
  getCompanyName: () => 'GopherCRM',
  getPrimaryColor: () => primaryColor,
  refreshConfigurations: vi.fn(async () => {}),
});

const tree = (primaryColor: string) => (
  <ConfigurationContext.Provider value={contextValue(primaryColor)}>
    <BrandThemeProvider>
      <PaletteProbe />
    </BrandThemeProvider>
  </ConfigurationContext.Provider>
);

const text = (id: string) => screen.getByTestId(id).textContent;

const expectDefaultPalette = () => {
  expect(text('main')).toBe('#1976d2');
  expect(text('light')).toBe('#42a5f5');
  expect(text('dark')).toBe('#1565c0');
};

describe('BrandThemeProvider', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('applies a stored primary colour to the theme', () => {
    render(tree('#ff5722'));
    expect(text('main')).toBe('#ff5722');

    render(tree(' rgb(46, 125, 50) '));
    expect(screen.getAllByTestId('main')[1].textContent).toBe('rgb(46, 125, 50)');
  });

  it('recomputes the light and dark variants from a stored colour', () => {
    render(tree('#ff5722'));
    // Not the default's stale variants, but what MUI derives from main alone.
    expect(text('light')).not.toBe('#42a5f5');
    expect(text('dark')).not.toBe('#1565c0');
    const derived = createTheme({ palette: { primary: { main: '#ff5722' } } }).palette.primary;
    expect(text('light')).toBe(derived.light);
    expect(text('dark')).toBe(derived.dark);
    expect(text('contrast')).toBe(derived.contrastText);

    // createAppTheme passes only main for a non-default colour.
    const theme = createAppTheme('#ff5722');
    expect(theme.palette.primary.light).toBe(derived.light);
    expect(theme.palette.primary.dark).toBe(derived.dark);
  });

  it('falls back to the default colour for an invalid stored value', () => {
    for (const invalid of ['dimgray', 'var(--brand)', '#ff572280', 'oklch(0.6 0.2 30)', '']) {
      const view = render(tree(invalid));
      expectDefaultPalette();
      view.unmount();
    }
    // createAppTheme itself never lets an invalid value through.
    expect(() => createAppTheme('not a colour')).not.toThrow();
    expect(createAppTheme('not a colour').palette.primary.main).toBe('#1976d2');
    expect(createAppTheme('#1976D2').palette.primary.light).toBe('#42a5f5');
  });

  it('uses the default colour while signed out and outside a ConfigurationProvider', async () => {
    const outside = render(
      <BrandThemeProvider>
        <PaletteProbe />
      </BrandThemeProvider>,
    );
    expectDefaultPalette();
    outside.unmount();

    // Signed out under the real provider: no request, the default colour.
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ConfigurationProvider>
          <BrandThemeProvider>
            <PaletteProbe />
          </BrandThemeProvider>
        </ConfigurationProvider>
      </QueryClientProvider>,
    );
    expectDefaultPalette();
    await act(async () => {});
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
  });

  it('follows a changed stored colour on re-render', () => {
    const view = render(tree('#1976d2'));
    expectDefaultPalette();

    // What refreshConfigurations() produces: a new context value.
    view.rerender(tree('#ff5722'));
    expect(text('main')).toBe('#ff5722');

    view.rerender(tree('hsl(120, 60%, 40%)'));
    expect(text('main')).toBe('hsl(120, 60%, 40%)');

    view.rerender(tree('dimgray'));
    expectDefaultPalette();
  });
});
