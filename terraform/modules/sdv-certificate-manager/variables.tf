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

variable "name" {
  default = "Define the name of the certificate"
  type    = string
}

variable "domains" {
  type        = map(string)
  description = "Map of env_name => domain_name"
}

variable "certificate_authorization_type" {
  description = "Certificate authorization: 'dns' (CNAME in zone) or 'load_balancer' (no CNAME; A records must point to LB)."
  type        = string
  default     = "dns"

  validation {
    condition     = contains(["dns", "load_balancer"], var.certificate_authorization_type)
    error_message = "certificate_authorization_type must be 'dns' or 'load_balancer'."
  }
}
