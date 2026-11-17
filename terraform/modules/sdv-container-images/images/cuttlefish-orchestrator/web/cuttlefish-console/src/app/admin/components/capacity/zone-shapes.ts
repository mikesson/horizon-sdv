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

import type {AdminZone} from '../../models/catalog-admin.model';

/**
 * How a zone's share of the curated shapes is worded. The board's tiles and the
 * offered-zone rows count against the same server-curated list, so both say it
 * the same way.
 *
 * A zone the catalog snapshot said nothing about reads as unreported, never as
 * offering none of the fleet's shapes. Those are different facts with
 * different fixes.
 */
export function shapesLabel(zone: AdminZone, curated: number): string {
  if (!zone.shapesReported) {
    return 'shapes unreported';
  }
  return `${zone.offeredShapes.length} of ${curated} shapes`;
}

/** The shapes themselves, for the row's tooltip. */
export function shapesTooltip(zone: AdminZone): string {
  if (!zone.shapesReported) {
    return 'Compute Engine did not report this zone, so the catalog is silent about it';
  }
  return zone.offeredShapes.join(', ') || 'None of the curated machine types';
}
