/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * What can address one instance, over which medium, and where it terminates.
 *
 * The question is only ever asked about a specific instance, so every value
 * here comes from that instance's own row and all of them change when another
 * is selected. Nothing in this file is a legend: a static illustration in a
 * live view charges attention on every visit, forever, including long after it
 * has been learned.
 *
 * There are two addressing depths, not three. adb and WebRTC both terminate at
 * the instance; the console holds one peer connection per session and displays
 * arrive as tracks multiplexed inside it, so drawing WebRTC reaching past adb
 * into a display would assert a difference that does not exist.
 *
 * Every line is DECLARED, from the launch config and from what the host
 * assigned. It says what can address the instance; it never says a packet was
 * exchanged.
 */

import type {DeviceInstance} from './device.model';
import type {RadioRail} from './slot-space';

interface IdentityRow {
  readonly label: string;
  readonly value: string;
}

interface DisplayTrack {
  readonly label: string;
  readonly size: string;
}

/** One channel that terminates at the instance, and what rides inside it. */
interface ReachRow {
  readonly channel: string;
  readonly detail: string;
  /**
   * Nested inside `channel`, never listed beside it. Guests sit behind
   * masquerade with no inbound DNAT, so nothing reaches a guest's network
   * except by tunnelling out from inside it. Drawing this as its own edge
   * would be a lie about how the packets travel.
   */
  readonly ridesInside?: string;
  /**
   * Multiplexed inside this row's one connection, so they nest here rather
   * than terminating a step deeper than the channel carrying them.
   */
  readonly tracksInside?: readonly DisplayTrack[];
}

/** One medium the instance is on, and who else is on it. */
interface ShareRow {
  readonly label: string;
  readonly value: string;
  readonly with: string;
}

export interface InstanceReachability {
  /** `my-car-rig / ivi`, the box label for the containment drawing. */
  readonly boxLabel: string;
  /** `slot 05`, or empty where the slot never resolved. */
  readonly slotLabel: string;
  readonly hostName: string;
  readonly identity: readonly IdentityRow[];
  readonly reach: readonly ReachRow[];
  readonly shares: readonly ShareRow[];
}

export interface ReachabilityContext {
  readonly instance: DeviceInstance;
  readonly deviceName: string;
  readonly hostName: string;
  /** The other instances in this group, by name. */
  readonly peers: readonly string[];
  readonly radio: RadioRail | null;
  /** The vsock band's caption, or empty where the group has no peer to reach. */
  readonly vsockLabel: string;
  /**
   * Whether a guest-to-guest vsock layer exists at all. The Shares row would
   * otherwise name a per-group daemon that the launch may never have started.
   */
  readonly vsockLayer: 'on' | 'off' | 'unresolved';
  /** Every placed instance on the host, this device's own included. */
  readonly hostInstances: number;
}

function padSlot(slot: number): string {
  return String(slot).padStart(2, '0');
}

/**
 * The guest's vsock context id, which is the instance number plus two.
 *
 * A derivation rather than a reported value, and a closed one: cvd's default
 * CID is `3 + base_instance_num - 1`, adjusted per instance by
 * `cid + num - base`, which collapses to `num + 2`. Upstream's own flag
 * documentation states the same result, "the guest vsock cid is 2 + instance
 * number".
 *
 * The formula holds here because the override cannot be reached. `cvd` accepts
 * `--vsock_guest_cid` only as an assemble flag, and the launch-config schema
 * CCO submits has exactly one vsock field, `guest_group`. There is no request
 * this API can express that changes the answer, so the derivation is accurate
 * and not a guess. If that schema ever gains such a field, this becomes one.
 */
function vsockCid(slot: number): number {
  return slot + 2;
}

/**
 * Display sizes arrive as the host rendered them, `720 x 1280 ( 320 )`. The
 * dimension is the part worth reading and it takes a multiplication sign;
 * anything that does not parse passes through untouched rather than being
 * reshaped into a form it may not have.
 */
const DISPLAY_SIZE = /^\s*(\d+)\s*x\s*(\d+)/;

function displayTracks(displays: readonly string[]): DisplayTrack[] {
  return displays.map((raw, index) => {
    const size = DISPLAY_SIZE.exec(raw);
    return {
      label: `display ${index}`,
      size: size ? `${size[1]} × ${size[2]}` : raw.trim(),
    };
  });
}

/**
 * Identity, in the order the reader needs it: where it sits, what it belongs
 * to, what it is called, and what addresses it.
 *
 * A row whose value the instance did not report is omitted rather than filled
 * with a placeholder. An absent slot means the host published no serial to
 * recover it from, and a TAP name derived from a slot that does not exist would
 * name a device that does not exist either.
 */
