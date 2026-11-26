# Getting started

## Open the web app and sign in

1. **Open your browser** to `http://localhost:5173` (the dev server), or `http://localhost:3000` on
   the Docker stacks.
2. **Sign in** with the admin account you created with `create-admin`.

What each user can see and do depends on the role they hold; the roles and their permissions are
described in [Roles and permissions](permissions-access/roles-and-permissions.md).

## Public sign-up

Public sign-up ships **disabled**: the login page offers no registration link until an admin (a
user whose role holds `settings.update`) turns on `security.allow_public_registration` under
**Settings > Configuration** (Security tab). When it is on, self-service registration always
creates a `customer` account.

Elevated roles (admin, sales, support) are never self-assigned. They are given by a user whose role
holds `users.assign_role` (the admin system role does), when creating a user with `POST /users` or
editing one; by the `create-admin` CLI; or by the single sign-on and SCIM role mapping an
administrator configured. The registration endpoint ignores any role supplied by the client.

## How long you stay signed in

Tokens are stored in `sessionStorage` and expire with the browser tab unless you tick **Remember
me** at login, which moves them to `localStorage`.
