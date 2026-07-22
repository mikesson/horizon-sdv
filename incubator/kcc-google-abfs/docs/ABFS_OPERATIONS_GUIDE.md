# ABFS Standalone Day 2 Operations Guide

This guide covers key operational tasks for managing and maintaining a standalone Android Build File System (ABFS) deployment on GKE, focusing on configuring and reconfiguring git-pushers to synchronize and seed new repositories or branches.

---

## 1. Reconfiguring Git-Pushers to Sync New Branches or Android Versions

The git-pushers (uploaders) fetch Git metadata and repository packs based on a central configuration. This configuration is declared in the Helm values and seeded into the ABFS metadata repository (`abfs-meta`) via a post-upgrade bootstrap Job.

Follow these steps to synchronize an additional Android release branch or version.

### Step 1.1: Modify values-sandbox.yaml

Add the desired branch and manifest file definition to the `pusher.branchFiles` list in your Helm values file.

Example: To add the `android-14.0.0_r2` branch alongside the existing branches:

```yaml
pusher:
  manifestProjectUrl: https://android.googlesource.com/platform/manifest
  branchFiles:
    - branch: main
      file: default.xml
    - branch: android-14.0.0_r1
      file: default.xml
    - branch: android-14.0.0_r2
      file: default.xml
```

### Step 1.2: Apply the Configuration via Helm

Run a Helm upgrade to update the pusher ConfigMap and trigger the post-upgrade bootstrap Job:

```bash
helm upgrade abfs ./incubator/kcc-google-abfs/rendered/standalone/chart/abfs \
  -f ./incubator/kcc-google-abfs/values-sandbox.yaml \
  -n abfs
```

### Step 1.3: Verify Bootstrap Job Execution

During the upgrade, Helm launches a new `abfs-pusher-config` Job pod. Verify that this job completes successfully:

```bash
kubectl get pods -n abfs -l job-name=abfs-pusher-config
```

Inspect the logs of the bootstrap container to ensure that the new config has been pushed and the server ref updated:

```bash
kubectl logs -n abfs job/abfs-pusher-config -c pusher-config
```

The logs should conclude with a successful `put-ref` output.

### Step 1.4: Monitor Uploader Logs

The uploader pods (`abfs-gerrit-uploader-<idx>`) poll the central configuration from the ABFS server. They should automatically pick up the new branch and begin fetching the new manifest:

```bash
kubectl logs -n abfs abfs-gerrit-uploader-0 --tail=100
```

To force an immediate reload or ensure clean state, you can perform a rolling restart of the uploader StatefulSet:

```bash
kubectl rollout restart statefulset/abfs-gerrit-uploader -n abfs
kubectl rollout status statefulset/abfs-gerrit-uploader -n abfs
```

---

## 2. Adding a Custom/External Manifest Repository

To sync a completely different set of repositories (such as a custom platform integration, hardware vendor drivers, or a private Git repository), you can point the git-pusher to a custom manifest.

### Step 2.1: Update values-sandbox.yaml with Custom Manifest

Point `manifestProjectUrl` to your custom manifest Git repository and specify the branch/file:

```yaml
pusher:
  manifestProjectUrl: https://github.com/your-org/custom-manifest.git
  branchFiles:
    - branch: main
      file: default.xml
```

> [!NOTE]
> If the custom manifest repository or target repositories are private, ensure the uploader service account has the necessary IAM/SSH keys configured, or use an unauthenticated public HTTPS mirror.

### Step 2.2: Apply and Verify

Apply using Helm as described in Section 1.2, and monitor the uploader logs to ensure the new custom manifest projects are being discovered and synced.

---

## 3. Troubleshooting Configuration Sync Issues

### 3.1. Dagsync Wait Timeout (`timeout waiting for dagsync to be idle`)

During bootstrap config execution, if the previous server configuration reference is corrupted or contains empty config definitions (e.g. 0-byte `clients/configs`), the local cacheman background process will fail to decode it:

```
failed to decode *instance.ConfigRemoteOverrides config ...: EOF
timeout waiting for dagsync to be idle
```

Because of this decode failure, the dagsync loop is never marked idle, causing `abfs cacheman wait` to block and eventually timeout.

**Remediation:**
1. Ensure the bootstrap Job initializes `clients/configs` with a valid empty JSON object `{}` instead of an empty file:
   ```bash
   echo "{}" > clients/configs
   ```
2. The bootstrap Job script is patched with `abfs cacheman wait || true` to make local waiting non-fatal. This guarantees that even if a historical broken ref causes cacheman to log a warning, the job successfully executes `put-ref` and overwrites the corrupted server ref with the new correct tree. Subsequent runs or uploader reloads will then decode successfully.