function identityRows(ctx: ReachabilityContext): IdentityRow[] {
  const rows: IdentityRow[] = [];
  const slot = ctx.instance.slot ?? 0;

  if (slot > 0) {
    rows.push({label: 'slot', value: padSlot(slot)});
  }
  rows.push({label: 'group', value: ctx.deviceName});
  rows.push({label: 'instance', value: ctx.instance.name});
  if (ctx.instance.webrtcDeviceId) {
    rows.push({label: 'operator id', value: ctx.instance.webrtcDeviceId});
  }
  if (slot > 0) {
    rows.push({label: 'tap', value: `cvd-etap-${padSlot(slot)}`});
    rows.push({label: 'vsock cid', value: String(vsockCid(slot))});
  }
  return rows;
}

function reachRows(ctx: ReachabilityContext, tracks: readonly DisplayTrack[]): ReachRow[] {
  const port = ctx.instance.adbPort ?? 0;
  const rows: ReachRow[] = [
    {
      channel: 'adb',
      // The port records provenance and is not a dial target. The host firewall
      // opens the first instance's port alone, and on a fragmented host slot 1
      // may belong to another group or to none. Every instance is reachable
      // over the operator's bridge, which is instance-addressed and works in
      // every ingress mode, so the bridge is what the row names and the port is
      // what it was derived from.
      detail: port > 0 ? `${port} ↔ WebSocket bridge` : '↔ WebSocket bridge',
      ridesInside: 'SOME/IP, tunnelled over adb reverse',
    },
  ];

  // Signaling addresses the INSTANCE, by the id the operator registered it
  // under. Without that id there is nothing to address, so the row is dropped
  // rather than rendered pointing at nothing.
  if (ctx.instance.webrtcDeviceId) {
    rows.push({
      channel: 'WebRTC',
      detail:
        tracks.length > 0
          ? `→ this instance · ${tracks.length} display ${tracks.length === 1 ? 'track' : 'tracks'}`
          : '→ this instance',
      tracksInside: tracks.length > 0 ? tracks : undefined,
    });
  }
  return rows;
}

/**
 * Who a vsock tag actually reaches, which depends first on whether the layer
 * carrying it exists. Naming a per-group daemon unconditionally would assert
 * the very thing `auto` leaves open.
 */
const VSOCK_COMPANY: Record<ReachabilityContext['vsockLayer'], string> = {
  on: 'this group only, one daemon per group enumerating only its members',
  off: 'nobody. With no daemon, vsock reaches the host and no sibling VM',
  unresolved: 'this group only, if the launch created the layer at all',
};

/**
 * The two facts nobody reading a device list would guess, stated as the
 * company each medium keeps.
 *
 * Same host does not mean same radio: every group runs its own netsim,
 * rootcanal, pica and casimir on ports offset from its own lowest slot, so two
 * groups on one host cannot hear each other however close their slots are.
 *
 * Same host always means same wire: every TAP hangs off the same two bridges,
 * so unrelated single-instance devices are adjacent at layer 2.
 */
function shareRows(ctx: ReachabilityContext): ShareRow[] {
  const rows: ShareRow[] = [];

  if (ctx.radio) {
    rows.push({
      label: 'radio',
      value: `rootcanal :${ctx.radio.rootcanalHci} · pica :${ctx.radio.picaUci} · anchored at slot ${padSlot(ctx.radio.anchorSlot)}`,
      with:
        ctx.peers.length > 0
          ? `${ctx.peers.join(', ')}, and no other group on this host`
          : 'no peer, and no other group on this host',
    });
  }

  rows.push({
    label: 'vsock',
    value: ctx.vsockLabel || 'no peer to reach',
    with: VSOCK_COMPANY[ctx.vsockLayer],
  });

  rows.push({
    label: 'the wire',
    value: 'cvd-ebr · cvd-wbr',
    with:
      ctx.hostInstances > 1
        ? `all ${ctx.hostInstances} placed instances on ${ctx.hostName}, every group included`
        : `every instance on ${ctx.hostName}`,
  });

  return rows;
}

export function instanceReachability(ctx: ReachabilityContext): InstanceReachability {
  const tracks = displayTracks(ctx.instance.displays ?? []);
  const slot = ctx.instance.slot ?? 0;

  return {
    boxLabel: `${ctx.deviceName} / ${ctx.instance.name}`,
    slotLabel: slot > 0 ? `slot ${padSlot(slot)}` : '',
    hostName: ctx.hostName,
    identity: identityRows(ctx),
    reach: reachRows(ctx, tracks),
    shares: shareRows(ctx),
  };
}
