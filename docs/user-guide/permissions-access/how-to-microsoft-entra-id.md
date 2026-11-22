# How to: single sign-on with Microsoft Entra ID

Back to the [overview](overview.md). Background: [Single sign-on](single-sign-on.md).

This walk-through connects GopherCRM to Microsoft Entra ID (formerly Azure Active Directory). It
assumes you can sign in to the Microsoft Entra admin center as an administrator who may register
applications and grant admin consent, and to GopherCRM as an administrator.

The example uses https://crm.example.com as the address of GopherCRM. Replace it with yours: it
must be the value of `APP_BASE_URL` on the GopherCRM server. Values in angle brackets, such as
`<tenant id>`, are placeholders for the values you copy from Entra ID.

**Licence.** Assigning users to the application works on every Entra ID edition, including Free.
Assigning groups to the application needs Microsoft Entra ID P1 (or higher). Without P1, assign
the people one by one.

## 1. Register the application

1. In the Microsoft Entra admin center, go to Identity → Applications → App registrations and
   choose **New registration**.
2. **Name**: for example GopherCRM.
3. **Supported account types**: "Accounts in this organizational directory only" (single tenant).
4. **Redirect URI**: platform **Web**, value https://crm.example.com/auth/callback, that is
   `APP_BASE_URL/auth/callback` exactly, with no trailing slash.
5. Choose **Register**.
6. On the application's **Overview** page, copy:
   - **Application (client) ID**: this is `<application (client) id>`.
   - **Directory (tenant) ID**: this is `<tenant id>`.

## 2. Create a client credential: a certificate (recommended) or a client secret

GopherCRM proves to Entra ID that it is the registered application when it exchanges the sign-in
code for tokens. Microsoft recommends a certificate over a client secret: GopherCRM then signs a
short-lived client assertion with the certificate's private key, and no shared secret is sent.
Choose one of the two options. If both are saved in GopherCRM, the certificate is used.

### Option A: a certificate (recommended)

1. Create a certificate and its private key. A certificate from your organisation's certificate
   authority works; a self-signed one is enough for Entra ID. With OpenSSL, on a trusted machine:

   ```
   openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes -subj "/CN=GopherCRM single sign-on" -keyout gophercrm-sso.key -out gophercrm-sso.crt
   ```

   This writes the certificate to `gophercrm-sso.crt` and its unencrypted RSA private key to
   `gophercrm-sso.key`. GopherCRM needs an RSA key of at least 2048 bits, not encrypted with a
   passphrase.
2. In the application, open **Certificates & secrets** → **Certificates** → **Upload
   certificate**, choose `gophercrm-sso.crt`, enter a description and choose **Add**. Upload only
   the certificate, never the key.
3. Note the **Thumbprint** Entra ID shows for the certificate and its expiry date. After you save
   the certificate in GopherCRM (step 6), Settings → Single sign-on shows the same thumbprint:
   compare the two.
4. Keep `gophercrm-sso.key` secret: do not commit it to git, do not paste it into chat or email,
   and delete local copies once it is saved in GopherCRM (keep one in your password manager or key
   vault if you need it again). GopherCRM stores the key encrypted and never shows it again.
5. Renew before the expiry date, or single sign-on stops working: create a new certificate, upload
   it here next to the old one, save the new certificate and key in GopherCRM (step 6), check that
   a sign-in works, and then delete the old certificate here.

### Option B: a client secret

1. In the application, open **Certificates & secrets** → **Client secrets** → **New client
   secret**.
2. Enter a description and choose an expiry. Entra ID does not allow secrets that never expire.
3. Choose **Add** and copy the secret's **Value** at once (not the Secret ID). Entra ID shows the
   value only now.
4. Note the expiry date. When the secret expires, single sign-on stops working: before that date,
   create a new secret here, enter it in GopherCRM (step 6) and then delete the old one.

## 3. API permissions

1. Open **API permissions**. Make sure the Microsoft Graph delegated permissions `openid`,
   `profile` and `email` are listed (add them with **Add a permission** → Microsoft Graph →
   Delegated permissions if not). `User.Read` is added by default.
2. If you will map **groups** instead of app roles, `User.Read` must stay: GopherCRM uses it to
   read the groups of people who are in more than 200 groups. No permission beyond `User.Read` is
   needed for groups; a broader group permission added earlier for GopherCRM's group lookup can
   be removed.
3. Choose **Grant admin consent for** your organisation and confirm. Every permission should show
   "Granted".

## 4. Create app roles

App roles are the recommended way to give GopherCRM roles: they need no extra permission and have
no limit on how many a person holds.

1. Open **App roles** → **Create app role**.
2. For the administrators:
   - **Display name**: CRM Admin
   - **Allowed member types**: Users/Groups
   - **Value**: `CRM.Admin`
   - **Description**: GopherCRM administrators
   - Tick **Do you want to enable this app role?** and choose **Apply**.
