import React, { useState, useEffect } from 'react';
import {
  Box,
  Card,
  CardContent,
  Typography,
  Grid,
  Chip,
  IconButton,
  Tooltip,
} from '@mui/material';
import {
  Settings as SettingsIcon,
  Security as SecurityIcon,
  Palette as PaletteIcon,
  Business as BusinessIcon,
} from '@mui/icons-material';
import type { Configuration } from '@/api/endpoints/configurations';
import { configurationsApi } from '@/api/endpoints/configurations';
import { useAuth } from '@/hooks/useAuth';

interface ConfigurationSummary {
  category: string;
  count: number;
  icon: React.ReactNode;
  color: string;
  label: string;
}

export const ConfigurationOverview: React.FC = () => {
  const [configurations, setConfigurations] = useState<Configuration[]>([]);
  const [loading, setLoading] = useState(false);
  const { user } = useAuth();

  useEffect(() => {
    if (user?.role === 'admin') {
      loadConfigurations();
    }
  }, [user]);

  const loadConfigurations = async () => {
    try {
      setLoading(true);
      const configs = await configurationsApi.getAll();
      setConfigurations(configs);
    } catch (error) {
      console.error('Failed to load configurations:', error);
    } finally {
      setLoading(false);
    }
  };

  const getConfigurationSummary = (): ConfigurationSummary[] => {
    const categories = [
      { key: 'general', label: 'General', icon: <BusinessIcon />, color: '#2196f3' },
      { key: 'ui', label: 'UI & Theme', icon: <PaletteIcon />, color: '#9c27b0' },
      { key: 'security', label: 'Security', icon: <SecurityIcon />, color: '#f44336' },
      { key: 'leads', label: 'Leads', icon: <SettingsIcon />, color: '#4caf50' },
      { key: 'customers', label: 'Customers', icon: <SettingsIcon />, color: '#ff9800' },
      { key: 'tickets', label: 'Tickets', icon: <SettingsIcon />, color: '#607d8b' },
      { key: 'tasks', label: 'Tasks', icon: <SettingsIcon />, color: '#795548' },
      { key: 'integration', label: 'Integration', icon: <SettingsIcon />, color: '#009688' },
    ];

    return categories.map(category => ({
      category: category.key,
      count: configurations.filter(config => config.category === category.key).length,
      icon: category.icon,
      color: category.color,
      label: category.label,
    }));
  };

  // Only show to admin users
  if (user?.role !== 'admin') {
    return null;
  }

  if (loading || configurations.length === 0) {
    return null;
  }

  const summary = getConfigurationSummary();
  const totalConfigs = configurations.length;
  const systemConfigs = configurations.filter(c => c.is_system).length;
  const readOnlyConfigs = configurations.filter(c => c.is_read_only).length;

  return (
    <Box sx={{ mb: 3 }}>
      <Typography variant="h6" gutterBottom>
        Configuration Overview
      </Typography>
      
      <Grid container spacing={2}>
        {/* Summary Cards */}
        <Grid item xs={12} sm={6} md={3}>
          <Card>
            <CardContent>
              <Typography color="text.secondary" gutterBottom variant="body2">
                Total Configurations
              </Typography>
              <Typography variant="h4">
                {totalConfigs}
              </Typography>
            </CardContent>
          </Card>
        </Grid>
        
        <Grid item xs={12} sm={6} md={3}>
          <Card>
            <CardContent>
              <Typography color="text.secondary" gutterBottom variant="body2">
                System Configurations
              </Typography>
              <Typography variant="h4">
                {systemConfigs}
              </Typography>
            </CardContent>
          </Card>
        </Grid>
        
        <Grid item xs={12} sm={6} md={3}>
          <Card>
            <CardContent>
              <Typography color="text.secondary" gutterBottom variant="body2">
                Read-Only Settings
              </Typography>
              <Typography variant="h4">
                {readOnlyConfigs}
              </Typography>
            </CardContent>
          </Card>
        </Grid>
        
        <Grid item xs={12} sm={6} md={3}>
          <Card>
            <CardContent>
              <Typography color="text.secondary" gutterBottom variant="body2">
                Categories
              </Typography>
              <Typography variant="h4">
                {summary.filter(s => s.count > 0).length}
              </Typography>
            </CardContent>
          </Card>
        </Grid>
        
        {/* Category Breakdown */}
        {summary.filter(s => s.count > 0).map((categorySum) => (
          <Grid item xs={12} sm={6} md={4} key={categorySum.category}>
            <Card>
              <CardContent>
                <Box display="flex" alignItems="center" justifyContent="space-between">
                  <Box display="flex" alignItems="center">
                    <Box 
                      sx={{ 
                        color: categorySum.color, 
                        mr: 1,
                        display: 'flex',
                        alignItems: 'center'
                      }}
                    >
                      {categorySum.icon}
                    </Box>
                    <Typography variant="body2">
                      {categorySum.label}
                    </Typography>
                  </Box>
                  <Chip 
                    label={categorySum.count} 
                    size="small" 
                    sx={{ 
                      backgroundColor: categorySum.color + '20',
                      color: categorySum.color 
                    }}
                  />
                </Box>
              </CardContent>
            </Card>
          </Grid>
        ))}
      </Grid>
    </Box>
  );
};