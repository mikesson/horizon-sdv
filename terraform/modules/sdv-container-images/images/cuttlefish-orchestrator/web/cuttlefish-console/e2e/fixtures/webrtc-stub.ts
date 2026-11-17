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
 * Browser-side WebRTC stub + signaling mocks for device-player e2e.
 *
 * Real media cannot flow in CI, so the boundary is faked one level down:
 * `RTCPeerConnection` is replaced with a scripted double that walks the
 * Cuttlefish handshake (answer set -> channels open -> `device-control`
 * arrives -> connected -> a `display_0` video track with a real, empty
 * `MediaStream`), and the signaling WebSocket is refused so the stack takes
 * the HTTP long-poll path, which page.route can mock deterministically.
 */

// biome-ignore-all lint/suspicious/noEmptyBlockStatements: a double implements the surface the stack calls and does nothing with most of it; an empty body is the stub.

import type {Page, Route} from '@playwright/test';

/** Signaling traffic captured for assertions. */
export interface StreamingMockState {
  /** Number of signaling connections opened (one per negotiation). */
  connects: number;
  /**
   * The instance each connection named, in order. Both spellings of the route
   * answer with success, so which Android VM was negotiated with is only
   * visible in the path the client asked for.
   */
  connectedInstances: string[];
  /** Messages the client forwarded to the device, in order. */
  forwards: Array<Record<string, unknown>>;
}

export async function installWebRTCStub(page: Page): Promise<void> {
  await page.addInitScript(() => {
    // Refuse the device sockets (other sockets, e.g. Vite HMR, pass through).
    // Signaling then falls back to mockable HTTP long-polling; ADB has no
    // fallback and simply reports a failed session, which is what these tests
    // want, because none of them drives a device over ADB.
    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        const target = String(url);
        if (target.includes('/signaling/ws')) {
          throw new Error('signaling WebSocket disabled in e2e');
        }
        if (/\/v2\/devices\/[^/]+\/adb$/.test(target)) {
          throw new Error('ADB WebSocket disabled in e2e');
        }
        super(url, protocols);
      }
    };

    interface StubChannel {
      label: string;
      readyState: string;
      binaryType: string;
      bufferedAmount: number;
      onopen: (() => void) | null;
      onclose: (() => void) | null;
      onerror: ((e: unknown) => void) | null;
      onmessage: ((e: unknown) => void) | null;
      send: () => void;
      close: () => void;
      addEventListener: () => void;
      removeEventListener: () => void;
    }

    const makeChannel = (label: string): StubChannel => ({
      label,
      readyState: 'connecting',
      binaryType: '',
      bufferedAmount: 0,
      onopen: null,
      onclose: null,
      onerror: null,
      onmessage: null,
      send: () => {},
      close: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
    });

    class StubRTCPeerConnection {
      connectionState = 'new';
      iceConnectionState = 'new';
      iceGatheringState = 'new';
      remoteDescription: unknown = null;
      localDescription: unknown = null;

      onicecandidate: ((e: unknown) => void) | null = null;
      onicecandidateerror: ((e: unknown) => void) | null = null;
      onicegatheringstatechange: (() => void) | null = null;
      onconnectionstatechange: (() => void) | null = null;
      oniceconnectionstatechange: (() => void) | null = null;
      ontrack: ((e: unknown) => void) | null = null;
      ondatachannel: ((e: {channel: StubChannel}) => void) | null = null;

      private readonly config: unknown;
      private readonly channels: StubChannel[] = [];

      constructor(config: unknown) {
        this.config = config;
      }

      createDataChannel(label: string): StubChannel {
        const channel = makeChannel(label);
        this.channels.push(channel);
        return channel;
      }

      // Promise-returning rather than async: the stub mirrors
      // RTCPeerConnection's signatures and there is nothing here to await.
      setRemoteDescription(desc: unknown): Promise<void> {
        this.remoteDescription = desc;
        return Promise.resolve();
      }

      createAnswer(): Promise<unknown> {
        return Promise.resolve({type: 'answer', sdp: 'e2e-answer-sdp'});
      }

      setLocalDescription(desc: unknown): Promise<void> {
        this.localDescription = desc;
        // Answer is in place, so bring the fake device side up shortly after.
        setTimeout(() => this.goLive(), 50);
        return Promise.resolve();
      }

      addIceCandidate(): Promise<void> {
        return Promise.resolve();
      }
      getStats(): Promise<Map<string, unknown>> {
        return Promise.resolve(new Map<string, unknown>());
      }
      getConfiguration(): unknown {
        return this.config;
      }
      setConfiguration(): void {}
      getReceivers(): unknown[] {
        return [];
      }
      restartIce(): void {}
      close(): void {
        this.connectionState = 'closed';
      }

      private goLive(): void {
        if (this.connectionState === 'closed') {
          return;
        }
        const input = this.channels.find(c => c.label === 'input-channel');
        if (input) {
          input.readyState = 'open';
          input.onopen?.();
        }
        const control = makeChannel('device-control');
        this.ondatachannel?.({channel: control});
        control.readyState = 'open';
        control.onopen?.();

        this.connectionState = 'connected';
        this.onconnectionstatechange?.();

        this.ontrack?.({
          track: {
            kind: 'video',
            label: 'display_0',
            id: 'e2e-track-0',
            readyState: 'live',
            onended: null,
          },
          streams: [new MediaStream()],
        });
      }
    }

    window.RTCPeerConnection = StubRTCPeerConnection as unknown as typeof RTCPeerConnection;
  });
}

