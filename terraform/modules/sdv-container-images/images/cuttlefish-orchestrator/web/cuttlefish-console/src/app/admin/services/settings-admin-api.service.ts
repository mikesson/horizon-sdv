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

import {HttpClient} from '@angular/common/http';
import {inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {forkJoin, of} from 'rxjs';
import {map} from 'rxjs/operators';
import type {PlatformLifetimes} from '../../shared/models/platform-lifetimes.schema';
import {PlatformLifetimesSchema} from '../../shared/models/platform-lifetimes.schema';
import {resolveBasePath} from '../../shared/utils';
import type {
  ArtifactSettings,
  DefaultsSettings,
  PlacementSettings,
  SpotSettings,
} from '../models/settings.model';
import {
  ArtifactSettingsSchema,
  DefaultsSettingsSchema,
  PlacementSettingsSchema,
  SpotSettingsSchema,
} from '../models/settings.schema';

/**
 * Transport for the system-config GET/PUT pairs, grouped the way the
 * Settings page renders them.
 *
 * One method per section rather than one for the page. Consolidating four
 * pages into one would otherwise mean any single failing endpoint blanks all
 * thirteen settings. Each section loads and fails on its own.
 *
 * Writes are per-key and skip what did not change, because every PUT lands a
 * config_change audit entry, so re-sending an untouched value pollutes the
 * Activity trail with edits nobody made.
 */
@Service()
export class SettingsAdminApiService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/admin/system-config');

  getPlacement(): Observable<PlacementSettings> {
    return forkJoin({
      zonePin: this.http.get<unknown>(`${this.baseUrl}/zone-pin`),
      quotaMargin: this.http.get<unknown>(`${this.baseUrl}/quota-margin`),
      v1ZoneHint: this.http.get<unknown>(`${this.baseUrl}/v1-zone-hint`),
    }).pipe(map(raw => PlacementSettingsSchema.parse(raw)));
  }

  getSpot(): Observable<SpotSettings> {
    return forkJoin({
      autoDefault: this.http.get<unknown>(`${this.baseUrl}/spot-auto-default`),
      minUptime: this.http.get<unknown>(`${this.baseUrl}/spot-min-uptime`),
    }).pipe(map(raw => SpotSettingsSchema.parse(raw)));
  }

  getDefaults(): Observable<DefaultsSettings> {
    return forkJoin({
      gpuMachineType: this.http.get<unknown>(`${this.baseUrl}/default-gpu-machine-type`),
      upstreamBuild: this.http.get<unknown>(`${this.baseUrl}/default-upstream-build`),
      dockerHost: this.http.get<unknown>(`${this.baseUrl}/docker-host`),
    }).pipe(map(raw => DefaultsSettingsSchema.parse(raw)));
  }

  getLifetimes(): Observable<PlatformLifetimes> {
    return this.http
      .get<unknown>(`${this.baseUrl}/lifetimes`)
      .pipe(map(raw => PlatformLifetimesSchema.parse(raw)));
  }

  /** The group endpoint writes all four keys, so any change sends one PUT. */
  saveLifetimes(next: PlatformLifetimes, baseline: PlatformLifetimes): Observable<unknown> {
    if (
      next.deviceTtlHours === baseline.deviceTtlHours &&
      next.extendHours === baseline.extendHours &&
      next.extendCount === baseline.extendCount &&
      next.hostTtlHours === baseline.hostTtlHours
    ) {
      return of([]);
    }
    return this.http.put<unknown>(`${this.baseUrl}/lifetimes`, {
      device_ttl_hours: next.deviceTtlHours,
      extend_hours: next.extendHours,
      extend_count: next.extendCount,
      host_ttl_hours: next.hostTtlHours,
    });
  }

  getArtifacts(): Observable<ArtifactSettings> {
    return this.http
      .get<unknown>(`${this.baseUrl}/artifact-buckets`)
      .pipe(map(raw => ArtifactSettingsSchema.parse(raw)));
  }

  /** Writes only the placement fields that differ from `baseline`. */
  savePlacement(next: PlacementSettings, baseline: PlacementSettings): Observable<unknown> {
    const puts: Array<Observable<unknown>> = [];
    if (next.zonePin !== baseline.zonePin) {
      puts.push(this.http.put<unknown>(`${this.baseUrl}/zone-pin`, {value: next.zonePin}));
    }
    if (next.quotaMarginPercent !== baseline.quotaMarginPercent) {
      puts.push(
        this.http.put<unknown>(`${this.baseUrl}/quota-margin`, {value: next.quotaMarginPercent})
      );
    }
    return puts.length > 0 ? forkJoin(puts) : of([]);
  }

  /** The paired endpoint writes both tiers, so either change sends one PUT. */
  saveSpot(next: SpotSettings, baseline: SpotSettings): Observable<unknown> {
    if (
      next.minUptimeSingleSeconds === baseline.minUptimeSingleSeconds &&
      next.minUptimeBatchSeconds === baseline.minUptimeBatchSeconds
    ) {
      return of([]);
    }
    return this.http.put<unknown>(`${this.baseUrl}/spot-min-uptime`, {
      single_seconds: next.minUptimeSingleSeconds,
      batch_seconds: next.minUptimeBatchSeconds,
    });
  }

  saveDefaults(next: DefaultsSettings, baseline: DefaultsSettings): Observable<unknown> {
    const puts: Array<Observable<unknown>> = [];
    if (next.gpuMachineType !== baseline.gpuMachineType) {
      puts.push(
        this.http.put<unknown>(`${this.baseUrl}/default-gpu-machine-type`, {
          value: next.gpuMachineType,
        })
      );
    }
    if (
      next.upstreamBranch !== baseline.upstreamBranch ||
      next.upstreamTarget !== baseline.upstreamTarget
    ) {
      puts.push(
        this.http.put<unknown>(`${this.baseUrl}/default-upstream-build`, {
          branch: next.upstreamBranch,
          target: next.upstreamTarget,
        })
      );
    }
    if (
      next.dockerMachineType !== baseline.dockerMachineType ||
      next.dockerTtlHours !== baseline.dockerTtlHours
    ) {
      puts.push(
        this.http.put<unknown>(`${this.baseUrl}/docker-host`, {
          machine_type: next.dockerMachineType,
          ttl_hours: next.dockerTtlHours,
        })
      );
    }
    return puts.length > 0 ? forkJoin(puts) : of([]);
  }

  /**
   * Artifact buckets are one endpoint carrying three keys, so a bucket edit
   * necessarily re-sends the toggle and a toggle re-sends the buckets. The
   * capture switch therefore calls this with the section's baseline, never its
   * draft, or flipping it would persist bucket text nobody saved.
   */
  saveArtifacts(next: ArtifactSettings, baseline: ArtifactSettings): Observable<unknown> {
    if (
      next.bugreportBucket === baseline.bugreportBucket &&
      next.recordingBucket === baseline.recordingBucket &&
      next.captureOnError === baseline.captureOnError
    ) {
      return of([]);
    }
    return this.http.put<unknown>(`${this.baseUrl}/artifact-buckets`, {
      bugreport_bucket: next.bugreportBucket,
      recording_bucket: next.recordingBucket,
      capture_on_error: next.captureOnError,
    });
  }

  /** The v1-zone-hint switch: one flip, one audited PUT. */
  saveV1ZoneHint(value: boolean): Observable<unknown> {
    return this.http.put<unknown>(`${this.baseUrl}/v1-zone-hint`, {value});
  }

  /** The Spot auto-default switch. */
  saveSpotAutoDefault(value: boolean): Observable<unknown> {
    return this.http.put<unknown>(`${this.baseUrl}/spot-auto-default`, {value});
  }
}
