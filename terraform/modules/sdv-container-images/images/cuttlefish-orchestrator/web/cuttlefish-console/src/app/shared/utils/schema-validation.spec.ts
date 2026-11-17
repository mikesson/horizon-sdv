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
 * Schema Validation Tests
 *
 * These tests validate that our Zod schemas correctly parse API responses
 * from the Huma backend. The sample data matches the Go struct JSON serialization.
 *
 * If these tests fail, it indicates schema drift between frontend and backend.
 */

import {describe, expect, it} from 'vitest';
import {UserSchema} from '../../auth/models/user.schema';
import {BuildSchema} from '../../builds/models/build.schema';
import {DeviceListResponseSchema, DeviceSchema} from '../../devices/models/device.schema';
import {SnapshotListSchema, SnapshotSchema} from '../../devices/models/snapshot.schema';
import {HostListResponseSchema, HostSchema} from '../../hosts/models/host.schema';
import {
  ListObjectsResponseSchema,
  VerifyAccessResponseSchema,
} from '../../storage/models/storage.schema';

describe('DeviceSchema - Huma API Response Validation', () => {
  /**
   * Sample device response matching Go struct serialization.
   * Field names use snake_case as defined in internal/domain/device/device.go
   */
  const sampleDeviceResponse = {
    id: 'dev-12345678-1234-1234-1234-123456789012',
    host_id: 'host-87654321-4321-4321-4321-210987654321',
    name: 'test-device-01',
    cvd_id: 'cvd/1',
    status: 'running',
    build_id: 'P12345678',
    build_target: 'aosp_cf_x86_64_phone-userdebug',
    gpu_mode: 'gfxstream',
    adb_port: 6520,
    webrtc_port: 8443,
    created_by: 'user@example.com',
    created_at: '2025-12-18T10:00:00Z',
    updated_at: '2025-12-18T10:30:00Z',
    started_at: '2025-12-18T10:05:00Z',
    health_status: 'healthy',
    lifecycle: {
      mode: 'ephemeral',
      ttl_seconds: 14400,
      expires_at: '2025-12-18T14:00:00Z',
    },
  };

  it('should parse a complete device response from Huma backend', () => {
    const result = DeviceSchema.parse(sampleDeviceResponse);

    expect(result.id).toBe('dev-12345678-1234-1234-1234-123456789012');
    expect(result.hostId).toBe('host-87654321-4321-4321-4321-210987654321');
    expect(result.name).toBe('test-device-01');
    expect(result.status).toBe('running');
    expect(result.gpuMode).toBe('gfxstream');
    expect(result.adbPort).toBe(6520);
    expect(result.createdAt).toBeInstanceOf(Date);
    expect(result.startedAt).toBeInstanceOf(Date);
  });

  it('should parse minimal device response', () => {
    const minimalResponse = {
      id: 'dev-minimal',
      name: 'minimal-device',
      status: 'pending',
    };

    const result = DeviceSchema.parse(minimalResponse);

    expect(result.id).toBe('dev-minimal');
    expect(result.name).toBe('minimal-device');
    expect(result.status).toBe('pending');
    expect(result.gpuMode).toBe('auto'); // Platform default
  });

  it('should parse device list response', () => {
    const listResponse = {
      items: [sampleDeviceResponse],
      total: 1,
      limit: 20,
      offset: 0,
    };

    const result = DeviceListResponseSchema.parse(listResponse);

    expect(result.items).toHaveLength(1);
    expect(result.items[0].name).toBe('test-device-01');
    expect(result.total).toBe(1);
  });
});

