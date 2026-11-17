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

# Bootstrap script for Cloud Cuttlefish Orchestrator
#
# This interactive wizard guides you through:
# 1. Prerequisites check
# 2. Project and region selection
# 3. Access configuration (IAP members)
# 4. terraform.tfvars generation
# 5. Terraform deployment
# 6. Next steps, from Terraform's next_steps output
#
# Usage: ./bootstrap.sh [--project PROJECT_ID]

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TERRAFORM_DIR="${SCRIPT_DIR}/../../terraform"

# Parse arguments
PROJECT_ID=""
while [[ $# -gt 0 ]]; do
    case $1 in
        --project)
            PROJECT_ID="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [--project PROJECT_ID]"
            echo ""
            echo "Bootstrap Cloud Cuttlefish Orchestrator deployment"
            echo ""
            echo "Options:"
            echo "  --project    GCP project ID (will prompt if not provided)"
            echo "  -h, --help   Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

echo -e "${CYAN}"
echo "╔═══════════════════════════════════════════════════════════════════════╗"
echo "║         Cloud Cuttlefish Orchestrator - Bootstrap Wizard              ║"
echo "╚═══════════════════════════════════════════════════════════════════════╝"
echo -e "${NC}"

# =============================================================================
# Prerequisites Check
# =============================================================================

echo -e "${BLUE}=== Step 1: Prerequisites Check ===${NC}"
echo ""

PREREQS_OK=true

# Check gcloud
if command -v gcloud &> /dev/null; then
    GCLOUD_VERSION=$(gcloud version 2>/dev/null | head -1 | awk '{print $4}')
    echo -e "${GREEN}✓${NC} gcloud CLI installed (version $GCLOUD_VERSION)"
else
    echo -e "${RED}✗${NC} gcloud CLI not found. Install from: https://cloud.google.com/sdk/docs/install"
    PREREQS_OK=false
fi

# Check terraform
if command -v terraform &> /dev/null; then
    TF_VERSION=$(terraform version -json 2>/dev/null | jq -r '.terraform_version' 2>/dev/null || terraform version | head -1 | awk '{print $2}')
    echo -e "${GREEN}✓${NC} Terraform installed (version $TF_VERSION)"
else
    echo -e "${RED}✗${NC} Terraform not found. Install from: https://developer.hashicorp.com/terraform/install"
    PREREQS_OK=false
fi

# Check jq (optional but helpful)
if command -v jq &> /dev/null; then
    echo -e "${GREEN}✓${NC} jq installed"
else
    echo -e "${YELLOW}!${NC} jq not found (optional, but recommended)"
fi

# Check authentication
if gcloud auth list --filter="status:ACTIVE" --format="value(account)" 2>/dev/null | head -1 | grep -q "@"; then
    ACTIVE_ACCOUNT=$(gcloud auth list --filter="status:ACTIVE" --format="value(account)" | head -1)
    echo -e "${GREEN}✓${NC} gcloud authenticated as: $ACTIVE_ACCOUNT"
else
    echo -e "${RED}✗${NC} gcloud not authenticated. Run: gcloud auth login"
    PREREQS_OK=false
fi

if [[ "$PREREQS_OK" != "true" ]]; then
    echo ""
    echo -e "${RED}Please install missing prerequisites and re-run this script.${NC}"
    exit 1
fi

echo ""

# =============================================================================
# Project Selection
# =============================================================================

echo -e "${BLUE}=== Step 2: Project Configuration ===${NC}"
echo ""

if [[ -z "$PROJECT_ID" ]]; then
    # Try to get default project
    DEFAULT_PROJECT=$(gcloud config get-value project 2>/dev/null || echo "")

    read -r -p "Enter GCP project ID [$DEFAULT_PROJECT]: " PROJECT_ID
    PROJECT_ID="${PROJECT_ID:-$DEFAULT_PROJECT}"
fi

if [[ -z "$PROJECT_ID" ]]; then
    echo -e "${RED}Project ID is required.${NC}"
    exit 1
fi

# Verify project exists and we have access
if ! gcloud projects describe "$PROJECT_ID" &>/dev/null; then
    echo -e "${RED}Cannot access project: $PROJECT_ID${NC}"
    echo "Make sure the project exists and you have access."
    exit 1
fi

echo -e "${GREEN}✓${NC} Project: $PROJECT_ID"

# Every gcloud call names the project, so the operator's gcloud configuration
# is never changed.
PROJECT_NUMBER=$(gcloud projects describe "$PROJECT_ID" --format="value(projectNumber)")
echo -e "${GREEN}✓${NC} Project Number: $PROJECT_NUMBER"

echo ""

# =============================================================================
# Region Selection
# =============================================================================

echo -e "${BLUE}=== Step 3: Region Selection ===${NC}"
echo ""

# No default: the region decides where the service, the database and the
# hosts live, and an organization policy may allow only some regions.
echo "Enter the region to deploy into, for example us-central1. Cloud Run,"
echo "Spanner and the host VMs all live there."
echo ""

read -r -p "Region: " REGION
if [[ ! "$REGION" =~ ^[a-z]+-[a-z]+[0-9]+$ ]]; then
    echo -e "${RED}A region name is required (for example us-central1), got: '$REGION'${NC}"
    exit 1
fi

echo -e "${GREEN}✓${NC} Region: $REGION"
echo ""

# =============================================================================
# Access Configuration
# =============================================================================

echo -e "${BLUE}=== Step 4: Access Configuration ===${NC}"
echo ""
echo "The orchestrator is protected by built-in Cloud Run IAP."
echo "The IAP access list is the ENTIRE access model: anyone on it has full"
echo "access to everything; anyone not on it cannot reach the app."
echo ""
echo "Enter the principals to grant access, comma-separated. Examples:"
echo "  user:alice@example.com"
echo "  group:team@example.com"
echo "  serviceAccount:ci@$PROJECT_ID.iam.gserviceaccount.com"
echo ""

ACTIVE_ACCOUNT=$(gcloud auth list --filter="status:ACTIVE" --format="value(account)" | head -1)
read -r -p "IAP members [user:$ACTIVE_ACCOUNT]: " IAP_MEMBERS_INPUT
IAP_MEMBERS_INPUT="${IAP_MEMBERS_INPUT:-user:$ACTIVE_ACCOUNT}"

# Convert to Terraform list format
IAP_MEMBERS=$(echo "$IAP_MEMBERS_INPUT" | tr ',' '\n' | while read -r m; do echo "  \"$(echo "$m" | xargs)\","; done)

echo -e "${GREEN}✓${NC} IAP members captured"
echo ""

# =============================================================================
# Deployment Topology
# =============================================================================

echo "Where should WebRTC device media meet the internet?"
echo "  1) public  - host VMs get public IPs; direct media, TURN optional [default]"
echo "  2) private - host VMs internal-only; clients connect over a VPN (no TURN)"
echo "  3) relay   - host VMs internal-only; media relayed through TURN"
echo ""
echo "This wizard always uses built-in Cloud Run IAP (auth = \"iap\"). For"
echo "network mode (no authentication, private ingress only), edit"
echo "terraform.tfvars by hand."
echo ""

read -r -p "Select topology [1]: " TOPOLOGY_CHOICE
TOPOLOGY_CHOICE="${TOPOLOGY_CHOICE:-1}"

AUTH="iap"
CLIENT_CIDRS_LINE=""
case $TOPOLOGY_CHOICE in
    2)
        INGRESS="private"
        DEVICE_INGRESS="private"
        read -r -p "Client CIDR ranges (comma-separated, e.g. 10.8.0.0/24): " CIDRS_INPUT
        CIDRS=$(echo "$CIDRS_INPUT" | tr ',' '\n' | while read -r c; do echo "  \"$(echo "$c" | xargs)\","; done)
        CLIENT_CIDRS_LINE=$(printf 'client_cidrs = [\n%s\n]' "$CIDRS")
        ;;
    3)
        INGRESS="public"
        DEVICE_INGRESS="relay"
        ;;
    *)
        INGRESS="public"
        DEVICE_INGRESS="public"
        ;;
