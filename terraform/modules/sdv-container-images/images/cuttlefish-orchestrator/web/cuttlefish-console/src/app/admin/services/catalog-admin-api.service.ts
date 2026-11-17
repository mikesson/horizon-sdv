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

import {HttpClient, httpResource} from '@angular/common/http';
import {computed, inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import type {MachineType} from '../../machine-types/models';
import {MachineTypeSchema} from '../../machine-types/models';
import {createApiErrorHandler, resolveBasePath} from '../../shared/utils';
import type {
  AdminZoneList,
  CatalogFreshness,
  MachineTypeAdminPatch,
} from '../models/catalog-admin.model';
import {
  AdminZoneListSchema,
  CatalogFreshnessSchema,
  EnabledZonesSchema,
} from '../models/catalog-admin.schema';
import type {AdminFamily, MachineFamilyPatch} from '../models/machine-family.model';
import {AdminFamilyListSchema} from '../models/machine-family.schema';

/** Catalog curation: zone enablement, machine-type flags, sync freshness. */
@Service()
export class CatalogAdminApiService {
  private readonly http = inject(HttpClient);
  private readonly handleError = createApiErrorHandler('Catalog', 'CatalogAdminApiService');

  private readonly freshnessResource = httpResource<CatalogFreshness>(
    () => resolveBasePath('/v2/admin/catalog/freshness'),
    {parse: raw => CatalogFreshnessSchema.parse(raw)}
  );

  /** Catalog snapshot age and its silences; null until first load. */
  readonly freshness = computed(() =>
    this.freshnessResource.hasValue() ? this.freshnessResource.value() : null
  );

  refreshFreshness(): void {
    this.freshnessResource.reload();
  }

  listZones(): Observable<AdminZoneList> {
    return this.http.get<unknown>(resolveBasePath('/v2/admin/zones')).pipe(
      map(data => AdminZoneListSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Replaces the enabled set whole and answers with what was stored. Zone
   * enablement is one operator decision read whole, so a single-zone toggle
   * sends the set it wants, never a patch of one member.
   */
  setEnabledZones(zones: string[]): Observable<string[]> {
    return this.http.put<unknown>(resolveBasePath('/v2/admin/zones/enabled'), {zones}).pipe(
      map(data => EnabledZonesSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * The curation view of the families, which is not the catalog's own family
   * listing: this one carries the families nobody admitted and the shapes too
   * small to offer, and those are exactly the rows curation acts on.
   */
  listFamilies(): Observable<AdminFamily[]> {
    return this.http.get<unknown>(resolveBasePath('/v2/admin/machine-families')).pipe(
      map(data => AdminFamilyListSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Curates one family. The response is the stored curation row, not the
   * family's shapes, so callers reload the listing to see what discovery made
   * of the change.
   */
  patchFamily(family: string, patch: MachineFamilyPatch): Observable<void> {
    const body: Record<string, unknown> = {};
    if (patch.isEnabled !== undefined) {
      body['is_enabled'] = patch.isEnabled;
    }
    if (patch.sortOrder !== undefined) {
      body['sort_order'] = patch.sortOrder;
    }
    if (patch.maxGpus !== undefined) {
      body['max_gpus'] = patch.maxGpus;
    }
    if (patch.defaultGpuType !== undefined) {
      body['default_gpu_type'] = patch.defaultGpuType;
    }
    if (patch.defaultGpuCount !== undefined) {
      body['default_gpu_count'] = patch.defaultGpuCount;
    }
    return this.http
      .patch<unknown>(resolveBasePath(`/v2/admin/machine-families/${family}`), body)
      .pipe(
        map(() => undefined),
        catchError(this.handleError)
      );
  }

  /**
   * The per-shape curation a family row cannot express: withholding one shape,
   * and electing the catalog default.
   */
  patchMachineType(id: string, patch: MachineTypeAdminPatch): Observable<MachineType> {
    const body: Record<string, unknown> = {};
    if (patch.isEnabled !== undefined) {
      body['is_enabled'] = patch.isEnabled;
    }
    if (patch.isDefault !== undefined) {
      body['is_default'] = patch.isDefault;
    }
    if (patch.maxDevices !== undefined) {
      body['max_devices'] = patch.maxDevices;
    }
    return this.http.patch<unknown>(resolveBasePath(`/v2/admin/machine-types/${id}`), body).pipe(
      map(data => MachineTypeSchema.parse(data)),
      catchError(this.handleError)
    );
  }
}
