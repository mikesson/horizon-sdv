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

import {vsockMembership} from './vsock-membership';

/** One launch-config instance entry, in the shape env_config carries. */
function entry(
  name: string,
  options: {tags?: string; layer?: string} = {}
): Record<string, unknown> {
  return {
    name,
    ...(options.tags ? {connectivity: {vsock: {guest_group: options.tags}}} : {}),
    ...(options.layer ? {vm: {crosvm: {vhost_user_vsock: options.layer}}} : {}),
  };
}

function config(...instances: Array<Record<string, unknown>>): Record<string, unknown> {
  return {instances};
}

describe('vsockMembership', () => {
  it('has nothing to say about a group with no peer', () => {
    expect(vsockMembership(['solo'], config(entry('solo', {layer: 'true'}))).kind).toBe('none');
  });

  describe('the layer question, which comes first', () => {
    it('reports the layer off and refuses to draw a partition through it', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(
          entry('phone', {tags: 'g1', layer: 'false'}),
          entry('watch', {tags: 'g1', layer: 'false'})
        )
      );

      // The tags are real in the config and inert in fact: with no daemon there
      // is no guest-to-guest vsock for a tag to partition.
      expect(membership.layer).toBe('off');
      expect(membership.kind).toBe('none');
      expect(membership.label).toBe('vsock: host only');
      expect(membership.caveat).toContain('the tags do nothing');
      expect(membership.groups).toHaveLength(0);
    });

    it('treats an absent switch as unresolved, because auto is the default', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone'), entry('watch'))
      );

      expect(membership.layer).toBe('unresolved');
      expect(membership.caveat).toContain('auto');
    });

    it('treats an explicit auto exactly as it treats an absent one', () => {
      const explicit = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', {layer: 'auto'}), entry('watch', {layer: 'auto'}))
      );

      expect(explicit.layer).toBe('unresolved');
    });

    it('settles the layer only when every instance agrees', () => {
      const disagreeing = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', {layer: 'true'}), entry('watch', {layer: 'false'}))
      );

      // One daemon serves the whole group, so a config that says two different
      // things is not saying what this reads it as saying.
      expect(disagreeing.layer).toBe('unresolved');
    });

    it('drops the caveat once the layer is certain', () => {
      const on = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', {layer: 'true'}), entry('watch', {layer: 'true'}))
      );

      expect(on.layer).toBe('on');
      expect(on.caveat).toBe('');
    });
  });

  describe('the partition, once a layer exists', () => {
    const on = {layer: 'true'};

    it('draws the mesh nobody configured, because that is the connectivity you got', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', on), entry('watch', on))
      );

      expect(membership.kind).toBe('mesh');
      expect(membership.label).toBe('vsock: untagged mesh, all 2');
      expect(membership.badges.size).toBe(0);
    });

    it('says a uniform tag once rather than on every tile', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', {...on, tags: 'g1'}), entry('watch', {...on, tags: 'g1'}))
      );

      expect(membership.kind).toBe('tagged');
      expect(membership.label).toBe('vsock: g1, all 2');
      expect(membership.badges.size).toBe(0);
    });

    it('badges every instance in a mixed group, including the untagged', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', {...on, tags: 'g1'}), entry('watch', on))
      );

      expect(membership.kind).toBe('mixed');
      expect(membership.badges.get('phone')).toBe('g1');
      // The exclusion is a fact about the instance, not something to infer from
      // where a bracket happens to stop.
      expect(membership.badges.get('watch')).toBe('untagged, cannot reach g1');
    });

    it('splits a multi-group tag rather than reading it as one name', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', {...on, tags: 'g1+g2'}), entry('watch', {...on, tags: 'g1'}))
      );

      expect(membership.groups.map(group => group.tag)).toEqual(['g1', 'g2']);
      expect(membership.groups.find(group => group.tag === 'g1')?.members).toEqual([
        'phone',
        'watch',
      ]);
    });

    it('matches config entries by name, not by position', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(entry('watch', {...on, tags: 'g1'}), entry('phone', on))
      );

      // Reversed on purpose: a positional read would tag the phone.
      expect(membership.badges.get('watch')).toBe('g1');
      expect(membership.badges.get('phone')).toContain('untagged');
    });

    it('declines rather than guessing when a partial name match is all there is', () => {
      const membership = vsockMembership(
        ['phone', 'watch'],
        config(entry('phone', {...on, tags: 'g1'}), {connectivity: {vsock: {guest_group: 'g2'}}})
      );

      // Attributing one instance's isolation to another is worse than not
      // drawing: this is the arm that refuses.
      expect(membership.kind).toBe('none');
    });
  });

  it('reads a group with no config at all as an unresolved mesh', () => {
    const membership = vsockMembership(['phone', 'watch'], undefined);

    // Nothing was tagged and untagged VMs mesh, so the partition is a mesh.
    // With no config there is no switch either, so it stays unresolved.
    expect(membership.kind).toBe('mesh');
    expect(membership.layer).toBe('unresolved');
  });
});
