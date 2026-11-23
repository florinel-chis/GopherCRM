# API keys

An API key lets a script or another system call the GopherCRM API without a password. Every key
belongs to the user who created it and acts as that user. This page walks through
Settings → API Keys. Which permissions a key carries, and how to give an integration only what it
needs, is explained in
[Assign a role to an API key](roles-and-permissions.md#assign-a-role-to-an-api-key).

## Who can hold keys

Only staff accounts hold API keys: a customer account is refused with 403. Every user, admin
included, sees and creates only their own keys; there is no account-wide view, and an
administrator cannot list or revoke another user's keys.

## Create a key

Creating a key needs a recent sign-in: a password or single sign-on sign-in within the last
15 minutes. Otherwise the request is refused with 403 `REAUTHENTICATION_REQUIRED`, and so is a
session whose token came from `POST /auth/refresh`, because such a token carries no sign-in time.
Sign out and sign in again, then create the key. A request authenticated with an API key cannot
create a key at all (403).

1. Open Settings → API Keys (`/settings/api-keys`) and click **Create API Key**.
2. Enter a **Name** of 3 to 100 characters that says what the key is for, for example
   "Zapier sync".
3. Optionally pick a date in **Expires on**. Left empty, the key expires one year from today; a
   date can be at most one year ahead (the date field offers nothing later). The key keeps working
   until the end of the chosen day.
4. Click **Create**.

The key is shown once, in **Your new API key**, with a copy button. Copy it then and store it in
the integration: GopherCRM keeps only a hash of it, and neither the screen nor the API can show it
again. A lost key cannot be recovered; create a new one and revoke the old one.

## The list of keys

The list shows each of your keys with these columns:

| Column | Meaning |
|---|---|
| Name | The name given at creation. |
| Prefix | The first 8 characters after `gcrm_`, so you can tell which key an integration uses without seeing the key. |
| Status | Active, Inactive (revoked) or Expired. |
| Created | The day the key was created. |
| Expires | The day the key stops working. |
| Last used | When the key last authenticated a request. |

## Use a key

Send the key in the `Authorization` header of each request, with the `ApiKey` scheme:

```
Authorization: ApiKey gcrm_...
```

The request then runs with the permissions the key's owner holds at that moment. A key is
refused when it is revoked or expired, or when its owner has been deactivated or erased, or is a
customer account.

## Revoke a key

Click the key's revoke button (its tooltip says **Revoke**) and confirm in **Revoke API key**.
Any integration still using the key stops authenticating immediately. Through the API, `DELETE /api-keys/{id}` revokes a key, and so does
`PUT /api-keys/{id}` with `is_active` false.

Revocation is final. The key stays in the list as Inactive and cannot be switched on again:
`PUT /api-keys/{id}` with `is_active` true on a revoked key is refused with 409, and a request
authenticated with an API key that tries it is refused with 403. To replace a key, create the new
one first, move the integration to it, then revoke the old one.

Deactivating the owner, or changing or resetting the owner's password, also revokes every key of
that user; see [Assign a role to an API key](roles-and-permissions.md#assign-a-role-to-an-api-key).

## What is recorded

Creating a key, revoking a key and a refused attempt to switch a revoked key on again each write
a row to the sign-in audit, with your user id and the key's id. Requests whose key is refused are
counted there too. A row never holds the key, its prefix or its name. See [Sign-in audit](sign-in-audit.md).
