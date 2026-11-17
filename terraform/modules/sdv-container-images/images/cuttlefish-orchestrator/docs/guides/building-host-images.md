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

# Building Host Images

How to build the GCE images CCO boots its Cuttlefish host VMs from. The
images are built with the upstream Android Cuttlefish tooling,
<https://github.com/google/android-cuttlefish>, under `tools/baseimage/`, from
packages in upstream's public apt repository. CCO ships no image pipeline and
needs no locally built packages. (The only Packer image in this repository is
the TURN relay VM in `deployment/packer/`.)

## What CCO needs from an image

| Requirement | Value |
| --- | --- |
| Operating system | Debian 13 (trixie) |
| Kernel | 6.4 or newer; Debian 13's cloud kernel is 6.12 |
| Packages | `cuttlefish-base`, `cuttlefish-user` and `cuttlefish-orchestration`, all at one pinned version that fetches `gs://` and `https://` builds |
| Host Orchestrator | installed by `cuttlefish-orchestration`, serving on its default port, 2080 |
| Image families | `cco-host` for x86 machine families, `cco-host-arm64` for `c4a` |
| Image project | the project named by `CCO_HOST_IMAGE_PROJECT` |

**Image project.** Terraform sets `CCO_HOST_IMAGE_PROJECT` from the
`host_image_project` variable, which defaults to the deployment's own project.
When it names another project, Terraform grants the orchestrator service
account `roles/compute.imageUser` there. That grant works only where the
operator running Terraform holds IAM in that project. One machine family can
read from a different project with
`cfctl admin host-image update FAMILY --source-project PROJECT`.

