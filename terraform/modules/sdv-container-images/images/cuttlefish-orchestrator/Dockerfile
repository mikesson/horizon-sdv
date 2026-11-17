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

# Split Go backend API container (cuttlefish-orchestrator-api).
# The Angular console and Nginx reverse proxy live in web/cuttlefish-console/Dockerfile.

FROM golang:1.26.9-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cloud-orchestrator ./cmd/orchestrator \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cfctl ./cmd/cfctl

FROM gcr.io/distroless/base-debian12:nonroot

WORKDIR /app

COPY --from=builder --chown=nonroot:nonroot /out/cloud-orchestrator /app/cloud-orchestrator
COPY --from=builder --chown=nonroot:nonroot /out/cfctl /app/cfctl

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/app/cloud-orchestrator"]
