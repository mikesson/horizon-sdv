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
 * Which instances in a group can reach each other over vsock.
 *
 * Two questions, in order, and the second only matters if the first says yes.
 *
 * FIRST: does the group have a guest-to-guest vsock layer at all? Only the
 * per-group `vhost_device_vsock` daemon can carry VM-to-VM vsock, since the
 * kernel's own vsock reaches the host and nothing else, and that daemon runs
 * only when `vhost_user_vsock` resolves true. It defaults to `auto`, which the
 * host resolves at launch from the guest image and the guest architecture, and on
 * x86_64 `auto` is false unless the image itself asks for it. So the common
 * case is a group with NO guest-to-guest vsock, and a mesh drawn there would be
 * the loudest possible false claim.
 *
 * SECOND, and only where the layer exists: tags partition inside the daemon.
 * Untagged VMs mesh with each other and a tagged VM cannot reach an untagged
 * one, so configuring nothing yields full connectivity, which is why absence
 * must never render as absence at that level.
 *
 * Where `auto` is unresolved the tags are still worth drawing, because they are
 * what the launch asked for; what changes is that they are labelled as declared
 * rather than as achieved. The host never reports the resolved value back, so
 * the console cannot close that gap by reading harder.
 *
 * Everything below is DECLARED, read from the launch config. It says two
 * instances share a medium; it never says they exchanged a packet.
 */

/** One tag, and the instances that named it. */
interface VsockGroup {
  readonly tag: string;
  readonly members: readonly string[];
}

/**
 * Whether the group has a guest-to-guest vsock layer.
 *
 * `unresolved` is the accurate answer for `auto`, which is also the default.
 * The host decides it at launch and never reports the decision back.
 */
type VsockLayer = 'on' | 'off' | 'unresolved';

export interface VsockMembership {
  readonly layer: VsockLayer;
  /**
   * `none` when the group is too small for the question to mean anything, the
   * launch config said nothing this console can read, or the layer is off and
   * the tags are therefore inert.
   */
  readonly kind: 'none' | 'mesh' | 'tagged' | 'mixed';
  /** The band or bracket caption, e.g. `vsock: untagged mesh, all 3`. */
  readonly label: string;
  /**
   * What the caption cannot be trusted to mean on its own, or empty where it
   * can. This carries the whole difference between a layer that exists and one
   * the launch may never have created.
   */
  readonly caveat: string;
  readonly groups: readonly VsockGroup[];
  readonly untagged: readonly string[];
  /**
   * Per-instance text, populated only where membership DIFFERS across the
   * group. A uniform group states its membership once in the band label; N
   * identical badges beneath it repeat that N times, and the commonest group is
   * the one that would suffer. Where membership is mixed the exclusion is a
   * fact about the instance rather than an inference from a bracket's span, so
   * every instance carries one, including the untagged.
   */
  readonly badges: ReadonlyMap<string, string>;
}

const NONE: VsockMembership = {
  layer: 'unresolved',
  kind: 'none',
  label: '',
  caveat: '',
  groups: [],
  untagged: [],
  badges: new Map(),
};

/**
 * What the launch config asked for, before the host resolves it.
 *
 * `auto` and absence are the same answer, undecided, because `auto` IS the
 * default. Anything else unrecognised is undecided too rather than assumed off:
 * a value this console does not know is not evidence of anything.
 */
function vhostUserVsockOf(entry: Record<string, unknown>): 'true' | 'false' | undefined {
  const declared = asRecord(asRecord(entry['vm'])?.['crosvm'])?.['vhost_user_vsock'];
  if (declared === 'true' || declared === true) {
    return 'true';
  }
  if (declared === 'false' || declared === false) {
    return 'false';
  }
  return undefined;
}

/**
 * The group's layer, which one instance can settle for all of them.
 *
 * The daemon is launched once per group by its first instance and enumerates
 * every member, so a group does not get a partial vsock layer. Where the
 * entries disagree the answer is unresolved rather than a majority verdict.
 * Disagreement means the config is not saying what this reads it as saying.
 */
function layerOf(entries: ReadonlyArray<Record<string, unknown>>): VsockLayer {
  if (entries.length === 0) {
    return 'unresolved';
  }
  const declared = entries.map(vhostUserVsockOf);
  if (declared.every(value => value === 'true')) {
    return 'on';
  }
  if (declared.every(value => value === 'false')) {
    return 'off';
  }
  return 'unresolved';
}

/** Where `auto` is in play, every membership below is a request, not a result. */
const UNRESOLVED_CAVEAT =
  'declared only, because vhost-user-vsock is auto here and the host resolves it at launch ' +
  'from the guest image and architecture';

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

/**
 * A launch config instance's declared vsock tags.
 *
 * `guest_group` carries a `+`-separated list, so one VM can sit in several
 * groups at once; splitting is what keeps a two-group VM from reading as a
 * single group literally named "g1+g2".
 */
