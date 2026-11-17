# Authentication

Authentication is the only gate. There are no roles, permissions,
organizations, or teams, and anyone who passes the front door has full access
to everything. The front door is one per-deployment choice (`auth`):

- **`iap`** (the default, and everything below unless said otherwise).
  **Built-in Cloud Run IAP** (Identity-Aware Proxy) verifies every caller;
  anyone not on the IAP access list cannot reach the app at all.
- **`network`** turns authentication off; see [Network Mode](#network-mode).

## Overview

```
┌──────────────────────────────────────────────────────────────┐
│                     Authentication Flow                       │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌─────────────────┐            ┌─────────────────┐          │
│  │  Web Browser    │            │  CLI / CI/CD    │          │
│  │  (Interactive)  │            │  (Programmatic) │          │
│  └────────┬────────┘            └────────┬────────┘          │
│           │ IAP sign-in                  │ Google ID token   │
│           │ (accounts.google.com)        │ (Bearer header)   │
│           └────────────┬─────────────────┘                   │
│                        ▼                                      │
│           ┌──────────────────────┐                            │
│           │  Cloud Run + IAP     │  access checked against   │
│           │  (Orchestrator)      │  the IAP access list      │
│           └──────────────────────┘                            │
│                                                               │
└──────────────────────────────────────────────────────────────┘
```

## The Access Model

Access is granted by putting a principal on the **IAP access list**, the
`roles/iap.httpsResourceAccessor` binding on the Cloud Run service, managed
via the `iap_members` Terraform variable:

```hcl
iap_members = [
  "user:alice@example.com",
  "group:team@example.com",
  "serviceAccount:ci@PROJECT.iam.gserviceaccount.com",
]
```

That list **is** the entire access model. There is no admin/developer split
and no in-app user management.

## Web Browser Authentication

1. The browser requests the `run.app` URL.
2. IAP detects no session and redirects to Google sign-in.
3. After sign-in, IAP checks the user against the access list.
4. IAP forwards the request with a signed `X-Goog-IAP-JWT-Assertion`
   header; the backend validates its signature, issuer
   (`https://cloud.google.com/iap`), and audience
   (`/projects/NUMBER/locations/REGION/services/NAME`).
5. The SPA loads the verified identity from `/v2/me`
   (`{email, display_name}`).

Sessions are managed entirely by IAP. Sign-out redirects to
`/?gcp-iap-mode=CLEAR_LOGIN_COOKIE`.

## Programmatic Authentication (CLI / CI/CD)

`cfctl` and CI pipelines authenticate a **service account** and send a token as
`Authorization: Bearer <token>`. The calling service account must be on the IAP
access list (`iap_members`) like any other principal.

IAP accepts two token types, and `cfctl` picks one automatically based on
whether an audience is configured:

- **Self-signed service-account JWT** (the default, with no `iap_audience`
  set). The service account signs a JWT with `aud =` the service URL
  (`<service-url>/*`). This is the path that works with the built-in
  **Google-managed** OAuth client, so it needs **no custom OAuth client**. The
  service account only has to be on `iap_members`.
- **OIDC ID token** (used when `iap_audience` *is* set). The token's `aud` is a
  custom IAP OAuth client ID. Required for user/browser-interactive access;
  optional for service accounts.

`cfctl` derives the signing identity from, in order:

1. **Service account impersonation** (`cfctl config set-impersonate`) signs
   the JWT via the IAM Credentials API. The caller needs
   `roles/iam.serviceAccountTokenCreator` on the target SA.
2. **A service account key** (`cfctl config set-credentials`) signs locally.
3. **Workload Identity Federation / attached SA** via ADC, on the OIDC path
   only (an audience is configured). The self-signed default has no ADC step.
   With no impersonation and no key configured, `cfctl` stops with guidance
   instead of sending an unauthenticated request. Keyless CI against the
   managed client therefore combines WIF credentials with the impersonation in
   step 1.

Plain `gcloud auth application-default login` *user* credentials cannot sign a
service-account JWT or mint an audience-scoped ID token. Use impersonation.

### Service account access without an OAuth client (recommended)

```bash
# Add serviceAccount:SA_EMAIL to iap_members and `terraform apply` first.
cfctl config set-server "$(terraform output -raw service_url)"
cfctl config set-impersonate SA_EMAIL   # or: cfctl config set-credentials key.json
# Leave the audience unset -> self-signed JWT (aud = service URL).
cfctl auth whoami
```

### User and browser-interactive access with a custom OAuth client

A *human* developer authenticating with their own Google account (not a service
account) cannot use the managed client programmatically, and the IAP OAuth Admin
API was shut down in March 2026. A **custom OAuth client** must therefore be
created once by hand:

1. Cloud console → **APIs & Services → Credentials → Create OAuth client ID →
   Web application**, then add the authorized redirect URI
   `https://iap.googleapis.com/v1/oauth/clientIds/CLIENT_ID:handleRedirect`.
2. Set `iap_oauth_client_id` in `terraform.tfvars` and run `terraform apply`.
   Terraform allowlists it via `google_iap_settings`
   (`oauth_settings.programmatic_clients`). The browser SPA flow keeps using the
   managed client either way.
3. `cfctl config set-audience "$(terraform output -raw cfctl_audience)"`. In
   iap mode that resolves to the custom client ID, switching cfctl to the OIDC
   token path. (Do **not** use the `iap_audience` output; that is the backend's
   assertion audience, a different value.)

Leave `iap_oauth_client_id` empty if only service accounts need programmatic
access.

## Network Mode

`auth = "network"` removes the cryptographic gate entirely. Cloud Run's
invoker IAM check is disabled, IAP is off, and the app validates nothing.
Terraform forces `ingress = "private"`, making **VPC reachability the entire
access model**. It exists for legacy clients that cannot present a credential
(OmniLab ATS hardcodes upstream Cloud Orchestrator's `username-only` Basic
header) and makes the same trust assumption as that upstream deployment.

Every request is attributed to **one fixed synthetic principal**
(`anonymous@network.internal`). The mode also:

- **Reads no identity header.** A self-asserted username or a spoofed
  identity header is never echoed. A forgeable name in the audit log would
  claim more trust than the mode provides.
- **Validates no token.** A request carrying a valid token gets the same
  synthetic principal as one carrying nothing.

As a result, `mine=true` means everything, `created_by` is
uniform, the audit log has one writer, and the console is reachable without
sign-in from inside the VPC. Point cfctl at such a deployment with
`cfctl config set-auth none`.

## CSRF Protection

The browser console authenticates via the ambient IAP session cookie, so
state-changing requests are protected by `net/http.CrossOriginProtection`
(Go 1.26), which rejects unsafe-method cross-site browser requests via the
`Sec-Fetch-Site` header. Bearer-token clients are unaffected.

## Local Development

For `npm start` against a local backend, enable dev mode via environment
variables. Put them in your local `.env` (see `.env.example`):

```bash
CCO_DEV_MODE=true                        # never in production
CCO_DEV_FALLBACK_EMAIL=dev@example.com
```

Dev mode trusts the plain `X-Goog-Authenticated-User-Email` header and
falls back to the configured email when the header is absent. Because that
header is forgeable, the server **refuses to start** with dev mode enabled
on Cloud Run or any other GCP environment. A deployed service always
validates a signed IAP assertion or Google ID token.
