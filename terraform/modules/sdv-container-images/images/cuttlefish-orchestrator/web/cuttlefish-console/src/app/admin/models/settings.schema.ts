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

import {z} from 'zod';
import {ArtifactBucketsSchema} from './artifact-buckets.model';
import type {
  ArtifactSettings,
  DefaultsSettings,
  PlacementSettings,
  SpotSettings,
} from './settings.model';

/**
 * `is_set` is what lets the page say "stored" or "defaulted"; it is optional
 * here only so a response predating it degrades to "defaulted" rather than
 * failing the whole section.
 */
const isSet = z.boolean().optional().default(false);

/** GET /v2/admin/system-config/zone-pin and default-gpu-machine-type. */
export const StringSettingSchema = z.object({value: z.string(), is_set: isSet});

/** GET /v2/admin/system-config/quota-margin. */
export const NumberSettingSchema = z.object({value: z.number(), is_set: isSet});

/** GET /v2/admin/system-config/v1-zone-hint and spot-auto-default. */
export const BooleanSettingSchema = z.object({value: z.boolean(), is_set: isSet});

/** GET /v2/admin/system-config/spot-min-uptime: one endpoint, two tiers. */
export const SpotMinUptimeSchema = z.object({
  single_seconds: z.number(),
  batch_seconds: z.number(),
  is_set: isSet,
});

/** GET /v2/admin/system-config/default-upstream-build. */
export const DefaultUpstreamBuildSchema = z.object({
  branch: z.string(),
  target: z.string(),
  is_set: isSet,
});

/** GET /v2/admin/system-config/docker-host: one endpoint, one host shape. */
export const DockerHostSettingSchema = z.object({
  machine_type: z.string(),
  ttl_hours: z.number(),
  max_ttl_hours: z.number(),
  is_set: isSet,
});

export const PlacementSettingsSchema = z
  .object({
    zonePin: StringSettingSchema,
    quotaMargin: NumberSettingSchema,
    v1ZoneHint: BooleanSettingSchema,
  })
  .transform(
    (d): PlacementSettings => ({
      zonePin: d.zonePin.value,
      zonePinIsSet: d.zonePin.is_set,
      quotaMarginPercent: d.quotaMargin.value,
      quotaMarginIsSet: d.quotaMargin.is_set,
      v1ZoneIsHint: d.v1ZoneHint.value,
      v1ZoneHintIsSet: d.v1ZoneHint.is_set,
    })
  );

export const SpotSettingsSchema = z
  .object({autoDefault: BooleanSettingSchema, minUptime: SpotMinUptimeSchema})
  .transform(
    (d): SpotSettings => ({
      autoDefault: d.autoDefault.value,
      autoDefaultIsSet: d.autoDefault.is_set,
      minUptimeSingleSeconds: d.minUptime.single_seconds,
      minUptimeBatchSeconds: d.minUptime.batch_seconds,
      minUptimeIsSet: d.minUptime.is_set,
    })
  );

export const DefaultsSettingsSchema = z
  .object({
    gpuMachineType: StringSettingSchema,
    upstreamBuild: DefaultUpstreamBuildSchema,
    dockerHost: DockerHostSettingSchema,
  })
  .transform(
    (d): DefaultsSettings => ({
      gpuMachineType: d.gpuMachineType.value,
      gpuMachineTypeIsSet: d.gpuMachineType.is_set,
      upstreamBranch: d.upstreamBuild.branch,
      upstreamTarget: d.upstreamBuild.target,
      upstreamBuildIsSet: d.upstreamBuild.is_set,
      dockerMachineType: d.dockerHost.machine_type,
      dockerTtlHours: d.dockerHost.ttl_hours,
      dockerMaxTtlHours: d.dockerHost.max_ttl_hours,
      dockerHostIsSet: d.dockerHost.is_set,
    })
  );

/**
 * Reuses the canonical bucket schema, because a settings-only copy would take
 * the shape the device artifacts card also reads off the drift gate.
 */
export const ArtifactSettingsSchema = ArtifactBucketsSchema.transform(
  (d): ArtifactSettings => ({
    bugreportBucket: d.bugreportBucket,
    recordingBucket: d.recordingBucket,
    defaultBucket: d.defaultBucket,
    captureOnError: d.captureOnError,
    artifactsIsSet: d.isSet,
  })
);
