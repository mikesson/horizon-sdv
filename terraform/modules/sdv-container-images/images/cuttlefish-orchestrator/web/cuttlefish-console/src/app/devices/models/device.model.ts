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

import type {PlacementRequirements} from '../../shared/models/requirements.model';

/**
 * Supported statuses for a Cuttlefish device throughout its lifecycle.
 * Mirrors the backend's device status enum exactly. The Zod schema casts the
 * wire string straight into this union, so a missing member here silently
 * mistypes real states.
 */
export type DeviceStatus =
  | 'pending'
  | 'creating'
  | 'starting'
  | 'running'
  | 'stopping'
  | 'stopped'
  | 'deleting'
  | 'deleted'
  | 'error';

/**
 * GPU acceleration modes for the virtual device.
 * 'gfxstream' is the preferred mode for high performance.
 */
export type GPUMode = 'gfxstream' | 'drm_virgl' | 'swiftshader' | 'auto';

/**
 * Lifecycle modes governing how the reaper handles the device.
 *
 * "Ephemeral by Default, Persistent by Intent"
 */
export type LifecycleMode = 'ephemeral' | 'pinned';

/**
 * Valid justifications for pinning a device to prevent auto-reaping.
 */
export type PinReason =
  | 'overnight_test'
  | 'memory_leak_debug'
  | 'long_running_ci'
  | 'demo_preparation'
  | 'customer_support'
  | 'other';

/**
 * Human-readable labels for pin reasons.
 */
export const PIN_REASON_LABELS: Record<PinReason, string> = {
  overnight_test: 'Overnight Stability Test',
  memory_leak_debug: 'Memory Leak Debugging',
  long_running_ci: 'Long-Running CI Pipeline',
  demo_preparation: 'Demo Preparation',
  customer_support: 'Customer Support Session',
  other: 'Other',
};

/**
 * Encapsulates the lifecycle state of a device (Ephemeral vs. Pinned).
 *
 * This value object governs the automated reaper logic and UI visibility
 * of expiration warnings.
 */
export interface DeviceLifecycle {
  mode: LifecycleMode;
  /** Original TTL duration (e.g., 4 hours = 14400). */
  ttlSeconds: number;
  /** When device will auto-terminate. */
  expiresAt?: Date;
  /** When device was pinned. */
  pinnedAt?: Date;
  /** User ID who pinned the device. */
  pinnedBy?: string;
  /** Justification for pinning. */
  pinnedReason?: PinReason;
  /** Additional detail for 'other' reason. */
  pinnedReasonDetail?: string;
  /** Target expiry for pinned devices. */
  pinnedUntil?: Date;
  /** How many times TTL was extended. */
  extendedCount: number;
}

/**
 * One Android VM inside a device's CVD group, as the host reported it.
 *
 * A device is one leased, placed, billed unit containing N of these. At N=1 the
 * single entry holds the same values the device's own flat fields do.
 */
export interface DeviceInstance {
  /** Name within the group; the segment every per-instance host route uses. */
  name: string;

  /** Raw CVD wire state for this instance; absent means the host did not list it. */
  state?: string;

  /**
   * The id the host's operator registered this instance under. Carried, never
   * rebuilt: the host appends a fleet-unique instance id that cannot be derived
   * from the group and name.
   */
  webrtcDeviceId?: string;

  /**
   * Host-side ADB port. It records provenance and is not a dial target. The
   * host firewall opens the first instance's port only.
   */
  adbPort?: number;

  /**
   * Cuttlefish instance number, which binds the instance to its TAP device,
   * vsock CID and the group's radio ports. Slots are host-global and taken
   * lowest-free-first, so one group's slots are routinely non-contiguous.
   * Never treat array position as the slot. Absent means unknown, not slot 0.
   */
  slot?: number;

  /** The host's own rendered display descriptions, e.g. "720 x 1280 ( 320 )". */
  displays?: string[];
}

/**
 * Represents the Device domain entity in the frontend.
 *
 * This interface mirrors the backend domain model but is decoupled from
 * API transport concerns (which are handled by Zod schemas).
 */
export interface Device {
  /** Unique UUID for the device. */
  id: string;
  /** User-defined display name. */
  name: string;
  /** ID of the GCE host VM. */
  hostId: string;
  hostName?: string;
  /** GCP zone (e.g. us-central1-a). Used for region display. */
  hostZone?: string;
  /** Host IP (external if available, else internal). */
  hostIp?: string;
  /** Host external IP (may be empty for private hosts). */
  hostExternalIp?: string;
  /** Host internal VPC IP. */
  hostInternalIp?: string;
  status: DeviceStatus;
  /** AIP-128 output-only: the system is actively converging this device. */
  reconciling?: boolean;
  buildId?: string;
  buildTarget?: string;
  gpuMode: GPUMode;
  adbPort?: number;
  webrtcPort?: number;
  createdBy: string;
  /** The batch this device belongs to (unset for standalone devices). */
  batchId?: string;
  createdAt: Date;
  updatedAt: Date;
  startedAt?: Date;
  stoppedAt?: Date;

