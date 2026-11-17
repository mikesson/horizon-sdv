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

import type {AdbPacketData, AdbPacketInit} from '@yume-chan/adb';
import {AdbPacketSerializeStream} from '@yume-chan/adb';
import type {Consumable, ReadableStream} from '@yume-chan/stream-extra';
import {
  DuplexStreamFactory,
  PushReadableStream,
  type PushReadableStreamController,
  pipeFrom,
  type ReadableWritablePair,
  WritableStream,
} from '@yume-chan/stream-extra';
import {resolveWebSocketUrl} from '../../../shared/utils';
import type {InstanceTarget} from '../../models/instance-target';
import {targetPath} from '../../models/instance-target';

/** Builds the orchestrator's ADB endpoint for one instance. */
export function adbSocketUrl(target: InstanceTarget): string {
  return resolveWebSocketUrl(`${targetPath(target)}/adb`);
}

/**
 * Bridges ya-webadb's AdbDaemonConnection interface onto the orchestrator's
 * ADB WebSocket, so the browser can speak the ADB wire protocol to a device it
 * has no route to.
 *
 * The socket carries a byte stream, not messages: the host operator writes
 * whatever `io.Copy` hands it and reassembles across frame boundaries, so a
 * frame is an arbitrary chunk. Hence the 24-byte-header reassembly below.
 * Packets are recovered from the byte stream, never assumed to be one frame.
 *
 * Unlike the streaming session's data channel, this socket belongs to the ADB
 * session alone, so `dispose()` closes it.
 */
