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
 * Framing tests for the WebSocket <-> ADB packet bridge.
 *
 * The 24-byte header format and the magic-validation resync must match the ADB
 * wire protocol exactly; a live Cuttlefish device is the only other thing that
 * would catch drift here. Frame boundaries carry no meaning on this transport,
 * because the orchestrator forwards whatever chunks the host operator produced.
 * Reassembly is the behaviour these tests exist to protect.
 */

import {Consumable} from '@yume-chan/stream-extra';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {adbSocketUrl, WebSocketAdbConnection} from './websocket-adb-connection';

type SocketListener = (event: Event | MessageEvent) => void;

const OPEN = 1;
const CLOSED = 3;

class FakeSocket {
  binaryType = '';
  readyState: number = OPEN;
  bufferedAmount = 0;
  readonly sent: Uint8Array[] = [];
  readonly close = vi.fn(() => {
    this.readyState = CLOSED;
  });

  private readonly listeners = new Map<string, SocketListener[]>();

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

  listenerCount(): number {
    return [...this.listeners.values()].reduce((sum, list) => sum + list.length, 0);
  }

  send(data: Uint8Array): void {
    this.sent.push(data);
  }

  dispatch(type: string, event: Event | MessageEvent): void {
    for (const listener of [...(this.listeners.get(type) ?? [])]) {
      listener(event);
    }
  }

  receive(bytes: Uint8Array): void {
    const buffer = new ArrayBuffer(bytes.length);
    new Uint8Array(buffer).set(bytes);
    this.dispatch('message', {data: buffer} as MessageEvent);
  }
}

/** Builds an ADB wire packet: 24-byte little-endian header + payload. */
function wirePacket(
  command: number,
  arg0: number,
  arg1: number,
  payload = new Uint8Array(0)
): Uint8Array {
  const packet = new Uint8Array(24 + payload.length);
  const view = new DataView(packet.buffer);
  view.setUint32(0, command, true);
  view.setUint32(4, arg0, true);
  view.setUint32(8, arg1, true);
  view.setUint32(12, payload.length, true);
  view.setUint32(16, 0, true); // Checksum: unvalidated on both sides.
  view.setUint32(20, (command ^ 0xffffffff) >>> 0, true);
  packet.set(payload, 24);
  return packet;
}

const CNXN = 0x4e584e43;
const WRTE = 0x45545257;