3. Repeat for the sales team with **Display name** CRM Sales and **Value** `CRM.Sales`, and for any
   other GopherCRM role you want to hand out.

The **Value** is what GopherCRM receives in the `roles` claim and what you type into the role
mapping. It is case-sensitive.

## 5. Choose who may sign in

1. Go to Identity → Applications → **Enterprise applications** and open the application with the
   same name.
2. Under **Properties**, set **Assignment required?** to **Yes** and save. Only assigned people can
   then sign in; Entra ID turns everyone else away before they reach GopherCRM.
3. Under **Users and groups**, choose **Add user/group**, pick the users (or, with P1, the groups),
   select the app role (CRM Admin or CRM Sales) and choose **Assign**.

## 6. Configure GopherCRM

1. Sign in to GopherCRM with a local administrator account (one with a password).
2. Find the GopherCRM role ids: open Settings → Roles and click a role; the number at the end of
   the address is its id. The API lists them too: `GET /roles`.
3. Open Settings → Single sign-on and enter:
   - **Issuer URL**: `https://login.microsoftonline.com/<tenant id>/v2.0`
   - **Client ID**: `<application (client) id>`
   - With a certificate (step 2, option A): paste the whole content of `gophercrm-sso.crt` into
     **Client certificate (PEM)** and the whole content of `gophercrm-sso.key` into **Client
     private key (PEM)**. Paste only this one certificate, not a chain of certificates. After the
     save, the line under the fields shows the thumbprint and the expiry: compare the thumbprint
     with the one in Entra ID. Leave **Client secret** empty.
   - With a client secret (step 2, option B): **Client secret**: the secret value from step 2
   - **Scopes**: leave the default `openid profile email`
4. Under **Role mapping**, choose **Add mapping** twice:
   - **Claim** `roles`, **Value** `CRM.Admin`, **Role** Admin
   - **Claim** `roles`, **Value** `CRM.Sales`, **Role** Sales
5. Leave **Allow password sign-in and password reset for accounts linked to single sign-on** off.
6. Switch **Enabled** on and choose **Save**.

The same configuration can come from the server's environment instead (a value saved in Settings
wins). The certificate has no environment variable: it is entered in Settings only. Replace the
role ids with those from step 6.2:

```
OIDC_ENABLED=true
OIDC_ISSUER_URL=https://login.microsoftonline.com/<tenant id>/v2.0
OIDC_CLIENT_ID=<application (client) id>
OIDC_CLIENT_SECRET=<client secret value>
OIDC_ROLE_MAPPING=[{"claim":"roles","value":"CRM.Admin","role_id":<Admin role id>},{"claim":"roles","value":"CRM.Sales","role_id":<Sales role id>}]
```

## 7. Test

1. In a private browser window, open GopherCRM and choose **Sign in with single sign-on**.
2. Sign in as a person assigned CRM Sales. You should land in GopherCRM with the Sales role; the
   account appears under Users.
