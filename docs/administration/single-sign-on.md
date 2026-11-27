# Single sign-on

Back to the [overview](overview.md).

GopherCRM supports single sign-on through OpenID Connect, with one identity provider at a time
(for example Microsoft Entra ID or Okta). SAML is not supported. Accounts are created at the first
sign-in, or in advance by an identity provider through SCIM 2.0; see
[SCIM provisioning](scim-provisioning.md).

## What happens at sign-in

1. When single sign-on is enabled, the sign-in page shows a **Sign in with single sign-on**
   button below the password form.
2. The button sends the browser to the identity provider. If GopherCRM cannot reach the provider,
   or single sign-on is enabled but not completely configured, the sign-in page shows "Single
   sign-on is unavailable. Sign in with your password."
3. After the person signs in there, the provider sends the browser back to GopherCRM's
   `/auth/callback` page, and GopherCRM checks the answer.
4. The role comes from the role mapping: GopherCRM goes through the mapping rows in order and
   uses the first row whose claim carries the row's value. When no row matches, the person gets
   no access: nothing is created and the page shows "Single sign-on failed. Try again or sign in
   with your password."
5. **First sign-in.** If the person has never signed in through the provider, GopherCRM creates an
   active account with the mapped role, the email address and the first and last name the provider
   sends. That account has no usable password.
6. **Later sign-ins.** The person is recognised by the provider's own identifier for them, never by
   email address. The role from the mapping is applied again at every sign-in, so a change at the
   provider takes effect at the person's next sign-in.
7. **How long a session lasts.** A session that starts with single sign-on can be refreshed for
   24 hours after the sign-in at the provider. After that the person signs in at the provider
   again, so changes made there (a removed assignment, another role) apply within about a day. An
   access token already issued stays valid until it expires (`JWT_EXPIRY_HOURS`). Password
   sessions keep the lifetime set by `REFRESH_TOKEN_EXPIRY_DAYS`, and a password change during a
   single sign-on session keeps the same 24-hour limit.
8. **Signing out.** Logout ends the GopherCRM session as for any account. For a single sign-on
   session it then also sends the browser to the provider's sign-out page, which signs the person
   out there and returns them to GopherCRM's sign-in page (`APP_BASE_URL/login`); without that, the
   next **Sign in with single sign-on** would sign them straight back in. This needs the
   provider to publish a sign-out endpoint (`end_session_endpoint`) and to accept
   `APP_BASE_URL/login` as a return address. When it does not, logout ends only the GopherCRM
   session and shows the sign-in page, as before. Password sessions are unchanged.
9. **Signing out at the provider.** When the person signs out at the provider, a provider that
   supports front-channel sign-out loads `GET /auth/oidc/frontchannel-logout` in the browser, and
   GopherCRM ends the sessions that signed in through that provider session. It ends every
   session of that person, on every device and password sessions included, and records a
   `logout` row in the sign-in audit. It works only when the provider sends a `sid` claim in its
   ID tokens and the front-channel logout URL is registered there.

Accounts are never linked by email. If any GopherCRM account, active, deactivated or deleted,
already uses the email address the provider sends (in any letter case), the sign-in is refused and
the existing account is left alone. A person who already has a password account therefore cannot
start using single sign-on with that address; see the [FAQ](faq.md).