  /** Managed lifecycle configuration. */
  lifecycle: DeviceLifecycle;

  /** Tags for filtering. */
  tags?: string[];

  /** Creation mode (simple or advanced). */
  createMode?: CreateMode;

  /** CVD environment configuration (advanced mode). */
  envConfig?: Record<string, unknown>;

  /** Number of CVD instances this device was created to have. */
  instanceCount?: number;

  /**
   * The instances the host actually reported, in its own listing order. Absent
   * until the host answers; `instanceCount` above is what was asked for, so the
   * two disagree for the whole of a create and whenever one is unobserved.
   */
  instances?: DeviceInstance[];

  /** Raw CVD fleet status last observed on the host (e.g. Running, Unreachable). */
  cvdStatus?: string;
  /** Boot progress observed from the CVD's own logs (unset until sampled). */
  bootObservation?: BootObservation;
  /** Health-driven CVD recreate budget (zombie recovery). */
  recreateCount?: number;
  recreateWindowStart?: Date;
  /** Health-driven CVD restart budget (the cheap rung before recreate). */
  restartCount?: number;
  restartWindowStart?: Date;
}

/**
 * One step of the create wait's load-status trail (absent → a transient
 * state → Running). The trail is what splits the wait into fetch and boot legs.
 */
interface BootTransition {
  status: string;
  at?: Date;
}

/**
 * Boot progress sampled from a CVD's own logs. It separates a slow boot from a
 * crash-looping guest. Server-derived and refresh-stable; every field comes
 * from the row, never a session counter.
 */
export interface BootObservation {
  /** Linux panic banners counted in the kernel.log tail. */
  kernelPanics?: number;
  /** secure_env reboot detections counted in the launcher.log tail. */
  guestReboots?: number;
  /** Bootloader banners counted in the kernel.log tail. */
  ubootStarts?: number;
  /** The last completion marker follows the last attempt marker. */
  bootCompleted?: boolean;
  /** The guest's own terminal boot-failure marker was the last word. */
  bootFailed?: boolean;
  /** kernel.log total size at observedAt (the growth signal's input). */
  kernelLogBytes?: number;
  /** The last panic line seen, the evidence that names the bug. */
  lastPanic?: string;
  /** The decision this sample supports (booted, looping, progress, ...). */
  verdict?: string;
  /** The load-status trail: one entry per observed change during create. */
  transitions?: BootTransition[];
  /** When the device entered its current transient state. */
  transientSince?: Date;
  /** When this sample was taken. */
  observedAt?: Date;
}

/**
 * Renders the live boot-progress line for the status row, or undefined when
 * the observation says nothing worth showing. Terminal verdicts (a boot loop,
 * a guest-declared failure) show on any status, because the evidence outlives
 * the transition to error. Progress lines show only while the device is still
 * coming up, so a stale "booting" can never sit on a running device.
 */
export function bootProgressLine(status: DeviceStatus, obs: BootObservation): string | undefined {
  const attempts = Math.max(obs.guestReboots ?? 0, obs.kernelPanics ?? 0);
  if (obs.verdict === 'looping') {
    const line = `Boot loop: rebooted ${attempts}×`;
    return obs.lastPanic ? `${line}, last: ${obs.lastPanic}` : line;
  }
  if (obs.verdict === 'boot_failed') {
    return 'Guest declared boot failure';
  }
  if (status !== 'creating' && status !== 'starting' && status !== 'pending') {
    return undefined;
  }
  switch (obs.verdict) {
    case 'no_markers':
      return 'No guest boot activity yet (fetching or assembling artifacts)';
    case 'progress':
    case 'stalled':
      return attempts > 0 ? `Booting, ${attempts} failed attempts observed` : 'Booting';
    case 'booted':
      return 'Guest booted';
    default:
      return undefined;
  }
}

/**
 * Creation mode for devices.
 * - simple: Uses sensible defaults (existing behavior)
 * - advanced: Uses full CVD configuration from env_config
 */
export type CreateMode = 'simple' | 'advanced';

/**
 * Advanced configuration for CVD creation.
 * Contains the full EnvironmentSpecification for advanced mode.
 */
interface AdvancedConfig {
  /**
   * The CVD environment configuration following the canonical format.
   * This is passed directly to the Host Orchestrator.
   * Uses Record type to avoid circular import with cvd-config.schema.ts.
   */
  envConfig: Record<string, unknown>;
}

