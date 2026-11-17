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

import type {PipeTransform} from '@angular/core';
import {Pipe} from '@angular/core';

/**
 * Region to flag emoji mapping
 */
const REGION_FLAGS: Record<string, string> = {
  // US regions
  'us-central1': '🇺🇸',
  'us-east1': '🇺🇸',
  'us-east4': '🇺🇸',
  'us-west1': '🇺🇸',
  'us-west2': '🇺🇸',
  'us-west3': '🇺🇸',
  'us-west4': '🇺🇸',

  // Europe regions
  'europe-west1': '🇧🇪', // Belgium
  'europe-west2': '🇬🇧', // London
  'europe-west3': '🇩🇪', // Frankfurt
  'europe-west4': '🇳🇱', // Netherlands
  'europe-west6': '🇨🇭', // Zurich
  'europe-north1': '🇫🇮', // Finland
  'europe-central2': '🇵🇱', // Warsaw

  // Asia regions
  'asia-east1': '🇹🇼', // Taiwan
  'asia-east2': '🇭🇰', // Hong Kong
  'asia-northeast1': '🇯🇵', // Tokyo
  'asia-northeast2': '🇯🇵', // Osaka
  'asia-northeast3': '🇰🇷', // Seoul
  'asia-south1': '🇮🇳', // Mumbai
  'asia-southeast1': '🇸🇬', // Singapore
  'asia-southeast2': '🇮🇩', // Jakarta

  // Other
  'australia-southeast1': '🇦🇺',
  'australia-southeast2': '🇦🇺',
  'southamerica-east1': '🇧🇷',
  'northamerica-northeast1': '🇨🇦',
};

/**
 * Short region names for display
 */
const REGION_SHORT_NAMES: Record<string, string> = {
  'us-central1': 'US-C1',
  'us-east1': 'US-E1',
  'us-east4': 'US-E4',
  'us-west1': 'US-W1',
  'us-west2': 'US-W2',
  'us-west3': 'US-W3',
  'us-west4': 'US-W4',
  'europe-west1': 'EU-W1',
  'europe-west2': 'EU-W2',
  'europe-west3': 'EU-W3',
  'europe-west4': 'EU-W4',
  'europe-west6': 'EU-W6',
  'europe-north1': 'EU-N1',
  'europe-central2': 'EU-C2',
  'asia-east1': 'AS-E1',
  'asia-east2': 'AS-E2',
  'asia-northeast1': 'AS-NE1',
  'asia-northeast2': 'AS-NE2',
  'asia-northeast3': 'AS-NE3',
  'asia-south1': 'AS-S1',
  'asia-southeast1': 'AS-SE1',
  'asia-southeast2': 'AS-SE2',
  'australia-southeast1': 'AU-SE1',
  'australia-southeast2': 'AU-SE2',
  'southamerica-east1': 'SA-E1',
  'northamerica-northeast1': 'NA-NE1',
};

/**
 * Converts a GCP region/zone to a flag emoji
 *
 * Usage:
 *   {{ 'europe-west4-a' | appRegionFlag }}           -> "🇳🇱"
 *   {{ 'europe-west4-a' | appRegionFlag:'short' }}   -> "🇳🇱 EU-W4"
 *   {{ 'europe-west4-a' | appRegionFlag:'full' }}    -> "🇳🇱 Europe-West4"
 */
@Pipe({
  name: 'appRegionFlag',
  pure: true,
})
export class RegionFlagPipe implements PipeTransform {
  transform(value: string | null | undefined, format: 'flag' | 'short' | 'full' = 'flag'): string {
    if (!value) {
      return '';
    }

    // Extract region from zone (e.g., "europe-west4-a" -> "europe-west4")
    const region = this.extractRegion(value);
    const flag = REGION_FLAGS[region] || '🌐';

    switch (format) {
      case 'short': {
        const shortName = REGION_SHORT_NAMES[region] || region.toUpperCase();
        return `${flag} ${shortName}`;
      }

      case 'full': {
        const fullName = this.formatRegionName(region);
        return `${flag} ${fullName}`;
      }

      default:
        return flag;
    }
  }

  private extractRegion(zoneOrRegion: string): string {
    // Handle zones like "europe-west4-a" -> "europe-west4"
    const parts = zoneOrRegion.split('-');
    if (parts.length >= 3 && parts[parts.length - 1].length === 1) {
      return parts.slice(0, -1).join('-');
    }
    return zoneOrRegion;
  }

  private formatRegionName(region: string): string {
    // Convert "europe-west4" to "Europe-West4"
    return region
      .split('-')
      .map(part => part.charAt(0).toUpperCase() + part.slice(1))
      .join('-');
  }
}