esac

echo -e "${GREEN}✓${NC} Topology: ingress=$INGRESS auth=$AUTH device_ingress=$DEVICE_INGRESS"
echo ""

# =============================================================================
# Generate terraform.tfvars
# =============================================================================

echo -e "${BLUE}=== Step 5: Generate terraform.tfvars ===${NC}"
echo ""

TFVARS_FILE="${TERRAFORM_DIR}/terraform.tfvars"

if [[ -f "$TFVARS_FILE" ]]; then
    echo -e "${YELLOW}terraform.tfvars already exists.${NC}"
    read -r -p "Overwrite? [y/N]: " OVERWRITE
    if [[ "$OVERWRITE" != "y" && "$OVERWRITE" != "Y" ]]; then
        echo "Keeping existing terraform.tfvars"
    else
        # Backup existing
        cp "$TFVARS_FILE" "${TFVARS_FILE}.bak.$(date +%Y%m%d%H%M%S)"
    fi
fi

cat > "$TFVARS_FILE" << EOF
# Cloud Cuttlefish Orchestrator - Generated by bootstrap.sh
# Generated: $(date)

# =============================================================================
# Required Configuration
# =============================================================================

project_id = "$PROJECT_ID"
region     = "$REGION"

# =============================================================================
# Deployment topology (three settings)
# =============================================================================
#
# ingress = reachability, auth = front door, device_ingress = WebRTC media.
# The iap_members list is the entire access model (auth = "iap"): anyone on it
# has full access; anyone not on it cannot reach the app.

