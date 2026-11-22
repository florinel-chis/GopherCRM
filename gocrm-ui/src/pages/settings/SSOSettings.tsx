import React, { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControl,
  FormControlLabel,
  IconButton,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import { Add as AddIcon, DeleteOutline as DeleteOutlineIcon } from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { useRoles } from '@/hooks/useRoles';
import { useSnackbar } from '@/hooks/useSnackbar';
import {
  ssoSettingsApi,
  type SSOClaim,
  type SSORoleMapping,
  type SSOSettingSource,
  type SSOSettings as SSOSettingsData,
  type SSOSettingsUpdate,
} from '@/api/endpoints';
import { apiErrorMessage, getAPIError, getHTTPStatus } from '@/utils/apiError';

// The query keys never carry the client secret: the GET answer has none.
const ssoSettingsKey = ['sso-settings'] as const;

const SSO_CLAIMS: SSOClaim[] = ['groups', 'roles'];

interface MappingRow extends Omit<SSORoleMapping, 'role_id'> {
  // 0 until a role is chosen for a new row.
  role_id: number;
}

interface FormState {
  enabled: boolean;
  allow_password: boolean;
  issuer_url: string;
  client_id: string;
  scopes: string;
  role_mapping: MappingRow[];
}

const toForm = (settings: SSOSettingsData): FormState => ({
  enabled: settings.enabled,
  allow_password: settings.allow_password,
  issuer_url: settings.issuer_url,
  client_id: settings.client_id,
  scopes: settings.scopes.join(' '),
  role_mapping: settings.role_mapping.map((row) => ({ ...row })),
});

// Where the value in force comes from, when it is not a stored setting.
const sourceHelp = (source: SSOSettingSource | undefined): string | undefined => {
  if (source === 'env') return 'From the environment (OIDC_*)';
  if (source === 'default') return 'Default value';
  return undefined;
};

// The answer to a save that changes the issuer in force while accounts are
// linked to the current one; the page asks before resending it.
const ISSUER_CHANGE_UNCONFIRMED = 'ISSUER_CHANGE_UNCONFIRMED';

const unconfirmedIssuerLinks = (error: unknown): number | null => {
  const apiError = getAPIError(error);
  if (getHTTPStatus(error) !== 409 || apiError?.code !== ISSUER_CHANGE_UNCONFIRMED) return null;
  const links = apiError.details?.affected_links;
  return typeof links === 'number' ? links : 0;
};

const linksText = (links: number): string =>
  links === 1
    ? '1 account link uses the current issuer.'
    : `${links} account links use the current issuer.`;

export const SSOSettings: React.FC = () => {
  const queryClient = useQueryClient();
  const { showSuccess } = useSnackbar();

  const settingsQuery = useQuery({
    queryKey: ssoSettingsKey,
    queryFn: ssoSettingsApi.get,
    // A refetch would reset the form under the admin's edits.
    refetchOnWindowFocus: false,
  });
  // The shared role list, so a role written in Settings > Roles is offered here at once.
  const rolesQuery = useRoles();

  const [form, setForm] = useState<FormState | null>(null);
  // The new client secret exists only in this state while it is typed; it is
  // cleared after every save and never shown anywhere but in the input.
  const [secret, setSecret] = useState('');
  const [clearSecret, setClearSecret] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  // A refused issuer change: the count and the body to resend on confirm.
  const [issuerChange, setIssuerChange] = useState<{ links: number; body: SSOSettingsUpdate } | null>(null);

  useEffect(() => {
    if (settingsQuery.data) {
      setForm(toForm(settingsQuery.data));
    }
  }, [settingsQuery.data]);

  const mutation = useMutation({
    mutationFn: (body: SSOSettingsUpdate) => ssoSettingsApi.update(body),
    onSuccess: (updated) => {
      setSecret('');
      setClearSecret(false);
      setSaveError(null);
      queryClient.setQueryData(ssoSettingsKey, updated);
      setForm(toForm(updated));
      showSuccess('Single sign-on settings saved');
    },
    onError: (error: unknown, body: SSOSettingsUpdate) => {
      const links = body.confirm_issuer_change ? null : unconfirmedIssuerLinks(error);
      if (links !== null) {
        setIssuerChange({ links, body });
        return;
      }
      setSaveError(
        apiErrorMessage(error, 'The single sign-on settings could not be saved.', {
          details: true,
        }),
      );
    },
  });

  if (settingsQuery.isLoading || (!form && !settingsQuery.isError)) {
    return <Loading />;
  }
  if (settingsQuery.isError || !form || !settingsQuery.data) {
    return (
      <Alert severity="error">
        {apiErrorMessage(settingsQuery.error, 'The single sign-on settings could not be loaded.')}
      </Alert>
    );
  }

  const settings = settingsQuery.data;
  const roles = rolesQuery.data ?? [];

  const update = (patch: Partial<FormState>) => setForm({ ...form, ...patch });
  const updateRow = (index: number, patch: Partial<MappingRow>) =>
    update({
      role_mapping: form.role_mapping.map((row, i) => (i === index ? { ...row, ...patch } : row)),
    });

  const save = (event: React.FormEvent) => {
    event.preventDefault();
    setSaveError(null);
    const body: SSOSettingsUpdate = {
      enabled: form.enabled,
      allow_password: form.allow_password,
      issuer_url: form.issuer_url,
      client_id: form.client_id,
      scopes: form.scopes.split(/\s+/).filter(Boolean),
      role_mapping: form.role_mapping.map((row) => ({
        claim: row.claim,
        value: row.value,
        role_id: row.role_id,
      })),
    };
    // Absent keeps the stored secret; "" clears it.
    if (secret !== '') {
      body.client_secret = secret;
    } else if (clearSecret) {
      body.client_secret = '';
    }
    mutation.mutate(body);
  };

  let secretStatus = settings.client_secret_set ? 'A client secret is set' : 'No client secret is set';
  if (clearSecret && secret === '') {
    secretStatus = 'The client secret will be cleared when you save';
  }

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        Single sign-on
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        OpenID Connect sign-in. Saving stores every field shown here as a setting, so a later change
        to the OIDC_* environment no longer applies to a saved field.
      </Typography>

      {rolesQuery.isError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          The role list could not be loaded. Existing mappings keep their roles.
        </Alert>
      )}
      {saveError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {saveError}
        </Alert>
      )}

      <Paper component="form" onSubmit={save} sx={{ p: 3 }} noValidate>
        <Stack spacing={2}>
          <Box>
            <FormControlLabel
              control={
                <Switch
                  checked={form.enabled}
                  onChange={(event) => update({ enabled: event.target.checked })}
                />
              }
              label="Enabled"
            />
            {sourceHelp(settings.sources.enabled) && (
              <Typography variant="caption" color="text.secondary" display="block">
                {sourceHelp(settings.sources.enabled)}
              </Typography>
            )}
          </Box>
          <Box>
            <FormControlLabel
              control={
                <Switch
                  checked={form.allow_password}
                  onChange={(event) => update({ allow_password: event.target.checked })}
                />
              }
              label="Allow password sign-in and password reset for accounts linked to single sign-on"
            />
            {sourceHelp(settings.sources.allow_password) && (
              <Typography variant="caption" color="text.secondary" display="block">
                {sourceHelp(settings.sources.allow_password)}
              </Typography>
            )}
            <Alert severity="warning" sx={{ mt: 1 }} data-testid="sso-allow-password-warning">
              When this is on, a password set by an account linked to single sign-on keeps working after
              the person is removed from the identity provider or loses the mapped role. When offboarding
              someone, also deactivate their account in GopherCRM. Accounts not linked to single sign-on
              can always use their password.
            </Alert>
          </Box>
          <TextField
            label="Issuer URL"
            value={form.issuer_url}
            onChange={(event) => update({ issuer_url: event.target.value })}
            helperText={sourceHelp(settings.sources.issuer_url)}
            fullWidth
          />
          <TextField
            label="Client ID"
            value={form.client_id}
            onChange={(event) => update({ client_id: event.target.value })}
            helperText={sourceHelp(settings.sources.client_id)}
            fullWidth
          />
          <TextField
            label="Scopes"
            value={form.scopes}
            onChange={(event) => update({ scopes: event.target.value })}
            helperText={sourceHelp(settings.sources.scopes) ?? 'Separated by spaces'}
            fullWidth
          />
          <Box>
            <TextField
              label="Client secret"
              type="password"
              autoComplete="new-password"
              value={secret}
              onChange={(event) => {
                setSecret(event.target.value);
                setClearSecret(false);
              }}
              helperText={sourceHelp(settings.sources.client_secret) ?? 'Leave empty to keep the current secret'}
              fullWidth
            />
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, mt: 1 }}>
              <Typography variant="body2" data-testid="sso-secret-status">
                {secretStatus}
              </Typography>
              <Button
                size="small"
                onClick={() => {
                  setSecret('');
                  setClearSecret(true);
                }}
              >
                Clear client secret
              </Button>
            </Box>
          </Box>

          <Box>
            <Typography variant="h6" component="h2">
              Role mapping
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
              Rows are checked in order; the first row whose claim carries the value decides the role.
              {sourceHelp(settings.sources.role_mapping) ? ` ${sourceHelp(settings.sources.role_mapping)}.` : ''}
            </Typography>
            <Stack spacing={1}>
              {form.role_mapping.map((row, index) => {
                const known = roles.some((role) => role.id === row.role_id);
                return (
                  <Box
                    key={index}
                    data-testid={`sso-mapping-row-${index}`}
                    sx={{ display: 'flex', gap: 1, alignItems: 'center' }}
                  >
                    <FormControl sx={{ minWidth: 140 }} size="small">
                      <InputLabel id={`sso-claim-${index}`}>Claim</InputLabel>
                      <Select
                        labelId={`sso-claim-${index}`}
                        label="Claim"
                        value={row.claim}
                        onChange={(event) => updateRow(index, { claim: event.target.value as SSOClaim })}
                      >
                        {SSO_CLAIMS.map((claim) => (
                          <MenuItem key={claim} value={claim}>
                            {claim}
                          </MenuItem>
                        ))}
                      </Select>
                    </FormControl>
                    <TextField
                      size="small"
                      label="Value"
                      value={row.value}
                      onChange={(event) => updateRow(index, { value: event.target.value })}
                      sx={{ flex: 1 }}
                    />
                    <FormControl sx={{ minWidth: 180 }} size="small">
                      <InputLabel id={`sso-role-${index}`}>Role</InputLabel>
                      <Select
                        labelId={`sso-role-${index}`}
                        label="Role"
                        value={row.role_id === 0 ? '' : String(row.role_id)}
                        onChange={(event) => updateRow(index, { role_id: Number(event.target.value) })}
                      >
                        {roles.map((role) => (
                          <MenuItem key={role.id} value={String(role.id)}>
                            {role.name}
                          </MenuItem>
                        ))}
                        {row.role_id !== 0 && !known && (
                          <MenuItem value={String(row.role_id)}>Role #{row.role_id}</MenuItem>
                        )}
                      </Select>
                    </FormControl>
                    <Tooltip title="Remove mapping">
                      <IconButton
                        aria-label={`Remove mapping ${index + 1}`}
                        onClick={() =>
                          update({ role_mapping: form.role_mapping.filter((_, i) => i !== index) })
                        }
                      >
                        <DeleteOutlineIcon />
                      </IconButton>
                    </Tooltip>
                  </Box>
                );
              })}
            </Stack>
            <Button
              startIcon={<AddIcon />}
              sx={{ mt: 1 }}
              onClick={() =>
                update({ role_mapping: [...form.role_mapping, { claim: 'groups', value: '', role_id: 0 }] })
              }
            >
              Add mapping
            </Button>
          </Box>

          <Box>
            <Button type="submit" variant="contained" disabled={mutation.isPending}>
              {mutation.isPending ? 'Saving...' : 'Save'}
            </Button>
          </Box>
        </Stack>
      </Paper>

      <Dialog
        open={issuerChange !== null}
        onClose={() => setIssuerChange(null)}
        aria-labelledby="sso-issuer-change-title"
        aria-describedby="sso-issuer-change-text"
      >
        <DialogTitle id="sso-issuer-change-title">Change the issuer?</DialogTitle>
        <DialogContent>
          <DialogContentText id="sso-issuer-change-text">
            {issuerChange ? linksText(issuerChange.links) : ''} After the change these links stop
            matching: those people are not recognised at their next sign-in, and provisioning no longer
            sees them.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setIssuerChange(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            onClick={() => {
              if (!issuerChange) return;
              const body = { ...issuerChange.body, confirm_issuer_change: true };
              setIssuerChange(null);
              mutation.mutate(body);
            }}
          >
            Change issuer
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};

export function Component() {
  return <SSOSettings />;
}

Component.displayName = 'SSOSettings';
