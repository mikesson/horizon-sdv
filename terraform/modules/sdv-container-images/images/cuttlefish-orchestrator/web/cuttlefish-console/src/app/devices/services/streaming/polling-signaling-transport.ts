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

import {firstValueFrom} from 'rxjs';
import {resolveBasePath} from '../../../shared/utils';
import type {InstanceTarget} from '../../models/instance-target';
import {targetPath} from '../../models/instance-target';
import type {
  SignalingMessageHandler,
  SignalingTransport,
  SignalingTransportDeps,
} from './signaling-transport';
import type {OutboundSignalingMessage} from './streaming.types';
import {parseInboundMessage} from './streaming.types';

interface ConnectResponse {
  connection_id: string;
  device_info: unknown;
}

const ACTIVE_POLL_MS = 100;
const IDLE_POLL_MS = 2000;
const ERROR_RETRY_MS = 2000;

/**
 * HTTP long-poll signaling via the orchestrator proxy.
 *
 * The Host Orchestrator's `?start=N` parameter skips already-delivered
 * messages; the counter is cumulative per connection. Poll errors are
 * retried, and connection health is judged at the WebRTC layer, not here.
 */
export class PollingSignalingTransport implements SignalingTransport {
  private received = 0;
  private idle = false;
  private closed = false;
  /** While in the future, poll at the active cadence despite being idle. */
  private activeUntil = 0;

  static async open(
    target: InstanceTarget,
    onMessage: SignalingMessageHandler,
    deps: SignalingTransportDeps
  ): Promise<PollingSignalingTransport> {
    const response = await firstValueFrom(
      deps.http.post<ConnectResponse>(`${targetPath(target)}/signaling/connect`, {})
    );
    return new PollingSignalingTransport(target, response.connection_id, onMessage, deps);
  }

  // The message routes stay device-scoped: the operator mints connectionId
  // against the instance this connect named, so the id is the binding from
  // here on and repeating the instance would decorate a segment nothing reads.
  private constructor(
    target: InstanceTarget,
    connectionId: string,
    private readonly onMessage: SignalingMessageHandler,
    private readonly deps: SignalingTransportDeps
  ) {
    this.url = resolveBasePath(
      `/v2/devices/${encodeURIComponent(target.deviceId)}/signaling/${connectionId}`
    );
    void this.pollLoop();
  }

  private readonly url: string;

  async send(message: OutboundSignalingMessage): Promise<void> {
    this.deps.logger.debug('Signaling', 'Sending message', message);
    await firstValueFrom(this.deps.http.post<void>(this.url, message));
  }

  setIdle(idle: boolean): void {
    this.idle = idle;
  }

  expectMessagesSoon(durationMs: number): void {
    this.activeUntil = Date.now() + durationMs;
  }

  close(): void {
    // An in-flight poll is left to finish on its own; the loop exits before
    // dispatching anything it returns, and the request settles within one
    // server response, so cancellation is not plumbed through HttpClient.
    this.closed = true;
  }

  private async pollLoop(): Promise<void> {
    while (!this.closed) {
      try {
        const raw = await firstValueFrom(
          this.deps.http.get<unknown[]>(`${this.url}?start=${this.received}`)
        );
        if (this.closed) {
          return;
        }
        if (raw && raw.length > 0) {
          this.received += raw.length;
          for (const item of raw) {
            const message = parseInboundMessage(item);
            if (message) {
              this.onMessage(message);
            } else {
              this.deps.logger.debug('Signaling', 'Dropping unrecognized message', item);
            }
          }
        }
        const active = !this.idle || Date.now() < this.activeUntil;
        await this.delay(active ? ACTIVE_POLL_MS : IDLE_POLL_MS);
      } catch (error: unknown) {
        this.deps.logger.warn('Signaling', 'Polling error', error);
        await this.delay(ERROR_RETRY_MS);
      }
    }
  }

  private delay(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
