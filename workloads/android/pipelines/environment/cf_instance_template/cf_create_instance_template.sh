#!/usr/bin/env bash

# Copyright (c) 2024-2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#        http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Description:
# Create the Cuttlefish boilerplate template instance for use with Jenkins
# GCE plugin and standalone gcloud compute CLI.
#
# To run, ensure gcloud is set up, authenticated and tunneling is
# configured, eg. --tunnel-through-iap.
#
# From command line, such as Google Cloud Shell, create templates for all
# versions of android-cuttlefish host tools/packages:
#
#  CUTTLEFISH_REVISION=v1.41.0 ./cf_create_instance_template.sh && \
#  CUTTLEFISH_REVISION=main ./cf_create_instance_template.sh
#
# The following variables are required to run the script, choose to use
# default values or override from command line.
#
#  - ADDITIONAL_NETWORKING: ARM64 Bare metal requires IDPF network interface.
#  - CURL_UPDATE_COMMAND: command to update/upgrade Curl
#        eg. Debian backports: apt install -t bookworm-backports -y curl libcurl4
#  - CUSTOM_VM_TYPE: Custom machine VM type.
#  - CUSTOM_CPU: Custom machine CPUs.
#  - CUSTOM_MEMORY: Custom machine memory.
#  - CUTTLEFISH_REVISION: the branch/tag version of Android Cuttlefish
#        to use. Default: main
#  - CUTTLEFISH_URL: the repo URL for android cuttlefish.
#  - CUTTLEFISH_POST_COMMAND: command to run in android-cuttlefish repo.
#  - BOOT_DISK_SIZE: Disk image size in GB. Default: 250GB
#  - BOOT_DISK_TYPE: Disk image disk type.
#  - DEFAULT_USER: VM instance default account username
#  - JAVA_VERSION: Apt JDK package (default temurin-21-jdk). Names starting with temurin- add Adoptium apt.
#  - MACHINE_TYPE: The machine type to create instance templates for.
#       If undefined, the CUSTOM_ parameters must be.
#  - MAX_RUN_DURATION: Limits how long this VM instance can run. Default: 10h
#  - NAMESPACE: k8s namespace. Default: jenkins
#  - NETWORK: The name of the VPC network. Default: sdv-network
#  - NODEJS_VERSION: The version of nodejs to install. Default: 20.9.0
#  - OS_VERSION: Default: debian-12-bookworm-v20260114
#  - PROJECT: The GCP project. Default: derived from gcloud config.
#  - REGION: The GCP region. Default: europe-west1
#  - REPO_USERNAME: username for access to private repo.
#  - REPO_PASSWORD: password for access to private repo.
#  - SERVICE_ACCOUNT: The GCP service account. Default: derived from gcloud
#        projects describe.
#  - SSH_PRIVATE_KEY_NAME: SSH key name to extract public key from
#        Private key would be created similar to:
#        ssh-keygen -t rsa -b 4096 -C "jenkins" -f jenkins_private_key -q -N ""
#        Ensure new line:
#        echo "" >> jenkins_private_key
#        -C: comment 'jenkins'
#        -N: no passphrase
#        This should produce an OpenSSH private and public key.
#        Then added to k8s secrets and defined in Jenkins credentials.
#        Default: jenkins-cuttlefish-vm-ssh-private-key
#  - SSH_PUBLIC_KEY_FILENAME: Public key file name.
#        Default: jenkins_private_key.pub
#  - SUBNET: The name of the subnet. Default: sdv-subnet
#  - CUTTLEFISH_INSTANCE_NAME: The name used to identify the instance.
#        Default: cuttlefish-vm-<branch-name>
#  - UPDATE_SSH_AUTHORIZED_KEYS: set true to refresh template key metadata
#        without rebuilding the image.
#  - ZONE: The GCP zone. Default: europe-west1-d
#
# The following arguments are optional and recommended run without args:

#  -h|--help :     - Print usage
#  1 : Run stage 1 - Build Cuttlefish image with Packer and create
#                    instance template from the baked image.
#  2 : Run stage 2 - Refresh SSH authorized_keys metadata on template
#                    without rebuilding image.
#  3 : Run stage 3 - Delete instances, templates and images.
#  No args:          run stage 1.
# Include common functions and variables.
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")"/cf_environment.sh "$0"