describe('HostSchema - Huma API Response Validation', () => {
  /**
   * Sample host response matching Go struct serialization.
   * Field names use snake_case as defined in internal/domain/host/host.go
   */
  const sampleHostResponse = {
    id: 'host-12345678-1234-1234-1234-123456789012',
    name: 'host-us-central1-a-001',
    zone: 'us-central1-a',
    gce_instance_name: 'cf-host-us-central1-a-001-a1b2c3d4',
    machine_type: 'n1-standard-8',
    status: 'running',
    gpu_type: 'nvidia-tesla-t4',
    gpu_count: 1,
    min_cpu_platform: 'Intel Cascade Lake',
    boot_disk_size_gb: 750,
    max_devices: 4,
    current_device_count: 2,
    ip_address: '10.128.0.50',
    internal_ip: '10.128.0.50',
    created_by: 'admin@example.com',
    created_at: '2025-12-01T00:00:00Z',
    updated_at: '2025-12-18T12:00:00Z',
    lifecycle: {
      mode: 'ephemeral',
      ttl_seconds: 36000,
      expires_at: '2025-12-19T00:00:00Z',
    },
  };

  it('should parse a complete host response from Huma backend', () => {
    const result = HostSchema.parse(sampleHostResponse);

    expect(result.id).toBe('host-12345678-1234-1234-1234-123456789012');
    expect(result.name).toBe('host-us-central1-a-001');
    expect(result.zone).toBe('us-central1-a');
    expect(result.status).toBe('running');
    expect(result.gpuType).toBe('nvidia-tesla-t4');
    // The transform must carry these; an omitted field is silently dropped.
    expect(result.minCpuPlatform).toBe('Intel Cascade Lake');
    expect(result.bootDiskSizeGb).toBe(750);
    expect(result.maxDevices).toBe(4);
    expect(result.currentDevices).toBe(2);
    expect(result.createdAt).toBeInstanceOf(Date);
  });

  it('should parse minimal host response', () => {
    const minimalResponse = {
      id: 'host-minimal',
      name: 'minimal-host',
      zone: 'us-central1-a',
      status: 'pending',
    };

    const result = HostSchema.parse(minimalResponse);

    expect(result.id).toBe('host-minimal');
    expect(result.zone).toBe('us-central1-a');
    // Absent shape fields stay undefined (server default), never 0 or ''.
    expect(result.minCpuPlatform).toBeUndefined();
    expect(result.bootDiskSizeGb).toBeUndefined();
  });

  it('should parse host list response', () => {
    const listResponse = {
      items: [sampleHostResponse],
      total: 1,
      limit: 20,
      offset: 0,
    };

    const result = HostListResponseSchema.parse(listResponse);

    expect(result.items).toHaveLength(1);
    expect(result.items[0].name).toBe('host-us-central1-a-001');
  });
});

describe('SnapshotSchema - Huma API Response Validation', () => {
  const sampleSnapshotResponse = {
    id: 'a1b2c3d4-1234-1234-1234-123456789012',
    device_id: 'device-12345678-1234-1234-1234-123456789012',
    host_id: 'host-12345678-1234-1234-1234-123456789012',
    state: 'ready',
    created_by: 'user@example.com',
    created_at: '2026-07-30T10:00:00Z',
    updated_at: '2026-07-30T10:05:00Z',
  };

  it('should parse a complete snapshot response from Huma backend', () => {
    const result = SnapshotSchema.parse(sampleSnapshotResponse);

    expect(result.id).toBe('a1b2c3d4-1234-1234-1234-123456789012');
    expect(result.deviceId).toBe('device-12345678-1234-1234-1234-123456789012');
    expect(result.hostId).toBe('host-12345678-1234-1234-1234-123456789012');
    expect(result.state).toBe('ready');
    expect(result.createdBy).toBe('user@example.com');
    expect(result.createdAt).toBeInstanceOf(Date);
    expect(result.updatedAt).toBeInstanceOf(Date);
  });

  it('should parse a snapshot without created_by', () => {
    const {created_by: _dropped, ...anonymous} = sampleSnapshotResponse;
    const result = SnapshotSchema.parse(anonymous);
    expect(result.createdBy).toBeUndefined();
  });

  it('should reject a state outside the published vocabulary', () => {
    const result = SnapshotSchema.safeParse({...sampleSnapshotResponse, state: 'archived'});
    expect(result.success).toBe(false);
  });

  it('should unwrap the snapshot list envelope', () => {
    const result = SnapshotListSchema.parse({snapshots: [sampleSnapshotResponse]});
    expect(result).toHaveLength(1);
    expect(result[0].state).toBe('ready');
  });

  it('should parse an empty inventory to an empty array', () => {
    expect(SnapshotListSchema.parse({snapshots: []})).toEqual([]);
  });
});

describe('BuildSchema - Huma API Response Validation', () => {
  /**
   * Sample build response matching Go struct serialization.
   */
  const sampleBuildResponse = {
    id: 'build-12345678-1234-1234-1234-123456789012',
    name: 'aosp_cf_x86_64_phone-userdebug-P12345678',
    source: 'android_build',
    build_id: 'P12345678',
    target: 'aosp_cf_x86_64_phone-userdebug',
    branch: 'aosp-main',
    flavor: 'userdebug',
    fingerprint: 'google/aosp_cf_x86_64_phone/vsoc_x86_64:14/main/P12345678:userdebug/dev-keys',
    created_at: '2025-12-15T00:00:00Z',
    updated_at: '2025-12-15T00:00:00Z',
  };

  it('should parse a complete build response from Huma backend', () => {
    const result = BuildSchema.parse(sampleBuildResponse);

    expect(result.id).toBe('build-12345678-1234-1234-1234-123456789012');
    expect(result.name).toBe('aosp_cf_x86_64_phone-userdebug-P12345678');
    expect(result.source).toBe('android_build');
    expect(result.buildId).toBe('P12345678');
    expect(result.target).toBe('aosp_cf_x86_64_phone-userdebug');
    expect(result.fingerprint).toContain('P12345678');
  });

  it('should parse minimal build response', () => {
    const minimalResponse = {
      id: 'build-minimal',
      name: 'minimal-build',
      source: 'url',
    };

    const result = BuildSchema.parse(minimalResponse);

    expect(result.id).toBe('build-minimal');
    expect(result.name).toBe('minimal-build');
  });
});

