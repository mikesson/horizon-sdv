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
import {
  ArtifactSettingsSchema,
  DefaultsSettingsSchema,
  PlacementSettingsSchema,
  SpotSettingsSchema,
} from './settings.schema';

describe('settings schemas', () => {
  it('carries provenance through to the page', () => {
    const parsed = PlacementSettingsSchema.parse({
      zonePin: {value: 'europe-west4-a', is_set: true},
      quotaMargin: {value: 10, is_set: false},
      v1ZoneHint: {value: true, is_set: false},
    });

    expect(parsed.zonePin).toBe('europe-west4-a');
    expect(parsed.zonePinIsSet).toBe(true);
    // A margin of 10 an operator never chose reads identically on the wire;
    // only is_set separates it from one they did.
    expect(parsed.quotaMarginPercent).toBe(10);
    expect(parsed.quotaMarginIsSet).toBe(false);
  });

  it('reads a response with no is_set as defaulted rather than failing', () => {
    const parsed = SpotSettingsSchema.parse({
      autoDefault: {value: false},
      minUptime: {single_seconds: 600, batch_seconds: 3600},
    });

    expect(parsed.minUptimeSingleSeconds).toBe(600);
    expect(parsed.minUptimeIsSet).toBe(false);
    expect(parsed.autoDefaultIsSet).toBe(false);
  });

  it('flattens the paired upstream build into one provenance answer', () => {
    const parsed = DefaultsSettingsSchema.parse({
      gpuMachineType: {value: '', is_set: false},
      upstreamBuild: {branch: 'aosp-main', target: 'phone', is_set: true},
      dockerHost: {machine_type: 'n1-standard-8', ttl_hours: 12, max_ttl_hours: 168},
    });

    expect(parsed.upstreamBranch).toBe('aosp-main');
    expect(parsed.upstreamTarget).toBe('phone');
    expect(parsed.upstreamBuildIsSet).toBe(true);
    expect(parsed.gpuMachineTypeIsSet).toBe(false);
    // The docker-host pair rides the same section: the shape, the compiled
    // cap, and a missing is_set reading as defaulted.
    expect(parsed.dockerMachineType).toBe('n1-standard-8');
    expect(parsed.dockerTtlHours).toBe(12);
    expect(parsed.dockerMaxTtlHours).toBe(168);
    expect(parsed.dockerHostIsSet).toBe(false);
  });

  it('keeps the deployment default alongside the override', () => {
    const parsed = ArtifactSettingsSchema.parse({
      bugreport_bucket: '',
      recording_bucket: 'recordings',
      default_bucket: 'fallback',
      capture_on_error: true,
      is_set: true,
    });

    expect(parsed.bugreportBucket).toBe('');
    expect(parsed.defaultBucket).toBe('fallback');
    expect(parsed.captureOnError).toBe(true);
    expect(parsed.artifactsIsSet).toBe(true);
  });
});
