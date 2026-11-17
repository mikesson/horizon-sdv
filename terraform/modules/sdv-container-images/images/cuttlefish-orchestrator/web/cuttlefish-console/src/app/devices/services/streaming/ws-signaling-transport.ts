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

import {resolveWebSocketUrl} from '../../../shared/utils';
import type {InstanceTarget} from '../../models/instance-target';
import {targetPath} from '../../models/instance-target';
import type {
  SignalingMessageHandler,
  SignalingTransport,
  SignalingTransportDeps,
} from './signaling-transport';
import type {OutboundSignalingMessage} from './streaming.types';
import {parseInboundMessage} from './streaming.types';

const CONNECT_TIMEOUT_MS = 5000;

/** Raw frame from the signaling proxy, before payload unwrapping. */
interface WsFrame {
  device_info?: unknown;
  error?: unknown;
}

/**
 * WebSocket signaling via the orchestrator's `/signaling/ws` proxy.
 *
 * The server sends `{device_info}` as its first frame (the handshake), then
 * signaling messages as `{payload: {...}}` frames. `{error}` frames map to a
 * typed error message. After an unexpected close the transport goes quiet
 * and send() rejects. Connection health is judged at the WebRTC layer.
 */
export class WsSignalingTransport implements SignalingTransport {
  static open(
    target: InstanceTarget,
    onMessage: SignalingMessageHandler,
    deps: SignalingTransportDeps
  ): Promise<WsSignalingTransport> {
    return new Promise((resolve, reject) => {
      const url = resolveWebSocketUrl(`${targetPath(target)}/signaling/ws`);
      deps.logger.debug('Signaling', 'Connecting WebSocket', {url});

      let ws: WebSocket;
      try {
        ws = new WebSocket(url);
      } catch (error: unknown) {
        reject(new Error(`WebSocket creation failed: ${String(error)}`));
        return;
      }

      let established = false;
      const timeout = setTimeout(() => {
        if (!established) {
          ws.close();
          reject(new Error('WebSocket connection timeout'));
        }
      }, CONNECT_TIMEOUT_MS);

      const transport = new WsSignalingTransport(ws, deps);

      ws.onmessage = event => {
        let frame: WsFrame;
        try {
          frame = JSON.parse(event.data as string) as WsFrame;
        } catch (error: unknown) {
          deps.logger.error('Signaling', 'Failed to parse WebSocket frame', error);
          return;
        }

        if (!established && frame.device_info !== undefined) {
          established = true;
          clearTimeout(timeout);
          deps.logger.debug('Signaling', 'WebSocket connection established');
          resolve(transport);
          return;
        }

        if (typeof frame.error === 'string') {
          if (!established) {
            clearTimeout(timeout);
            reject(new Error(frame.error));
            return;
          }
          onMessage({type: 'error', message: frame.error});
          return;
        }

        const message = parseInboundMessage(frame);
        if (message) {
          onMessage(message);
        } else {
          deps.logger.debug('Signaling', 'Dropping unrecognized frame', frame);
        }
      };

      ws.onerror = event => {
        deps.logger.error('Signaling', 'WebSocket error', event);
        if (!established) {
          clearTimeout(timeout);
          reject(new Error('WebSocket connection failed'));
        }
      };

      ws.onclose = event => {
        deps.logger.debug('Signaling', 'WebSocket closed', {
          code: event.code,
          reason: event.reason,
        });
        if (!established) {
          clearTimeout(timeout);
          reject(new Error('WebSocket closed before receiving device info'));
        }
      };
    });
  }

  private constructor(
    private readonly ws: WebSocket,
    private readonly deps: SignalingTransportDeps
  ) {}

  send(message: OutboundSignalingMessage): Promise<void> {
    if (this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('WebSocket not connected'));
    }
    this.deps.logger.debug('Signaling', 'Sending message', {type: message.type});
    this.ws.send(JSON.stringify(message));
    return Promise.resolve();
  }

  setIdle(_idle: boolean): void {
    // Push-based; nothing to throttle.
  }

  expectMessagesSoon(_durationMs: number): void {
    // Push-based; messages arrive as soon as they exist.
  }

  close(): void {
    this.ws.close(1000, 'Client disconnect');
  }
}
