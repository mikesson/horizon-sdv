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

import {HttpClient, HttpParams} from '@angular/common/http';
import {inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import type {Resolution} from '../../shared/models/requirements.model';
import {LoggingService} from '../../shared/services';
import {createApiErrorHandler, resolveBasePath} from '../../shared/utils';
import type {Collection} from '../../shared/utils/paged-collection';
import {collectPages, MAX_PAGE_SIZE} from '../../shared/utils/paged-collection';
import type {
  AddDisplayRequest,
  AddDisplayResponse,
  BugreportArtifact,
  CreateDeviceRequest,
  Device,
  DeviceListFilter,
  DeviceListResponse,
  DisplayScreenshot,
  ExtendRequest,
  IceConfig,
  ListDisplaysResponse,
  PinRequest,
  RecordingArtifact,
} from '../models/device.model';
import {
  BugreportArtifactSchema,
  CreatePlanResponseSchema,
  DeviceListResponseSchema,
  DeviceSchema,
  RecordingArtifactSchema,
  RecordingListSchema,
} from '../models/device.schema';
import {IceConfigSchema} from '../models/ice.schema';
import type {InstanceTarget} from '../models/instance-target';
import {targetPath} from '../models/instance-target';
import type {Operation} from '../models/operation.model';
import {OperationListSchema, OperationSchema} from '../models/operation.schema';
import type {Snapshot} from '../models/snapshot.model';
import {SnapshotListSchema} from '../models/snapshot.schema';

/**
 * Manages the HTTP transport layer for the Device vertical slice in the frontend.
 *
 * This service acts as the anti-corruption layer, translating backend DTOs
 * into pure frontend domain models using Zod schemas for runtime validation.
 */
@Service()
export class DeviceApiService {
  private readonly http = inject(HttpClient);
  private readonly logger = inject(LoggingService);
  private readonly baseUrl = resolveBasePath('/v2/devices');
  // Operations are a top-level collection, not a device sub-resource: a handle
  // outlives the verb and is addressable on its own.
  private readonly operationsUrl = resolveBasePath('/v2/operations');
  private readonly handleError = createApiErrorHandler('Device', 'DeviceApiService');

  /**
   * Retrieves one window of devices matching the filter.
   *
   * VALIDATION: Uses DeviceListResponseSchema to validate the backend response at runtime.
   * TRANSFORMATION: Translates frontend camelCase filters to backend snake_case query parameters.
   *
   * @param filter - Optional criteria to filter the device list (e.g., hostId, status).
   */
  list(filter?: DeviceListFilter): Observable<DeviceListResponse> {
    // Always request the backend's maximum page size: its default (20)
    // silently truncates fleet lists. An explicit filter.limit still wins.
    let params = new HttpParams().set('limit', String(filter?.limit ?? MAX_PAGE_SIZE));

    if (filter) {
      if (filter.hostId) {
        params = params.set('host_id', filter.hostId);
      }
      if (filter.status) {
        params = params.set('status', filter.status);
      }
      if (filter.buildId) {
        params = params.set('build_id', filter.buildId);
      }
      if (filter.mine) {
        params = params.set('mine', 'true');
      }
      if (filter.createdBy) {
        params = params.set('created_by', filter.createdBy);
      }
      if (filter.pageToken) {
        params = params.set('page_token', filter.pageToken);
      }
    }

    return this.http.get<unknown>(this.baseUrl, {params}).pipe(
      map(data => DeviceListResponseSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Retrieves every device matching the filter, following the server's pages.
   *
   * This is what the store loads: the device KPIs, the ADB install picker and
   * select-all all count what is in the store, so a fleet past one page would
   * make those numbers wrong rather than merely shorten a table.
   */
  listAll(filter?: DeviceListFilter): Observable<Collection<Device>> {
    return collectPages(window => this.list({...filter, ...window}));
  }

  /**
   * Retrieves a single device entity by its unique identifier.
   *
   * VALIDATION: Uses DeviceSchema to perform runtime validation and snake_case transformation.
   */
  get(id: string): Observable<Device> {
    return this.http.get<unknown>(`${this.baseUrl}/${id}`).pipe(
      map(data => DeviceSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Requests the creation of a new Cuttlefish device instance.
   *
   * The Idempotency-Key is what makes a retry safe. Nothing else stops a
   * second device, because device names are not unique, so a create the caller
   * re-sends after a lost response would otherwise provision a second device
   * and, declaratively, a second billed host. The caller owns the key's
   * lifetime; it must stay stable across retries of one intent and change when
   * the body does (the backend 422s a key reused with a different request).
   *
   * TRANSFORMATION: Explicitly maps frontend camelCase request fields to backend snake_case.
   * VALIDATION: Validates the resulting device entity using DeviceSchema.
   *
   * @returns An observable of the newly created and validated Device entity.
   */
  create(request: CreateDeviceRequest, idempotencyKey: string): Observable<Device> {
    return this.http
      .post<unknown>(this.baseUrl, this.toCreateBody(request), {
        headers: {'Idempotency-Key': idempotencyKey},
      })
      .pipe(
        map(data => DeviceSchema.parse(data)),
        catchError(this.handleError)
      );
  }

  /**
   * Dry-runs a create (AIP-163 validate_only). The backend performs the full
   * resolution and every check the real call would, including quota 429s and
   * capacity 503s, but commits nothing. Declarative plans resolve to a Resolution;
   * imperative plans have none (null) and their value is the error surface.
   *
   * Carries no Idempotency-Key: a plan creates nothing for a replay to
   * duplicate, and the backend short-circuits validate_only before it reads
   * the key at all.
   */
  planCreate(request: CreateDeviceRequest): Observable<Resolution | null> {
    const body = this.toCreateBody(request);
    body['validate_only'] = true;
    return this.http.post<unknown>(this.baseUrl, body).pipe(
      map(data => CreatePlanResponseSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Serializes a create request to the wire shape (camelCase → snake_case). */
  private toCreateBody(request: CreateDeviceRequest): Record<string, unknown> {
    const body: Record<string, unknown> = {name: request.name};

    // Exactly one of host_id (imperative) or requirements (declarative);
    // gpu_mode is omitted when unset so the backend default (auto) applies.
    if (request.hostId) {
      body['host_id'] = request.hostId;
    }
    if (request.requirements) {
      body['requirements'] = this.toRequirementsBody(request.requirements);
    }
    if (request.gpuMode) {
      body['gpu_mode'] = request.gpuMode;
    }
    if (request.buildId) {
      body['build_id'] = request.buildId;
    }

    // Lifecycle: the backend requires pinned_reason only alongside pinned,
    // so the reason fields are gated on the flag.
    if (request.ttlHours) {
      body['ttl_hours'] = request.ttlHours;
    }
    if (request.pinned) {
      body['pinned'] = true;
      body['pinned_reason'] = request.pinnedReason;
      if (request.pinnedReasonDetail) {
        body['pinned_reason_detail'] = request.pinnedReasonDetail;
      }
      if (request.pinnedUntil) {
        body['pinned_until'] = request.pinnedUntil.toISOString();
      }
    }
    if (request.tags && request.tags.length > 0) {
      body['tags'] = request.tags;
    }

    if (request.mode === 'advanced' && request.advancedConfig) {
      body['mode'] = 'advanced';
      body['advanced_config'] = {
        env_config: request.advancedConfig.envConfig,
      };
      this.logger.debug('DeviceApi', 'Creating device with advanced mode');
    } else if (request.buildTarget) {
      // Simple mode - add optional build fields
      body['build_target'] = request.buildTarget;
    }

    return body;
  }

  private toRequirementsBody(
    requirements: NonNullable<CreateDeviceRequest['requirements']>
  ): Record<string, unknown> {
    const wire: Record<string, unknown> = {interaction: requirements.interaction};
    if (requirements.gpuRequired) {
      wire['gpu_required'] = true;
    }
    if (requirements.preferredRegion) {
      wire['location'] = {preferred_region: requirements.preferredRegion};
    }
    if (requirements.capacity && requirements.capacity !== 'auto') {
      wire['capacity'] = requirements.capacity;
    }
    // x86 and an unpinned pool are the backend's own defaults, so neither is
    // sent: the field absent and the field at its default mean the same thing.
    if (requirements.architecture === 'arm') {
      wire['architecture'] = 'arm';
    }
    if (requirements.pool) {
      wire['pool'] = requirements.pool;
    }
    const build = requirements.build;
    if (build?.kind === 'registered') {
      wire['build'] = {registered: build.id};
    } else if (build?.kind === 'track') {
      wire['build'] = {track: {branch: build.branch, target: build.target}};
    }
    // kind 'default' (or absent) omits build: the platform resolves it.
    return wire;
  }

  /**
   * Permanently deletes a device and releases its associated resources.
   */
  delete(id: string): Observable<void> {
    // Use responseType: 'text' to handle 204 No Content responses properly
    return this.http
      .delete(`${this.baseUrl}/${id}`, {responseType: 'text'})
      .pipe(map(() => void 0));
  }

  /**
   * Signals a stopped device instance to begin the boot process.
   */
  start(id: string): Observable<{status: string}> {
    return this.http.post<{status: string}>(`${this.baseUrl}/${id}/start`, {});
  }

  /**
   * Signals a running device instance to perform a graceful shutdown.
   */
  stop(id: string): Observable<{status: string}> {
    return this.http.post<{status: string}>(`${this.baseUrl}/${id}/stop`, {});
  }

  /**
   * Starts relaunching a running device's CVD in place (no artifact re-fetch),
   * returning the operation handle its outcome is read from.
   *
   * The old response was `{status: 'running'}`, a constant, since the device row
   * is identical before and after, so it could never report whether the restart
   * happened. The handle is the first completion signal this verb has had.
   */
  restart(target: InstanceTarget): Observable<Operation> {
    return this.postInstanceVerb(target, 'restart');
  }

  /**
   * Starts resetting a running device to first-boot state (userdata wiped, same
   * build), returning the operation handle its outcome is read from.
   */
  powerwash(target: InstanceTarget): Observable<Operation> {
    return this.postInstanceVerb(target, 'powerwash');
  }

  /** Lists a device's snapshot inventory, newest first. */
  listSnapshots(deviceId: string): Observable<Snapshot[]> {
    return this.http.get<unknown>(`${this.baseUrl}/${deviceId}/snapshots`).pipe(
      map(data => SnapshotListSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Starts removing a device's snapshot, returning the operation handle. */
  deleteSnapshot(deviceId: string, snapshotId: string): Observable<Operation> {
    return this.http.delete<unknown>(`${this.baseUrl}/${deviceId}/snapshots/${snapshotId}`).pipe(
      map(data => OperationSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Lists a device's operations, newest first. */
  listOperations(deviceId: string, limit = 10): Observable<Operation[]> {
    const params = new HttpParams().set('resource_id', deviceId).set('limit', limit);
    return this.http.get<unknown>(this.operationsUrl, {params}).pipe(
      map(data => OperationListSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Admits an async per-instance verb and parses its handle. */
  private postInstanceVerb(target: InstanceTarget, verb: string): Observable<Operation> {
    return this.http.post<unknown>(`${targetPath(target)}/${verb}`, {}).pipe(
      map(data => OperationSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Presses one instance's power button. */
  powerbtn(target: InstanceTarget): Observable<{status: string}> {
    return this.http.post<{status: string}>(`${targetPath(target)}/powerbtn`, {});
  }

  /**
   * Captures a bugreport on the device's host into artifact storage. Slow,
   * taking seconds for the host-only flavor and minutes with includeAdb.
   */
  bugreport(id: string, includeAdb: boolean): Observable<BugreportArtifact> {
    const body = includeAdb ? {include_adb: true} : {};
    return this.http.post<unknown>(`${this.baseUrl}/${id}/bugreport`, body).pipe(
      map(data => BugreportArtifactSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Starts a screen recording on one running instance. */
  recordStart(target: InstanceTarget): Observable<{status: string}> {
    return this.http.post<{status: string}>(`${targetPath(target)}/recordings/start`, {});
  }

  /** Stops the screen recording on one running instance. */
  recordStop(target: InstanceTarget): Observable<{status: string}> {
    return this.http.post<{status: string}>(`${targetPath(target)}/recordings/stop`, {});
  }

  /** Lists the host-local screen recordings for one instance. */
  listRecordings(target: InstanceTarget): Observable<string[]> {
    return this.http.get<unknown>(`${targetPath(target)}/recordings`).pipe(
      map(data => RecordingListSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Archives a host-local recording to the configured GCS bucket. */
  fetchRecording(target: InstanceTarget, name: string): Observable<RecordingArtifact> {
    return this.http
      .post<unknown>(`${targetPath(target)}/recordings/${encodeURIComponent(name)}/fetch`, {})
      .pipe(
        map(data => RecordingArtifactSchema.parse(data)),
        catchError(this.handleError)
      );
  }

  /** Captures one display of one instance (bytes arrive base64-encoded). */
  getScreenshot(target: InstanceTarget, displayNumber: number): Observable<DisplayScreenshot> {
    return this.http
      .get<DisplayScreenshot>(`${targetPath(target)}/displays/${displayNumber}/screenshot`)
      .pipe(catchError(this.handleError));
  }

  /**
   * Retrieves the ICE configuration for a device's media: the servers for each
   * end of the connection, with any TURN credential minted for this caller,
   * and the transport policy this end is held to.
   *
   * VALIDATION: Uses IceConfigSchema to validate and transform the response.
   */
  getIceConfig(id: string): Observable<IceConfig> {
    return this.http.get<unknown>(`${this.baseUrl}/${id}/ice-config`).pipe(
      map(data => IceConfigSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  // ========== Display Management ==========

  /**
   * Lists the active displays and available configuration presets for a device.
   */
  listDisplays(target: InstanceTarget): Observable<ListDisplaysResponse> {
    return this.http
      .get<ListDisplaysResponse>(`${targetPath(target)}/displays`)
      .pipe(catchError(this.handleError));
  }

  /**
   * Adds a new virtual display to the running Cuttlefish instance.
   *
   * WebRTC streaming requires at least one display to be configured.
   *
   * @param request - The dimensions or preset name for the new display.
   */
  addDisplay(target: InstanceTarget, request: AddDisplayRequest): Observable<AddDisplayResponse> {
    return this.http
      .post<AddDisplayResponse>(`${targetPath(target)}/displays`, request)
      .pipe(catchError(this.handleError));
  }

  /**
   * Removes a specific virtual display from the device.
   */
  removeDisplay(target: InstanceTarget, displayNumber: number): Observable<void> {
    return this.http
      .delete(`${targetPath(target)}/displays/${displayNumber}`, {responseType: 'text'})
      .pipe(
        map(() => void 0),
        catchError(this.handleError)
      );
  }

  // ========== Lifecycle Management ==========

  /**
   * Transition a device to 'pinned' mode to prevent automated reaper termination.
   *
   * TRANSFORMATION: Maps frontend camelCase request fields to backend snake_case.
   * VALIDATION: Validates the updated device entity using DeviceSchema.
   */
  pin(id: string, request: PinRequest): Observable<Device> {
    const body = {
      reason: request.reason,
      reason_detail: request.reasonDetail,
      pinned_until: request.pinnedUntil,
    };
    return this.http.post<unknown>(`${this.baseUrl}/${id}/pin`, body).pipe(
      map(data => DeviceSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Removes the 'pinned' status from a device, restoring TTL-based expiration.
   *
   * VALIDATION: Validates the updated device entity using DeviceSchema.
   */
  unpin(id: string): Observable<Device> {
    return this.http.post<unknown>(`${this.baseUrl}/${id}/unpin`, {}).pipe(
      map(data => DeviceSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Extends the time-to-live (TTL) for an ephemeral device session.
   *
   * VALIDATION: Validates the updated device entity using DeviceSchema.
   *
   * @param request - Optional request specifying the extension duration.
   */
  extend(id: string, request?: ExtendRequest): Observable<Device> {
    const body = request ? {seconds: request.seconds} : {};
    return this.http.post<unknown>(`${this.baseUrl}/${id}/extend`, body).pipe(
      map(data => DeviceSchema.parse(data)),
      catchError(this.handleError)
    );
  }
}
