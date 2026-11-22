import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@/test/test-utils';
import { Component as SSOSettings } from './SSOSettings';
import { rolesApi, ssoSettingsApi, type SSOSettings as SSOSettingsData } from '@/api/endpoints';
import type { Role } from '@/api/endpoints/roles';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';

vi.mock('@/api/endpoints', () => ({
  ssoSettingsApi: {
    get: vi.fn(),
    update: vi.fn(),
  },
  rolesApi: {
    getRoles: vi.fn(),
  },
}));

const SECRET = 'oidc-client-secret-6Hq2';

const role = (id: number, name: string): Role => ({
  id,
  name,
  description: '',
  system_key: null,
  is_system: false,
  user_count: 0,
  permissions: [],
});

const ROLES: Role[] = [role(1, 'admin'), role(2, 'sales'), role(3, 'support')];

const ALLOW_PASSWORD_LABEL = 'Allow password sign-in and password reset for accounts linked to single sign-on';

const stored: SSOSettingsData = {
  enabled: true,
  allow_password: true,
  issuer_url: 'https://issuer.example/tenant',
  client_id: 'gophercrm',
  scopes: ['openid', 'profile', 'email'],
  role_mapping: [
    { claim: 'groups', value: 'crm-admins', role_id: 1 },
    { claim: 'roles', value: 'crm-sales', role_id: 2 },
  ],
  client_secret_set: true,
  client_certificate_set: false,
  client_certificate_thumbprint: '',
  client_certificate_expires_at: '',
  sources: {
    enabled: 'settings',
    allow_password: 'settings',
    issuer_url: 'settings',
    client_id: 'env',
    client_secret: 'settings',
    scopes: 'default',
    role_mapping: 'settings',
    client_certificate: 'default',
  },
};

const CERT_PEM =
  '-----BEGIN CERTIFICATE-----\nTUlJQ2VydGlmaWNhdGVGaXh0dXJl\n-----END CERTIFICATE-----\n';
// Not a key: placeholder text in the PEM shape, the label built so that no
// tracked file carries a PEM key header.
const KEY_LABEL = ['PRIVATE', 'KEY'].join(' ');
const KEY_PEM = `-----BEGIN ${KEY_LABEL}-----\nTm90QVJlYWxQcml2YXRlS2V5Rm9yVGVzdHM\n-----END ${KEY_LABEL}-----\n`;
const THUMBPRINT = '3C5A3F0B2E9D4C1A7B6E8F9012345678ABCDEF01';

const withCertificate: SSOSettingsData = {
  ...stored,
  client_certificate_set: true,
  client_certificate_thumbprint: THUMBPRINT,
  client_certificate_expires_at: '2099-01-15T10:00:00Z',
  sources: { ...stored.sources, client_certificate: 'settings' },
};


const lastBody = () => {
  const calls = vi.mocked(ssoSettingsApi.update).mock.calls;
  return calls[calls.length - 1][0];
};

const save = async () => {
  fireEvent.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(ssoSettingsApi.update).toHaveBeenCalled());
};

const renderPage = async () => {
  const view = render(<SSOSettings />);
  await screen.findByLabelText('Issuer URL');
  return view;
};

