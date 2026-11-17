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

import {beforeEach, describe, expect, it} from 'vitest';
import {fromSpec, toSpec} from './advanced-draft.mapper';
import {clearDraft, loadDraft, saveDraft} from './advanced-draft.storage';

const VALID_CONFIG = JSON.stringify({
  instances: [{'@import': 'phone', name: 'device-1'}],
});

/** What the editor wrote before it could hold more than one screen. */
const SINGLE_DISPLAY_DRAFT = JSON.stringify({
  config: {
    instances: [
      {
        '@import': 'auto',
        name: 'device-1',
        vm: {cpus: 4, memory_mb: 4096, use_sdcard: true},
        graphics: {
          displays: [{width: 1080, height: 600, dpi: 120, refresh_rate_hertz: 60}],
          gpu_mode: 'gfxstream',
        },
      },
    ],
  },
  hostId: 'host-1',
  savedAt: '2026-08-01T10:00:00.000Z',
});

describe('advanced draft storage', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('saves and loads a valid draft, round-tripping host and build selection', () => {
    saveDraft(VALID_CONFIG, 'host-1', 'build-1', true, 'auto');
    const draft = loadDraft();
    expect(draft).not.toBeNull();
    expect(draft?.hostId).toBe('host-1');
    expect(draft?.buildId).toBe('build-1');
    expect(draft?.config.instances[0]?.name).toBe('device-1');
    expect(Date.parse(draft?.savedAt ?? '')).not.toBeNaN();
  });

  it('does not persist configs that fail schema validation', () => {
    saveDraft(JSON.stringify({instances: []}), 'host-1', 'build-1', true, 'auto'); // min(1) violated
    expect(loadDraft()).toBeNull();
  });

  it('does not persist unparseable JSON', () => {
    saveDraft('not json{', 'host-1', 'build-1', true, 'auto');
    expect(loadDraft()).toBeNull();
  });

  it('returns null for corrupt stored data', () => {
    localStorage.setItem('cvd-advanced-draft', '{"config": "nope"}');
    expect(loadDraft()).toBeNull();
  });

  it('restores a draft written before multi-display, as one screen', () => {
    localStorage.setItem('cvd-advanced-draft', SINGLE_DISPLAY_DRAFT);
    const draft = loadDraft();
    expect(draft).not.toBeNull();
    // Restoring is loadDraft + fromSpec; the stored spec is the whole state.
    const restored = fromSpec(draft?.config ?? {instances: []});
    expect(restored.instances[0].displays).toEqual([
      {width: 1080, height: 600, dpi: 120, refreshRate: 60},
    ]);
  });

  it('round-trips per-instance builds and the host package through storage', () => {
    const config = toSpec(
      fromSpec({
        common: {host_package: 'gs://b/ivi/cvd-host_package.tar.gz'},
        instances: [
          {name: 'core', '@build': 'b-core'},
          {name: 'ivi', disk: {default_build: 'gs://b/ivi/img.zip'}},
        ],
      })
    );
    saveDraft(JSON.stringify(config), 'host-1', '', false, 'auto');

    // A schema without @build would refuse the draft and drop it silently.
    const stored = loadDraft();
    expect(stored).not.toBeNull();
    const restored = fromSpec(stored?.config ?? {instances: []}, {
      sameBuild: stored?.sameBuild,
      buildId: stored?.buildId,
    });
    expect(restored.hostPackage).toBe('gs://b/ivi/cvd-host_package.tar.gz');
    expect(restored.sameBuild).toBe(false);
    expect(restored.instances.map(inst => inst.build)).toEqual(['b-core', 'gs://b/ivi/img.zip']);
  });

  it('round-trips a shared URL build through storage', () => {
    const draft = fromSpec({instances: [{name: 'a'}, {name: 'b'}]});
    const config = toSpec({...draft, deviceBuild: 'gs://b/img.zip'});
    saveDraft(JSON.stringify(config), '', '', true, 'auto');

    const stored = loadDraft();
    expect(stored?.sameBuild).toBe(true);
    const restored = fromSpec(stored?.config ?? {instances: []}, {
      sameBuild: stored?.sameBuild,
      buildId: stored?.buildId,
    });
    expect(restored).toMatchObject({sameBuild: true, deviceBuild: 'gs://b/img.zip'});
  });

  it('round-trips a shared registered build through storage', () => {
    saveDraft(VALID_CONFIG, '', 'build-1', true, 'auto');
    const stored = loadDraft();
    const restored = fromSpec(stored?.config ?? {instances: []}, {
      sameBuild: stored?.sameBuild,
      buildId: stored?.buildId,
    });
    expect(restored).toMatchObject({sameBuild: true, deviceBuild: 'build-1'});
  });

  it('round-trips the capacity choice through storage', () => {
    saveDraft(VALID_CONFIG, '', '', true, 'standard');
    const stored = loadDraft();
    expect(stored?.capacity).toBe('standard');
    const restored = fromSpec(stored?.config ?? {instances: []}, {capacity: stored?.capacity});
    expect(restored.capacity).toBe('standard');
  });

  it('restores a draft saved before the capacity choice as auto', () => {
    localStorage.setItem('cvd-advanced-draft', SINGLE_DISPLAY_DRAFT);
    const stored = loadDraft();
    expect(stored?.capacity).toBeUndefined();
    expect(fromSpec(stored?.config ?? {instances: []}, {capacity: stored?.capacity}).capacity).toBe(
      'auto'
    );
  });

  it('clearDraft removes the stored draft', () => {
    saveDraft(VALID_CONFIG, 'host-1', 'build-1', true, 'auto');
    clearDraft();
    expect(loadDraft()).toBeNull();
  });
});
