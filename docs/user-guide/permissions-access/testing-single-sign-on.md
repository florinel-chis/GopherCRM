# Testing single sign-on

Back to the [overview](overview.md). Background: [Single sign-on](single-sign-on.md).

This page is for an administrator who wants to verify single sign-on against their own Microsoft
Entra ID tenant before rolling it out to everyone. It shows how to set up a test, run a local copy
of GopherCRM, check the configuration before the first browser sign-in, which cases to try and
where to read the result. Values in angle brackets are placeholders: `<tenant id>`,
`<application (client) id>`, `<APP_BASE_URL>`, `<admin@example.com>` and `<test user email>`.
Replace them with your own values and never paste real ids or secrets into tickets or chat.

## 1. Set up Entra ID

Follow [How to: Microsoft Entra ID](how-to-microsoft-entra-id.md); this page does not repeat its
steps. Before you test, check that you have:

- **An app registration** (section 1 of the how-to), single tenant.
- **The Web redirect URI** `APP_BASE_URL/auth/callback`, that is `<APP_BASE_URL>/auth/callback`
  exactly, with no trailing slash (section 1, step 4). Entra ID accepts `http` only for
  localhost, so a local test uses for example http://localhost:5173/auth/callback.
- **A client secret or a certificate** (section 2). For a secret, copy its **Value**, not the
  Secret ID. A certificate and its private key are entered in Settings → Single sign-on only; there
  is no environment variable for them.
- **Admin consent** granted for the API permissions (section 3).
- **App roles** `CRM.Admin` and `CRM.Sales` (section 4).
- **Assignment required** set to **Yes**, and **one test person** assigned an app role
  (section 5). Keep a second tenant user without any assignment for the refusal tests.

## 2. Copy the values into GopherCRM

Open Settings → Single sign-on (`/settings/sso`) as a local administrator and enter:

- **Issuer URL**: `https://login.microsoftonline.com/<tenant id>/v2.0`
- **Client ID**: `<application (client) id>`
- **Client secret**: the secret's Value, or, with a certificate, **Client certificate (PEM)** and
  **Client private key (PEM)** (section 6 of the how-to)
- **Scopes**: leave the default `openid profile email`

For the **Role mapping** rows you need the GopherCRM role ids. `GET /api/v1/roles` lists them, or
open Settings → Roles and click a role: the number at the end of the address is its id. Add one row
per app role (claim `roles`, value `CRM.Admin` or `CRM.Sales`), as in section 6 of the how-to.

## 3. Run a local copy for the test