function tagsOf(entry: Record<string, unknown>): string[] {
  const vsock = asRecord(asRecord(entry['connectivity'])?.['vsock']);
  const raw = vsock?.['guest_group'];
  if (typeof raw !== 'string') {
    return [];
  }
  return raw
    .split('+')
    .map(tag => tag.trim())
    .filter(tag => tag.length > 0);
}

/**
 * Pairs each observed instance with the tags its launch config declared.
 *
 * Matched by name, because that is the only identifier both sides agree on. A
 * config whose entries are all unnamed falls back to position, and then only
 * when the two arrays are the same length. A partial positional match would
 * silently attribute one instance's isolation to another, which is worse than
 * declining to draw.
 */
function tagsByInstance(
  instanceNames: readonly string[],
  entries: ReadonlyArray<Record<string, unknown>>
): Map<string, string[]> | undefined {
  const named = entries.filter(entry => typeof entry['name'] === 'string');

  if (named.length === entries.length && entries.length > 0) {
    const byName = new Map<string, string[]>();
    for (const entry of entries) {
      byName.set(entry['name'] as string, tagsOf(entry));
    }
    return new Map(instanceNames.map(name => [name, byName.get(name) ?? []]));
  }

  if (named.length === 0 && entries.length === instanceNames.length) {
    return new Map(instanceNames.map((name, i) => [name, tagsOf(entries[i])]));
  }

  return undefined;
}

/** Stable key for "the set of tags this instance declared". */
function tagKey(tags: readonly string[]): string {
  return [...tags].sort().join('+');
}

export function vsockMembership(
  instanceNames: readonly string[],
  envConfig: Record<string, unknown> | undefined
): VsockMembership {
  // One instance reaches nobody, so there is no reachability worth drawing.
  if (instanceNames.length < 2) {
    return NONE;
  }

  const rawInstances = envConfig?.['instances'];
  const entries = Array.isArray(rawInstances)
    ? rawInstances.map(asRecord).filter((e): e is Record<string, unknown> => e !== undefined)
    : [];

  // Answer the layer question first. With no daemon there is no guest-to-guest
  // vsock to partition, so the tags below would be describing the shape of
  // something that does not exist.
  const layer = layerOf(entries);
  if (layer === 'off') {
    return {
      ...NONE,
      layer,
      label: 'vsock: host only',
      caveat:
        'no guest-to-guest layer, because vhost-user-vsock is off, so the tags do nothing and ' +
        'each VM reaches the host alone',
    };
  }
  const caveat = layer === 'unresolved' ? UNRESOLVED_CAVEAT : '';

  // No config to read is not "no tags meant nothing". Nothing was tagged, and
  // untagged VMs mesh, so within a layer that exists the group has the
  // connectivity it would have had by configuring none, which is exactly the
  // case this drawing exists to make visible.
  const tags =
    entries.length === 0
      ? new Map(instanceNames.map(name => [name, [] as string[]]))
      : tagsByInstance(instanceNames, entries);

  if (!tags) {
    return NONE;
  }

  const tagged = instanceNames.filter(name => (tags.get(name) ?? []).length > 0);
  const untagged = instanceNames.filter(name => (tags.get(name) ?? []).length === 0);

  if (tagged.length === 0) {
    return {
      layer,
      kind: 'mesh',
      label: `vsock: untagged mesh, all ${instanceNames.length}`,
      caveat,
      groups: [],
      untagged,
      badges: new Map(),
    };
  }

  const byTag = new Map<string, string[]>();
  for (const name of tagged) {
    for (const tag of tags.get(name) ?? []) {
      byTag.set(tag, [...(byTag.get(tag) ?? []), name]);
    }
  }
  const groups: VsockGroup[] = [...byTag.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([tag, members]) => ({tag, members}));

  const tagNames = groups.map(group => group.tag).join(', ');
  const uniform =
    untagged.length === 0 &&
    new Set(instanceNames.map(name => tagKey(tags.get(name) ?? []))).size === 1;

  if (uniform) {
    return {
      layer,
      kind: 'tagged',
      label: `vsock: ${tagNames}, all ${instanceNames.length}`,
      caveat,
      groups,
      untagged,
      badges: new Map(),
    };
  }

  // Mixed. A tagged VM and an untagged one cannot talk, and that exclusion is a
  // fact about each instance rather than something to be inferred from where a
  // bracket happens to start and stop.
  const badges = new Map<string, string>();
  for (const name of instanceNames) {
    const own = tags.get(name) ?? [];
    badges.set(name, own.length > 0 ? own.join(', ') : `untagged, cannot reach ${tagNames}`);
  }

  return {layer, kind: 'mixed', label: `vsock: ${tagNames}`, caveat, groups, untagged, badges};
}
