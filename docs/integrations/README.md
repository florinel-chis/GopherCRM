# Integrations

> **For:** Developers connecting another system to the GopherCRM API. Part of the [documentation index](../README.md).

Every API route sits under `/api/v1` and answers with the same JSON envelope
(`success`, `data`, `error`, `meta`). The full list of routes is the offline
[OpenAPI reference](../../api/index.html).

## Your first API request

The commands below run against a local backend on port 8080, as in the
[setup walkthrough](../development/local-setup.md#verify).

```bash
# Sign in; the response data carries "token" and "refresh_token"
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"your-password"}'

# Call a protected route with the access token
curl http://localhost:8080/api/v1/users/me \
  -H "Authorization: Bearer YOUR_TOKEN_HERE"

# An API key goes in the same header with the ApiKey scheme
curl http://localhost:8080/api/v1/users/me \
  -H "Authorization: ApiKey YOUR_API_KEY"
```

The two schemes are parsed in `internal/middleware/auth.go`.

## More

- [API keys](api-keys.md): creating and revoking API keys for scripts and integrations.
- [Webhooks](webhooks.md): outbound webhooks that tell another system when a record changes in GopherCRM.
