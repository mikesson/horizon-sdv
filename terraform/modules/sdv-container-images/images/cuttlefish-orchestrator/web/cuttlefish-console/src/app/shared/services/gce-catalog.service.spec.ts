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

import {provideHttpClient} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {ZoneAvailability} from '../models/zone-availability.schema';
import {GceCatalogService} from './gce-catalog.service';

describe('GceCatalogService', () => {
  let service: GceCatalogService;
  let httpMock: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [GceCatalogService, provideHttpClient(), provideHttpClientTesting()],
    });

    service = TestBed.inject(GceCatalogService);
    httpMock = TestBed.inject(HttpTestingController);
  });

  describe('getZoneAvailability', () => {
    it('should fetch zone availability without filters', () => {
      const mockResponse = {
        zones: [
          {
            zone: 'us-central1-a',
            region: 'us-central1',
            status: 'available',
            is_enabled: true,
          },
          {
            zone: 'us-central1-b',
            region: 'us-central1',
            status: 'available',
            is_enabled: true,
          },
        ],
      };

      let result: ZoneAvailability[] = [];
      service.getZoneAvailability().subscribe(zones => {
        result = zones;
      });

      const req = httpMock.expectOne('/v2/zones/availability');
      expect(req.request.method).toBe('GET');
      req.flush(mockResponse);

      expect(result).toHaveLength(2);
      expect(result[0].zone).toBe('us-central1-a');
      expect(result[0].region).toBe('us-central1');
      expect(result[0].status).toBe('available');
      expect(result[0].isEnabled).toBe(true);
    });

    it('should pass machine_type filter parameter', () => {
      const mockResponse = {zones: []};

      service.getZoneAvailability('n1-standard-8').subscribe();

      const req = httpMock.expectOne('/v2/zones/availability?machine_type=n1-standard-8');
      expect(req.request.method).toBe('GET');
      req.flush(mockResponse);
    });

    it('should pass gpu_type filter parameter', () => {
      const mockResponse = {zones: []};

      service.getZoneAvailability(undefined, 'nvidia-tesla-t4').subscribe();

      const req = httpMock.expectOne('/v2/zones/availability?gpu_type=nvidia-tesla-t4');
      expect(req.request.method).toBe('GET');
      req.flush(mockResponse);
    });

    it('should pass both filter parameters', () => {
      const mockResponse = {zones: []};

      service.getZoneAvailability('n1-standard-8', 'nvidia-tesla-t4').subscribe();

      const req = httpMock.expectOne(
        '/v2/zones/availability?machine_type=n1-standard-8&gpu_type=nvidia-tesla-t4'
      );
      expect(req.request.method).toBe('GET');
      req.flush(mockResponse);
    });

    it('should transform snake_case response to camelCase', () => {
      const mockResponse = {
        zones: [
          {
            zone: 'europe-west4-a',
            region: 'europe-west4',
            status: 'available',
            is_enabled: true,
            machine_type_available: true,
            gpu_available: true,
            gpu_max_count: 4,
          },
        ],
      };

      let result: ZoneAvailability[] = [];
      service.getZoneAvailability().subscribe(zones => {
        result = zones;
      });

      const req = httpMock.expectOne('/v2/zones/availability');
      req.flush(mockResponse);

      expect(result[0].machineTypeAvailable).toBe(true);
      expect(result[0].gpuAvailable).toBe(true);
      expect(result[0].gpuMaxCount).toBe(4);
    });

    it('should handle camelCase response fields', () => {
      const mockResponse = {
        zones: [
          {
            zone: 'asia-east1-a',
            region: 'asia-east1',
            status: 'degraded',
            isEnabled: true,
            machineTypeAvailable: false,
            gpuAvailable: false,
          },
        ],
      };

      let result: ZoneAvailability[] = [];
      service.getZoneAvailability().subscribe(zones => {
        result = zones;
      });

      const req = httpMock.expectOne('/v2/zones/availability');
      req.flush(mockResponse);

      expect(result[0].status).toBe('degraded');
      expect(result[0].machineTypeAvailable).toBe(false);
    });

    it('should return empty array on error', () => {
      let result: ZoneAvailability[] = [];
      service.getZoneAvailability().subscribe(zones => {
        result = zones;
      });

      const req = httpMock.expectOne('/v2/zones/availability');
      req.error(new ErrorEvent('Network error'));

      expect(result).toEqual([]);
    });

    it('should handle null zones array', () => {
      const mockResponse = {zones: null};

      let result: ZoneAvailability[] = [];
      service.getZoneAvailability().subscribe(zones => {
        result = zones;
      });

      const req = httpMock.expectOne('/v2/zones/availability');
      req.flush(mockResponse);

      expect(result).toEqual([]);
    });
  });

  describe('formatZoneLabel', () => {
    it('should format zone with region label', () => {
      const zone: ZoneAvailability = {
        zone: 'us-central1-a',
        region: 'us-central1',
        status: 'available',
        isEnabled: true,
      };

      const label = service.formatZoneLabel(zone);
      expect(label).toBe('Iowa (us-central1-a)');
    });

    it('should append unavailable indicator', () => {
      const zone: ZoneAvailability = {
        zone: 'us-central1-b',
        region: 'us-central1',
        status: 'unavailable',
        isEnabled: false,
      };

      const label = service.formatZoneLabel(zone);
      expect(label).toContain('Unavailable');
    });

    it('should append degraded indicator', () => {
      const zone: ZoneAvailability = {
        zone: 'europe-west4-a',
        region: 'europe-west4',
        status: 'degraded',
        isEnabled: true,
      };

      const label = service.formatZoneLabel(zone);
      expect(label).toContain('Degraded');
    });

    it('should handle unknown region', () => {
      const zone: ZoneAvailability = {
        zone: 'unknown-region-a',
        region: 'unknown-region',
        status: 'available',
        isEnabled: true,
      };

      const label = service.formatZoneLabel(zone);
      expect(label).toBe('unknown-region (unknown-region-a)');
    });
  });
});
