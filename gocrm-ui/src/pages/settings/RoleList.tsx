import React, { useState } from 'react';
import { Link as RouterLink, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Link,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { Add as AddIcon } from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { rolesApi, type Role } from '@/api/endpoints';
import { usePermissions } from '@/hooks/usePermissions';
import { useSnackbar } from '@/hooks/useSnackbar';
import { getAPIError } from '@/utils/apiError';
import { CloneRoleDialog } from './CloneRoleDialog';

const holdersSentence = (count: number): string =>
  `Still assigned to ${count} ${count === 1 ? 'user' : 'users'}`;

/**
 * Settings > Roles: the four system roles and the custom ones. A system role
 * can only be cloned; a custom role can be edited, cloned and deleted. Every
 * action needs roles.manage; without it the rows link to the read-only editor.
 */
export const Component: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { can } = usePermissions();
  const { showSuccess, showError } = useSnackbar();
  const canManage = can('roles.manage');

  const [cloneSource, setCloneSource] = useState<Role | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Role | null>(null);
  const [deleteError, setDeleteError] = useState('');

  const {
    data: roles,
    isLoading,
    isError,
  } = useQuery({ queryKey: ['roles'], queryFn: rolesApi.getRoles });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => rolesApi.deleteRole(id),
    onSuccess: () => {
      showSuccess('Role deleted');
      queryClient.invalidateQueries({ queryKey: ['roles'] });
      setDeleteTarget(null);
    },
    onError: (error: unknown) => {
      const apiError = getAPIError(error);
      if (apiError?.code === 'ROLE_IN_USE') {
        // The dialog stays open and names the number of holders.
        const count = apiError.details?.user_count;
        setDeleteError(
          typeof count === 'number'
            ? `${holdersSentence(count)}. Give them another role first.`
            : 'The role is still assigned to users. Give them another role first.',
        );
        queryClient.invalidateQueries({ queryKey: ['roles'] });
        return;
      }
      showError(apiError?.message || 'Failed to delete the role');
      setDeleteTarget(null);
    },
  });

  const openDelete = (target: Role) => {
    setDeleteError('');
    setDeleteTarget(target);
  };

  if (isLoading) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Roles</Typography>
        {canManage && (
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => navigate('/settings/roles/new')}
          >
            New role
          </Button>
        )}
      </Box>

      {isError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          The roles could not be loaded.
        </Alert>
      )}

      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>Type</TableCell>
              <TableCell align="right">Users</TableCell>
              <TableCell align="right">Permissions</TableCell>
              {canManage && <TableCell align="right">Actions</TableCell>}
            </TableRow>
          </TableHead>
          <TableBody>
            {(roles ?? []).map((item) => (
              <TableRow key={item.id} hover>
                <TableCell>
                  <Link component={RouterLink} to={`/settings/roles/${item.id}`} underline="hover">
                    {item.name}
                  </Link>
                  {item.description && (
                    <Typography variant="body2" color="text.secondary">
                      {item.description}
                    </Typography>
                  )}
                </TableCell>
                <TableCell>
                  <Chip
                    size="small"
                    label={item.is_system ? 'System' : 'Custom'}
                    color={item.is_system ? 'default' : 'primary'}
                    variant="outlined"
                  />
                </TableCell>
                <TableCell align="right">{item.user_count}</TableCell>
                <TableCell align="right">{item.permissions?.length ?? 0}</TableCell>
                {canManage && (
                  <TableCell align="right">
                    {!item.is_system && (
                      <Button
                        size="small"
                        aria-label={`Edit ${item.name}`}
                        onClick={() => navigate(`/settings/roles/${item.id}`)}
                      >
                        Edit
                      </Button>
                    )}
                    <Button
                      size="small"
                      aria-label={`Clone ${item.name}`}
                      onClick={() => setCloneSource(item)}
                    >
                      Clone
                    </Button>
                    {!item.is_system && (
                      <Button
                        size="small"
                        color="error"
                        aria-label={`Delete ${item.name}`}
                        onClick={() => openDelete(item)}
                      >
                        Delete
                      </Button>
                    )}
                  </TableCell>
                )}
              </TableRow>
            ))}
            {!isError && (roles ?? []).length === 0 && (
              <TableRow>
                <TableCell colSpan={canManage ? 5 : 4} align="center">
                  No roles
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </TableContainer>

      <CloneRoleDialog
        source={cloneSource}
        onClose={() => setCloneSource(null)}
        onCloned={(created) => {
          setCloneSource(null);
          showSuccess('Role cloned');
          navigate(`/settings/roles/${created.id}`);
        }}
      />

      <Dialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        fullWidth
        maxWidth="xs"
        aria-labelledby="delete-role-title"
      >
        <DialogTitle id="delete-role-title">Delete role</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Delete the role {deleteTarget?.name}? A role that users still hold cannot be deleted.
          </DialogContentText>
          {deleteError && (
            <Alert severity="error" sx={{ mt: 2 }}>
              {deleteError}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button
            color="error"
            variant="contained"
            disabled={deleteMutation.isPending}
            onClick={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
          >
            Delete
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};
