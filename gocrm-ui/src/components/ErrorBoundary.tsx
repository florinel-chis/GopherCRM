import { Component } from 'react';
import type { ErrorInfo, ReactNode } from 'react';
import { useRouteError } from 'react-router-dom';
import { Box, Typography, Button, Paper, Stack } from '@mui/material';
import { ErrorOutline } from '@mui/icons-material';

interface Props {
  children: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
  errorInfo: ErrorInfo | null;
}

/**
 * Turns whatever was thrown into something printable. Route errors are not
 * necessarily `Error` instances — a loader may throw a Response or a string.
 */
const describeError = (error: unknown): string => {
  if (error instanceof Error) {
    return error.stack || error.toString();
  }
  if (typeof error === 'string') {
    return error;
  }
  try {
    return JSON.stringify(error, null, 2);
  } catch {
    return String(error);
  }
};

/**
 * The fallback screen itself, shared by the class boundary below and by the
 * router's `errorElement`. Error detail is dev-only: in production users get
 * the apology and the two recovery actions, nothing that leaks internals.
 */
export function ErrorFallback({
  error,
  detail,
}: {
  error?: unknown;
  detail?: string;
}) {
  const debugText = detail ?? (error === undefined ? undefined : describeError(error));

  return (
    <Box
      role="alert"
      sx={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        minHeight: '100vh',
        padding: 3,
        backgroundColor: 'background.default',
      }}
    >
      <Paper
        elevation={3}
        sx={{
          padding: 4,
          maxWidth: 600,
          textAlign: 'center',
        }}
      >
        <ErrorOutline
          sx={{
            fontSize: 64,
            color: 'error.main',
            mb: 2,
          }}
        />
        <Typography variant="h4" gutterBottom>
          Oops! Something went wrong
        </Typography>
        <Typography
          variant="body1"
          color="text.secondary"
          sx={{ mb: 3 }}
        >
          We're sorry for the inconvenience. An unexpected error has occurred.
          Please try reloading the page or contact support if the problem persists.
        </Typography>
        {import.meta.env.DEV && debugText && (
          <Box
            sx={{
              mt: 2,
              p: 2,
              backgroundColor: 'grey.100',
              borderRadius: 1,
              textAlign: 'left',
            }}
          >
            <Typography variant="caption" component="pre" sx={{ whiteSpace: 'pre-wrap' }}>
              {debugText}
            </Typography>
          </Box>
        )}
        <Stack direction="row" spacing={2} justifyContent="center" sx={{ mt: 3 }}>
          <Button
            variant="contained"
            color="primary"
            onClick={() => window.location.reload()}
          >
            Reload Page
          </Button>
          <Button
            variant="outlined"
            color="primary"
            onClick={() => {
              window.location.href = '/';
            }}
          >
            Return to Dashboard
          </Button>
        </Stack>
      </Paper>
    </Box>
  );
}

/**
 * Router-level boundary. A data router catches render errors in its own routes
 * before they can reach a boundary wrapped around `RouterProvider`, so without
 * this the app would fall back to React Router's built-in error screen. Wired
 * as `errorElement` on every top-level route in `@/routes`.
 */
export function RouteErrorBoundary() {
  const error = useRouteError();

  // The router does not log these itself in production builds.
  console.error('Route error boundary caught an error:', error);

  return <ErrorFallback error={error} />;
}

/**
 * Component boundary for the tree outside the router (providers, theme). Kept
 * as a class because that is the only way to catch a render error in React.
 */
export class ErrorBoundary extends Component<Props, State> {
  public state: State = {
    hasError: false,
    error: null,
    errorInfo: null,
  };

  public static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error, errorInfo: null };
  }

  public componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error('ErrorBoundary caught an error:', error, errorInfo);
    this.setState({
      error,
      errorInfo,
    });
  }

  public render() {
    if (this.state.hasError) {
      return (
        <ErrorFallback
          detail={`${this.state.error?.toString() ?? ''}${this.state.errorInfo?.componentStack ?? ''}`}
        />
      );
    }

    return this.props.children;
  }
}