A mapped role is applied like a manual change: the last active administrator cannot be moved to
another role this way (the sign-in is refused instead), a customer login is not given a custom
role, and every role change is written to the role audit log with `auth_method` `oidc` (see
[Audit trail](roles-and-permissions.md#audit-trail)).

Every refused sign-in shows the same message, "Single sign-on failed. Try again or sign in with
your password.", so people cannot learn why. The reason is recorded for you in Settings → Sign-in
audit (`/settings/sign-in-audit`) and in the server log; the troubleshooting table below lists
the common ones.

## Values to exchange with the identity provider

| Direction | Value | Where it goes |
|---|---|---|
| GopherCRM → provider | Redirect URI: `APP_BASE_URL/auth/callback`, for example https://crm.example.com/auth/callback | The provider's application, as a Web redirect URI. It must match exactly, including `https` and with no trailing slash. |
| GopherCRM → provider | Post-logout redirect URI: `APP_BASE_URL/login`, for example https://crm.example.com/login | The provider's application, as a second Web redirect URI (some providers call it a post-logout or sign-out redirect URI). Without it the provider refuses the sign-out that follows a logout. |
| GopherCRM → provider | Front-channel logout URL: the API origin, the API prefix and `/auth/oidc/frontchannel-logout`, for example https://crm.example.com/api/v1/auth/oidc/frontchannel-logout | The provider's application, as its front-channel logout URL. It must be `https`. Without it a sign-out at the provider leaves the GopherCRM session running until it expires. |
| Provider → GopherCRM | The `sid` claim in the ID token | Configured at the provider (for Entra ID an optional claim of the ID token). Without it the front-channel sign-out finds no session to end. |
| Provider → GopherCRM | Issuer URL | **Issuer URL**. An absolute `https` URL, exactly as the provider publishes it. |
| Provider → GopherCRM | Client ID | **Client ID** |
| Provider → GopherCRM | Client secret, or a client certificate | **Client secret**, or **Client certificate (PEM)** and **Client private key (PEM)**. The certificate is uploaded at the provider and its private key stays with GopherCRM; while one is set it is used instead of the secret. |
| Provider → GopherCRM | Scopes | **Scopes**, separated by spaces. The default is `openid profile email`; a list you enter must contain `openid`. |
| Provider → GopherCRM | Group ids or app role values | **Role mapping**, one row per value, with the claim `roles` (app roles) or `groups` (groups). |

`APP_BASE_URL` is the address of the GopherCRM web interface, set in the server's environment.

Map only values that never change. For Microsoft Entra ID that means app role values, or group
object ids, never group display names. For Okta it means the group names your groups claim filter
returns, kept stable.

## Configure single sign-on

You need `settings.sso_manage`; only the Admin role can hold it. Register GopherCRM at your provider
first; for Entra ID follow [How to: Microsoft Entra ID](how-to-microsoft-entra-id.md).

1. Open Settings → Single sign-on (`/settings/sso`).
2. Fill in **Issuer URL**, **Client ID** and, if you need more than the default, **Scopes**.
3. Type the **Client secret**, or paste a **Client certificate (PEM)** and its **Client private
   key (PEM)** (see [Client certificate](#client-certificate)).
4. Under **Role mapping**, click **Add mapping** for each row. In each row choose the **Claim**
   (`roles` or `groups`), type the **Value** exactly as the provider sends it, and pick the
   **Role**. Rows are checked from top to bottom and the first match wins, so put the most
   powerful role first when one person may match several rows.
5. Leave **Allow password sign-in and password reset for accounts linked to single sign-on** off
   unless you need it (see [Mixed accounts](#mixed-accounts)).
6. Switch **Enabled** on and click **Save**.

   When the save changes the issuer in force while accounts are linked to the current issuer,
   the dialog **Change the issuer?** shows how many account links stop matching: those people are
   not recognised at their next sign-in, and provisioning no longer sees them. Click
   **Change issuer** to save anyway, or **Cancel** to keep the current issuer; nothing is saved
   until you confirm. Setting the first issuer, keeping it, or changing it while no account is
   linked needs no confirmation.

7. Sign out and test with a person who should get each mapped role. Keep a local administrator
   account signed in elsewhere until the test passes.
   [Testing single sign-on](testing-single-sign-on.md) lists the checks and test cases.

**The client secret is write-only.** GopherCRM never shows it again. The page says "A client
secret is set" or "No client secret is set". Leave the field empty to keep the stored secret, type
a new one to replace it, or click **Clear client secret** to remove it.

### Client certificate

Instead of the client secret, GopherCRM can prove its identity to the provider with a certificate:
it signs a short-lived client assertion (`private_key_jwt`) with the certificate's private key, and
the token request then carries no client secret. Microsoft recommends this for Entra ID; the
[Entra ID how-to](how-to-microsoft-entra-id.md) shows how to create and upload the certificate.

| Field | What to enter |
|---|---|
| **Client certificate (PEM)** | One certificate in PEM form, starting with `-----BEGIN CERTIFICATE-----`: the certificate uploaded at the provider, not a chain. It must not have expired. At most 16384 bytes. |
| **Client private key (PEM)** | The certificate's RSA private key in PEM form (PKCS#8 or PKCS#1), at least 2048 bits, not encrypted with a passphrase, and matching the certificate. At most 16384 bytes. |

The two are saved together: paste both, or click **Clear client certificate** to remove both.
Leave both empty to keep the stored certificate. A save with only one of them, a key that does not
match the certificate, or a certificate that has expired is refused field by field and nothing is
stored.

**The private key is write-only.** GopherCRM stores the certificate and the key encrypted, never
shows either again, and empties both fields after a save. The page says "No client certificate is
set", or "A client certificate is set" with its thumbprint (the SHA-1 thumbprint the provider
shows) and its expiry date. While a certificate is set it is used instead of the client secret; a
stored secret is kept but not sent. A certificate that has expired stays in force and the provider
refuses it, so renew it before the expiry date. A stored certificate that cannot be read (for
example after `API_KEY_SECRET` was changed) counts as not set, and the client secret applies again.
The certificate has no environment variable.

**Environment fallbacks.** Each setting can also come from the server's environment:
`OIDC_ENABLED`, `OIDC_ALLOW_PASSWORD`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`,
`OIDC_SCOPES` and `OIDC_ROLE_MAPPING`. A value stored in Settings wins over the environment, and
the page marks a field whose value comes from the environment with "From the environment
(OIDC_*)". The first **Save** stores every value the page shows, including those that came from
the environment, so from then on changing an `OIDC_*` variable has no effect on that field. Stored
settings take effect without a restart. Two single sign-on settings exist only in the environment:
`OIDC_GRAPH_MAX_PAGES` (see group overage below) and `OIDC_ENTRA_APP_SIGNING_KEYS` (Entra ID
only, default off): set it to `true` when the Entra ID app registration uses claims mapping, so ID
tokens are checked against the application's own signing keys (`jwks_uri?appid=<client id>`).

GopherCRM keeps the provider's discovery document and signing keys for up to 24 hours, fetches the
keys again when a token is signed with a key it does not know yet, and starts over on the next
sign-in after you change the issuer.

To switch single sign-on off, switch **Enabled** off and save. The button disappears from the
sign-in page; local password accounts keep working.

## Mixed accounts

An account created by a single sign-on sign-in is linked to single sign-on. Whether such an
account may also use a password is the setting **Allow password sign-in and password reset for
accounts linked to single sign-on** (`security.sso.allow_password`, environment fallback
`OIDC_ALLOW_PASSWORD`). It is off by default.

| Setting | Linked accounts | Offboarding a person |
|---|---|---|
| Off (default) | Sign in only through the provider. Password sign-in is refused with the same "Invalid email or password" answer as a wrong password, and password reset sends no mail. | Remove the person, or their group or app role, at the provider. They cannot sign in again. |
| On | May set a password through password reset and use it like any local account. | Remove them at the provider **and** deactivate the account in GopherCRM. A password keeps working after the provider removes the person. |

Switching the setting off does not end sessions that are already open; a password set while it was
on stops working at the next password sign-in.

**Local accounts are the break-glass path.** Accounts that were not created by single sign-on
(for example the administrator created at installation) can always sign in with their password,
whatever this setting says. Keep at least one local administrator account, so a provider outage or
a wrong role mapping cannot lock you out.

## Troubleshooting

Find the reason of a refused sign-in in Settings → Sign-in audit: filter on the single sign-on
method and read the reason column.

| Symptom | Cause | Fix |
|---|---|---|
| The provider shows an error instead of returning to GopherCRM, for Entra ID AADSTS50011 ("The redirect URI ... does not match"). | The redirect URI registered at the provider is not exactly `APP_BASE_URL/auth/callback`, or `APP_BASE_URL` points at the API instead of the web app. | Register the exact URI (scheme, host, port, path, no trailing slash) as a Web redirect URI, and check that `APP_BASE_URL` on the server is the address people use: the web app's address, not the API's. |
| "Single sign-on failed" for a first sign-in; reason `email_missing`. | The ID token carries no email claim; with Entra ID this is often an external (`#EXT#`) account. | Keep `email` in **Scopes**, give the person an email address at the provider, and for Entra ID add the optional claim email to the ID token if it is still missing (see [external accounts](testing-single-sign-on.md#7-external-ext-accounts)). |
| "Single sign-on failed"; reason `email_taken`. | Another GopherCRM account already uses that email address. Accounts are never linked by email. | See the [FAQ](faq.md#someone-already-has-a-password-account-can-they-switch-to-single-sign-on). |
| "Single sign-on failed"; reason `no_mapping_match`. | No role mapping row matches the person's `roles` or `groups` values. | Compare the values recorded in the sign-in audit row with the mapping. Assign the person the app role or group at the provider, or add a row. Values are case-sensitive. |
| "Single sign-on failed" after the provider page; reason `token_exchange_failed` (the API answers 503 `SSO_UNAVAILABLE`). | The provider refused the client secret or the client certificate: it is wrong, expired, or belongs to another application, or the certificate was not uploaded at the provider. | Create a new secret at the provider, type it into **Client secret** and save; or upload the certificate at the provider, compare its thumbprint with the one Settings → Single sign-on shows, and renew it if it has expired. |
| "Single sign-on is unavailable. Sign in with your password." on the sign-in page (503 `SSO_UNAVAILABLE`). | The issuer cannot be reached or discovered, or a required setting is empty. | Check **Issuer URL**, **Client ID** and **Client secret**, and that the server can reach the issuer over https. |
| Saving the settings fails on **Issuer URL**. | The issuer is not an absolute `https` URL of at most 512 bytes, or it carries credentials, a query or a fragment. | Enter the issuer exactly as the provider publishes it, starting with `https://`. An invalid `OIDC_ISSUER_URL` (one longer than 512 bytes included) is ignored with a warning in the server log. |
| Saving opens **Change the issuer?** (the API answers 409 `ISSUER_CHANGE_UNCONFIRMED`). | The save changes the issuer in force while accounts are linked to the current issuer; the `affected_links` detail is the number of account links that stop matching. | Click **Change issuer** only if those people should be linked again under the new issuer; an API client resends the same body with `confirm_issuer_change` true. Otherwise click **Cancel**. |
| "Single sign-on failed" for people in many groups; reason `groups_unavailable` or `group_overage`. | Group overage: above 200 groups Entra ID leaves the groups out of the token. GopherCRM then reads them from Microsoft Graph, which needs the delegated permission `User.Read` (granted), and reads at most `OIDC_GRAPH_MAX_PAGES` pages (default 10). An overage from a sovereign cloud (US Government, China) or from any other provider is refused with `group_overage`. | Prefer app roles (claim `roles`): they have no overage. Otherwise check that `User.Read` is listed and granted, or raise `OIDC_GRAPH_MAX_PAGES` (at most 50). |
| "Single sign-on failed" for everyone with Entra ID; reason `id_token_invalid`, after claims mapping was set up for the app registration. | With claims mapping, Entra ID signs the ID tokens with the application's own keys, which the plain signing keys do not include. | Set `OIDC_ENTRA_APP_SIGNING_KEYS=true` in the server's environment and restart. |
| "Single sign-on failed"; reason `account_inactive` or `account_erased`. | The account was deactivated or deleted in GopherCRM. | Activate the account if the person should have access. A deleted account cannot be restored. |
| "Single sign-on failed"; reason `last_admin`. | The mapping would move the last active administrator to another role. | Map that person to the Admin role, or make another user an administrator first. |
