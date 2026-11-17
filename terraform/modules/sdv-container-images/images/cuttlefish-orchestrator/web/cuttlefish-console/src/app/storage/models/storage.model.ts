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
 * Represents a Google Cloud Storage (GCS) Bucket.
 */
export interface Bucket {
  /** The unique name of the bucket. */
  name: string;
  /** The geographic location (e.g., US, EUROPE-WEST1). */
  location?: string;
  /** Timestamp of creation. */
  createdAt?: Date;
}

/**
 * Represents a file or folder within a GCS bucket.
 */
export interface StorageObject {
  /** The full path including prefix (e.g., "folder/file.txt"). */
  name: string;
  /** The display name (filename or folder name). */
  displayName: string;
  /** The parent bucket name. */
  bucket: string;
  /** Size in bytes (undefined for folders). */
  size?: number;
  /** MIME type. */
  contentType?: string;
  /** True if this represents a directory prefix. */
  isFolder: boolean;
  /** Last modification timestamp. */
  updatedAt?: Date;
  /** The fully qualified gs:// URI. */
  gsUri: string;
  /** The public HTTPS URL (if accessible). */
  publicUrl?: string;
  /** The object generation number. */
  generation?: number;
}

/**
 * Response for listing objects.
 */
export interface ListObjectsResponse {
  objects: StorageObject[];
  prefixes?: string[];
  nextPageToken?: string;
}

/**
 * Represents a valid Cuttlefish build artifact detected in GCS.
 */
interface BuildArtifact {
  object: StorageObject;
  buildName?: string;
  buildTarget?: string;
  version?: string;
  isValid: boolean;
}

/**
 * Breadcrumb item for navigation within the GCS browser.
 */
export interface BreadcrumbItem {
  name: string;
  path: string;
}

/**
 * Result of checking service account access permissions.
 */
export interface AccessCheckResult {
  serviceAccount: string;
  hasAccess: boolean;
  permissions?: string[];
  missingPerms?: string[];
  error?: string;
  checkedAt: string;
}

/**
 * Response containing access verification details.
 */
export interface VerifyAccessResponse {
  gsUri: string;
  orchestratorAccess: AccessCheckResult;
  hostAccess?: AccessCheckResult;
  canProceed: boolean;
  message?: string;
}

/** Select mode for the GCS browser dialog. */
type GcsBrowserSelectMode = 'file' | 'folder' | 'artifact' | 'multi-file';

/** Dialog input configuration for GCS browser. */
export interface GcsBrowserDialogData {
  title?: string;
  initialBucket?: string;
  initialPath?: string;
  selectMode: GcsBrowserSelectMode;
  hostId?: string;
  /** Hint for required file types (for multi-file mode). */
  requiredTypes?: string[];
}

/** Result for single-select GCS browser dialog. */
export interface GcsBrowserDialogResult {
  bucket: string;
  object: StorageObject;
  artifact?: BuildArtifact;
  gsUri: string;
  accessVerified?: boolean;
}

/** Result for multi-select GCS browser dialog. */
export interface GcsBrowserMultiSelectResult {
  bucket: string;
  objects: StorageObject[];
  gsUris: string[];
  totalSize: number;
  accessVerified?: boolean;
  accessResults?: Map<string, VerifyAccessResponse>;
}