const json = (route: Route, body: unknown): Promise<void> =>
  route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify(body)});

/**
 * The two legs are disjoint on purpose: each end of a connection reaches the
 * relay at a different address, so a test that would pass with them swapped
 * proves nothing.
 */
const E2E_CLIENT_SERVERS = [
  {urls: ['stun:stun.e2e.example:19302']},
  {urls: ['turns:relay.e2e.example:443'], username: 'e2e-user', credential: 'e2e-secret'},
];
export const E2E_DEVICE_SERVERS = [
  {urls: ['turn:10.0.0.7:3478'], username: 'e2e-user', credential: 'e2e-secret'},
];

/**
 * Mocks the signaling + ICE endpoints. Registered after the base API mocks,
 * so these win route priority. The first poll of each negotiation
 * (`?start=0`) delivers the device's offer; later polls are empty.
 */
export async function installStreamingMocks(page: Page): Promise<StreamingMockState> {
  const state: StreamingMockState = {connects: 0, connectedInstances: [], forwards: []};

  await page.route(/\/v2\/devices\/[^/]+\/ice-config(\?|$)/, route =>
    json(route, {
      client_servers: E2E_CLIENT_SERVERS,
      device_servers: E2E_DEVICE_SERVERS,
      ice_transport_policy: 'all',
    })
  );

  await page.route(/\/v2\/devices\/[^/]+\/instances\/([^/]+)\/signaling\/connect$/, route => {
    state.connects++;
    const instance = /\/instances\/([^/]+)\/signaling\/connect$/.exec(
      new URL(route.request().url()).pathname
    )?.[1];
    state.connectedInstances.push(decodeURIComponent(instance ?? ''));
    return json(route, {connection_id: 'e2e-conn', device_info: {}});
  });

  await page.route(/\/v2\/devices\/[^/]+\/signaling\/e2e-conn/, route => {
    const request = route.request();
    if (request.method() === 'POST') {
      state.forwards.push(request.postDataJSON() as Record<string, unknown>);
      return json(route, {});
    }
    const start = new URL(request.url()).searchParams.get('start');
    const messages =
      start === '0'
        ? [{message_type: 'device_msg', payload: {type: 'offer', sdp: 'e2e-offer-sdp'}}]
        : [];
    return json(route, messages);
  });

  return state;
}
