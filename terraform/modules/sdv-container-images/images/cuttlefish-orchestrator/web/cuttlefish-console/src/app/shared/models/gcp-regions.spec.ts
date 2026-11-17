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

import {describe, expect, it} from 'vitest';
import {GCP_REGIONS, getGeographyGroups, getRegionInfo, regionFromZone} from './gcp-regions';

describe('GCP Regions', () => {
  describe('GCP_REGIONS constant', () => {
    it('should contain all major GCP regions', () => {
      expect(GCP_REGIONS.length).toBeGreaterThan(40);
    });

    it('should have required fields for each region', () => {
      for (const region of GCP_REGIONS) {
        expect(region.region).toBeTruthy();
        expect(region.displayName).toBeTruthy();
        expect(region.country).toBeTruthy();
        expect(region.geography).toBeTruthy();
        expect(typeof region.lowCO2).toBe('boolean');
      }
    });

    it('should include common regions', () => {
      const regionIds = GCP_REGIONS.map(r => r.region);
      expect(regionIds).toContain('us-central1');
      expect(regionIds).toContain('europe-west4');
      expect(regionIds).toContain('asia-east1');
    });
  });

  describe('getRegionInfo', () => {
    it('should return region info for valid region', () => {
      const info = getRegionInfo('us-central1');
      expect(info).toBeDefined();
      expect(info?.displayName).toBe('Iowa');
      expect(info?.country).toBe('United States');
      expect(info?.geography).toBe('americas');
    });

    it('should return undefined for unknown region', () => {
      const info = getRegionInfo('unknown-region');
      expect(info).toBeUndefined();
    });

    it('should return info for european regions', () => {
      const info = getRegionInfo('europe-west4');
      expect(info).toBeDefined();
      expect(info?.displayName).toBe('Netherlands');
      expect(info?.geography).toBe('europe');
    });

    it('should return info for asia pacific regions', () => {
      const info = getRegionInfo('asia-east1');
      expect(info).toBeDefined();
      expect(info?.country).toBe('Taiwan');
      expect(info?.geography).toBe('asia-pacific');
    });
  });

  describe('regionFromZone', () => {
    it('should extract region from zone name', () => {
      expect(regionFromZone('us-central1-a')).toBe('us-central1');
      expect(regionFromZone('us-central1-b')).toBe('us-central1');
      expect(regionFromZone('us-central1-f')).toBe('us-central1');
    });

    it('should handle zones with double-digit suffixes', () => {
      expect(regionFromZone('europe-west4-a')).toBe('europe-west4');
      expect(regionFromZone('asia-northeast1-c')).toBe('asia-northeast1');
    });

    it('should handle zones with long region names', () => {
      expect(regionFromZone('northamerica-northeast1-a')).toBe('northamerica-northeast1');
      expect(regionFromZone('southamerica-east1-b')).toBe('southamerica-east1');
    });

    it('should return empty string for invalid zone', () => {
      expect(regionFromZone('')).toBe('');
      expect(regionFromZone('invalid')).toBe('invalid');
    });
  });

  describe('getGeographyGroups', () => {
    it('should return all geography groups', () => {
      const groups = getGeographyGroups();
      expect(groups.length).toBeGreaterThan(0);
    });

    it('should have Americas group', () => {
      const groups = getGeographyGroups();
      const americas = groups.find(g => g.geography === 'americas');
      expect(americas).toBeDefined();
      expect(americas?.label).toBe('Americas');
      expect(americas?.regions.length).toBeGreaterThan(0);
    });

    it('should have Europe group', () => {
      const groups = getGeographyGroups();
      const europe = groups.find(g => g.geography === 'europe');
      expect(europe).toBeDefined();
      expect(europe?.label).toBe('Europe');
      expect(europe?.regions.length).toBeGreaterThan(0);
    });

    it('should have Asia Pacific group', () => {
      const groups = getGeographyGroups();
      const asiaPacific = groups.find(g => g.geography === 'asia-pacific');
      expect(asiaPacific).toBeDefined();
      expect(asiaPacific?.label).toBe('Asia Pacific');
      expect(asiaPacific?.regions.length).toBeGreaterThan(0);
    });

    it('should include regions in their correct geography', () => {
      const groups = getGeographyGroups();
      const americas = groups.find(g => g.geography === 'americas');
      const americasRegionIds = americas?.regions.map(r => r.region) ?? [];
      expect(americasRegionIds).toContain('us-central1');
      expect(americasRegionIds).not.toContain('europe-west4');
    });
  });
});
