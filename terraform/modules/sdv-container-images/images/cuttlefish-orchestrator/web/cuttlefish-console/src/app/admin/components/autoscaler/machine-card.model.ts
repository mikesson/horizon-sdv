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

import type {AutoscalerSnapshot, FleetHost, FleetPool} from '../../models/autoscaler.model';
import {reclaims} from '../../models/autoscaler.model';
import type {NotchKind} from './fleet-band.model';
import {
  buildFleetRuler,
  failingTermPhrase,
  fleetHeadline,
  ordinal,
  refusalPhrase,
} from './fleet-band.model';

/**
 * One machine of the drawing, everything it says already composed.
 *
 * Derived here rather than in the components so the rules that cost defects
 * (a device sized by its instances, totals over backed slots, a lease-lowering
 * never drawn as permitted under provision_only) are assertable as arithmetic.
 * The components render this and compute nothing.
 */

/** How much fidelity the machine count affords. */
export type CanvasTier = 'cards' | 'compact' | 'clusters';

/** What one machine is doing. One value per machine, in the priority below. */
export type MachineState = 'arriving' | 'leaving' | 'free' | 'carrying' | 'idle' | 'ghost';

/** One cell of a machine's interior. */
interface CanvasSlot {
  readonly kind: NotchKind | 'would';
  /** Instance slots this cell spans. Devices are sized; everything else is 1. */
  readonly span: number;
  readonly label: string;
  /** The state in words. Colour alone may not signal it. */
  readonly title: string;
}

/**
 * A device whose instances take the whole machine, drawn as one block.
 *
 * Its instances share the machine's netsim and rootcanal radio medium, so
 * placement fences the machine to it and a stranger's device would join their
 * radio. Slot cells would draw a machine that could still be shared.
 */
interface GroupBlock {
  readonly name: string;
  readonly instances: number;
  readonly note: string;
}

/** One machine, everything it says already composed. */
export interface MachineCard {
  readonly key: string;
  /** Empty for a machine that does not exist yet, so there is no page to open. */
  readonly hostId: string;
  readonly name: string;
  readonly shape: string;
  /** CPU, or the accelerator and how many. */
  readonly compute: string;
  readonly accelerated: boolean;
  readonly state: MachineState;
  /** The state, always as a word. */
  readonly word: string;
  /** `1 of 10 backed · 12 promised`, both terms, never a percentage. */
  readonly slotLine: string;
  readonly slots: readonly CanvasSlot[];
  readonly group: GroupBlock | null;
  /** Everything else the machine has to say, in reading order. */
  readonly lines: readonly string[];
  /** Its place in the order a reduction would reclaim; 0 = not in it. */
  readonly order: number;
}

export const STATE_WORD: Record<MachineState, string> = {
  arriving: 'arriving',
  leaving: 'giving back',
  free: 'free machine · ready for a device',
  carrying: 'carrying devices',
  idle: 'not free',
  ghost: 'not bought yet',
};

/** Statuses placement already counts, because the machine is coming up. */
const ARRIVING: readonly string[] = ['pending', 'provisioning', 'starting'];

/** `6m`, `2h`. A duration a reader can act on without arithmetic. */
function spanOf(milliseconds: number): string {
  const minutes = Math.max(0, Math.round(milliseconds / 60_000));
  return minutes < 120 ? `${minutes}m` : `${Math.round(minutes / 60)}h`;
}

/** Never a mark without its word. Every cell state reads as text too. */
function slotTitle(kind: NotchKind | 'would', label: string, span: number): string {
  const slots = `${span} instance ${span === 1 ? 'slot' : 'slots'}`;
  switch (kind) {
    case 'occupied':
      return `${label}, ${slots}`;
    case 'error-held':
      return `${label}, in error, still holding ${slots}`;
    case 'unbacked':
      return 'unbacked, promised by the machine but not backed by its image';
    case 'would':
      return 'a slot this machine would add once it is bought';
    default:
      return 'free, so a device can start here now';
  }
}

function slotsFor(host: FleetHost): CanvasSlot[] {
  return buildFleetRuler(host).map(notch => ({
    kind: notch.kind,
    span: notch.span,
    label: notch.label,
    title: slotTitle(notch.kind, notch.label, notch.span),
  }));
}

/**
 * A device that took the whole machine, or nothing.
 *
 * One occupant over more than one slot is exclusive by placement's own rule
 * (`Instances > 1` fences the machine), so its card interior is that one block
 * rather than a slot each.
 */
function groupFor(host: FleetHost): GroupBlock | null {
  const only = host.occupants.length === 1 ? host.occupants[0] : null;
  if (!only || only.instances < 2) {
    return null;
  }
  return {
    name: only.name,
    instances: only.instances,
    note: `${only.instances} instances · takes the whole machine, exclusive`,
  };
}

export function stateOf(host: FleetHost, lowers: boolean): MachineState {
  if (ARRIVING.includes(host.status)) {
    return 'arriving';
  }
  // Only a mode that may lower a lease can be drawn as giving a machine back.
  if (host.shortenOrder > 0 && lowers) {
    return 'leaving';
  }
  if (host.standby) {
    return 'free';
  }
  return host.liveInstances + host.errorInstances > 0 ? 'carrying' : 'idle';
}

