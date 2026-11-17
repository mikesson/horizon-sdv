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
import {of} from 'rxjs';
import {catchError, map, shareReplay} from 'rxjs/operators';
import {z} from 'zod';
import {getRegionInfo, regionFromZone} from '../models/gcp-regions';
import type {ZoneAvailability} from '../models/zone-availability.schema';
import {ZoneAvailabilityListSchema} from '../models/zone-availability.schema';
import {resolveBasePath} from '../utils';

const ZonePinSchema = z.object({value: z.string()});

/**
 * Service for fetching GCE catalog information.
 *
 * Provides zone availability and resource information from the backend catalog.
 */
@Service()
export class GceCatalogService {
  private readonly http = inject(HttpClient);
  private readonly baseUrl = resolveBasePath('/v2/zones/availability');

  private zoneCache$: Observable<ZoneAvailability[]> | null = null;

  /**
   * Fetches zone availability from the GCE catalog.
   *
   * @param machineType - Optional machine type to check availability for.
   * @param gpuType - Optional GPU type to check availability for.
   */
  getZoneAvailability(machineType?: string, gpuType?: string): Observable<ZoneAvailability[]> {
    let params = new HttpParams();

    if (machineType) {
      params = params.set('machine_type', machineType);
    }
    if (gpuType) {
      params = params.set('gpu_type', gpuType);
    }

    return this.http.get<unknown>(this.baseUrl, {params}).pipe(
      map(data => ZoneAvailabilityListSchema.parse(data).zones),
      catchError(err => {
        console.error('[GceCatalogService] Failed to fetch zone availability', err);
        // Return empty array on error to allow graceful degradation
        return of([]);
      })
    );
  }

  /**
   * Fetches the operator's placement zone pin (system_config
   * `placement.zone_pin`); empty when unset or on error. When set, all
   * server-ranked placement is restricted to this zone, so the create form
   * preselects it.
   */
  getZonePin(): Observable<string> {
    return this.http.get<unknown>(resolveBasePath('/v2/admin/system-config/zone-pin')).pipe(
      map(data => ZonePinSchema.parse(data).value),
      catchError(() => of(''))
    );
  }

  /**
   * Fetches zone availability with caching.
   * This version caches the result for better performance when called multiple times.
   */
  getZoneAvailabilityCached(): Observable<ZoneAvailability[]> {
    this.zoneCache$ ??= this.getZoneAvailability().pipe(shareReplay(1));
    return this.zoneCache$;
  }

  /**
   * Clears the zone availability cache.
   * Call this when you need fresh data.
   */
  clearCache(): void {
    this.zoneCache$ = null;
  }

  /**
   * Gets unique regions from available zones.
   *
   * @param machineType - Optional machine type to filter by.
   * @param gpuType - Optional GPU type to filter by.
   */
  getAvailableRegions(machineType?: string, gpuType?: string): Observable<string[]> {
    return this.getZoneAvailability(machineType, gpuType).pipe(
      map(zones => {
        const regions = new Set(zones.map(z => z.region));
        return [...regions].sort();
      })
    );
  }

  /**
   * Creates a display label for a zone, including availability info.
   */
  formatZoneLabel(zone: ZoneAvailability): string {
    const regionLabel = this.getRegionLabel(zone.region);
    let label = `${regionLabel} (${zone.zone})`;

    if (zone.status === 'unavailable') {
      label += ' - Unavailable';
    } else if (zone.status === 'degraded') {
      label += ' - Degraded';
    }

    return label;
  }

  /**
   * Gets a human-readable label for a GCP region.
   * Uses the centralized GCP regions model.
   */
  getRegionLabel(region: string): string {
    const info = getRegionInfo(region);
    return info?.displayName || region;
  }

  /**
   * Extracts the region from a zone ID.
   */
  regionFromZone(zone: string): string {
    return regionFromZone(zone);
  }
}
