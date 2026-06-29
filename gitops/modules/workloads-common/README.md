<!-- Copyright (c) 2026 Accenture, All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

        http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# workloads-common (Module Manager)

Deploys shared **ClusterWorkflowTemplates** and colocated **Argo Events Sensors** via Argo CD (`application-workloads-common.yaml` multi-source):

- `workloads/common/common_docker_image/helm` → **common-docker-image-build** + Sensor **webhook-common-docker-image-build**
- `prepare-github-app-git-creds/` (Helm chart) → **prepare-pipeline-git-creds** (umbrella) + **prepare-github-app-git-creds** + Sensor **webhook-prepare-github-app-git-creds** + ConfigMap **{namespacePrefix}workflow-github-app-token-script** when `scm.authMethod` is **app** (GitHub App pipeline git; ExternalSecret **workflow-github-app** stays in platform `argo-workflows-init`)

Enable this module **before** **workloads-android** (hard dependency). Sync wave **2** in the child Application; **workloads-android** is wave **3**.

Migration from the removed root-chart Application `workflows-common-cluster-templates`: enable **`workloads-common`** in Module Manager so the new child Application applies before or as you rely on those CWTs.