describe('SSOSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(ssoSettingsApi.get).mockResolvedValue(stored);
    vi.mocked(ssoSettingsApi.update).mockImplementation(async (body) => ({
      ...stored,
      enabled: body.enabled,
      allow_password: body.allow_password,
      issuer_url: body.issuer_url,
      client_id: body.client_id,
      scopes: body.scopes,
      role_mapping: body.role_mapping,
      client_secret_set: body.client_secret === undefined ? stored.client_secret_set : body.client_secret !== '',
      ...(body.client_certificate === undefined
        ? {}
        : body.client_certificate === ''
          ? {
              client_certificate_set: false,
              client_certificate_thumbprint: '',
              client_certificate_expires_at: '',
            }
          : {
              client_certificate_set: true,
              client_certificate_thumbprint: THUMBPRINT,
              client_certificate_expires_at: '2099-01-15T10:00:00Z',
            }),
    }));
    vi.mocked(rolesApi.getRoles).mockResolvedValue(ROLES);
  });

  it('shows the effective values, the sources and the secret status, never a secret', async () => {
    const { container } = await renderPage();

    expect(screen.getByLabelText('Issuer URL')).toHaveValue('https://issuer.example/tenant');
    expect(screen.getByLabelText('Client ID')).toHaveValue('gophercrm');
    expect(screen.getByLabelText('Scopes')).toHaveValue('openid profile email');
    expect(screen.getByLabelText('Enabled')).toBeChecked();
    expect(screen.getByText('From the environment (OIDC_*)')).toBeInTheDocument();
    expect(screen.getByText('Default value')).toBeInTheDocument();
    expect(screen.getByText('A client secret is set')).toBeInTheDocument();
    expect(screen.getByLabelText('Client secret')).toHaveValue('');
    expect(screen.getByText(/first row whose claim carries the value decides the role/)).toBeInTheDocument();
    expect(container.innerHTML).not.toContain(SECRET);
  });

  it('shows the password toggle and its offboarding warning, and sends allow_password', async () => {
    await renderPage();

    const toggle = screen.getByLabelText(ALLOW_PASSWORD_LABEL);
    expect(toggle).toBeChecked();
    expect(
      screen.getByText(/keeps working after the person is removed from the identity provider or loses the mapped role/),
    ).toBeInTheDocument();
    expect(screen.getByTestId('sso-allow-password-warning')).toHaveTextContent(/deactivate their account in GopherCRM/);

    fireEvent.click(toggle);
    expect(toggle).not.toBeChecked();
    await save();
    expect(lastBody().allow_password).toBe(false);

    await waitFor(() => expect(screen.getByLabelText(ALLOW_PASSWORD_LABEL)).not.toBeChecked());
    fireEvent.click(screen.getByLabelText(ALLOW_PASSWORD_LABEL));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(ssoSettingsApi.update).toHaveBeenCalledTimes(2));
    expect(lastBody().allow_password).toBe(true);
  });

  it('shows the toggle off when the stored value is false, the warning still shown', async () => {
    vi.mocked(ssoSettingsApi.get).mockResolvedValue({
      ...stored,
      allow_password: false,
      sources: { ...stored.sources, allow_password: 'settings' },
    });
    await renderPage();
    expect(screen.getByLabelText(ALLOW_PASSWORD_LABEL)).not.toBeChecked();
    expect(screen.getByTestId('sso-allow-password-warning')).toBeInTheDocument();
  });

  it('reports that no client secret is set', async () => {
    vi.mocked(ssoSettingsApi.get).mockResolvedValue({ ...stored, client_secret_set: false });
    await renderPage();
    expect(screen.getByText('No client secret is set')).toBeInTheDocument();
  });

  it('sends the mapping unchanged and in order, and no client_secret key without a typed secret', async () => {
    await renderPage();
    await waitFor(() => expect(within(screen.getByTestId('sso-mapping-row-1')).getByText('sales')).toBeInTheDocument());

    await save();

    const body = lastBody();
    expect(body).not.toHaveProperty('client_secret');
    expect(body).toEqual({
      enabled: true,
      allow_password: true,
      issuer_url: 'https://issuer.example/tenant',
      client_id: 'gophercrm',
      scopes: ['openid', 'profile', 'email'],
      role_mapping: [
        { claim: 'groups', value: 'crm-admins', role_id: 1 },
        { claim: 'roles', value: 'crm-sales', role_id: 2 },
      ],
    });
  });

  it('sends a typed secret once and drops it from the page after the save', async () => {
    const { container } = await renderPage();

    fireEvent.change(screen.getByLabelText('Client secret'), { target: { value: SECRET } });
    await save();

    expect(lastBody().client_secret).toBe(SECRET);
    await waitFor(() => expect(screen.getByLabelText('Client secret')).toHaveValue(''));
    expect(container.innerHTML).not.toContain(SECRET);
    expect(screen.getByText('A client secret is set')).toBeInTheDocument();
  });

  it('sends an empty client_secret after "Clear client secret"', async () => {
    await renderPage();

    fireEvent.click(screen.getByRole('button', { name: 'Clear client secret' }));
    await save();

    expect(lastBody()).toHaveProperty('client_secret', '');
    expect(await screen.findByText('No client secret is set')).toBeInTheDocument();
  });

  it('sends an added row with the role chosen from the role list', async () => {
    await renderPage();

    fireEvent.click(screen.getByRole('button', { name: 'Add mapping' }));
    const row = within(screen.getByTestId('sso-mapping-row-2'));
    fireEvent.change(row.getByLabelText('Value'), { target: { value: 'crm-support' } });
    fireEvent.mouseDown(row.getByRole('combobox', { name: 'Role' }));
    fireEvent.click(await screen.findByRole('option', { name: 'support' }));
    await save();

    expect(lastBody().role_mapping).toEqual([
      { claim: 'groups', value: 'crm-admins', role_id: 1 },
      { claim: 'roles', value: 'crm-sales', role_id: 2 },
      { claim: 'groups', value: 'crm-support', role_id: 3 },
    ]);
  });

  it('removes a row', async () => {
    await renderPage();

    fireEvent.click(screen.getByRole('button', { name: 'Remove mapping 1' }));
    await save();

    expect(lastBody().role_mapping).toEqual([{ claim: 'roles', value: 'crm-sales', role_id: 2 }]);
  });

  it('keeps the role ids of existing rows when the role list fails', async () => {
    vi.mocked(rolesApi.getRoles).mockRejectedValue(new Error('network down'));
    await renderPage();

    expect(await screen.findByText(/The role list could not be loaded/)).toBeInTheDocument();
    expect(within(screen.getByTestId('sso-mapping-row-0')).getByText('Role #1')).toBeInTheDocument();
    await save();

    expect(lastBody().role_mapping).toEqual(stored.role_mapping);
  });

  it('keeps a role id the role list does not name', async () => {
    vi.mocked(ssoSettingsApi.get).mockResolvedValue({
      ...stored,
      role_mapping: [{ claim: 'groups', value: 'legacy', role_id: 42 }],
    });
    await renderPage();

    expect(within(screen.getByTestId('sso-mapping-row-0')).getByText('Role #42')).toBeInTheDocument();
    await save();

    expect(lastBody().role_mapping).toEqual([{ claim: 'groups', value: 'legacy', role_id: 42 }]);
  });

  describe('client certificate', () => {
    it('reports that no client certificate is set and sends neither key by default', async () => {
      await renderPage();
      expect(screen.getByTestId('sso-certificate-status')).toHaveTextContent(
        'No client certificate is set',
      );
      expect(screen.getByLabelText('Client certificate (PEM)')).toHaveValue('');
      expect(screen.getByLabelText('Client private key (PEM)')).toHaveValue('');
      await save();
      expect(lastBody()).not.toHaveProperty('client_certificate');
      expect(lastBody()).not.toHaveProperty('client_private_key');
    });

    it('shows the thumbprint and the expiry of a stored certificate', async () => {
      vi.mocked(ssoSettingsApi.get).mockResolvedValue(withCertificate);
      await renderPage();
      const status = screen.getByTestId('sso-certificate-status');
      expect(status).toHaveTextContent(
        `A client certificate is set (thumbprint ${THUMBPRINT}, expires Jan 15, 2099); it is used instead of the client secret`,
      );
      expect(status).not.toHaveTextContent('expired');
    });

    it('makes an expired certificate visible', async () => {
      vi.mocked(ssoSettingsApi.get).mockResolvedValue({
        ...withCertificate,
        client_certificate_expires_at: '2020-03-01T00:00:00Z',
      });
      await renderPage();
      expect(screen.getByTestId('sso-certificate-status')).toHaveTextContent(
        /expires Mar 1, 2020.*It has expired/,
      );
    });

    it('sends both keys when one input is filled, and empties both inputs after the save', async () => {
      const { container } = await renderPage();
      fireEvent.change(screen.getByLabelText('Client certificate (PEM)'), {
        target: { value: CERT_PEM },
      });
      fireEvent.change(screen.getByLabelText('Client private key (PEM)'), {
        target: { value: KEY_PEM },
      });
      await save();
      expect(lastBody().client_certificate).toBe(CERT_PEM);
      expect(lastBody().client_private_key).toBe(KEY_PEM);
      await waitFor(() =>
        expect(screen.getByLabelText('Client private key (PEM)')).toHaveValue(''),
      );
      expect(screen.getByLabelText('Client certificate (PEM)')).toHaveValue('');
      expect(screen.getByTestId('sso-certificate-status')).toHaveTextContent(
        `thumbprint ${THUMBPRINT}`,
      );
      expect(container.innerHTML).not.toContain(KEY_LABEL);
      expect(container.innerHTML).not.toContain('Tm90QVJlYWxQcml2YXRlS2V5');

      vi.mocked(ssoSettingsApi.update).mockClear();
      fireEvent.change(screen.getByLabelText('Client certificate (PEM)'), {
        target: { value: CERT_PEM },
      });
      await save();
      expect(lastBody()).toHaveProperty('client_certificate', CERT_PEM);
      expect(lastBody()).toHaveProperty('client_private_key', '');
    });

    it('sends both keys empty after "Clear client certificate"', async () => {
      vi.mocked(ssoSettingsApi.get).mockResolvedValue(withCertificate);
      await renderPage();
      fireEvent.click(screen.getByRole('button', { name: 'Clear client certificate' }));
      expect(screen.getByTestId('sso-certificate-status')).toHaveTextContent(
        'The client certificate will be cleared when you save',
      );
      await save();
      expect(lastBody()).toHaveProperty('client_certificate', '');
      expect(lastBody()).toHaveProperty('client_private_key', '');
      await waitFor(() =>
        expect(screen.getByTestId('sso-certificate-status')).toHaveTextContent(
          'No client certificate is set',
        ),
      );
    });

    it('shows a refused key under its input and keeps the pasted values', async () => {
      vi.mocked(ssoSettingsApi.update).mockRejectedValue(
        Object.assign(new Error('400'), {
          response: {
            status: 400,
            data: {
              code: 'VALIDATION_ERROR',
              message: 'Validation failed',
              details: { client_private_key: 'does not match client_certificate' },
            },
          },
        }),
      );
      await renderPage();
      fireEvent.change(screen.getByLabelText('Client certificate (PEM)'), {
        target: { value: CERT_PEM },
      });
      fireEvent.change(screen.getByLabelText('Client private key (PEM)'), {
        target: { value: KEY_PEM },
      });
      await save();
      const keyInput = screen.getByLabelText('Client private key (PEM)');
      await waitFor(() => expect(keyInput).toHaveAttribute('aria-invalid', 'true'));
      expect(keyInput).toHaveAccessibleDescription('does not match client_certificate');
      expect(screen.getByLabelText('Client certificate (PEM)')).toHaveAttribute(
        'aria-invalid',
        'false',
      );
      expect(keyInput).toHaveValue(KEY_PEM);
    });
  });

  it('shows the refused fields of a 400 answer', async () => {
    vi.mocked(ssoSettingsApi.update).mockRejectedValue(
      Object.assign(new Error('400'), {
        response: {
          status: 400,
          data: {
            code: 'INVALID_REFERENCE',
            message: 'The role mapping names a role that does not exist',
            details: { 'role_mapping[0].role_id': 'names a role that does not exist' },
          },
        },
      }),
    );
    await renderPage();

    fireEvent.change(screen.getByLabelText('Client secret'), { target: { value: SECRET } });
    await save();

    expect(
      await screen.findByText(
        'The role mapping names a role that does not exist: role_mapping[0].role_id names a role that does not exist',
      ),
    ).toBeInTheDocument();
  });

  it('shows only the server message of a rate-limited save', async () => {
    vi.mocked(ssoSettingsApi.update).mockRejectedValue(
      Object.assign(new Error('429'), {
        response: {
          status: 429,
          data: {
            code: 'TOO_MANY_REQUESTS',
            message: 'Too many requests. Please try again later.',
            details: { retry_after: '60s' },
          },
        },
      }),
    );
    await renderPage();

    fireEvent.change(screen.getByLabelText('Client secret'), { target: { value: SECRET } });
    await save();

    expect(
      await screen.findByText('Too many requests. Please try again later.'),
    ).toBeInTheDocument();
    expect(screen.queryByText(/retry_after/)).toBeNull();
  });

  describe('changing the issuer while accounts are linked', () => {
    const issuerChangeRefusal = (links: number) =>
      Object.assign(new Error('409'), {
        response: {
          status: 409,
          data: {
            code: 'ISSUER_CHANGE_UNCONFIRMED',
            message: 'Changing the issuer unlinks the accounts linked to the current issuer',
            details: { affected_links: links },
          },
        },
      });

    const saveNewIssuer = async (links: number) => {
      vi.mocked(ssoSettingsApi.update).mockRejectedValueOnce(issuerChangeRefusal(links));
      await renderPage();
      fireEvent.change(screen.getByLabelText('Issuer URL'), { target: { value: 'https://other.example/tenant' } });
      await save();
      return screen.findByRole('dialog', { name: 'Change the issuer?' });
    };

    it('opens the dialog with the count; the first PUT carries no confirmation', async () => {
      const dialog = await saveNewIssuer(3);

      expect(within(dialog).getByText(/3 account links use the current issuer\./)).toBeInTheDocument();
      expect(within(dialog).getByText(/not recognised at their next sign-in/)).toBeInTheDocument();
      expect(within(dialog).getByText(/provisioning no longer sees them/)).toBeInTheDocument();
      expect(ssoSettingsApi.update).toHaveBeenCalledTimes(1);
      expect(lastBody()).not.toHaveProperty('confirm_issuer_change');
      expect(lastBody().issuer_url).toBe('https://other.example/tenant');
      expect(screen.queryByText(/could not be saved/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Changing the issuer unlinks/)).not.toBeInTheDocument();
    });

    it('uses the singular for one link', async () => {
      const dialog = await saveNewIssuer(1);
      expect(within(dialog).getByText(/1 account link uses the current issuer\./)).toBeInTheDocument();
    });

    it('"Change issuer" resends the same body with confirm_issuer_change', async () => {
      const dialog = await saveNewIssuer(2);
      const first = lastBody();

      fireEvent.click(within(dialog).getByRole('button', { name: 'Change issuer' }));

      await waitFor(() => expect(ssoSettingsApi.update).toHaveBeenCalledTimes(2));
      expect(lastBody()).toEqual({ ...first, confirm_issuer_change: true });
      await waitFor(() =>
        expect(screen.queryByRole('dialog', { name: 'Change the issuer?' })).not.toBeInTheDocument(),
      );
      await waitFor(() => expect(screen.getByLabelText('Issuer URL')).toHaveValue('https://other.example/tenant'));
    });

    it('Cancel sends nothing and keeps the form values', async () => {
      const dialog = await saveNewIssuer(2);

      fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));

      await waitFor(() =>
        expect(screen.queryByRole('dialog', { name: 'Change the issuer?' })).not.toBeInTheDocument(),
      );
      expect(ssoSettingsApi.update).toHaveBeenCalledTimes(1);
      expect(screen.getByLabelText('Issuer URL')).toHaveValue('https://other.example/tenant');
    });
  });

  it('offers a role written in Settings > Roles once the shared role list is invalidated', async () => {
    const client = createProductionQueryClient();
    render(withClient(client, <SSOSettings />));
    await screen.findByLabelText('Issuer URL');
    await waitFor(() => expect(rolesApi.getRoles).toHaveBeenCalledTimes(1));

    // A role write elsewhere invalidates ['roles'], as RoleEditor, RoleList and
    // CloneRoleDialog do.
    vi.mocked(rolesApi.getRoles).mockResolvedValue([...ROLES, role(4, 'partners')]);
    await client.invalidateQueries({ queryKey: ['roles'] });

    fireEvent.click(screen.getByRole('button', { name: 'Add mapping' }));
    const row = within(screen.getByTestId('sso-mapping-row-2'));
    fireEvent.mouseDown(row.getByRole('combobox', { name: 'Role' }));
    expect(await screen.findByRole('option', { name: 'partners' })).toBeInTheDocument();
  });
});
