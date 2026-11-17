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
import {createApiErrorHandler, resolveBasePath} from '../../shared/utils';
import type {Collection} from '../../shared/utils/paged-collection';
import {collectPages, MAX_PAGE_SIZE} from '../../shared/utils/paged-collection';
import type {
  CreateHostRequest,
  Host,
  HostListFilter,
  HostListResponse,
  RetryHostRequest,
} from '../models/host.model';
import {HostListResponseSchema, HostSchema} from '../models/host.schema';

/**
 * Manages the HTTP transport layer for the Host vertical slice.
 *
 * This service handles GCE instance provisioning, status synchronization,
 * and lifecycle management (start/stop/delete) for Cuttlefish hosts.
 */
@Service()
export class HostApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/hosts');
  private readonly handleError = createApiErrorHandler('Host', 'HostApiService');

  /**
   * Retrieves one window of GCE hosts matching the filter.
   *
   * VALIDATION: Uses HostListResponseSchema to validate backend response.
   * TRANSFORMATION: Maps camelCase filters to snake_case query params.
   *
   * @param filter - Optional criteria to filter hosts by zone, status, etc.
   */
  list(filter?: HostListFilter): Observable<HostListResponse> {
    // Always request the backend's maximum page size: its default (20)
    // silently truncates fleet lists. An explicit filter.limit still wins.
    let params = new HttpParams().set('limit', String(filter?.limit ?? MAX_PAGE_SIZE));

    if (filter) {
      if (filter.zone) {
        params = params.set('zone', filter.zone);
      }
      if (filter.status) {
        params = params.set('status', filter.status);
      }
      if (filter.mine) {
        params = params.set('mine', 'true');
      }
      if (filter.createdBy) {
        params = params.set('created_by', filter.createdBy);
      }
      // Server-side because the class is derived from three columns the
      // response does not carry separately enough to re-derive here.
      if (filter.hostClass) {
        params = params.set('class', filter.hostClass);
      }
      if (filter.pageToken) {
        params = params.set('page_token', filter.pageToken);
      }
    }

    return this.http.get<unknown>(this.baseUrl, {params}).pipe(
      map(data => HostListResponseSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Retrieves every host matching the filter, following the server's pages.
   *
   * This is what the store loads: the device-create host picker offers what is
   * in the store and the capacity KPIs sum it, so a fleet past one page would
   * hide hosts a device could have been placed on.
   */
  listAll(filter?: HostListFilter): Observable<Collection<Host>> {
    return collectPages(window => this.list({...filter, ...window}));
  }

  /**
   * Retrieves a single host entity by ID.
   */
  get(id: string): Observable<Host> {
    return this.http.get<unknown>(`${this.baseUrl}/${id}`).pipe(
      map(data => HostSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Provisions a new GCE Virtual Machine to act as a Cuttlefish Host.
   *
   * This operation triggers a complex backend workflow:
   * 1. Validates quota.
   * 2. Calls GCE API to create the VM.
   * 3. Configures startup scripts for the Host Orchestrator.
   *
   * The Idempotency-Key is what makes a retry safe: host names are not unique
   * and nothing else dedupes, so a create re-sent after a lost response would
   * otherwise provision a second billed VM. The caller owns the key's
   * lifetime; it must stay stable across retries of one intent and change when
   * the body does (the backend 422s a key reused with a different request).
   *
   * @param request - Configuration for the new VM (zone, machine type, etc.).
   * @returns Observable of the newly created Host entity (usually in 'provisioning' state).
   */
  create(request: CreateHostRequest, idempotencyKey: string): Observable<Host> {
    const body: Record<string, unknown> = {
      name: request.name,
      zone: request.zone,
      machine_type: request.machineType || 'n1-standard-8',
      max_devices: request.maxDevices || 4,
    };

    // Only include GPU params if GPU is configured (not for ARM/bare metal)
    if (request.gpuType && request.gpuCount && request.gpuCount > 0) {
      body['gpu_type'] = request.gpuType;
      body['gpu_count'] = request.gpuCount;
    }

    if (request.ttlHours) {
      body['ttl_hours'] = request.ttlHours;
    }

    if (request.provisioningModel) {
      body['provisioning_model'] = request.provisioningModel;
    }

    if (request.exclusive) {
      body['exclusive'] = true;
    }

    if (request.minCpuPlatform) {
      body['min_cpu_platform'] = request.minCpuPlatform;
    }

    // Omitted rather than 0: the backend reads 0 as "use the default" but
    // rejects any non-zero value under 10, so an empty field must not travel.
    if (request.bootDiskSizeGb) {
      body['boot_disk_size_gb'] = request.bootDiskSizeGb;
    }

    return this.http
      .post<unknown>(this.baseUrl, body, {headers: {'Idempotency-Key': idempotencyKey}})
      .pipe(
        map(data => HostSchema.parse(data)),
        catchError(this.handleError)
      );
  }

  /**
   * Permanently deletes a host and its underlying GCE instance.
   *
   * WARNING: This will also terminate all running Cuttlefish devices on the host.
   */
  delete(id: string): Observable<void> {
    return this.http.delete<void>(`${this.baseUrl}/${id}`);
  }

  /**
   * Starts a stopped GCE instance.
   */
  start(id: string): Observable<{message: string}> {
    return this.http.post<{message: string}>(`${this.baseUrl}/${id}/start`, {});
  }

  /**
   * Stops a running GCE instance.
   */
  stop(id: string): Observable<{message: string}> {
    return this.http.post<{message: string}>(`${this.baseUrl}/${id}/stop`, {});
  }

  /**
   * Triggers an immediate state synchronization with the GCE API.
   *
   * This is useful when the orchestrator's state drifts from reality
   * (e.g., if a VM was preempted or deleted externally).
   */
  sync(id: string): Observable<{message: string}> {
    return this.http.post<{message: string}>(`${this.baseUrl}/${id}/sync`, {});
  }

  /**
   * Retries a preempted or failed host by recreating the GCE instance.
   *
   * This is used for Spot VMs that have been preempted by GCE, or hosts
   * that encountered an error during provisioning.
   *
   * @param request - Optional retry options (e.g., fallback to standard VM).
   */
  /** Reshapes a live host; the server refuses a limit below its live devices. */
  updateMaxDevices(id: string, maxDevices: number): Observable<Host> {
    return this.http.patch<unknown>(`${this.baseUrl}/${id}`, {max_devices: maxDevices}).pipe(
      map(data => HostSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Raises a reserved host's lease by `hours` (1 to 168 per request) and
   * returns the updated host. The server's refusal detail names the reason
   * (wrong class, pinned devices, the VM stop time), so callers show it as sent.
   */
  extend(id: string, hours: number): Observable<Host> {
    return this.http.post<unknown>(`${this.baseUrl}/${id}/extend`, {hours}).pipe(
      map(data => HostSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  retry(id: string, request?: RetryHostRequest): Observable<Host> {
    const body = {
      fallback_to_standard: request?.fallbackToStandard ?? false,
    };
    return this.http.post<unknown>(`${this.baseUrl}/${id}/retry`, body).pipe(
      map(data => HostSchema.parse(data)),
      catchError(this.handleError)
    );
  }
}
