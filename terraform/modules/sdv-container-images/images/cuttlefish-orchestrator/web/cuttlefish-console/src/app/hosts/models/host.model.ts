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

import type {HostErrorType} from '../../shared/constants';

/**
 * Valid operational states for a Host VM.
 */
type HostStatus =
  | 'pending'
  | 'provisioning'
  | 'starting'
  | 'running'
  | 'stopping'
  | 'stopped'
  | 'deleting'
  | 'deleted'
  | 'error'
  | 'unreachable'
  | 'preempted';

/**
 * Provisioning model for a Host VM.
 *
 * - 'STANDARD': Regular on-demand VM (default).
 * - 'SPOT': Preemptible Spot VM with discounted pricing.
 */
export type ProvisioningModel = 'STANDARD' | 'SPOT';

/**
 * Which path created a host. Provenance, stamped once at create.
 *
 * - 'user': someone asked for it, through the native or the legacy API.
 * - 'auto': a declarative create provisioned it after finding no capacity.
 * - 'pool': a fleet top-up provisioned it as standing inventory.
 * - 'batch': it was provisioned to carry a batch's members.
 */
export type HostOrigin = 'user' | 'auto' | 'pool' | 'batch';

/**
 * Whose lifecycle rules apply to a host. The backend derives it from origin,
 * exclusivity and batch membership, and never stores it.
 *
 * - 'reserved': runs its full lease; only a user delete reclaims it.
 * - 'fleet': shared standing capacity the orchestrator manages.
 * - 'dedicated': fenced to one group, so an empty one is pure cost.
 * - 'batch': batch settlement owns it end to end.
 */
export type HostClass = 'reserved' | 'fleet' | 'dedicated' | 'batch';

/**
 * Structured error information for a host in error state.
 *
 * Provides actionable information about what went wrong and how to fix it.
 */
export interface HostError {
  message: string;
  type: HostErrorType;
  /** GCE error code if available (e.g., QUOTA_EXCEEDED). */
  code?: string;
  suggestion?: string;
  alternativeZones?: string[];
  retryable: boolean;
  occurredAt?: Date;
}

/**
 * Lifecycle modes for a Host.
 *
 * - 'ephemeral': Default. Host expires after TTL if not renewed.
 * - 'protected': Host is protected from auto-termination because it contains pinned devices.
 */
type HostLifecycleMode = 'ephemeral' | 'protected';

/**
 * Encapsulates the lifecycle metadata for a Host.
 *
 * Governs the auto-reaper logic. A host is 'protected' implicitly if it has
 * pinned devices, preventing accidental deletion of long-running workloads.
 */
export interface HostLifecycle {
  mode: HostLifecycleMode;
  /** Original TTL duration in seconds (e.g., 10 hours = 36000). */
  ttlSeconds: number;
  expiresAt?: Date;
  protectedDeviceCount: number;
}

/**
 * Represents a GCE Host Virtual Machine.
 *
 * A Host is the compute unit that runs Cuttlefish virtual devices.
 * It maps 1:1 to a Google Compute Engine instance.
 */
export interface Host {
  id: string;
  name: string;
  /** GCP Zone (e.g., us-central1-a), the physical binding placement chose. */
  zone: string;
  /** Placement intent: the pinned zone or location hint (unset = auto). */
  requestedZone?: string;
  /** The creator named an exact zone; failover never moves this host. */
  zonePinned: boolean;
  /** The actual GCE VM instance name (e.g., cf-myhost-abc12345). */
  gceInstanceName?: string;
  /** GCE Machine Type (e.g., n1-standard-4). */
  machineType: string;
  status: HostStatus;
  /** AIP-128 output-only: the system is actively converging this host. */
  reconciling?: boolean;
  gpuType: string;
  gpuCount: number;
  /** Minimum CPU platform constraint passed to GCE; absent = none. */
  minCpuPlatform?: string;
  /** Requested boot disk size; absent = the server's default (400 GB). */
  bootDiskSizeGb?: number;
  /** Self link of the image the VM was created from; absent before provisioning. */
  bootImage?: string;
  provisioningModel: ProvisioningModel;
  maxDevices: number;
  /** Fenced from automatic placement (dedicated to explicit/batch use). */
  exclusive: boolean;
  /** Its CVDs arrive via the /v1 proxy (legacy docker-shape create); the orphan census skips it. */
  foreignCvds: boolean;
  /** The batch that provisioned this host (unset for user-created hosts). */
  batchId?: string;
  /** Which path created this host. */
  origin?: HostOrigin;
  /**
   * Whose lifecycle rules apply. Named hostClass rather than class because the
   * bare word reads as a CSS binding everywhere else in this codebase.
   */
  hostClass?: HostClass;
  currentDevices: number;
  externalIP?: string;
  internalIP?: string;
  createdBy: string;
  createdAt: Date;
  updatedAt: Date;

  lifecycle?: HostLifecycle;

  error?: HostError;

  preemptedAt?: Date;

  preemptionCount: number;

  lastPreemptionId?: string;

  /** Incarnation with an unrecovered preemption; drives the derived 'preempted' phase. */
  preemptedIncarnation?: number;

  /** Desired state (running|stopped|absent), the spec half of the split. */
  desiredState?: string;
  /** Raw GCE instance state last observed by the host reconciler, and when. */
  vmStatus?: string;
  vmObservedAt?: Date;
  /** When Compute Engine stops the VM under its maximum run duration; a restart resets it. */
  vmTerminatesAt?: Date;
  /** Re-provision counter; the GCE instance name embeds it. */
  incarnation?: number;
  /** Stalled condition: still converging (e.g. a teardown that keeps failing). */
  stalledReason?: string;
  /** ho_reachable condition: when the Host Orchestrator first answered (null while 'starting'). */
  hoReadyAt?: Date;
  /** Host-poison condition: leaked-tap create failures and the degraded fence. */
  consecutiveCreateFails?: number;
  lastCreateFailReason?: string;
  degradedAt?: Date;
  degradedReason?: string;
  /** Provision-blocked condition: GCE refused the provision for capacity, so placement skips the host until its VM exists. */
  provisionBlockedAt?: Date;
  provisionBlockedReason?: string;
}

/**
 * Payload for creating a new Host VM.
 */
export interface CreateHostRequest {
  name: string;
  zone: string;
  machineType?: string;
  gpuType?: string;
  gpuCount?: number;
  maxDevices?: number;
  ttlHours?: number;
  provisioningModel?: ProvisioningModel;
  /**
   * Fence the host from automatic placement: only creates naming it by
   * host_id land here.
   */
  exclusive?: boolean;
  /** GCE minimum CPU platform, passed through verbatim; '' = no constraint. */
  minCpuPlatform?: string;
  /** Boot disk size in GB; 0 = the platform default. */
  bootDiskSizeGb?: number;
}

/**
 * Payload for retrying a preempted or failed Host VM.
 */
export interface RetryHostRequest {
  fallbackToStandard?: boolean;
}

/**
 * Filter criteria for listing hosts.
 */
export interface HostListFilter {
  zone?: string;
  status?: HostStatus;
  /** Server-side scope to the caller's own hosts (the console default). */
  mine?: boolean;
  createdBy?: string;
  /** Derived server-side, so this filter cannot be applied on the client. */
  hostClass?: HostClass;
  limit?: number;
  pageToken?: string;
}

/**
 * Paginated response for the host list API.
 */
export interface HostListResponse {
  items: Host[];
  total: number;
  limit: number;
  offset: number;
  nextPageToken: string;
}
