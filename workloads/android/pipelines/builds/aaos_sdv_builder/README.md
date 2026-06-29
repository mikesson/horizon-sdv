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

# AAOS SDV Builder — Jenkins and `.gitcookies` and/or `.git-credentials`

This job (`Android Workflows` → `Builds` → `AAOS SDV Builder`) mirrors **AAOS Builder** shell logic under [`../aaos_builder/`](../aaos_builder/); the pipeline lives in [`Jenkinsfile`](Jenkinsfile) and [`groovy/job.groovy`](groovy/job.groovy).

General Android build parameters and script behaviour are documented in [docs/workloads/android/builds/aaos_builder.md](../../../../../docs/workloads/android/builds/aaos_builder.md).

## Setup

### Prerequisites

- **PVC / StorageClass for SDV (build cache)** — Jenkins binds an ephemeral volume for AAOS builds using the **SDV** storage class from [`gitops/workloads/android/templates/dynpvc-sc.yaml`](../../../../../gitops/workloads/android/templates/dynpvc-sc.yaml) (`reclaimable-storage-class-android-sdv`, with your Helm `namespacePrefix` applied to the metadata name). The AAOS SDV Builder pod template references this class (see `STORAGE_CLASS_SUFFIX` / `android-sdv` in the pipeline). Before the job can schedule, that **StorageClass** must exist in the cluster:
  - **GitOps (recommended):** Deploy the Android workloads so Argo CD (or equivalent) applies `dynpvc-sc.yaml` with the correct values.
  - **Manual:** Render or edit the template for your environment, then `kubectl apply -f …` against the target cluster namespace and storage layer.
  - **Terraform / automation:** If your platform provisions workloads or add-ons via Terraform (for example Helm releases or `kubectl` manifests), include the same StorageClass in that pipeline so it matches what GitOps would apply.

- **Gerrit `.gitcookies` (base64)** — Partner HTTPS to `*.googlesource.com` requires a valid `.gitcookies` file. For Jenkins, paste **base64** of the raw file into the **non-stored** build parameter **`GERRIT_GITCOOKIES_BASE64`** (see below). Do **not** use a Jenkins **Secret text** credential for this job; the value is entered per run and is not stored in the credentials store.

- **Gerrit `.git-credentials` (base64)** - Additional gerrit/git HTTP (User and Password / Personal Access Token) credentials which can be used i.e. for access to other software repositories. For Jenkins, paste **base64** of the raw file into the **non-stored** build parameter **`GERRIT_GITCREDENTIALS_BASE64`** (see below). Do **not** use a Jenkins **Secret text** credential for this job; the value is entered per run and is not storred in the credentials store.

### Execution

- Under **Build with Parameters**, paste **base64** `.gitcookies` into **`GERRIT_GITCOOKIES_BASE64`** (masked field) so the Initialise step can write `~/.gitcookies`. Leave it empty only when **`GERRIT_AUTH_MODE`** is **`none`** (public Gerrit / no cookie auth).

- If required additional gerrit/git credentials can be provided as **base64** `.git-credentials` into **`GERRIT_GITCREDENTIALS_BASE64`** (masked field) so the Initialize step can create `~/git-credentials`.

### Testing (CVD Launcher / MTK Connect)

Use the **CVD Launcher** or **CTS Execution** jobs to boot Cuttlefish against artifacts produced by this builder (see [docs/workloads/android/tests/cvd_launcher.md](../../../../../docs/workloads/android/tests/cvd_launcher.md)):

- **`CUTTLEFISH_DOWNLOAD_URL`** — Set this to the **same** GCS prefix as the SDV build output (artifact location / build number), e.g. `gs://<bucket>/Android/Builds/AAOS_SDV_Builder/<BUILD_NUMBER>/`, so the launcher fetches `cvd-host_package.tar.gz`, `*img*.zip`, and `sdv-cf` from that run.

- **`CVD_COMMAND_LINE`** — For SDV-published `sdv-cf` in that directory, an example is: `./sdv-cf create --instance_name=instance1` (adjust flags as needed; see the CVD Launcher doc).

- **Keep-alive** — If you want to debug the host or use **MTK Connect**, set **`CUTTLEFISH_KEEP_ALIVE_TIME`** (CVD Launcher / CTS Execution) to **greater than 0** minutes so the instance stays up after the stage completes; enable MTK Connect per that job’s parameters.

