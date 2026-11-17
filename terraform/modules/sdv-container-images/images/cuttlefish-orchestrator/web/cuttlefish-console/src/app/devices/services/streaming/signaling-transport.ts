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

import type {HttpClient} from '@angular/common/http';
import type {LoggingService} from '../../../shared/services';
import type {InstanceTarget} from '../../models/instance-target';
import {PollingSignalingTransport} from './polling-signaling-transport';
import type {InboundSignalingMessage, OutboundSignalingMessage} from './streaming.types';
import {WsSignalingTransport} from './ws-signaling-transport';

/**
 * One live signaling channel to a device, proxied through the orchestrator.
 *
 * A transport is created per streaming session and owns all per-connection
 * signaling state (socket or poll counter). Messages are pushed to the
 * session's handler as they arrive, over exactly one delivery path.
 */
export interface SignalingTransport {
  send(message: OutboundSignalingMessage): Promise<void>;
  /** Slows the poll cadence once media is up. No-op for push transports. */
  setIdle(idle: boolean): void;
  /** Polls actively for a while when messages are expected (e.g. a
   * renegotiation offer after a display change). No-op for push transports. */
  expectMessagesSoon(durationMs: number): void;
  close(): void;
}

export interface SignalingTransportDeps {
  http: HttpClient;
  logger: LoggingService;
}

export type SignalingMessageHandler = (message: InboundSignalingMessage) => void;

/**
 * Opens the signaling channel for one instance: WebSocket first, falling back
 * to HTTP long-polling when the socket cannot be established.
 */
export async function openSignalingTransport(
  target: InstanceTarget,
  onMessage: SignalingMessageHandler,
  deps: SignalingTransportDeps
): Promise<SignalingTransport> {
  try {
    return await WsSignalingTransport.open(target, onMessage, deps);
  } catch (error: unknown) {
    deps.logger.warn('Signaling', 'WebSocket failed, falling back to HTTP polling', {
      error: error instanceof Error ? error.message : String(error),
    });
  }
  return PollingSignalingTransport.open(target, onMessage, deps);
}
