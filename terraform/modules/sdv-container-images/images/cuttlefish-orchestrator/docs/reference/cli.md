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

# cfctl - CCO Command-Line Interface

## Overview

`cfctl` manages Cloud Cuttlefish Orchestrator resources from the terminal.
It authenticates with **Google ID tokens** sent as `Authorization: Bearer`
to the IAP-protected Cloud Run service. The calling identity (your user
account via impersonation, a service account, or a WIF-federated CI
identity) must be on the deployment's **IAP access list**. That list is
the entire access model, and there are no roles.

## Installation

```bash
go build -o cfctl ./cmd/cfctl
# or
go install ./cmd/cfctl
```

## Quick Start

```bash
# 1. Sign in, then save the deployment and identity as a profile
gcloud auth application-default login
cfctl config create prod --server https://cuttlefish-orchestrator-xxxx.a.run.app \
  --impersonate cco-cli@PROJECT.iam.gserviceaccount.com
#    No audience needed: cfctl signs a self-signed JWT (aud = service URL) that
#    works with the managed IAP client. Set an audience only for a custom IAP
#    OAuth client (see Authentication).

# 2. Verify (the SA must be on iap_members)
cfctl auth whoami
cfctl host list
```

`cfctl setup [key.json] [--profile NAME]` walks through the same
configuration interactively.

## Configuration

The config file holds named profiles. Each is one deployment and how to
authenticate to it. It lives in `cfctl/config.yaml` under the user config
directory (`~/.config` on Linux, `~/Library/Application Support` on macOS,
`%AppData%` on Windows), or under `$CFCTL_CONFIG_DIR`, which moves the token
cache, the completion cache and the ADB connection state with it.

A command uses the profile named by `--profile`, else `CFCTL_PROFILE`, else
the stored `current_profile`. An empty value means unset, and an unknown name
is an error. The `set-*` and `clear-*` commands change the selected profile;
with none selected they write a profile named `default` and make it current.

| Command | Description |
|---------|-------------|
| `cfctl config show` | Print each effective setting and its source (`flag`, `env`, `profile`, `unset`) |
| `cfctl config profiles` | List profiles; `*` marks the current one |
| `cfctl config use <name>` | Store `<name>` as `current_profile` |
| `cfctl config create <name> --server <url> [--impersonate SA \| --credentials KEY] [--audience ID] [--auth none]` | Create a profile without prompts. The server comes from `--server` only. The first profile becomes current |
| `cfctl config delete <name>` | Delete a profile |
| `cfctl config rename <old> <new>` | Rename a profile, keeping it current if it was |
| `cfctl config set-server <url>` | Orchestrator URL (run.app) |
| `cfctl config set-audience <client-id>` | OIDC-token audience, optional. Leave unset for the default self-signed JWT (managed IAP client). Set only for a custom IAP OAuth client |
| `cfctl config set-auth <auto\|none>` | Client auth mode. `none` sends no credentials (for a network-mode deployment, where the VPC is the gate); `auto` (the default) uses the credential ladder |
| `cfctl config set-credentials <path>` | Use a service-account key file |
| `cfctl config clear-credentials` | Stop using the key file |
| `cfctl config set-impersonate <sa-email>` | Impersonate a service account with your own credentials |
| `cfctl config clear-impersonate` | Stop impersonating |

## Authentication

`cfctl` authenticates a **service account** and sends a `Bearer` token. With no
audience configured (the default on a Google-managed IAP deployment) it signs a
**self-signed JWT** with `aud =` the service URL, so no custom OAuth client is
needed. When an audience *is* set, it sends an **OIDC token** with that audience
(a custom IAP OAuth client ID).

The signing identity comes from the first available source:

1. **Service-account impersonation** (`set-impersonate`): your ADC credentials
   sign as the target SA. Requires `roles/iam.serviceAccountTokenCreator` on it
   for the signing principal: your gcloud user, a key file's `client_email`,
   the account an `external_account` file impersonates, or on a Compute
   Engine VM with no user credentials, the VM's service account. `auth status`
   and `doctor` name it, and a refused signature prints the
   `gcloud iam service-accounts add-iam-policy-binding` command that grants it.
