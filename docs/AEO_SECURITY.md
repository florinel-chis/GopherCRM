# AEO provider egress and failure diagnostics

The AEO module sends each tracked prompt, and the engine's API key, to every configured answer
engine. Administrators can change provider base URLs in **Settings > AEO**, so without a policy
that setting could point a key at any host, including loopback, the private network or a cloud
metadata service. This document covers the outbound trust policy that closes that gap (issue #98),
the migration steps for operators, and how provider failures are reported.

The design follows the [OWASP SSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
allow-list approach. The connection-level controls rely on the documented hooks of Go's
[`net/http.Transport`](https://pkg.go.dev/net/http#Transport): `DialContext`, `Proxy` and
`TLSClientConfig`.

## Who decides what is trusted

| Source | What it can change | What it can never change |
|---|---|---|
| Environment (`AEO_EGRESS_ALLOWED_ORIGINS`, `AEO_EGRESS_LOCAL_ORIGINS`) | Which origins each engine may reach | — |
| Environment (`AEO_<PROVIDER>_BASE_URL`, `AEO_CUSTOM_BASE_URL`) | Which endpoint an engine asks for | Whether that endpoint is trusted |
| Settings > AEO (admin, stored in the database) | Keys, models and base URLs of the five hosted engines | Trust. Stored base URLs are checked against the environment policy like any other |

The trust policy is read only from the environment. It is not a configuration entry, the
ConfigurationService does not expose it, and the UI cannot edit it. A base URL override,
whether from the environment or the database, selects an endpoint. If the policy does not
trust that endpoint, the engine stays listed but each query fails with
`endpoint_not_permitted` before any DNS lookup, connection or request. This also covers rows
stored before HTTPS-only validation existed.

## Default trust

With no policy variables set, each engine trusts only its own official HTTPS origin:

| Engine | Trusted origin |
|---|---|
| `anthropic` | `https://api.anthropic.com` |
| `openai` | `https://api.openai.com` |
| `gemini` | `https://generativelanguage.googleapis.com` |
| `kimi` | `https://api.moonshot.ai` |
| `perplexity` | `https://api.perplexity.ai` |
| `custom` | none: needs an explicit entry |

Origins are compared exactly: scheme, host and port. An engine cannot use another engine's
origin, so the `openai` key never reaches `api.anthropic.com`. Subdomains
(`eu.api.openai.com`), suffix tricks (`api.openai.com.evil.example`), alternate ports
(`:8443`) and `http://` downgrades are all refused. Any path on a trusted origin is
accepted, so `https://api.openai.com/v1/` and a proxy path on the same origin both work.

The custom engine's trust is always looked up under `custom`. `AEO_CUSTOM_NAME=openai`
changes the display name and the stored provider label, not the trust.

## Explicit operator exceptions

Both variables take comma-separated `engine=origin` entries. `engine` is one of `anthropic`,
`openai`, `gemini`, `kimi`, `perplexity` or `custom`. An origin is `scheme://host[:port]`,
with no path, credentials, query or fragment.

### `AEO_EGRESS_ALLOWED_ORIGINS`: additional public HTTPS origins

```dotenv
# A hosted OpenAI-compatible gateway for the custom engine, and an
# enterprise proxy in front of OpenAI.
AEO_EGRESS_ALLOWED_ORIGINS=custom=https://llm.example.com,openai=https://openai-gateway.example.com
```

- HTTPS only, and a host name only. Address literals belong in the local list.
- The destination address must still be public at connection time. An allowed host name that
  resolves to a private, loopback or metadata address is refused.
- Allowing a host means trusting its operator with that engine's key. Add only hosts you
  control or contract with.

### `AEO_EGRESS_LOCAL_ORIGINS`: narrow loopback/private exceptions

```dotenv
# LM Studio on the LAN, a local Ollama, and an internal HTTPS host pinned to its subnet.
AEO_EGRESS_LOCAL_ORIGINS=custom=http://10.0.1.21:1234,custom=http://localhost:11434,custom=https://llm.internal:8443@10.0.1.0/24
```

- `http` or `https` is allowed.
- An IPv4/IPv6 literal origin can reach exactly that address.
- `localhost` can reach `127.0.0.1` and `::1` only.
- Any other host name needs an explicit `@address` or `@CIDR`. The resolved addresses must
  fall inside it.
- The address or range must lie inside one of `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`,
  `192.168.0.0/16`, `100.64.0.0/10`, `::1/128` or `fc00::/7`.
- Link-local addresses (`169.254.0.0/16`, `fe80::/10`) and cloud metadata or credential
  addresses (`169.254.169.254`, `169.254.170.2`, `169.254.170.23`, `fd00:ec2::254`,
  `fd00:ec2::23`, `fd20:ce::254`, `100.100.100.200`, `168.63.129.16`) can never be authorized,
  neither as a literal, as a DNS answer, nor inside an `@CIDR` range; a range that contains one
  is rejected. The IPv6 entries lie inside `fc00::/7`, so they are listed explicitly:
  `fd20:ce::254` is the Google Cloud IPv6 metadata server
  ([source](https://docs.cloud.google.com/compute/docs/metadata/overview)) and
  `fd00:ec2::23` / `169.254.170.23` are the AWS EKS Pod Identity credential endpoints
  ([agent setup](https://docs.aws.amazon.com/eks/latest/userguide/pod-id-agent-setup.html),
  [credential provider](https://docs.aws.amazon.com/aws-sdk-php/v3/api/class-Aws.Credentials.EcsCredentialProvider.html)).
  Other addresses in the same `/64` remain authorizable, for example
  `custom=http://internal.example@fd00:ec2::10/124`.
- An exception covers one engine. `custom=http://10.0.1.21:1234` does not let `openai` reach
  that address.

### Malformed policy fails closed

If any entry is malformed, the whole policy is discarded and every AEO provider request is
refused. Examples: an unknown engine, a path, a query, `http` in the allowed list, an address
outside the local ranges, or a range containing a metadata address. The CRM still starts.
Startup logs an error naming the variable and entry number, never the value. Fix the entry and
restart.

## Connection-time enforcement

Every engine builds its SDK client with an HTTP client from one shared guard
(`internal/aeo/egress.go`). This applies to Anthropic, OpenAI, Gemini, Kimi, Perplexity, custom,
and both the run engine and prompt generation. For each request:

1. The request URL must match the engine's approved origin, or it is refused locally.
2. On every new connection the approved host is resolved, and **every** returned address is
   checked. One unacceptable answer fails the connection, so a mixed public/private answer cannot
   be used to stage DNS rebinding. The dialer then connects to a validated address literal. No
   second DNS lookup happens.
3. Without a local exception, only globally routable unicast addresses are accepted. The guard
   refuses private, loopback, link-local, metadata, unspecified, broadcast, multicast,
   carrier-grade NAT, documentation, benchmarking and reserved ranges. In IPv6 it refuses
   unique-local, NAT64 and 6to4/Teredo addresses. IPv4-mapped IPv6 addresses are judged as the
   IPv4 address they carry. Zone identifiers are refused.
4. **Redirects are never followed.** Any 3xx response ends the query with
   `redirect_not_followed`, keeping only the numeric status. This applies to same-host
   downgrades and cross-origin targets alike. The `Location` header is never read. The SDKs
   retry transport errors once; a per-query latch makes that retry fail locally, so a refused
   redirect or destination never triggers a second request.
5. **Ambient proxies are ignored.** `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` do not apply to
   AEO traffic, so a proxy cannot route around the address policy. If you need an egress proxy,
   allow its origin explicitly and set it as the engine's base URL.
6. TLS certificate and host-name verification stay on (TLS 1.2 minimum). The verified name is the
   configured host, not the dialed literal. Context cancellation and the per-query timeout behave
   as before.

## Sanitized failure diagnostics

Provider error bodies are attacker-influenced. They can reflect the API key, the prompt or
arbitrary text, and an SDK error string can include the request URL. Redacting that text
reliably is not possible, so it is dropped. Every provider failure becomes an
`*aeo.ProviderError` with three parts:

- the engine name (restricted to a safe character set),
- one fixed category from the table below,
- the numeric HTTP status, when there was one.

The rendered message is at most 160 characters, for example
`gemini: provider unavailable (HTTP 503)`. This text is what `AEOAnswer.Error` stores, what logs
record (with `failure_category` and `http_status` fields), and what prompt generation returns.
The `PROVIDER_REJECTED` API response still reports the numeric status.

| Category | Meaning |
|---|---|
| `http_status` | The provider answered with an error status (401/403 authentication, 404, 429 rate limit, other 4xx, 5xx) |
| `timeout`, `canceled` | Deadline or cancellation; `errors.Is(err, context.DeadlineExceeded / context.Canceled)` still works |
| `endpoint_not_permitted` | The base URL is outside the egress policy (`errors.Is(err, aeo.ErrEndpointNotPermitted)`) |
| `destination_not_permitted` | A resolved address was refused (`aeo.ErrDestinationNotPermitted`) |
| `redirect_not_followed` | A 3xx response was refused (`aeo.ErrRedirectNotFollowed`) |
| `dns`, `tls`, `connection` | Transport failures |
| `invalid_response`, `empty_response` | Undecodable or empty provider response |
| `panic` | A provider panicked. The panic value is neither logged nor stored |
| `request_failed` | Anything else, including errors returned by custom `Provider` implementations |

`ProviderError.Unwrap` exposes only the trusted sentinels above, never the SDK error, so
neither `%v` formatting nor `errors.As` can bring the raw text back. Successful answers,
Perplexity's native citations, models, timeouts and the single SDK retry are unchanged.

To diagnose a failure, use the status and category. For the provider's own explanation, reproduce
the call outside the CRM with your own tooling.

## Migration checklist

1. **Hosted engines on official endpoints:** nothing to do.
2. **A base URL override on another host** (gateway, regional endpoint such as
   `api.moonshot.cn`, enterprise proxy): add it to `AEO_EGRESS_ALLOWED_ORIGINS` for that engine.
   Until you do, that engine's answers record `endpoint_not_permitted`.
3. **A custom engine** (`AEO_CUSTOM_BASE_URL`): add a `custom=` entry. Use the local list for
   LAN/loopback servers, for example `AEO_CUSTOM_BASE_URL=http://10.0.1.21:1234/v1` needs
   `AEO_EGRESS_LOCAL_ORIGINS=custom=http://10.0.1.21:1234`. Use the allowed list for a hosted
   HTTPS server.
4. **Stored overrides in Settings > AEO** using `http://`, credentials, a query or a fragment can
   no longer be saved. Existing rows are not rewritten. The guard refuses them, so clear or
   correct them in the settings page.
5. **Automation that parsed provider error text** from answers or generation responses should
   switch to the numeric status and the category.
6. After restarting, check the startup log for `AEO egress policy is malformed` and the provider
   log for `endpoint is not permitted by the egress policy`.

## Regression coverage

No test reaches a real provider or uses a real credential. The tests inject a scripted resolver,
a recording dialer and a test certificate authority. They also use an explicitly declared test
policy. Production defaults are not weakened, and no `localhost` or `.test` special case exists.

| Test | Package | Covers |
|---|---|---|
| `TestAEOEndpointSyntax` | `internal/models` | Settings validation: HTTPS, host, no userinfo/query (bare `?` included)/fragment/opaque form/zone/malformed port; blank keeps precedence |
| `TestAEOOperatorEgressConfig` | `internal/config` | Policy parsing, defaults, exact origins, local scoping, metadata/link-local refusal (hard-coded GCP and EKS IPv6 credential endpoints as literals and inside ranges), fail-closed errors without values |
| `TestAEOConfigTrustBoundary` | `internal/service` | Stored and legacy-invalid overrides cannot widen trust, for every engine; key/model/base URL precedence preserved |
| `TestAEOEndpointTrust` | `internal/aeo` | Every wiring path reaches its trusted origin; cross-provider, suffix, port and downgrade refused before any lookup, dial or request; malformed policy fails closed; ambient proxies ignored |
| `TestAEODialPolicy` | `internal/aeo` | Dials only validated literals; private, mapped, zoned, mixed and rebinding answers refused; local exceptions scoped and, even when built by hand, never dial `fd20:ce::254` or `fd00:ec2::23`; TLS verification and cancellation intact |
| `TestAEORedirectPolicy` | `internal/aeo` | 3xx responses never produce a second request, for any target |
| `TestAEOFailureSanitization` | `internal/aeo` | All providers: SDK errors, Gemini envelopes, non-JSON/oversized bodies, transport/TLS/DNS, cancellation, redirects; reflected fake keys and logger capture |
| `TestAEOFailurePersistence` | `internal/aeo` | Engine repository boundary: bounded `AEOAnswer.Error`, counts, status, hostile errors and panics, success path |
| `TestAEOGenerationFailureSanitization` | `internal/service` | Prompt generation with hostile errors and panics, captured logs, success path |
