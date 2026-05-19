# workloads-common (Module Manager)

Deploys shared **ClusterWorkflowTemplates** and colocated **Argo Events Sensors** via Argo CD (`application-workloads-common.yaml` multi-source):

- `workloads/common/common_docker_image/helm` → **common-docker-image-build** + Sensor **webhook-common-docker-image-build**
- `prepare-github-app-git-creds/` (Helm chart) → **prepare-pipeline-git-creds** (umbrella) + **prepare-github-app-git-creds** + Sensor **webhook-prepare-github-app-git-creds** + ConfigMap **{namespacePrefix}workflow-github-app-token-script** when `scm.authMethod` is **app** (GitHub App pipeline git; ExternalSecret **workflow-github-app** stays in platform `argo-workflows-init`)

Enable this module **before** **workloads-android** (hard dependency). Sync wave **2** in the child Application; **workloads-android** is wave **3**.

Migration from the removed root-chart Application `workflows-common-cluster-templates`: enable **`workloads-common`** in Module Manager so the new child Application applies before or as you rely on those CWTs.
