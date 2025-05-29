import React, { useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Chip,
  Divider,
  Stack,
  Tab,
  Tabs,
  List,
  ListItem,
  ListItemText,
  ListItemIcon,
  IconButton,
} from '@mui/material';
import {
  Edit as EditIcon,
  Delete as DeleteIcon,
  Phone as PhoneIcon,
  Email as EmailIcon,
  Business as BusinessIcon,
  Person as PersonIcon,
  CalendarToday as CalendarIcon,
  AttachMoney as MoneyIcon,
  Group as GroupIcon,
  Language as WebIcon,
  LocationOn as LocationIcon,
  History as HistoryIcon,
  ConfirmationNumber as TicketIcon,
  Notes as NotesIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useSnackbar } from '@/hooks/useSnackbar';
import { customersApi, ticketsApi } from '@/api/endpoints';
import type { Ticket } from '@/types';
import { format } from 'date-fns';

interface TabPanelProps {
  children?: React.ReactNode;
  index: number;
  value: number;
}

function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;
  return (
    <div
      role="tabpanel"
      hidden={value !== index}
      id={`tabpanel-${index}`}
      aria-labelledby={`tab-${index}`}
      {...other}
    >
      {value === index && <Box sx={{ py: 3 }}>{children}</Box>}
    </div>
  );
}

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  
  const [tabValue, setTabValue] = useState(0);
  const [deleteDialog, setDeleteDialog] = useState(false);

  const { data: customer, isLoading } = useQuery({
    queryKey: ['customer', id],
    queryFn: () => customersApi.getCustomer(Number(id)),
    enabled: !!id,
  });

  const { data: ticketsData } = useQuery({
    queryKey: ['tickets', { customer_id: id }],
    queryFn: () => ticketsApi.getTickets({ customer_id: Number(id) }),
    enabled: !!id,
  });

  const deleteMutation = useMutation({
    mutationFn: () => customersApi.deleteCustomer(Number(id)),
    onSuccess: () => {
      showSuccess('Customer deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      navigate('/customers');
    },
    onError: () => {
      showError('Failed to delete customer');
    },
  });

  if (isLoading || !customer) {
    return <Loading />;
  }

  const activities = [
    {
      id: 1,
      type: 'created',
      description: 'Customer account created',
      user: 'System',
      timestamp: customer.created_at,
    },
    {
      id: 2,
      type: 'updated',
      description: 'Customer information updated',
      user: customer.owner?.username || 'Unknown',
      timestamp: customer.updated_at,
    },
  ];

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Box display="flex" alignItems="center" gap={2}>
          <Typography variant="h4">{customer.company_name}</Typography>
          <Chip
            label={customer.is_active ? 'Active' : 'Inactive'}
            color={customer.is_active ? 'success' : 'default'}
          />
        </Box>
        <Box display="flex" gap={1}>
          <Button
            variant="contained"
            startIcon={<TicketIcon />}
            onClick={() => navigate(`/tickets/new?customer_id=${customer.id}`)}
          >
            Create Ticket
          </Button>
          <Button
            variant="outlined"
            startIcon={<EditIcon />}
            onClick={() => navigate(`/customers/${id}/edit`)}
          >
            Edit
          </Button>
          <IconButton
            color="error"
            onClick={() => setDeleteDialog(true)}
          >
            <DeleteIcon />
          </IconButton>
        </Box>
      </Box>

      <Paper>
        <Tabs
          value={tabValue}
          onChange={(_, newValue) => setTabValue(newValue)}
          sx={{ borderBottom: 1, borderColor: 'divider' }}
        >
          <Tab label="Details" />
          <Tab label="Tickets" />
          <Tab label="History" />
        </Tabs>

        <TabPanel value={tabValue} index={0}>
          <Box sx={{ p: 3 }}>
            <Stack spacing={3}>
              <Box>
                <Typography variant="h6" gutterBottom>
                  Contact Information
                </Typography>
                <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                  <Box display="flex" alignItems="center" gap={1}>
                    <PersonIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Primary Contact
                      </Typography>
                      <Typography>{customer.contact_name}</Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <BusinessIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Industry
                      </Typography>
                      <Typography>{customer.industry || 'Not specified'}</Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <EmailIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Email
                      </Typography>
                      <Typography>
                        <a href={`mailto:${customer.email}`}>{customer.email}</a>
                      </Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <PhoneIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Phone
                      </Typography>
                      <Typography>
                        <a href={`tel:${customer.phone}`}>{customer.phone}</a>
                      </Typography>
                    </Box>
                  </Box>
                  {customer.website && (
                    <Box display="flex" alignItems="center" gap={1}>
                      <WebIcon color="action" />
                      <Box>
                        <Typography variant="caption" color="text.secondary">
                          Website
                        </Typography>
                        <Typography>
                          <a href={customer.website} target="_blank" rel="noopener noreferrer">
                            {customer.website}
                          </a>
                        </Typography>
                      </Box>
                    </Box>
                  )}
                </Box>
              </Box>

              <Divider />

              <Box>
                <Typography variant="h6" gutterBottom>
                  Business Information
                </Typography>
                <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                  <Box display="flex" alignItems="center" gap={1}>
                    <MoneyIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Annual Revenue
                      </Typography>
                      <Typography>
                        {customer.annual_revenue ? `$${customer.annual_revenue.toLocaleString()}` : 'Not specified'}
                      </Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <GroupIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Employees
                      </Typography>
                      <Typography>
                        {customer.employee_count ? customer.employee_count.toLocaleString() : 'Not specified'}
                      </Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <MoneyIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Total Revenue
                      </Typography>
                      <Typography>${customer.total_revenue.toLocaleString()}</Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <CalendarIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Customer Since
                      </Typography>
                      <Typography>
                        {format(new Date(customer.created_at), 'MMM dd, yyyy')}
                      </Typography>
                    </Box>
                  </Box>
                </Box>
              </Box>

              {(customer.address || customer.city || customer.country) && (
                <>
                  <Divider />
                  <Box>
                    <Box display="flex" alignItems="center" gap={1} mb={2}>
                      <LocationIcon color="action" />
                      <Typography variant="h6">Address</Typography>
                    </Box>
                    <Typography>
                      {customer.address && <>{customer.address}<br /></>}
                      {customer.city && `${customer.city}${customer.state ? `, ${customer.state}` : ''}`}
                      {customer.postal_code && ` ${customer.postal_code}`}
                      {customer.country && <><br />{customer.country}</>}
                    </Typography>
                  </Box>
                </>
              )}

              {customer.notes && (
                <>
                  <Divider />
                  <Box>
                    <Box display="flex" alignItems="center" gap={1} mb={1}>
                      <NotesIcon color="action" />
                      <Typography variant="h6">Notes</Typography>
                    </Box>
                    <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>
                      {customer.notes}
                    </Typography>
                  </Box>
                </>
              )}
            </Stack>
          </Box>
        </TabPanel>

        <TabPanel value={tabValue} index={1}>
          <Box sx={{ p: 3 }}>
            <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
              <Typography variant="h6">Support Tickets</Typography>
              <Button
                variant="outlined"
                size="small"
                startIcon={<TicketIcon />}
                onClick={() => navigate(`/tickets/new?customer_id=${customer.id}`)}
              >
                New Ticket
              </Button>
            </Box>
            {ticketsData?.data && ticketsData.data.length > 0 ? (
              <List>
                {ticketsData.data.map((ticket: Ticket) => (
                  <ListItem
                    key={ticket.id}
                    sx={{ px: 0, cursor: 'pointer' }}
                    onClick={() => navigate(`/tickets/${ticket.id}`)}
                  >
                    <ListItemText
                      primary={ticket.subject}
                      secondary={
                        <>
                          #{ticket.id} • {ticket.status} • {ticket.priority} priority •{' '}
                          {format(new Date(ticket.created_at), 'MMM dd, yyyy')}
                        </>
                      }
                    />
                  </ListItem>
                ))}
              </List>
            ) : (
              <Typography color="text.secondary">No tickets found</Typography>
            )}
          </Box>
        </TabPanel>

        <TabPanel value={tabValue} index={2}>
          <Box sx={{ p: 3 }}>
            <Box display="flex" alignItems="center" gap={1} mb={2}>
              <HistoryIcon />
              <Typography variant="h6">Activity History</Typography>
            </Box>
            <List>
              {activities.map((activity) => (
                <ListItem key={activity.id} sx={{ px: 0 }}>
                  <ListItemIcon>
                    <HistoryIcon color="action" />
                  </ListItemIcon>
                  <ListItemText
                    primary={activity.description}
                    secondary={
                      <>
                        {activity.user} • {format(new Date(activity.timestamp), 'MMM dd, yyyy HH:mm')}
                      </>
                    }
                  />
                </ListItem>
              ))}
            </List>
          </Box>
        </TabPanel>
      </Paper>

      <ConfirmDialog
        open={deleteDialog}
        title="Delete Customer"
        message={`Are you sure you want to delete "${customer.company_name}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};