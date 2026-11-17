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

# Creating Devices

This guide explains how to create and manage Cuttlefish Virtual Devices (CVDs) in Cloud Cuttlefish Orchestrator.

## Overview

A device is a virtual Android instance running on a host VM. CCO supports two creation modes:

| Mode | Use Case |
|------|----------|
| **Simple** | An interview: answer what you need, the platform resolves the rest |
| **Advanced** | Full control over instances, CPU, memory, displays, and configuration |

There are **no prerequisites** for the simple path: with an empty project,
the platform registers the latest AOSP phone build for you and provisions a
host automatically. Registering builds and creating hosts up front remains
fully supported for operators who manage builds and hosts explicitly.

## Simple Mode (the interview)

### Using the Web Console

1. Navigate to **Devices** in the sidebar
2. Click **Create Device**. A chooser offers three cards: **Simple**,
   **Advanced**, and **AAOS SDV Core + IVI**, which opens Advanced with an
   SDV Core and IVI pair already set up
3. Pick **Simple** and answer the four questions:

   **Step 1: Android**
   - The platform default build is preselected and stated ("Using ..., the
     platform default build"); click **Choose different** to pick another
     registered build
   - With an empty registry, the statement says the latest AOSP phone build
     (`aosp-android-latest-release`) will be registered for you
   - The device name is pre-filled with a valid generated name
   - **Host** defaults to **Automatic (recommended)**, where the platform
     places the device on existing capacity or provisions a host. **Choose a
     host** shows the picker (a chosen host requires a registered build)

   **Step 2: Usage**
   - **Interactive** (browser view & control) prefers a GPU host and falls
     back to software rendering when none is available
   - **Headless** (ADB / automation) uses plain compute capacity

   **Step 3: Duration**
   - Session TTL (1-8 hours, default 4)
   - Optionally pin the device (requires a reason) to exempt it from
     automatic cleanup

   **Step 4: Plan**
   - A server-resolved dry run shows exactly what launch will do: which
     build (use or register), which host (place on existing capacity, or
     provision a machine type in a capacity-ranked zone, with the ranking
     reasons), plus any warnings, before anything is created
   - Click **Launch Device**

While the device comes up on a provisioning host, its detail page shows
"Waiting for host …" with the host's status and a link to it.

### Using the CLI

```bash
# Declarative: the platform resolves build and placement
cfctl device create --name my-device --interaction interactive --wait

# Preview the plan without creating anything (AIP-163 validate_only)
cfctl device create --name my-device --interaction headless --dry-run

# Track the latest build on a specific branch
cfctl device create --name my-device --interaction headless \
  --track-branch aosp-android-latest-release \
  --track-target aosp_cf_x86_64_only_phone-userdebug

# Prefer a region for placement (a preference, never a constraint)
cfctl device create --name my-device --interaction interactive --region us-central1

# Imperative: a specific host and registered build
cfctl device create \
  --host HOST_ID \
  --name my-device \
  --build-id BUILD_ID
```

## Advanced Mode

Advanced mode provides full control over device configuration.

### When to Use Advanced Mode

| Requirement | Simple | Advanced |
|-------------|:------:|:--------:|
| Basic device with defaults | Yes | Yes |
| Custom CPU/memory allocation | - | Yes |
| Multiple displays | - | Yes |
| Multiple device instances | - | Yes |
| Automotive/TV/Foldable form factors | - | Yes |
| Custom boot options | - | Yes |
| Shareable configuration files | - | Yes |

Each mode keeps its own draft, so leaving a mode and coming back resumes
where you left off. **Change mode** in the page header returns to the
chooser. From Simple, **Continue in Advanced** in the header, or **Customize
in Advanced** on the Plan step, opens Advanced seeded with the interview
answers: the device name, one phone instance with rendering derived from
usage, and the build, host and lifecycle choices. Nothing carries back from
Advanced to Simple. Advanced has its own **Device name** field, which names
the whole device. Each instance has a separate name.

Advanced placement mirrors the interview: **Automatic** placement submits
the same declarative request; choosing a host requires a build. Under
automatic placement, **Capacity** picks the provisioning model of a host the
create provisions: **Auto** (operator policy decides), **On-demand**, or
**Spot**. It is sent as `requirements.capacity` and is hidden for a chosen
host.
Lifecycle (TTL, pinning, tags) is a first-class section.

### Builds in Advanced mode

The **Builds** section under the instance cards is the only place Advanced
chooses builds. Each build field lists the registered builds and also takes a
pasted `gs://` or `https://` link to an image zip, a `gs://` build folder
(ending in `/`), or an `@ab/` Android Build reference. For a folder, cvd on
the host finds the image zip and the host package inside it. Registering a
build first is optional.

**Browse** next to each build field opens the Cloud Storage browser. Pick an
image zip, or open a build folder and choose **Select this folder**. The
browser opens where another build of the device lives, or at the bucket list
when there is none; the back arrow at the top level returns to the bucket
list to switch bucket.

| Setting | What is sent |
|---------|--------------|
| **Same build for every instance** on, registered build | the request's `build_id` |
| On, pasted URL or `@ab/` reference | `disk.default_build` on every instance |
| On, platform default (automatic placement only) | no build; the platform picks its default |
| Off, registered build on a row | that instance's `@build` |
| Off, pasted URL or `@ab/` reference on a row | that instance's `disk.default_build` |

With the switch off, every instance needs its own build before **Create** is
enabled. Turning the switch off copies the shared build into every row.
Turning it on keeps the first instance's build. The switch is hidden while the
device has one instance.

**Host tools** takes the URL of a `cvd-host_package.tar.gz` (`gs://` or
`https://`) and is sent as `common.host_package`; its **Browse** picks a
file. It is optional unless an SDV pair has no registered build anywhere and
its first instance's build is not a `gs://` folder, because an image zip
carries no host tools. With a folder as the first instance's build, cvd takes
the host package from that folder. The **AAOS SDV Core + IVI** preset starts
with the switch off: the `core` row expects an `sdv_core_cf` image or folder,
the `ivi` row an `sdv_ivi_cf` one, and the IVI build's host package is the
usual choice for host tools. The preset also sets **Capacity** to
**On-demand**, because a Spot preemption stops both guests mid-session.

### Advanced Mode Features

**Device Templates**
- Phone (default)
- Tablet
- Foldable
- TV
- Automotive
- Wear OS
- Custom

**Resource Configuration**
- CPU cores
- Memory allocation
- Display resolution and DPI
- Multiple displays

**Multi-Instance**
- Create multiple devices with one configuration
- Each instance has unique settings

### Using the JSON Editor

Advanced mode includes a JSON editor for direct configuration:

```json
{
  "instances": [
    {
      "name": "phone-1",
      "template": "phone",
      "cpu": 4,
      "memory_mb": 4096,
      "displays": [
        {
          "width": 1080,
          "height": 2400,
          "dpi": 420
        }
      ]
    }
  ]
}
```

### Import/Export

Save and share configurations:

1. Configure your device settings
2. Click **Export** to download JSON
3. Share the file or save for later
4. Use **Import** to load a saved configuration

## Build Sources

### Tracking vs pinned builds

A build registered with only a **branch** (no build ID) *tracks latest*: it
resolves to the newest build on that branch at every device launch. The
Builds page marks these with a "Tracks latest" chip. A build registered
with a specific **build ID** is pinned to it. The auto-registered platform
default is a tracking build on `aosp-android-latest-release`.

### Android CI (ci.android.com)

Use official Android builds:

1. Go to [ci.android.com](https://ci.android.com)
2. Find your target build (e.g., `aosp_cf_x86_64_only_phone-userdebug`)
3. Copy the build ID (or register branch-only to track latest)
4. Enter in CCO with the build target

### Custom URLs (registration only, not yet launchable)

Builds can be **registered** from your own artifact URLs (GCS `gs://…` or
HTTPS), including per-role compositions, but **devices cannot launch from
URL-source builds yet**: the on-host `cvd` fetcher only understands Android
Build API references today, so device creation rejects URL-source builds at
admission with a clear 400 (and the console disables their launch actions).
URL launch support arrives with the upstream `cvd` GCS/HTTPS fetch work;
until then, launchable builds are Android CI references (a pinned build ID
or a tracking branch/target).

## GPU Modes

| Mode | Description | When to Use |
|------|-------------|-------------|
| `auto` | Rendering matches the host's hardware | The default, almost always right |
| `gfxstream` | NVIDIA GPU hardware rendering | Force hardware rendering (GPU hosts only) |
| `drm_virgl` | CPU-based OpenGL | Hosts without GPU |
| `swiftshader` | Software rendering | Testing, development |

In the interview, rendering is derived from the Usage answer instead of
being asked directly.

## Device Lifecycle

### Session Duration (TTL)

Devices are reclaimed after their TTL expires to free resources:

| Setting | Description |
|---------|-------------|
| **1-8 hours** | Session length (default 4h) |
| **Pinned** | Exempt from expiry; requires a reason (e.g. overnight test, demo preparation) |

TTL can be extended from the device's lifecycle timer in the console while
it is running.

### Device States

| State | Description |
|-------|-------------|
| `pending` | Accepted; waiting to start (e.g. its host is still provisioning) |
| `creating` | Device is being provisioned on its host |
| `running` | Device is active and connectable |
| `stopping` | Device is shutting down |
| `stopped` | Device is stopped but can be restarted |
| `deleting` / `deleted` | Teardown in progress / complete |
| `error` | Device creation or operation failed |

## Connecting to Devices

Once a device is running:

1. Click the play icon on the device row (or **Open player** on the detail
   page's connection card) to open the integrated streaming player
2. Interact using touch and keyboard; the ADB tool panels (shell, files,
   apps, logcat) live in the player

Or use the CLI:
```bash
cfctl device connect DEVICE_ID   # tunnel ADB to a local port (stays running)
cfctl device webrtc DEVICE_ID    # open the stream in a browser
```

## Managing Devices

### Stop a Device

Preserve the device but free host resources:

```bash
cfctl device stop DEVICE_ID
```

### Resume a Stopped Device

```bash
cfctl device start DEVICE_ID
```

### Restart, Powerwash, Power Button

```bash
cfctl device restart DEVICE_ID     # relaunch the CVD in place
cfctl device powerwash DEVICE_ID   # reset to first-boot state (wipes userdata)
cfctl device powerbtn DEVICE_ID    # press the power button
```

### Snapshots

A snapshot captures a running device's full guest state, RAM and disk, so
the device can later be restored to that exact moment: logged in, app
installed, test fixture loaded.

```bash
cfctl device snapshot take DEVICE_ID --wait      # prints the snapshot id
cfctl device stop DEVICE_ID
cfctl device snapshot restore DEVICE_ID SNAPSHOT_ID --wait
```

The Snapshots card on the console's device page lists a device's snapshots
and deletes them. Take and restore are in cfctl and the API only, because a
restored device never serves its screen again. What to know before relying on
them:

- **Snapshots are host-pinned.** The data lives on the device's host VM and
  does not survive the host's disk: a host re-provisioned after an error,
  recovered from Spot preemption, or expired takes its snapshots with it. Such
  entries stay listed as `lost` so the disappearance is visible. There is no
  cross-host restore and no create-from-snapshot.
- **Restore targets the same stopped device.** Stop the device first; restore
  starts it from the snapshot instead of a cold boot, replacing whatever state
  it had.
- **A restored device serves ADB, not video.** The upstream streamer has no
  restore path: the resumed guest keeps pushing frames at its pre-snapshot
  streaming session, so screenshots, screen recordings and the console player
  do not work on a restored device (verified live: the pipeline never
  recovers on its own). ADB and every lifecycle verb work normally. To get
  the display back, restart the device. That reboots Android and gives up
  the resumed in-memory state, keeping the disk state. This makes restore
  best suited to ADB-driven (CI) workloads.
- **Software rendering only.** Taking a snapshot requires `gpu_mode`
  `swiftshader`. Every other mode, including `auto`, is refused at
  admission. A capture in any accelerated mode kills the virtual machine
  monitor mid-take and the device with it, and `auto` resolves on the host
  from a candidate list the guest image itself can supply, so nothing the
  platform holds makes it predictable.
- **Each device holds at most three.** A snapshot is roughly guest RAM plus
  disk overlays on the host's boot disk; delete old ones to take more.
- The device is briefly suspended while the capture runs (streams freeze for
  a few seconds) and keeps running afterwards.
- **A failed take can take the device with it.** The capture sequence is
  suspend → snapshot → resume; when a leg fails, the platform restarts the
  device defensively. If that restart is refused too, which is the shape of a
  capture that killed the launcher, the device is marked `error` with the
  reason instead of being left reading `running`. Restart the device, or delete it if
  the restart fails; a device the platform later observes alive settles back
  to `running` on its own.
- **Snapshot-capable devices run without the crosvm sandbox.** The virtual
  machine monitor cannot suspend a virtio-fs shared folder, and upstream
  enables that folder, riding the sandbox flag, under exactly the one
  rendering mode a snapshot can capture. The platform launches `swiftshader`
  devices with `enable_sandbox=false`, fills the same field (and the renderer)
  into an `env_config` that leaves them out, and marks every host so that a
  stop/start re-assembly keeps the sandbox off too. Only an explicit
  `vm.crosvm.enable_sandbox: true` in your `env_config` survives, and a take
  on such a device is refused with the reason instead of being attempted. An
  explicit `enable_sandbox: true` may also fail to launch at all on the
  platform's hosts.
- **The take verifies the launch that is actually running.** Before
  suspending, the platform reads the device's own launcher log on its host; a
  launch it cannot capture, or cannot verify, is refused with an
  explanation instead of killing the guest. If a take is refused after the
  device was stopped and started, stop and start it again and retry: the
  relaunch re-assembles on the host's snapshot-safe defaults.
- **`swiftshader` devices are placed on CPU hosts.** Software rendering never
  needs an accelerator, and a host without one is what keeps the device
  capturable across restarts. `--gpu-required` therefore conflicts with
  `--gpu-mode swiftshader` and the create is refused.

### Delete a Device

Permanently remove a device (its snapshots are removed with it):

```bash
cfctl device delete DEVICE_ID
```

## Troubleshooting

### Device Creation Fails

| Symptom | Cause | Solution |
|---------|-------|----------|
| "quota exceeded" | Host quota is exhausted | Delete unused hosts/devices or raise the quota |
| "a build is required" | Imperative create without a build | Pass `--build-id`, or use `--interaction` for a platform-resolved build |
| No host has capacity | All hosts full | Use Automatic placement; the platform provisions one |
| "Build not found" | Invalid build ID | Verify the build ID on ci.android.com |

### Device Won't Connect

| Symptom | Cause | Solution |
|---------|-------|----------|
| Black screen | WebRTC blocked | Check firewall allows UDP |
| Connection timeout | Signaling issue | Refresh and try again |
| "Device not ready" | Still starting | Wait for "running" state |

## Device Fleets

A batch admits many devices as one unit, with all-or-nothing (`atomic`)
semantics, idempotent retry, and a members listing with ADB serials.

In the console, **Batches → Create batch** runs the same four-step interview
the device wizard uses: template, usage, shape, then a `validate_only` plan
showing how many devices fit, how many hosts that provisions and which zone
they land in, before anything is committed. For CI, `cfctl batch create` takes
the same spec in one command; see the
[CLI Reference](../reference/cli.md#batches).

## Next Steps

- [Managing Hosts](managing-hosts.md) - Host VM lifecycle
- [Using the CLI](using-the-cli.md) - Command-line workflows
- [WebRTC Streaming](../concepts/webrtc-streaming.md) - How streaming works