2. **Explicit key file** (`set-credentials`): signs locally.
3. **Application Default Credentials**: covers Workload Identity Federation in
   CI, attached service accounts on GCE/Cloud Run, and
   `GOOGLE_APPLICATION_CREDENTIALS`.

Whichever service account produces the token must be on the IAP access list
(`iap_members` in Terraform).

A self-signed JWT minted through impersonation is cached in
`token-cache.json` in the config directory (mode 0600) and reused while at least five
minutes of validity remain, so commands skip the IAM round trip.
`cfctl auth logout` deletes the cache.

The server URL must use `https`. Plain `http` is accepted only for a
loopback host (`localhost`, `127.0.0.0/8`, `::1`) or with
`cfctl config set-auth none`, where no credential is sent.

For CI/CD, prefer keyless WIF. See
[CI/CD Integration](../guides/cicd-integration.md).

## Commands

### Naming a device or host

Every `<device>` argument and every `<host>` argument or `--host` flag takes
an ID or a name. An argument shaped like a UUID is an ID and is used as it
is; the server refuses UUID-shaped names at create, so the two never clash.
Any other argument is a name, which cfctl looks up with the list endpoint's
exact-match `name` filter. Deleted devices and hosts keep no name.

| Matches | Result |
| --- | --- |
| One | That device or host is used |
| Several | Exit 5, with a table of the matches (ID, HOST or ZONE, STATUS, CREATED BY, CREATED) on stderr. Pass an ID |
| None | Exit 4 |

There is no prompt and no tie-break, so a script gets the same answer every
time. Batch members are named `PREFIX-NNN`, so two batches with one prefix
share names. `device disconnect` and `device connections` never call the
server: `device disconnect` matches a name against its local connection
records instead. Background agents stay keyed by device ID.

Shell completion offers device and host names, and profile names for
`--profile` and for `config use`, `config delete` and `config rename`.

### Authentication

```bash
cfctl auth whoami        # Verify authentication; prints your email
cfctl auth status        # Profile, method, credential and signing principal
```

### Hosts

```bash
cfctl host list [--zone ZONE] [--status STATUS] [--mine | --created-by EMAIL]
cfctl host get <host>
cfctl host create --name NAME [--zone ZONE] [--machine-type TYPE] [--gpu-type TYPE] \
  [--gpu-count N] [--min-cpu-platform PLATFORM] [--boot-disk-size-gb GB] \
  [--max-devices N] [--ttl-hours N] [--exclusive] [--wait]
cfctl host delete <host> [--force] [--wait]
cfctl host start <host> [--wait]
cfctl host stop <host> [--wait]
cfctl host extend <host> --hours N   # Add N hours (1 to 168) to a reserved host's lease
cfctl host ssh <host> [--no-tunnel] [-- CMD...]   # SSH via gcloud, tunneled through IAP
cfctl host cvds <host>   # Every CVD on the host, including ones cvdr created
```

`host list` follows the server's page token, so a host created or deleted
while it pages never pushes another host out of the list.

`host extend` counts the new lease from the later of now and the current
expiry, so it never shortens a lease. Only a reserved host takes it: one a
user created, outside a batch. Fleet, dedicated and batch hosts have leases
the platform manages, so that refusal exits 5 and names `cfctl device extend`
and `cfctl device pin`. A host protected by pinned devices has no expiry, and
a deleting or deleted host has no lease left; both exit 5. A lease that would
pass the time Compute Engine stops the VM also exits 5; a host restart resets
that time. A missing host exits 4, and `--hours` outside
1 to 168 exits 1. Repeated extends have no cumulative cap.

`host cvds` asks the host's Host Orchestrator live, so the host must be
running. The DEVICE column names the CCO device that owns each CVD, or `-`
for a CVD that CCO did not create.

Omitting `--machine-type` uses the catalog default the operator set. A
remote command after `--` runs with each argument single-quoted, so write
`-- sh -c 'CMD'` when it needs pipes or globs.