**Image families.** The schema seed maps every machine family, `c4a`
included, to `cco-host`. `c4a` is arm64, so it is refused as `arch_mismatch`
until its row names an arm64 family (see [Tagging](#tagging)).

**Why the kernel must be 6.4 or newer.** Every AMX-capable machine type is
unusable on an older host kernel. Intel added AMX with Sapphire Rapids, so
that covers c3, c4 and n4. Before 6.4, KVM reports `XTILE_CFG` in
`KVM_GET_SUPPORTED_CPUID` while excluding `XTILE_DATA`, because the VMM never
requested AMX guest permission. Setting `XTILE_CFG` in `XCR0` without
`XTILE_DATA` is architecturally illegal, so the Android guest kernel computes
`XCR0 = 0x200e7`, faults on its own first `XSETBV` inside
`fpu__init_cpu_xstate`, and panics. `panic=-1` reboots it immediately, forever.
Linux 6.4 filters `XTILE_CFG` out when `XTILE_DATA` is not permitted, so the
guest sees no AMX and boots normally.

On a `c4-standard-4` host running `6.1.0-50-cloud-amd64`, the VM provisioned
cleanly (nested virtualization on, `/dev/kvm` present, `vmx` present, host
`running`) and the device sat in `creating` through 435 guest reboots, with no
error surfaced anywhere. Nothing distinguishes it from a slow create without
reading the guest kernel log. n1, n2 and g2 predate AMX and are unaffected.

## Prerequisites

- A clone of <https://github.com/google/android-cuttlefish>. The commands below
  run from its `tools/baseimage/` directory.
- A Go toolchain, for `go run`.
- `gcloud`, signed in, with Application Default Credentials for the tools:
  `gcloud auth application-default login`.
- A Debian machine or container with `apt-get`, to download the packages. The
  packages are only downloaded there.
- Compute Engine permissions in the image project to create VMs, disks and
  images, plus a network and a firewall rule (see [Network](#network)).
- SSH login as an administrator on the builder VM, because every build step
  runs over `gcloud compute ssh` with `sudo`. Where OS Login is enforced that
  means `roles/compute.osAdminLogin` in the image project; otherwise it means
  permission to set the project's SSH-key metadata.

The upstream builder needs two things the organization's policy must allow in
the image project:

| Policy | What the builder needs |
| --- | --- |
| `constraints/compute.vmExternalIpAccess` | an external IP on the builder VM |
| `constraints/compute.trustedImageProjects` | `debian-cloud`, the source of the builder VM and of the base image |

The builder has no flag for a private network or an IAP tunnel. An organization
whose policy denies external IPs cannot run it at all. Build the image in a
project without that restriction and share it:

1. Set `host_image_project` to the building project and apply Terraform, which
   grants the orchestrator `roles/compute.imageUser` there.
2. Add the building project to `constraints/compute.trustedImageProjects` in
   the deploying organization, or host creates are refused by policy.

Every build tool defaults to `-zone us-central1-a`. Pass `-zone` to each
command below to build where you choose. The zone must offer `n1-standard-16`
for x86 builds and `t2a-standard-16` for arm64 builds. The arm64 builder
falls back to `c4a-standard-16` only when `t2a-standard-16` is out of stock,
so a zone without t2a fails the arm64 build.

## Network

The builder creates a temporary VM on the project's `default` network, gives
it an external IP, and reaches it with plain `gcloud compute ssh` and
`gcloud compute scp`. A project created under an organization usually has no
`default` network, and IAP's TCP forwarding range does not cover plain SSH to
an external IP. Create both for the build and delete both afterwards:

```bash
PROJECT=your-image-project
MY_IP=203.0.113.7   # the public egress address of the machine running the tools

gcloud compute networks create default --subnet-mode=auto --project="$PROJECT"
gcloud compute firewall-rules create baseimage-ssh --project="$PROJECT" \
  --network=default --direction=INGRESS --allow=tcp:22 \
  --source-ranges="$MY_IP/32"
```

When the build ends:

```bash
gcloud compute firewall-rules delete baseimage-ssh --project="$PROJECT"
gcloud compute networks delete default --project="$PROJECT"
```

A failed build can leave its builder resources behind. The builder VM carries
the `-image-name` of the step that failed, its scratch disk is
`<image-name>-attached-disk`, and the optional validation VM is
`<user>-validate-image`. A step refuses to start when its output image or its
scratch disk already exists, and a leftover builder VM of the same name fails
the VM create. List and delete them by hand:

```bash
gcloud compute instances list --project="$PROJECT"
gcloud compute disks list --project="$PROJECT" --filter="name~attached-disk"
gcloud compute instances delete NAME --zone=ZONE --project="$PROJECT"
gcloud compute disks delete NAME --zone=ZONE --project="$PROJECT"
```

The network delete fails while any VM still uses it, which is a useful check
that nothing was left running.

## Packages

The packages come from upstream's apt repository in Artifact Registry, suite
`android-cuttlefish-nightly`. The stable suite, `android-cuttlefish`, is at
1.57.0, which cannot fetch `gs://` or `https://` builds. Pin the nightly
packages at `1.58.0~git202610160155.4c8e83e9` or later until a stable 1.58 is
published, then move to the stable suite. The nightly suite keeps old
versions, so a pinned build stays reproducible.

On the Debian machine:

```bash
sudo curl -fsSL https://us-apt.pkg.dev/doc/repo-signing-key.gpg \
  -o /etc/apt/trusted.gpg.d/artifact-registry.asc
sudo chmod a+r /etc/apt/trusted.gpg.d/artifact-registry.asc
echo "deb https://us-apt.pkg.dev/projects/android-cuttlefish-artifacts android-cuttlefish-nightly main" \
  | sudo tee /etc/apt/sources.list.d/cuttlefish-nightly.list
sudo dpkg --add-architecture arm64   # only for c4a images
sudo apt-get update

apt-cache madison cuttlefish-base    # the versions the suite offers

VERSION='1.58.0~git202610160155.4c8e83e9'
mkdir -p ~/cf-debs/amd64 ~/cf-debs/arm64

cd ~/cf-debs/amd64
apt-get download "cuttlefish-base=$VERSION" "cuttlefish-user=$VERSION" \
  "cuttlefish-orchestration=$VERSION"

cd ~/cf-debs/arm64
apt-get download "cuttlefish-base:arm64=$VERSION" "cuttlefish-user:arm64=$VERSION" \
  "cuttlefish-orchestration:arm64=$VERSION"
```

Copy the `.deb` files to the machine that runs the build tools if it is a
different one. The install step takes local files only.

## Build

Three steps, each a `go run` in `tools/baseimage/`. Each step boots a builder
VM, attaches a disk made from its source image, changes it, and saves the disk
as a new image in `-project`. Name each output image with the date and the
architecture, so one day's images stay apart from another's. The builder VM
and its disk take the same name, so a rerun on the same day needs a new
suffix, or the leftover VM, disk and image deleted first.

**1. Kernel.** Starts from the dated Debian 13 image in `debian-cloud` that
the tool names, installs the named kernel package, and removes every other
kernel. The step refuses with `source image ... does not exist` if
`debian-cloud` has deleted that dated image; a newer upstream checkout names
a current one.

```bash
P=your-image-project
Z=your-zone
D=$(date +%Y%m%d)

go run ./cmd/create_gce_fixed_kernel -project="$P" -zone="$Z" \
  -arch=x86_64 -debian-version=13 \
  -linux-image-deb=linux-image-6.12.N+deb13-cloud-amd64 \
  -image-name="cco-kernel-x86-$D"
```

`-arch` has no default, and every step aborts with
``usage: `-arch` must not be empty`` without it. `-debian-version` defaults to
13. `-linux-image-deb` is mandatory, and upstream's README example names a 6.1
kernel; ignore it.

The step upgrades the image's packages first, then checks that
`linux-image-cloud-amd64` depends on exactly the package you named. Name the
one Debian 13 currently ships: `apt-cache depends linux-image-cloud-amd64` on
a Debian 13 machine prints it. A wrong name fails the step with
`Expected <yours>, got: <current>`, which tells you what to pass.

Steps 2 and 3 hold the kernel packages before they upgrade the image, and
still fail if the kernel changed:

```
IMAGE ENDS WITH KERNEL: 6.12.101+deb13-cloud-amd64
KERNEL UPDATE DETECTED!!! 6.12.100+deb13-cloud-amd64 -> 6.12.101+deb13-cloud-amd64
```

The message names the version without its package prefix. When it appears,
rerun step 1 with `linux-image-` in front of that version, for example
`-linux-image-deb=linux-image-6.12.101+deb13-cloud-amd64`.

**2. Base image.** Installs the NVIDIA driver stack inside a chroot (no GPU is
needed on the builder) and stamps the `enable-vmx` license that nested
virtualization requires:

```bash
go run ./cmd/create_gce_base_image -project="$P" -zone="$Z" -arch=x86_64 \
  -source-image-project="$P" -source-image="cco-kernel-x86-$D" \
  -image-name="cco-base-x86-$D"
```

**3. Cuttlefish packages.** Uploads and installs the packages; their Debian
dependencies come from the Debian archive:

```bash
go run ./cmd/gce_install_cuttlefish_packages -project="$P" -zone="$Z" -arch=x86_64 \
  -source-image-project="$P" -source-image="cco-base-x86-$D" \
  -image-name="cco-host-x86-$D" \
  -deb ~/cf-debs/amd64/cuttlefish-base_*_amd64.deb \
  -deb ~/cf-debs/amd64/cuttlefish-user_*_amd64.deb \
  -deb ~/cf-debs/amd64/cuttlefish-orchestration_*_amd64.deb
```

**arm64.** Repeat the three steps with `-arch=arm64`, an arm64 kernel package
(`linux-image-6.12.N+deb13-cloud-arm64`, from
`apt-cache depends linux-image-cloud-arm64`), the debs from `~/cf-debs/arm64`,
and `arm64` in the image names. The builder VM is a `t2a-standard-16`, or a
`c4a-standard-16` when t2a is out of stock.

The stock package defaults are correct for CCO, so the optional
`gce_install_custom_configs` step is not needed. A baked Host Orchestrator
defaults file once sent build fetches to an API the fetcher's key was blocked
on, and every device create failed.

## Tagging

The tools set no image family. CCO boots whatever image a family points at,
so prove an image before it reaches `cco-host`, because every host provisioned
after the tag uses it at once.

1. Put the new image in a staging family and point one machine family at it:

   ```bash
   gcloud compute images update "cco-host-x86-$D" --family=cco-host-staging --project="$P"
   cfctl admin host-image update n1 --image-family cco-host-staging
   ```

2. Create a host and a device on n1 and confirm the device reaches `running`
   (see [Checking](#checking)). Do the same on one AMX shape, for example
   `c4-standard-4`, after pointing `c4` at the staging family. One AMX create
   covers the c3, c4 and n4 group. `/dev/kvm` being present proves nothing:
   that is how broken c4 and n4 rows once reached the catalog.

3. Move the image to its real family and point the machine families back:

   ```bash
   gcloud compute images update "cco-host-x86-$D" --family=cco-host --project="$P"
   cfctl admin host-image update n1 --image-family cco-host
   cfctl admin host-image update c4 --image-family cco-host
   ```

For arm64, tag the image `cco-host-arm64` and map `c4a` to it once:

```bash
gcloud compute images describe "cco-host-arm64-$D" --project="$P" --format='value(architecture)'
gcloud compute images update "cco-host-arm64-$D" --family=cco-host-arm64 --project="$P"
cfctl admin host-image update c4a --image-family cco-host-arm64
```

The `describe` must print `ARM64`. CCO reads the image's declared
architecture, treats an image that declares none as x86, and refuses to boot
an image of the wrong architecture on a machine family. A deployment that
builds no arm64 image disables the row instead, so the image check in
`cfctl doctor` passes:

```bash
cfctl admin host-image update c4a --no-enabled
```

A label can record the package version for people reading the image list.
Label values allow only lowercase letters, digits, `-` and `_`, so spell the
version with hyphens. CCO does not read it.

```bash
gcloud compute images add-labels "cco-host-x86-$D" --project="$P" \
  --labels=cuttlefish-version=1-58-0-git202610160155-4c8e83e9
```

## Checking

`cfctl doctor` resolves every enabled machine family's image the way a host
create does and fails its `images` line when any of them cannot boot. The
line names the machine family, the image family, the project and why the image
is unusable. `cfctl admin host-image list` shows every row, disabled ones
included, with the image it resolves to or a reason code such as `not_found`,
`permission_denied` or `arch_mismatch`:

```bash
cfctl doctor
cfctl admin host-image list    # the image each family resolves to, or why none
```

Then prove the image end to end with one host and one device:

```bash
cfctl host create --name image-check --machine-type n1-standard-8 --wait
cfctl device create --host HOST_ID --build-id BUILD_ID \
  --build-target aosp_cf_x86_64_phone-trunk_staging-userdebug --wait
```

The device must reach `running`. If your builds live in a bucket, also create
one device from a pushed build (`cfctl device create --local-image ...`), which
exercises the `gs://` fetch that needs the 1.58 packages. `cfctl host get
HOST_ID --json` shows `boot_image`, the image the host's VM was created from.
Delete the host afterwards.

Upstream's `gce_validate_image` is optional. It boots the image on an
`n1-standard-16` with an `nvidia-tesla-p100-vws` GPU and an external IP, so it
needs that GPU's quota in the zone and the same SSH rule as the build:

```bash
go run ./cmd/gce_validate_image -project="$P" -zone="$Z" \
  -image-project="$P" -image="cco-host-x86-$D"
```
