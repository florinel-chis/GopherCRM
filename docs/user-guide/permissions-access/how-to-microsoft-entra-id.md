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

## 2. Create a client secret

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
   Delegated permissions if not). `User.Read`, added by default, can stay.
2. Only if you will map **groups** instead of app roles: also add the delegated permission
   `GroupMember.Read.All`. GopherCRM uses it to read the groups of people who are in more than
   200 groups.
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
   - **Client secret**: the secret value from step 2
   - **Scopes**: leave the default `openid profile email`
4. Under **Role mapping**, choose **Add mapping** twice:
   - **Claim** `roles`, **Value** `CRM.Admin`, **Role** Admin
   - **Claim** `roles`, **Value** `CRM.Sales`, **Role** Sales
5. Leave **Allow password sign-in and password reset for accounts linked to single sign-on** off.
6. Switch **Enabled** on and choose **Save**.

The same configuration can come from the server's environment instead (a value saved in Settings
wins). Replace the role ids with those from step 6.2:

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

Keep your local administrator account. It is the way back in if the secret expires or the mapping
is wrong.

## Mapping groups instead of app roles

If you prefer groups, use the claim `groups` and the group's **Object ID** as the value, never its
name. Add a groups claim to the token in **Token configuration** → **Add groups claim** (security
groups), and grant `GroupMember.Read.All` with admin consent as in step 3. A person in more than 200
groups has their groups read from Microsoft Graph; see group overage in the
[troubleshooting table](single-sign-on.md#troubleshooting).

## Accounts created through SCIM

If Entra ID also provisions the people through [SCIM](scim-provisioning.md), keep the issuer URL
tenant-specific, `https://login.microsoftonline.com/<tenant id>/v2.0` as in step 6. A person's first
single sign-on then finds the account SCIM created for them: the token's `oid` must equal the
`externalId` Entra ID sent (the `objectId`, the default mapping) and its `tid` must equal the
`<tenant id>` of the issuer. From then on the person signs in to that account directly. A token
from another tenant or without those claims is handled as a first sign-in, and an email address
alone never links an account. Details:
[SCIM provisioning](scim-provisioning.md#microsoft-entra-id-the-first-single-sign-on-of-a-scim-created-account).
