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
import {signal} from '@angular/core';
import {Adb, AdbDaemonTransport} from '@yume-chan/adb';

import type {LoggingService} from '../../../shared/services';
import type {InstanceTarget} from '../../models/instance-target';
import {targetKey} from '../../models/instance-target';
import {adbSocketUrl, WebSocketAdbConnection} from './websocket-adb-connection';

export type AdbConnectionState = 'disconnected' | 'connecting' | 'connected' | 'error';

export interface AdbSessionTimings {
  /** Abort authentication if the device does not answer in time. */
  connectTimeoutMs: number;
  /** Probe cadence while connected; catches a silently dead proxy. */
  heartbeatIntervalMs: number;
  heartbeatTimeoutMs: number;
  /** First delay before a reconnect attempt; doubles up to the cap. */
  reconnectDelayMs: number;
  /** Upper bound on the backoff between reconnect attempts. */
  maxReconnectDelayMs: number;
  /** Attempts before the session settles in `error`. */
  maxReconnectAttempts: number;
}

const DEFAULT_TIMINGS: AdbSessionTimings = {
  connectTimeoutMs: 10_000,
  heartbeatIntervalMs: 15_000,
  heartbeatTimeoutMs: 5_000,
  reconnectDelayMs: 1_000,
  maxReconnectDelayMs: 15_000,
  // A guest reboot takes adbd away for the better part of a minute; the
  // backoff sequence spans that without hammering a device that is simply
  // gone.
  maxReconnectAttempts: 6,
};

export interface AdbSessionDeps {
  logger: LoggingService;
  timings?: AdbSessionTimings;
  /** Opens the transport; injectable so tests need no real socket. */
  openSocket?: (target: InstanceTarget) => WebSocket;
}

function withTimeout<T>(promise: Promise<T>, ms: number, message: string): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(message)), ms);
    promise.then(
      value => {
        clearTimeout(timer);
        resolve(value);
      },
      (error: unknown) => {
        clearTimeout(timer);
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    );
  });
}

/**
 * No-op credential store for Cuttlefish.
 *
 * The host's ADB proxy handles authentication transparently: the device
 * replies CNXN directly and never sends AUTH, so no RSA keys are needed.
 */
const CUTTLEFISH_CREDENTIAL_STORE = {
  async *iterateKeys(): AsyncGenerator<never> {
    // No stored keys - the Cuttlefish ADB proxy handles auth
  },
  generateKey(): Promise<never> {
    return Promise.reject(new Error('Key generation not needed for Cuttlefish'));
  },
};

/**
 * One device's ADB connection, over the orchestrator's ADB WebSocket.
 *
 * The session owns its transport outright and is not bound to the device's
 * video stream, so ADB works whether or not anyone is watching the screen, and
 * ADB traffic never crosses a media relay. It connects as soon as it is
 * claimed and reconnects with backoff when the transport dies, which is also
 * how it recovers from a guest reboot taking adbd away.
 *
 * Sessions are created and disposed by {@link AdbService} claim counting.
 * Never construct one directly in a component.
 */
export class AdbSession {
  private readonly stateSignal = signal<AdbConnectionState>('disconnected');
  private readonly errorSignal = signal<Error | null>(null);

  readonly state: Signal<AdbConnectionState> = this.stateSignal.asReadonly();
  readonly error: Signal<Error | null> = this.errorSignal.asReadonly();

  private device: Adb | null = null;
  private connection: WebSocketAdbConnection | null = null;
  private connecting: Promise<Adb> | null = null;
  /** Bumped on invalidate/dispose so stale in-flight connects are discarded. */
  private generation = 0;
  private heartbeatTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempts = 0;
  private disposed = false;
  private readonly timings: AdbSessionTimings;
  private readonly openSocket: (target: InstanceTarget) => WebSocket;

  constructor(
    readonly target: InstanceTarget,
    private readonly deps: AdbSessionDeps
  ) {
    this.timings = deps.timings ?? DEFAULT_TIMINGS;
    this.openSocket = deps.openSocket ?? (t => new WebSocket(adbSocketUrl(t)));
    // A claim is the intent to use ADB; nothing else has to ask.
    void this.ensureConnected().catch((error: unknown) => {
      this.deps.logger.debug('ADB', `Initial connect failed for ${targetKey(target)}`, error);
    });
  }

  /**
   * Returns the connected `Adb` device, connecting first if needed.
   *
   * Compose with the plain functions in adb-exec/adb-files/adb-packages:
   * `await exec(await session.ensureConnected(), 'ls')`.
   */
  ensureConnected(): Promise<Adb> {
    if (this.device && this.stateSignal() === 'connected') {
      return Promise.resolve(this.device);
    }
    this.connecting ??= this.connect().finally(() => {
      this.connecting = null;
    });
    return this.connecting;
  }

