import React, { useState, useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import {
  Paper,
  Stack,
  Typography,
  Box,
  Card,
  CardContent,
  List,
  ListItem,
  ListItemText,
  ListItemAvatar,
  Avatar,
  Chip,
  IconButton,
  Skeleton,
} from '@mui/material';
import {
  Business,
  Assignment,
  Task,
  TrendingUp,
  ArrowForward,
  ContactPhone,
  AssignmentTurnedIn,
  NoteAdd as NoteAddIcon,
  PersonAdd as PersonAddIcon,
  ConfirmationNumber as TicketIcon,
} from '@mui/icons-material';
import { dashboardApi } from '@/api/endpoints';
import { Button } from '@mui/material';
import { formatDistanceToNow } from 'date-fns';
import { 
  AreaChart, 
  Area, 
  XAxis, 
  YAxis, 
  CartesianGrid, 
  Tooltip, 
  ResponsiveContainer 
} from 'recharts';

interface StatCardProps {
  title: string;
  value: number | string;
  icon: React.ReactNode;
  color: string;
  trend?: number;
}

const StatCard: React.FC<StatCardProps> = ({ title, value, icon, color, trend }) => {
  return (
    <Card>
      <CardContent>
        <Box display="flex" alignItems="center" justifyContent="space-between">
          <Box>
            <Typography color="textSecondary" gutterBottom variant="body2">
              {title}
            </Typography>
            <Typography variant="h4" component="div">
              {value}
            </Typography>
            {trend !== undefined && (
              <Box display="flex" alignItems="center" mt={1}>
                <TrendingUp 
                  fontSize="small" 
                  sx={{ color: trend > 0 ? 'success.main' : 'error.main' }}
                />
                <Typography 
                  variant="body2" 
                  sx={{ 
                    color: trend > 0 ? 'success.main' : 'error.main',
                    ml: 0.5 
                  }}
                >
                  {Math.abs(trend)}%
                </Typography>
              </Box>
            )}
          </Box>
          <Avatar sx={{ bgcolor: color, width: 56, height: 56 }}>
            {icon}
          </Avatar>
        </Box>
      </CardContent>
    </Card>
  );
};

export const Dashboard: React.FC = () => {
  const navigate = useNavigate();
  
  const { data: stats, isLoading: statsLoading } = useQuery({
    queryKey: ['dashboard', 'stats'],
    queryFn: dashboardApi.getStats,
  });

  const { data: activities, isLoading: activitiesLoading } = useQuery({
    queryKey: ['dashboard', 'activities'],
    queryFn: () => dashboardApi.getRecentActivities(10),
    enabled: false, // Disable for now since backend doesn't implement this yet
  });

  const { data: salesData } = useQuery({
    queryKey: ['dashboard', 'sales'],
    queryFn: () => dashboardApi.getSalesPerformance('month'),
    enabled: false, // Disable for now since backend doesn't implement this yet
  });

  const { data: upcomingTasks } = useQuery({
    queryKey: ['dashboard', 'tasks'],
    queryFn: () => dashboardApi.getUpcomingTasks(5),
    enabled: false, // Disable for now since backend doesn't implement this yet
  });

  const getActivityIcon = (type: string) => {
    switch (type) {
      case 'lead_created':
        return <ContactPhone />;
      case 'lead_converted':
        return <Business />;
      case 'ticket_created':
        return <Assignment />;
      case 'ticket_resolved':
        return <AssignmentTurnedIn />;
      case 'task_completed':
        return <Task />;
      default:
        return <Assignment />;
    }
  };

  const getActivityColor = (type: string) => {
    switch (type) {
      case 'lead_created':
      case 'lead_converted':
        return 'primary';
      case 'ticket_created':
      case 'ticket_resolved':
        return 'secondary';
      case 'task_completed':
        return 'success';
      default:
        return 'default';
    }
  };

  return (
    <Box>
      <Typography variant="h4" gutterBottom>
        Dashboard
      </Typography>
      
      {/* Stats Cards */}
      <Box 
        sx={{ 
          display: 'flex', 
          flexWrap: 'wrap', 
          gap: 3, 
          mb: 3 
        }}
      >
        <Box sx={{ flex: { xs: '1 1 100%', sm: '1 1 calc(50% - 12px)', md: '1 1 calc(25% - 18px)' } }}>
          {statsLoading ? (
            <Skeleton variant="rectangular" height={140} />
          ) : (
            <StatCard
              title="Total Leads"
              value={stats?.total_leads || 0}
              icon={<ContactPhone />}
              color="primary.main"
              trend={12}
            />
          )}
        </Box>
        <Box sx={{ flex: { xs: '1 1 100%', sm: '1 1 calc(50% - 12px)', md: '1 1 calc(25% - 18px)' } }}>
          {statsLoading ? (
            <Skeleton variant="rectangular" height={140} />
          ) : (
            <StatCard
              title="Total Customers"
              value={stats?.total_customers || 0}
              icon={<Business />}
              color="success.main"
              trend={8}
            />
          )}
        </Box>
        <Box sx={{ flex: { xs: '1 1 100%', sm: '1 1 calc(50% - 12px)', md: '1 1 calc(25% - 18px)' } }}>
          {statsLoading ? (
            <Skeleton variant="rectangular" height={140} />
          ) : (
            <StatCard
              title="Open Tickets"
              value={stats?.open_tickets || 0}
              icon={<Assignment />}
              color="warning.main"
              trend={-5}
            />
          )}
        </Box>
        <Box sx={{ flex: { xs: '1 1 100%', sm: '1 1 calc(50% - 12px)', md: '1 1 calc(25% - 18px)' } }}>
          {statsLoading ? (
            <Skeleton variant="rectangular" height={140} />
          ) : (
            <StatCard
              title="Pending Tasks"
              value={stats?.pending_tasks || 0}
              icon={<Task />}
              color="info.main"
            />
          )}
        </Box>
        <Box sx={{ flex: { xs: '1 1 100%', sm: '1 1 calc(50% - 12px)', md: '1 1 calc(25% - 18px)' } }}>
          {statsLoading ? (
            <Skeleton variant="rectangular" height={140} />
          ) : (
            <StatCard
              title="Conversion Rate"
              value={`${(stats?.conversion_rate || 0).toFixed(1)}%`}
              icon={<TrendingUp />}
              color="success.main"
            />
          )}
        </Box>
      </Box>

      {/* Quick Actions Panel */}
      <Paper sx={{ p: 2, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Quick Actions
        </Typography>
        <Box display="flex" gap={2} flexWrap="wrap">
          <Button
            variant="contained"
            startIcon={<PersonAddIcon />}
            onClick={() => navigate('/leads/new')}
          >
            New Lead
          </Button>
          <Button
            variant="contained"
            color="secondary"
            startIcon={<TicketIcon />}
            onClick={() => navigate('/tickets/new')}
          >
            New Ticket
          </Button>
          <Button
            variant="contained"
            color="info"
            startIcon={<NoteAddIcon />}
            onClick={() => navigate('/tasks/new')}
          >
            New Task
          </Button>
          <Button
            variant="outlined"
            startIcon={<Business />}
            onClick={() => navigate('/customers')}
          >
            View Customers
          </Button>
        </Box>
      </Paper>

      {/* Charts and Activities */}
      <Stack spacing={3}>
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 3 }}>
          {/* Sales Performance Chart */}
          <Box sx={{ flex: { xs: '1 1 100%', md: '1 1 calc(66.666% - 12px)' } }}>
            <Paper sx={{ p: 2 }}>
              <Typography variant="h6" gutterBottom>
                Sales Performance
              </Typography>
              {salesData && (
                <ResponsiveContainer width="100%" height={300}>
                  <AreaChart data={salesData.datasets[0].data.map((value, index) => ({
                    name: salesData.labels[index],
                    value,
                  }))}>
                    <CartesianGrid strokeDasharray="3 3" />
                    <XAxis dataKey="name" />
                    <YAxis />
                    <Tooltip />
                    <Area 
                      type="monotone" 
                      dataKey="value" 
                      stroke="#1976d2" 
                      fill="#1976d2" 
                      fillOpacity={0.3}
                    />
                  </AreaChart>
                </ResponsiveContainer>
              )}
            </Paper>
          </Box>

          {/* Placeholder for future features */}
          <Box sx={{ flex: { xs: '1 1 100%', md: '1 1 calc(33.333% - 12px)' } }}>
            <Paper sx={{ p: 2, height: '100%' }}>
              <Typography variant="h6" gutterBottom>
                Coming Soon
              </Typography>
              <Typography color="text.secondary">
                Recent activities, sales charts, and task management will be available in future updates.
              </Typography>
            </Paper>
          </Box>
        </Box>
      </Stack>
    </Box>
  );
};