export class WebSocketAdbConnection
  implements ReadableWritablePair<AdbPacketData, Consumable<AdbPacketInit>>
{
  private readonly socket: WebSocket;
  private readonly duplex: DuplexStreamFactory<AdbPacketData, Consumable<Uint8Array>>;
  private pushController!: PushReadableStreamController<AdbPacketData>;

  readonly readable: ReadableStream<AdbPacketData>;
  readonly writable: WritableStream<Consumable<AdbPacketInit>>;

  private buffer = new Uint8Array(0);

  private readonly readyResolver: {
    promise: Promise<void>;
    resolve: () => void;
    reject: (error: Error) => void;
  };

  private closed = false;
  /** The push stream settles exactly once (close and error both race in). */
  private streamEnded = false;
  private readonly socketListeners: SocketListenerRegistration[] = [];
  private drainTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(socket: WebSocket) {
    this.socket = socket;

    let resolveReady!: () => void;
    let rejectReady!: (error: Error) => void;
    const readyPromise = new Promise<void>((resolve, reject) => {
      resolveReady = resolve;
      rejectReady = reject;
    });
    this.readyResolver = {promise: readyPromise, resolve: resolveReady, reject: rejectReady};

    socket.binaryType = 'arraybuffer';

    this.duplex = new DuplexStreamFactory<AdbPacketData, Consumable<Uint8Array>>({
      close: () => {
        this.closed = true;
      },
      dispose: () => {
        this.closed = true;
      },
    });

    const pushReadable = new PushReadableStream<AdbPacketData>(controller => {
      this.pushController = controller;
    });
    this.readable = this.duplex.wrapReadable(pushReadable);

    this.writable = pipeFrom(
      this.duplex.createWritable(
        new WritableStream<Consumable<Uint8Array>>({
          write: async chunk => {
            await this.sendData(chunk.value);
            chunk.consume();
          },
        })
      ),
      new AdbPacketSerializeStream()
    );

    this.setupSocketHandlers();
  }

  /** Resolves once the socket is open and ready to carry packets. */
  get ready(): Promise<void> {
    return this.readyResolver.promise;
  }

  /**
   * Closes the socket and detaches from it. The socket is this connection's
   * own, unlike the streaming session's shared data channel, so tearing the
   * ADB session down must close it, or the orchestrator holds a stream for a
   * client that is gone.
   */
  dispose(): void {
    this.closed = true;
    for (const {type, listener} of this.socketListeners) {
      this.socket.removeEventListener(type, listener);
    }
    this.socketListeners.length = 0;
    if (this.drainTimer !== null) {
      clearTimeout(this.drainTimer);
      this.drainTimer = null;
    }
    if (
      this.socket.readyState === WebSocket.OPEN ||
      this.socket.readyState === WebSocket.CONNECTING
    ) {
      this.socket.close();
    }
    this.endStream();
  }

  private listen(type: string, listener: EventListener): void {
    this.socket.addEventListener(type, listener);
    this.socketListeners.push({type, listener});
  }

  private setupSocketHandlers(): void {
    if (this.socket.readyState === WebSocket.OPEN) {
      this.readyResolver.resolve();
    } else {
      this.listen('open', () => {
        this.readyResolver.resolve();
      });
    }

    this.listen('message', event => {
      const data = (event as MessageEvent).data as ArrayBuffer;
      this.handleIncomingData(new Uint8Array(data));
    });

    this.listen('close', () => {
      this.closed = true;
      // A socket that closes before opening never settles `ready` otherwise,
      // leaving the authenticate() await hanging until its own timeout.
      this.readyResolver.reject(new Error('ADB connection closed'));
      this.endStream();
    });

    this.listen('error', () => {
      // The WebSocket error event carries no detail by design (it would leak
      // cross-origin information), so the close event that follows is where
      // any diagnosis has to come from.
      const error = new Error('ADB WebSocket error');
      this.closed = true;
      this.readyResolver.reject(error);
      this.failStream(error);
    });
  }

  /** Browsers fire error then close on abrupt teardown; settle only once. */
  private endStream(): void {
    if (this.streamEnded) {
      return;
    }
    this.streamEnded = true;
    this.pushController.close();
  }

  private failStream(error: Error): void {
    if (this.streamEnded) {
      return;
    }
    this.streamEnded = true;
    this.pushController.error(error);
  }

  /**
   * Recovers ADB packets from the byte stream.
   *
   * ADB packet format:
   * - Header: 24 bytes (command, arg0, arg1, payloadLength, checksum, magic)
   * - Payload: variable length
   *
   * The magic field must equal (command XOR 0xffffffff) for valid packets.
   * Invalid packets are skipped byte-by-byte to recover from stream desync.
   */
  private handleIncomingData(data: Uint8Array): void {
    const newBuffer = new Uint8Array(this.buffer.length + data.length);
    newBuffer.set(this.buffer);
    newBuffer.set(data, this.buffer.length);
    this.buffer = newBuffer;

    while (this.buffer.length >= 24) {
      const view = new DataView(this.buffer.buffer, this.buffer.byteOffset, 24);
      const command = view.getUint32(0, true);
      const arg0 = view.getUint32(4, true);
      const arg1 = view.getUint32(8, true);
      const payloadLength = view.getUint32(12, true);
      // checksum at offset 16 (not validated for Android 9+)
      const magic = view.getUint32(20, true);

      const expectedMagic = (command ^ 0xffffffff) >>> 0;
      if (magic !== expectedMagic) {
        this.buffer = this.buffer.slice(1);
        continue;
      }

      const totalLength = 24 + payloadLength;
      if (this.buffer.length < totalLength) {
        break;
      }

      const payload = payloadLength > 0 ? this.buffer.slice(24, totalLength) : new Uint8Array(0);
      void this.pushController.enqueue({command, arg0, arg1, payload});
      this.buffer = this.buffer.slice(totalLength);
    }
  }

  /**
   * Sends one serialized packet.
   *
   * Frames for a dead or detached socket are dropped, not errored: the
   * transport learns of the death from the readable ending, and a throw here
   * would only surface as an unhandled rejection in ya-webadb's floating pipe
   * promise.
   */
  private async sendData(data: Uint8Array): Promise<void> {
    await this.ready;
    if (!this.canSend()) {
      return;
    }

    if (this.socket.bufferedAmount > SEND_BUFFER_THRESHOLD) {
      await this.waitForBufferDrain(SEND_BUFFER_THRESHOLD / 2);
      if (!this.canSend()) {
        return;
      }
    }

    // Copied into a fresh array for the DOM API: ya-webadb hands out
    // Uint8Array<ArrayBufferLike>, which WebSocket.send does not accept.
    const buffer = new Uint8Array(data.length);
    buffer.set(data);
    this.socket.send(buffer);
  }

  private canSend(): boolean {
    return !this.closed && this.socket.readyState === WebSocket.OPEN;
  }

  /**
   * Resolves once `bufferedAmount` drains to the threshold.
   *
   * Polled, unlike the data-channel path: the WebSocket API has no
   * `bufferedamountlow` event, so a timer is the only way to observe the
   * queue draining. The interval only runs while a large transfer is in
   * flight and outrunning the socket.
   */
  private waitForBufferDrain(threshold: number): Promise<void> {
    return new Promise(resolve => {
      const check = (): void => {
        if (!this.canSend() || this.socket.bufferedAmount <= threshold) {
          this.drainTimer = null;
          resolve();
          return;
        }
        this.drainTimer = setTimeout(check, DRAIN_POLL_INTERVAL_MS);
      };
      check();
    });
  }
}

const SEND_BUFFER_THRESHOLD = 1024 * 1024;
const DRAIN_POLL_INTERVAL_MS = 20;

interface SocketListenerRegistration {
  type: string;
  listener: EventListener;
}
