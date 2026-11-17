# cfctl for cvdr Users

This guide is for people who already use `cvdr`, the command-line client of
the upstream
[cloud-android-orchestration](https://github.com/google/cloud-android-orchestration)
project, and want to know what the same work looks like with `cfctl`.

## Both tools keep working

CCO serves two APIs, and each client talks to one of them.

| Client | API | Reference |
|---|---|---|
| `cvdr` | The legacy `/v1` API, byte-compatible with upstream Cloud Orchestrator | [Legacy API compatibility](../reference/legacy-api.md) |
| `cfctl` | The native `/v2` API | [CLI Reference](../reference/cli.md) |

Nobody has to switch. A `cvdr` setup that works today keeps working against
the same deployment, and the
[Quick Start](../getting-started/quick-start.md#7-cli-access-a-service-account-for-cfctl-and-cvdr)
sets up one service account that both tools can use. Move a workflow to
`cfctl` when you want something from the list at the end of this page.

## Terms

| cvdr | cfctl | What it is |
|---|---|---|
| CVD group | device | One `cvd` launch on a host. It has an ID, a build and a lifecycle |
| CVD | instance | One Android guest inside it. For a multi-instance device, `cfctl device get DEVICE` lists the instance names |
| host | host | A Compute Engine VM that runs the Host Orchestrator and the CVDs |

A `cvdr` command names a CVD by `--host`, `--group` and `--name`. A `cfctl`
command names a device by its ID or its name alone, and adds
`--instance NAME` where a command acts on one instance of a multi-instance
device. A name must match exactly one device that is not deleted: several
matches exit 5 with a table of them, and none exits 4. Hosts work the same
way.

## Global flags

| cvdr | cfctl |
|---|---|
| `--service_url URL` | `cfctl config create NAME --server URL` once, or `--server URL` or `CFCTL_SERVER` per command |
| `--zone ZONE` | No global zone. `host create --zone` pins a host, `device create --region` states a preference, `host list --zone` filters. Without them the server ranks placement across the zones the operator enabled |
| `--proxy URL` | Set `HTTPS_PROXY` in the environment. cfctl has no proxy flag |
| `-s NAME` (named service) | `--profile NAME` or `CFCTL_PROFILE=NAME`. `cfctl config use NAME` sets the default |
| `-v`, `--verbose` | `-v`, `--verbose` |

## Commands

In this table, `DEVICE` is a cfctl device ID or name and `HOST` a cfctl
host ID or name. `cfctl device list` and `cfctl host list` print both.

| cvdr | cfctl |
|---|---|
| `cvdr create` | `cfctl device create --wait` |
| `cvdr create --branch B --build_target T` | `cfctl device create --track-branch B --track-target T --wait` |
| `cvdr create --build_id N --build_target T` | `cfctl device create --build-id N --build-target T --wait` |
| `cvdr create --local_image` | `cfctl device create --product-out --wait` |
| `cvdr create --local_images_zip_src Z --local_cvd_host_pkg_src P` | `cfctl device create --local-image Z --local-image P --wait` |
| `cvdr create --num_instances N` | `cfctl device create --instances N --wait` |
| `cvdr create config.json` | `cfctl device create --env-config config.json --wait` |
| `cvdr create --host H` | `cfctl device create --host HOST --wait` |
| `cvdr create --host_gcp_machine_type M` | `cfctl host create --name NAME --machine-type M --wait`, then `cfctl device create --host NAME` |
| `cvdr create` (auto-connects by default) | `cfctl device create --connect` |
| `cvdr create --auto_connect=false` | `cfctl device create --wait` |
| `cvdr list` | `cfctl device list` |
| `cvdr list --host H` | `cfctl device list --host HOST` |
| `cvdr delete --host H GROUP` | `cfctl device delete DEVICE` |
| `cvdr stop --host H --group G --name I` | `cfctl device stop DEVICE --wait` |
| `cvdr start --host H --group G --name I` | `cfctl device start DEVICE --wait` |
| `cvdr snapshot --host H --group G --name I` | `cfctl device snapshot take DEVICE --wait` |
| `cvdr start ... --snapshot_id S` | `cfctl device snapshot restore DEVICE S --wait` |
| `cvdr reset --host H` | No counterpart. See below |
| `cvdr bugreport --host H --group G` | `cfctl device bugreport DEVICE` |
| `cvdr connect --host H --group G I` | `cfctl device connect DEVICE --background --instance I` |
| `cvdr connect` for every instance of a group | `cfctl device connect DEVICE --background --all-instances` |
| `cvdr disconnect --host H --group G I` | `cfctl device disconnect DEVICE --instance I` |
| `cvdr disconnect` for every connection | `cfctl device disconnect --all` |
| Connection status in `cvdr list` | `cfctl device connections` |
| `cvdr host create` | `cfctl host create --name NAME --wait` |
| `cvdr host create --gcp_machine_type M --gcp_min_cpu_platform P --gcp_boot_disk_size_gb G` | `cfctl host create --name NAME --machine-type M --min-cpu-platform P --boot-disk-size-gb G` |
| `cvdr host create --gcp_accelerator type=T,count=N` | `cfctl host create --name NAME --gpu-type T --gpu-count N` |
| `cvdr host list` | `cfctl host list` |
| `cvdr host delete H1 H2` | `cfctl host delete HOST`, once per host |
| No counterpart | `cfctl host extend HOST --hours N` adds to the lease of a host you created |

Some rows behave differently in the two tools.

- **Placement.** `cvdr create` without `--host` creates a new host for every
  group. `cfctl device create` without `--host` places the device on an
  existing host with room, and provisions a host only when none fits.
- **Build defaults.** Without a build flag, `cvdr` uses `aosp-main` and its
  default target. `cfctl` uses the build the operator registered as the
  default. A numeric `--build-id` is an Android CI build ID; cfctl registers
  it in the build registry first and prints the registry ID it got. The
  registry ID works in later commands. `cfctl build register --build-id N
  --target T` registers one without creating a device.
- **Local images.** `--product-out` reads `$ANDROID_PRODUCT_OUT` and
  `$ANDROID_HOST_OUT/cvd-host_package.tar.gz` the way `cvdr --local_image`
  does. Name a directory with `--product-out=DIR` and a package with
  `--host-package FILE`. The push skips content that is already staged.
  [Testing Local Builds](local-builds.md) covers the whole workflow.
- **Instances.** `cvdr stop` and `cvdr start` act on one CVD. `cfctl device
  stop` and `cfctl device start` act on the whole device, every instance at
  once.
- **Connections.** `cvdr create` connects ADB unless told not to. `cfctl
  device create` connects only with `--connect`. A background connection
  runs as a separate `cfctl` process that tunnels ADB through the
  orchestrator, so it needs no route to the host. Its state lives in
  `connections/` under the cfctl config directory, and it keeps the profile
  it started with. Stop it with `cfctl device disconnect` when you
  delete the device. Without `--background`, `cfctl device connect` holds
  the tunnel in the foreground until you press Ctrl-C.
- **Bugreports.** `cfctl device bugreport` stores the report in the
  deployment's artifact bucket and downloads it. `--include-adb` adds a full
  on-device `adb bugreport`.

## No direct counterpart

| cvdr | Why, and what to do instead |
|---|---|
| `--proxy` | cfctl uses Go's standard proxy settings. Set `HTTPS_PROXY` (and `NO_PROXY` if needed) |
| `reset --host H` | cfctl has no command that wipes every CVD on a host. Delete the host's devices with `cfctl device delete`, or delete the host with `cfctl host delete HOST` |
| The acloud config import on first run | cfctl has its own config file of named profiles (`~/.config/cfctl/config.yaml` on Linux). `cfctl setup` writes a profile interactively, `cfctl config create` writes one without prompts, and `cfctl doctor` checks it |
| The WebRTC connect agent (`--connect_agent`, `--ice_config`) | cfctl tunnels ADB over one WebSocket through the orchestrator, so it needs no ICE configuration and no TURN server. `cfctl device webrtc DEVICE` opens the console's player for the screen |

## Using both tools on one deployment

The two APIs share hosts. Each tool manages only the CVDs it created.

- A CVD that `cvdr` created does not appear in `cfctl device list`, because
  CCO has no device record for it. `cfctl host cvds HOST` lists every CVD
  on a running host, cvdr's included. The DEVICE column shows `-` for a CVD
  that CCO did not create. The view is read-only: manage those CVDs with
  `cvdr`.
- `cfctl host list` shows every host, including the ones `cvdr` created.
  `cvdr host list` shows only the hosts created through `/v1`.
- A host that `cvdr` created is fenced from automatic placement, so a
  `cfctl device create` without `--host` never lands on it.
- `cvdr` cannot create a CVD on a host that CCO provisioned for `cfctl`
  devices. The server refuses it with `403`.
- A snapshot `cvdr snapshot` takes does not appear in `cfctl device
  snapshot list`. It stays usable through `cvdr start --snapshot_id`.

Give each tool its own hosts.

## What cfctl adds

- `--json` on every command, with progress and hints on stderr.
- `--wait` on create, start, stop and delete, bounded by `--timeout`, with
  [exit codes](../reference/cli.md#exit-codes) a script can branch on.
- `--idempotency-key` on creates, so a retried CI step returns the device
  it already made.
- Batches: `cfctl batch create --prefix P --count N` gets a fleet for one
  CI run, and `cfctl batch devices ID` prints their ADB serials.
- Dry runs: `cfctl device create --dry-run` prints the placement and build
  plan without creating anything.
- A build registry: `cfctl build push`, `cfctl build register` and
  `cfctl build list`, with a default build per deployment.
- `cfctl setup` to configure a workstation and `cfctl doctor` to diagnose
  connectivity, auth, quota and storage access.

## See also

- [Using the CLI](using-the-cli.md), the cfctl walkthrough
- [CLI Reference](../reference/cli.md), every cfctl command
- [Replacing Cloud Orchestrator](../getting-started/replacing-cloud-orchestrator.md),
  the server-side migration for cvdr users
