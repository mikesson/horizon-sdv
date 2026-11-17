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

/**
 * Geographic groupings for GCP regions.
 */
type Geography = 'americas' | 'europe' | 'asia-pacific' | 'middle-east' | 'africa';

/**
 * Information about a GCP region.
 */
export interface RegionInfo {
  /** Region identifier (e.g., "us-central1"). */
  region: string;
  /** Human-friendly display name (e.g., "Iowa"). */
  displayName: string;
  geography: Geography;
  country: string;
  /** Whether this region uses low CO2 energy. */
  lowCO2: boolean;
}

/**
 * Display labels for geographic groupings.
 */
const GEOGRAPHY_LABELS: Record<Geography, string> = {
  americas: 'Americas',
  europe: 'Europe',
  'asia-pacific': 'Asia Pacific',
  'middle-east': 'Middle East',
  africa: 'Africa',
};

/**
 * Order for displaying geographic groupings.
 */
const GEOGRAPHY_ORDER: Geography[] = [
  'americas',
  'europe',
  'asia-pacific',
  'middle-east',
  'africa',
];

/**
 * All GCP regions with metadata.
 * Data sourced from https://cloud.google.com/compute/docs/regions-zones
 */
export const GCP_REGIONS: RegionInfo[] = [
  // Americas
  {
    region: 'us-central1',
    displayName: 'Iowa',
    geography: 'americas',
    country: 'United States',
    lowCO2: true,
  },
  {
    region: 'us-east1',
    displayName: 'South Carolina',
    geography: 'americas',
    country: 'United States',
    lowCO2: false,
  },
  {
    region: 'us-east4',
    displayName: 'Virginia',
    geography: 'americas',
    country: 'United States',
    lowCO2: false,
  },
  {
    region: 'us-east5',
    displayName: 'Columbus',
    geography: 'americas',
    country: 'United States',
    lowCO2: false,
  },
  {
    region: 'us-south1',
    displayName: 'Dallas',
    geography: 'americas',
    country: 'United States',
    lowCO2: false,
  },
  {
    region: 'us-west1',
    displayName: 'Oregon',
    geography: 'americas',
    country: 'United States',
    lowCO2: true,
  },
  {
    region: 'us-west2',
    displayName: 'Los Angeles',
    geography: 'americas',
    country: 'United States',
    lowCO2: false,
  },
  {
    region: 'us-west3',
    displayName: 'Salt Lake City',
    geography: 'americas',
    country: 'United States',
    lowCO2: false,
  },
  {
    region: 'us-west4',
    displayName: 'Las Vegas',
    geography: 'americas',
    country: 'United States',
    lowCO2: false,
  },
  {
    region: 'northamerica-northeast1',
    displayName: 'Montréal',
    geography: 'americas',
    country: 'Canada',
    lowCO2: true,
  },
  {
    region: 'northamerica-northeast2',
    displayName: 'Toronto',
    geography: 'americas',
    country: 'Canada',
    lowCO2: true,
  },
  {
    region: 'northamerica-south1',
    displayName: 'Querétaro',
    geography: 'americas',
    country: 'Mexico',
    lowCO2: false,
  },
  {
    region: 'southamerica-east1',
    displayName: 'São Paulo',
    geography: 'americas',
    country: 'Brazil',
    lowCO2: true,
  },
  {
    region: 'southamerica-west1',
    displayName: 'Santiago',
    geography: 'americas',
    country: 'Chile',
    lowCO2: true,
  },

  // Europe
  {
    region: 'europe-west1',
    displayName: 'Belgium',
    geography: 'europe',
    country: 'Belgium',
    lowCO2: true,
  },
  {
    region: 'europe-west2',
    displayName: 'London',
    geography: 'europe',
    country: 'United Kingdom',
    lowCO2: true,
  },
  {
    region: 'europe-west3',
    displayName: 'Frankfurt',
    geography: 'europe',
    country: 'Germany',
    lowCO2: true,
  },
  {
    region: 'europe-west4',
    displayName: 'Netherlands',
    geography: 'europe',
    country: 'Netherlands',
    lowCO2: true,
  },
  {
    region: 'europe-west6',
    displayName: 'Zürich',
    geography: 'europe',
    country: 'Switzerland',
    lowCO2: true,
  },
  {
    region: 'europe-west8',
    displayName: 'Milan',
    geography: 'europe',
    country: 'Italy',
    lowCO2: false,
  },
  {
    region: 'europe-west9',
    displayName: 'Paris',
    geography: 'europe',
    country: 'France',
    lowCO2: true,
  },
  {
    region: 'europe-west10',
    displayName: 'Berlin',
    geography: 'europe',
    country: 'Germany',
    lowCO2: true,
  },
  {
    region: 'europe-west12',
    displayName: 'Turin',
    geography: 'europe',
    country: 'Italy',
    lowCO2: false,
  },
  {
    region: 'europe-north1',
    displayName: 'Finland',
    geography: 'europe',
    country: 'Finland',
    lowCO2: true,
  },
  {
    region: 'europe-north2',
    displayName: 'Stockholm',
    geography: 'europe',
    country: 'Sweden',
    lowCO2: true,
  },
  {
    region: 'europe-central2',
    displayName: 'Warsaw',
    geography: 'europe',
    country: 'Poland',
    lowCO2: false,
  },
  {
    region: 'europe-southwest1',
    displayName: 'Madrid',
    geography: 'europe',
    country: 'Spain',
    lowCO2: true,
  },

  // Asia Pacific
  {
    region: 'asia-east1',
    displayName: 'Taiwan',
    geography: 'asia-pacific',
    country: 'Taiwan',
    lowCO2: false,
  },
  {
    region: 'asia-east2',
    displayName: 'Hong Kong',
    geography: 'asia-pacific',
    country: 'Hong Kong',
    lowCO2: false,
  },
  {
    region: 'asia-northeast1',
    displayName: 'Tokyo',
    geography: 'asia-pacific',
    country: 'Japan',
    lowCO2: false,
  },
  {
    region: 'asia-northeast2',
    displayName: 'Osaka',
    geography: 'asia-pacific',
    country: 'Japan',
    lowCO2: false,
  },
  {
    region: 'asia-northeast3',
    displayName: 'Seoul',
    geography: 'asia-pacific',
    country: 'South Korea',
    lowCO2: false,
  },
  {
    region: 'asia-south1',
    displayName: 'Mumbai',
    geography: 'asia-pacific',
    country: 'India',
    lowCO2: false,
  },
  {
    region: 'asia-south2',
    displayName: 'Delhi',
    geography: 'asia-pacific',
    country: 'India',
    lowCO2: false,
  },
  {
    region: 'asia-southeast1',
    displayName: 'Singapore',
    geography: 'asia-pacific',
    country: 'Singapore',
    lowCO2: false,
  },
  {
    region: 'asia-southeast2',
    displayName: 'Jakarta',
    geography: 'asia-pacific',
    country: 'Indonesia',
    lowCO2: false,
  },
  {
    region: 'australia-southeast1',
    displayName: 'Sydney',
    geography: 'asia-pacific',
    country: 'Australia',
    lowCO2: false,
  },
  {
    region: 'australia-southeast2',
    displayName: 'Melbourne',
    geography: 'asia-pacific',
    country: 'Australia',
    lowCO2: false,
  },

  // Middle East
  {
    region: 'me-west1',
    displayName: 'Tel Aviv',
    geography: 'middle-east',
    country: 'Israel',
    lowCO2: false,
  },
  {
    region: 'me-central1',
    displayName: 'Doha',
    geography: 'middle-east',
    country: 'Qatar',
    lowCO2: false,
  },
  {
    region: 'me-central2',
    displayName: 'Dammam',
    geography: 'middle-east',
    country: 'Saudi Arabia',
    lowCO2: false,
  },

  // Africa
  {
    region: 'africa-south1',
    displayName: 'Johannesburg',
    geography: 'africa',
    country: 'South Africa',
    lowCO2: false,
  },
];

