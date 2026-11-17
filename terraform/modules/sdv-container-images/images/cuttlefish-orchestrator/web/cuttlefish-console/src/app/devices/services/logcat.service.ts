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

import type {EffectRef, Signal} from '@angular/core';
import {computed, effect, Injector, inject, Service, signal, untracked} from '@angular/core';

import {LoggingService} from '../../shared/services';
import type {InstanceTarget} from '../models/instance-target';
import {targetKey} from '../models/instance-target';
import type {AdbClaim} from './adb/adb.service';
import {AdbService} from './adb/adb.service';
import type {LogBufferId} from './adb/adb-logcat';
import {
  clearLogBuffers,
  DEFAULT_LOG_BUFFERS,
  openLogcat,
  resolveAppPid,
  toLogEntry,
} from './adb/adb-logcat';
import type {LogEntry} from './logcat-model';

/** Log stream state. */
export type LogStreamState = 'waiting_adb' | 'connecting' | 'streaming' | 'paused' | 'error';

const MAX_LOG_ENTRIES = 10000;
/** Entries are batched and flushed on a timer so a log flood costs one
 * signal emission per interval instead of one per line. */
const FLUSH_INTERVAL_MS = 100;

interface LogcatSessionDeps {
  adb: AdbService;
  logger: LoggingService;
  injector: Injector;
}

/**
 * One device's logcat stream: holds its own ADB claim, starts a binary
 * logcat stream when ADB connects, stops when it drops, and exposes
 * structured entries as signals. Pausing drops incoming entries (the
 * stream keeps running); resume continues live. Changing buffers or the
 * app filter restarts the underlying stream.
 */
export class LogcatSession {
  private readonly stateSignal = signal<LogStreamState>('waiting_adb');
  private readonly entriesSignal = signal<readonly LogEntry[]>([]);
  private readonly pausedSignal = signal(false);
  private readonly errorSignal = signal<string | null>(null);
  private readonly buffersSignal = signal<readonly LogBufferId[]>(DEFAULT_LOG_BUFFERS);
  private readonly appFilterSignal = signal<string | null>(null);

  readonly state: Signal<LogStreamState> = this.stateSignal.asReadonly();
  readonly entries: Signal<readonly LogEntry[]> = this.entriesSignal.asReadonly();
  readonly isPaused: Signal<boolean> = this.pausedSignal.asReadonly();
  readonly error: Signal<string | null> = this.errorSignal.asReadonly();
  readonly buffers: Signal<readonly LogBufferId[]> = this.buffersSignal.asReadonly();
  /** Package name the stream is restricted to, if any. */
  readonly appFilter: Signal<string | null> = this.appFilterSignal.asReadonly();

  private readonly adbClaim: AdbClaim;
  private readonly watcher: EffectRef;
  private cancelStream: (() => void) | null = null;
  /** Bumped on stop so a stale read loop's exit is not reported as an error. */
  private generation = 0;
  private pending: LogEntry[] = [];
  private flushTimer: ReturnType<typeof setTimeout> | null = null;
  private nextId = 0;

  constructor(
    readonly target: InstanceTarget,
    private readonly deps: LogcatSessionDeps
  ) {
    this.adbClaim = deps.adb.acquire(target);
    this.watcher = effect(
      () => {
        const adbState = this.adbClaim.session.state();
        untracked(() => {
          const current = this.stateSignal();
          if (adbState === 'connected') {
            if (current === 'waiting_adb' || current === 'error') {
              void this.start();
            }
          } else if (current === 'streaming' || current === 'paused' || current === 'connecting') {
            this.stop();
            this.stateSignal.set('waiting_adb');
          }
        });
      },
      {injector: deps.injector}
    );
  }

  pause(): void {
    if (this.stateSignal() === 'streaming') {
      this.pausedSignal.set(true);
      this.stateSignal.set('paused');
    }
  }

  resume(): void {
    if (this.stateSignal() === 'paused') {
      this.pausedSignal.set(false);
      this.stateSignal.set('streaming');
    }
  }

  clear(): void {
    this.pending = [];
    this.entriesSignal.set([]);
  }

  /** Selects the device-side buffers to stream; restarts a live stream. */
  setBuffers(buffers: readonly LogBufferId[]): void {
    this.buffersSignal.set(buffers);
    this.restartIfActive();
  }

  /**
   * Restricts the stream to one app, or clears the restriction. Throws when
   * the app has no running process (pids are resolved per stream start, so
   * an app restart needs the filter re-applied).
   */
  async setAppFilter(packageName: string | null): Promise<void> {
    if (packageName) {
      const device = await this.adbClaim.session.ensureConnected();
      const pid = await resolveAppPid(device, packageName);
      if (pid === null) {
        throw new Error(`${packageName} is not running`);
      }
    }
    this.appFilterSignal.set(packageName);
    this.restartIfActive();
  }