# Colours for logging.
GREEN='\033[1;32m'
ORANGE='\033[1;33m'
RED='\033[1;31m'
NC='\033[0m'
SCRIPT_NAME=$(basename "$0")

# Environment variables that can be overridden from command line.
# android-cuttlefish revisions can be of the form v1.7.0, main etc.
ADDITIONAL_NETWORKING=${ADDITIONAL_NETWORKING:-}
[ -n "${ADDITIONAL_NETWORKING}" ] && ADDITIONAL_NETWORKING=",${ADDITIONAL_NETWORKING}"
BOOT_DISK_SIZE=${BOOT_DISK_SIZE:-500GB}
BOOT_DISK_SIZE=$(echo "${BOOT_DISK_SIZE}" | awk '{print toupper($0)}' | xargs)
BOOT_DISK_TYPE=${BOOT_DISK_TYPE:-pd-balanced}
CURL_UPDATE_COMMAND=${CURL_UPDATE_COMMAND:-}
CUTTLEFISH_INSTANCE_NAME=${CUTTLEFISH_INSTANCE_NAME:-cuttlefish-vm}
CUTTLEFISH_INSTANCE_NAME=$(echo "${CUTTLEFISH_INSTANCE_NAME}" | awk '{print tolower($0)}' | xargs)
CUTTLEFISH_REVISION=${CUTTLEFISH_REVISION:-v1.41.0}
CUTTLEFISH_REVISION=$(echo "${CUTTLEFISH_REVISION}" | xargs)
CUTTLEFISH_URL=${CUTTLEFISH_URL:-https://github.com/google/android-cuttlefish.git}
CUTTLEFISH_URL=$(echo "${CUTTLEFISH_URL}" | xargs)
CUTTLEFISH_POST_COMMAND=${CUTTLEFISH_POST_COMMAND:-}
DEFAULT_USER=${DEFAULT_USER:-jenkins}
JAVA_VERSION=${JAVA_VERSION:-temurin-21-jdk}
MACHINE_TYPE=${MACHINE_TYPE:-}
MACHINE_TYPE=$(echo "${MACHINE_TYPE}" | xargs)
MAX_RUN_DURATION=${MAX_RUN_DURATION:-10h}
NAMESPACE=${NAMESPACE:-jenkins}
NETWORK=${NETWORK:-sdv-network}
NODEJS_VERSION=${NODEJS_VERSION:-20.9.0}
NODEJS_VERSION=$(echo "${NODEJS_VERSION}" | xargs)
OS_PROJECT=${OS_PROJECT:-debian-cloud}
OS_PROJECT=$(echo "${OS_PROJECT}" | xargs)
OS_VERSION=${OS_VERSION:-debian-12-bookworm-v20260114}
OS_VERSION=$(echo "${OS_VERSION}" | xargs)
PROJECT=${PROJECT:-$(gcloud config list --format 'value(core.project)'|head -n 1)}
REGION=${REGION:-europe-west1}
REPO_USERNAME=${REPO_USERNAME:-}
REPO_USERNAME=$(echo "${REPO_USERNAME}" | xargs)
REPO_PASSWORD=${REPO_PASSWORD:-}
REPO_PASSWORD=$(echo "${REPO_PASSWORD}" | xargs)
SERVICE_ACCOUNT=${SERVICE_ACCOUNT:-$(gcloud projects describe "${PROJECT}" --format='get(projectNumber)')-compute@developer.gserviceaccount.com}
SSH_PRIVATE_KEY_NAME=${SSH_PRIVATE_KEY_NAME:-jenkins-cuttlefish-vm-ssh-private-key}
SSH_PUBLIC_KEY_FILENAME=${SSH_PUBLIC_KEY_FILENAME:-jenkins_private_key.pub}
SUBNET=${SUBNET:-sdv-subnet}
UPDATE_SSH_AUTHORIZED_KEYS=${UPDATE_SSH_AUTHORIZED_KEYS:-false}
ZONE=${ZONE:-europe-west1-d}

IMAGE="projects/${OS_PROJECT}/global/images/${OS_VERSION}"

# Define architecture based on OS_VERSION as this will always include arch for arm.
if [[ "$OS_VERSION" == *arm64* ]]; then
    ARCHITECTURE="ARM64"
    VM_SUFFIX="-arm64"
else
    ARCHITECTURE="X86_64"
fi
# Machine type or custom type
declare machine_type_args=""
if [ -z "${MACHINE_TYPE}" ]; then
    if [[ -z "${CUSTOM_VM_TYPE}" || -z "${CUSTOM_CPU}"  || -z "${CUSTOM_VM_TYPE}" ]]; then
        echo -e "${RED}ERROR: MACHINE_TYPE or CUSTOM options must be defined.${NC}"
        exit 1
    else
        machine_type_args="--custom-vm-type=${CUSTOM_VM_TYPE} --custom-cpu=${CUSTOM_CPU} --custom-memory=${CUSTOM_MEMORY}"
    fi
else
    machine_type_args="--machine-type=${MACHINE_TYPE}"
fi
VM_SUFFIX=${VM_SUFFIX:-}

# Instance names can only include specific characters, drop '.' and replace paths in branch, '/' with '-'.
declare -r vm_base_instance=vm-"${OS_VERSION}"
declare -r vm_base_instance_template=instance-template-vm-"${OS_VERSION}"
declare cuttlefish_version=${CUTTLEFISH_REVISION//./}
cuttlefish_version=${cuttlefish_version//\//-}
declare cuttlefish_name=${CUTTLEFISH_INSTANCE_NAME//./-}
if [[ "${cuttlefish_name}" == "cuttlefish-vm" ]]; then
    # If name is default, append version.
    cuttlefish_name="${cuttlefish_name}"-"${cuttlefish_version}""${VM_SUFFIX}"
fi
declare -r vm_cuttlefish_image=image-"${cuttlefish_name}"
declare -r vm_cuttlefish_instance_template=instance-template-"${cuttlefish_name}"
declare -r vm_cuttlefish_instance="${cuttlefish_name}"
declare -r packer_template_path="${CF_SCRIPT_PATH}/packer/cuttlefish.pkr.hcl"
declare -r packer_provision_script_path="${CF_SCRIPT_PATH}/packer/provision_cf_host.sh"
declare -r startup_script_path="${CF_SCRIPT_PATH}/startup/refresh_authorized_keys.sh"

# This timeout was just a coverall for GCE issues that have since been resolved
# in Jenkins. But if creating a VM instance from the template, it is best not to set
# because the VM instance would have been deleted after the duration.
# 0 indicates not to limit run duration.
declare max_run_duration_args=""
if [ "${MAX_RUN_DURATION}" != '0' ]; then
    max_run_duration_args="--max-run-duration=${MAX_RUN_DURATION} --instance-termination-action=delete"
fi

# Increase the IAP TCP upload bandwidth
# shellcheck disable=SC2155
export PATH=$PATH:$(gcloud info --format="value(basic.python_location)")
$(gcloud info --format="value(basic.python_location)") -m pip install --upgrade pip --no-warn-script-location > /dev/null 2>&1 || true
$(gcloud info --format="value(basic.python_location)") -m pip install numpy --no-warn-script-location > /dev/null 2>&1 || true
export CLOUDSDK_PYTHON_SITEPACKAGES=1

# Catch Ctrl+C and terminate all
trap terminate SIGINT
function terminate() {
    echo -e "${RED}CTRL+C: exit requested!${NC}"
    exit 1
}

# Progress spinner. Wait for PID to complete.
function progress_spinner() {
    local -r spinner='-\|/'
    while sleep 0.1; do
        i=$(( (i+1) %4 ))
        # Only show spinner on local, save on console noise.
        if [ -z "${WORKSPACE}" ]; then
            # shellcheck disable=SC2059
            printf "\r${spinner:$i:1}"
        fi
        if ! ps -p "$1" > /dev/null; then
            break
        fi
    done
    printf "\r"
    wait "${1}"
    rc=$?
    if [ "${rc}" -ne 0 ]; then
        echo -e "${RED}Process $1 failed, exit.${NC}"
        # Ensure we cleanup leftovers
        delete_instances
        exit "${rc}"
    fi
}

# Echo formatted output.
function echo_formatted() {
    echo -e "\r${GREEN}[$SCRIPT_NAME] $1${NC}"
}

# Echo environment variables.
function echo_environment() {
    echo_formatted "Environment variables:"
    echo "ARCHITECTURE=${ARCHITECTURE}"
    echo "ADDITIONAL_NETWORKING=${ADDITIONAL_NETWORKING}"
    echo "BOOT_DISK_SIZE=${BOOT_DISK_SIZE}"
    echo "BOOT_DISK_TYPE=${BOOT_DISK_TYPE}"
    echo "CURL_UPDATE_COMMAND=${CURL_UPDATE_COMMAND}"
    echo "CUSTOM_VM_TYPE=${CUSTOM_VM_TYPE}"
    echo "CUSTOM_CPU=${CUSTOM_CPU}"
    echo "CUSTOM_MEMORY=${CUSTOM_MEMORY}"
    echo "CUTTLEFISH_INSTANCE_NAME=${cuttlefish_name}"
    echo "CUTTLEFISH_REVISION=${CUTTLEFISH_REVISION}"
    echo "CUTTLEFISH_POST_COMMAND=${CUTTLEFISH_POST_COMMAND}"
    echo "CUTTLEFISH_URL=${CUTTLEFISH_URL}"
    echo "DEFAULT_USER=${DEFAULT_USER}"
    echo "IMAGE=${IMAGE}"
    echo "JAVA_VERSION=${JAVA_VERSION}"
    echo "NAMESPACE=${NAMESPACE}"
    echo "MACHINE_TYPE=${MACHINE_TYPE}"
    echo "MAX_RUN_DURATION=${MAX_RUN_DURATION}"
    echo "NAMESPACE=${NAMESPACE}"
    echo "NETWORK=${NETWORK}"
    echo "NODEJS_VERSION=${NODEJS_VERSION}"
    echo "OS_PROJECT=${OS_PROJECT}"
    echo "OS_VERSION=${OS_VERSION}"
    echo "PROJECT=${PROJECT}"
    echo "REGION=${REGION}"
    echo "SERVICE_ACCOUNT=${SERVICE_ACCOUNT}"
    echo "SSH_PRIVATE_KEY_NAME=${SSH_PRIVATE_KEY_NAME}"
    echo "SSH_PUBLIC_KEY_FILENAME=${SSH_PUBLIC_KEY_FILENAME}"
    echo "SUBNET=${SUBNET}"
    echo "UPDATE_SSH_AUTHORIZED_KEYS=${UPDATE_SSH_AUTHORIZED_KEYS}"
    echo "VM_SUFFIX=${VM_SUFFIX}"
    echo "WORKSPACE=${WORKSPACE}"
    echo "ZONE=${ZONE}"
    echo
}

function print_usage() {
    echo "Usage:
      ARCHITECTURE=${ARCHITECTURE} \\
      ADDITIONAL_NETWORKING=${ADDITIONAL_NETWORKING} \\
      BOOT_DISK_SIZE=${BOOT_DISK_SIZE} \\
      BOOT_DISK_TYPE=${BOOT_DISK_TYPE} \\
      CURL_UPDATE_COMMAND=${CURL_UPDATE_COMMAND} \\
      CUSTOM_VM_TYPE=${CUSTOM_VM_TYPE} \\
      CUSTOM_CPU=${CUSTOM_CPU} \\
      CUSTOM_MEMORY=${CUSTOM_MEMORY} \\
      CUTTLEFISH_INSTANCE_NAME=${cuttlefish_name} \\
      CUTTLEFISH_REVISION=${CUTTLEFISH_REVISION} \\
      CUTTLEFISH_URL=${CUTTLEFISH_URL} \\
      CUTTLEFISH_POST_COMMAND=${CUTTLEFISH_POST_COMMAND} \\
      DEFAULT_USER=${DEFAULT_USER} \\
      IMAGE=${IMAGE} \\
      JAVA_VERSION=${JAVA_VERSION} \\
      SSH_PUBLIC_KEY_FILENAME=${SSH_PUBLIC_KEY_FILENAME} \\
      MACHINE_TYPE=${MACHINE_TYPE} \\
      MAX_RUN_DURATION=${MAX_RUN_DURATION} \\
      NAMESPACE=${NAMESPACE} \\
      NETWORK=${NETWORK} \\
      NODEJS_VERSION=${NODEJS_VERSION} \\
      OS_PROJECT=${OS_PROJECT} \\
      OS_VERSION=${OS_VERSION} \\
      PROJECT=${PROJECT} \\
      REGION=${REGION} \\
      SERVICE_ACCOUNT=${SERVICE_ACCOUNT} \\
      SSH_PRIVATE_KEY_NAME=${SSH_PRIVATE_KEY_NAME} \\
      SSH_PUBLIC_KEY_FILENAME=${SSH_PUBLIC_KEY_FILENAME} \\
      SUBNET=${SUBNET} \\
      UPDATE_SSH_AUTHORIZED_KEYS=${UPDATE_SSH_AUTHORIZED_KEYS} \\
      VM_SUFFIX=${VM_SUFFIX} \\
      WORKSPACE=${WORKSPACE} \\
      ZONE=${ZONE} \\
      ./${SCRIPT_NAME}"
    echo "Use defaults or override environment variables."
    echo
    echo "Primary commands:"
    echo "  ./${SCRIPT_NAME} 1        Build image with Packer and create template"
    echo "  ./${SCRIPT_NAME} 2        Refresh SSH authorized_keys metadata only"
    echo "  ./${SCRIPT_NAME} 3        Delete generated artifacts"
}

# Check environment.
function check_environment() {
    if [ -z "${PROJECT}" ]; then
        echo -e "${RED}Environment variable PROJECT must be defined${NC}"
        exit 1
    fi
    if [ -z "${SERVICE_ACCOUNT}" ]; then
        echo -e "${RED}Environment variable SERVICE_ACCOUNT must be defined${NC}"
        exit 1
    fi
    if [[ "${cuttlefish_name}" != cuttlefish-vm* ]]; then
        echo -e "${RED}CUTTLEFISH_INSTANCE_NAME must start with cuttlefish-vm${NC}"
        exit 1
    fi
}

# Extract the local public key from Kubernetes secret if needed.
function ensure_ssh_public_key_local() {
    if [ ! -f "${SSH_PUBLIC_KEY_FILENAME}" ]; then
        echo -e "${GREEN}Extracting public key ${SSH_PUBLIC_KEY_FILENAME}${NC}"
        # shellcheck disable=SC1083
        kubectl get secrets -n "${NAMESPACE}" "${SSH_PRIVATE_KEY_NAME}" \
            --template={{.data.privateKey}} | base64 -d > local_private_key
        chmod 600 local_private_key
        ssh-keygen -y -f local_private_key > "${SSH_PUBLIC_KEY_FILENAME}" || true
        rm -f local_private_key || true

        if [ ! -f "${SSH_PUBLIC_KEY_FILENAME}" ]; then
            echo -e "${RED}ERROR: Failed to extract public key from private key${NC}"
            return 1
        fi
    else
        echo -e "${GREEN}Using local public key ${SSH_PUBLIC_KEY_FILENAME}${NC}"
    fi
}

function get_packer_ssh_username() {
    local username="debian"
    if [[ "${OS_PROJECT}" == ubuntu* ]]; then
        username="ubuntu"
    fi
    echo "${username}"
}

function convert_memory_to_mb() {
    local value
    value=$(echo "$1" | awk '{print toupper($0)}' | xargs)
    if [[ "${value}" =~ ^([0-9]+)GB$ ]]; then
        echo "$((BASH_REMATCH[1] * 1024))"
        return 0
    fi
    if [[ "${value}" =~ ^([0-9]+)MB$ ]]; then
        echo "${BASH_REMATCH[1]}"
        return 0
    fi
    if [[ "${value}" =~ ^[0-9]+$ ]]; then
        echo "$((value * 1024))"
        return 0
    fi
    echo -e "${RED}Unsupported CUSTOM_MEMORY format: ${value}. Use MB or GB.${NC}"
    return 1
}

function get_packer_machine_type() {
    if [ -n "${MACHINE_TYPE}" ]; then
        echo "${MACHINE_TYPE}"
        return 0
    fi

    local memory_mb
    memory_mb=$(convert_memory_to_mb "${CUSTOM_MEMORY}") || return 1
    echo "${CUSTOM_VM_TYPE}-custom-${CUSTOM_CPU}-${memory_mb}"
}

function boot_disk_size_gb() {
    local value
    value=$(echo "${BOOT_DISK_SIZE}" | awk '{print toupper($0)}' | xargs)
    value=${value%GB}
    value=${value%G}
    echo "${value}"
}

function build_image_with_packer() {
    echo_formatted "1. Build Cuttlefish image with Packer"

    if ! command -v packer >/dev/null 2>&1; then
        echo -e "${RED}ERROR: packer binary not found in PATH.${NC}"
        exit 1
    fi
    if [ ! -f "${packer_template_path}" ] || [ ! -f "${packer_provision_script_path}" ]; then
        echo -e "${RED}ERROR: Packer files missing under ${CF_SCRIPT_PATH}/packer.${NC}"
        exit 1
    fi

    ensure_ssh_public_key_local || exit 1

    local ssh_public_key_b64
    ssh_public_key_b64=$(base64 < "${SSH_PUBLIC_KEY_FILENAME}" | tr -d '\n')
    local packer_machine_type
    packer_machine_type=$(get_packer_machine_type) || exit 1
    local packer_ssh_username
    packer_ssh_username=$(get_packer_ssh_username)
    local disk_size_gb
    disk_size_gb=$(boot_disk_size_gb)

    yes Y | gcloud compute images delete "${vm_cuttlefish_image}" >/dev/null 2>&1 || true

    packer init "${packer_template_path}"
    packer build \
        -var "project_id=${PROJECT}" \
        -var "zone=${ZONE}" \
        -var "region=${REGION}" \
        -var "network=${NETWORK}" \
        -var "subnetwork=${SUBNET}" \
        -var "source_image_project_id=${OS_PROJECT}" \
        -var "source_image=${OS_VERSION}" \
        -var "machine_type=${packer_machine_type}" \
        -var "disk_size_gb=${disk_size_gb}" \
        -var "disk_type=${BOOT_DISK_TYPE}" \
        -var "image_name=${vm_cuttlefish_image}" \
        -var "image_description=${vm_cuttlefish_image}" \
        -var "ssh_username=${packer_ssh_username}" \
        -var "default_user=${DEFAULT_USER}" \
        -var "cf_script_path=${CF_SCRIPT_PATH}" \
        -var "android_cuttlefish_revision=${CUTTLEFISH_REVISION}" \
        -var "cuttlefish_url=${CUTTLEFISH_URL}" \
        -var "cuttlefish_post_command=${CUTTLEFISH_POST_COMMAND}" \
        -var "repo_username=${REPO_USERNAME}" \
        -var "repo_password=${REPO_PASSWORD}" \
        -var "java_version=${JAVA_VERSION}" \
        -var "nodejs_version=${NODEJS_VERSION}" \
        -var "curl_update_command=${CURL_UPDATE_COMMAND}" \
        -var "os_version=${OS_VERSION}" \
        -var "cts_android_16_url=${CTS_ANDROID_16_URL}" \
        -var "cts_android_15_url=${CTS_ANDROID_15_URL}" \
        -var "cts_android_14_url=${CTS_ANDROID_14_URL}" \
        -var "ssh_public_key_b64=${ssh_public_key_b64}" \
        "${packer_template_path}"

    echo -e "${GREEN}Image ${vm_cuttlefish_image} built with Packer${NC}"
}

function publish_instance_template_from_image() {
    ensure_ssh_public_key_local || return 1
    if [ ! -f "${startup_script_path}" ]; then
        echo -e "${RED}ERROR: Startup script missing: ${startup_script_path}${NC}"
        return 1
    fi

    echo -e "${GREEN}Deleting ${vm_cuttlefish_instance_template}${NC}"
    yes Y | gcloud compute instance-templates delete \
        "${vm_cuttlefish_instance_template}" >/dev/null 2>&1 || true &
    progress_spinner "$!"

    echo -e "${GREEN}Creating ${vm_cuttlefish_instance_template}${NC}"
    # shellcheck disable=SC2086
    gcloud compute instance-templates create "${vm_cuttlefish_instance_template}" \
        --description="${vm_cuttlefish_instance_template}" \
        --shielded-integrity-monitoring \
        --key-revocation-action-type=none \
        --service-account="${SERVICE_ACCOUNT}" \
        ${machine_type_args} \
        --maintenance-policy=TERMINATE \
        --image-project="${OS_PROJECT}" \
        --create-disk=image="${vm_cuttlefish_image}",boot=yes,auto-delete=yes,type="${BOOT_DISK_TYPE}" \
        --metadata=enable-oslogin=true,jenkins-user="${DEFAULT_USER}" \
        --metadata-from-file=startup-script="${startup_script_path}",jenkins-authorized-key="${SSH_PUBLIC_KEY_FILENAME}" \
        --reservation-affinity=any \
        --enable-nested-virtualization \
        --region="${REGION}" \
        --network-interface network="${NETWORK}",subnet="${SUBNET}",stack-type=IPV4_ONLY,no-address"${ADDITIONAL_NETWORKING}" \
        ${max_run_duration_args} &
    progress_spinner "$!"

    template_exists=$(gcloud compute instance-templates list --filter="name=${vm_cuttlefish_instance_template}" --format='get(name)')
    if [ "${template_exists}" != "${vm_cuttlefish_instance_template}" ]; then
       echo -e "${RED}ERROR: Failed to create template: ${vm_cuttlefish_instance_template}, review logs.${NC}"
       return 1
    fi
    echo -e "${GREEN}Instance Template ${vm_cuttlefish_instance_template} created${NC}"

    echo -e "${GREEN}Deleting ${vm_cuttlefish_instance}${NC}"
    yes Y | gcloud compute instances delete "${vm_cuttlefish_instance}" \
        --zone="${ZONE}" >/dev/null 2>&1 || true &
    progress_spinner "$!"

    echo -e "${GREEN}Skipping optional VM creation from instance template.${NC}"
}

function create_instance_template_from_image() {
    echo_formatted "1. Create Cuttlefish instance template from baked image"
    publish_instance_template_from_image
}

function refresh_ssh_authorized_keys_on_existing_template() {
    echo_formatted "2. Refresh SSH key metadata on existing template"
    if ! publish_instance_template_from_image; then
        echo -e "${RED}ERROR: Failed to refresh SSH key metadata on template.${NC}"
        exit 1
    fi
    echo -e "${GREEN}Template SSH key metadata refreshed (no image rebuild).${NC}"
}

# Delete all VM instances and artifacts
function delete_instances() {
    echo_formatted "3. Delete VM instances and artifacts"

    yes Y | gcloud compute instance-templates delete "${vm_base_instance_template}" >/dev/null 2>&1 || true
    echo_formatted "   Deleted ${vm_base_instance_template}"

    yes Y | gcloud compute instance-templates delete "${vm_cuttlefish_instance_template}" >/dev/null 2>&1 || true
    echo_formatted "   Deleted ${vm_cuttlefish_instance_template}"

    yes Y | gcloud compute images delete "${vm_cuttlefish_image}" >/dev/null 2>&1 || true
    echo_formatted "   Deleted ${vm_cuttlefish_image}"

    gcloud compute instances stop "${vm_base_instance}" --zone="${ZONE}" >/dev/null 2>&1 || true
    echo_formatted "   Stopped ${vm_base_instance}"

    yes Y | gcloud compute instances delete "${vm_base_instance}" --zone="${ZONE}" >/dev/null 2>&1 || true
    echo_formatted "   Deleted ${vm_base_instance}"

    gcloud compute instances stop "${vm_cuttlefish_instance}" --zone="${ZONE}" >/dev/null 2>&1 || true
    echo_formatted "   Stopped ${vm_cuttlefish_instance}"

    yes Y | gcloud compute instances delete "${vm_cuttlefish_instance}" --zone="${ZONE}" >/dev/null 2>&1 || true
    echo_formatted "   Deleted ${vm_cuttlefish_instance}"
}

# Main: run all or allow the user to select which steps to run.
function main() {
    echo -e "${GREEN}HOST IP: ${NC} $(hostname -I || true)"
    echo_environment
    check_environment
    case "$1" in
        1)  build_image_with_packer
            if ! create_instance_template_from_image; then
                delete_instances
                exit 1
            fi
            ;;
        2)  refresh_ssh_authorized_keys_on_existing_template ;;
        3)  delete_instances ;;
        *h*)
            print_usage
            exit 0
            ;;
        *)  build_image_with_packer
            if ! create_instance_template_from_image; then
                delete_instances # Clean up on error
                exit 1
            fi
            echo_formatted "Done. Please check the output above and enjoy Cuttlefish!"
            ;;
    esac
}

main "$1"
