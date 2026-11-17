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

# WebRTC Streaming

Cloud Cuttlefish Orchestrator uses WebRTC to stream video and audio from virtual Android devices directly to your browser. This document explains how the streaming system works.

## Overview

When you connect to a device, CCO establishes a peer-to-peer WebRTC connection between your browser and the Cuttlefish instance running on a host VM. This provides:

- **Low latency** - Direct media streaming without server-side transcoding
- **GPU acceleration** - Hardware-accelerated video encoding on the host
- **Full interactivity** - Touch, keyboard, and audio input/output

```
┌─────────────────────────────────────────────────────────────────────┐
│                         WebRTC Connection                           │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌─────────────┐                              ┌─────────────┐       │
│  │   Browser   │◄══════════════════════════►. │  Cuttlefish │       │
│  │             │       WebRTC Media           │  Instance   │       │
│  │  - Video    │       (H.264/VP8)            │             │       │
│  │  - Audio    │                              │  - Video    │       │
│  │  - Input    │                              │  - Audio    │       │
│  └─────────────┘                              │  - ADB      │       │
│                                               └─────────────┘       │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

## Connection Modes

The `device_ingress` variable (see
[Deployment Modes](../getting-started/deployment-modes.md)) selects where
device media meets the internet. Signaling is **always** proxied through the
orchestrator; only the media path changes.

**Media confidentiality is identical in all three modes.** WebRTC media is
DTLS-SRTP end-to-end encrypted between the client and the device. A TURN
server forwards encrypted packets and **cannot decrypt them**. The modes
differ in **attack surface and choke points**; media confidentiality is the
same in each.

### `public` mode, where media meets the internet at the host

Host VMs get external IPs; the client connects media directly to the host's
external IP via STUN. Configured TURN servers, if any, are offered only as a
fallback for restrictive client networks.

| Aspect | Configuration |
|--------|---------------|
| Signaling | Through orchestrator proxy |
| Media | Direct (STUN) to the host's external IP; TURN fallback if configured |
| Host exposure | WebRTC media ports open to `client_cidrs` (or `0.0.0.0/0`) |

### `private` mode, where media stays on the private network

Host VMs are internal-only; clients reach media over a VPN or private network
whose ranges are listed in `client_cidrs`. No TURN and no internet-facing
firewall rule.

| Aspect | Configuration |
|--------|---------------|
| Signaling | Through orchestrator proxy |
| Media | Direct over the VPN / private ranges (`client_cidrs`) |
| Host exposure | None internet-facing; VPN reachability only |

### `relay` mode, where media meets the internet at a TURN server

Host VMs are internal-only; all client media is relayed through a TURN
cluster. The orchestrator fails fast at startup without configured TURN
servers (`CCO_TURN_SERVERS` and the shared-secret volume, both set by
Terraform in relay mode). Relay **reduces host attack surface** (hosts are
never internet-reachable). It does not add or remove media encryption.

The two ends of a relayed connection reach the same relay at different
addresses: a client over the internet at the external one (`CCO_TURN_SERVERS`),
a host from inside the VPC at the internal one (`CCO_DEVICE_TURN_SERVERS`),
since it has no address of its own and the network has no NAT. Both are
reserved addresses, so they survive a relay being replaced.

| Aspect | Configuration |
|--------|---------------|
| Signaling | Through orchestrator proxy |
| Media | Relayed through TURN (still DTLS-SRTP end-to-end) |
| Host exposure | None internet-facing; TURN is the only public choke point |

## Signaling

Before media can flow, the browser and Cuttlefish instance must exchange connection information through a process called signaling.

### Signaling Flow

1. **Browser requests connection** - Opens signaling channel to device
2. **Device info returned** - Details about the Cuttlefish instance
3. **Request offer** - Browser asks device for WebRTC offer
4. **SDP offer received** - Device describes its media capabilities
5. **SDP answer sent** - Browser responds with its capabilities
6. **ICE candidates exchanged** - Both sides share network paths
7. **Connection established** - Media starts flowing

### Transport Options

CCO supports two signaling transports:

| Transport | Use Case |
|-----------|----------|
| **WebSocket** | Primary - lower latency, real-time messaging |
| **HTTP Polling** | Fallback - works through restrictive proxies |

The client automatically selects the best transport.

## TURN Relay

TURN (Traversal Using Relays around NAT) servers relay media traffic when direct connections aren't possible.

### When TURN is Used

- Clients behind symmetric NAT
- Restrictive corporate firewalls
- Private VPC hosts without public IPs
- UDP traffic blocked by network

### How TURN Works

```
┌─────────────┐         ┌─────────────┐         ┌─────────────┐
│   Browser   │◄───────►│ TURN Server │◄───────►│  Cuttlefish │
│             │  Media   │   (Relay)   │  Media   │             │
└─────────────┘         └─────────────┘         └─────────────┘
```

1. The client fetches `GET /v2/devices/{id}/ice-config` from the orchestrator
2. The answer carries a list per leg and one credential shared by both
3. The client configures its own peer connection from `client_servers` and
   sends `device_servers` to the device in `request-offer`
4. Media flows through the relay when a direct pair cannot form, or always
   where `ice_transport_policy` is `relay`

### The two legs

The lists are not interchangeable, and swapping them fails as a connection
that never establishes, with nothing logged at either end:

| Field | Who it is for | What it carries |
|-------|---------------|-----------------|
| `client_servers` | the browser or CLI | relays at their **external** addresses, plus STUN unless the client is pinned |
| `device_servers` | the device, via `request-offer` | relays at their **internal** addresses, plus STUN only where hosts have external addresses of their own |
| `ice_transport_policy` | the client only | `relay` where device media never meets the internet at the host |

STUN reaches a leg only where that leg can use it. A pinned client gathers no
server-reflexive candidate whatever it is given, and a host VM without an
external address has no route to a public STUN server at all. The query can
only time out, on the way to every connection.

The policy binds the client alone. A device builds its peer connection from a
server list and a port range and never sets a transport type, and no signaling
field carries one, so a device cannot be held to relayed transport however the
deployment is configured.

### TURN Authentication

CCO uses HMAC-SHA1 authentication for TURN:

- **Credentials are time-limited** - they expire after the credential TTL
  (12 hours, a code default with no deployment setting)
- **Generated per request** - scoped to the caller and to the device (or, on
  `/v1`, the host) whose media they will carry
- **Verified by the orchestrator** - relays hold no shared secret and delegate
  every authentication back to `POST /v2/relay/authorize`

The TTL is a **session ceiling**, and nothing refreshes against it. The expiry
is baked into the username, so renewing means a new allocation, and no client
CCO serves can re-gather without rebuilding its peer connection. A session
outlives its credential only by reconnecting. This does not widen the
revocation window. A relay's agent re-asks the orchestrator on a much shorter
ceiling, so a deleted device stops relaying within minutes however long its
credential was valid.

## Video Encoding

Cuttlefish uses GPU-accelerated video encoding when available:

| GPU Type | Encoder | Quality |
|----------|---------|---------|
| NVIDIA (T4, L4, etc.) | NVENC H.264 | Best |
| No GPU | Software VP8 | Good |

GPU rendering modes:

| Mode | Description |
|------|-------------|
| `auto` | Automatic selection based on host capabilities (the default) |
| `gfxstream` | NVIDIA GPU rendering |
| `drm_virgl` | CPU-based rendering |
| `swiftshader` | Software rendering |

## Multi-Display Support

CCO supports multiple virtual displays per device:

- Add/remove displays dynamically through the console
- Each display streams as a separate video track
- Control sidebar lets you switch between displays

## Troubleshooting

### Connection Issues

| Symptom | Possible Cause | Solution |
|---------|----------------|----------|
| Black screen | WebRTC connection failed | Check firewall allows UDP |
| High latency | Using TURN relay | Consider `device_ingress = "public"` |
| No audio | Audio not enabled | Check device audio settings |
| Connection timeout | Signaling blocked | Try different network |

### Network Requirements

For optimal performance:

- **UDP ports 3478, 5349** - TURN server connectivity
- **UDP high ports (49152-65535)** - Direct WebRTC media
- **Low latency connection** - < 100ms RTT recommended
