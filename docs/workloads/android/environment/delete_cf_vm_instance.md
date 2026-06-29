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

# Delete Cuttlefish VM Instance

## Table of contents
- [Introduction](#introduction)
- [Prerequisites](#prerequisites)
- [Environment Variables/Parameters](#environment-variables)
- [System Variables](#system-variables)

## Introduction <a name="introduction"></a>

During developing the Android workload and test workflow/pipelines, sometimes it may be necessary to delete VM instances created for Cuttlefish related test jobs that may have been left in place.

This is not intended for everyday usage, simply a tool when developing tests to ensure that resources can be removed if errors occur during development.

## Prerequisites<a name="prerequisites"></a>

One-time setup requirements.

- Before running this pipeline job, ensure that the following template has been created by running the corresponding job:
  - Docker image template: ``Android Workflows/Environment/Docker Image Template`

## Environment Variables/Parameters <a name="environment-variables"></a>

**Jenkins Parameters:** Defined in the groovy job definition `groovy/job.groovy`.

### `VM_INSTANCE_NAME`

The name of the VM instance to terminate and delete, but it is restricted to those prefixed with `cuttlefish-vm` to avoid accidental termination and removal of other unrelated VMs.

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

-   `HORIZON_SCM_URL`
    - The URL to the Horizon SDV git repository.

-   `HORIZON_SCM_BRANCH`
    - The branch name the job will be configured for from `HORIZON_SCM_URL`.

-   `JENKINS_SERVICE_ACCOUNT`
    - Service account to use for pipelines. Required to ensure correct roles and permissions for GCP resources.
