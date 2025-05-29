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
  IconButton,
  TextField,
  List,
  ListItem,
  ListItemText,
  ListItemAvatar,
  Avatar,
  Card,
  CardContent,
} from '@mui/material';
import {
  Edit as EditIcon,
  Delete as DeleteIcon,
  Person as PersonIcon,
  Business as BusinessIcon,
  CalendarToday as CalendarIcon,
  Assignment as AssignmentIcon,
  Flag as FlagIcon,
  Message as MessageIcon,
  Send as SendIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { useSnackbar } from '@/hooks/useSnackbar';
import { ticketsApi } from '@/api/endpoints';
import type { Ticket } from '@/types';
import { format } from 'date-fns';

const getStatusColor = (status: Ticket['status']) => {
  switch (status) {
    case 'open':
      return 'info';
    case 'in_progress':
      return 'warning';
    case 'resolved':
      return 'success';
    case 'closed':
      return 'default';
    default:
      return 'default';
  }
};

const getPriorityColor = (priority: Ticket['priority']) => {
  switch (priority) {
    case 'low':
      return 'default';
    case 'medium':
      return 'primary';
    case 'high':
      return 'warning';
    case 'urgent':
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
  
  const [deleteDialog, setDeleteDialog] = useState(false);
  const [comment, setComment] = useState('');

  const { data: ticket, isLoading } = useQuery({
    queryKey: ['ticket', id],
    queryFn: () => ticketsApi.getTicket(Number(id)),
    enabled: !!id,
  });

  const deleteMutation = useMutation({
    mutationFn: () => ticketsApi.deleteTicket(Number(id)),
    onSuccess: () => {
      showSuccess('Ticket deleted successfully');
      queryClient.invalidateQueries({ queryKey: ['tickets'] });
      navigate('/tickets');
    },
    onError: () => {
      showError('Failed to delete ticket');
    },
  });

  const addCommentMutation = useMutation({
    mutationFn: (content: string) => ticketsApi.addComment(Number(id), { content }),
    onSuccess: () => {
      showSuccess('Comment added successfully');
      queryClient.invalidateQueries({ queryKey: ['ticket', id] });
      setComment('');
    },
    onError: () => {
      showError('Failed to add comment');
    },
  });

  const handleAddComment = (e: React.FormEvent) => {
    e.preventDefault();
    if (comment.trim()) {
      addCommentMutation.mutate(comment);
    }
  };

  if (isLoading || !ticket) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Box display="flex" alignItems="center" gap={2}>
          <Typography variant="h4">Ticket #{ticket.id}</Typography>
          <Chip
            label={ticket.status.split('_').map(s => s.charAt(0).toUpperCase() + s.slice(1)).join(' ')}
            color={getStatusColor(ticket.status)}
          />
          <Chip
            label={ticket.priority.charAt(0).toUpperCase() + ticket.priority.slice(1)}
            color={getPriorityColor(ticket.priority)}
            size="small"
          />
        </Box>
        <Box display="flex" gap={1}>
          <Button
            variant="outlined"
            startIcon={<EditIcon />}
            onClick={() => navigate(`/tickets/${id}/edit`)}
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

      <Stack spacing={3}>
        <Paper sx={{ p: 3 }}>
          <Typography variant="h5" gutterBottom>
            {ticket.subject}
          </Typography>
          
          <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={3} mt={3}>
            <Box>
              <Box display="flex" alignItems="center" gap={1} mb={2}>
                <BusinessIcon color="action" />
                <Box>
                  <Typography variant="caption" color="text.secondary">
                    Customer
                  </Typography>
                  <Typography>
                    {ticket.customer?.company_name || 'N/A'}
                  </Typography>
                </Box>
              </Box>
              
              <Box display="flex" alignItems="center" gap={1} mb={2}>
                <PersonIcon color="action" />
                <Box>
                  <Typography variant="caption" color="text.secondary">
                    Created By
                  </Typography>
                  <Typography>
                    {ticket.created_by ? `${ticket.created_by.first_name} ${ticket.created_by.last_name}` : 'Unknown'}
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
                    {format(new Date(ticket.created_at), 'MMM dd, yyyy HH:mm')}
                  </Typography>
                </Box>
              </Box>
            </Box>
            
            <Box>
              <Box display="flex" alignItems="center" gap={1} mb={2}>
                <AssignmentIcon color="action" />
                <Box>
                  <Typography variant="caption" color="text.secondary">
                    Assigned To
                  </Typography>
                  <Typography>
                    {ticket.assigned_to ? `${ticket.assigned_to.first_name} ${ticket.assigned_to.last_name}` : 'Unassigned'}
                  </Typography>
                </Box>
              </Box>
              
              <Box display="flex" alignItems="center" gap={1} mb={2}>
                <FlagIcon color="action" />
                <Box>
                  <Typography variant="caption" color="text.secondary">
                    Priority
                  </Typography>
                  <Typography>
                    {ticket.priority.charAt(0).toUpperCase() + ticket.priority.slice(1)}
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
                    {format(new Date(ticket.updated_at), 'MMM dd, yyyy HH:mm')}
                  </Typography>
                </Box>
              </Box>
            </Box>
          </Box>

          <Divider sx={{ my: 3 }} />

          <Typography variant="h6" gutterBottom>
            Description
          </Typography>
          <Typography variant="body1" sx={{ whiteSpace: 'pre-wrap' }}>
            {ticket.description}
          </Typography>
        </Paper>

        <Paper sx={{ p: 3 }}>
          <Box display="flex" alignItems="center" gap={1} mb={3}>
            <MessageIcon />
            <Typography variant="h6">Comments</Typography>
          </Box>

          {ticket.comments && ticket.comments.length > 0 ? (
            <List sx={{ mb: 3 }}>
              {ticket.comments.map((comment) => (
                <ListItem key={comment.id} alignItems="flex-start" sx={{ px: 0 }}>
                  <ListItemAvatar>
                    <Avatar>
                      {comment.user?.first_name?.charAt(0) || 'U'}
                    </Avatar>
                  </ListItemAvatar>
                  <ListItemText
                    primary={
                      <Box display="flex" justifyContent="space-between" alignItems="center">
                        <Typography variant="subtitle2">
                          {comment.user ? `${comment.user.first_name} ${comment.user.last_name}` : 'Unknown User'}
                        </Typography>
                        <Typography variant="caption" color="text.secondary">
                          {format(new Date(comment.created_at), 'MMM dd, yyyy HH:mm')}
                        </Typography>
                      </Box>
                    }
                    secondary={
                      <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap', mt: 1 }}>
                        {comment.content}
                      </Typography>
                    }
                  />
                </ListItem>
              ))}
            </List>
          ) : (
            <Typography color="text.secondary" sx={{ mb: 3 }}>
              No comments yet
            </Typography>
          )}

          <Card variant="outlined">
            <CardContent>
              <form onSubmit={handleAddComment}>
                <TextField
                  fullWidth
                  multiline
                  rows={3}
                  placeholder="Add a comment..."
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                  sx={{ mb: 2 }}
                />
                <Box display="flex" justifyContent="flex-end">
                  <Button
                    type="submit"
                    variant="contained"
                    startIcon={<SendIcon />}
                    disabled={!comment.trim() || addCommentMutation.isPending}
                  >
                    Add Comment
                  </Button>
                </Box>
              </form>
            </CardContent>
          </Card>
        </Paper>
      </Stack>

      <ConfirmDialog
        open={deleteDialog}
        title="Delete Ticket"
        message={`Are you sure you want to delete ticket #${ticket.id}? This action cannot be undone.`}
        severity="error"
        confirmText="Delete"
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setDeleteDialog(false)}
      />
    </Box>
  );
};