/**
 * Map of region ID to RegionInfo for quick lookup.
 */
const REGION_MAP = new Map<string, RegionInfo>(GCP_REGIONS.map(r => [r.region, r]));

/**
 * Get region info by region ID.
 */
export function getRegionInfo(region: string): RegionInfo | undefined {
  return REGION_MAP.get(region);
}

/**
 * Get regions grouped by geography.
 */
function getRegionsByGeography(): Map<Geography, RegionInfo[]> {
  const grouped = new Map<Geography, RegionInfo[]>();

  for (const geo of GEOGRAPHY_ORDER) {
    grouped.set(geo, []);
  }

  for (const region of GCP_REGIONS) {
    const list = grouped.get(region.geography);
    if (list) {
      list.push(region);
    }
  }

  return grouped;
}

/**
 * Extract region from a zone ID (e.g., "us-central1-a" -> "us-central1").
 */
export function regionFromZone(zone: string): string {
  const lastDash = zone.lastIndexOf('-');
  if (lastDash === -1) {
    return zone;
  }
  return zone.substring(0, lastDash);
}

/**
 * Interface for grouped regions by geography.
 */
export interface GeographyGroup {
  geography: Geography;
  label: string;
  regions: RegionInfo[];
}

/**
 * Get regions as an array of geography groups for display.
 */
export function getGeographyGroups(): GeographyGroup[] {
  const grouped = getRegionsByGeography();
  return GEOGRAPHY_ORDER.map(geo => ({
    geography: geo,
    label: GEOGRAPHY_LABELS[geo],
    regions: grouped.get(geo) ?? [],
  }));
}