**`host ssh` tunnels through IAP TCP forwarding by default.** It is the only
route that works from outside the VPC, since hosts never expose port 22 to
the internet in any topology. It runs as *you* (gcloud's account, not
cfctl's service account) and needs: gcloud authenticated,
`roles/iap.tunnelResourceAccessor` (the `cli_users` Terraform variable
grants it, scoped to port 22), read access to the instance
(`compute.instances.get`), and an SSH login path (OS Login or permission
to push a metadata key), which stay operator-granted. IAP disconnects
tunnels idle for an hour. `--no-tunnel` restores gcloud's direct dial for
callers inside the VPC or on the deployment's VPN.

**Zone is optional.** Omitting `--zone` lets the server rank placement across
the enabled zone catalog (capacity headroom, recent failures); naming a zone
**pins** the host to it, so placement uses it verbatim and automatic zone
failover never moves a pinned host. `--exclusive` fences the host from
automatic placement (only creates targeting it by ID land there).

### Devices

```bash
cfctl device list [--host HOST] [--status STATUS] [--mine | --created-by EMAIL]
cfctl device get <device>
cfctl device create [--name NAME] [--wait]   # declarative and headless by default
cfctl device create --host HOST [--name NAME] [--build-id ID] [--build-target TARGET] [--wait]
cfctl device create ... --local-image FILE --local-image FILE   # push local artifacts, then create
cfctl device create ... --product-out[=DIR] [--host-package FILE]   # pack a build tree, push, create
cfctl device create ... --instances N | --env-config FILE   # multi-instance device
cfctl device create ... --connect   # wait, then attach ADB to every instance in the background
cfctl device create --interaction MODE [--name NAME] [...]   # declarative (see below)
cfctl device delete <device> [--force] [--wait]
cfctl device start <device> [--wait]
cfctl device stop <device> [--wait]
cfctl device restart <device>      # Relaunch the CVD in place
cfctl device powerwash <device>    # Reset to first-boot state (wipes userdata)
cfctl device powerbtn <device>     # Press the device's power button
cfctl device screenshot <device> [-o FILE]
cfctl device connect <device> [--instance NAME] [--port N] [--no-attach]   # Tunnel ADB and attach adb
cfctl device connect <device> -- shell getprop ro.build.id   # Run one adb command and exit
cfctl device connect <device> --background [--instance NAME | --all-instances] [--port N] [--no-attach]
cfctl device disconnect [<device>] [--instance NAME] [--all]   # Stop background connections
cfctl device connections           # List background connections
cfctl device webrtc <device>       # Open the console player page in a browser
```

A device create has no machine-type flag. To run on an exact machine shape,
create the host first, then place the device on it by name:

```bash
cfctl host create --name cf-host-3 --machine-type TYPE [--exclusive] --wait
cfctl device create --host cf-host-3
```

Without `--name` a device gets a generated `cvd-XXXXXXXX` name, derived
from `--idempotency-key` when one is given. With neither `--host` nor
`--interaction` the create is declarative with `--interaction headless`, so
`cfctl device create --wait` is the shortest complete form.

`--build-id` takes a registry ID or an Android CI build ID. An Android CI
build ID needs `--build-target`; cfctl registers it first (as
`build register` does), prints `Registered Android CI build N as ID` on
stderr, and creates from the registry ID. `--dry-run` refuses it, because a
dry run commits nothing. `--instances N` (1 to 8) launches N identical
instances in one device; `--env-config FILE` describes them in full, and
the two are mutually exclusive. `--product-out` and `--host-package` work
as they do for `build push` (see Builds) and exclude `--local-image` and
`--dry-run`.

`device connect` tunnels the device's ADB stream through the orchestrator to a
local port and runs `adb connect` against it, so it needs no route to the
host. That is the only thing that works, since the host firewall admits the ADB
port from inside the VPC only, and in relay mode the host has no public
address at all. It runs in the foreground for as long as you want the
connection: the tunnel *is* the connection. The tunnel serves before
`adb connect` runs, and the command then polls `adb get-state` until it
answers `device`, for up to `--wait-online` (default 60s, 0 skips the wait,
independent of `--timeout`). A failed device stream dial (403, 404, 502)
ends the wait at once with that error. A wait that runs out exits 7 when the
device is running and 5 otherwise, naming the last adb state or tunnel
error. Only then does a passthrough command run; it exits with adb's exit
code, which can collide with cfctl's 3, 4 and 5. cfctl's own messages and
adb's connect and get-state output go to stderr, so stdout holds only the
passthrough output or the `--json` result. `--no-attach` opens and closes one
device stream to check access, then prints the local address instead of
invoking `adb`, and `--port` pins the port instead of taking a free one.

`--background` hands the tunnel to a detached `cfctl` agent. The agent runs
`adb connect` itself, waits up to `--wait-online` for `adb get-state` to
answer `device`, and records the result in its state file. The command
returns once that record says the device is online. A failed device stream
dial during that wait ends it at once, as in the foreground: the command
stops the agent and exits with the code of the dial's error. On a timeout
the agent keeps serving, and the error names its log, since only the agent
sees why its streams failed. `--all-instances` starts one agent per instance.

The agent keeps adb attached. Every 15 seconds, and right after the device
stream closes, it runs `adb get-state`. adb gives up on a TCP device after
about a minute of failed reconnects (a Cloud Run rollout, laptop sleep, an
IAP outage). When adb answers `not found` and the device still exists, the
agent runs `adb connect` again, and counts a reattach once adb knows the
address again. While the stream stays down, adb forgets the address after
each try, so no reattach is counted until the outage ends. It also re-attaches
after `adb kill-server` and undoes a manual `adb disconnect`, so use
`cfctl device disconnect` to stop it. A shell or logcat stream that is open
during an outage still ends, because each adb stream belongs to one
connection to the device's adbd; start it again after the reattach.
`device create --connect` does the same for every instance of the new
device after it is running, and with `--json` adds a `connections` array
to the printed device. Each agent keeps its state in `connections/` under
the config directory (a `.json` state file, a `.lock` it holds while alive,
and a `.log`). It is started with the parent's `--profile`, so a later
`config use` does not change the credentials a running agent uses. An
agent stops by itself once its device is being deleted or is gone. When the
device stream fails to open or carries no data, the agent waits 1s before
it dials again, doubling up to 30s, so adb redials slowly. It logs a repeated
error once, then a periodic count, and truncates its log past 1 MiB. `device
connections` lists the live agents (DEVICE, NAME, INSTANCE, ADDRESS, STATE,
REATTACHES, PID, SINCE) and removes the files of agents that died. STATE is
adb's last answer (`device`, `offline`, `unauthorized`, `not found`), or
`attaching`, `attach failed` or `not attached` (started with `--no-attach`).
`device disconnect` stops the agents for one device, one instance
(`--instance`), or all of them (`--all`), and exits 0 when none match. It
takes a device ID or the device name stored in the agents' records. A name
that records give to more than one device exits 5 and prints the matching
records on stderr; pass the ID instead. Neither command contacts the
server. `--background` refuses a passthrough adb command, `--all-instances`
needs `--background` and excludes `--instance`, and `--port` with
`--all-instances` works only on a single-instance device.

`--wait` blocks until the operation reaches its final state (running, stopped,
or gone) and, with `--json`, prints the final entity. Combine it with
`--timeout` (a duration, e.g. `--timeout 8m`) to bound it. Waits fail fast
when the resource lands in a terminal error state, with the backend's error
message.

List commands show everyone's resources by default (CI safety); `--mine`
scopes to your own, `--created-by` to another creator. Deleting a resource
someone else created prompts with a warning naming its creator.

#### Declarative create

With `--interaction` (instead of `--host`) the platform resolves everything:
the device is placed on an existing host with capacity or a new host is
provisioned from the operator's policy defaults in a capacity-ranked zone,
and the build resolves to the registered default (auto-registered from the
upstream AOSP default when none is registered).

```bash
# One command, zero prerequisites: place or provision, default build.
cfctl device create --name ci-dev --interaction headless --wait

# Interactive use prefers GPU hosts; --gpu-required fails instead of
# degrading to software rendering when no GPU capacity exists.
cfctl device create --name demo --interaction interactive --gpu-required

# Prefer a region. It is never a constraint: placement still lands wherever
# capacity exists. Dominant for interactive intent, a tie-break for headless.
cfctl device create --name demo --interaction interactive --region REGION

# Track the latest build on a branch/target, or use a registered build ID.
cfctl device create --name d1 --interaction headless \
  --track-branch aosp-android-latest-release \
  --track-target aosp_cf_x86_64_only_phone-userdebug

# Dry run: validate and print the plan (build/host actions, provisioning
# shape, the chosen zone with its placement reasons, warnings) without
# creating anything.
cfctl device create --name d1 --interaction headless --dry-run
```

A device created against a still-provisioning host sits in `creating` until
the host comes up (typically about a minute); `--wait` rides through this.
When a host is auto-provisioned, the chosen zone is initial placement:
provisioning may still fail over to another ranked zone on a capacity
failure.

### Device artifacts

```bash
cfctl device bugreport <device> [--include-adb] [-o FILE]  # Capture to the artifact bucket + download
cfctl device record start <device>
cfctl device record stop <device>
cfctl device record list <device>            # Host-local recordings (die with the host)
cfctl device record fetch <device> <name> [-o FILE]  # Archive to the bucket + download
```

### Device snapshots

```bash
cfctl device snapshot take <device> [--wait]           # Capture guest state onto the host
cfctl device snapshot list <device>
cfctl device snapshot restore <device> <snapshot> [--force] [--wait]  # Device must be stopped
cfctl device snapshot delete <device> <snapshot> [--force] [--wait]
```

A snapshot captures the running device's full guest state, RAM and disk,
onto its host's disk; restoring one starts the *stopped* device exactly where
the capture was taken. Snapshots are **host-pinned**: they die with the host's
disk (re-provision after an error, Spot preemption recovery, host expiry), and
the list shows such entries as `lost`. Taking one requires `gpu_mode`
`swiftshader`. Every other mode, `auto` included, is refused at admission
because a capture in an accelerated mode destroys the device. A failed take is
compensated with a defensive restart; if that restart is refused too, the
device is marked `error` with the reason. A device holds at most three. `take
--wait` prints the snapshot id; without `--wait` it appears in `snapshot list`
once the capture settles. A restored device serves **ADB and lifecycle verbs
only**. Screenshots and streaming stay dead until a restart, an upstream
limitation of the resumed guest's display pipeline (see
[Creating Devices](../guides/creating-devices.md)).