describe('Storage schemas - Huma API Response Validation', () => {
  it('should parse an object listing with snake_case fields', () => {
    const response = {
      objects: [
        {
          name: 'builds/aosp/',
          display_name: 'aosp',
          bucket: 'my-artifacts',
          is_folder: true,
          gs_uri: 'gs://my-artifacts/builds/aosp/',
          public_url: 'https://storage.googleapis.com/my-artifacts/builds/aosp/',
        },
        {
          name: 'builds/cvd-host_package.tar.gz',
          display_name: 'cvd-host_package.tar.gz',
          bucket: 'my-artifacts',
          size: 123456,
          content_type: 'application/gzip',
          is_folder: false,
          updated_at: '2026-07-01T00:00:00Z',
          gs_uri: 'gs://my-artifacts/builds/cvd-host_package.tar.gz',
          public_url: 'https://storage.googleapis.com/my-artifacts/builds/cvd-host_package.tar.gz',
        },
      ],
      prefixes: ['builds/aosp/'],
      next_page_token: 'token-1',
    };

    const result = ListObjectsResponseSchema.parse(response);

    expect(result.objects).toHaveLength(2);
    expect(result.objects[0].isFolder).toBe(true);
    expect(result.objects[0].displayName).toBe('aosp');
    expect(result.objects[1].isFolder).toBe(false);
    expect(result.objects[1].gsUri).toBe('gs://my-artifacts/builds/cvd-host_package.tar.gz');
    expect(result.objects[1].updatedAt).toBeInstanceOf(Date);
    expect(result.nextPageToken).toBe('token-1');
  });

  it('should default a null objects array and synthesize gsUri', () => {
    expect(ListObjectsResponseSchema.parse({objects: null}).objects).toEqual([]);

    const result = ListObjectsResponseSchema.parse({
      objects: [{name: 'f.txt', bucket: 'b'}],
    });
    expect(result.objects[0].displayName).toBe('f.txt');
    expect(result.objects[0].gsUri).toBe('gs://b/f.txt');
  });

  it('should parse a verify-access response with snake_case fields', () => {
    const response = {
      gs_uri: 'gs://my-artifacts/builds',
      orchestrator_access: {
        service_account: 'orchestrator@project.iam.gserviceaccount.com',
        has_access: true,
        permissions: ['storage.objects.get'],
        checked_at: '2026-07-06T00:00:00Z',
      },
      host_access: {
        service_account: 'host@project.iam.gserviceaccount.com',
        has_access: false,
        missing_perms: ['storage.objects.get'],
        error: 'permission denied',
        checked_at: '2026-07-06T00:00:00Z',
      },
      can_proceed: true,
      message: 'orchestrator ok, host missing perms',
    };

    const result = VerifyAccessResponseSchema.parse(response);

    expect(result.orchestratorAccess.hasAccess).toBe(true);
    expect(result.orchestratorAccess.serviceAccount).toBe(
      'orchestrator@project.iam.gserviceaccount.com'
    );
    expect(result.hostAccess?.hasAccess).toBe(false);
    expect(result.hostAccess?.missingPerms).toEqual(['storage.objects.get']);
    expect(result.canProceed).toBe(true);
  });
});

describe('UserSchema - Huma API Response Validation', () => {
  /**
   * Sample user response matching the /v2/me handler. With IAP as the only
   * gate the identity is just the verified email plus a display name.
   */
  const sampleUserResponse = {
    email: 'developer@example.com',
    display_name: 'Test Developer',
  };

  it('should parse a complete user response from the backend', () => {
    const result = UserSchema.parse(sampleUserResponse);

    expect(result.email).toBe('developer@example.com');
    expect(result.displayName).toBe('Test Developer');
  });

  it('should parse minimal user response with required fields', () => {
    const minimalResponse = {
      email: 'minimal@example.com',
    };

    const result = UserSchema.parse(minimalResponse);

    expect(result.email).toBe('minimal@example.com');
    expect(result.displayName).toBeUndefined();
  });

  it('should require email field', () => {
    expect(() => UserSchema.parse({display_name: 'No Email'})).toThrow();
  });

  it('should reject an invalid email', () => {
    expect(() => UserSchema.parse({email: 'not-an-email'})).toThrow();
  });
});