/**
 * Payload for creating a new device. Exactly one of hostId (imperative) or
 * requirements (declarative) must be set.
 */
export interface CreateDeviceRequest {
  hostId?: string;
  name: string;
  requirements?: PlacementRequirements;
  buildId?: string;
  buildTarget?: string;
  gpuMode?: GPUMode;

  /**
   * Creation mode: 'simple' (default) or 'advanced'.
   * Advanced mode uses the full CVD configuration from advancedConfig.
   */
  mode?: CreateMode;

  /**
   * Advanced CVD configuration.
   * Required when mode is 'advanced'.
   */
  advancedConfig?: AdvancedConfig;

  /** Session duration (default 4h, max 8h). */
  ttlHours?: number;
  /** Pin immediately upon creation. */
  pinned?: boolean;
  /** Required if pinned is true. */
  pinnedReason?: PinReason;
  /** For 'other' reason. */
  pinnedReasonDetail?: string;
  /** Target expiry for pinned. */
  pinnedUntil?: Date;

  /** Metadata tags. */
  tags?: string[];
}

/**
 * Request to pin a device (API format).
 */
export interface PinRequest {
  reason: PinReason;
  reasonDetail?: string;
  pinnedUntil?: Date;
}

/**
 * Request to extend device TTL (API format).
 */
export interface ExtendRequest {
  /** If not provided, uses default extension (2 hours). */
  seconds?: number;
}

/** Filters for listing devices. */
export interface DeviceListFilter {
  hostId?: string;
  status?: DeviceStatus;
  buildId?: string;
  /** Server-side scope to the caller's own devices (the console default). */
  mine?: boolean;
  createdBy?: string;
  limit?: number;
  pageToken?: string;

  lifecycleMode?: LifecycleMode;
  /** For "Zombie Hunter" view. */
  pinnedOnly?: boolean;
  /** Minutes until expiration. */
  expiringWithin?: number;

  tags?: string[];
}

/** Paginated response from device list API. */
export interface DeviceListResponse {
  items: Device[];
  total: number;
  limit: number;
  offset: number;
  nextPageToken: string;
}

/**
 * One ICE server, in the shape both RTCPeerConnection and the device's
 * request-offer take, so the device leg passes through unmapped.
 */
export interface IceServer {
  urls: string[];
  /** Present on TURN entries only; format "expiry:email:kind:id". */
  username?: string;
  /** HMAC-SHA1 signature of the username. */
  credential?: string;
}

/**
 * ICE configuration for one device's media: a list per end of the connection.
 *
 * The lists are not interchangeable. The client leg carries the addresses a
 * browser can route to and the device leg the addresses a host VM can, so
 * sending the wrong one produces a connection that never establishes.
 */
export interface IceConfig {
  clientServers: IceServer[];
  /** Sent to the device in request-offer; never used locally. */
  deviceServers: IceServer[];
  /** Binds this end only: the device cannot be held to relayed transport. */
  policy: RTCIceTransportPolicy;
}

/**
 * Display preset for quickly adding common display configurations.
 */
export interface DisplayPreset {
  name: string;
  width: number;
  height: number;
  dpi: number;
  /** Note: snake_case to match backend JSON */
  refresh_rate_hz: number;
}

/**
 * Display information for a CVD.
 * Note: Field names match backend JSON (snake_case).
 */
export interface DisplayInfo {
  display_number: number;
  width: number;
  height: number;
  dpi: number;
  refresh_rate_hz: number;
}

/**
 * Response from listing displays on a device.
 */
export interface ListDisplaysResponse {
  displays: DisplayInfo[];
  presets: DisplayPreset[];
}

/** Field names match backend JSON; the bytes arrive base64-encoded. */
export interface DisplayScreenshot {
  screenshot_bytes: string;
  screenshot_mime_type: string;
}

/** Where a captured bugreport landed in artifact storage. */
export interface BugreportArtifact {
  bucket: string;
  object: string;
  gsUri: string;
  downloadPath: string;
}

/** Where a fetched screen recording landed in artifact storage. */
export interface RecordingArtifact {
  bucket: string;
  object: string;
  gsUri: string;
  downloadPath: string;
}

/**
 * Request to add a display to a device.
 */
/** Field names match backend JSON (snake_case); the body is posted as-is. */
export interface AddDisplayRequest {
  width?: number;
  height?: number;
  dpi?: number;
  refresh_rate_hz?: number;
  /** Optional: use a preset instead of custom dimensions. */
  preset?: string;
}

/**
 * Response from adding a display to a device.
 * Note: Field names match backend JSON (snake_case).
 */
export interface AddDisplayResponse {
  display_number: number;
  message: string;
}
