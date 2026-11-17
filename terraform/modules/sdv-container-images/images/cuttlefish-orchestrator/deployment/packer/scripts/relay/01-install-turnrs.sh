#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Build turn-rs from a pinned revision.
#
# The binary is compiled here instead of downloaded. It faces the internet and
# comes from a project with effectively one maintainer, so the image should
# contain what was reviewed. A release asset could be replaced under a moved
# tag; a commit cannot.

set -euo pipefail

: "${TURNRS_VERSION:?TURNRS_VERSION is required}"
: "${TURNRS_REPO:?TURNRS_REPO is required}"

export DEBIAN_FRONTEND=noninteractive
apt-get update
# libprotobuf-dev is needed as well as protobuf-compiler. turn-rs's sdk/build.rs
# runs protoc over a proto that imports google/protobuf/empty.proto, and the
# well-known types ship only in the dev package.
apt-get install -y --no-install-recommends build-essential git ca-certificates curl pkg-config libssl-dev protobuf-compiler libprotobuf-dev

# rustup is used instead of Debian's rust package, because turn-rs tracks a
# recent toolchain.
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain stable
# shellcheck source=/dev/null
source "$HOME/.cargo/env"

git clone --depth 1 --branch "$TURNRS_VERSION" "$TURNRS_REPO" /tmp/turn-rs
cd /tmp/turn-rs

# Record what was actually built. A tag can move; this commit is the answer to
# "what is running" for the life of the image.
git rev-parse HEAD > /etc/turn-server-revision

# The feature list is given explicitly instead of taking the default set. ssl
# carries the TLS listener and api carries the authorization hook; if either
# ever left the defaults upstream, a build would silently produce a relay that
# cannot serve TURNS or cannot authorize anyone.
cargo build --release --bin turn-server \
  --no-default-features --features udp,tcp,ssl,api,prometheus

install -m 0755 target/release/turn-server /usr/local/bin/turn-server
install -d -m 0755 /etc/turn-server

# turn-rs runs unprivileged. It needs port 443 for TURNS, which the systemd
# unit grants as a capability rather than by running as root.
useradd --system --no-create-home --shell /usr/sbin/nologin turnserver

# The toolchain is build-time only; leaving it would enlarge the image and give
# an intruder a compiler.
rm -rf /tmp/turn-rs "$HOME/.cargo" "$HOME/.rustup"
apt-get purge -y build-essential pkg-config libssl-dev protobuf-compiler libprotobuf-dev
apt-get autoremove -y
apt-get clean
