#!/bin/bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Setup GKE Workload Identity for Argo Workflows to access CCO
#
# This script configures the IAM bindings needed for Argo Workflows running on
# GKE to authenticate to CCO using GKE Workload Identity.
#
# Prerequisites:
# - GKE cluster with Workload Identity enabled
# - gcloud CLI authenticated with appropriate permissions
# - CCO deployed with auth = "iap" (built-in Cloud Run IAP)
#
# Usage:
#   ./setup-gke-workload-identity.sh \
#     --project=my-project \
#     --cco-project=cco-project \
#     --gke-namespace=argo \
#     --ksa-name=cco-ci-sa

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Default values
GKE_PROJECT=""
CCO_PROJECT=""
GKE_NAMESPACE="argo"
KSA_NAME="cco-ci-sa"
GSA_NAME="cco-argo-sa"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --project=*)
            GKE_PROJECT="${1#*=}"
            shift
            ;;
        --cco-project=*)
            CCO_PROJECT="${1#*=}"
            shift
            ;;
        --gke-namespace=*)
            GKE_NAMESPACE="${1#*=}"
            shift
            ;;
        --ksa-name=*)
            KSA_NAME="${1#*=}"
            shift
            ;;
        --gsa-name=*)
            GSA_NAME="${1#*=}"
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [options]"
            echo ""
            echo "Setup GKE Workload Identity for Argo Workflows to access CCO."
            echo ""
            echo "Options:"
            echo "  --project=PROJECT        GKE project ID (where Argo runs)"
            echo "  --cco-project=PROJECT    CCO project ID (where CCO is deployed)"
            echo "  --gke-namespace=NS       Kubernetes namespace for Argo (default: argo)"
            echo "  --ksa-name=NAME          Kubernetes ServiceAccount name (default: cco-ci-sa)"
            echo "  --gsa-name=NAME          Google ServiceAccount name (default: cco-argo-sa)"
            echo "  -h, --help               Show this help message"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Validate required arguments
if [[ -z "$GKE_PROJECT" ]]; then
    echo -e "${RED}Error: --project is required${NC}"
    exit 1
fi

if [[ -z "$CCO_PROJECT" ]]; then
    CCO_PROJECT="$GKE_PROJECT"
    echo -e "${YELLOW}Using GKE project as CCO project: $CCO_PROJECT${NC}"
fi

echo ""
echo "=== GKE Workload Identity Setup for CCO ==="
echo ""
echo "GKE Project:     $GKE_PROJECT"
echo "CCO Project:     $CCO_PROJECT"
echo "GKE Namespace:   $GKE_NAMESPACE"
echo "KSA Name:        $KSA_NAME"
echo "GSA Name:        $GSA_NAME"
echo ""

# Step 1: Create Google Service Account
echo -e "${GREEN}Step 1: Creating Google Service Account...${NC}"
if gcloud iam service-accounts describe "${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com" &>/dev/null; then
    echo "  Service account already exists"
else
    gcloud iam service-accounts create "$GSA_NAME" \
        --project="$CCO_PROJECT" \
        --display-name="CCO Argo Workflows SA" \
        --description="Service account for Argo Workflows to access CCO via GKE Workload Identity"
    echo "  Created service account: ${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com"
fi

# Step 2: Put the service account on the IAP access list
echo ""
echo -e "${GREEN}Step 2: Add the service account to the IAP access list...${NC}"
echo ""
echo "  CCO uses built-in Cloud Run IAP, and the iap_members list is the whole"
echo "  access model. Add this principal to iap_members in your"
echo "  terraform/terraform.tfvars, then re-apply:"
echo ""
echo "    iap_members = ["
echo "      # ... the principals already listed ..."
echo "      \"serviceAccount:${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com\","
echo "    ]"
echo ""
echo "    terraform -chdir=terraform apply"

# Step 3: Create Kubernetes ServiceAccount (if it doesn't exist)
echo ""
echo -e "${GREEN}Step 3: Creating Kubernetes ServiceAccount...${NC}"
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: $KSA_NAME
  namespace: $GKE_NAMESPACE
  annotations:
    iam.gke.io/gcp-service-account: ${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com
EOF
echo "  Created/updated KSA: $KSA_NAME in namespace $GKE_NAMESPACE"

# Step 4: Bind KSA to GSA via Workload Identity
echo ""
echo -e "${GREEN}Step 4: Binding KSA to GSA via Workload Identity...${NC}"
gcloud iam service-accounts add-iam-policy-binding \
    "${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com" \
    --project="$CCO_PROJECT" \
    --role="roles/iam.workloadIdentityUser" \
    --member="serviceAccount:${GKE_PROJECT}.svc.id.goog[${GKE_NAMESPACE}/${KSA_NAME}]"
echo "  Bound ${GKE_NAMESPACE}/${KSA_NAME} to ${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com"

# Step 5: Let the service account sign its own IAP JWT
echo ""
echo -e "${GREEN}Step 5: Granting the service account signJwt on itself...${NC}"
gcloud iam service-accounts add-iam-policy-binding \
    "${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com" \
    --project="$CCO_PROJECT" \
    --role="roles/iam.serviceAccountTokenCreator" \
    --member="serviceAccount:${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com"
echo "  cfctl signs the IAP JWT as this account, so it must be able to sign"
echo "  for itself. There is nothing to register in CCO. The iap_members list"
echo "  from step 2 is the whole access model."

# Summary
echo ""
echo "=== Setup Complete ==="
echo ""
echo "To use this in your Argo Workflow:"
echo ""
echo "  spec:"
echo "    serviceAccountName: $KSA_NAME"
echo "    templates:"
echo "      - name: my-template"
echo "        container:"
echo "          image: google/cloud-sdk:slim"
echo "          command: [cfctl, device, list]"
echo "          env:"
echo "            - name: CFCTL_SERVER"
echo "              value: https://cco.example.com"
echo "            - name: CFCTL_IMPERSONATE"
echo "              value: ${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com"
echo ""
echo "The pod will automatically authenticate as:"
echo "  ${GSA_NAME}@${CCO_PROJECT}.iam.gserviceaccount.com"
echo ""
