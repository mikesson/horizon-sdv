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
import {computed, InjectionToken, inject, Service, signal} from '@angular/core';

import {LoggingService} from '../../../shared/services';
import type {InstanceTarget} from '../../models/instance-target';
import {targetKey} from '../../models/instance-target';
import type {AdbConnectionState} from './adb-session';
import {AdbSession} from './adb-session';
import {adbSocketUrl} from './websocket-adb-connection';

/**
 * Opens a device's ADB transport. A token rather than a bare call so tests can
 * drive the whole stack against a fake daemon without a server.
 */
export const ADB_SOCKET_FACTORY = new InjectionToken<(target: InstanceTarget) => WebSocket>(
  'ADB socket factory',
  {
    providedIn: 'root',
    factory: () => (target: InstanceTarget) => new WebSocket(adbSocketUrl(target)),
  }
);

/**
 * A held reference to a device's ADB session. Release when done; the
 * session is disposed when the last claim is released.
 */
export interface AdbClaim {
  readonly session: AdbSession;
  release(): void;
}

/**
 * Registry of per-INSTANCE {@link AdbSession}s with claim counting.
 *
 * Every interested party (device player page, logcat, tool panels) acquires
 * its own claim, so one consumer going away never tears down a session others
 * still rely on. A session connects as soon as it is claimed and needs no
 * video stream. Shell, file and package operations live in adb-exec/adb-files/
 * adb-packages as plain functions over `session.ensureConnected()`.
 *
 * Keyed by instance rather than device: each Android VM in a CVD group runs
 * its own adbd, so two instances of one device are two sessions and a shell
 * opened on one must never be handed to the other.
 */
@Service()
export class AdbService {
  private readonly logger = inject(LoggingService);
  private readonly openSocket = inject(ADB_SOCKET_FACTORY);

  private readonly sessionsSignal = signal<ReadonlyMap<string, AdbSession>>(new Map());
  private readonly claimCounts = new Map<string, number>();

  /** Reactive session lookup; null while no one holds a claim. */
  session(target: InstanceTarget): Signal<AdbSession | null> {
    const key = targetKey(target);
    return computed(() => this.sessionsSignal().get(key) ?? null);
  }

  /** Reactive connection state; 'disconnected' while no session exists. */
  state(target: InstanceTarget): Signal<AdbConnectionState> {
    const key = targetKey(target);
    return computed(() => this.sessionsSignal().get(key)?.state() ?? 'disconnected');
  }

  acquire(target: InstanceTarget): AdbClaim {
    const key = targetKey(target);
    let session = this.sessionsSignal().get(key);
    if (!session) {
      const created = new AdbSession(target, {
        logger: this.logger,
        openSocket: this.openSocket,
      });
      session = created;
      this.updateSessions(map => map.set(key, created));
    }
    this.claimCounts.set(key, (this.claimCounts.get(key) ?? 0) + 1);

    let released = false;
    return {
      session,
      release: () => {
        if (released) {
          return;
        }
        released = true;
        this.release(key);
      },
    };
  }

  private release(key: string): void {
    const count = (this.claimCounts.get(key) ?? 1) - 1;
    if (count > 0) {
      this.claimCounts.set(key, count);
      return;
    }
    this.claimCounts.delete(key);
    this.sessionsSignal().get(key)?.dispose();
    this.updateSessions(map => map.delete(key));
    this.logger.debug('ADB', `Released last claim for ${key}`);
  }

  private updateSessions(mutate: (map: Map<string, AdbSession>) => void): void {
    const next = new Map(this.sessionsSignal());
    mutate(next);
    this.sessionsSignal.set(next);
  }
}
