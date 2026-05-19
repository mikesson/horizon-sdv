# AAOS Builder Image Helm Chart

This chart installs the AAOS builder image WorkflowTemplate and references the
shared ClusterWorkflowTemplate (`common-docker-image-build`), Run with `argo submit --from workflowtemplate/aaos-builder-runtime-image -n <namespace>` or via Argo CD.

**Pipeline repo (non-local):** Git clone options are **`spec.pipelineRepoUrl`**, **`spec.pipelineRepoRevision`**, and **`scm.authMethod`** / **`spec.pipelineRepoSecret`**. **userpass** and **app** (remote) both use **ClusterWorkflowTemplate** **`prepare-pipeline-git-creds`**, which either copies the static pipeline Secret into **`{{workflow.uid}}-pipeline-git-creds`** (**userpass**) or delegates to **`prepare-github-app-git-creds`** (**app**). Platform GitOps deploys this chart as a **`spec.sources`** entry on **`workloads-android`** (`gitops/modules/workloads-android` via Module Manager). **ClusterWorkflowTemplate** **`common-docker-image-build`** comes from Module Manager module **`workloads-common`** when enabled. **`ai-review`** is a namespaced WorkflowTemplate on **`workloads-android`** (gemini chart).

**GCP project / region / zone:** With **`cloudEnvConfigMapName`** set (default **`horizon-workflow-cloud-env`**, same as aaos-builder’s platform env), **`cloudProject`**, **`cloudRegion`**, and **`cloudZone`** are **WorkflowTemplate** parameters resolved from **`valueFrom.configMapKeyRef`** on keys **`CLOUD_PROJECT`**, **`CLOUD_REGION`**, **`CLOUD_ZONE`** (from **`gitops/templates/argo-workflows-init.yaml`**). Set **`cloudEnvConfigMapName: ""`** for local clusters without that ConfigMap and use **`spec.cloudProject`** / **`spec.cloudRegion`** / **`spec.cloudZone`** instead.

## What’s in this chart

- `Chart.yaml`: Helm chart metadata.
- `values.yaml`: Default values; update this for configuration.
- `templates/workflow/workflowtemplates.yaml`: Argo WorkflowTemplate wrapper for image builds.
- `templates/workflow/_templates.tpl`: Aggregates all split workflow templates.
- `templates/workflow/_build.tpl`: Build DAG template.
- `README.md`: This document.

## Why split templates

The workflow templates are split into per-step files to keep each task easy to find,
review, and maintain. The `templates/workflow/_templates.tpl` file aggregates them
in a stable order.

## Prerequisites

- Argo Workflows installed in the cluster
- `kubectl` and `helm` available locally (or Argo CD to sync this chart)
- ClusterWorkflowTemplate applied (from `workloads/common/common_docker_image`)

## Namespace (main vs sub-environment)

By default the WorkflowTemplate is rendered into namespace `workflows` (`namespacePrefix: ""`). For GitOps sub-environments that use `namespacePrefix` (e.g. `dev-`), set `namespacePrefix: "dev-"` so the template is applied to `dev-workflows`. To use an arbitrary name, set `namespace` (it overrides the prefix rule).

## Deploy

```bash
helm template aaos-builder-runtime-image \
  workloads/android/pipelines/environment/docker_image_template/helm \
  | kubectl apply -f -
```

## Update after changes

Re-apply the chart:

```bash
helm template aaos-builder-runtime-image \
  workloads/android/pipelines/environment/docker_image_template/helm \
  | kubectl apply -f -
```

If a WorkflowTemplate name changed, re-submit workflows afterward.

## Run the workflow

```bash
argo submit --from workflowtemplate/aaos-builder-runtime-image -n workflows   # or -n dev-workflows if namespacePrefix=dev-
```

## Local repo configuration

Local repo mounts are configured on the shared common-docker-image-build chart.
Follow `workloads/common/common_docker_image/helm/README.md` for PVC-backed
repo setup and local mount behavior.

## Common Options (values.yaml)

- `namespacePrefix` / `namespace`: Target namespace (`namespacePrefix` + `workflows`, or explicit `namespace`)
- `workflowTemplateName`: WorkflowTemplate name
- `clusterWorkflowTemplateName`: Shared ClusterWorkflowTemplate name
- `spec.cloudProject`: GCP project
- `spec.cloudRegion`: GCP region
- `spec.dockerArtifactPathName`: Artifact registry path
- `spec.dryRun`: `true` to skip push (maps to `NO_PUSH`)
- `spec.imageTag`: Image tag pushed to Artifact Registry (default `argowf-latest` to avoid clobbering a shared `:latest`)
- `spec.pipelineRepoUrl`: Pipeline repo URL
- `spec.pipelineRepoRevision`: Pipeline repo revision
- `spec.pipelineRepoSecret`: Optional git credentials secret
- `spec.useElevatedWorkflowIam`: `true` → `workflow-executor-elevated` (Terraform `argo_workflows_elevated`); `false` → use `spec.serviceAccountName` (default `workflow-executor`, `argo_workflows`)
- `spec.serviceAccountName`: Service account when `useElevatedWorkflowIam` is false
- `podGcStrategy`: `OnPodCompletion` or `OnWorkflowCompletion`

## Same cluster as GitOps: pause sync, local `helm apply`, resync

If Argo CD manages this WorkflowTemplate (via **`workloads-android`**), a manual `helm template | kubectl apply` (for example with **`-f values-local.yaml`**) can be **reverted** when Argo syncs from Git. To test local manifests without losing them immediately:

1. **Find the Application** (with GitOps **`namespacePrefix`**, e.g. **`dev-workloads-android`** in **`argocd`**):

   ```bash
   kubectl get applications -A | grep workloads-android
   ```

2. **Pause automated sync** (replace `APP_NAME` and `ARGOCD_NS` with values from step 1):

   ```bash
   kubectl patch application APP_NAME \
     -n ARGOCD_NS \
     --type=json \
     -p='[{"op":"remove","path":"/spec/syncPolicy/automated"}]'
   ```

3. **Apply** your chart (example with local overrides):

   ```bash
   helm template aaos-builder-runtime-image \
     workloads/android/pipelines/environment/docker_image_template/helm \
     -f workloads/android/pipelines/environment/docker_image_template/helm/values-local.yaml \
     | kubectl apply -n workflows -f -
   ```

4. **Resync from Git** when you want the cluster to match the repo again (one-off):

   ```bash
   argocd app sync APP_NAME
   ```

   Or use the Argo CD UI **Sync** button for that Application.

5. **Re-enable continuous sync** (optional):

   ```bash
   kubectl patch application APP_NAME \
     -n ARGOCD_NS \
     --type=merge \
     -p '{"spec":{"syncPolicy":{"automated":{}}}}'
   ```

While automated sync is off, avoid running a **Sync** on **`workloads-android`** if you still want to keep hand-applied YAML; a sync reapplies Git and overwrites **all** sources for that Application.

## Test / Verify

```bash
kubectl get workflowtemplate -n <namespace> aaos-builder-runtime-image
kubectl get cwf -n <namespace>
```

## Artifacts (optional)

To enable Argo artifact downloads (UI/CLI), configure an artifact repository
in the Argo controller config (e.g., GCS/S3/MinIO). Example (GCS):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-workflows-config
  namespace: argo
data:
  artifactRepository: |
    gcs:
      bucket: <bucket-name>
      keyFormat: "{{workflow.name}}/{{pod.name}}/{{artifact.name}}"
```

