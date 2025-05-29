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
  Tooltip,
} from '@mui/material';
import {
  Edit as EditIcon,
  Delete as DeleteIcon,
  SwapHoriz as ConvertIcon,
  Phone as PhoneIcon,
  Email as EmailIcon,
  Business as BusinessIcon,
  Person as PersonIcon,
  CalendarToday as CalendarIcon,
  Source as SourceIcon,
  Notes as NotesIcon,
  History as HistoryIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { ConvertLeadDialog, type ConvertLeadData } from '@/components/ConvertLeadDialog';
import { useSnackbar } from '@/hooks/useSnackbar';
import { useConfiguration } from '@/contexts/ConfigurationContext';
import { leadsApi } from '@/api/endpoints';
import type { Lead } from '@/types';
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

const getStatusColor = (status: Lead['status']) => {
  switch (status) {
    case 'new':
      return 'info';
    case 'contacted':
      return 'primary';
    case 'qualified':
      return 'warning';
    case 'converted':
      return 'success';
    case 'lost':
      return 'error';
    default:
      return 'default';
  }
};

export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const { getLeadConversionStatuses } = useConfiguration();
  
  const [tabValue, setTabValue] = useState(0);
  const [deleteDialog, setDeleteDialog] = useState(false);
  const [convertDialog, setConvertDialog] = useState(false);

  const { data: lead, isLoading } = useQuery({
    queryKey: ['lead', id],
    queryFn: () => leadsApi.getLead(Number(id)),
    enabled: !!id,
  });

  const deleteMutation = useMutation({
    mutationFn: () => leadsApi.deleteLead(Number(id)),
    onSuccess: () => {
      showSuccess('Lead deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      navigate('/leads');
    },
    onError: () => {
      showError('Failed to delete lead');
    },
  });

  const convertMutation = useMutation({
    mutationFn: (data: ConvertLeadData) => leadsApi.convertLead(Number(id), data),
    onSuccess: (data) => {
      showSuccess('Lead converted to customer successfully');
      queryClient.invalidateQueries({ queryKey: ['leads'] });
      navigate(`/customers/${data.customer_id}`);
    },
    onError: () => {
      showError('Failed to convert lead');
    },
  });

  const handleConvert = (data: ConvertLeadData) => {
    convertMutation.mutate(data);
    setConvertDialog(false);
  };

  if (isLoading || !lead) {
    return <Loading />;
  }

  const activities = [
    {
      id: 1,
      type: 'created',
      description: 'Lead created',
      user: 'System',
      timestamp: lead.created_at,
    },
    {
      id: 2,
      type: 'status_change',
      description: `Status changed to ${lead.status}`,
      user: lead.owner?.username || 'Unknown',
      timestamp: lead.updated_at,
    },
  ];

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Box display="flex" alignItems="center" gap={2}>
          <Typography variant="h4">{lead.company_name}</Typography>
          <Chip
            label={lead.status.charAt(0).toUpperCase() + lead.status.slice(1)}
            color={getStatusColor(lead.status)}
          />
        </Box>
        <Box display="flex" gap={1}>
          {getLeadConversionStatuses().includes(lead.status) && (
            <Button
              variant="contained"
              color="success"
              startIcon={<ConvertIcon />}
              onClick={() => setConvertDialog(true)}
              sx={{ mr: 1 }}
            >
              Convert to Customer
            </Button>
          )}
          <Button
            variant="outlined"
            startIcon={<EditIcon />}
            onClick={() => navigate(`/leads/${id}/edit`)}
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
          <Tab label="Activities" />
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
                        Contact Name
                      </Typography>
                      <Typography>{lead.contact_name}</Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <BusinessIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Company
                      </Typography>
                      <Typography>{lead.company_name}</Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <EmailIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Email
                      </Typography>
                      <Typography>
                        <a href={`mailto:${lead.email}`}>{lead.email}</a>
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
                        <a href={`tel:${lead.phone}`}>{lead.phone}</a>
                      </Typography>
                    </Box>
                  </Box>
                </Box>
              </Box>

              <Divider />

              <Box>
                <Typography variant="h6" gutterBottom>
                  Lead Information
                </Typography>
                <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                  <Box display="flex" alignItems="center" gap={1}>
                    <SourceIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Source
                      </Typography>
                      <Typography>{lead.source}</Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <PersonIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Owner
                      </Typography>
                      <Typography>
                        {lead.owner ? `${lead.owner.first_name} ${lead.owner.last_name}` : 'Unassigned'}
                      </Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <CalendarIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Created
                      </Typography>
                      <Typography>
                        {format(new Date(lead.created_at), 'MMM dd, yyyy HH:mm')}
                      </Typography>
                    </Box>
                  </Box>
                  <Box display="flex" alignItems="center" gap={1}>
                    <CalendarIcon color="action" />
                    <Box>
                      <Typography variant="caption" color="text.secondary">
                        Last Updated
                      </Typography>
                      <Typography>
                        {format(new Date(lead.updated_at), 'MMM dd, yyyy HH:mm')}
                      </Typography>
                    </Box>
                  </Box>
                </Box>
              </Box>

              {lead.notes && (
                <>
                  <Divider />
                  <Box>
                    <Box display="flex" alignItems="center" gap={1} mb={1}>
                      <NotesIcon color="action" />
                      <Typography variant="h6">Notes</Typography>
                    </Box>
                    <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>
                      {lead.notes}
                    </Typography>
                  </Box>
                </>
              )}
            </Stack>
          </Box>
        </TabPanel>

        <TabPanel value={tabValue} index={1}>
          <Box sx={{ p: 3 }}>
            <Box display="flex" alignItems="center" gap={1} mb={2}>
              <HistoryIcon />
              <Typography variant="h6">Activity Timeline</Typography>
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
        title="Delete Lead"
        message={`Are you sure you want to delete "${lead.company_name}"? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />

      <ConvertLeadDialog
        open={convertDialog}
        lead={lead}
        onClose={() => setConvertDialog(false)}
        onConfirm={handleConvert}
        isLoading={convertMutation.isPending}
      />
    </Box>
  );
};