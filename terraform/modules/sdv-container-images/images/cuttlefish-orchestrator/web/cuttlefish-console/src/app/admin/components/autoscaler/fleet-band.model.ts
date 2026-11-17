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

import type {FleetHost, HostClass, StandbyTerm} from '../../models/autoscaler.model';

/**
 * The fleet band's derivations, kept out of the component so they can be tested
 * as arithmetic rather than through a rendered ruler.
 *
 * The band inherits the topology view's grammar (ruler, notch, one referent
 * each) with one divergence stated here because it is the sort of thing that
 * gets "fixed" later. This ruler does NOT claim slot positions.
 *
 * That view draws one host and numbers every notch, because cvd allocates slots
 * lowest-free-first and a group placed on a churned host holds 2, 5 and 8.
 * Closing those holes would hide the fragmentation the ruler exists to show.
 * Recovering true positions needs each device's observed instance array. This
 * band draws twenty hosts and needs only their sizes, so it renders spans in
 * listing order and numbers nothing. No numbers is the visible promise that no
 * position is being claimed; the host page is where positions live.
 */

/** What one cell of a fleet ruler stands on. */
export type NotchKind =
  /** Held by a device that is running. */
  | 'occupied'
  /**
   * Held by a device row in error. NOT unbacked. The slot is real and the image
   * backs it, with a dead row sitting in it. Conflating the two would blame the
   * host image for a device failure.
   */
  | 'error-held'
  /** Backed by the image and unoccupied. */
  | 'free'
  /**
   * Inside the host's promised range but past what the image backs. The host
   * declared more slots than there are TAP sets to launch against.
   */
  | 'unbacked';

/** One cell of a host's ruler. */
export interface FleetNotch {
  readonly kind: NotchKind;
  /** Instance slots this cell spans. Devices are sized; everything else is 1. */
  readonly span: number;
  readonly label: string;
}

/**
 * One host's ruler: its devices sized by the slots they cost, then what is
 * free, then the whole unbacked tail.
 *
 * The tail is drawn in full and never sampled. Eliding six unbacked slots to
 * two would understate the over-admission by two thirds, and that number is the
 * only reason the tail is drawn at all.
 */
export function buildFleetRuler(host: FleetHost): FleetNotch[] {
  const notches: FleetNotch[] = [];
  for (const occupant of host.occupants) {
    notches.push({
      kind: occupant.errored ? 'error-held' : 'occupied',
      span: occupant.instances,
      label: occupant.name,
    });
  }
  // Held can exceed what the image backs when a host over-admitted AND filled
  // past the backed range. Free is then zero, never a negative count.
  const held = host.liveInstances + host.errorInstances;
  for (let i = 0; i < Math.max(0, host.backedSlots - held); i++) {
    notches.push({kind: 'free', span: 1, label: 'free'});
  }
  for (let i = 0; i < Math.max(0, host.maxDevices - host.backedSlots); i++) {
    notches.push({kind: 'unbacked', span: 1, label: 'unbacked'});
  }
  return notches;
}

/**
 * The host header's capacity line.
 *
 * `· N promised` renders only where the host promised more than the image
 * backs. That is the topology view's rule, for its reason. A phrase that
 * appears on every host stops being read on the host where it matters.
 */
export function fleetHeadline(host: FleetHost): string {
  const used = host.liveInstances + host.errorInstances;
  const backed = `${used} of ${host.backedSlots} backed`;
  return host.maxDevices > host.backedSlots ? `${backed} · ${host.maxDevices} promised` : backed;
}

/**
 * Why placement will not use this host, or empty when it will.
 *
 * A HOST property, never a notch state: degraded, exclusive or a lease running
 * out makes placement decline the whole host, so it belongs in the header the
 * way a lane header names its exception rather than smeared across every cell.
 * Free slots on a refused host stay drawn as free; this says why they are not
 * capacity.
 */
export function refusalPhrase(host: FleetHost, provisionLeadSeconds: number, now: Date): string {
  if (host.hostClass !== 'fleet') {
    return '';
  }
  if (host.failingTerm === 'not_degraded') {
    return 'degraded, so placement will not use it until remediation clears the condition';
  }
  if (host.failingTerm === 'lease' && host.expiresAt) {
    const minutes = Math.max(0, Math.round((host.expiresAt.getTime() - now.getTime()) / 60000));
    const lead = Math.round(provisionLeadSeconds / 60);
    return `lease ends in ${minutes}m, inside the ${lead}m provisioning lead, so a create cannot count on it`;
  }
  if (host.failingTerm === 'placeable') {
    return `${host.status}: placement lands on running and provisioning hosts only`;
  }
  return '';
}

