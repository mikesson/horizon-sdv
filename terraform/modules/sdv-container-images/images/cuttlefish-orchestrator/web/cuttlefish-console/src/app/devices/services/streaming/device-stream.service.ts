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

import {HttpClient} from '@angular/common/http';
import type {Signal} from '@angular/core';
import {computed, inject, Service, signal} from '@angular/core';
import {LoggingService} from '../../../shared/services';
import type {InstanceTarget} from '../../models/instance-target';
import {targetKey} from '../../models/instance-target';
import {DeviceApiService} from '../device-api.service';
import {DeviceInputService} from '../input/device-input.service';
import type {DeviceStreamSessionDeps} from './device-stream-session';
import {DeviceStreamSession} from './device-stream-session';

/**
 * Registry of live device streaming sessions.
 *
 * Each connected device gets one {@link DeviceStreamSession} owning its
 * WebRTC machinery; this service only creates, hands out, and disposes them.
 * A failed session stays registered (so the UI can show the error and offer
 * a retry); calling connect() again replaces it.
 */
@Service()
export class DeviceStreamService {
  private readonly deps: DeviceStreamSessionDeps = {
    api: inject(DeviceApiService),
    http: inject(HttpClient),
    input: inject(DeviceInputService),
    logger: inject(LoggingService),
  };

  private readonly sessionsSignal = signal<ReadonlyMap<string, DeviceStreamSession>>(new Map());

  /** Reactive handle for one instance's session; null while not streaming. */
  session(target: InstanceTarget): Signal<DeviceStreamSession | null> {
    const key = targetKey(target);
    return computed(() => this.sessionsSignal().get(key) ?? null);
  }

  /**
   * The device's streaming instances, oldest session first.
   *
   * Insertion order is part of the contract. A caller capping how many
   * sessions it holds needs to know which one to drop, and the first key is
   * the one connected longest ago. Device ids are UUIDs and instance names
   * are gated to letters, digits, underscores and hyphens, so the '/' the key
   * joins on appears exactly once.
   */
  liveInstances(deviceId: string): Signal<readonly string[]> {
    const prefix = `${deviceId}/`;
    return computed(() =>
      [...this.sessionsSignal().keys()]
        .filter(key => key.startsWith(prefix))
        .map(key => key.slice(prefix.length))
    );
  }

  /**
   * Starts (or restarts, after failure) streaming for one instance.
   * No-op when a session is already live.
   *
   * Keyed by instance: each Android VM registers its own operator device id, so
   * a group's members stream independently and one session must never be handed
   * to another instance's video element.
   */
  connect(target: InstanceTarget): void {
    const key = targetKey(target);
    const existing = this.sessionsSignal().get(key);
    if (existing) {
      if (existing.state() !== 'failed') {
        return;
      }
      existing.dispose();
    }
    const session = new DeviceStreamSession(target, this.deps);
    this.sessionsSignal.update(sessions => new Map(sessions).set(key, session));
  }

  /** Stops streaming and forgets the session. */
  disconnect(target: InstanceTarget): void {
    const key = targetKey(target);
    const session = this.sessionsSignal().get(key);
    if (!session) {
      return;
    }
    session.dispose();
    this.sessionsSignal.update(sessions => {
      const next = new Map(sessions);
      next.delete(key);
      return next;
    });
  }
}