## SDV Cuttlefish lunch targets (`AAOS_LUNCH_TARGET`)

Partner SDV **Cuttlefish** lunch targets use the pattern `sdv_*_cf*` (see [`aaos_environment.sh`](../aaos_builder/aaos_environment.sh)). Typical values:

| `AAOS_LUNCH_TARGET` | Purpose |
|---------------------|---------|
| `sdv_core_cf-trunk_staging-userdebug` | Build an image of AAOS SDV Core (`sdv_core`) that runs on Cuttlefish (`cf`). |
| `sdv_media_cf-trunk_staging-userdebug` | Build an image of AAOS SDV Core with virtIO-virtualized media APIs (`sdv_media`). |
| `sdv_media_har_cf-trunk_staging-userdebug` | Extends the AAOS SDV media (`sdv_media_cf`) lunch target with the high-availability renderer (HAR). HAR runs automatically on boot. |
| `sdv_ivi_cf-trunk_staging-userdebug` | Build an image of In-Vehicle Infotainment (IVI) containing the SDV Gateway and Java sample apps. |
| `sdv_ivi_cf_ds-trunk_staging-userdebug` | Extends the AAOS SDV IVI target (`sdv_ivi_cf`) and includes Display Safety’s DriverUI and Instrument Cluster implementation. |

The job default in [`groovy/job.groovy`](groovy/job.groovy) is `sdv_core_cf-trunk_staging-userdebug`.

## Build parameter: `GERRIT_GITCOOKIES_BASE64`

The job exposes a **non-stored password** parameter (`nonStoredPassword` in Job DSL). The value must be **base64** of the raw **`.gitcookies`** file (Netscape format, **tab-separated** fields)—**one line**, with no manual line breaks inside the value. It is **not** saved in Jenkins credential stores; enter it each time you run the job (or use **`GERRIT_AUTH_MODE` = `none`** when cookies are not required). Encoding avoids browsers and Jenkins altering multi-line pastes.

### Encode your `.gitcookies` locally

The decoded bytes must match a working local file (compare SHA-256 after decode).

```bash
# macOS — single line in clipboard
base64 -i ~/.gitcookies | tr -d '\n' | pbcopy

# Linux (GNU coreutils) — no wrapping
base64 -w0 < ~/.gitcookies | xclip -selection clipboard   # or: tr -d '\n'
```

Sanity check before pasting into the build:

```bash
pbpaste | tr -d '\n\r \t' | base64 -d | shasum -a 256   # macOS pbpaste
# compare to: shasum -a 256 ~/.gitcookies
```

## Build parameter: `GERRIT_GITCREDENTIALS_BASE64`

The job exposes a **non-stored password** parameter (`nonStoredPassword` in Job DSL). The value must be **base64** of the raw **`.git-credentials`** file, **Tab-separated** lines, **one line**, with no manual line breaks inside the value. It is **not** saved in Jenkins credential stores; enter it each time you run the job. You can use same encoding steps as described for `.gitcookies` above, executed on desired `.git-credentials` file.

### Each build

1. **Build with Parameters** → paste the **base64** string into **`GERRIT_GITCOOKIES_BASE64`** (masked).
3. If required, **Build with Parameters** → paste the **base64** string into **`GERRIT_GITCREDENTIALS_BASE64`** (masked).
3. Do not paste raw multi-line `.gitcookies` or `.git-credentials` into the above fields; do not commit any of these files to Git.

If you previously used **plain** multi-line `.gitcookies` or `.git-credentials` text, **re-encode** with the commands above (plain text will not base64-decode to valid credentials bytes).

### Initialise stage behaviour

Processing of cookies (**`GERRIT_GITCOOKIES_BASE64`**)

1. Binds the parameter value of **`GERRIT_GITCOOKIES_BASE64`** to environment variable **`GITCOOKIES_SECRET`** (masked in console output via **Mask Passwords**).
2. Strips whitespace from the values, **base64-decodes** into `~/.gitcookies`, then `chmod 600`. Decode failure fails the build with a clear error.
3. Runs `git config --global http.cookiefile ~/.gitcookies`.
4. Unsets `credential.helper` and removes `~/.git-credentials` to wipe existing stored username/password data.
5. Logs **`HOME`**, confirms `~/.gitcookies` is non-empty, prints **byte size**, **SHA-256** (for comparison with a known-good file), **`http.cookiefile`**, and a dump of **`~/.gitconfig`** after cookie setup (before `aaos_initialise.sh`; no cookie file contents in log).