describe('WebSocketAdbConnection framing', () => {
  let socket: FakeSocket;
  let connection: WebSocketAdbConnection;

  beforeEach(() => {
    socket = new FakeSocket();
    connection = new WebSocketAdbConnection(socket as unknown as WebSocket);
  });

  async function readPacket(): Promise<{command: number; payload: Uint8Array}> {
    const reader = connection.readable.getReader();
    const {value} = await reader.read();
    reader.releaseLock();
    return value as {command: number; payload: Uint8Array};
  }

  it('sets binaryType to arraybuffer and is ready when the socket is open', async () => {
    expect(socket.binaryType).toBe('arraybuffer');
    await expect(connection.ready).resolves.toBeUndefined();
  });

  it('waits for the open event when the socket is still connecting', async () => {
    const connecting = new FakeSocket();
    connecting.readyState = 0;
    const pending = new WebSocketAdbConnection(connecting as unknown as WebSocket);

    let ready = false;
    void pending.ready.then(() => {
      ready = true;
    });
    await Promise.resolve();
    expect(ready).toBe(false);

    connecting.readyState = OPEN;
    connecting.dispatch('open', new Event('open'));
    await expect(pending.ready).resolves.toBeUndefined();
  });

  it('parses a complete packet from a single message', async () => {
    socket.receive(wirePacket(CNXN, 0x01000001, 0x100000, new TextEncoder().encode('device::')));
    const packet = await readPacket();
    expect(packet.command).toBe(CNXN);
    expect(new TextDecoder().decode(packet.payload)).toBe('device::');
  });

  // The transport is a byte stream: the operator chunks on io.Copy's buffer,
  // so a packet routinely arrives in pieces that mean nothing on their own.
  it('reassembles a packet split across message events', async () => {
    const packet = wirePacket(WRTE, 1, 2, new TextEncoder().encode('hello world'));
    socket.receive(packet.slice(0, 10));
    socket.receive(packet.slice(10, 27));
    socket.receive(packet.slice(27));

    const received = await readPacket();
    expect(received.command).toBe(WRTE);
    expect(new TextDecoder().decode(received.payload)).toBe('hello world');
  });

  it('parses multiple packets from one message event', async () => {
    const first = wirePacket(WRTE, 1, 2, new TextEncoder().encode('one'));
    const second = wirePacket(WRTE, 3, 4, new TextEncoder().encode('two'));
    const combined = new Uint8Array(first.length + second.length);
    combined.set(first);
    combined.set(second, first.length);
    socket.receive(combined);

    expect(new TextDecoder().decode((await readPacket()).payload)).toBe('one');
    expect(new TextDecoder().decode((await readPacket()).payload)).toBe('two');
  });

  it('handles zero-payload packets', async () => {
    socket.receive(wirePacket(0x4b41544f, 5, 6));
    const packet = await readPacket();
    expect(packet.payload.length).toBe(0);
  });

  it('skips garbage bytes until a valid magic resyncs the stream', async () => {
    const garbage = new Uint8Array([1, 2, 3, 4, 5]);
    const packet = wirePacket(WRTE, 7, 8, new TextEncoder().encode('after'));
    const combined = new Uint8Array(garbage.length + packet.length);
    combined.set(garbage);
    combined.set(packet, garbage.length);
    socket.receive(combined);

    expect(new TextDecoder().decode((await readPacket()).payload)).toBe('after');
  });

  it('ends the readable when the socket closes', async () => {
    socket.readyState = CLOSED;
    socket.dispatch('close', new Event('close'));

    const reader = connection.readable.getReader();
    await expect(reader.read()).resolves.toMatchObject({done: true});
  });

  it('rejects ready when the socket closes before opening', async () => {
    const connecting = new FakeSocket();
    connecting.readyState = 0;
    const pending = new WebSocketAdbConnection(connecting as unknown as WebSocket);

    connecting.readyState = CLOSED;
    connecting.dispatch('close', new Event('close'));

    // Otherwise authenticate() would wait out its own timeout instead of
    // failing as soon as the transport is known to be gone.
    await expect(pending.ready).rejects.toThrow(/closed/);
  });

  it('serializes written packets into valid wire frames', async () => {
    const payload = new TextEncoder().encode('ping');
    const writer = connection.writable.getWriter();
    await writer.write(
      new Consumable({
        command: WRTE,
        arg0: 1,
        arg1: 2,
        checksum: 0,
        magic: (WRTE ^ 0xffffffff) >>> 0,
        payload,
      })
    );
    writer.releaseLock();

    // ya-webadb may emit header and payload as separate sends.
    const total = socket.sent.reduce((sum, chunk) => sum + chunk.length, 0);
    const frame = new Uint8Array(total);
    let offset = 0;
    for (const chunk of socket.sent) {
      frame.set(chunk, offset);
      offset += chunk.length;
    }

    const view = new DataView(frame.buffer);
    expect(view.getUint32(0, true)).toBe(WRTE);
    expect(view.getUint32(12, true)).toBe(payload.length);
    expect(view.getUint32(20, true)).toBe((WRTE ^ 0xffffffff) >>> 0);
    expect(new TextDecoder().decode(frame.slice(24))).toBe('ping');
  });

  it('survives error followed by close without a second stream settle', async () => {
    socket.dispatch('error', new Event('error'));
    socket.readyState = CLOSED;
    socket.dispatch('close', new Event('close'));

    const reader = connection.readable.getReader();
    await expect(reader.read()).rejects.toThrow(/WebSocket error/);
  });

  // The opposite of the data channel this replaced: the socket is the ADB
  // session's own, so letting it go must close it or the orchestrator holds a
  // device stream open for a client that has gone away.
  it('dispose closes the socket and detaches every listener', () => {
    expect(socket.listenerCount()).toBeGreaterThan(0);
    connection.dispose();
    expect(socket.close).toHaveBeenCalled();
    expect(socket.listenerCount()).toBe(0);
  });

  it('dispose ends the readable', async () => {
    connection.dispose();
    const reader = connection.readable.getReader();
    await expect(reader.read()).resolves.toMatchObject({done: true});
  });

  it('drops frames written to a closed socket instead of throwing', async () => {
    socket.readyState = CLOSED;
    const writer = connection.writable.getWriter();
    await writer.write(
      new Consumable({
        command: WRTE,
        arg0: 1,
        arg1: 2,
        checksum: 0,
        magic: (WRTE ^ 0xffffffff) >>> 0,
        payload: new Uint8Array(0),
      })
    );
    writer.releaseLock();
    expect(socket.sent).toHaveLength(0);
  });
});

describe('adbSocketUrl', () => {
  it('targets the orchestrator ADB route for one instance', () => {
    const url = adbSocketUrl({deviceId: 'dev-1', instance: 'watch'});
    expect(url).toMatch(/^wss?:\/\//);
    expect(url.endsWith('/v2/devices/dev-1/instances/watch/adb')).toBe(true);
  });

  it('escapes both segments', () => {
    expect(adbSocketUrl({deviceId: 'a/b', instance: 'c/d'})).toContain(
      '/v2/devices/a%2Fb/instances/c%2Fd/adb'
    );
  });
});
