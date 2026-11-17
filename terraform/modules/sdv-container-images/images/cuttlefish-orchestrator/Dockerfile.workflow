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
# Workflow runner container for Cuttlefish Orchestrator Argo Workflows
# (cco-host-image-build, cco-cvd-run, cco-cts-run). Contains cfctl, gcloud,
# SHA256-verified Packer + googlecompute plugin, adb, headless JRE for
# cts-tradefed, and CLI utilities running as non-root UID 1000.

FROM --platform=$BUILDPLATFORM golang:1.26.9-alpine AS cfctl-builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w" -o /out/cfctl ./cmd/cfctl

FROM debian:12-slim

ARG TARGETARCH=amd64
ARG PACKER_VERSION=1.15.0
ARG PACKER_GOOGLECOMPUTE_VERSION=1.2.4
ARG GCLOUD_CLI_VERSION=latest

LABEL org.opencontainers.image.title="cuttlefish-workflow" \
      org.opencontainers.image.description="Horizon SDV Cuttlefish Orchestrator Argo Workflows runner (cfctl, Packer, gcloud, adb, Java JRE)" \
      org.opencontainers.image.licenses="Apache-2.0 AND BSL-1.1 AND MPL-2.0 AND GPL-2.0-with-classpath-exception"

RUN apt-get update && apt-get install -y --no-install-recommends \
      android-tools-adb \
      ca-certificates \
      curl \
      gnupg \
      jq \
      openjdk-17-jre-headless \
      openssh-client \
      python3 \
      unzip \
    && curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg \
      | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
      > /etc/apt/sources.list.d/google-cloud-sdk.list \
    && apt-get update \
    && if [ "${GCLOUD_CLI_VERSION}" = "latest" ]; then \
         apt-get install -y --no-install-recommends google-cloud-cli; \
       else \
         apt-get install -y --no-install-recommends "google-cloud-cli=${GCLOUD_CLI_VERSION}"; \
       fi \
    && rm -rf /var/lib/apt/lists/*

RUN set -eu; \
    arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    pkg="packer_${PACKER_VERSION}_linux_${arch}.zip"; \
    sums="packer_${PACKER_VERSION}_SHA256SUMS"; \
    tmpdir="$(mktemp -d)"; \
    curl -fsSL "https://releases.hashicorp.com/packer/${PACKER_VERSION}/${pkg}" -o "${tmpdir}/${pkg}"; \
    curl -fsSL "https://releases.hashicorp.com/packer/${PACKER_VERSION}/${sums}" -o "${tmpdir}/${sums}"; \
    (cd "${tmpdir}" && grep "  ${pkg}\$" "${sums}" | sha256sum -c -); \
    unzip -q "${tmpdir}/${pkg}" packer -d /usr/local/bin; \
    chmod 0755 /usr/local/bin/packer; \
    rm -rf "${tmpdir}"; \
    packer version

ENV PACKER_PLUGIN_PATH=/usr/local/share/packer/plugins

RUN mkdir -p "${PACKER_PLUGIN_PATH}" \
    && packer plugins install "github.com/hashicorp/googlecompute" "v${PACKER_GOOGLECOMPUTE_VERSION}" \
    && chmod -R a+rX "${PACKER_PLUGIN_PATH}"

COPY --from=cfctl-builder /out/cfctl /usr/local/bin/cfctl
RUN chmod 0755 /usr/local/bin/cfctl

RUN groupadd -g 1000 cco \
    && useradd -u 1000 -g 1000 -m -s /bin/bash cco \
    && install -d -o 1000 -g 1000 /workspace

USER 1000:1000
WORKDIR /workspace
