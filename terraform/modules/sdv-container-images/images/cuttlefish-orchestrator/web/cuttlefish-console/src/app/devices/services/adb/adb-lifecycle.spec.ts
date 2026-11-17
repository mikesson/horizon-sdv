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
 * Lifecycle contract tests for the ADB stack.
 *
 * A fake ADB daemon answers the CNXN handshake over a fake WebSocket, so the
 * REAL ya-webadb transport authenticates through the REAL packet bridge. This
 * pins the Cuttlefish assumption that the host's ADB proxy handles auth
 * (device replies CNXN directly, never AUTH), and the connect/reconnect
 * behaviour of a session that owns its own transport.
 */

import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {LoggingService} from '../../../shared/services';
import type {AdbClaim} from './adb.service';
import {ADB_SOCKET_FACTORY, AdbService} from './adb.service';
import type {AdbSessionTimings} from './adb-session';
import {AdbSession} from './adb-session';

const DEVICE_ID = 'dev-1';
const TARGET = {deviceId: DEVICE_ID, instance: 'inst'};
const CNXN = 0x4e584e43;
const DEVICE_BANNER = 'device::ro.product.name=cuttlefish;features=shell_v2,cmd';

type SocketListener = (event: Event | MessageEvent) => void;

/**
 * A WebSocket with a minimal ADB daemon behind it: parses outgoing wire frames
 * and answers the CNXN handshake like the Cuttlefish ADB proxy (connect
 * accepted directly, with no AUTH round).
 *
 * Frames are split in half on the way back on purpose, because the real
 * transport is a byte stream whose frame boundaries mean nothing. A packet
 * that only reassembles when it arrives whole would pass a friendlier fake and
 * fail in production.
 */
class FakeAdbDaemonSocket {
  static readonly OPEN = 1;
  binaryType = '';
  readyState: number = FakeAdbDaemonSocket.OPEN;
  bufferedAmount = 0;
  /** Set false to simulate a silently dead proxy (socket open, no daemon). */
  answerCnxn = true;
  readonly received: Array<{command: number; payload: string}> = [];
  readonly close = vi.fn(() => {
    this.readyState = 3;
  });

  private readonly listeners = new Map<string, SocketListener[]>();
  private buffer = new Uint8Array(0);

  addEventListener(type: string, listener: SocketListener): void {
    const existing = this.listeners.get(type) ?? [];
    existing.push(listener);
    this.listeners.set(type, existing);
  }

  removeEventListener(type: string, listener: SocketListener): void {
    const existing = this.listeners.get(type) ?? [];
    this.listeners.set(
      type,
      existing.filter(l => l !== listener)
    );
  }

  /** Simulates adbd dropping: the orchestrator closes the socket. */
  fireClose(): void {
    this.readyState = 3;
    for (const listener of this.listeners.get('close') ?? []) {
      listener(new Event('close'));
    }
  }

  send(data: Uint8Array): void {
    const combined = new Uint8Array(this.buffer.length + data.length);
    combined.set(this.buffer);
    combined.set(data, this.buffer.length);
    this.buffer = combined;
    this.drainFrames();
  }

  private drainFrames(): void {
    while (this.buffer.length >= 24) {
      const view = new DataView(this.buffer.buffer, this.buffer.byteOffset, 24);
      const command = view.getUint32(0, true);
      const payloadLength = view.getUint32(12, true);
      if (this.buffer.length < 24 + payloadLength) {
        return;
      }
      const payload = this.buffer.slice(24, 24 + payloadLength);
      this.buffer = this.buffer.slice(24 + payloadLength);

      this.received.push({command, payload: new TextDecoder().decode(payload)});
      if (command === CNXN && this.answerCnxn) {
        this.reply(CNXN, 0x01000001, 0x100000, new TextEncoder().encode(DEVICE_BANNER));
      }
    }
  }

  private reply(command: number, arg0: number, arg1: number, payload: Uint8Array): void {
    const frame = new Uint8Array(24 + payload.length);
    const view = new DataView(frame.buffer);
    view.setUint32(0, command, true);
    view.setUint32(4, arg0, true);
    view.setUint32(8, arg1, true);
    view.setUint32(12, payload.length, true);
    view.setUint32(16, 0, true);
    view.setUint32(20, (command ^ 0xffffffff) >>> 0, true);
    frame.set(payload, 24);

    // Split across two frames: the transport must reassemble from bytes.
    const split = Math.floor(frame.length / 2);
    this.deliver(frame.slice(0, split));
    this.deliver(frame.slice(split));
  }

  private deliver(bytes: Uint8Array): void {
    const buffer = new ArrayBuffer(bytes.length);
    new Uint8Array(buffer).set(bytes);
    queueMicrotask(() => {
      for (const listener of this.listeners.get('message') ?? []) {
        listener({data: buffer} as MessageEvent);
      }
    });
  }
}

const FAST_TIMINGS: AdbSessionTimings = {
  connectTimeoutMs: 3000,
  heartbeatIntervalMs: 60_000,
  heartbeatTimeoutMs: 60_000,
  reconnectDelayMs: 10,
  maxReconnectDelayMs: 20,
  maxReconnectAttempts: 3,
};

