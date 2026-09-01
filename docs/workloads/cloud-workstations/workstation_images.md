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

# Workstation Images

## Table of contents
- [Introduction](#introduction)
- [Layered architecture](#layered-architecture)
- [Hook system](#hook-system)
- [Build order](#build-order)
- [Prerequisites](#prerequisites)
- [Environment Variables/Parameters](#environment-variables)
- [System Variables](#system-variables)
- [Antigravity tooling per image](#antigravity-tooling-per-image)
- [Known Issues](#known-issues)

## Introduction <a name="introduction"></a>

The Jenkins folder `Cloud Workstations > Workstation Images` houses pipelines that build Docker images that can later be used in Cloud Workstations as containers.

The following pipelines are currently available:
- **Horizon Preflight**: Builds the Alpine `/export/` provider layer used as `BASE_IMAGE` by Horizon GNOME.
- **Horizon GNOME**: Builds the shared headless GNOME desktop base (RDP / Guacamole), consuming published Preflight via `BASE_IMAGE`.
- **Horizon Code OSS**: Builds a lightweight, general-purpose IDE based on open-source VS Code. Remains on the legacy stack for this iteration (see [Layered architecture](#layered-architecture)).
- **Horizon Android Studio**: Thin child of Horizon GNOME via `BASE_IMAGE` (standard Android Studio IDE).
- **Horizon Android Studio for Platform (ASfP)**: Thin child of Horizon GNOME via `BASE_IMAGE` (AOSP / platform IDE).
- **Horizon AOSP Build Image Chain** (folder `Cloud Workstations > Workstation Image Chain`): Parent orchestrator that optionally builds Preflight → GNOME, then builds the selected IDE children (ASfP and/or Android Studio, both enabled by default) in parallel while threading the resolved published tag as `BASE_IMAGE`. Lives outside Workstation Images so leaf `blockOn` rules do not deadlock against the parent. Leaf jobs remain independently triggerable against an existing published GNOME tag.

Image pipelines need only be run once, or when their Dockerfile is updated. There is an option not to push the resulting image to the registry, so that devs can test their changes before committing the image. When using the chain job, base stages (when enabled) always push so downstream stages can pull the published tag; child `NO_PUSH` still controls whether the IDE images are published.

**Antigravity IDE is not included** in any workstation image. **Gemini Code Assist** install and behaviour are unchanged by Antigravity tooling.

### References
- [buildkit](https://hub.docker.com/r/moby/buildkit)
- [Base Image for Horizon Code OSS](https://us-central1-docker.pkg.dev/cloud-workstations-images/predefined/code-oss:latest)
- [Upstream Preflight module](https://github.com/sce-taid/cloud-workstations-custom-image-examples/tree/main/examples/preflight)
- [Upstream GNOME blueprint](https://github.com/sce-taid/cloud-workstations-custom-image-examples/tree/main/examples/images/gnome)
- [Base Template for Horizon Android Studio for Platform (ASfP)](https://github.com/GoogleCloudPlatform/cloud-workstations-custom-image-examples/tree/main/examples/images/android-open-source-project/android-studio-for-platform)
- [Base Template for Horizon Android Studio](https://github.com/GoogleCloudPlatform/cloud-workstations-custom-image-examples/tree/main/examples/images/android/android-studio)

## Layered architecture <a name="layered-architecture"></a>

Desktop IDE images follow a **preflight → gnome → child** layering model. Each layer publishes to Artifact Registry and is consumed by the next via the `BASE_IMAGE` build parameter (full image URI).

| Layer | Image | Role |
| --- | --- | --- |
| Preflight provider | `horizon-preflight` | Alpine asset-export image. Packages the nginx Preflight loader SPA, systemd orchestration, machine-id / Cloud Logging setup, and `config_rendering` under `/export/`. Not bootable on its own; GNOME copies `/export/` into the desktop base. |
| GNOME base | `horizon-gnome` | Bootable headless GNOME desktop on Google’s `predefined/base`. Adds `gnome-remote-desktop` (internal RDP), Apache Guacamole (`guacd` + web app) via in-image Docker, Chrome, Gemini CLI / ADK, and Horizon customizations (`gemini-mcp-agent`). Consumes published Preflight as `BASE_IMAGE`. |
| IDE children | `horizon-asfp`, `horizon-android-studio` | Thin children of published GNOME. Add IDE packages, Cuttlefish / Android SDK tooling, and layer-specific hooks. Inherit desktop remote access, Chrome, and shared agents from GNOME — do not reinstall them. |

```text
horizon-preflight  →  horizon-gnome  →  horizon-asfp
                                   └→  horizon-android-studio
```

**Remote desktop:** Browser access for GNOME-based images uses Guacamole over RDP (`gnome-remote-desktop`). The legacy **noVNC / TigerVNC** stack is **retired** for `horizon-gnome` and its IDE children (TigerVNC and Chrome Remote Desktop stay disabled on the GNOME base).

**Legacy exception:** `horizon-code-oss` remains on the legacy Code OSS / predefined stack for this iteration and is **not** layered on Preflight or GNOME.

Source paths live under `workloads/cloud-workstations/pipelines/workstation-images/` (`horizon-preflight`, `horizon-gnome`, `horizon-asfp`, `horizon-android-studio`, `horizon-code-oss`).

## Hook system <a name="hook-system"></a>

GNOME and IDE child images extend the base through a centralized build script rather than monolithic Dockerfiles.

`/google/scripts/build/configure_workstation.sh` (shipped from Preflight into GNOME, then inherited by children) runs at image build time and:

1. Applies asset permissions and regional APT configuration (`GCP_REGION`).
2. Runs **`/build-hooks.d/`** scripts (before package install — add repos, binaries, or early setup).
3. Refreshes package lists and installs `EXTRA_PKGS` / `.deb` files from `EXTRA_DEB_URLS`.
4. Runs **`/post-install-hooks.d/`** scripts (after install — patch third-party packages, desktop files, tooling).
5. Applies desktop integration (autostart / dock pinning) and cleans temporary hook directories from the image.

Child Dockerfiles follow this pattern:

```dockerfile
ARG BASE_IMAGE
FROM ${BASE_IMAGE}

ENV EXTRA_PKGS="..."
ENV EXTRA_DEB_URLS="..."
ENV WORKSTATION_DEFAULT_APP="..."

COPY assets/ /          # includes build-hooks.d/ and post-install-hooks.d/
RUN /google/scripts/build/configure_workstation.sh
```

Hook scripts are numbered for ordering (for example `01_…`, `15_…`). Place layer-specific logic in those directories under each image’s `assets/`; keep shared desktop/remote-access logic in GNOME.

## Build order <a name="build-order"></a>

Build and publish layers in dependency order so each stage can pull a published upstream tag:

1. **Horizon Preflight** — push `horizon-preflight:<tag>`.
2. **Horizon GNOME** — set `BASE_IMAGE` to that Preflight URI; push `horizon-gnome:<tag>`.
3. **IDE children** — set `BASE_IMAGE` to the GNOME URI; build `horizon-asfp` and/or `horizon-android-studio` (independently or in parallel).

Use **Horizon AOSP Build Image Chain** (`Cloud Workstations > Workstation Image Chain`) to run that sequence in one job: optional Preflight → optional GNOME → selected IDE children in parallel, with the resolved published URI threaded as `BASE_IMAGE`. Leave base stages unchecked to reuse already-published `PREFLIGHT_IMAGE` / `GNOME_IMAGE` values, and uncheck `BUILD_HORIZON_ASFP` or `BUILD_HORIZON_ANDROID_STUDIO` to skip a child image (both are enabled by default). Leaf image jobs stay independently triggerable when only one child needs a rebuild.

`horizon-code-oss` is outside this chain and may be built on its own schedule.

## Prerequisites<a name="prerequisites"></a>

All of these pipelines depend on [`buildkit`](https://hub.docker.com/r/moby/buildkit) which should be installed by default.

GNOME and IDE child builds that run an in-image Docker daemon (Guacamole) and nested virtualization (Cuttlefish) need a machine type that supports both.

## Environment Variables/Parameters <a name="environment-variables"></a>

**Jenkins Parameters:** Image build pipelines share the following core parameters in their respective `groovy/job.groovy` definitions. Layered jobs (GNOME and IDE children) also take `BASE_IMAGE` (full published URI of the upstream layer). The chain orchestrator adds optional `BUILD_HORIZON_PREFLIGHT` / `BUILD_HORIZON_GNOME` flags plus `PREFLIGHT_IMAGE` / `GNOME_IMAGE` to reuse already-published bases, and `BUILD_HORIZON_ASFP` / `BUILD_HORIZON_ANDROID_STUDIO` (default on) to choose which child IDE images to build.

### `NO_PUSH`

Build the container image but don't push to the registry. On the chain job this applies to child IDE images only; enabled base stages always push.

### `IMAGE_TAG`

This is the tag that will be applied when the container image is pushed to the registry. For the current release we
simply use `latest` because all pipelines that depend on this container image are using `latest`.

### `BASE_IMAGE`

Full Artifact Registry URI of the published upstream image (Preflight for GNOME; GNOME for IDE children). It is used so a single IDE image can be rebuilt without rebuilding bases.

### `BUILD_HORIZON_ASFP` / `BUILD_HORIZON_ANDROID_STUDIO`

Chain job only. Select which IDE children the chain builds; both default to enabled. Uncheck one to rebuild only the other child against the resolved GNOME base. The chain fails early if no image is selected at all, and `GNOME_IMAGE` is only required when at least one child is selected and GNOME is not built in-chain.

## System Variables <a name="system-variables"></a>

There are a number of system environment variables that are unique to each platform but required by these Jenkins Cloud Workstation `Workstation Images` pipelines.

These are defined in Jenkins CasC `values-jenkins.yaml` and can be viewed in Jenkins UI under `Manage Jenkins` -> `System` -> `Global Properties` -> `Environment variables`.

These are as follows:

-   `CLOUD_PROJECT`
    - The GCP project, unique to each project. Important for bucket, registry paths used in pipelines.

-   `CLOUD_REGION`
    - The GCP project region. Important for bucket, registry paths used in pipelines.

-   `HORIZON_SCM_URL`
    - The URL to the Horizon SDV git repository.

-   `HORIZON_SCM_BRANCH`
    - The branch name the job will be configured for from `HORIZON_SCM_URL`.

-   `JENKINS_SERVICE_ACCOUNT`
    - Service account to use for pipelines. Required to ensure correct roles and permissions for GCP resources.

Below variables have their values defined under `config.workloads.cloudWorkstations.workstationPresets.wsImages` in `gitops/workloads/values-jenkins.yaml` (CasC) and are exposed as Jenkins global environment variables.

-   `CLOUD_WS_HORIZON_CODE_OSS_IMAGE_NAME`
    - Name of the Docker image on GCP Artifact registry for VS Code IDE (`horizon-code-oss`), that is used in Cloud Workstations.
    - Used by pipeline: `Horizon Code OSS`

-   `CLOUD_WS_HORIZON_PREFLIGHT_IMAGE_NAME`
    - Name of the Docker image on GCP Artifact registry for the Preflight provider (`horizon-preflight`).
    - Used by pipelines: `Horizon Preflight`, `Horizon GNOME` (`BASE_IMAGE` default), `Horizon AOSP Build Image Chain`

-   `CLOUD_WS_HORIZON_GNOME_IMAGE_NAME`
    - Name of the Docker image on GCP Artifact registry for the GNOME base (`horizon-gnome`).
    - Used by pipelines: `Horizon GNOME`, IDE children (`BASE_IMAGE` default), `Horizon AOSP Build Image Chain`

-   `CLOUD_WS_HORIZON_ASFP_IMAGE_NAME`
    - Name of the Docker image on GCP Artifact registry for Android Studio for Platform (`horizon-asfp`), that is used in Cloud Workstations.
    - Used by pipeline: `Horizon Android Studio for Platform (ASfP)`

-   `CLOUD_WS_HORIZON_ANDROID_STUDIO_IMAGE_NAME`
    - Name of the Docker image on GCP Artifact registry for Android Studio (`horizon-android-studio`), that is used in Cloud Workstations.
    - Used by pipeline: `Horizon Android Studio`

## Antigravity tooling per image <a name="antigravity-tooling-per-image"></a>

Antigravity Agent / CLI install matrix, Jenkins build parameters, and runtime notes are documented in [Antigravity on Horizon Cloud Workstation images](../common/agentic-ai/antigravity.md).

MCP client mapping (`gemini-mcp-agent` vs `antigravity-mcp-agent`): [MCP Setup and Usage Guide](../../guides/mcp_setup.md#mcp-client--tool-mapping).

## Known Issues <a name="known-issues"></a>
The builds for `Horizon Android Studio for Platform (ASfP)` and `Horizon Android Studio` pipelines may fail during a certain period citing 503 HTTP issues. This is a problem at Google's end - something out of our scope and we can only try building these images later again.

The configurations created for `Horizon Android Studio for Platform (ASfP)` and `Horizon Android Studio` workstations must have the parameter `HOST_BOOT_DISK_SIZE ≥ 31 GB`. "Start Workstation" fails when `HOST_BOOT_DISK_SIZE` is set to any value less than 31 GB. For this reason the default is set to 40GB.