/** Why an idle host is not counted, in the operator's terms rather than the predicate's. */
export function failingTermPhrase(term: StandbyTerm | ''): string {
  switch (term) {
    case 'class':
      return 'not the fleet’s, so nothing here counts or reclaims it';
    case 'placeable':
      return 'its status is not one placement lands on';
    case 'not_degraded':
      return 'degraded, so placement refuses it';
    case 'shareable':
      return 'exclusive, so placement will not add a stranger';
    case 'idle':
      return 'it carries devices';
    case 'lease':
      return 'its lease runs out inside the provisioning lead';
    default:
      return '';
  }
}

/** One class's slice of the band. */
export interface ClassGroup {
  readonly hostClass: HostClass;
  readonly hosts: FleetHost[];
  readonly backed: number;
  readonly live: number;
}

/**
 * The band's order: the fleet first and expanded, the other three present but
 * muted.
 *
 * Present so the totals reconcile against the hosts page; subordinate so nobody
 * reads them as capacity the target accounts for. A class with no hosts is left
 * out entirely rather than drawn as an empty band.
 */
const CLASS_ORDER: readonly HostClass[] = ['fleet', 'dedicated', 'batch', 'reserved'];

export function groupByClass(hosts: readonly FleetHost[]): ClassGroup[] {
  return CLASS_ORDER.map(hostClass => {
    const members = hosts.filter(h => h.hostClass === hostClass);
    return {
      hostClass,
      hosts: members,
      backed: members.reduce((sum, h) => sum + h.backedSlots, 0),
      live: members.reduce((sum, h) => sum + h.liveInstances, 0),
    };
  }).filter(group => group.hosts.length > 0);
}

/** The fleet band's totals. */
export interface BandTotals {
  readonly hosts: number;
  /**
   * Slots the fleet can actually run, Σ backed, never Σ max_devices. Summing
   * the promise is the exact claim the ruler exists to refute. On a fleet of
   * 16-slot hosts against a 10-slot image it overstates capacity by 60%.
   */
  readonly backed: number;
  readonly live: number;
  readonly errored: number;
  readonly devices: number;
}

export function bandTotals(hosts: readonly FleetHost[]): BandTotals {
  return {
    hosts: hosts.length,
    backed: hosts.reduce((sum, h) => sum + h.backedSlots, 0),
    live: hosts.reduce((sum, h) => sum + h.liveInstances, 0),
    errored: hosts.reduce((sum, h) => sum + h.errorInstances, 0),
    devices: hosts.reduce((sum, h) => sum + h.occupants.filter(o => !o.errored).length, 0),
  };
}

/**
 * The fleet band's own order.
 *
 * Under a surplus the reclamation order leads, because "which hosts does
 * lowering this take?" is the page's one opaque consequence and the server
 * answered it from the reclaimer's own listing. Everything else falls back to
 * name, which is stable across a refresh. Sorting by a state would move rows
 * under a reader every time one flipped.
 */
export function fleetOrder(hosts: readonly FleetHost[]): FleetHost[] {
  return [...hosts].sort((a, b) => {
    if (a.shortenOrder !== b.shortenOrder) {
      // 0 means "not next", so it sorts last rather than first.
      if (a.shortenOrder === 0) {
        return 1;
      }
      if (b.shortenOrder === 0) {
        return -1;
      }
      return a.shortenOrder - b.shortenOrder;
    }
    return a.name.localeCompare(b.name);
  });
}

/** `1st`, `2nd`, `3rd`. The ordinal a reclamation chip carries. */
export function ordinal(n: number): string {
  const tens = n % 100;
  if (tens >= 11 && tens <= 13) {
    return `${n}th`;
  }
  switch (n % 10) {
    case 1:
      return `${n}st`;
    case 2:
      return `${n}nd`;
    case 3:
      return `${n}rd`;
    default:
      return `${n}th`;
  }
}
