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

import {describe, expect, it} from 'vitest';

import type {DeviceInstance} from './device.model';
import type {ReachabilityContext} from './reachability';
import {instanceReachability} from './reachability';
import {radioRail} from './slot-space';

function instance(overrides: Partial<DeviceInstance> = {}): DeviceInstance {
  return {
    name: 'phone',
    state: 'Running',
    webrtcDeviceId: 'cvdgroup-phone-5',
    adbPort: 6524,
    slot: 5,
    displays: ['1080 x 2400 ( 420 )'],
    ...overrides,
  };
}

function context(overrides: Record<string, unknown> = {}): ReachabilityContext {
  return {
    instance: instance(),
    deviceName: 'dbg-1',
    hostName: 'cf-host-07',
    peers: ['watch'],
    radio: radioRail([5, 8]),
    vsockLabel: 'vsock: untagged mesh, all 2',
    vsockLayer: 'on',
    hostInstances: 7,
    ...overrides,
  };
}

function rowValue(rows: ReadonlyArray<{label: string; value: string}>, label: string): string {
  return rows.find(row => row.label === label)?.value ?? '';
}

describe('instanceReachability', () => {
  describe('identity', () => {
    it('derives the TAP name and the vsock CID from the slot', () => {
      const {identity} = instanceReachability(context());

      // CID is instance number + 2, and the TAP is named for the slot itself.
      // Both are derivations rather than reported values, so a change to either
      // formula has to break here rather than on someone's screen.
      expect(rowValue(identity, 'slot')).toBe('05');
      expect(rowValue(identity, 'tap')).toBe('cvd-etap-05');
      expect(rowValue(identity, 'vsock cid')).toBe('7');
    });

    it('omits the slot-derived rows entirely when the slot never resolved', () => {
      const {identity} = instanceReachability(
        context({instance: instance({slot: undefined, adbPort: undefined})})
      );

      // The rows are absent, not blank. A TAP named for a slot that does not
      // exist would name a device that does not exist, and CID 2 would be a
      // fabricated address.
      expect(identity.map(row => row.label)).not.toContain('tap');
      expect(identity.map(row => row.label)).not.toContain('vsock cid');
      expect(identity.map(row => row.label)).not.toContain('slot');
      expect(rowValue(identity, 'instance')).toBe('phone');
    });

    it('drops the operator id rather than naming an unregistered instance', () => {
      const {identity} = instanceReachability(
        context({instance: instance({webrtcDeviceId: undefined})})
      );

      expect(identity.map(row => row.label)).not.toContain('operator id');
    });
  });

  describe('reachability', () => {
    it('names the bridge and carries the port as its provenance', () => {
      const {reach} = instanceReachability(context());
      const adb = reach.find(row => row.channel === 'adb');

      expect(adb?.detail).toBe('6524 ↔ WebSocket bridge');
      // SOME/IP nests inside adb rather than sitting beside it: guests are
      // behind masquerade with no inbound DNAT, so it cannot be its own edge.
      expect(adb?.ridesInside).toContain('SOME/IP');
    });

    it('still names the bridge when no port was ever recovered', () => {
      const {reach} = instanceReachability(context({instance: instance({adbPort: undefined})}));

      // The bridge is instance-addressed and works regardless, so the row
      // survives; what goes is the number, not the channel.
      expect(reach.find(row => row.channel === 'adb')?.detail).toBe('↔ WebSocket bridge');
    });

    it('counts display tracks inside the one WebRTC connection', () => {
      const {reach} = instanceReachability(
        context({
          instance: instance({displays: ['1080 x 2400 ( 420 )', '720 x 1280 ( 320 )']}),
        })
      );
      const webrtc = reach.find(row => row.channel === 'WebRTC');

      // Two depths, not three: the displays are tracks nested in this row, not
      // a termination beyond it.
      expect(webrtc?.detail).toBe('→ this instance · 2 display tracks');
      expect(webrtc?.tracksInside).toHaveLength(2);
      expect(webrtc?.tracksInside?.[0]).toEqual({label: 'display 0', size: '1080 × 2400'});
    });

    it('singularises one track and drops the clause at none', () => {
      expect(
        instanceReachability(context()).reach.find(row => row.channel === 'WebRTC')?.detail
      ).toBe('→ this instance · 1 display track');

      expect(
        instanceReachability(context({instance: instance({displays: []})})).reach.find(
          row => row.channel === 'WebRTC'
        )?.detail
      ).toBe('→ this instance');
    });

    it('passes a display description through unchanged when it does not parse', () => {
      const {reach} = instanceReachability(
        context({instance: instance({displays: ['whatever the host said']})})
      );

      expect(reach.find(row => row.channel === 'WebRTC')?.tracksInside?.[0].size).toBe(
        'whatever the host said'
      );
    });

    it('drops the WebRTC row when nothing registered the instance', () => {
      const {reach} = instanceReachability(
        context({instance: instance({webrtcDeviceId: undefined})})
      );

      expect(reach.map(row => row.channel)).toEqual(['adb']);
    });
  });

  describe('shares', () => {
    it('anchors the radio at the group lowest slot and says no group can hear it', () => {
      const {shares} = instanceReachability(context());
      const radio = shares.find(row => row.label === 'radio');

      // Slots 5 and 8 anchor at 5, so the offset is 4. This is why a port is
      // never printed without the slot it came from.
      expect(radio?.value).toContain('rootcanal :7304');
      expect(radio?.value).toContain('anchored at slot 05');
      expect(radio?.with).toContain('watch');
      expect(radio?.with).toContain('no other group on this host');
    });

    it('omits the radio row when no slot in the group resolved', () => {
      const {shares} = instanceReachability(context({radio: null}));

      expect(shares.map(row => row.label)).not.toContain('radio');
    });

    it('says the whole host shares the wire, counting every group', () => {
      const {shares} = instanceReachability(context());
      const wire = shares.find(row => row.label === 'the wire');

      expect(wire?.value).toBe('cvd-ebr · cvd-wbr');
      expect(wire?.with).toContain('all 7 placed instances on cf-host-07');
    });

    it('never claims a per-group daemon the launch may not have started', () => {
      const on = instanceReachability(context({vsockLayer: 'on'}));
      const off = instanceReachability(context({vsockLayer: 'off'}));
      const unresolved = instanceReachability(context({vsockLayer: 'unresolved'}));

      const vsockOf = (r: ReturnType<typeof instanceReachability>): string =>
        r.shares.find(row => row.label === 'vsock')?.with ?? '';

      expect(vsockOf(on)).toContain('one daemon per group');
      expect(vsockOf(off)).toContain('no sibling VM');
      expect(vsockOf(unresolved)).toContain('if the launch created the layer');
      // Three different sentences. A single one would be wrong in at least two
      // of the three states.
      expect(new Set([vsockOf(on), vsockOf(off), vsockOf(unresolved)]).size).toBe(3);
    });
  });

  it('labels the containment box with the group, the instance and the slot', () => {
    const reach = instanceReachability(context());

    expect(reach.boxLabel).toBe('dbg-1 / phone');
    expect(reach.slotLabel).toBe('slot 05');
    expect(reach.hostName).toBe('cf-host-07');
  });

  it('leaves the slot label empty rather than inventing one', () => {
    const reach = instanceReachability(context({instance: instance({slot: 0})}));

    expect(reach.slotLabel).toBe('');
  });
});
