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
 * The runtime settings, grouped as the Settings page renders them.
 *
 * Every group carries `isSet` per pair: the API distinguishes a value an
 * operator stored from the compiled default it would serve anyway, and
 * without that the page cannot say which is which.
 */

export interface PlacementSettings {
  /** '' = placement ranks across the whole enabled catalog. */
  zonePin: string;
  zonePinIsSet: boolean;
  quotaMarginPercent: number;
  quotaMarginIsSet: boolean;
  /** Switch: the legacy /v1 zone token is a hint rather than a literal pin. */
  v1ZoneIsHint: boolean;
  v1ZoneHintIsSet: boolean;
}

export interface SpotSettings {
  /** Switch: auto-capacity headless intent resolves to Spot hosts. */
  autoDefault: boolean;
  autoDefaultIsSet: boolean;
  minUptimeSingleSeconds: number;
  minUptimeBatchSeconds: number;
  /** The two tiers are written together, so they report together. */
  minUptimeIsSet: boolean;
}

export interface DefaultsSettings {
  /** '' = the first enabled GPU-capable catalog type. */
  gpuMachineType: string;
  gpuMachineTypeIsSet: boolean;
  upstreamBranch: string;
  upstreamTarget: string;
  upstreamBuildIsSet: boolean;
  /** Devices packed onto each auto-provisioned host; explicit creates carry their own. */
  /** The GCE shape a legacy /v1 docker-only host create compiles to (OmniLab ATS). */
  dockerMachineType: string;
  /** Its whole lifetime: a docker-shape host's CVDs have no rows, so nothing extends the lease. */
  dockerTtlHours: number;
  /** Compiled cap on dockerTtlHours, published so the form never retypes it. */
  dockerMaxTtlHours: number;
  /** The pair is written together, so it reports together. */
  dockerHostIsSet: boolean;
}

export interface ArtifactSettings {
  /** '' = the deployment default bucket applies. */
  bugreportBucket: string;
  recordingBucket: string;
  /** The deployment default (CCO_ARTIFACTS_BUCKET); '' = capture unconfigured. */
  defaultBucket: string;
  /** Switch: capture a bugreport when a device reaches the error state. */
  captureOnError: boolean;
  artifactsIsSet: boolean;
}

/** The min-uptime tiers the Spot advice API discretizes to. */
export const SPOT_UPTIME_TIERS: ReadonlyArray<{value: number; label: string}> = [
  {value: 0, label: 'No minimum'},
  {value: 60, label: '1 minute'},
  {value: 600, label: '10 minutes'},
  {value: 3600, label: '1 hour'},
];
