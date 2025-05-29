import React from 'react';
import { useNavigate } from 'react-router-dom';
import { Box, Typography, Button, Container } from '@mui/material';
import { Home, Lock } from '@mui/icons-material';

export const Unauthorized: React.FC = () => {
  const navigate = useNavigate();

  return (
    <Container>
      <Box
        sx={{
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          minHeight: '100vh',
          textAlign: 'center',
        }}
      >
        <Lock sx={{ fontSize: '4rem', color: 'error.main', mb: 2 }} />
        <Typography variant="h1" component="h1" gutterBottom sx={{ fontSize: '3rem', fontWeight: 'bold' }}>
          Access Denied
        </Typography>
        <Typography variant="h5" component="h2" gutterBottom>
          You don't have permission to access this page
        </Typography>
        <Typography variant="body1" color="text.secondary" paragraph>
          Please contact your administrator if you believe this is an error.
        </Typography>
        <Button
          variant="contained"
          startIcon={<Home />}
          onClick={() => navigate('/')}
          sx={{ mt: 2 }}
        >
          Go to Dashboard
        </Button>
      </Box>
    </Container>
  );
};