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
# Units for the relay and its authorization agent, plus the first-boot step
# that renders the config Terraform passed in metadata.

set -euo pipefail

# First boot: take the relay config and the orchestrator URL from instance
# metadata. The config is rendered by Terraform so its contents can be checked
# at plan time. In particular it carries no static auth secret, which would
# bypass the agent and stop credentials expiring.
cat > /usr/local/sbin/cco-relay-firstboot <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

metadata() {
  curl -fsS -H "Metadata-Flavor: Google" \
    "http://metadata.google.internal/computeMetadata/v1/instance/attributes/$1"
}

install -d -m 0755 /etc/turn-server
install -d -m 0750 -o root -g turnserver /etc/turn-server/tls
metadata cco-relay-config > /etc/turn-server/turn-server.toml
chown root:turnserver /etc/turn-server/turn-server.toml
chmod 0640 /etc/turn-server/turn-server.toml

install -d -m 0755 /etc/cco
printf 'CCO_ORCHESTRATOR_URL=%s\n' "$(metadata cco-orchestrator-url)" > /etc/cco/relayhook.env
chmod 0644 /etc/cco/relayhook.env

# A placeholder keeps the TLS listener bindable before the first certificate
# arrives. Browsers reject it, exactly as they would reject a closed port. Plain
# TURN on 3478 stays up either way, so a certificate problem degrades the relay
# instead of taking it out.
if [ ! -s /etc/turn-server/tls/certificate.crt ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -subj "/CN=cco-relay-placeholder" \
    -keyout /etc/turn-server/tls/private.key \
    -out /etc/turn-server/tls/certificate.crt >/dev/null 2>&1
  chown root:turnserver /etc/turn-server/tls/private.key /etc/turn-server/tls/certificate.crt
  chmod 0640 /etc/turn-server/tls/private.key /etc/turn-server/tls/certificate.crt
fi
EOF
chmod 0755 /usr/local/sbin/cco-relay-firstboot

cat > /etc/systemd/system/cco-relay-firstboot.service <<'EOF'
[Unit]
Description=Render TURN relay configuration from instance metadata
After=network-online.target
Wants=network-online.target
Before=relayhook.service turn-server.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/cco-relay-firstboot

[Install]
WantedBy=multi-user.target
EOF

# The agent starts first and turn-server requires it. A relay that cannot reach
# its authorization path must not be answering clients.
cat > /etc/systemd/system/relayhook.service <<'EOF'
[Unit]
Description=CCO relay authorization agent
After=cco-relay-firstboot.service
Requires=cco-relay-firstboot.service

[Service]
Type=simple
User=relayhook
EnvironmentFile=/etc/cco/relayhook.env
ExecStart=/usr/local/bin/relayhook -orchestrator ${CCO_ORCHESTRATOR_URL} -listen 127.0.0.1:3001
Restart=always
RestartSec=2

# It talks to the metadata server and the orchestrator, and holds a token in
# memory. Nothing on disk needs writing.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
MemoryDenyWriteExecute=yes

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/turn-server.service <<'EOF'
[Unit]
Description=turn-rs TURN relay
After=relayhook.service
Requires=relayhook.service

[Service]
Type=simple
User=turnserver
ExecStart=/usr/local/bin/turn-server --config=/etc/turn-server/turn-server.toml
Restart=always
RestartSec=2

# TURNS listens on 443, which is privileged. A capability grants exactly that
# rather than running the process as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
MemoryDenyWriteExecute=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable cco-relay-firstboot.service relayhook.service turn-server.service
