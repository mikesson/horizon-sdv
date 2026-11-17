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

# Using the CLI

`cfctl` is the command-line client for the Cloud Cuttlefish Orchestrator
(CCO), a service that runs Android Cuttlefish virtual devices on Google
Cloud. With it you create virtual devices, attach `adb` to them, stream their
screens, and tear them down.

This page takes you from nothing to a running device, assuming you have used
neither before. For the complete command list, see
[CLI Reference](../reference/cli.md).

## What you need first

CCO is a service that someone in your organization deploys. cfctl talks to a
deployment; it does not create one. Collect these first:

| What | Why |
|---|---|
| The URL of a CCO deployment | e.g. `https://cuttlefish-orchestrator-xxxx.a.run.app`. Ask whoever runs it |
| Access to that deployment | Someone has to let you in. How depends on the deployment mode, described below |
| The Go toolchain | To build cfctl. The module builds with Go 1.26 |
| `gcloud` | Used for the sign-in step on an authenticated deployment |
| `adb` (Android platform-tools) | `cfctl device connect` runs it for you, so it has to be on your `PATH` |

## Words this CLI uses

| Term | Meaning |
|---|---|
| Host | A Compute Engine VM running the Cuttlefish host tools. It holds several devices at once |
| Device | One Cuttlefish virtual Android device running on a host. This is the thing you `adb` into |
| Build | A registered set of Android artifacts a device boots from. Every deployment has a default, so you can ignore builds until you need your own |
| Batch | A group of devices created and torn down together, for CI |
| Operation | The record of a long-running action, such as provisioning. `--wait` polls one for you |
| Quota | The platform-wide cap on how many hosts can exist at once |

You do not need to create a host to get a device. Ask for a device and the
platform places it on a host that has room, or provisions a new one.

## Installing cfctl

```bash
go build -o cfctl ./cmd/cfctl
sudo mv cfctl /usr/local/bin/
```

`go install ./cmd/cfctl` works too and puts the binary in `$(go env GOPATH)/bin`.

Check it runs:

```bash
cfctl --version
```

## Getting started against Cloud Run with IAP

This is the default deployment shape and the one most readers will meet.
CCO runs as a single Cloud Run service behind built-in Identity-Aware Proxy
(IAP). IAP checks every caller against an access list before the request
reaches the application. That list is the entire access model, so anyone on
it can do everything and anyone off it cannot reach the service at all.
There are no roles to ask for.

