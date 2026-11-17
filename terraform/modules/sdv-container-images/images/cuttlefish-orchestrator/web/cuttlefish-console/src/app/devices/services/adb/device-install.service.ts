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

import type {Signal} from '@angular/core';
import {computed, inject, Service, signal} from '@angular/core';
import {LoggingService} from '../../../shared/services';
import type {InstanceTarget} from '../../models/instance-target';
import {targetKey} from '../../models/instance-target';
import {AdbService} from './adb.service';
import type {InstallProgress} from './adb-install';
import {installApks} from './adb-install';
import {uninstallApp} from './adb-packages';
import type {ClassifiedInstallError} from './install-errors';
import {classifyInstallError} from './install-errors';
import type {InstallPlan, InstallUnit} from './install-plan';
import {readDeviceProfile, selectSplitsForDevice} from './install-split-select';

type InstallPhase = 'preparing' | 'uploading' | 'committing' | 'done' | 'error';

export interface InstallState {
  phase: InstallPhase;
  /** The units being installed (from the plan). */
  units: InstallUnit[];
  progress: InstallProgress | null;
  /** Upload throughput in bytes/sec (moving average). */
  bytesPerSecond: number;
  /** Estimated seconds remaining, or null when unknown. */
  etaSeconds: number | null;
  error: ClassifiedInstallError | null;
  /** Installed package name on success, for a Launch action. */
  installedPackage: string | null;
}

interface RunOptions {
  downgrade?: boolean;
  allowTest?: boolean;
  /** Ignore device-aware split filtering; install every provided file. */
  allSplits?: boolean;
}

interface ActiveInstall {
  state: InstallState;
  controller: AbortController;
  /** Everything needed to re-run for a remediation. */
  plan: InstallPlan;
  options: RunOptions;
}

/**
 * Owns APK installation per instance so progress survives navigation away from
 * the app manager. One install at a time per instance; consumers read state as
 * a signal and can cancel or apply a remediation.
 *
 * Keyed by instance, not device: an APK is pushed into one Android VM's package
 * manager, so a phone and a watch in one group install independently.
 */
@Service()
export class DeviceInstallService {
  private readonly adb = inject(AdbService);
  private readonly logger = inject(LoggingService);

  private readonly installsSignal = signal<ReadonlyMap<string, ActiveInstall>>(new Map());

  state(target: InstanceTarget): Signal<InstallState | null> {
    const key = targetKey(target);
    return computed(() => this.installsSignal().get(key)?.state ?? null);
  }

  isInstalling(target: InstanceTarget): Signal<boolean> {
    const key = targetKey(target);
    return computed(() => {
      const phase = this.installsSignal().get(key)?.state.phase;
      return phase === 'preparing' || phase === 'uploading' || phase === 'committing';
    });
  }

  /** Starts (or replaces) an install for one instance. */
  install(target: InstanceTarget, plan: InstallPlan, options: RunOptions = {}): void {
    if (plan.units.length === 0) {
      return;
    }
    const key = targetKey(target);
    this.installsSignal().get(key)?.controller.abort();

    const controller = new AbortController();
    const active: ActiveInstall = {
      controller,
      plan,
      options,
      state: {
        phase: 'preparing',
        units: plan.units,
        progress: null,
        bytesPerSecond: 0,
        etaSeconds: null,
        error: null,
        installedPackage: null,
      },
    };
    this.put(key, active);
    void this.run(target, active);
  }

  cancel(target: InstanceTarget): void {
    this.installsSignal().get(targetKey(target))?.controller.abort();
  }

  dismiss(target: InstanceTarget): void {
    const key = targetKey(target);
    this.installsSignal().get(key)?.controller.abort();
    this.remove(key);
  }

  /** Retries the last install with a remediation applied. */
  applyRemedy(target: InstanceTarget): void {
    const active = this.installsSignal().get(targetKey(target));
    const remedy = active?.state.error?.remedy;
    if (!(active && remedy)) {
      return;
    }

    switch (remedy.kind) {
      case 'retry-downgrade':
        this.install(target, active.plan, {...active.options, downgrade: true});
        break;
      case 'retry-test':
        this.install(target, active.plan, {...active.options, allowTest: true});
        break;
      case 'retry-all-splits':
        this.install(target, active.plan, {...active.options, allSplits: true});
        break;
      case 'uninstall-retry':
        void this.uninstallThenRetry(target, active);
        break;
      default:
        break;
    }
  }

  private async uninstallThenRetry(target: InstanceTarget, active: ActiveInstall): Promise<void> {
    const packageName = active.plan.units.find(u => u.packageName)?.packageName;
    if (!packageName) {
      return;
    }
    try {
      const session = this.adb.session(target)();
      if (session) {
        await uninstallApp(await session.ensureConnected(), packageName);
      }
    } catch (error) {
      this.logger.warn('Install', `Uninstall before retry failed for ${packageName}`, error);
    }
    this.install(target, active.plan, active.options);
  }

  private async run(target: InstanceTarget, active: ActiveInstall): Promise<void> {
    const key = targetKey(target);
    const claim = this.adb.acquire(target);
    const samples: Array<{t: number; bytes: number}> = [];

    try {
      const device = await claim.session.ensureConnected();

      for (const unit of active.plan.units) {
        const files = active.options.allSplits
          ? unit.files
          : selectSplitsForDevice(unit.files, await readDeviceProfile(device));

        this.update(key, {phase: 'uploading'});
        await installApks(device, files, {
          downgrade: active.options.downgrade,
          allowTest: active.options.allowTest,
          signal: active.controller.signal,
          onProgress: progress => {
            this.update(key, {
              phase: progress.committing ? 'committing' : 'uploading',
              progress,
              ...this.rate(samples, progress),
            });
          },
        });
      }

      const installedPackage =
        active.plan.units.length === 1 ? active.plan.units[0].packageName : null;
      this.update(key, {phase: 'done', installedPackage, progress: null, etaSeconds: 0});
    } catch (error) {
      if (active.controller.signal.aborted) {
        this.remove(key);
        return;
      }
      this.logger.error('Install', `Install failed for ${key}`, error);
      this.update(key, {phase: 'error', error: classifyInstallError(error)});
    } finally {
      claim.release();
    }
  }

  /** Moving-average throughput + ETA over the last ~2s of samples. */
  private rate(
    samples: Array<{t: number; bytes: number}>,
    progress: InstallProgress
  ): {bytesPerSecond: number; etaSeconds: number | null} {
    const now = performance.now();
    samples.push({t: now, bytes: progress.sentBytes});
    while (samples.length > 2 && now - samples[0].t > 2000) {
      samples.shift();
    }
    const first = samples[0];
    const elapsed = (now - first.t) / 1000;
    if (elapsed <= 0) {
      return {bytesPerSecond: 0, etaSeconds: null};
    }
    const bytesPerSecond = (progress.sentBytes - first.bytes) / elapsed;
    const remaining = progress.totalBytes - progress.sentBytes;
    return {
      bytesPerSecond,
      etaSeconds: bytesPerSecond > 0 ? Math.ceil(remaining / bytesPerSecond) : null,
    };
  }

  private update(key: string, patch: Partial<InstallState>): void {
    const active = this.installsSignal().get(key);
    if (!active) {
      return;
    }
    this.put(key, {...active, state: {...active.state, ...patch}});
  }

  private put(key: string, active: ActiveInstall): void {
    const next = new Map(this.installsSignal());
    next.set(key, active);
    this.installsSignal.set(next);
  }

  private remove(key: string): void {
    const next = new Map(this.installsSignal());
    next.delete(key);
    this.installsSignal.set(next);
  }
}
