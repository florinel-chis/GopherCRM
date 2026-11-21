import React, { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Divider,
  FormControlLabel,
  FormHelperText,
  NativeSelect,
  Paper,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import {
  Cancel as CancelIcon,
  ContentCopy as CloneIcon,
  Save as SaveIcon,
} from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { rolesApi, type CatalogueNode, type Role, type RoleData } from '@/api/endpoints';
import { useAuth } from '@/hooks/useAuth';
import { usePermissions } from '@/hooks/usePermissions';
import { useSnackbar } from '@/hooks/useSnackbar';
import { getAPIError } from '@/utils/apiError';
import type { Permission } from '@/types';
import { CloneRoleDialog } from './CloneRoleDialog';

type Scope = Permission['scope'];
// The ticked permissions: leaf key -> the scope it is held at.
type Selection = Record<string, Scope>;

const NAME_MAX = 100;
const DESCRIPTION_MAX = 500;

// The API marks a group and a permission only a system role holds the same
// way (selectable: false), so the children decide: a node with children is a
// group, one without is a permission and is offered only when selectable.
const isGroup = (node: CatalogueNode): boolean => (node.children?.length ?? 0) > 0;

/** The catalogue without what a custom role cannot hold, and without the groups that leaves empty. */
const offered = (nodes: CatalogueNode[]): CatalogueNode[] =>
  nodes.flatMap((node) => {
    if (!isGroup(node)) return node.selectable ? [node] : [];
    const children = offered(node.children);
    return children.length > 0 ? [{ ...node, children }] : [];
  });

const leavesOf = (node: CatalogueNode): CatalogueNode[] =>
  isGroup(node) ? node.children.flatMap(leavesOf) : [node];

const canBeOwn = (leaf: CatalogueNode): boolean => (leaf.scopes ?? []).includes('own');

interface TreeProps {
  nodes: CatalogueNode[];
  selection: Selection;
  disabled: boolean;
  onToggleLeaf: (leaf: CatalogueNode, checked: boolean) => void;
  onToggleGroup: (group: CatalogueNode, checked: boolean) => void;
  onScope: (leaf: CatalogueNode, scope: Scope) => void;
}

const PermissionTree: React.FC<TreeProps> = (props) => {
  const { nodes, selection, disabled, onToggleLeaf, onToggleGroup, onScope } = props;
  return (
    <>
      {nodes.map((node) => {
        if (isGroup(node)) {
          const leaves = leavesOf(node);
          const ticked = leaves.filter((leaf) => leaf.key in selection).length;
          const all = ticked === leaves.length;
          return (
            <Box key={node.key} data-testid={`permission-group-${node.key}`} sx={{ mb: 1 }}>
              <FormControlLabel
                label={<Typography fontWeight={600}>{node.title}</Typography>}
                control={
                  <Checkbox
                    checked={all}
                    indeterminate={ticked > 0 && !all}
                    disabled={disabled}
                    onChange={() => onToggleGroup(node, !all)}
                    slotProps={{ input: { 'aria-label': node.title } }}
                  />
                }
              />
              <Box sx={{ pl: 4 }}>
                <PermissionTree {...props} nodes={node.children} />
              </Box>
            </Box>
          );
        }
        const checked = node.key in selection;
        return (
          <Box
            key={node.key}
            data-testid={`permission-${node.key}`}
            display="flex"
            alignItems="center"
            gap={2}
          >
            <FormControlLabel
              label={node.title}
              control={
                <Checkbox
                  checked={checked}
                  disabled={disabled}
                  onChange={(event) => onToggleLeaf(node, event.target.checked)}
                  slotProps={{ input: { 'aria-label': node.title } }}
                />
              }
            />
            {checked && canBeOwn(node) && (
              <NativeSelect
                value={selection[node.key]}
                disabled={disabled}
                onChange={(event) => onScope(node, event.target.value === 'own' ? 'own' : 'all')}
                inputProps={{ 'aria-label': `Scope of ${node.title}` }}
                sx={{ fontSize: '0.875rem' }}
              >
                <option value="all">All</option>
                <option value="own">Own</option>
              </NativeSelect>
            )}
          </Box>
        );
      })}
    </>
  );
};

/**
 * The role editor behind /settings/roles/new and /settings/roles/:id. A
 * system role, and any role for a caller without roles.manage, is shown
 * read-only.
 */
export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const queryClient = useQueryClient();
  const { can } = usePermissions();
  const { refreshUser } = useAuth();
  const { showSuccess, showError } = useSnackbar();
  const isNew = id === undefined;
  const roleId = Number(id);
  const canManage = can('roles.manage');

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [selection, setSelection] = useState<Selection>({});
  const [nameError, setNameError] = useState('');
  const [permissionError, setPermissionError] = useState('');
  const [cloneSource, setCloneSource] = useState<Role | null>(null);
  // The stored role is copied into the form once, so a refetch (window focus,
  // an invalidation) never overwrites what is being typed.
  const [loadedId, setLoadedId] = useState<number | null>(null);

  const catalogueQuery = useQuery({
    queryKey: ['permissions', 'catalogue'],
    queryFn: rolesApi.getCatalogue,
  });
  const roleQuery = useQuery({
    queryKey: ['roles', roleId],
    queryFn: () => rolesApi.getRole(roleId),
    enabled: !isNew,
  });
  const stored = roleQuery.data;

  const tree = useMemo(() => offered(catalogueQuery.data ?? []), [catalogueQuery.data]);
  const leaves = useMemo(() => tree.flatMap(leavesOf), [tree]);

  useEffect(() => {
    if (!stored || loadedId === stored.id) return;
    setName(stored.name);
    setDescription(stored.description ?? '');
    setSelection(
      Object.fromEntries((stored.permissions ?? []).map((entry) => [entry.key, entry.scope])),
    );
    setLoadedId(stored.id);
  }, [stored, loadedId]);

  const saveMutation = useMutation({
    mutationFn: (data: RoleData) =>
      isNew ? rolesApi.createRole(data) : rolesApi.updateRole(roleId, data),
    onSuccess: () => {
      showSuccess(isNew ? 'Role created' : 'Role saved');
      queryClient.invalidateQueries({ queryKey: ['roles'] });
      // The signed-in user may hold the saved role: reload their permissions
      // so the menus and route guards follow without waiting for a focus.
      if (!isNew) void refreshUser();
      navigate('/settings/roles');
    },
    onError: (error: unknown) => {
      const apiError = getAPIError(error);
      if (apiError?.code === 'ROLE_NAME_TAKEN') {
        setNameError('Another role already has this name');
        return;
      }
      if (apiError?.code === 'INVALID_PERMISSION') {
        const keys = apiError.details?.keys;
        const named = Array.isArray(keys) ? keys.filter((key) => typeof key === 'string') : [];
        setPermissionError(
          named.length > 0
            ? `These permissions cannot be saved: ${named.join(', ')}`
            : 'The permission list is not valid',
        );
        return;
      }
      showError(apiError?.message || 'Failed to save the role');
    },
  });

  if (catalogueQuery.isLoading || (!isNew && roleQuery.isLoading)) {
    return <Loading />;
  }
  if (catalogueQuery.isError || (!isNew && (roleQuery.isError || !stored))) {
    return (
      <Box>
        <Alert severity="error" sx={{ mb: 2 }}>
          The role could not be loaded.
        </Alert>
        <Button variant="outlined" onClick={() => navigate('/settings/roles')}>
          Back to roles
        </Button>
      </Box>
    );
  }
  if (!isNew && loadedId !== stored?.id) {
    return <Loading />;
  }

  const isSystem = !isNew && stored?.is_system === true;
  const readOnly = isSystem || !canManage;

  const toggleLeaf = (leaf: CatalogueNode, checked: boolean) => {
    setPermissionError('');
    setSelection((current) => {
      const next = { ...current };
      if (checked) next[leaf.key] = 'all';
      else delete next[leaf.key];
      return next;
    });
  };

  const toggleGroup = (group: CatalogueNode, checked: boolean) => {
    setPermissionError('');
    setSelection((current) => {
      const next = { ...current };
      for (const leaf of leavesOf(group)) {
        if (!checked) delete next[leaf.key];
        // A leaf that is already ticked keeps its scope.
        else if (!(leaf.key in next)) next[leaf.key] = 'all';
      }
      return next;
    });
  };

  const setScope = (leaf: CatalogueNode, scope: Scope) => {
    setPermissionError('');
    setSelection((current) => ({ ...current, [leaf.key]: scope }));
  };

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (readOnly || saveMutation.isPending) return;
    const trimmed = name.trim();
    if (!trimmed) {
      setNameError('Name is required');
      return;
    }
    // All three keys, always: on PUT an omitted description or permissions
    // field keeps the stored value, so "cleared" must be sent as empty.
    saveMutation.mutate({
      name: trimmed,
      description: description.trim(),
      permissions: leaves
        .filter((leaf) => leaf.key in selection)
        .map((leaf) => ({
          key: leaf.key,
          scope: canBeOwn(leaf) ? selection[leaf.key] : 'all',
        })),
    });
  };

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">{isNew ? 'New role' : readOnly ? 'Role' : 'Edit role'}</Typography>
        {isSystem && canManage && stored && (
          <Button variant="outlined" startIcon={<CloneIcon />} onClick={() => setCloneSource(stored)}>
            Clone
          </Button>
        )}
      </Box>

      {isSystem && (
        <Alert severity="info" sx={{ mb: 2 }}>
          A system role cannot be changed.{canManage ? ' Clone it to make a role of your own.' : ''}
        </Alert>
      )}
      {!isNew && !isSystem && (
        <Alert severity="info" sx={{ mb: 2 }}>
          A change applies to the users holding this role on their next request.
        </Alert>
      )}

      <Paper sx={{ p: 3 }}>
        <form onSubmit={submit} noValidate>
          <Stack spacing={3}>
            <TextField
              label="Name"
              required
              fullWidth
              value={name}
              disabled={readOnly}
              onChange={(event) => {
                setName(event.target.value);
                setNameError('');
              }}
              error={!!nameError}
              helperText={nameError || undefined}
              slotProps={{ htmlInput: { maxLength: NAME_MAX } }}
            />
            <TextField
              label="Description"
              fullWidth
              multiline
              minRows={2}
              value={description}
              disabled={readOnly}
              onChange={(event) => setDescription(event.target.value)}
              slotProps={{ htmlInput: { maxLength: DESCRIPTION_MAX } }}
            />

            <Divider />

            <Box>
              <Typography variant="h6" gutterBottom>
                Permissions
              </Typography>
              {permissionError && (
                <FormHelperText error role="alert" sx={{ mb: 1 }}>
                  {permissionError}
                </FormHelperText>
              )}
              <PermissionTree
                nodes={tree}
                selection={selection}
                disabled={readOnly}
                onToggleLeaf={toggleLeaf}
                onToggleGroup={toggleGroup}
                onScope={setScope}
              />
            </Box>

            <Box display="flex" gap={2} justifyContent="flex-end">
              <Button
                variant="outlined"
                startIcon={<CancelIcon />}
                onClick={() => navigate('/settings/roles')}
              >
                {readOnly ? 'Back' : 'Cancel'}
              </Button>
              {!readOnly && (
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={saveMutation.isPending}
                >
                  Save
                </Button>
              )}
            </Box>
          </Stack>
        </form>
      </Paper>

      <CloneRoleDialog
        source={cloneSource}
        onClose={() => setCloneSource(null)}
        onCloned={(created) => {
          setCloneSource(null);
          showSuccess('Role cloned');
          navigate(`/settings/roles/${created.id}`);
        }}
      />
    </Box>
  );
};
