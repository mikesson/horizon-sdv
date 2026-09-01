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

# sample-data module

Helm chart: `gitops/modules/sample-data-module` (Argo CD Application `mod-sample-data` when enabled from the Developer Portal).

Declares **`GCSBucket`** `sample-workloads-data` → GCP bucket **`{project_id}-sample-workloads-data`** (`bucketName` Helm value). The CR is reconciled by the **storage-gcs** operator (enable that module first). The bucket CR is owned by this module, not by `storage-gcs`, so disabling storage-gcs does not prune the CR while sample-data remains enabled.

**Clients** (e.g. **`sample`** / `sample-smoke-test`) pass this bucket name into **`storage-gcs-upload`** as the `bucket` parameter (`gcsBucket` workflow arg). The upload bucket is **not** configured inside `storage-gcs`.

## Module catalog

- **`sample-data`** lists **`storage-gcs`** as a **hard dependency** (enable storage-gcs first).
- **`sample`** lists **`sample-data`** and **`storage-gcs`** as soft dependencies (smoke-test GCS upload when both are enabled).

## Migrating from `workloads-android-data`

The bucket was renamed to **`sample-workloads-data`** (`GCSBucket` CR and GCP bucket `{project_id}-sample-workloads-data`). On clusters that already had the old CR:

1. Copy any objects you need from `gs://{project_id}-workloads-android-data/` to the new bucket (or leave the old bucket for archival).
2. Delete the old `GCSBucket` CR `workloads-android-data` in `horizon` if you want to drop operator management. Default policy is **Retain** (GCP bucket kept). Set `spec.deletionPolicy: Delete` first if you want the operator to empty and delete that bucket.
3. Sync `mod-sample-data-gcs-bucket` so the new `sample-workloads-data` CR is applied.

## Data cleanup on disable

| Action | Bucket CR | GCP bucket / objects |
|--------|-----------|----------------------|
| Disable **storage-gcs** while **sample-data** enabled | Blocked (409 — hard dependent) | Retained |
| Disable **sample-data** (manager on) | Removed | Emptied and deleted (`deletionPolicy: Delete` on this CR) |
| Disable **storage-gcs** after **sample-data** off | N/A | CR already gone or deleted |

## Enable order

1. **storage-gcs** (operator + API)
2. **sample-data** (`GCSBucket` CR → bucket created by operator)
3. **sample** (optional GCS smoke stage when **sample-data** and **storage-gcs** soft deps are on)