ingress        = "$INGRESS"
auth           = "$AUTH"
device_ingress = "$DEVICE_INGRESS"
$CLIENT_CIDRS_LINE
iap_members = [
$IAP_MEMBERS
]

# =============================================================================
# Resource Configuration
# =============================================================================

service_name = "cuttlefish-orchestrator"

# Cloud Run
cloud_run_cpu           = 4
cloud_run_memory        = "8Gi"
cloud_run_min_instances = 0
cloud_run_max_instances = 10

# Spanner
create_spanner           = true
spanner_instance_name    = "cuttlefish-orchestrator"
spanner_database_name    = "orchestrator"
spanner_processing_units = 100

# Networking
create_network = true
network_name   = "cuttlefish-network"
subnet_cidr    = "10.0.0.0/16"

# =============================================================================
# Labels
# =============================================================================

labels = {
  app         = "cuttlefish-orchestrator"
  component   = "orchestrator"
  environment = "production"
}
EOF

echo -e "${GREEN}✓${NC} Generated: $TFVARS_FILE"
echo ""

# =============================================================================
# Terraform Init and Plan
# =============================================================================

echo -e "${BLUE}=== Step 6: Terraform Initialization ===${NC}"
echo ""

cd "$TERRAFORM_DIR"

echo "Running terraform init..."
terraform init

echo ""
echo "Running terraform plan..."
# An empty image keeps the image the service runs now. Before the service
# exists there is none to keep, so the first plan passes the placeholder.
PLAN_ARGS=()
if ! terraform state list 2>/dev/null | grep -q 'google_cloud_run_v2_service\.orchestrator$'; then
    PLAN_ARGS=(-var "image=us-docker.pkg.dev/cloudrun/container/hello")
fi
terraform plan "${PLAN_ARGS[@]}" -out=tfplan

echo ""
echo -e "${GREEN}✓${NC} Terraform plan created"
echo ""

# =============================================================================
# Terraform Apply
# =============================================================================

echo -e "${BLUE}=== Step 7: Deploy Infrastructure ===${NC}"
echo ""

read -r -p "Ready to deploy? This will create GCP resources and incur costs. [y/N]: " DEPLOY
if [[ "$DEPLOY" != "y" && "$DEPLOY" != "Y" ]]; then
    echo ""
    echo "Deployment cancelled. To deploy later, run:"
    echo "  cd $TERRAFORM_DIR"
    echo "  terraform apply tfplan"
    exit 0
fi

echo ""
echo "Deploying infrastructure..."
terraform apply tfplan

echo ""
echo -e "${GREEN}✓${NC} Infrastructure deployed!"
echo ""

# =============================================================================
# Post-Deployment Steps
# =============================================================================

echo -e "${BLUE}=== Step 8: Post-Deployment ===${NC}"
echo ""

# Terraform's next_steps output is the one copy of these instructions.
terraform output -raw next_steps
echo ""
echo -e "${CYAN}Optional: Workload Identity Federation (for CI/CD):${NC}"
echo ""
echo "To set up keyless authentication from GitHub Actions or GitLab CI:"
echo "   see terraform/modules/wif/"
echo ""

echo -e "${GREEN}Bootstrap complete!${NC}"
