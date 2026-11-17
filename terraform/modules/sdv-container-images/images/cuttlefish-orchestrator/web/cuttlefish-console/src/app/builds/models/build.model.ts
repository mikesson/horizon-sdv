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
 * Origin of the build artifacts, and the build's provenance. Display
 * surfaces derive their AOSP/Custom labels from it.
 * - 'android_build': Official Android CI (ci.android.com).
 * - 'url': Operator-supplied artifacts (GCS or HTTPS).
 */
export type BuildSource = 'android_build' | 'url';

/**
 * Type of artifact source URL.
 */
type ArtifactSourceType = 'gcs' | 'https';

/**
 * Semantic role of an artifact in a build.
 * Maps directly to Host Orchestrator env_config fields.
 */
export type ArtifactRole =
  | 'host_package'
  | 'default_build'
  | 'kernel_build'
  | 'boot_build'
  | 'bootloader_build'
  | 'system_image_build';

/** All valid artifact roles for iteration. */
export const ARTIFACT_ROLES: ReadonlyArray<{value: ArtifactRole; label: string}> = [
  {value: 'host_package', label: 'Host Package'},
  {value: 'default_build', label: 'Device Image'},
  {value: 'kernel_build', label: 'Kernel'},
  {value: 'boot_build', label: 'Boot Image'},
  {value: 'bootloader_build', label: 'Bootloader'},
  {value: 'system_image_build', label: 'System Image'},
] as const;

/**
 * An artifact source for URL-based builds.
 * Supports GCS (gs://) and HTTPS URLs with semantic roles.
 */
export interface ArtifactSource {
  /** Source type: 'gcs' for gs:// URLs, 'https' for HTTPS URLs. */
  type: ArtifactSourceType;
  /** Full URL to the artifact (e.g., gs://bucket/path/file.zip). */
  url: string;
  /** Semantic role of this artifact (maps to env_config field). */
  role: ArtifactRole;
}

/**
 * Represents an Android Build artifact record.
 *
 * This entity tracks the metadata required to provision a Cuttlefish device.
 * It abstracts the difference between Android CI builds and URL-based
 * artifact sets.
 */
export interface Build {
  /** Internal UUID for this build record. */
  id: string;
  /** User-friendly display name. */
  name: string;
  source: BuildSource;

  // Android Build source fields
  /** Android CI numeric build ID (e.g., "12345678"). */
  buildId?: string;
  /** Build target (e.g., "aosp_cf_x86_64_phone-userdebug"). */
  target?: string;
  /** Git branch (e.g., "aosp-main"). */
  branch?: string;
  /** Build flavor (e.g., "userdebug", "user"). */
  flavor?: string;

  // URL source fields (preferred for custom builds)
  /** Artifact sources for URL-based builds. */
  artifactSources?: ArtifactSource[];

  // Additional metadata
  /** Whether this is the default build for new devices. */
  isDefault?: boolean;
  /** Optional description of the build contents. */
  description?: string;
  /** Build fingerprint. */
  fingerprint?: string;
  /** Email of the creator (informational). */
  createdBy?: string;

  /** Record creation timestamp. */
  createdAt?: Date;
  /** Last update timestamp. */
  updatedAt?: Date;
}

/**
 * Returns the fetch_cvd compatible build identifier for a build.
 *
 * ARCHITECTURE:
 * The Cuttlefish `fetch_cvd` tool uses specific URI schemes to download images.
 * This helper ensures the frontend generates identifiers that the host orchestrator
 * can interpret correctly.
 *
 * - For Android Build source with build ID: "@ab/{buildId}/{target}"
 * - For Android Build source with branch: "@ab/{branch}/{target}" (uses latest)
 * - For GCS source: The HTTPS URL of the GCS path
 *
 * @param build - The build entity to identify.
 * @returns A string identifier compatible with fetch_cvd.
 */
/**
 * A branch-only Android CI registration resolves to the NEWEST build on its
 * branch at every device launch (the backend emits `@ab/{branch}/{target}`),
 * unlike a registration pinned to a specific build ID.
 */
export function isTrackingBuild(build: Build): boolean {
  return build.source === 'android_build' && !!build.branch && !build.buildId;
}

/**
 * Human labels for the artifact roles a URL build composes, in canonical role
 * order, naming what the build contains (system image, kernel, …).
 */
export function artifactRoleLabels(build: Build): string[] {
  const present = new Set((build.artifactSources ?? []).map(a => a.role));
  return ARTIFACT_ROLES.filter(r => present.has(r.value)).map(r => r.label);
}

export function getBuildIdentifier(build: Build): string {
  if (build.source === 'android_build' && build.target) {
    // Prefer specific build ID, fall back to branch name
    const buildRef = build.buildId || build.branch;
    if (buildRef) {
      return `@ab/${buildRef}/${build.target}`;
    }
  }
  if (build.source === 'url' && build.artifactSources && build.artifactSources.length > 0) {
    // Return first artifact URL as identifier (for display purposes)
    return build.artifactSources[0].url;
  }
  return '';
}

/**
 * Payload for registering a new build artifact.
 */
export interface CreateBuildRequest {
  name: string;
  source: BuildSource;
  /** Android CI build ID. */
  buildId?: string;
  /** Build target (required for android_build source). */
  target?: string;
  /** Git branch for android_build source. */
  branch?: string;
  /** Artifact sources for URL-based builds. */
  artifactSources?: ArtifactSource[];
  description?: string;
  isDefault?: boolean;
}

/**
 * Filter criteria for build listings.
 */
export interface BuildListFilter {
  source?: BuildSource;
  limit?: number;
  pageToken?: string;
}

/**
 * Paginated response from the builds API.
 */
export interface BuildListResponse {
  items: Build[];
  total: number;
  limit: number;
  offset: number;
  nextPageToken: string;
}