3. Repeat with a person who has no assignment: Entra ID should refuse them.
4. If a sign-in fails, read the reason in Settings → Sign-in audit and look it up in the
   [troubleshooting table](single-sign-on.md#troubleshooting).

Keep your local administrator account. It is the way back in if the certificate or the secret
expires or the mapping is wrong.

## 8. Provision accounts through SCIM (optional)

With SCIM, Entra ID creates, updates and deactivates the GopherCRM accounts of the people assigned
to the application before they sign in. See [SCIM provisioning](scim-provisioning.md) for what
GopherCRM does with each request.

1. In GopherCRM, open Settings → SCIM tokens and create a token as described in
   [SCIM provisioning](scim-provisioning.md#tokens). Copy it at once.
2. In the Microsoft Entra admin center, go to Identity → Applications → Enterprise applications →
   the application → **Provisioning**. Set **Provisioning Mode** to **Automatic**.
3. Under **Admin credentials**, enter:
   - **Tenant URL**: `PUBLIC_BASE_URL/scim/v2`, for example https://crm.example.com/scim/v2
   - **Secret Token**: the SCIM token from step 8.1

   Choose **Test Connection**, then **Save**.
4. Under **Mappings**, open **Provision Microsoft Entra ID Users** and change the attribute
   mappings:
   1. Edit the row whose target attribute is `externalId`. Entra ID's default source attribute
      for it is `mailNickname`. Change the source attribute to `objectId`, with mapping type
      **Direct**. Single sign-on links the account SCIM created through the token's `oid`, which
      equals the `objectId` (see [Accounts created through SCIM](#accounts-created-through-scim)).
      With `mailNickname`, the person's first single sign-on is refused with the audit reason
      `email_taken`. The `objectId` never changes, so Entra ID never sends an update for it.
   2. Make this change before the first provisioning cycle. An account created while the mapping
      sent `mailNickname` keeps that value: PUT and PATCH both ignore `externalId`, so a later
      change of the mapping does not relink the account.
   3. GopherCRM reads these rows:
      - `userPrincipalName` → `userName` (the matching attribute, unchanged)
      - the `IsSoftDeleted` expression → `active`
      - `givenName` → `name.givenName`
      - `surname` → `name.familyName`
      - `mail` → `emails[type eq "work"].value`
      - `objectId` → `externalId`

      The other rows, for example `displayName`, `title`, the phone numbers, the addresses,
      `preferredLanguage` and the enterprise extension rows, can stay: GopherCRM accepts their
      updates and does not store them, as [SCIM provisioning](scim-provisioning.md) says.
   4. Choose **Add New Mapping**: **Mapping type** Expression, **Expression**
      `SingleAppRoleAssignment([appRoleAssignments])`, **Target attribute**
      `roles[primary eq "True"].value`. Choose **Ok**. GopherCRM reads the app role's value, which
      must exactly equal the **Value** of a `roles` row of the role mapping (step 6.4). Every person
      in scope needs an app role: a create without a matching role, for example an assignment with
      "Default Access", is refused with 400 `invalidValue`.

      A later change of a person's app role in Entra ID is applied: Entra ID sends it as an
      update of `roles`, and GopherCRM accepts both forms Entra ID uses for the role, the app
      role's value as plain text and the JSON object Entra ID sends without the
      `aadOptscim062020` flag, from which it reads the inner `value`.
   5. Choose **Save**.
5. Only when you map groups (needs P1; see
   [Mapping groups instead of app roles](#mapping-groups-instead-of-app-roles)): under
   **Mappings**, open **Provision Microsoft Entra ID Groups**.
   1. Edit the row whose target attribute is `displayName` and change its source attribute to
      `objectId`. Keep **Match objects using this attribute** set to **Yes**, with matching
      precedence 1.
   2. Delete the row `objectId` → `externalId`. Keep the `members` row.
   3. Choose **Save**.

   Entra ID looks a group up with `displayName eq "<value>"`. GopherCRM serves each `groups` value
   of the role mapping (the group's Object ID) as a group whose `displayName` is that value,
   matched exactly, so the group's `objectId` finds it. The `objectId` never changes, so Entra ID
   never sends a `displayName` update; GopherCRM would refuse one with 400 `invalidPath`. A group
   that is assigned in Entra ID but has no `groups` row in the role mapping is not found: Entra ID
   then tries to create it, GopherCRM refuses the create with 400 `invalidValue`, and the
   **Provisioning logs** show the failure. Add the group's Object ID to the role mapping instead.
   When Entra ID creates a group whose Object ID is in the role mapping, GopherCRM answers 201
   with the existing group and adds nothing.

   Under the default group mapping (`displayName` → `displayName`), a group whose Object ID is in
   the role mapping is still created (201), because Entra ID also sends the Object ID as
   `externalId`. Entra ID then sends a `displayName` update with the group's name, which GopherCRM
   refuses with 400 `invalidPath`, so keep the mapping of this step.
6. Under **Settings**, set **Scope** to "Sync only assigned users and groups". Set **Provisioning
   Status** to **On** and choose **Save**.
7. Optionally, use **Provision on demand** for one person first and read the result under
   **Provisioning logs**.

## Mapping groups instead of app roles

If you prefer groups, use the claim `groups` and the group's **Object ID** as the value, never its
name. Add a groups claim to the token in **Token configuration** → **Add groups claim** (security
groups), and keep `User.Read` granted as in step 3. A person in more than 200 groups has their
groups read from Microsoft Graph; see group overage in the
[troubleshooting table](single-sign-on.md#troubleshooting). To provision the groups through SCIM,
see step 8.5.

## Claims mapping (custom signing keys)

If the app registration uses claims mapping (custom signing keys), set
`OIDC_ENTRA_APP_SIGNING_KEYS=true` on the server, or every sign-in is refused with
`id_token_invalid`.

## Accounts created through SCIM

If Entra ID also provisions the people through [SCIM](scim-provisioning.md), keep the issuer URL
tenant-specific, `https://login.microsoftonline.com/<tenant id>/v2.0` as in step 6. A person's first
single sign-on then finds the account SCIM created for them: the token's `oid` must equal the
`externalId` Entra ID sent, which must be the `objectId` as mapped in
[step 8.4](#8-provision-accounts-through-scim-optional) (Entra ID's default sends `mailNickname`),
and its `tid` must equal the `<tenant id>` of the issuer. From then on the person signs in to that account directly. A token
from another tenant or without those claims is handled as a first sign-in, and an email address
alone never links an account. Details:
[SCIM provisioning](scim-provisioning.md#microsoft-entra-id-the-first-single-sign-on-of-a-scim-created-account).