### Builds

```bash
cfctl build push FILE... [--name NAME] [--description TEXT] [--default]
cfctl build push --dist-dir DIR [...]   # find the artifacts in an `m dist` output
cfctl build push --product-out[=DIR] [--host-package FILE] [...]   # pack the images from a build tree
cfctl build register --build-id BUILD_ID --target TARGET [--branch BRANCH] \
  [--name NAME] [--description TEXT] [--default]   # register an Android CI build
cfctl build list
cfctl build update ID [--name N] [--description D] [--default] [--artifact ROLE=URL]...
```

`build push` stages local artifacts (the images zip and
`cvd-host_package.tar.gz` from `m dist`) content-addressed in the artifacts
bucket and registers them as a build; already-staged content is skipped and
interrupted transfers resume. `--dist-dir` finds the two artifacts in the
dist directory itself and fails if the zip match is missing or ambiguous.
`--product-out` packs the images a Cuttlefish launch needs from a product-out
directory (`DIR`, or `$ANDROID_PRODUCT_OUT` when no `DIR` is given) into an
images zip and pushes it with the host package. The host package defaults to
`$ANDROID_HOST_OUT/cvd-host_package.tar.gz`, which is refused when the
`cvd-host_package` directory beside it is missing or newer than the
tarball; `--host-package FILE` names one explicitly and is valid only with
`--product-out`. Named files, `--dist-dir` and `--product-out` are mutually
exclusive. `device create --local-image` and `device create --product-out`
compose push and create in one command. Pass `--timeout` sized for the
upload. Full workflow: [Testing Local Builds](../guides/local-builds.md).

