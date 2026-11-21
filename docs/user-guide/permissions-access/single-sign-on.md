# Single sign-on

Back to the [overview](overview.md).

GopherCRM supports single sign-on through OpenID Connect, with one identity provider at a time
(for example Microsoft Entra ID or Okta). SAML is not supported. Accounts are created at the first
sign-in, or in advance by a provider that provisions through SCIM 2.0 (create, update and
deactivate; a deactivation signs the person out everywhere at once, a SCIM DELETE deactivates
too, and SCIM never erases an account; the provider can also manage the members of the groups the
role mapping names; see [SCIM provisioning](scim-provisioning.md)). Limit the provider's SCIM attribute
mappings to the user name, active state, given and family name, work email and roles: an update
that also sends another attribute, such as `externalId` or `displayName`, is refused.

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
7. **How long a session lasts.** A session that starts with single sign-on can be refreshed for 24
   hours after the sign-in at the provider. After that the person signs in at the provider again,
   so changes made there (a removed assignment, another role) apply within about a day. An access
   token already issued stays valid until it expires (`JWT_EXPIRY_HOURS`). Password sessions keep
   the lifetime set by `REFRESH_TOKEN_EXPIRY_DAYS`, and a password change during a single sign-on
   session keeps the same 24-hour limit.

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
| Provider → GopherCRM | Issuer URL | **Issuer URL**. An absolute `https` URL, exactly as the provider publishes it. |
| Provider → GopherCRM | Client ID | **Client ID** |
| Provider → GopherCRM | Client secret | **Client secret** |
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
3. Type the **Client secret**.
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

**Environment fallbacks.** Each setting can also come from the server's environment:
`OIDC_ENABLED`, `OIDC_ALLOW_PASSWORD`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`,
`OIDC_SCOPES` and `OIDC_ROLE_MAPPING`. A value stored in Settings wins over the environment, and
the page marks a field whose value comes from the environment with "From the environment
(OIDC_*)". The first **Save** stores every value the page shows, including those that came from
the environment, so from then on changing an `OIDC_*` variable has no effect on that field. Stored
settings take effect without a restart. One single sign-on setting exists only in the environment:
`OIDC_GRAPH_MAX_PAGES` (see group overage below).

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
| The provider shows an error instead of returning to GopherCRM, for Entra ID AADSTS50011 ("The redirect URI ... does not match"). | The redirect URI registered at the provider is not exactly `APP_BASE_URL/auth/callback`. | Register the exact URI (scheme, host, port, path, no trailing slash) as a Web redirect URI, and check that `APP_BASE_URL` on the server is the address people use. |
| "Single sign-on failed" for a first sign-in; reason `email_missing`. | The ID token carries no email claim. | Keep `email` in **Scopes**, give the person an email address at the provider, and for Entra ID add the optional claim email to the ID token if it is still missing. |
| "Single sign-on failed"; reason `email_taken`. | Another GopherCRM account already uses that email address. Accounts are never linked by email. | See the [FAQ](faq.md#someone-already-has-a-password-account-can-they-switch-to-single-sign-on). |
| "Single sign-on failed"; reason `no_mapping_match`. | No role mapping row matches the person's `roles` or `groups` values. | Compare the values recorded in the sign-in audit row with the mapping. Assign the person the app role or group at the provider, or add a row. Values are case-sensitive. |
| "Single sign-on failed" after the provider page; reason `token_exchange_failed` (the API answers 503 `SSO_UNAVAILABLE`). | The provider refused the client secret: it is wrong, expired, or belongs to another application. | Create a new secret at the provider, type it into **Client secret** and save. |
| "Single sign-on is unavailable. Sign in with your password." on the sign-in page (503 `SSO_UNAVAILABLE`). | The issuer cannot be reached or discovered, or a required setting is empty. | Check **Issuer URL**, **Client ID** and **Client secret**, and that the server can reach the issuer over https. |
| Saving the settings fails on **Issuer URL**. | The issuer is not an absolute `https` URL of at most 512 bytes, or it carries credentials, a query or a fragment. | Enter the issuer exactly as the provider publishes it, starting with `https://`. An invalid `OIDC_ISSUER_URL` (one longer than 512 bytes included) is ignored with a warning in the server log. |
| Saving opens **Change the issuer?** (the API answers 409 `ISSUER_CHANGE_UNCONFIRMED`). | The save changes the issuer in force while accounts are linked to the current issuer; the `affected_links` detail is the number of account links that stop matching. | Click **Change issuer** only if those people should be linked again under the new issuer; an API client resends the same body with `confirm_issuer_change` true. Otherwise click **Cancel**. |
| "Single sign-on failed" for people in many groups; reason `groups_unavailable` or `group_overage`. | Group overage: above 200 groups Entra ID leaves the groups out of the token. GopherCRM then reads them from Microsoft Graph, which needs the delegated permission `GroupMember.Read.All` with admin consent, and reads at most `OIDC_GRAPH_MAX_PAGES` pages (default 10). An overage from a sovereign cloud (US Government, China) or from any other provider is refused with `group_overage`. | Prefer app roles (claim `roles`): they have no overage. Otherwise grant `GroupMember.Read.All` with admin consent, or raise `OIDC_GRAPH_MAX_PAGES` (at most 50). |
| "Single sign-on failed"; reason `account_inactive` or `account_erased`. | The account was deactivated or deleted in GopherCRM. | Activate the account if the person should have access. A deleted account cannot be restored. |
| "Single sign-on failed"; reason `last_admin`. | The mapping would move the last active administrator to another role. | Map that person to the Admin role, or make another user an administrator first. |
