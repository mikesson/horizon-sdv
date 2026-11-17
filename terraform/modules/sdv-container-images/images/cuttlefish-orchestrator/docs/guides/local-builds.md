<!-- Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Testing Local Builds

Push a workstation-built Android image to the cloud and boot devices from
it. This is the cloud equivalent of running `launch_cvd` against your own
build tree, for the times your own machine is not enough.

## When to use this

If your workstation is a Linux machine with KVM, the fastest inner loop is
local: `lunch aosp_cf_x86_64_phone-userdebug && m && launch_cvd`. Push to
the cloud when local does not work or does not scale:

- your machine cannot run Cuttlefish (macOS, no nested virtualization,
  architecture mismatch)
- you need more devices than one workstation can host, or device shapes it
  cannot provide
- you want the device to outlive your laptop's lid
- you want to hand a running device to someone else as a link

The last one is the thing no local workflow can do: after pushing, the
console's streaming player makes your build a URL anyone on the deployment's
access list can open.

## Availability

Pushing and registering builds works on every deployment. **Creating a
device from a pushed build additionally requires a host image whose `cvd`
can fetch `gs://` build sources** (upstream android-cuttlefish pull request
[#3047](https://github.com/google/android-cuttlefish/pull/3047)). A device
created from a URL-source build on a host image that predates that
capability fails during the launch, because `cvd` discards a build string it
cannot resolve and then finds no images to start. The pushed build stays
registered and becomes usable when the host image is updated.

## How it works

`cfctl build push` hashes each artifact and asks the orchestrator for an
upload session. Artifacts are stored content-addressed
(`staging/sha256/<hash>/<filename>` in the deployment's artifacts bucket),
so a file whose hash is already staged is **skipped entirely**. The reply
says so and no bytes move. New content streams directly to Google Cloud
Storage through a resumable session: the bytes never pass through the
orchestrator, an interrupted transfer resumes from the last byte the server
confirmed, and progress prints in 10% steps.

The pushed set is then registered as a build with typed artifact roles, and
its **fingerprint** (a hash over the sorted per-file content hashes)
makes registration idempotent: pushing identical bytes again returns the
existing build instead of creating a duplicate, regardless of file paths,
ordering, or who pushes.

At device-create time the host's `cvd fetch` downloads the artifacts from
GCS directly, authenticated as the host VM's own service account, then
boots them exactly as it would an `@ab/` build from ci.android.com.

## Prerequisites

- An AOSP tree built for a Cuttlefish target. `m dist` produces both
  required artifacts in `out/dist/`:
  - `<target>-img-<build>.zip`, the device images
  - `cvd-host_package.tar.gz`, the host tools, which must come from the
    same build as the images
- `cfctl` authenticated against the deployment (see
  [Using the CLI](using-the-cli.md)).

## The developer loop

```bash
# Build.
lunch aosp_cf_x86_64_only_phone-trunk_staging-userdebug
m dist

# Push and boot in one command. --timeout bounds the whole run;
# size it for the upload plus the ~4 minute create.
cfctl device create --interaction interactive --name mybuild \
  --local-image out/dist/aosp_cf_x86_64_only_phone-img-*.zip \
  --local-image out/dist/cvd-host_package.tar.gz \
  --timeout 30m --wait
```

`--local-image` pushes the named files, registers them, and creates the
device from the resulting build. It is mutually exclusive with
`--build-id` and `--track-*`, because the pushed build *is* the build. It
works with both creation modes: `--interaction` for automatic placement,
`--host` to target a specific host.

To separate the steps, pushing once and booting many times:

```bash
cfctl build push --dist-dir out/dist --timeout 30m
# (equivalent to naming the images zip and cvd-host_package.tar.gz yourself)
# → Build registered: aosp_cf_x86_64_only_phone-img-8912345 (BUILD_ID)

cfctl device create --interaction headless --name shard-1 --build-id BUILD_ID
cfctl device create --interaction headless --name shard-2 --build-id BUILD_ID
cfctl build list
```

Iterating is cheaper than the first push: `cvd-host_package.tar.gz` rarely
changes between rebuilds of the same tree, so its transfer is skipped, and
only the changed images zip uploads. A re-push with **no** changed files
just returns the existing build.

### Sharing a running build

Open the device in the console (**Devices → your device**) or jump straight
to the player with `cfctl device webrtc <id>`, and share that URL. Anyone on
the deployment's IAP access list sees your build running, with no checkout,
no artifacts and no setup.

## CI patterns

Two shapes, depending on where the pipeline's artifacts live:

- **Artifacts already land in GCS** (the common case): skip pushing
  entirely. Register the build by URL (`POST /v2/builds` with `source:
  "url"` and one `artifact_specs` entry per role) and create devices from
  the returned ID. No bytes move twice.
- **Artifacts only exist on the runner**: `cfctl build push` in a pipeline
  step, then create per shard from the printed build ID.

Either way the fingerprint makes concurrent registration safe: N shards
racing to register the same build all converge on one row. See
[CI/CD Integration](cicd-integration.md) for authenticating pipelines.

## Artifact rules

Registration validates the artifact set against what the host's fetcher can
actually consume, so an unbootable set fails immediately with the reason,
instead of minutes later inside a launch:

| Rule | Why |
|------|-----|
| Exactly one `host_package`, ending `.tar.gz` | The host tools have their own extraction path; without them nothing can launch |
| Exactly one `default_build`, ending `.zip` | The images zip is extracted in full; tar archives are not extracted for target artifacts |
| No duplicate roles | Later entries would silently win |
| `kernel_build`, `boot_build`, `bootloader_build`, `system_image_build`: a `.zip` (optionally with a `{file}` selector) or a plain file | Mirrors the fetcher: `{file}` extracts a single member from a zip; tar-family is refused |
| `https://` allowed, `http://` refused | Cleartext transport for boot images invites tampering; the host refuses it too |

`cfctl build push` infers roles from filenames (`*-img-*.zip` →
`default_build`, `cvd-host_package.tar.gz` → `host_package`) and refuses
files it cannot classify. Sets with the additional roles are registered
through the API or the console's build form, where every role is explicit.

## Retention

Staged artifacts live under the `staging/` prefix of the artifacts bucket
with their own lifecycle (`staging_retention_days`, default 180), separate
from device captures, which expire after `artifacts_retention_days`
(default 30). A build whose staged artifacts have expired fails at device
create; re-pushing the same files restores them under the same
content-addressed paths and the same build.

## Troubleshooting

| Symptom | Meaning |
|---------|---------|
| `already staged, skipping upload` | Expected: that exact content is present; nothing to transfer |
| Push interrupted mid-file | Transient failures resume from the server-confirmed offset within the run. If the command itself dies, re-run it: completed files skip, the interrupted file starts over on a fresh session |
| `cannot infer a role for …` | Push exactly the images zip and host package; register other roles via the API or console |
| Registration returns 400 | The artifact set violates a rule above; the message names which |
| Device create reaches `error` with no artifacts fetched | The host image's `cvd` predates `gs://` build sources, so it discarded the URL and found no images. See Availability |
| Push seems to hang on a large file without `--timeout` | It doesn't; the transfer itself is unbounded. Pass `--timeout` anyway so the *whole* command is bounded and `--wait` can't outlive your patience |