`build register` records an Android CI build (from ci.android.com) in the
registry without uploading anything. The name defaults to
`TARGET-BUILD_ID`. Registering a build that is already registered returns
the existing record, and cfctl warns on stderr if its target differs.

### Batches

```bash
cfctl batch create --prefix ci --count 20 --interaction headless \
  [--min-ready N] [--fill-policy best_effort|atomic] [--placement isolated|pack] \
  [--ready-timeout SECONDS] [--ttl-hours N] [--gpu-required] \
  [--build-id ID | --track-branch BRANCH --track-target TARGET] [--dry-run] [--wait]
cfctl batch list [--mine | --created-by EMAIL]
cfctl batch get <id>
cfctl batch devices <id>       # Members with ADB serials (the CI payload)
cfctl batch delete <id>        # Tear down all members
```

An `Idempotency-Key` derived retry of `batch create` resumes the same batch
instead of provisioning a second fleet; `atomic` fails and tears down the
whole batch if a member permanently fails or the ready timeout lapses.

### Admin

```bash
cfctl admin tasks list                 # List dead-lettered tasks
cfctl admin tasks retry <task-id>      # Retry a dead-lettered task (rejected for tasks whose resource is already terminal; use the resource's retry instead)
cfctl admin tasks delete <task-id>     # Delete a task from the queue
cfctl admin tasks stats                # Task-queue depth (pending/leased/failed, oldest age)
cfctl admin stats                      # Platform statistics: host and device counts by status
cfctl admin provisioning               # Recent provisioning outcome counts
cfctl admin reclaimable                # Pinned, stopped and expiring resources to reclaim
cfctl admin audit [--actor EMAIL] [--action A] [--resource-type T] [--resource-id ID]
  [--since TIME] [--until TIME] [--limit N] [--offset N]   # Audit log, newest first
cfctl admin control-plane              # One-call snapshot of the machinery: queue, live tasks, leaders
cfctl admin capacity                   # Where the fleet can grow: zone verdicts, quota headroom, Spot capacity
cfctl admin zone list                  # Every zone Compute Engine offers, with the curated shapes each stocks
cfctl admin zone enable|disable ZONE   # Curate the placement space
cfctl admin machine-type update MT [--enabled|--no-enabled] [--default]   # Exclude one shape or elect the default; display order and GPU policy are set per machine family
cfctl admin catalog-freshness          # Catalog snapshot age + the scopes Compute Engine did not report
cfctl admin host-image list|get|create|update|delete   # Which GCE image family each machine family boots, and the image it resolves to now
cfctl admin config get [KEY]           # Read a platform setting (every setting when KEY is omitted)
cfctl admin config set KEY VALUE       # Write a scalar setting (zone-pin, quota-margin, v1-zone-hint, spot-auto-default, default-gpu-machine-type)
cfctl admin config set KEY FIELD=VALUE...  # Write a composite setting (default-upstream-build, spot-min-uptime, lifetimes, docker-host, artifact-buckets, sweeps, fleet); unnamed fields keep their current value
cfctl admin config set fleet ladder=n2-standard-8,n1-standard-8   # Machine shapes the CPU provisioning path prefers, most preferred first; ladder=none restores the catalog default
```