/** The machine's own sentences, after the state word. */
function linesFor(
  host: FleetHost,
  state: MachineState,
  snap: AutoscalerSnapshot,
  lowers: boolean
): string[] {
  const lines: string[] = [];
  if (state === 'arriving') {
    lines.push(
      `started ${spanOf(snap.now.getTime() - host.createdAt.getTime())} ago. A machine takes minutes to boot, which is the whole argument for keeping free ones warm`
    );
  }
  if (state === 'free') {
    lines.push('warm, so the next device starts on it now instead of waiting for a boot');
  }
  if (state === 'idle') {
    lines.push(failingTermPhrase(host.failingTerm));
  }
  if (host.expiresAt) {
    lines.push(`ends in ${spanOf(host.expiresAt.getTime() - snap.now.getTime())}`);
  }
  if (host.shortenOrder > 0) {
    const within = host.shortenOrder <= snap.limits.maxShortensPerCycle;
    const place = within ? `${ordinal(host.shortenOrder)} to give back` : 'later in that order';
    lines.push(
      lowers
        ? `${place}, and its lease is brought forward, which deletes nothing itself`
        : `${place} if a reduction were permitted, and it is not in this mode`
    );
  }
  if (host.failingTerm === 'lease' && snap.gate.deficit > 0 && snap.fleet.status.mode !== 'off') {
    lines.push('renewing this one is free, because it is taken before any machine is bought');
  }
  const refusal = refusalPhrase(host, snap.limits.provisionLeadSeconds, snap.now);
  if (refusal) {
    lines.push(refusal);
  }
  return lines.filter(line => line !== '');
}

export function cardFor(host: FleetHost, snap: AutoscalerSnapshot, tier: CanvasTier): MachineCard {
  const lowers = reclaims(snap.fleet.status.mode);
  const state = stateOf(host, lowers);
  const group = groupFor(host);
  return {
    key: host.id,
    hostId: host.id,
    name: host.name,
    shape: `${host.machineType} · ${host.zone}`,
    compute: host.gpuCount > 0 ? `${host.gpuType} × ${host.gpuCount}` : 'CPU',
    accelerated: host.gpuCount > 0,
    state,
    word: STATE_WORD[state],
    slotLine: fleetHeadline(host),
    slots: tier === 'cards' && !group ? slotsFor(host) : [],
    group: tier === 'cards' ? group : null,
    lines: linesFor(host, state, snap, lowers),
    order: host.shortenOrder,
  };
}

/** What a machine bought for this pool right now would be, and what it would hold. */
interface WouldBuy {
  readonly name: string;
  readonly shape: string;
  readonly slots: number;
  readonly slotLine: string;
}

/**
 * What a machine bought for this pool right now would be built as.
 *
 * The pool's own order is used where that order fell through. The walk lands
 * below the shape the pool prefers only after finding that one stocked out, so
 * the card can name both. A pool with no order of its own builds the catalog's
 * default shape. The snapshot names that shape only for the pool every create
 * still lands in, so any other pool says the shape is decided when it is
 * bought instead of borrowing a figure that is not about it.
 */
function wouldBuy(pool: FleetPool, snap: AutoscalerSnapshot, isDefaultPool: boolean): WouldBuy {
  const landing = pool.ladder.findIndex(rung => rung.landing);
  if (landing > 0) {
    return {
      name: `would buy ${pool.ladder[landing].machineType}`,
      shape: `${pool.ladder[0].machineType} stocked out`,
      slots: pool.ladder[landing].slots,
      slotLine: `${pool.ladder[landing].slots} slots once it is bought`,
    };
  }
  if (landing === 0) {
    return {
      name: 'would buy this one',
      shape: pool.ladder[0].machineType,
      slots: pool.ladder[0].slots,
      slotLine: `${pool.ladder[0].slots} slots once it is bought`,
    };
  }
  if (!isDefaultPool) {
    return {
      name: 'would buy a machine',
      shape: 'this pool’s shape is decided when it is bought',
      slots: 0,
      slotLine: 'its slots are decided with its shape',
    };
  }
  return {
    name: 'would buy this one',
    shape: snap.densityMachineType || 'the default shape',
    slots: snap.effectiveDensity,
    slotLine: `${snap.effectiveDensity} slots once it is bought`,
  };
}

/**
 * One card per machine this pool is short of, at the density it would buy at.
 *
 * Sized by the pool's deficit. One card per target instead would say a pool
 * with three machines standing against a target of three is empty.
 */
export function ghostsFor(
  pool: FleetPool,
  snap: AutoscalerSnapshot,
  tier: CanvasTier,
  isDefaultPool: boolean
): MachineCard[] {
  const would = wouldBuy(pool, snap, isDefaultPool);
  const slots: CanvasSlot[] = Array.from({length: would.slots}, () => ({
    kind: 'would' as const,
    span: 1,
    label: 'slot',
    title: slotTitle('would', 'slot', 1),
  }));
  return Array.from({length: pool.deficit}, (_, i) => ({
    key: `would-${pool.pool}-${i}`,
    hostId: '',
    name: would.name,
    shape: would.shape,
    compute: '',
    accelerated: false,
    state: 'ghost' as const,
    word: STATE_WORD.ghost,
    slotLine: would.slotLine,
    slots: tier === 'cards' ? slots : [],
    group: null,
    lines:
      snap.fleet.status.mode === 'off'
        ? ['the mode permits no buying, so this machine stays a hole']
        : ['a create takes minutes, so the gap is open until it boots'],
    order: 0,
  }));
}
