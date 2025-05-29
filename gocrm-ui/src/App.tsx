import { RouterProvider } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ReactQueryDevtools } from '@tanstack/react-query-devtools';
import { ThemeProvider, CssBaseline } from '@mui/material';
import { AuthProvider } from '@/contexts/AuthContext';
import { SnackbarProvider } from '@/contexts/SnackbarContext';
import { ConfigurationProvider } from '@/contexts/ConfigurationContext';
import { ErrorBoundary } from '@/components/ErrorBoundary';
import { router } from '@/routes';
import { theme } from '@/theme';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 1000 * 60 * 5, // 5 minutes
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

function App() {
  return (
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <ThemeProvider theme={theme}>
          <CssBaseline />
          <SnackbarProvider>
            <AuthProvider>
              <ConfigurationProvider>
                <RouterProvider router={router} />
              </ConfigurationProvider>
            </AuthProvider>
          </SnackbarProvider>
        </ThemeProvider>
        {import.meta.env.DEV && <ReactQueryDevtools initialIsOpen={false} />}
      </QueryClientProvider>
    </ErrorBoundary>
  );
}

export default App;
