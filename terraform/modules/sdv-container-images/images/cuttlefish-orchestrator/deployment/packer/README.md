# Packer images

This directory builds exactly one image: the TURN relay VM
(`relay-image.pkr.hcl` plus the provisioners in `scripts/relay/`). turn-rs is
compiled from a pinned source revision and `relayhook` from this repository, so
nothing is fetched at boot and no credential is baked in. What the image is for
is described in [terraform/modules/relay/README.md](../../terraform/modules/relay/README.md).

```bash
packer build \
  -var project_id=<project> \
  -var zone=<zone> \
  -var cco_revision="$(git rev-parse HEAD)" \
  relay-image.pkr.hcl
```

The result is named `cco-relay-<timestamp>` in the image family `cco-relay`,
which is what the `relay_image` Terraform variable points at. The last
provisioner fails the build rather than publish an image that cannot start: it
checks the two binaries (`turn-server` and `relayhook`) and three systemd units
(`cco-relay-firstboot`, `relayhook`, and `turn-server`), and verifies that no
build toolchain survived into a VM that faces the internet.

The build needs the following.

- **A network to build in.** `network` and `subnetwork` default to
  `cuttlefish-network` and `cuttlefish-network-subnet`, the names the Terraform
  configuration creates. A project whose default network was deleted needs
  those flags to name a network that exists, and the subnet must be in the
  build zone's region.
- **SSH over IAP TCP forwarding.** The builder is reached that way, because a
  VPC built for this deployment opens port 22 to nothing else. That needs one
  firewall rule permitting `tcp:22` from `35.235.240.0/20` to the
  `cco-image-build` network tag. Terraform does not carry it, since it exists
  only for image builds. The header comment in `relay-image.pkr.hcl` has the
  command, and says to delete the rule when the build is done.

Cuttlefish **host** images are not built here. They come from the Go tooling
in the upstream repository (<https://github.com/google/android-cuttlefish>,
`tools/baseimage/`). See
[docs/guides/building-host-images.md](../../docs/guides/building-host-images.md).
