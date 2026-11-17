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

output "service_account_email" {
  description = "The relay service account. Add it to iap_members so the agent can reach the orchestrator."
  value       = google_service_account.relay.email
}

output "external_addresses" {
  description = "External address per relay instance."
  value       = { for name, addr in google_compute_address.relay : name => addr.address }
}

output "internal_addresses" {
  description = "Internal address per relay instance, for the device leg of an asymmetric ICE configuration."
  value       = { for name, addr in google_compute_address.relay_internal : name => addr.address }
}

output "turn_servers" {
  description = <<-EOT
    TURN URIs for the client leg, in the form the orchestrator's
    CCO_TURN_SERVERS expects.

    TURNS comes first where a name is configured: on the networks relay mode
    exists to serve, 443 is often the only port that leaves at all, and ICE
    should try it before the ports that will not.
  EOT
  value = concat(
    local.turns_enabled ? ["turns:${var.turns_domain}:443"] : [],
    [for name, addr in google_compute_address.relay : "turn:${addr.address}:3478"],
  )
}

output "device_turn_servers" {
  description = <<-EOT
    TURN URIs for the device leg, on internal addresses and plain turn:.

    The device is a native WebRTC client, which validates TLS against a root
    list compiled into the binary rather than the system trust store, so giving
    it a turns: URI risks a silent failure that depends on how old the host
    image is. It sits inside the VPC and needs none.

    Read from the reserved address rather than the instance: the orchestrator
    consumes this, and an instance carries the orchestrator's own URL, so the
    instance would close a dependency cycle.
  EOT
  value       = [for name, addr in google_compute_address.relay_internal : "turn:${addr.address}:3478"]
}