describe('ADB connection lifecycle', () => {
  let adb: AdbService;
  /** Every socket the sessions opened, newest last. */
  let sockets: FakeAdbDaemonSocket[];
  let claims: AdbClaim[];
  let answerCnxn: boolean;

  /** The socket currently carrying the session. */
  const daemon = (): FakeAdbDaemonSocket => sockets[sockets.length - 1];

  function openFake(): WebSocket {
    const socket = new FakeAdbDaemonSocket();
    socket.answerCnxn = answerCnxn;
    sockets.push(socket);
    return socket as unknown as WebSocket;
  }

  beforeEach(() => {
    sockets = [];
    claims = [];
    answerCnxn = true;
    TestBed.configureTestingModule({
      providers: [{provide: ADB_SOCKET_FACTORY, useValue: openFake}],
    });
    adb = TestBed.inject(AdbService);
  });

  afterEach(() => {
    for (const claim of claims) {
      claim.release();
    }
    vi.restoreAllMocks();
  });

  function acquire(): AdbClaim {
    const claim = adb.acquire(TARGET);
    claims.push(claim);
    return claim;
  }

  function newSession(timings: Partial<AdbSessionTimings> = {}): AdbSession {
    return new AdbSession(TARGET, {
      logger: TestBed.inject(LoggingService),
      timings: {...FAST_TIMINGS, ...timings},
      openSocket: openFake,
    });
  }

  async function waitForAdbState(expected: string): Promise<void> {
    await vi.waitFor(
      () => {
        expect(adb.state(TARGET)()).toBe(expected);
      },
      {timeout: 3000, interval: 10}
    );
  }

  // The point of the whole change: ADB no longer waits on a video stream.
  it('connects as soon as it is claimed, with no stream involved', async () => {
    acquire();
    await waitForAdbState('connected');

    expect(daemon().received.map(f => f.command)).toEqual([CNXN]);
    expect(daemon().received[0].payload.startsWith('host::')).toBe(true);
  });

  it('authenticates without an AUTH round', async () => {
    const session = newSession();
    try {
      await session.ensureConnected();
      expect(daemon().received.map(f => f.command)).toEqual([CNXN]);
    } finally {
      session.dispose();
    }
  });

  it('reconnects on a fresh socket when the transport dies', async () => {
    const session = newSession();
    try {
      await session.ensureConnected();
      expect(session.state()).toBe('connected');
      expect(sockets).toHaveLength(1);

      // adbd going away closes the socket. Unlike the data channel this
      // replaced, a new one can simply be opened.
      daemon().fireClose();

      // Wait for the replacement socket first: asserting 'connected' straight
      // away would pass on the state the dead connection left behind.
      await vi.waitFor(() => expect(sockets.length).toBeGreaterThan(1), {timeout: 3000});
      await vi.waitFor(() => expect(session.state()).toBe('connected'), {timeout: 3000});
      expect(daemon().received.map(f => f.command)).toEqual([CNXN]);
    } finally {
      session.dispose();
    }
  });

  it('gives up loudly after exhausting its reconnect attempts', async () => {
    answerCnxn = false;
    const session = newSession({connectTimeoutMs: 30});
    try {
      await expect(session.ensureConnected()).rejects.toThrow(/did not respond/);
      await vi.waitFor(() => expect(session.state()).toBe('error'), {timeout: 3000});
      expect(session.error()).not.toBeNull();
    } finally {
      session.dispose();
    }
  });

  it('fails the connect instead of hanging when the device never answers', async () => {
    answerCnxn = false;
    const session = newSession({connectTimeoutMs: 50});
    try {
      await expect(session.ensureConnected()).rejects.toThrow(/did not respond/);
      expect(session.state()).toBe('error');
    } finally {
      session.dispose();
    }
  });

  it('drops a connection whose device stops answering the heartbeat', async () => {
    const session = newSession({heartbeatIntervalMs: 50, heartbeatTimeoutMs: 50});
    try {
      await session.ensureConnected();
      expect(session.state()).toBe('connected');
      // The fake answers CNXN but never the heartbeat's getprop.
      await vi.waitFor(() => expect(session.state()).not.toBe('connected'), {timeout: 3000});
    } finally {
      session.dispose();
    }
  });

  it('closes its socket on dispose', async () => {
    const session = newSession();
    await session.ensureConnected();
    session.dispose();
    expect(daemon().close).toHaveBeenCalled();
  });

  it('keeps the session alive until the last claim is released', async () => {
    const first = adb.acquire(TARGET);
    const second = adb.acquire(TARGET);
    expect(second.session).toBe(first.session);

    await waitForAdbState('connected');

    // Releasing one claim (even twice) must not tear down the session.
    second.release();
    second.release();
    expect(adb.session(TARGET)()).not.toBeNull();
    expect(adb.state(TARGET)()).toBe('connected');

    first.release();
    expect(adb.session(TARGET)()).toBeNull();
    expect(adb.state(TARGET)()).toBe('disconnected');
  });
});
