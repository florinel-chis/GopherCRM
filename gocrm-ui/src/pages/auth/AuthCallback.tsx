import React, { useEffect, useRef, useState } from 'react';
import { Link as RouterLink, useNavigate, useSearchParams } from 'react-router-dom';
import { Alert, Box, CircularProgress, Container, Link, Paper, Typography } from '@mui/material';
import { useAuth } from '@/hooks/useAuth';

export const SSO_CALLBACK_FAILED_MESSAGE = 'Single sign-on failed. Try again or sign in with your password.';

interface CallbackParams {
  code: string;
  state: string;
  error: boolean;
}

/**
 * The page the identity provider redirects back to after an OpenID Connect
 * sign-in (#436). Public and outside the application shell: the visitor has
 * no session yet. It reads code and state once, drops them from the address
 * bar, posts them once and starts the session the way a password login does.
 * Neither the code, the state nor the tokens are ever rendered or logged.
 */
export const AuthCallback: React.FC = () => {
  const navigate = useNavigate();
  const { loginWithSSO } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  // Read once, on the first render, and kept in component state only.
  const [params] = useState<CallbackParams>(() => ({
    code: searchParams.get('code') ?? '',
    state: searchParams.get('state') ?? '',
    error: searchParams.has('error'),
  }));
  const usable = Boolean(params.code && params.state && !params.error);
  const [failed, setFailed] = useState(!usable);
  // The code is single-use: the ref keeps a development double effect from
  // posting it twice.
  const posted = useRef(false);

  // Drop the query from the address bar with a replace navigation, so the
  // history entry no longer carries the code and the state.
  useEffect(() => {
    if (searchParams.toString() !== '') {
      setSearchParams({}, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  useEffect(() => {
    if (!usable || posted.current) {
      return;
    }
    posted.current = true;
    loginWithSSO(params.code, params.state)
      .then(() => navigate('/', { replace: true }))
      .catch(() => setFailed(true));
  }, [usable, params, loginWithSSO, navigate]);

  return (
    <Container component="main" maxWidth="xs">
      <Box sx={{ marginTop: 8, display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
        <Paper elevation={3} sx={{ padding: 4, width: '100%' }}>
          <Typography component="h1" variant="h5" align="center" gutterBottom>
            Single sign-on
          </Typography>
          {failed ? (
            <>
              <Alert severity="error" sx={{ mt: 2 }}>
                {SSO_CALLBACK_FAILED_MESSAGE}
              </Alert>
              <Box sx={{ mt: 2, textAlign: 'center' }}>
                <Link component={RouterLink} to="/login" variant="body2">
                  Back to sign in
                </Link>
              </Box>
            </>
          ) : (
            <Box sx={{ mt: 2, display: 'flex', justifyContent: 'center' }}>
              <CircularProgress aria-label="Signing in" />
            </Box>
          )}
        </Paper>
      </Box>
    </Container>
  );
};

export const Component = AuthCallback;