**`APP_BASE_URL` is the web app's address, not the API's.** GopherCRM builds the redirect URI from
it, so it must be the origin the browser shows: the Vite dev server (for example
http://localhost:5173) or the address that serves the UI. If it points at the API port, Entra ID
sends the browser to the API and the sign-in never completes.

**Create a break-glass local administrator** before you switch single sign-on on:

```
make build-tools && make create-admin
```

Give it an address with a dotted domain, such as `<admin@example.com>`. An address without a dot
in the domain, such as admin@localhost, can be created by `create-admin` (it does not check the
address), but the sign-in form and the login API both refuse it, so that account can never sign
in. Keep this account signed in in another browser until the tests pass.

**Load the client secret from a git-ignored env file**, not from the command line and not from a
tracked file. The server reads `.env` in the working directory at startup, and `.env` is listed in
`.gitignore`. Add a line such as:

```
APP_BASE_URL=http://localhost:5173
OIDC_CLIENT_SECRET=<client secret value>
```

Edit the file in an editor; do not echo or cat it, and do not paste it into a terminal that records
its history. A value saved in Settings → Single sign-on wins over the environment, so if you typed
the secret there you need no env line for it.

## 4. Quick checks before the first browser sign-in

Run these against the API (replace the host and port with yours). Both are unauthenticated.

1. `GET /api/v1/auth/registration` must report `sso_enabled: true`. If it reports false, single
   sign-on is switched off or its settings cannot be read.

   ```
   curl -s http://localhost:8080/api/v1/auth/registration
   ```

2. `POST /api/v1/auth/oidc/start` takes no body. It belongs to the strict rate tier of the
   sign-in routes (10 requests per minute per client address), so do not loop it.

   ```
   curl -s -X POST http://localhost:8080/api/v1/auth/oidc/start
   ```

   The answer carries `authorization_url`. Check in it:
   - `redirect_uri` is the URL-encoded `<APP_BASE_URL>/auth/callback`, for example
     http%3A%2F%2Flocalhost%3A5173%2Fauth%2Fcallback;
   - `client_id` is `<application (client) id>`;
   - code_challenge_method=S256 is present.

   A 404 means single sign-on is disabled. A 503 `SSO_UNAVAILABLE` means it is enabled but not
   completely configured, or the issuer cannot be discovered (check the Issuer URL and that the
   server can reach login.microsoftonline.com over https).

## 5. Live tests

Use a private browser window for every sign-in, choose **Sign in with single sign-on**, and sign
out between tests.

| Test | What to do | Expected result | Where to see it |
|---|---|---|---|
| No app role | Sign in as a tenant user who is assigned to the application but holds no app role that a mapping row names. | Refused with "Single sign-on failed". No user is created. | Sign-in audit: reason `no_mapping_match`. Users list unchanged. |
| Mapped role | Sign in as the test person assigned CRM Sales (or CRM Admin). | Signed in with the mapped role. The account is created and linked to the provider identity. | Users list; `GET /roles/audit` shows a role audit row with `auth_method` `oidc`; Sign-in audit shows a success row. |
| Role changed in Entra ID | Change the test person's app role in Entra ID, then sign in again. | The new role applies at this sign-in, not before. | The user's role in Users; a new role audit row. |
| Last admin demoted | On a test database, map the test person to Admin and sign in once, then deactivate the other administrators so the test person is the only active one (they stay an administrator and can activate the others again). Give them a non-admin app role in Entra ID and sign in. | Refused; GopherCRM never ends up with zero administrators. | Sign-in audit: reason `last_admin`. |
| Not assigned | Sign in as a tenant user with no assignment to the application. | Microsoft refuses the sign-in before it reaches GopherCRM. | The Microsoft sign-in page; no row in GopherCRM's Sign-in audit. |
| Email already used | Create a local account with the test person's email address first, then sign in as that person. | Refused; the local account is left alone and not linked. | Sign-in audit: reason `email_taken`. |
| Local admin password | Sign in with the break-glass local administrator's password. | Works, whether single sign-on is on or off. | Sign-in audit: a password success row. |
| Password for a linked account, setting off | With `security.sso.allow_password` off (the default), try a password sign-in and a password reset for the account single sign-on created. | Sign-in gets the generic "Invalid email or password"; password reset answers as usual but sends no mail. | Sign-in audit: reason `sso_account`. No mail in the mail log. |
| Password for a linked account, setting on | Switch `security.sso.allow_password` on and request a password reset for that account. | The reset mail is sent and the new password works. | The mail (or the log-only mailer's line); Sign-in audit: a password success row. |

## 6. Where to look

- **Settings → Sign-in audit** (`/settings/sign-in-audit`, API `GET /api/v1/audit/sign-ins`): one
  row per attempt, with the method, the outcome and the reason code. Filter on the single sign-on
  method.
- **The server log.** Every sign-in step writes lines with the component `oidc_sign_in`. They carry
  the `request_id`, the `attempt_id` and reason codes only: no email address, subject, token or
  authorization code.

To confirm that, capture the server output to a file during the test and search it afterwards:

```
make run 2>&1 | tee <log file>
grep -c -F 'oidc_sign_in' <log file>
grep -c -i -F '<test user email>' <log file>
```

The first count should be above 0, the second must be 0. Delete the log file when you are done.

## 7. External (#EXT#) accounts

A personal Microsoft account (or an account from another tenant) invited into your tenant shows a
user principal name like `<name>_<domain>#EXT#@<tenant>.onmicrosoft.com`. That is normal and does
not stop single sign-on. GopherCRM takes the address from the `email` claim of the ID token, never
from the user principal name. If Entra ID does not send that claim for such an account, the
sign-in is refused with reason `email_missing`: in the app registration open **Token
configuration** → **Add optional claim**, choose the ID token and add `email`.

## 8. Common errors

| Symptom | Cause | Fix |
|---|---|---|
| Entra ID shows AADSTS50011 (redirect URI mismatch). | The registered redirect URI is not exactly `<APP_BASE_URL>/auth/callback`, or `APP_BASE_URL` points at the API instead of the web app. | Register the exact URI (scheme, host, port, path, no trailing slash) and set `APP_BASE_URL` to the web app's address. |
| Reason `email_missing`. | The ID token has no email claim (often an `#EXT#` account). | Add the `email` optional claim to the ID token (section 7) and keep `email` in **Scopes**. |
| Reason `no_mapping_match`. | The person holds no app role that a mapping row names. | Assign the app role in Entra ID or add a mapping row. Values are case-sensitive. |
| Reason `email_taken`. | A GopherCRM account already uses that address. | Use another test person, or see the [FAQ](faq.md). Accounts are never linked by email. |
| Nobody knows the test user's password. | The tenant user was created without a password you can use. | Reset the password in the Microsoft Entra admin center and sign in once to set a new one. |
| 503 `SSO_UNAVAILABLE`, or "Single sign-on is unavailable" on the sign-in page. | The Issuer URL, the Client ID or the client secret is missing or wrong, or the issuer cannot be reached. A secret the issuer refuses is recorded with reason `token_exchange_failed`. | Check the three values in `/settings/sso`; create a new secret if needed. |

For every other reason code, see [the troubleshooting table](single-sign-on.md#troubleshooting).