A composite `set` reads the setting, applies the named fields and writes it
back only if nobody changed it in between. When someone did, the command
fails with "the setting changed since it was read" and nothing is written;
run it again. A `fleet` set can issue several writes. If a later one fails,
the error lists the writes that were already applied.

Soft-deleted records need no manual purging: tombstones age out automatically
via the database's row-deletion policies (7 days).

### Storage

```bash
cfctl storage ls                     # List the artifact buckets
cfctl storage ls BUCKET [--prefix P] # List a bucket's objects, every page
cfctl storage rm BUCKET OBJECT       # Delete an object (confirms unless --force)
```

### Utilities

```bash
cfctl quota show              # Host-capacity quota and current usage
cfctl quota set MAX_HOSTS     # Set the platform host quota
cfctl status                  # Server health
cfctl completion bash|zsh|fish
cfctl --verbose ...           # Debug logging (including auth flow)
```

## Output Formats

Default output is a table; add `--json` for machine-readable output.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success, or a confirmation prompt was declined |
| 1 | Any other error |
| 3 | Authentication or permission failure (HTTP 401 or 403, no usable credential) |
| 4 | Not found (HTTP 404) |
| 5 | Conflict or failed precondition (HTTP 409 or 412) |
| 6 | A `--wait`, or a waited operation, ended in a failure state |
| 7 | Transient: HTTP 5xx or 429, a network error, or a timeout. Retrying may succeed |
| 80 | Usage error: unknown command or flag, missing argument, bad flag value |
| 130 | Interrupted by SIGINT or SIGTERM |

