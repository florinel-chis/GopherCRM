import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { AuthProvider } from '@/contexts/AuthContext';
import { ConfigurationProvider } from '@/contexts/ConfigurationContext';
import { SnackbarProvider } from '@/contexts/SnackbarContext';
import { ThemeProvider, createTheme } from '@mui/material/styles';

const theme = createTheme();

interface TestProvidersProps {
  children: React.ReactNode;
}

export const TestProviders: React.FC<TestProvidersProps> = ({ children }) => {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });

  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <ThemeProvider theme={theme}>
          <SnackbarProvider>
            <AuthProvider>
              <ConfigurationProvider>
                {children}
              </ConfigurationProvider>
            </AuthProvider>
          </SnackbarProvider>
        </ThemeProvider>
      </BrowserRouter>
    </QueryClientProvider>
  );
};
