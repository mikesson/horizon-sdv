# Copyright (c) 2024-2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#         http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

data "google_project" "project" {}

resource "google_container_cluster" "sdv_cluster" {
  project                  = data.google_project.project.project_id
  name                     = var.cluster_name
  location                 = var.location
  network                  = var.network
  subnetwork               = var.subnetwork
  remove_default_node_pool = true
  initial_node_count       = 1
  fleet {
    project = var.project_id
  }

  # Set `deletion_protection` to `true` will ensure that one cannot
  # accidentally delete this instance by use of Terraform.
  deletion_protection = false

  workload_identity_config {
    workload_pool = "${var.project_id}.svc.id.goog"
  }

  master_authorized_networks_config {
    gcp_public_cidrs_access_enabled = false
  }

  ip_allocation_policy {
    stack_type                    = "IPV4"
    cluster_secondary_range_name  = "pods-range"
    services_secondary_range_name = "services-range"
  }

  private_cluster_config {
    enable_private_nodes    = true
    enable_private_endpoint = true
    master_ipv4_cidr_block  = "10.0.0.0/28"
  }

  secret_manager_config {
    enabled = true
  }

  # Explicitly opt out of any release channel so Terraform can pin min_master_version
  # and node pools can set auto_upgrade = false for CASFS kernel module compatibility.
  release_channel {
    channel = "UNSPECIFIED"
  }

  min_master_version = var.cluster_version

  # The maintenance policy to use for the cluster - when updates can occur
  maintenance_policy {
    recurring_window {
      start_time = "2025-01-01T00:00:00Z"
      end_time   = "2050-01-01T00:00:00Z"
      recurrence = "FREQ=WEEKLY;BYDAY=SA,SU"
    }
  }

  # enable gateway api
  gateway_api_config {
    channel = "CHANNEL_STANDARD"
  }

  addons_config {
    network_policy_config {
      disabled = false
    }
    http_load_balancing {
      disabled = false
    }
    gcp_filestore_csi_driver_config {
      enabled = true
    }
    config_connector_config {
      enabled = true
    }
  }

  # Enable network policy enforcement for pod-to-pod traffic restriction
  # Required for GCP 327 compliance: Kubernetes pod-to-pod traffic must be restricted
  # Fix for vulnerability #6 - Using Calico provider (legacy datapath)
  network_policy {
    enabled  = true
    provider = "CALICO"
  }

  # Enable autoscaling
  cluster_autoscaling {
    enabled             = false
    autoscaling_profile = "OPTIMIZE_UTILIZATION"
  }

  # monitoring configuration
  monitoring_config {
    enable_components = ["SYSTEM_COMPONENTS", "APISERVER", "SCHEDULER", "CONTROLLER_MANAGER", "CADVISOR", "KUBELET"]
    # DISABLED monitoring for Kube state metrics : STORAGE, POD, DEPLOYMENT, STATEFULSET, DAEMONSET, JOBSET

    # Control Plane Metrics enabled
    managed_prometheus {
      enabled = true
    }
  }

  # Enable intranode visibility for better network monitoring and security
  # Allows monitoring of pod-to-pod traffic within nodes for security analysis
  enable_intranode_visibility = true

  # Enable application-layer secrets encryption with Cloud KMS (Fix for vulnerability #8)
  # Encrypts Kubernetes secrets at rest using customer-managed encryption keys
  # NOTE: Once enabled, GKE database encryption cannot be disabled without recreating the cluster
  # When enable_kms_encryption = false, this explicitly sets state to DECRYPTED
  database_encryption {
    state    = var.enable_kms_encryption ? "ENCRYPTED" : "DECRYPTED"
    key_name = var.enable_kms_encryption ? var.kms_crypto_key_id : ""
  }
}

# Automatically enable flow logs on the GKE-auto-created master subnet
# GKE creates this subnet automatically for the private cluster control plane
# This null_resource runs after cluster creation to enable flow logs
resource "null_resource" "enable_gke_master_subnet_flow_logs" {
  # Trigger on cluster recreation
  triggers = {
    cluster_id = google_container_cluster.sdv_cluster.id
  }

  provisioner "local-exec" {
    command = <<-EOT
      set -e
      
      # Find the GKE master subnet
      MASTER_SUBNET=$(gcloud compute networks subnets list \
        --project=${var.project_id} \
        --network=${var.network} \
        --filter="name~'gke-${var.cluster_name}-.*-pe-subnet' AND region:${var.location}" \
        --format="value(name)" \
        --limit=1)
      
      if [ -z "$MASTER_SUBNET" ]; then
        echo "ERROR: GKE master subnet not found. This may indicate the cluster is not a private cluster."
        exit 1
      fi
      
      echo "Found GKE master subnet: $MASTER_SUBNET"
      
      # Check if flow logs are already enabled
      FLOW_LOGS_ENABLED=$(gcloud compute networks subnets describe "$MASTER_SUBNET" \
        --project="${var.project_id}" \
        --region="${var.location}" \
        --format="value(enableFlowLogs)" 2>/dev/null || echo "False")
      
      if [ "$FLOW_LOGS_ENABLED" = "True" ]; then
        echo "Flow logs are already enabled on $MASTER_SUBNET"
        exit 0
      fi
      
      echo "Enabling flow logs on GKE master subnet: $MASTER_SUBNET"
      
      gcloud compute networks subnets update "$MASTER_SUBNET" \
        --project="${var.project_id}" \
        --region="${var.location}" \
        --enable-flow-logs \
        --logging-aggregation-interval=interval-5-min \
        --logging-flow-sampling=0.5 \
        --logging-metadata=include-all
      
      echo "✓ Flow logs successfully enabled on GKE master subnet"
    EOT
  }

  depends_on = [
    google_container_cluster.sdv_cluster
  ]
}


