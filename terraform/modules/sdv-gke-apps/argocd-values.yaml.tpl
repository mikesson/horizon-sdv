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

crds:
  # Allow Helm to delete CRDs during uninstall (prevents warning message)
  keep: false

configs:
  params:
    server.insecure: true
    server.rootpath: /argocd
    # GitHub HTTPS from the cluster can be slow or stall; defaults (15s git, 60s
    # server/controller → repo-server) surface as UI "Connection Failed" and
    # `context deadline exceeded` in repo-server while apps may still use cache.
    reposerver.git.request.timeout: "120s"
    server.repo.server.timeout.seconds: "180"
    controller.repo.server.timeout.seconds: "180"
  secret:
    createSecret: false
  cm:
    # Must match server.rootpath (/argocd) and the public Gateway prefix or Keycloak rejects
    # redirect_uri (OAuth callback must be under this URL).
    url: https://${subdomain_name}.${domain_name}/argocd
    resource.customizations: |
      Secret:
        ignoreDifferences: |
          jsonPointers:
          - /metadata/annotations/argocd.argoproj.io~1tracking-id
      external-secrets.io/ExternalSecret:
        ignoreDifferences: |
          jsonPointers:
          - /status
global:
  domain: ${subdomain_name}.${domain_name}