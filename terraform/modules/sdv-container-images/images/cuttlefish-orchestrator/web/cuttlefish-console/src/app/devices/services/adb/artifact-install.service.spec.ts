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

import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {browserLocation} from '../../../shared/utils';
import {AdbService} from './adb.service';
import {ArtifactInstallService} from './artifact-install.service';
import {DeviceInstallService} from './device-install.service';

describe('ArtifactInstallService', () => {
  let baseEl: HTMLBaseElement | null = null;
  let assignSpy: ReturnType<typeof vi.spyOn>;
  let fetchSpy: ReturnType<typeof vi.spyOn>;

  const mountBase = (href: string): void => {
    baseEl?.remove();
    baseEl = document.createElement('base');
    baseEl.setAttribute('href', href);
    document.head.appendChild(baseEl);
  };

  beforeEach(() => {
    sessionStorage.clear();
    assignSpy = vi.spyOn(browserLocation, 'assign').mockReturnValue(undefined);
    fetchSpy = vi.spyOn(globalThis, 'fetch');
  });

  afterEach(() => {
    baseEl?.remove();
    baseEl = null;
    sessionStorage.clear();
    vi.restoreAllMocks();
  });

  it('sends X-Requested-With and triggers subpath reauth on 401', async () => {
    mountBase('/cuttlefish-orchestrator/');
    const connectSpy = vi.fn();
    const installSpy = vi.fn();

    TestBed.configureTestingModule({
      providers: [
        ArtifactInstallService,
        {provide: AdbService, useValue: {connect: connectSpy}},
        {provide: DeviceInstallService, useValue: {installFiles: installSpy}},
      ],
    });
    const service = TestBed.inject(ArtifactInstallService);

    fetchSpy.mockResolvedValue(new Response(null, {status: 401}));

    await expect(
      service.installArtifact({deviceId: 'd1', instance: 'cvd-1'}, 'cco-bucket', 'dir/app.apk')
    ).rejects.toThrow(/401/);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0];
    expect(url).toBe('/cuttlefish-orchestrator/v2/storage/buckets/cco-bucket/objects/dir/app.apk');
    expect(init).toMatchObject({
      credentials: 'same-origin',
      headers: {'X-Requested-With': 'XMLHttpRequest'},
    });
    expect(assignSpy).toHaveBeenCalledTimes(1);
    expect(String(assignSpy.mock.calls[0][0])).toMatch(
      /^\/cuttlefish-orchestrator\/oauth2\/start\?rd=/
    );
    expect(connectSpy).not.toHaveBeenCalled();

    // Subsequent 401 within cooldown must not trigger a duplicate redirect.
    await expect(
      service.installArtifact({deviceId: 'd1', instance: 'cvd-1'}, 'cco-bucket', 'dir/app.apk')
    ).rejects.toThrow(/401/);
    expect(assignSpy).toHaveBeenCalledTimes(1);
  });
});