Processing of additional credentials (**`GERRIT_GITCREDENTIALS`**)

1. Binds the parameter value of **`GERRIT_GITCREDENTIALS_BASE64`** to environment variable **`GITCREDENTIALS_SECRET`** (masked in console output via **Mask Passwords**).
2. Strips whitespace from the values, **base64-decodes** into `~/.git-credentials`, then `chmod 600`. Decode failure fails the build with a clear error.
3. Runs `git config --global credential.helper store`.
4. Logs **`HOME`**, confirms `~/.git-credentials` is non-empty, prints **byte size**, **SHA-256** (for comparison with a known-good file), and a dump of **`~/.gitconfig`** after additional credentials setup (before `aaos_initialise.sh`; no credentials file contents in log).

After processing of cookies and additional credentials the Initialize stage invokes `aaos_initialise.sh` with **`GERRIT_AUTH_MODE`** from the job (default **`auto`** both cookies and additional credentials take effect)

### Scheduled / automated builds

**Non-stored** parameters have no saved default. Jobs triggered by timer or SCM with no user must either use **`GERRIT_AUTH_MODE` = `none`** (if appropriate) or use a **wrapper** job that injects the secret from your vault (not covered here).

### Comparing the build with SSH on the same agent

On the build pod, a working manual setup uses **`git config --global http.cookiefile`** pointing at **`~/.gitcookies`** (see `[http] cookiefile` in `~/.gitconfig`). The pipeline sets the same. To verify the **pasted base64** matches the file you use by hand, compare **byte count** and **SHA-256** from the Initialise log with:

```bash
wc -c < ~/.gitcookies
sha256sum ~/.gitcookies   # or: shasum -a 256 ~/.gitcookies
```

If **SHA-256** differs, update the value you paste. If they match but **`repo init` still fails** in CI, suspect **network/egress** from the job environment rather than cookie bytes.

### `git requires authentication, but repo cannot perform interactive authentication`

The base64 decoded successfully, but **Git is not accepting the cookie file** for HTTPS to `*.googlesource.com`. Check:

1. **Netscape format, tab-separated fields** — Fix the file locally (e.g. `tr ',' '\t'` if the export used commas), verify with `repo`/`git`, then re-encode to base64 and paste again.
2. **Valid base64** — Re-encode with the commands above; avoid extra spaces or line breaks inside the pasted value.
3. **Valid, unexpired cookies** — Regenerate `.gitcookies` from the partner flow and update what you paste.
4. **Cookie domain** — Lines must apply to the host you fetch (e.g. **`.googlesource.com`** or **`partner-android.googlesource.com`** per Google’s instructions).

## Horizon Gerrit vs partner `*.googlesource.com`

- **AAOS Builder** (default) uses **Username with password** for Horizon Gerrit HTTP and `~/.git-credentials`.
- **AAOS SDV Builder** is intended for partner manifests where Git HTTPS is driven by **`.gitcookies`** (OAuth-style cookies), not that username/password pair.

## `GERRIT_TOPIC` and Gerrit auth

`aaos_initialise.sh` queries the Gerrit REST API via **`curl_gerrit_rest_get`**, controlled by **`GERRIT_AUTH_MODE`**:

| Mode | Behaviour |
|------|-------------|
| **`username_password`** | `curl -u` with **`GERRIT_USERNAME`** and **`GERRIT_PASSWORD`**. |
| **`gitcookies`** | `curl -b` using **`git config http.cookiefile`** or **`~/.gitcookies`**. |
| **`none`** | Unauthenticated `curl` (public Gerrit API only). |
| **`auto`** (or unset) | Username/password if both set; else cookie file if present; else **`none`**. |

AAOS SDV Builder defaults the job parameter to **`gitcookies`** and installs `.gitcookies` from **`GERRIT_GITCOOKIES_BASE64`** before **`aaos_initialise.sh`** runs (unless **`none`** is selected).

## Rotating cookies

When the vendor rotates tokens, regenerate `.gitcookies`, re-encode to base64, then paste the new value into **`GERRIT_GITCOOKIES_BASE64`** on the next run.
