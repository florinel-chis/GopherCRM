import React from 'react';
import { useNavigate } from 'react-router-dom';
import { Box, Typography, Button, Container } from '@mui/material';
import { Home } from '@mui/icons-material';

export const NotFound: React.FC = () => {
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
        <Typography variant="h1" component="h1" gutterBottom sx={{ fontSize: '6rem', fontWeight: 'bold' }}>
          404
        </Typography>
        <Typography variant="h4" component="h2" gutterBottom>
          Page Not Found
        </Typography>
        <Typography variant="body1" color="text.secondary" paragraph>
          The page you are looking for doesn't exist or has been moved.
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