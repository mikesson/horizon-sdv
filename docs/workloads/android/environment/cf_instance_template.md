# Cuttlefish Instance Template Pipeline

## Table of contents
- [Introduction](#introduction)
- [Packer and Startup Files](#packer-and-startup-files)
- [Prerequisites](#prerequisites)
- [Environment Variables/Parameters](#environment-variables)
- [Private repo and branch (e.g. horizon/main) — artifacts and Jenkins GCE](#private-repo-and-branch-eg-horizonmain--artifacts-and-jenkins-gce)
- [Example Usage](#examples)
- [System Variables](#system-variables)

## Introduction <a name="introduction"></a>

This pipeline creates (or deletes) ARM64 and x86_64 Cuttlefish instance templates which are used by the Jenkins test pipelines to spin up cloud instances which are cuttlefish-ready and CTS-ready; these cloud instances are then used to launch CVD and run CTS tests.

Users may select from standard machine types or create custom machine types.  If the `MACHINE_TYPE` parameter is set to an empty string, the custom parameter values will be used to create the machine type, i.e.:

- `CUSTOM_VM_TYPE`
- `CUSTOM_CPUS`
- `CUSTOM_MEMORY`

During the process of creating an instance template, this pipeline also creates a custom image which is referenced by the created instance template. The image is baked with Packer and then used by `gcloud` to create the final instance template. This image is created using the same naming convention as the instance template.

For example:

- <b>Name (provided or auto-generated)</b>: cuttlefish-vm-main
- <b>Image Name</b>: image-cuttlefish-vm-main
- <b>Instance Template Name</b>: instance-template-cuttlefish-vm-main

The following gcloud commands can be used to view images and instance templates:

- gcloud compute instance-templates list | grep cuttlefish-vm
- gcloud compute instances list | grep cuttlefish-vm

<b>Important:</b> This pipeline may not be run concurrently - this is to avoid clashes with temporary artifacts the job creates in order to produce the Cuttlefish instance template.

### Pipeline execution stages

The script command interface uses three primary stages:

- `1`: Build image with Packer and create/update the instance template.
- `2`: Refresh SSH key metadata on a **new** instance template revision (no Packer image rebuild; template resource is recreated with updated `jenkins-authorized-key` / related metadata).
- `3`: Delete generated instance/template/image artifacts.

### References <a name="references"></a>

- [Cuttlefish Virtual Devices](https://source.android.com/docs/devices/cuttlefish) for use with [CTS](https://source.android.com/docs/compatibility/cts) and emulators.
- [Virtual Device for Android host-side utilities](https://github.com/google/android-cuttlefish)
- [Compatibility Test Suite downloads](https://source.android.com/docs/compatibility/cts/downloads)
- [Compute Instance Templates](https://cloud.google.com/sdk/gcloud/reference/compute/instance-templates/create)

## Packer and Startup Files <a name="packer-and-startup-files"></a>

The Cuttlefish image/template flow uses the following files together:

- `workloads/android/pipelines/environment/cf_instance_template/packer/cuttlefish.pkr.hcl`
  - Main Packer template.
  - Defines the temporary build VM source image/machine/network.
  - Copies provisioning scripts and runs provisioning steps.
  - Produces the final GCE disk image used by the CF instance template.

- `workloads/android/pipelines/environment/cf_instance_template/packer/provision_cf_host.sh`
  - Script executed by Packer on the temporary build VM.
  - Calls `cf_host_initialise.sh` to install/configure Cuttlefish host tooling.
  - Ensures default user SSH bootstrap content is present during image bake.

- `workloads/android/pipelines/environment/cf_instance_template/cf_host_initialise.sh`
  - Performs host-side setup used by the Packer provisioning phase.
  - Installs dependencies, builds/install Cuttlefish packages, and prepares host runtime.

- `workloads/android/pipelines/environment/cf_instance_template/startup/refresh_authorized_keys.sh`
  - Runtime startup script attached via instance template metadata.
  - Runs when a VM boots from the template and rewrites **`authorized_keys` for the VM login user** (see below).
  - Reads the public key from instance metadata attribute `jenkins-authorized-key` on every boot.
  - Reads the target account from instance metadata attribute **`jenkins-user`** (historical name). If `jenkins-user` is empty, the script defaults to `jenkins`.
  - The file updated is always **`/home/<jenkins-user value>/.ssh/authorized_keys`** — **not** necessarily `/home/jenkins/...` if the template was built with a different `DEFAULT_USER`.

**VM SSH user (Cuttlefish GCE agent) vs Docker image user:** Jenkins CasC (`values-jenkins.yaml` / `jenkins-init.yaml`) configures the GCE plugin to SSH as **`jenkins`** with `remoteFs` `/home/jenkins`. The Packer/template pipeline therefore defaults **`DEFAULT_USER` to `jenkins`** in `cf_create_instance_template.sh` so the baked image, instance metadata (`jenkins-user`), and Jenkins agree. That is **independent** of the non-root user in the AAOS **Docker** image (`docker_image_template/Dockerfile`, typically `builder`), which only applies inside Kubernetes build pods.

In short: **Packer files create the immutable image**, while the **startup script updates SSH key material at boot** so key rotation does not require rebuilding the image.

When the Jenkins SSH key rotates, use `UPDATE_SSH_AUTHORIZED_KEYS=true` to republish the instance template with updated metadata (including `jenkins-authorized-key`). That path **does not** run a new Packer bake, but it **recreates** the instance template resource (same as a full template publish step), not an in-place metadata patch on an existing template API object.

## Prerequisites<a name="prerequisites"></a>

One-time setup requirements.

- Before running this pipeline job, ensure that the following template has been created by running the corresponding job:
  - Docker image template: ``Android Workflows/Environment/Docker Image Template`
- The Google Compute Engine is configured with `noDelayProvisioning: false` in `gitops/workloads/values-jenkins.yaml` to help reduce costs. With this setting, multiple VM instances are not started immediately, which lowers expenses for each run. However, disabling immediate provisioning may slightly increase VM startup times. This trade-off allows users to choose between faster VM availability and lower operational costs.

## Environment Variables/Parameters <a name="environment-variables"></a>

**Jenkins Parameters:** Defined in the groovy job definition `groovy/job.groovy`.

### `ANDROID_CUTTLEFISH_REPO_URL`

This defines which repository will be used to create the cuttlefish instance from. Users may choose to use the standard Google repository, or their own fork and revisions. This allows users to fix issues in android-cuttlefish builds from their own repository versions.

If using your own repository, and it a private repository, ensure `REPO_USERNAME` and `REPO_PASSWORD` have been defined.

### `ANDROID_CUTTLEFISH_REVISION`

This defines the branch/tag to use from `ANDROID_CUTTLEFISH_REPO_URL`, e.g.

- `main` - the main working branch of `android-cuttlefish`
- `v1.41.0` - the latest tagged version.
- `horizon/main` - a private repository fork of `main`
- `horizon/v1.41.0` - a private fork of tag `v1.41.0`

User may define any valid version so long as that version contains `tools/buildutils/build_packages.sh` which is a dependency for these scripts.

### `CUTTLEFISH_INSTANCE_NAME`
**Note:** Name must be a match of regex `(?:[a-z](?:[-a-z0-9]{0,61}[a-z0-9])?)`, i.e lower case.

Optional parameter to allow users to create their own unique instance templates for use in development and/or testing.

If left empty, the name will be derived from `ANDROID_CUTTLEFISH_REVISION` e.g. `cuttlefish-vm-main` and create
an instance template `instance-template-cuttlefish-vm-main` and an image `image-cuttlefish-vm-main`. If the `ANDROID_CUTTLEFISH_REVISION` contains special characters, these will be replaced, eg. `/` replaced by `-` and `.` removed, this is to comply with GCE regex requirements.

If user defines a unique name, ensure the following is met:

- The name should start with `cuttlefish-vm`
- Jenkins CasC (`values-jenkins.yaml`) must be updated to provide a new `computeEngine` entry for this unique template. For reference, see existing entry for `cuttlefish-vm-main`.
  - Choose a sensible `cloudName`, such as `cuttlefish-vm-unique-name` (e.g. the same name as the instance template with the "instance-template" prefix removed).
  - Once synced, this new cloud will appear in `Manage Jenkins` -> `Clouds`
  - Tests jobs may then reference that unique instance by setting the `JENKINS_GCE_CLOUD_LABEL` parameter to the new cloud label (`cloudName`).

### `DELETE`

Allows deletion of an existing instance templates and its referenced image.

If deleting a standard instance template (i.e. name auto-generated), simply define the version in `ANDROID_CUTTLEFISH_REVISION` and the required names will be derived automatically.

- `ANDROID_CUTTLEFISH_REVISION`: choose the version you wish to delete
- `DELETE`: This ensures the instance template, disk image and VM instance are deleted.
- `Build` : trigger build to delete all artifacts.

If user is deleting a uniquely-created instance template (i.e. name specified by `CUTTLEFISH_INSTANCE_NAME`), then define `CUTTLEFISH_INSTANCE_NAME` as was used to create it (i.e. the same name as the instance template with the "instance-template" prefix removed).

- `CUTTLEFISH_INSTANCE_NAME`: choose the template unique name you wish to delete
- `DELETE`: This ensures the instance template, disk image and VM instance are deleted.
- `Build` : trigger build to delete all artifacts.

### `UPDATE_SSH_AUTHORIZED_KEYS`

Republishes the instance template with an updated `jenkins-authorized-key` (and related fields) using the public key derived from `SSH_PRIVATE_KEY_NAME`. The implementation **recreates** the instance template; it does **not** run Packer.

Use this when rotating the Jenkins SSH key and you need new VMs created from the template to pick up the new key without rebuilding the Packer image.

- `UPDATE_SSH_AUTHORIZED_KEYS`: set to `true`
- `DELETE`: keep `false`
- `ANDROID_CUTTLEFISH_REVISION` or `CUTTLEFISH_INSTANCE_NAME`: set to target template
- `Build`: triggers stage 2 (template republish; no image bake)

### `SSH_PRIVATE_KEY_NAME`

Jenkins credential name of the private SSH key used by the pipeline.

The pipeline derives the matching public key and injects it into instance template metadata (`jenkins-authorized-key`).
At VM boot, the startup script writes that key to **`/home/<jenkins-user>/.ssh/authorized_keys`**, where **`jenkins-user`** instance metadata is set from **`DEFAULT_USER`** when the template is created (default **`jenkins`** in `cf_create_instance_template.sh`). If you change `DEFAULT_USER`, you must align Jenkins CasC (`runAsUser`, `remoteFs`, and the SSH credential **username**) and rebuild the Packer image so the account exists on disk.

### `DEFAULT_USER`

Unix account created on the Cuttlefish VM during the Packer bake and propagated to instance template metadata as **`jenkins-user`**. Default in `cf_create_instance_template.sh` is **`jenkins`**, matching the Cuttlefish GCE cloud configuration in GitOps. Override only when you intentionally change CasC and the SSH credential to the same username; it is **not** tied to the Docker image `ARG USER` used by the CF Instance Template build pod.

### `REPO_USERNAME`

Required if using a private repository defined in `ANDROID_CUTTLEFISH_REPO_URL`.

### `REPO_PASSWORD`

Required if using a private repository defined in `ANDROID_CUTTLEFISH_REPO_URL`.

### `ANDROID_CUTTLEFISH_POST_COMMAND`

Command to run in the `ANDROID_CUTTLEFISH_REPO_URL` defined repo. e.g.
- To fix the netsimd build issues with cxxbridge:
  - `git cherry-pick 78b66377`
- Replace stale repos cuttlefish may be using, such as old kernel.org repos that have been deleted:
  - `sed -i 's|https://git.kernel.org/pub/scm/linux/kernel/git/jaegeuk/f2fs-tools|https://github.com/jaegeuk/f2fs-tools|g' ./base/cvd/build_external/f2fs_tools/f2fs_tools.MODULE.bazel`

### `MACHINE_TYPE`

The machine type to be used for the VM instance. For x86, the default is `n1-standard-64`. Whereas ARM64 currently only `c4a-highmem-96-metal` is available.

Defines the (--machine-type)[https://cloud.google.com/compute/docs/general-purpose-machines] parameter.

To create a custom machine type, do not define `MACHINE_TYPE` and instead define the 3 `CUSTOM` options which will specify the machine type.

### `CUSTOM_VM_TYPE`

Specifies a custom machine type.

Defines the (--custom-vm-type)[https://cloud.google.com/sdk/gcloud/reference/compute/instance-templates/create] parameter.

### `CUSTOM_CPU`

Specifies the number of cores needed for custom machine type.

Defines the (--custom-cpu)[https://cloud.google.com/sdk/gcloud/reference/compute/instance-templates/create] parameter.

### `CUSTOM_MEMORY`

Specifies the memory needed for custom machine type.

Defines the (--custom-memory)[https://cloud.google.com/sdk/gcloud/reference/compute/instance-templates/create] parameter.

### `BOOT_DISK_SIZE`

A boot disk is required to create the instance, therefore define the size of disk required.

### `BOOT_DISK_TYPE`

Define the Boot disk type. Typically:
- x86_64: `pd-balanced`
- ARM64: `hyperdisk-balanced`

### `MAX_RUN_DURATION`

VM instances are expensive so it is advisable to define the maximum amount of time to run the instance before it will automatically be terminated. This avoids leaving expensive instances in running state and consuming resources.

User may disable by setting the value to 0, but they must be aware of any costs that they may incur to their project.  Setting to 0 is useful when creating development test instances so users can connect directly to the VM instance.

### `JAVA_VERSION`

Exact **Debian/Ubuntu apt package name** for the JDK (no hidden fallbacks). Examples: **`temurin-21-jdk`** (Eclipse Temurin), **`openjdk-21-jdk-headless`** (distro OpenJDK). Match the Java major to your Jenkins controller (e.g. **2.555+** agents need **21**).

- **x86 Jenkins job** default: **`temurin-21-jdk`**. If the package name starts with **`temurin-`**, provisioning adds the **Adoptium** apt repo, then runs **`apt install ${JAVA_VERSION}`**.
- **ARM64 Jenkins job** default: **`openjdk-21-jdk-headless`** (Ubuntu repos). Use **`temurin-21-jdk`** there only if you want Temurin (Adoptium repo is added the same way).

Build VMs need outbound **HTTPS** to distro mirrors and, for Temurin, **packages.adoptium.net**.

### `OS_VERSION`

Override the OS version. These regularly become deprecated and superceded, hence option to update to newer version.

- x86_64: use versions from debian family only.
- ARM64: use versions from `ubuntu-2204-lts-arm64` family.

Refer to `gcloud compute images list` for the version names based on family.

### `OS_PROJECT`

Disk image project.

Refer to `gcloud compute images list` for the project names based on family and OS version.

### `CURL_UPDATE_COMMAND`

Command provided to upgrade Curl from standard OS release versions. In the case of debian, bakports are used.

e.g. `"sudo apt install -t bookworm-backports -y curl libcurl4` would update Curl to latest from Debian backports.

### `NODEJS_VERSION`

MTK Connect requires NodeJS; this option allows you to update the version to install on the instance template.

### `CTS_ANDROID_<14|15|16>_URL`

Defines the URL where to retrieve and install the Android CTS test harness. Leave blank if not required, or override the
current default using your own version, e.g. from bucket storage.

### `ARM64 Unique Configuration`

The following are unique to ARM64 support because support is currently in preview and limited to United States region,
therefore users may need to override if their projects are not located within `us-central1`.

#### `ADDITIONAL_NETWORKING`

Only applicable to ARM64 instances is still in early development support.

ARM64 bare metal currently require `nic-type=IDPF`

#### `SUBNET`

Define the subnet to use for ARM64 instances, or leave blank to use default platform subnet.

#### `REGION`

Region of the instance to create. Leave black to use the default platform region.

#### `ZONE`

Region of the instance to create. Leave black to use the default platform zone.

## Private repo and branch (e.g. `horizon/main`) — artifacts and Jenkins GCE <a name="private-repo-and-branch-eg-horizonmain--artifacts-and-jenkins-gce"></a>

Use this when **`ANDROID_CUTTLEFISH_REPO_URL`** points to a **private** fork (or mirror) and **`ANDROID_CUTTLEFISH_REVISION`** is a branch such as **`horizon/main`**.

### 1. Jenkins job parameters (CF Instance Template)

| Parameter | Example | Purpose |
|-----------|---------|---------|
| `ANDROID_CUTTLEFISH_REPO_URL` | `https://github.com/your-org/android-cuttlefish.git` | Clone URL for Cuttlefish sources (HTTPS is typical for `REPO_*` credentials). |
| `ANDROID_CUTTLEFISH_REVISION` | `horizon/main` | Branch or tag to `git checkout` during the image bake. |
| `REPO_USERNAME` | service account or PAT user | Required for **private** HTTPS clones. |
| `REPO_PASSWORD` | PAT or password | Use a credential with **read** access to the repo. |
| `CUTTLEFISH_INSTANCE_NAME` | *(empty)* | Leave empty to **derive** the VM/template name from the revision (see below), or set an explicit name starting with `cuttlefish-vm-`. |

Run stage **`1`** (normal build) with **`DELETE=false`** unless you are deleting artifacts.

### 2. What gets created in GCP (auto-derived name from `horizon/main`)

Naming follows `cf_create_instance_template.sh`: the revision string is sanitized for GCE (`.` removed, **`/` → `-`**), then prefixed with `cuttlefish-vm-` when `CUTTLEFISH_INSTANCE_NAME` is left at the default `cuttlefish-vm`.

For **`ANDROID_CUTTLEFISH_REVISION=horizon/main`** and default instance naming:

| Resource | Name |
|----------|------|
| Cuttlefish “version” token | `horizon-main` (from `horizon/main`) |
| Logical / VM prefix | **`cuttlefish-vm-horizon-main`** |
| Disk image | **`image-cuttlefish-vm-horizon-main`** |
| Instance template | **`instance-template-cuttlefish-vm-horizon-main`** |

Verify after the job:

```bash
gcloud compute instance-templates list | grep cuttlefish-vm-horizon-main
gcloud compute images list | grep image-cuttlefish-vm-horizon-main
```

### 3. Wire Jenkins GCE Cloud (GitOps — preferred)

Test jobs (CVD Launcher, CTS Execution, etc.) provision agents via the **Google Compute Engine** plugin using a **label** that must match a **cloud** whose **`template`** URL points at your new instance template.

1. Open **`gitops/workloads/values-jenkins.yaml`** (CasC for Jenkins).
2. Under **`jenkins:` → `controller:` → `JCasC` → `configScripts:`** (or equivalent), find the **`clouds:`** list and the existing **`computeEngine`** entries (e.g. `cuttlefish-vm-main`).
3. **Add a new** `- computeEngine:` block by **copying** an existing Cuttlefish entry and changing only what identifies the template and labels:
   - **`cloudName`**: e.g. **`cuttlefish-vm-horizon-main`** (this is the **cloud id** in Jenkins).
   - **`labelString`** / **`labels`** / **`namePrefix`**: use the **same** string as `cloudName` (e.g. **`cuttlefish-vm-horizon-main`**), consistent with existing entries.
   - **`template`**: set to the full instance-template self-link for **`instance-template-cuttlefish-vm-horizon-main`** in your project (same pattern as siblings — only the template **name suffix** changes).
   - Keep **`zone`**, **`region`**, **`projectId`**, **`credentialsId`**, **`sshConfiguration`**, **`remoteFs`**, **`runAsUser`** aligned with other Cuttlefish clouds unless you intentionally differ.
4. Merge and deploy via your normal **GitOps** process so CasC reapplies Jenkins configuration.

After sync, **Manage Jenkins → Clouds** should list the new cloud (e.g. `cuttlefish-vm-horizon-main`).

### 4. Wire Jenkins GCE Cloud (UI only — not source of truth)

You can **verify** or **prototype** under **Manage Jenkins → Clouds →** (Google Compute Engine plugin):

- Add or edit a cloud so the **Instance template** points at `instance-template-cuttlefish-vm-horizon-main` and the **labels** match what test jobs use.

**Caution:** Manual UI edits are **overwritten** on the next CasC sync unless the same settings exist in **`values-jenkins.yaml`**. Treat GitOps as authoritative for production.

### 5. Point test jobs at the new cloud

Jobs that run on Cuttlefish VMs expose **`JENKINS_GCE_CLOUD_LABEL`** (see job DSL under `workloads/android/pipelines/tests/*/groovy/job.groovy`). Set it to the **label** that matches the cloud — typically the same as **`cloudName`** / **`labelString`**, e.g.:

- **`JENKINS_GCE_CLOUD_LABEL=cuttlefish-vm-horizon-main`**

Default parameter values may still reference `cuttlefish-vm-main`; change per run or update the job default in **Jenkins** / **Seed job** / **CasC** if this template becomes the platform standard.

## Example Usage <a name="examples"></a>

If user wishes to create a temporary test instance to work with, then they can do so as follows from Jenkins:

- `ANDROID_CUTTLEFISH_REVISION`: choose the version you wish to build the template from
- `CUTTLEFISH_INSTANCE_NAME` : provide a name, starting with cuttlefish-vm, e.g. `cuttlefish-vm-test-instance-v110.`
- `MAX_RUN_DURATION` : set to 0 to avoid instance being deleted after this time.
- `Build`

Once they have finished with the instances, they should delete to avoid excessive costs.

- `CUTTLEFISH_INSTANCE_NAME` : provide a unique name, starting with cuttlefish-vm, e.g. `cuttlefish-vm-test-instance-v110.`
- `DELETE` : This ensures the instance template, disk image and VM instance are deleted.
- `Build`

## SYSTEM VARIABLES <a name="system-variables"></a>

There are a number of system environment variables that are unique to each platform but required by Jenkins build, test and environment pipelines.

These are defined in Jenkins CasC `values-jenkins.yaml` and can be viewed in Jenkins UI under `Manage Jenkins` -> `System` -> `Global Properties` -> `Environment variables`.

These are as follows:

-   `ANDROID_BUILD_DOCKER_ARTIFACT_PATH_NAME`
    - Defines the registry path where the Docker image used by builds, tests and environments is stored.

-   `CLOUD_PROJECT`
    - The GCP project, unique to each project. Important for bucket, registry paths used in pipelines.

-   `CLOUD_REGION`
    - The GCP project region. Important for bucket, registry paths used in pipelines.

-   `CLOUD_ZONE`
    - The GCP project zone. Important for bucket, registry paths used in pipelines.

-   `HORIZON_DOMAIN`
    - The URL domain which is required by pipeline jobs to derive URL for tools and GCP.

-   `HORIZON_SCM_URL`
    - The URL to the Horizon SDV git repository.

-   `HORIZON_SCM_BRANCH`
    - The branch name the job will be configured for from `HORIZON_SCM_URL`.

-   `JENKINS_SERVICE_ACCOUNT`
    - Service account to use for pipelines. Required to ensure correct roles and permissions for GCP resources.
