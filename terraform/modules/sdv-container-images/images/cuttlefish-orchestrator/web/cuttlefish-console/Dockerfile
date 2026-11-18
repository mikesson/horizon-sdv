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

# Split Nginx frontend container (cuttlefish-console), built with context web/cuttlefish-console/.

ARG NODE_VERSION=24-alpine
ARG NGINX_VERSION=1.31.2-alpine3.23

FROM node:${NODE_VERSION} AS frontend-builder

ARG BASE_HREF=/cuttlefish-orchestrator/

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci --legacy-peer-deps --no-audit --no-fund

COPY . .
RUN npx ng build --configuration=production --base-href "${BASE_HREF}" && \
    test -f dist/cuttlefish-console/browser/index.html

FROM nginx:${NGINX_VERSION}

LABEL org.opencontainers.image.title="cuttlefish-console" \
      org.opencontainers.image.description="Cuttlefish Orchestrator Angular Console and Nginx Reverse Proxy" \
      org.opencontainers.image.licenses="Apache-2.0"

ENV API_UPSTREAM=cuttlefish-orchestrator-api:8080
ENV CCO_CONSOLE_PROBE_PORT=8081

RUN rm -rf /etc/nginx/conf.d/* /usr/share/nginx/html/* && \
    mkdir -p /etc/nginx/snippets /tmp/client_temp /tmp/proxy_temp /tmp/fastcgi_temp /tmp/uwsgi_temp /tmp/scgi_temp && \
    chown -R 101:101 /tmp /usr/share/nginx/html

COPY --from=frontend-builder --chown=101:101 /app/dist/cuttlefish-console/browser/ /usr/share/nginx/html/
COPY --chown=101:101 nginx/nginx.conf.template /etc/nginx/nginx.conf.template
COPY --chown=101:101 nginx/snippets/ /etc/nginx/snippets/
COPY --chmod=0755 nginx/entrypoint.sh /entrypoint.sh

USER 101:101
EXPOSE 8080 8081

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8081/healthz || exit 1

ENTRYPOINT ["/entrypoint.sh"]
