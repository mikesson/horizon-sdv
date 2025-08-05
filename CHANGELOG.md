# Changelog

## [Unreleased]

### Added

- Ability to use an existing VPC network and subnetwork for the Terraform deployment.
  - A new boolean variable `sdv_create_network` is introduced in `terraform/env/variables.tf`. Set it to `false` to use an existing network.
  - When `sdv_create_network` is `false`, you must provide the names of the existing network and subnetwork using the `sdv_existing_network_name` and `sdv_existing_subnetwork_name` variables respectively.

### Fixed

- The firewall rule `allow_tcp_22` now correctly attaches to the specified network, whether it's newly created or an existing one.

### Requirements for Existing Subnetworks

When using an existing subnetwork (`sdv_create_network = false`), it must meet the following requirements for the GKE cluster creation to succeed:

- **Secondary IP Ranges:** The subnetwork must have two secondary IP address ranges with the following exact names:
  - `pods-range` (for GKE pods)
  - `services-range` (for GKE services)
- **IP Address Range Sizing:**
  - The primary IP range of the subnetwork should be at least `/24` to accommodate the various VMs and GKE nodes.
  - The `pods-range` should be at least `/16` to provide enough IP addresses for the GKE pods.
  - The `services-range` should be at least `/16` to provide enough IP addresses for the GKE services.