resource "google_container_node_pool" "sdv_main_node_pool" {
  name           = var.node_pool_name
  location       = var.location
  cluster        = google_container_cluster.sdv_cluster.name
  node_count     = var.node_count
  node_locations = var.node_locations
  node_config {
    preemptible  = false
    machine_type = var.machine_type

    # Google recommends custom service accounts that have cloud-platform
    # scope and permissions granted via IAM Roles.
    service_account = var.service_account
    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform"
    ]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  autoscaling {
    min_node_count = var.node_pool_min_node_count
    max_node_count = var.node_pool_max_node_count
  }

}

resource "google_container_node_pool" "sdv_build_node_pool" {
  name           = var.build_node_pool_name
  location       = var.location
  cluster        = google_container_cluster.sdv_cluster.name
  node_count     = var.build_node_pool_node_count
  node_locations = var.node_locations
  node_config {
    preemptible  = false
    machine_type = var.build_node_pool_machine_type
    disk_size_gb = 500
    image_type   = "UBUNTU_CONTAINERD"

    # Google recommends custom service accounts that have cloud-platform
    # scope and permissions granted via IAM Roles.
    service_account = var.service_account
    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform"
    ]

    labels = {
      workloadLabel = "android"
    }

    taint {
      key    = "workloadType"
      value  = "android"
      effect = "NO_SCHEDULE"
    }

    metadata = {
      disable-legacy-endpoints = "true"
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  autoscaling {
    min_node_count = var.build_node_pool_min_node_count
    max_node_count = var.build_node_pool_max_node_count
  }

}

resource "google_container_node_pool" "sdv_abfs_build_node_pool" {
  name           = var.abfs_build_node_pool_name
  location       = var.location
  cluster        = google_container_cluster.sdv_cluster.name
  version        = var.abfs_build_node_pool_version
  node_count     = var.abfs_build_node_pool_node_count
  node_locations = var.node_locations
  node_config {
    preemptible  = false
    machine_type = var.abfs_build_node_pool_machine_type
    disk_size_gb = 500
    image_type   = "UBUNTU_CONTAINERD"

    # Google recommends custom service accounts that have cloud-platform
    # scope and permissions granted via IAM Roles.
    service_account = var.service_account
    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform"
    ]

    labels = {
      workloadLabel = "android-abfs"
    }

    taint {
      key    = "workloadType"
      value  = "android-abfs"
      effect = "NO_SCHEDULE"
    }

    metadata = {
      disable-legacy-endpoints = "true"
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  autoscaling {
    min_node_count = var.abfs_build_node_pool_min_node_count
    max_node_count = var.abfs_build_node_pool_max_node_count
  }

  # Block auto-upgrade so pool stays on pinned version (CASFS kernel module compatibility).
  management {
    auto_repair  = true
    auto_upgrade = false
  }
}

resource "google_container_node_pool" "sdv_openbsw_build_node_pool" {
  name           = var.openbsw_build_node_pool_name
  location       = var.location
  cluster        = google_container_cluster.sdv_cluster.name
  node_count     = var.openbsw_build_node_pool_node_count
  node_locations = var.node_locations
  node_config {
    preemptible  = false
    machine_type = var.openbsw_build_node_pool_machine_type
    disk_size_gb = 500
    image_type   = "UBUNTU_CONTAINERD"

    # Google recommends custom service accounts that have cloud-platform
    # scope and permissions granted via IAM Roles.
    service_account = var.service_account
    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform"
    ]

    labels = {
      workloadLabel = "openbsw"
    }

    taint {
      key    = "workloadType"
      value  = "openbsw"
      effect = "NO_SCHEDULE"
    }

    metadata = {
      disable-legacy-endpoints = "true"
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  autoscaling {
    min_node_count = var.openbsw_build_node_pool_min_node_count
    max_node_count = var.openbsw_build_node_pool_max_node_count
  }

}

resource "google_container_node_pool" "sdv_utility_node_pool" {
  name           = var.utility_node_pool_name
  location       = var.location
  cluster        = google_container_cluster.sdv_cluster.name
  node_count     = var.utility_node_pool_node_count
  node_locations = var.node_locations
  node_config {
    preemptible  = false
    machine_type = var.utility_node_pool_machine_type
    disk_size_gb = 500
    image_type   = "UBUNTU_CONTAINERD"

    service_account = var.service_account
    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform"
    ]

    labels = {
      workloadLabel = "utility"
    }

    taint {
      key    = "workloadType"
      value  = "utility"
      effect = "NO_SCHEDULE"
    }

    metadata = {
      disable-legacy-endpoints = "true"
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  autoscaling {
    min_node_count = var.utility_node_pool_min_node_count
    max_node_count = var.utility_node_pool_max_node_count
  }

}