  /** Clears the selected device-side buffers and the local view. */
  async clearDeviceBuffers(): Promise<void> {
    const device = await this.adbClaim.session.ensureConnected();
    await clearLogBuffers(device, this.buffersSignal());
    this.clear();
  }

  /** Manual retry from the error state. */
  restart(): void {
    this.stop();
    void this.start();
  }

  dispose(): void {
    this.watcher.destroy();
    this.stop();
    this.adbClaim.release();
  }

  private restartIfActive(): void {
    const state = this.stateSignal();
    if (state === 'streaming' || state === 'paused' || state === 'connecting') {
      this.restart();
    }
  }

  private async start(): Promise<void> {
    const generation = ++this.generation;
    this.stateSignal.set('connecting');
    this.errorSignal.set(null);

    try {
      const device = await this.adbClaim.session.ensureConnected();

      let pid: number | undefined;
      const packageName = this.appFilterSignal();
      if (packageName) {
        const resolved = await resolveAppPid(device, packageName);
        if (resolved === null) {
          throw new Error(`${packageName} is not running`);
        }
        pid = resolved;
      }
      if (generation !== this.generation) {
        return;
      }

      const reader = openLogcat(device, {buffers: this.buffersSignal(), pid}).getReader();
      // Cancel failures mean the stream is already gone, so nothing to clean up.
      const cancelReader = (): void => {
        void reader.cancel().catch(() => undefined);
      };
      if (generation !== this.generation) {
        cancelReader();
        return;
      }

      this.cancelStream = cancelReader;
      this.stateSignal.set('streaming');
      this.pausedSignal.set(false);
      void this.read(reader, generation);
      this.deps.logger.debug('Logcat', `Connected for ${targetKey(this.target)}`);
    } catch (error: unknown) {
      if (generation !== this.generation) {
        return;
      }
      this.deps.logger.error('Logcat', 'Failed to connect', error);
      this.stateSignal.set('error');
      this.errorSignal.set(error instanceof Error ? error.message : String(error));
    }
  }

  private async read(
    reader: ReturnType<ReturnType<typeof openLogcat>['getReader']>,
    generation: number
  ): Promise<void> {
    try {
      while (generation === this.generation) {
        const {done, value} = await reader.read();
        if (done) {
          break;
        }

        if (this.pausedSignal()) {
          continue;
        }
        this.pending.push(toLogEntry(value, ++this.nextId));
        this.scheduleFlush();
      }

      reader.releaseLock();
    } catch (error: unknown) {
      if (generation === this.generation) {
        this.deps.logger.error('Logcat', `Stream error for ${targetKey(this.target)}`, error);
        this.stateSignal.set('error');
        this.errorSignal.set('Logcat stream ended unexpectedly');
      }
    }
  }

  private scheduleFlush(): void {
    this.flushTimer ??= setTimeout(() => {
      this.flushTimer = null;
      if (this.pending.length === 0) {
        return;
      }
      const batch = this.pending;
      this.pending = [];
      this.entriesSignal.update(entries => {
        const combined = [...entries, ...batch];
        return combined.length > MAX_LOG_ENTRIES ? combined.slice(-MAX_LOG_ENTRIES) : combined;
      });
    }, FLUSH_INTERVAL_MS);
  }

  private stop(): void {
    this.generation++;
    if (this.flushTimer !== null) {
      clearTimeout(this.flushTimer);
      this.flushTimer = null;
    }
    this.pending = [];

    this.cancelStream?.();
    this.cancelStream = null;
  }
}

/**
 * Registry of per-device logcat sessions.
 *
 * `watch()` creates (or returns) the device's session; `unwatch()` disposes
 * it. Single-consumer by design, since only the logcat viewer drives it.
 */
@Service()
export class LogcatService {
  private readonly logger = inject(LoggingService);
  private readonly adb = inject(AdbService);
  private readonly injector = inject(Injector);

  private readonly sessionsSignal = signal<ReadonlyMap<string, LogcatSession>>(new Map());

  session(target: InstanceTarget): Signal<LogcatSession | null> {
    const key = targetKey(target);
    return computed(() => this.sessionsSignal().get(key) ?? null);
  }

  watch(target: InstanceTarget): LogcatSession {
    const key = targetKey(target);
    const existing = this.sessionsSignal().get(key);
    if (existing) {
      return existing;
    }
    const session = new LogcatSession(target, {
      adb: this.adb,
      logger: this.logger,
      injector: this.injector,
    });
    this.updateSessions(map => map.set(key, session));
    return session;
  }

  unwatch(target: InstanceTarget): void {
    const key = targetKey(target);
    const session = this.sessionsSignal().get(key);
    if (session) {
      session.dispose();
      this.updateSessions(map => map.delete(key));
    }
  }

  private updateSessions(mutate: (map: Map<string, LogcatSession>) => void): void {
    const next = new Map(this.sessionsSignal());
    mutate(next);
    this.sessionsSignal.set(next);
  }
}
