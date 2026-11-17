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

import {Injector, inject, Service} from '@angular/core';

import {browserLocation, resolveBasePath} from '../../../shared/utils';
import {waitForSignal} from '../../../shared/utils/signals';
import type {InstanceTarget} from '../../models/instance-target';
import {AdbService} from './adb.service';
import {DeviceInstallService} from './device-install.service';
import {buildInstallPlan} from './install-plan';

const ADB_WAIT_TIMEOUT_MS = 30_000;

/**
 * Installs a build artifact (an APK in GCS) onto a device by streaming it
 * through the browser, so the bytes never touch the user's desktop.
 *
 * Installing needs only ADB, which reaches the device over its own socket, so
 * this no longer brings a video stream up to carry the bytes.
 */
@Service()
export class ArtifactInstallService {
  private readonly adb = inject(AdbService);
  private readonly installService = inject(DeviceInstallService);
  private readonly injector = inject(Injector);

  async installArtifact(target: InstanceTarget, bucket: string, objectPath: string): Promise<void> {
    const file = await this.fetchArtifact(bucket, objectPath);

    // Hold the session for the duration so a concurrent release cannot tear
    // the connection down mid-install.
    const claim = this.adb.acquire(target);

    try {
      await this.waitForAdbReady(target);
      const plan = await buildInstallPlan([file]);
      if (plan.units.length === 0) {
        throw new Error(plan.warnings[0] ?? 'The artifact is not an installable APK');
      }
      this.installService.install(target, plan);
      await this.waitForInstallToSettle(target);
    } finally {
      claim.release();
    }
  }

  private async fetchArtifact(bucket: string, objectPath: string): Promise<File> {
    const url = resolveBasePath(
      `/v2/storage/buckets/${bucket}/objects/${objectPath
        .split('/')
        .map(encodeURIComponent)
        .join('/')}`
    );
    const response = await fetch(url, {
      credentials: 'same-origin',
      headers: {'X-Requested-With': 'XMLHttpRequest'},
    });
    if (response.status === 401) {
      this.redirectForReauth();
      throw new Error('Failed to download artifact (401)');
    }
    if (!response.ok) {
      throw new Error(`Failed to download artifact (${response.status})`);
    }
    const blob = await response.blob();
    const name = objectPath.split('/').pop() ?? 'artifact.apk';
    return new File([blob], name, {type: 'application/vnd.android.package-archive'});
  }

  private redirectForReauth(): void {
    const reauthTsKey = 'cco_reauth_ts';
    const cooldownMs = 15_000;
    try {
      const now = Date.now();
      const raw =
        typeof sessionStorage !== 'undefined' ? sessionStorage.getItem(reauthTsKey) : null;
      const last = raw ? Number(raw) : 0;
      if (now - last < cooldownMs) {
        return;
      }
      if (typeof sessionStorage !== 'undefined') {
        sessionStorage.setItem(reauthTsKey, String(now));
      }
    } catch {
      // sessionStorage may be unavailable in restricted contexts.
    }
    const target =
      typeof window !== 'undefined'
        ? `${window.location.pathname}${window.location.search}${window.location.hash}`
        : '/';
    browserLocation.assign(`${resolveBasePath('/oauth2/start')}?rd=${encodeURIComponent(target)}`);
  }

  private async waitForAdbReady(target: InstanceTarget): Promise<void> {
    await waitForSignal(
      () => {
        const state = this.adb.state(target)();
        if (state === 'error') {
          throw new Error(this.adb.session(target)()?.error()?.message ?? 'ADB failed to connect');
        }
        return state === 'connected' ? true : undefined;
      },
      {
        injector: this.injector,
        timeoutMs: ADB_WAIT_TIMEOUT_MS,
        timeoutMessage: 'Timed out waiting for the device to be ready',
      }
    );
  }

  private async waitForInstallToSettle(target: InstanceTarget): Promise<void> {
    // The install always ends in done, error, or dismissal (state removed).
    await waitForSignal(
      () => {
        const phase = this.installService.state(target)()?.phase;
        return phase === 'done' || phase === 'error' || phase === undefined ? true : undefined;
      },
      {injector: this.injector}
    );
  }
}
