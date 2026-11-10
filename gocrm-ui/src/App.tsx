import { RouterProvider } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ThemeProvider, CssBaseline } from '@mui/material';
import { AuthProvider } from '@/contexts/AuthContext';
import { SnackbarProvider } from '@/contexts/SnackbarContext';
import { ConfigurationProvider } from '@/contexts/ConfigurationContext';
import { ErrorBoundary } from '@/components/ErrorBoundary';
import { router } from '@/routes';
import { theme } from '@/theme';
import { BrandThemeProvider } from '@/theme/BrandThemeProvider';
import { APP_QUERY_DEFAULTS } from '@/api/queryDefaults';

const queryClient = new QueryClient({ defaultOptions: APP_QUERY_DEFAULTS });

function App() {
  return (
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <ThemeProvider theme={theme}>
          <CssBaseline />
          <SnackbarProvider>
            <AuthProvider>
              <ConfigurationProvider>
                {/* The stored primary colour applies below the configuration;
                    the snackbars above keep the static theme (severity colours only). */}
                <BrandThemeProvider>
                  <RouterProvider router={router} />
                </BrandThemeProvider>
              </ConfigurationProvider>
            </AuthProvider>
          </SnackbarProvider>
        </ThemeProvider>
      </QueryClientProvider>
    </ErrorBoundary>
  );
}

export default App;