`cfctl doctor` exits 1 when any check fails. Its `images` check fails when an
enabled machine family cannot boot a host, and prints the server's reason. A
host image family resolves in the row's `--source-project`, else in the
deployment's `CCO_HOST_IMAGE_PROJECT`; there is no settable default image
project.

## Environment Variables

Each setting takes a flag first, then its variable, then the selected
profile. A variable set to an empty string clears the profile's value.

| Variable | Purpose |
|----------|---------|
| `CFCTL_PROFILE` | Profile to use (overridden by `--profile`; empty means unset) |
| `CFCTL_SERVER` | Orchestrator server URL (overridden by `--server`) |
| `CFCTL_AUDIENCE` | ID-token audience (see `config set-audience`) |
| `CFCTL_IMPERSONATE` | Service account to impersonate |
| `CFCTL_CREDENTIALS` | Service-account key file to sign with |
| `CFCTL_AUTH_NONE` | `true` sends no credentials, `false` sends them; parsed as a Go boolean, anything else is an error |
| `CFCTL_TIMEOUT` | Default for `--timeout` |
| `CFCTL_CONFIG_DIR` | Directory for the config file and every cfctl cache and state file |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to a credentials file for ADC |

No other flag reads the environment, so an ambient `CFCTL_HOST` or
`CFCTL_FORCE` changes nothing.

When `--server` or `CFCTL_SERVER` names a server other than the selected
profile's (compared by scheme and host, ignoring a trailing slash), none of
the profile's auth settings apply and cfctl prints one warning line. Auth
then comes from the variables only, so `CFCTL_SERVER` with
`CFCTL_IMPERSONATE` makes a CI runner config-file-free. The `config`
commands write only the file and never persist ambient environment values.

A config file in the old single-target format, or one left in `~/.cfctl` by
an older cfctl, is refused with the `cfctl config create` line that
recreates it as a profile, and a list of any background agents the old
binary still runs from `~/.cfctl`.

## Troubleshooting

```bash
cfctl --verbose auth whoami
```

| Symptom | Likely cause |
|---------|--------------|
| `no service account configured for self-signed JWT` | Set a service-account identity: `cfctl config set-impersonate <sa-email>` (or `set-credentials <key.json>`) |
| `401 Unauthorized` | Token audience doesn't match the deployment, or the identity is not on the IAP access list |
| `403 Forbidden` from IAP | Service account not in `iap_members` (allow a few minutes for IAM to propagate) |
| `authentication required` | No usable credential source; see Authentication above |
| `not allowed to sign as <sa>` | The signing principal the message names lacks `roles/iam.serviceAccountTokenCreator` on `<sa>`; the message prints the grant command |
| `profile "X" (from ...) does not exist` | `--profile`, `CFCTL_PROFILE` or `current_profile` names a missing profile; `cfctl config profiles` lists them |

## Related Documentation

- [Using the CLI](../guides/using-the-cli.md)
- [Authentication](../concepts/authentication.md)
- [CI/CD Integration](../guides/cicd-integration.md)
