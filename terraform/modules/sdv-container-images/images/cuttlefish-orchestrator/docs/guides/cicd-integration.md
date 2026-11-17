# Workload Identity Federation for CI/CD

## Overview

CI/CD pipelines authenticate to CCO **keylessly** with Workload Identity
Federation (WIF): the CI provider's OIDC token is exchanged for a Google ID
token, which is sent to the IAP-protected Cloud Run service as
`Authorization: Bearer <token>`.

There is no service-account allowlist and no role to assign. The CI
service account only needs to be on the **IAP access list** like any other
principal.

## Architecture

```
GitHub Actions / GitLab CI
        │ OIDC token
        ▼
Workload Identity Pool ──impersonates──► CI service account
        │                                     │
        │            Google ID token (audience = IAP client ID)
        ▼                                     ▼
              Cloud Run + IAP  ◄── must be in iap_members
```

## Setup

1. Apply the WIF module (creates the pool, OIDC providers, and CI service
   account):

   ```hcl
   module "wif" {
     source     = "./modules/wif"
     project_id = var.project_id

     enable_github_provider      = true
     github_attribute_condition  = "assertion.repository_owner == 'your-org'"

     create_service_account = true
     service_account_id     = "cco-cicd"

     # Puts the CI service account on the IAP access list
     cloud_run_service_name = "cuttlefish-orchestrator"
     cloud_run_location     = "us-central1"   # required, no default
   }
   ```

2. Alternatively, add the CI service account to the root module's
   `iap_members` list:

   ```hcl
   iap_members = [
     "user:you@example.com",
     "serviceAccount:cco-cicd@PROJECT.iam.gserviceaccount.com",
   ]
   ```

## GitHub Actions

```yaml
permissions:
  id-token: write
  contents: read

steps:
  - uses: actions/checkout@v4

  - id: auth
    uses: google-github-actions/auth@v2
    with:
      workload_identity_provider: ${{ vars.WIF_PROVIDER }}
      service_account: cco-cicd@PROJECT.iam.gserviceaccount.com

  - name: Use cfctl
    env:
      CFCTL_SERVER: https://cuttlefish-orchestrator-xxxx.a.run.app
      # Managed-client IAP: cfctl signs a self-signed JWT as the CI service
      # account (aud = service URL). The SA must be on iap_members and able to
      # sign its own JWT (roles/iam.serviceAccountTokenCreator on itself).
      CFCTL_IMPERSONATE: cco-cicd@PROJECT.iam.gserviceaccount.com
    run: |
      cfctl auth whoami
      cfctl device list
```

The environment variables configure cfctl completely, so a CI runner never
needs a config file. `cfctl` picks up the federated credentials
automatically via Application Default Credentials and signs the JWT as the
impersonated service account. If your deployment uses a **custom IAP OAuth
client** instead, replace `CFCTL_IMPERSONATE` with
`CFCTL_AUDIENCE: <iap-oauth-client-id>`. The federated credentials then
mint OIDC tokens for that client directly.

## GitLab CI

```yaml
deploy:
  image: google/cloud-sdk:slim
  id_tokens:
    GITLAB_OIDC_TOKEN:
      aud: https://iam.googleapis.com/<workload-identity-provider>
  script:
    - gcloud iam workload-identity-pools create-cred-config
        <workload-identity-provider>
        --service-account=cco-cicd@PROJECT.iam.gserviceaccount.com
        --output-file=credentials.json
        --credential-source-file=$CI_JOB_JWT_V2
    - export GOOGLE_APPLICATION_CREDENTIALS=credentials.json
    - export CFCTL_SERVER=<service-url>
    - export CFCTL_IMPERSONATE=cco-cicd@PROJECT.iam.gserviceaccount.com
    - cfctl device list
```

## Verification

```bash
cfctl auth whoami     # prints the CI service account email
cfctl host list
```

If authentication fails, check that:

1. The CI service account is on the IAP access list
   (`iap_members` or the WIF module's `cloud_run_service_name` grant).
2. For the self-signed JWT path, the CI service account can sign its own JWT
   (`roles/iam.serviceAccountTokenCreator` on itself); for a custom-client
   deployment, the configured audience matches the IAP OAuth client ID.
3. The WIF provider's attribute condition matches your repository.
