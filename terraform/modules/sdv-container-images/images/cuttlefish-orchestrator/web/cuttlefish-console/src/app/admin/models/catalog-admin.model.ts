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

/** One zone Compute Engine offers, from GET /v2/admin/zones. */
export interface AdminZone {
  zone: string;
  region: string;
  /** GCE zone status: "UP", "DOWN", or "" when the snapshot is silent. */
  status: string;
  isEnabled: boolean;
  /** Curated machine types this zone stocks. */
  offeredShapes: string[];
  /**
   * Whether the catalog snapshot holds an answer for this zone. False with an
   * empty offeredShapes means Compute Engine said nothing, which is not the
   * same as the zone offering none of them.
   */
  shapesReported: boolean;
}

/** The zone curation list from GET /v2/admin/zones. */
export interface AdminZoneList {
  zones: AdminZone[];
  /** The machine types the fleet provisions, which availability is reported for. */
  curatedShapes: string[];
}

/** Catalog snapshot freshness from GET /v2/admin/catalog/freshness. */
export interface CatalogFreshness {
  /** When the snapshot was built; null until the first refresh lands. */
  builtAt: Date | null;
  /** Catalog scopes Compute Engine did not report. */
  partial: string[];
}

/**
 * Curation patch for PATCH /v2/admin/machine-types/{id}, the two moves a
 * family row cannot express. Display order and GPU policy are family curation
 * now, so nothing here writes them.
 */
export interface MachineTypeAdminPatch {
  /** Withholds one shape from an admitted family, or re-admits it. */
  isEnabled?: boolean;
  /** Only true is accepted. The default moves, and is never cleared. */
  isDefault?: boolean;
  /** Per-shape capacity override; 0 clears back to the computed fit. */
  maxDevices?: number;
}