Other shapes exist. [Deployment Modes](../getting-started/deployment-modes.md)
covers all of them; [Deployments without IAP](#deployments-without-iap) below
covers what changes for cfctl.

### The identity cfctl uses

A browser reaches the CCO console, gets redirected to Google sign-in, and
comes back with an IAP session cookie. A command-line tool has no browser
session to lean on, and IAP will not accept plain `gcloud` user credentials
for programmatic access.

So cfctl authenticates as a **service account**. It signs a JSON Web Token
whose audience is the service URL and sends it as a bearer token on every
call. Built-in Cloud Run IAP accepts that token with its Google-managed OAuth
client, which means no custom OAuth client, no consent screen, and no
downloaded key file.

Your own Google account still does the authorizing. cfctl asks Google's IAM
Credentials API to sign as the service account on your behalf, which works
because your operator granted you the `roles/iam.serviceAccountTokenCreator`
role on it. This is called impersonation, and it keeps private key material
off your machine.

### Three things to get from your operator

| What | Looks like | Where they find it |
|---|---|---|
| The service URL | `https://cuttlefish-orchestrator-xxxx.a.run.app` | `terraform output -raw service_url` |
| The signing service account | `cco-cli@PROJECT.iam.gserviceaccount.com` | `terraform output -raw cli_service_account` |
| Two IAM grants | The service account on the IAP access list, and `roles/iam.serviceAccountTokenCreator` for you on that service account | Both come from one Terraform variable |

**For operators:** setting `cli_users = ["user:alice@example.com", ...]` in
`terraform.tfvars` creates the `cco-cli` service account, adds it to the IAP
access list, grants every listed principal the token-creator role on it, and
grants `roles/iap.tunnelResourceAccessor` scoped to port 22 so
`cfctl host ssh` works. After `terraform apply`, `terraform output
next_steps` prints the exact cfctl commands with real values. Removing
someone from `cli_users` revokes their access and leaves the service account
for everyone else.

### Configuring cfctl

The wizard asks three questions and then tests what it saved:

```bash
cfctl setup
```

1. **Orchestrator URL.** Paste the service URL.
2. **Token audience.** Press Enter. An audience is only for a deployment
   with a custom IAP OAuth client, and the built-in one does not use it.
3. **How cfctl should authenticate.** Press Enter to accept impersonation.
   Then give it the service-account email.

If your machine has no Application Default Credentials, the wizard notices
and offers to run `gcloud auth application-default login` for you. It
finishes with a real call to the server and prints the identity that came
back, so a broken configuration fails here instead of on your first real
command.

The wizard saves the answers as a named profile (see
[Profiles](#profiles)). It writes the profile `--profile` names, else the
current one, else one called `default`, and the first profile becomes the
current one.

The same setup without prompts, if you prefer it scripted:

```bash
cfctl auth login                 # gcloud auth application-default login
cfctl config create prod --server https://cuttlefish-orchestrator-xxxx.a.run.app \
  --impersonate cco-cli@PROJECT.iam.gserviceaccount.com
cfctl auth whoami
```

Leave the audience unset. An empty audience is what selects the self-signed
JWT that built-in IAP accepts.

### Checking the setup

```bash
cfctl auth status   # profile, method, credential and the signing principal
cfctl auth whoami   # a real round trip; prints the identity the server saw
cfctl doctor        # signer, health, readiness, version, auth and quota; exits non-zero if any check fails
```

The signing principal is the identity whose credentials ask Google to sign
as the service account. It is the one that needs the token-creator grant.
On your laptop it is your gcloud user. On a Compute Engine VM with no user
credentials it is the VM's own service account. `auth status` may make one
metadata or tokeninfo call to name it.

`cfctl doctor` is the one to run when something feels wrong, and it is safe
to put in a CI preflight because a failed check is a non-zero exit.

Two consequences of authenticating as a shared service account:

- `cfctl auth whoami` prints the **service account's** email. Your own
  account signs for it and never appears in the request.
- Everything you create is recorded as created by that service account, so
  `--mine` on a list command means "the signing identity's", and a team
  sharing one identity sees one owner on everything. Give your resources
  `--name` prefixes you recognize.

### First-run failures

Add `--verbose` to any command to see the auth flow and the raw error:

```bash
cfctl --verbose auth whoami
```

| Message | What happened | Fix |
|---|---|---|
| `no server configured` | No profile is selected and no URL was given | `cfctl config create NAME --server URL`, or `cfctl setup` |
| `profile "X" (from ...) does not exist` | `--profile`, `CFCTL_PROFILE` or `current_profile` names a missing profile | `cfctl config profiles` lists the ones that exist |
| `no service account configured for self-signed JWT authentication` | Server is set, signing identity is not | `cfctl config set-impersonate SA_EMAIL` |
| `no Application Default Credentials` | Nothing on this machine can sign as the service account | `cfctl auth login` |
| `the active Google credentials lack the cloud-platform scope` | Common on a VM whose attached credentials carry limited scopes | `cfctl auth login` to use your own account instead |
| `not allowed to sign as <sa>` | The signing principal lacks `roles/iam.serviceAccountTokenCreator` on the service account itself | The message names the principal and prints the `gcloud iam service-accounts add-iam-policy-binding` command for your operator. On a VM, `cfctl auth login` signs with your own account instead |
| `service account <sa> not found` | Wrong email | Check it with your operator |
| `403` from IAP | The service account is not on the access list, or the IAM change has not propagated | Wait a minute and retry; then ask your operator to check `iap_members` |
| `401 Unauthorized` | The token audience does not match the deployment | Clear the audience with `cfctl config set-audience ""` unless your deployment uses a custom OAuth client |
| `Warning: URL is not profile "X"'s server` | `--server` or `CFCTL_SERVER` points elsewhere, so the profile's auth settings are not used | Pick the matching profile with `--profile`, or set the auth through `CFCTL_*` variables |

A corrupt config file fails every command with the parse error and names the
file. Fix it by hand, or delete it and re-run `cfctl setup`.

### Two commands that run as you

Most of cfctl acts as the service account. These two do not, and they need
your own Google account to have access:

- **`cfctl device webrtc DEVICE`** opens the console's player page in your
  browser. The browser goes through IAP interactively, so your own Google
  account has to be on the IAP access list as well.
- **`cfctl host ssh HOST`** shells into a host VM through gcloud and IAP TCP
  forwarding, using your gcloud account. It needs
  `roles/iap.tunnelResourceAccessor` (the `cli_users` variable grants it),
  read access to the instance, and an SSH login path such as OS Login.

## Your first device

```bash
cfctl device create --name my-first-device --interaction headless \
  --wait --timeout 15m
```

That one command finds a host with capacity or provisions a new one in a
capacity-ranked zone, resolves the deployment's default Android build, and
blocks until the device is running. Provisioning a fresh host adds a few
minutes.

`--interaction` states how you intend to use the device. `headless` is for
`adb` and automation. `interactive` prefers a GPU host for browser
streaming. Add `--dry-run` to print the plan (which host, which zone, which
build, plus any warnings) without creating anything.

```bash
cfctl device list --mine
cfctl device get my-first-device
```

Every command that takes a device or a host takes its ID or its name. A name
must match exactly one device or host that is not deleted. When several
match, the command exits 5 and prints the matches (ID, host or zone,
status, creator, created time) on stderr; pass the ID instead. When none
match, it exits 4. It never guesses and never prompts. Batch members are
named `PREFIX-NNN`, so two batches with one prefix share names.

### Attaching adb

```bash
cfctl device connect DEVICE
```

This opens a tunnel from a local port through the orchestrator to the
device, runs `adb connect` against it, and waits until `adb get-state`
reports the device online. It stays in the foreground for as long as you
want the connection, because the tunnel is the connection. Leave it in one
terminal, use `adb` normally from another, and press Ctrl+C to disconnect.

The wait lasts up to 60 seconds; `--wait-online` changes that, and
`--wait-online 0` skips it. It is separate from the global `--timeout`. If
the orchestrator refuses the device stream (403, 404, 502), the command
fails at once with that error. If adb still does not see the device online
when the wait ends, the command fails with exit code 7 when the device is
running (adbd may still be starting, so retry) and 5 when it is not. The
message names the last state adb reported.

Everything cfctl and adb print during the connect goes to stderr. Stdout
carries only the output of a passthrough command, so a script can capture
it directly.

The tunnel is the route that works from anywhere. Host VMs admit the ADB port
from inside the VPC only, and in some deployment modes they have no public
address at all.

```bash
# Run one adb command once the device is online, then exit; everything
# after -- goes to adb.
cfctl device connect DEVICE -- shell getprop ro.build.id

# Print the local address instead of launching adb yourself.
cfctl device connect DEVICE --no-attach
```

A passthrough command exits with adb's own exit code. adb's codes can
collide with cfctl's 3, 4 and 5, so a script that needs to tell them apart
should check the device first, or run adb itself against `--no-attach` or
`--background`.

`--no-attach` opens and closes one device stream before it prints the
address, so a permission or routing failure shows up there and not later in
your own adb.

### Background connections

```bash
cfctl device connect DEVICE --background   # returns once the device is online
cfctl device connections                   # list the background agents
cfctl device disconnect DEVICE             # stop them, by ID or name
```

`--background` hands the tunnel to a detached cfctl agent, which runs
`adb connect` and waits for the device to come online before the command
returns. If the orchestrator refuses the device stream during that wait,
the command stops the agent and fails at once with that error. The agent
then keeps adb attached. adb gives up on a network device
after about a minute without a connection, for example across a laptop
sleep or an orchestrator redeploy. The agent checks adb every 15 seconds and
runs `adb connect` again when adb has dropped the device, so the device
comes back in `adb devices` by itself. The REATTACHES column of
`device connections` counts the reconnects after which adb knew the device
again, and STATE shows what adb last reported.

A shell or logcat stream that is open during such an outage still ends.
Start it again once the device is back. Because the agent reattaches after
`adb disconnect` and `adb kill-server` too, stop it with
`cfctl device disconnect`. That command takes the device's ID or its name,
and it works without reaching the orchestrator: it matches the name against
the agents' own records. If two connected devices share the name, it lists
them and exits 5; pass the ID.

### The screen

```bash
cfctl device webrtc DEVICE              # opens the console player in a browser
cfctl device screenshot DEVICE -o shot.png
```

### Device expiry

Every device has a time to live. When it lapses, a background job deletes
the device, which is what keeps an unattended deployment from filling up.

```bash
cfctl device lifecycle DEVICE       # remaining TTL, expiry, pin state
cfctl device extend DEVICE          # adds the server's default, two hours
cfctl device extend DEVICE --by 4h
cfctl device pin DEVICE --reason overnight_test   # fence it from reclamation
cfctl device unpin DEVICE
```

Hosts have a TTL too, and a device cannot outlive its host. You do not have
to manage both: extending a device's TTL raises its host's expiry to match,
and a host carrying a pinned device is left alone. A host you created
yourself can also be extended directly with `cfctl host extend HOST --hours N`
(see Hosts below).

### Stopping and deleting

```bash
cfctl device stop DEVICE      # keeps the record; start it again later
cfctl device start DEVICE
cfctl device delete DEVICE --wait
```

## Everyday workflows

### A fleet for a CI run

```bash
cfctl batch create --prefix ci --count 20 --interaction headless \
  --fill-policy atomic --ready-timeout 900 --wait
cfctl batch devices BATCH_ID   # members with their ADB serials
cfctl batch delete BATCH_ID    # tears down every member
```

`--fill-policy atomic` means all or nothing. If a member permanently fails or
the ready timeout lapses, the whole batch is torn down. `best_effort` keeps
whatever came up. `cfctl batch devices` is the payload a CI job wants, since
it lists every member with the serial to hand to `adb`.

### Hosts

Most people never create a host by hand. When you do want one:

```bash
cfctl host list
cfctl host create --name my-host --wait
cfctl host get my-host
cfctl host extend my-host --hours 24   # add 24 hours to its lease
cfctl host ssh my-host
cfctl host delete my-host --force
```

Omitting `--zone` lets the server rank placement across the zones the
operator enabled. Naming a zone pins the host there and disables automatic
zone failover for it. Omitting `--machine-type` uses the catalog default
machine type your operator set. `cfctl machine-type default` prints it, and
`cfctl machine-type list` shows the alternatives. A device create takes no
machine type: for an exact shape, run `cfctl host create --machine-type TYPE
--exclusive`, then `cfctl device create --host NAME`.

`host extend` adds 1 to 168 hours per call, counted from the later of now
and the current expiry. Other hosts exit 5: a fleet, dedicated or batch host
names `cfctl device extend` and `cfctl device pin`, and a pinned host has no
expiry to extend.

### Scripting

Every command takes `--json`. Progress messages and hints go to stderr, so a
pipe stays clean.

```bash
cfctl device list --json | jq -r '.[].id'
cfctl device create --name d1 --interaction headless --wait --json   # prints the final device
```

`--wait` blocks until the action reaches a final state and fails fast, with
the server's message, if the resource lands in an error state. Bound it with
the global `--timeout` (`--timeout 8m`); without one, each individual request
still times out after 30 seconds.

Shell completion:

```bash
eval "$(cfctl completion bash)"   # also zsh, fish. Put it in your shell rc file
```

It completes device, host and profile names.

## Deployments without IAP

### A custom IAP OAuth client

Some deployments register their own OAuth client instead of using the
Google-managed one. Setting an audience switches cfctl from the self-signed
JWT to an OIDC ID token:

```bash
cfctl config set-audience YOUR_CLIENT_ID.apps.googleusercontent.com
```

Your operator gets that value from `terraform output -raw cfctl_audience`.
Set it only when told to. On a built-in-IAP deployment an audience produces
a token IAP rejects with 401.

### Network mode

A lab deployment can run with no authentication at all, reachable only from
inside the VPC. cfctl then sends no credentials:

```bash
cfctl config create lab --server http://cco.internal --auth none
# or, on an existing profile: cfctl config set-auth none
# or, for one shell: export CFCTL_AUTH_NONE=true
```

You still need network reachability, which usually means a VPN into the VPC
or a workstation inside it. Every request is attributed to one synthetic
identity, so `--mine` means everything and `created_by` is the same on every
resource.

### Horizon SDV in-cluster Workflows (proxy mode)

Inside Horizon SDV GKE clusters (`cuttlefish-orchestrator` module), `cuttlefish-workflow`
Argo pods (`cco-host-image-build`, `cco-cvd-run`, `cco-cts-run`) connect directly
to the cluster-internal API service in `CCO_AUTH_MODE=proxy` with `NetworkPolicy`
egress isolation:

```bash
export CFCTL_SERVER="http://cuttlefish-orchestrator-api.cuttlefish-orchestrator.svc.cluster.local:8080"
export CFCTL_AUTH_NONE=true
export CFCTL_PROXY_EMAIL="argo-workflow@horizon-sdv.local"
cfctl device list
```

For Horizon SDV sub-environments, use the prefixed namespace `<prefix>cuttlefish-orchestrator` instead (for example `http://cuttlefish-orchestrator-api.<prefix>cuttlefish-orchestrator.svc.cluster.local:8080`, injected automatically into `cco-*` Argo WorkflowTemplates).

### CI pipelines

A CI runner should use Workload Identity Federation and no key file. It also
needs no config file, because environment variables configure cfctl
completely:

```bash
export CFCTL_CONFIG_DIR="$RUNNER_TEMP/cfctl"   # keep the runner's stores private to the job
export CFCTL_SERVER=https://cuttlefish-orchestrator-xxxx.a.run.app
export CFCTL_IMPERSONATE=cco-cicd@PROJECT.iam.gserviceaccount.com
cfctl device list
```

A script must never rely on `current_profile`, which every shell of the user
shares. Pass `--profile NAME`, set `CFCTL_PROFILE`, or use environment
variables only, as above.

See [CI/CD Integration](cicd-integration.md) for the provider setup.

Where federation is impossible, a downloaded service-account key works:
`cfctl setup ./key.json` configures a profile from it, and
`cfctl config create ci --server URL --credentials ./key.json` does the same
without prompts. `CFCTL_CREDENTIALS=./key.json` sets it for one shell.

## Configuration

### Profiles

A profile is one deployment and the way cfctl authenticates to it. A company
with prod and staging keeps one profile for each and switches between them.

```bash
cfctl config create prod    --server https://prod-xxxx.a.run.app --impersonate cco-cli@prod.iam.gserviceaccount.com
cfctl config create staging --server https://staging-xxxx.a.run.app --impersonate cco-cli@staging.iam.gserviceaccount.com
cfctl config profiles               # lists them; * marks the current one
cfctl config use staging            # makes staging current
cfctl --profile prod device list    # one command against prod
cfctl config rename staging stage
cfctl config delete stage
```

The profile a command uses is chosen in this order: `--profile`, then
`CFCTL_PROFILE`, then the stored `current_profile`. An empty value at any of
these means unset. A name that does not exist is an error that names it.

`config create` takes the server only from `--server`, never from
`CFCTL_SERVER`. Its identity flags are `--impersonate SA`,
`--credentials KEY_FILE` (one of the two), `--audience ID` for a custom OAuth
client, and `--auth none`. The first profile you create becomes current.

`config set-server`, `set-impersonate`, `set-credentials`, `set-audience`,
`set-auth` and the two `clear-*` commands change the selected profile, so
`cfctl --profile staging config set-audience ""` edits staging only. With no
profile selected they write a profile named `default` and make it current,
as `setup` does.

### Where values come from

Each setting takes the first of: a flag, an environment variable, the
selected profile. A variable that is set to an empty string clears the
profile's value.

| Variable | Setting |
|---|---|
| `CFCTL_PROFILE` | Which profile to use |
| `CFCTL_SERVER` | Server URL (`--server` beats it) |
| `CFCTL_IMPERSONATE` | Service account to sign as |
| `CFCTL_CREDENTIALS` | Service-account key file to sign with |
| `CFCTL_AUDIENCE` | ID-token audience |
| `CFCTL_AUTH_NONE` | `true` sends no credentials, `false` sends them even if the profile says none. Any other value is an error |
| `CFCTL_TIMEOUT` | The `--timeout` default |
| `CFCTL_CONFIG_DIR` | Moves every cfctl store (see below) |
| `GOOGLE_APPLICATION_CREDENTIALS` | Credentials file for Application Default Credentials |

No other flag reads the environment.

When `--server` or `CFCTL_SERVER` names a server other than the selected
profile's, cfctl takes none of the profile's auth settings and prints one
warning line saying so. Auth then comes from the `CFCTL_*` variables only, so
one deployment's credentials never reach another. The comparison ignores a
trailing slash and the case of the scheme and host.

`cfctl config show` prints each effective setting and where it came from:
`flag`, `env`, `profile` or `unset`.

### Files

cfctl keeps everything in one directory: `cfctl` under the user config
directory (`~/.config/cfctl` on Linux, `~/Library/Application Support/cfctl`
on macOS, `%AppData%\cfctl` on Windows), or `$CFCTL_CONFIG_DIR` when it is set.

| File | Holds |
|---|---|
| `config.yaml` | The profiles and `current_profile`, owner-only permissions |
| `token-cache.json` | Signed tokens, keyed by service account and audience |
| `completion-cache.json` | Completion values, keyed by server |
| `connections/` | The state, lock and log files of background ADB agents |

```yaml
current_profile: prod
profiles:
  prod:
    server: https://prod-xxxx.a.run.app
    impersonate_service_account: cco-cli@prod.iam.gserviceaccount.com
  staging:
    server: https://staging-xxxx.a.run.app
    impersonate_service_account: cco-cli@staging.iam.gserviceaccount.com
```

The `config` commands write the file only, under a file lock, so two
concurrent writes never lose one. An ambient environment value is never
persisted into it.

### Upgrading from a cfctl without profiles

Older cfctl kept a single target in `~/.cfctl/config.yaml`. This cfctl
refuses that file and prints the exact `cfctl config create` line that
recreates its values as a profile. Run that line, and the new file goes in
the new directory. An old file that set both a key file and impersonation
becomes a profile with impersonation only, the identity cfctl used.

Background ADB agents started by the old binary keep their files under
`~/.cfctl`, where this binary does not look. Run
`cfctl device disconnect --all` with the old binary before upgrading. If you
did not, the refusal and the `config create` it prints both list the agents
found there and the command that stops them:

```bash
CFCTL_CONFIG_DIR=$HOME/.cfctl cfctl device disconnect --all
```

Global flags: `--server`, `--profile`, `--json`, `--verbose`, `--timeout`.

## Troubleshooting

Start with `cfctl doctor`. It reports connectivity, authentication and quota
in one pass, and its failing line names the request that failed.

**`curl -I <url>` returns a redirect to Google.** That is IAP doing its job.
A browser follows the redirect and signs in; a bare `curl` cannot. Use
`cfctl doctor` to test reachability instead.

**Nothing connects, and DNS resolves fine.** The deployment may have private
ingress, meaning it is not reachable from the public internet at all. You
need the VPN, or a workstation inside the VPC.

**Device creation fails with a placement or capacity error.** No zone is
enabled for placement, or the region is out of capacity for the shape asked
for. `cfctl admin capacity` shows where the fleet can grow, and
`cfctl quota show` shows whether the host quota is the limit.

**`adb is not on PATH`.** Install Android platform-tools, or use
`cfctl device connect --no-attach` and run your own adb against the printed
address.

## Where to go next

- [CLI Reference](../reference/cli.md) - Every command and flag
- [Creating Devices](creating-devices.md) - Build selection, GPU, multi-display
- [Testing Local Builds](local-builds.md) - Boot a cloud device from a build on your workstation
- [Authentication](../concepts/authentication.md) - The access model in full
- [Deployment Modes](../getting-started/deployment-modes.md) - The deployment shapes and what each one changes
- [CI/CD Integration](cicd-integration.md) - Keyless pipeline auth
- [cfctl for cvdr Users](cvdr-to-cfctl.md) - Every cvdr command and its cfctl equivalent
