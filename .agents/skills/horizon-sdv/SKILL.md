---
name: horizon-sdv
description: Guides and automates the deployment, operation, and maintenance of the Horizon SDV platform on Google Cloud Platform.
---

# Horizon SDV Custom Deployment Skill (Release 4.1)

This skill contains the authoritative guides, runbooks, and automation flows to deploy and operate the Accenture Horizon SDV platform, specifically targeting **Horizon SDV Release 4.1**.

## 1. Prerequisites & Required GCP APIs

To deploy Horizon SDV successfully, ensure that the following GCP APIs are enabled on your project:
*   Artifact Registry API (`artifactregistry.googleapis.com`)
*   Certificate Manager API (`certificatemanager.googleapis.com`)
*   Cloud DNS API (`dns.googleapis.com`)
*   Compute Engine API (`compute.googleapis.com`)
*   Kubernetes Engine API (`container.googleapis.com`)
*   GKE Hub API (`gkehub.googleapis.com`)
*   Secret Manager API (`secretmanager.googleapis.com`)
*   Parameter Manager API (`parametermanager.googleapis.com`)
*   Service Usage API (`serviceusage.googleapis.com`)
*   Vertex AI API (`aiplatform.googleapis.com`)

## 2. Organization Policy Remediations

Corporate parent organizations often enforce strict rules that will cause standard GKE clusters or emulator VM deployment to fail. Apply these override policies at the project level:

### Policy 1: Service Account Key Creation
*   **Constraint**: `constraints/iam.disableServiceAccountKeyCreation`
*   **File**: `disable-sa-key-creation.yaml`
    ```yaml
    name: projects/<PROJECT_ID>/policies/iam.disableServiceAccountKeyCreation
    spec:
      rules:
      - enforce: false
    ```
*   **Command**: `gcloud org-policies set-policy disable-sa-key-creation.yaml --project=<PROJECT_ID>`

### Policy 2: Nested Virtualization (Required for Android Cuttlefish/ARM64 Emulators)
*   **Constraint**: `constraints/compute.disableNestedVirtualization`
*   **File**: `disable-nested-virtualization.yaml`
    ```yaml
    name: projects/<PROJECT_ID>/policies/compute.disableNestedVirtualization
    spec:
      rules:
      - enforce: false
    ```
*   **Command**: `gcloud org-policies set-policy disable-nested-virtualization.yaml --project=<PROJECT_ID>`

### Policy 3: External VM IPs (Required for Load Balancer endpoints)
*   **Constraint**: `constraints/compute.vmExternalIpAccess`
*   **File**: `allow-vm-external-ips.yaml`
    ```yaml
    name: projects/<PROJECT_ID>/policies/compute.vmExternalIpAccess
    spec:
      rules:
      - allowAll: true
    ```
*   **Command**: `gcloud org-policies set-policy allow-vm-external-ips.yaml --project=<PROJECT_ID>`

---

## 3. Configuration Setup (`terraform.tfvars`)

Copy the sample file and fill in your variables:
```bash
cp terraform/env/terraform.tfvars.sample terraform/env/terraform.tfvars
```
Ensure you have the following critical variables:
```hcl
sdv_gcp_project_id           = "<PROJECT_ID>"
sdv_gcp_backend_bucket       = "<TERRAFORM_STATE_BUCKET_NAME>"
sdv_root_domain              = "<SUB_DOMAIN>.<PARENT_DOMAIN>"
scm_auth_method              = "none" # For public repositories
sdv_dns_use_static_a_records = true   # For static A record configuration
```

---

## 4. Execution Procedures

Always run the deployment steps using the container wrapper to guarantee tool chain consistency:

### A. Run Terraform Plan (Dry-run)
```bash
./tools/scripts/deployment/container-deploy.sh --plan
```

### B. Run Terraform Apply (Deployment)
```bash
./tools/scripts/deployment/container-deploy.sh --apply
```

### C. Run Terraform Destroy (Teardown)
```bash
./tools/scripts/deployment/container-deploy.sh --destroy
```

---

## 5. Post-Deployment Verification (Static A Records Setup)

Once the deployment completes, the Load Balancer IP must be mapped to your custom subdomains:

1.  **Retrieve Load Balancer IP**:
    Find the external IP address of the deployed Ingress Load Balancer:
    ```bash
    gcloud compute addresses list --project=<PROJECT_ID>
    ```
2.  **Add A-Records**:
    Manually create two `A` records in your DNS domain registrar pointing to that Load Balancer IP:
    *   `horizon-<ID>.<PARENT_DOMAIN>` -> `LOAD_BALANCER_IP`
    *   `*.horizon-<ID>.<PARENT_DOMAIN>` -> `LOAD_BALANCER_IP`
3.  **Confirm Certificates**:
    Wait for the GKE managed certificates to validate. You can verify their status via the Google Cloud Console under **Network Security -> Certificate Manager**.