  private async connect(): Promise<Adb> {
    const generation = this.generation;
    this.stateSignal.set('connecting');
    this.errorSignal.set(null);

    try {
      const connection = new WebSocketAdbConnection(this.openSocket(this.target));
      // Held so invalidate() can close it; connects are serialized by
      // `connecting`, so this never clobbers a live one.
      this.connection = connection;

      // A dead proxy leaves the socket open but silent. Without a deadline
      // the handshake would hang in 'connecting' forever.
      const transport = await withTimeout(
        (async () => {
          await connection.ready;
          return AdbDaemonTransport.authenticate({
            serial: targetKey(this.target),
            connection,
            credentialStore: CUTTLEFISH_CREDENTIAL_STORE,
          });
        })(),
        this.timings.connectTimeoutMs,
        'ADB did not respond; the device may still be booting'
      );
      const device = new Adb(transport);

      if (generation !== this.generation) {
        throw new Error('ADB connection was invalidated during setup');
      }

      this.device = device;
      this.reconnectAttempts = 0;
      this.stateSignal.set('connected');
      this.observeDisconnect(device, generation);
      this.scheduleHeartbeat(device, generation);
      this.deps.logger.debug('ADB', `Connected to ${targetKey(this.target)}`, {
        features: transport.banner.features,
      });
      return device;
    } catch (error: unknown) {
      const err = error instanceof Error ? error : new Error(String(error));
      if (generation === this.generation) {
        this.connection?.dispose();
        this.connection = null;
        this.stateSignal.set('error');
        this.errorSignal.set(err);
        this.scheduleReconnect();
      }
      this.deps.logger.error('ADB', `Connection failed for ${targetKey(this.target)}`, error);
      throw err;
    }
  }

  /**
   * The transport ends (resolve or reject) when its packet streams close,
   * either from adbd going away or from the platform closing a long-lived
   * request. Either way a fresh socket recovers, so reconnect rather than
   * surfacing failure.
   */
  private observeDisconnect(device: Adb, generation: number): void {
    const onDead = (reason: unknown): void => {
      if (generation !== this.generation) {
        return;
      }
      this.deps.logger.warn('ADB', `Transport died for ${targetKey(this.target)}`, reason);
      this.invalidate();
      this.scheduleReconnect();
    };
    device.disconnected.then(() => onDead('transport closed'), onDead);
  }

  /** Periodic probe: catches the proxy dying without closing the socket. */
  private scheduleHeartbeat(device: Adb, generation: number): void {
    this.heartbeatTimer = setTimeout(() => {
      void withTimeout(
        device.getProp('ro.product.model'),
        this.timings.heartbeatTimeoutMs,
        'heartbeat timed out'
      ).then(
        () => {
          if (generation === this.generation) {
            this.scheduleHeartbeat(device, generation);
          }
        },
        (error: unknown) => {
          if (generation !== this.generation) {
            return;
          }
          this.deps.logger.warn('ADB', `Heartbeat failed for ${targetKey(this.target)}`, error);
          this.invalidate();
          this.scheduleReconnect();
        }
      );
    }, this.timings.heartbeatIntervalMs);
  }

  /**
   * Retries with exponential backoff, then gives up loudly.
   *
   * Bounded on purpose: a device that was deleted, or one whose ADB is broken,
   * must settle in `error` rather than reconnecting forever behind a UI that
   * claims to be trying.
   */
  private scheduleReconnect(): void {
    if (this.disposed || this.reconnectTimer !== null) {
      return;
    }
    if (this.reconnectAttempts >= this.timings.maxReconnectAttempts) {
      this.deps.logger.warn('ADB', `Giving up reconnecting to ${targetKey(this.target)}`);
      this.stateSignal.set('error');
      this.errorSignal.set(
        this.errorSignal() ?? new Error('ADB connection lost and could not be re-established')
      );
      return;
    }

    const delay = Math.min(
      this.timings.reconnectDelayMs * 2 ** this.reconnectAttempts,
      this.timings.maxReconnectDelayMs
    );
    this.reconnectAttempts++;
    const generation = this.generation;

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (this.disposed || generation !== this.generation) {
        return;
      }
      void this.ensureConnected().catch((error: unknown) => {
        this.deps.logger.debug('ADB', `Reconnect failed for ${targetKey(this.target)}`, error);
      });
    }, delay);
  }

  /**
   * Drops the connection and closes its socket. Unlike the data-channel
   * transport this replaced, the socket is the session's own, so leaving it
   * open would hold a stream on the orchestrator for a client that is gone.
   */
  private invalidate(): void {
    this.generation++;
    this.device = null;
    this.connection?.dispose();
    this.connection = null;
    if (this.heartbeatTimer !== null) {
      clearTimeout(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
    if (this.stateSignal() !== 'disconnected') {
      this.stateSignal.set('disconnected');
      this.errorSignal.set(null);
      this.deps.logger.debug('ADB', `Invalidated connection for ${targetKey(this.target)}`);
    }
  }

  /** Called by the registry when the last claim is released. */
  dispose(): void {
    this.disposed = true;
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.invalidate();
  }
}
