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
 * Wire-protocol contract tests for the device streaming stack.
 *
 * These tests pin the Cuttlefish signaling sequence (endpoint URLs, message
 * ordering, and both asymmetric ICE wire formats) so the streaming internals
 * can be restructured without drifting behavior that only a live Cuttlefish
 * device would otherwise catch. Fakes sit at the true process boundaries
 * (HttpTestingController, global WebSocket, global RTCPeerConnection);
 * everything in between is the real stack.
 *
 * Invariants pinned here (must match upstream cuttlefish webrtc client):
 * - TURN credentials fetched before signaling; relay-only transport policy.
 * - WebSocket-first signaling with HTTP long-poll fallback.
 * - Nine client data channels created in a fixed order.
 * - The device is the offerer: request-offer (carrying ice_servers with
 *   array-wrapped urls) -> offer -> answer.
 * - Outbound ICE candidates use the nested format {candidate: {candidate,
 *   sdpMid, sdpMLineIndex}}; inbound use the flat {candidate, mid, mLineIndex}.
 * - Poll requests carry a cumulative ?start=N message counter.
 * - Renegotiation offers received after the answer are answered in place.
 * - ICE failure triggers restartIce plus a fresh request-offer.
 */

import {provideHttpClient} from '@angular/common/http';
import type {TestRequest} from '@angular/common/http/testing';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {DeviceStreamService} from './streaming/device-stream.service';

const DEVICE_ID = 'dev-1';
const TARGET = {deviceId: DEVICE_ID, instance: 'inst'};
const ICE_URL = `/v2/devices/${DEVICE_ID}/ice-config`;
// Opening a channel names the instance; the message routes stay device-scoped
// because the operator's connection id is the binding once connect has run.
const SIGNALING_OPEN = `/v2/devices/${DEVICE_ID}/instances/${TARGET.instance}/signaling`;
const SIGNALING_BASE = `/v2/devices/${DEVICE_ID}/signaling`;

/**
 * The two legs are disjoint on purpose. Each end of a connection reaches the
 * relay at a different address, so a test that would pass with the lists
 * swapped is testing nothing.
 */
const CLIENT_SERVERS = [
  {urls: ['stun:stun.l.google.com:19302']},
  {
    urls: ['turns:relay.example.com:443'],
    username: 'turn-user',
    credential: 'turn-secret',
  },
];
const DEVICE_SERVERS = [
  {urls: ['turn:10.0.0.7:3478'], username: 'turn-user', credential: 'turn-secret'},
];
const ICE_CONFIG = {
  client_servers: CLIENT_SERVERS,
  device_servers: DEVICE_SERVERS,
  ice_transport_policy: 'all',
};

/** The exact channel set and order the upstream Cuttlefish client creates. */
const EXPECTED_CHANNEL_ORDER = [
  'input-channel',
  'sensors-channel',
  'adb-channel',
  'bluetooth-channel',
  'location-channel',
  'kml-locations-channel',
  'gpx-locations-channel',
  'lights-channel',
  'camera-data-channel',
];

interface FakeChannel {
  label: string;
  readyState: string;
  binaryType: string;
  send: ReturnType<typeof vi.fn>;
  close: ReturnType<typeof vi.fn>;
  onopen: (() => void) | null;
  onclose: (() => void) | null;
  onerror: ((e: unknown) => void) | null;
  onmessage: ((e: {data: unknown}) => void) | null;
}

function fakeChannel(label: string): FakeChannel {
  return {
    label,
    readyState: 'connecting',
    binaryType: '',
    send: vi.fn(),
    close: vi.fn(),
    onopen: null,
    onclose: null,
    onerror: null,
    onmessage: null,
  };
}

class FakeRTCPeerConnection {
  static instances: FakeRTCPeerConnection[] = [];

  readonly config: RTCConfiguration;
  readonly channels: FakeChannel[] = [];

  connectionState: RTCPeerConnectionState = 'new';
  iceConnectionState: RTCIceConnectionState = 'new';
  iceGatheringState: RTCIceGatheringState = 'new';
  remoteDescription: RTCSessionDescriptionInit | null = null;
  localDescription: RTCSessionDescriptionInit | null = null;

  onicecandidate: ((event: {candidate: unknown}) => void) | null = null;
  onicecandidateerror: ((event: unknown) => void) | null = null;
  onicegatheringstatechange: (() => void) | null = null;
  onconnectionstatechange: (() => void) | null = null;
  oniceconnectionstatechange: (() => void) | null = null;
  ontrack: ((event: unknown) => void) | null = null;
  ondatachannel: ((event: {channel: FakeChannel}) => void) | null = null;

  readonly setRemoteDescription = vi.fn((desc: RTCSessionDescriptionInit) => {
    this.remoteDescription = desc;
    return Promise.resolve();
  });
  readonly createAnswer = vi.fn(() =>
    Promise.resolve({type: 'answer' as const, sdp: 'ANSWER_SDP'})
  );
  readonly setLocalDescription = vi.fn((desc: RTCSessionDescriptionInit) => {
    this.localDescription = desc;
    return Promise.resolve();
  });
  readonly addIceCandidate = vi.fn(() => Promise.resolve());
  readonly getStats = vi.fn(() => Promise.resolve(new Map()));
  readonly restartIce = vi.fn();
  readonly setConfiguration = vi.fn();
  readonly close = vi.fn(() => {
    this.connectionState = 'closed';
  });

  constructor(config: RTCConfiguration) {
    this.config = config;
    FakeRTCPeerConnection.instances.push(this);
  }

  createDataChannel(label: string): FakeChannel {
    const channel = fakeChannel(label);
    this.channels.push(channel);
    return channel;
  }

  getConfiguration(): RTCConfiguration {
    return this.config;
  }

  getReceivers(): unknown[] {
    return [];
  }

  fireConnectionState(state: RTCPeerConnectionState): void {
    this.connectionState = state;
    this.onconnectionstatechange?.();
  }
}

/** Captures the RTCIceCandidateInit the stack constructs from inbound wire messages. */
class FakeRTCIceCandidate {
  static lastInit: RTCIceCandidateInit | null = null;

  constructor(init: RTCIceCandidateInit) {
    FakeRTCIceCandidate.lastInit = init;
    Object.assign(this, init);
  }
}

/** WebSocket whose construction fails, forcing the HTTP long-poll fallback. */
class RefusingWebSocket {
  static readonly OPEN = 1;
  constructor() {
    throw new Error('WebSocket disabled in this test');
  }
}

/** Controllable WebSocket for driving the WS signaling path. */
class FakeWebSocket {
  static readonly OPEN = 1;
  static instances: FakeWebSocket[] = [];

  readonly url: string;
  readonly sent: string[] = [];
  readyState = 0;

  onopen: (() => void) | null = null;
  onmessage: ((event: {data: string}) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  onclose: ((event: {code: number; reason: string}) => void) | null = null;

  readonly close = vi.fn((_code?: number, _reason?: string) => {
    this.readyState = 3;
  });

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  open(): void {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  deliver(message: unknown): void {
    this.onmessage?.({data: JSON.stringify(message)});
  }
}

describe('streaming wire protocol', () => {
  let httpMock: HttpTestingController;
  let service: DeviceStreamService;

  beforeEach(() => {
    FakeRTCPeerConnection.instances = [];
    FakeWebSocket.instances = [];
    FakeRTCIceCandidate.lastInit = null;
    vi.stubGlobal('RTCPeerConnection', FakeRTCPeerConnection);
    vi.stubGlobal('RTCIceCandidate', FakeRTCIceCandidate);

    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    httpMock = TestBed.inject(HttpTestingController);
    service = TestBed.inject(DeviceStreamService);
  });

  afterEach(() => {
    service.disconnect(TARGET);
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  /**
   * The single line that touches the connect API. Kept isolated so the wire
   * assertions survive API-shape changes during streaming refactors.
   */
  function driveConnect(): void {
    service.connect(TARGET);
  }

  function sessionState(): string | undefined {
    return service.session(TARGET)()?.state();
  }

  /** Waits for the next HTTP request matching the predicate and consumes it. */
  function nextRequest(
    predicate: (method: string, url: string) => boolean,
    label: string
  ): Promise<TestRequest> {
    return vi.waitFor(
      () => httpMock.expectOne(req => predicate(req.method, req.urlWithParams), label),
      {timeout: 3000, interval: 10}
    );
  }

  async function flushIceConfig(config: object = ICE_CONFIG): Promise<void> {
    const req = await nextRequest((m, u) => m === 'GET' && u === ICE_URL, 'ICE config');
    req.flush(config);
  }

  describe('HTTP long-poll transport', () => {
    beforeEach(() => {
      vi.stubGlobal('WebSocket', RefusingWebSocket);
    });

    /** Drives the flow up to (and including) the answer; returns the fake pc. */
    async function establishToAnswer(): Promise<FakeRTCPeerConnection> {
      driveConnect();
      await flushIceConfig();

      const connect = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_OPEN}/connect`,
        'signaling connect'
      );
      expect(connect.request.body).toEqual({});
      connect.flush({connection_id: 'conn-1', device_info: {}});

      const requestOffer = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_BASE}/conn-1`,
        'request-offer'
      );
      // The DEVICE leg goes to the device, and not the list this end configured.
      expect(requestOffer.request.body).toEqual({
        type: 'request-offer',
        ice_servers: DEVICE_SERVERS,
      });
      requestOffer.flush(null);

      const offerPoll = await nextRequest(
        (m, u) => m === 'GET' && u === `${SIGNALING_BASE}/conn-1?start=0`,
        'offer poll'
      );
      offerPoll.flush([{message_type: 'device_msg', payload: {type: 'offer', sdp: 'OFFER_SDP'}}]);

      const pc = FakeRTCPeerConnection.instances[0];
      await vi.waitFor(() => expect(pc.setRemoteDescription).toHaveBeenCalled());
      expect(pc.setRemoteDescription).toHaveBeenCalledWith({type: 'offer', sdp: 'OFFER_SDP'});

      const answer = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_BASE}/conn-1`,
        'answer'
      );
      expect(answer.request.body).toEqual({type: 'answer', sdp: 'ANSWER_SDP'});
      answer.flush(null);

      return pc;
    }

    it('falls back to HTTP signaling when the WebSocket cannot be established', async () => {
      driveConnect();
      await flushIceConfig();
      const connect = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_OPEN}/connect`,
        'fallback connect'
      );
      expect(connect.request.method).toBe('POST');
    });

    it('creates a default-transport peer connection and the upstream channel set', async () => {
      await establishToAnswer();

      const pc = FakeRTCPeerConnection.instances[0];
      // This end takes the CLIENT leg and the deployment's own policy.
      expect(pc.config.iceServers).toEqual(CLIENT_SERVERS);
      expect(pc.config.iceTransportPolicy).toBe('all');
      expect(pc.config.iceCandidatePoolSize).toBe(10);
      expect(pc.channels.map(c => c.label)).toEqual(EXPECTED_CHANNEL_ORDER);
    });

    // Where media is relayed, the deployment pins this end, and a pinned end
    // must not pre-gather. Every pooled session would allocate on every TURN
    // server, for candidates it will never use.
    it('pins transport and drops the candidate pool under a relay policy', async () => {
      driveConnect();
      await flushIceConfig({...ICE_CONFIG, ice_transport_policy: 'relay'});

      const connect = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_OPEN}/connect`,
        'relay connect'
      );
      connect.flush({connection_id: 'conn-1', device_info: {}});

      const pc = await vi.waitFor(() => {
        const instance = FakeRTCPeerConnection.instances[0];
        expect(instance).toBeDefined();
        return instance;
      });
      expect(pc.config.iceTransportPolicy).toBe('relay');
      expect(pc.config.iceCandidatePoolSize).toBe(0);
    });

    // A deployment with no relay answers with STUN alone rather than failing,
    // so the endpoint has no "TURN is missing" error to degrade from. The
    // 2026-07-08 black-screen failure cannot occur on this path.
    it('negotiates on a STUN-only configuration', async () => {
      driveConnect();
      const stunOnly = {
        client_servers: [{urls: ['stun:stun.l.google.com:19302']}],
        device_servers: [{urls: ['stun:stun.l.google.com:19302']}],
        ice_transport_policy: 'all',
      };
      await flushIceConfig(stunOnly);

      const connect = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_OPEN}/connect`,
        'STUN-only connect'
      );
      connect.flush({connection_id: 'conn-1', device_info: {}});

      const requestOffer = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_BASE}/conn-1`,
        'STUN-only request-offer'
      );
      expect(requestOffer.request.body).toEqual({
        type: 'request-offer',
        ice_servers: stunOnly.device_servers,
      });
      requestOffer.flush(null);
    });

    // The endpoint that answers this is the one that serves signaling, so a
    // failure here means no session at all, and not a degraded one. Say so
    // rather than black-screening on a connection that cannot form.
    it('fails the session when the ICE configuration cannot be fetched', async () => {
      driveConnect();
      const req = await nextRequest((m, u) => m === 'GET' && u === ICE_URL, 'ICE failure');
      req.flush({title: 'boom'}, {status: 500, statusText: 'Internal Server Error'});

      await vi.waitFor(() => expect(sessionState()).toBe('failed'));
      expect(service.session(TARGET)()?.error()).toBeTruthy();
    });

    it('applies inbound flat-format ICE candidates and advances ?start', async () => {
      const pc = await establishToAnswer();

      const icePoll = await nextRequest(
        (m, u) => m === 'GET' && u === `${SIGNALING_BASE}/conn-1?start=1`,
        'ICE poll'
      );
      icePoll.flush([
        {
          message_type: 'device_msg',
          payload: {
            type: 'ice-candidate',
            candidate: 'candidate:remote 1 udp 1 10.0.0.1 3478 typ relay',
            mLineIndex: 0,
            mid: '0',
          },
        },
      ]);

      await vi.waitFor(() => expect(pc.addIceCandidate).toHaveBeenCalled());
      expect(FakeRTCIceCandidate.lastInit).toEqual({
        candidate: 'candidate:remote 1 udp 1 10.0.0.1 3478 typ relay',
        sdpMLineIndex: 0,
        sdpMid: '0',
      });

      // The counter must be cumulative: the offer plus one candidate seen.
      const nextPoll = await nextRequest(
        (m, u) => m === 'GET' && u === `${SIGNALING_BASE}/conn-1?start=2`,
        'poll counter'
      );
      nextPoll.flush([]);
    });

    it('sends local ICE candidates in the nested wire format', async () => {
      const pc = await establishToAnswer();

      pc.onicecandidate?.({
        candidate: {
          candidate: 'candidate:local 1 udp 2 10.0.0.2 51000 typ relay',
          sdpMid: '0',
          sdpMLineIndex: 0,
          type: 'relay',
        },
      });

      const candidatePost = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_BASE}/conn-1`,
        'local candidate'
      );
      expect(candidatePost.request.body).toEqual({
        type: 'ice-candidate',
        candidate: {
          candidate: 'candidate:local 1 udp 2 10.0.0.2 51000 typ relay',
          sdpMid: '0',
          sdpMLineIndex: 0,
        },
      });
      candidatePost.flush(null);
    });

    it('never sends candidates the device parser rejects', async () => {
      const pc = await establishToAnswer();

      // Firefox emits an empty candidate near end-of-gathering; the device
      // CF_EXPECTs candidate, sdpMid, and sdpMLineIndex, so empty/null
      // variants must be filtered client-side.
      pc.onicecandidate?.({candidate: {candidate: '', sdpMid: '0', sdpMLineIndex: 0}});
      pc.onicecandidate?.({candidate: {candidate: 'candidate:x', sdpMid: null, sdpMLineIndex: 0}});
      pc.onicecandidate?.({
        candidate: {candidate: 'candidate:valid', sdpMid: '0', sdpMLineIndex: 0},
      });

      const candidatePost = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_BASE}/conn-1`,
        'first surviving candidate'
      );
      expect(candidatePost.request.body).toEqual({
        type: 'ice-candidate',
        candidate: {candidate: 'candidate:valid', sdpMid: '0', sdpMLineIndex: 0},
      });
      candidatePost.flush(null);
    });

    it('surfaces rotation and boot events from the device-control channel', async () => {
      const pc = await establishToAnswer();
      const session = service.session(TARGET)();

      // device-control is created device-side and arrives via ondatachannel.
      const controlChannel = fakeChannel('device-control');
      pc.ondatachannel?.({channel: controlChannel});

      expect(session?.rotation()).toBe(0);
      expect(session?.bootCount()).toBe(0);

      controlChannel.onmessage?.({
        data: JSON.stringify({
          event: 'VIRTUAL_DEVICE_SCREEN_CHANGED',
          metadata: {rotation: 90},
        }),
      });
      expect(session?.rotation()).toBe(90);

      controlChannel.onmessage?.({data: JSON.stringify({event: 'VIRTUAL_DEVICE_BOOT_STARTED'})});
      expect(session?.bootCount()).toBe(1);

      // Malformed frames are dropped without disturbing state.
      controlChannel.onmessage?.({data: 'not json'});
      expect(session?.rotation()).toBe(90);
    });

    it('answers renegotiation offers received after connection', async () => {
      const pc = await establishToAnswer();

      const icePoll = await nextRequest(
        (m, u) => m === 'GET' && u === `${SIGNALING_BASE}/conn-1?start=1`,
        'renegotiation poll'
      );
      icePoll.flush([
        {message_type: 'device_msg', payload: {type: 'offer', sdp: 'RENEGOTIATION_SDP'}},
      ]);

      await vi.waitFor(() => expect(pc.setRemoteDescription).toHaveBeenCalledTimes(2));
      expect(pc.setRemoteDescription).toHaveBeenLastCalledWith({
        type: 'offer',
        sdp: 'RENEGOTIATION_SDP',
      });

      const answer = await nextRequest(
        (m, u) => m === 'POST' && u === `${SIGNALING_BASE}/conn-1`,
        'renegotiation answer'
      );
      expect(answer.request.body).toEqual({type: 'answer', sdp: 'ANSWER_SDP'});
      answer.flush(null);
    });

    it('registers video tracks by display id parsed from the STREAM id (msid)', async () => {
      const pc = await establishToAnswer();

      // Upstream publishes each display as a stream named display_N; remote
      // track labels are not reliable across browsers and must not be used.
      const stream = {
        id: 'display_1',
        onremovetrack: null,
        getVideoTracks: () => [{readyState: 'live'}],
      };
      pc.ontrack?.({
        track: {kind: 'video', label: 'arbitrary-track-label', readyState: 'live', onended: null},
        streams: [stream],
      });

      const streams = service.session(TARGET)()?.streams();
      expect(streams?.get(1)).toBe(stream as unknown as MediaStream);
    });

    it('treats ICE failure as fatal and rebuilds from scratch, never restarting in-session', async () => {
      const pc = await establishToAnswer();
      pc.fireConnectionState('connected');

      pc.iceConnectionState = 'failed';
      pc.oniceconnectionstatechange?.();

      // request-offer makes the DEVICE rebuild its peer connection (new
      // DTLS/ICE), so an in-session restartIce + re-request can never
      // succeed. The only valid recovery is a fresh connection, which
      // fetches a fresh ICE configuration first.
      expect(pc.restartIce).not.toHaveBeenCalled();
      expect(sessionState()).toBe('reconnecting');

      await flushIceConfig();
    });

    it('transitions to reconnecting when the connection drops', async () => {
      const pc = await establishToAnswer();

      pc.fireConnectionState('connected');
      expect(sessionState()).toBe('connected');

      pc.fireConnectionState('failed');
      expect(sessionState()).toBe('reconnecting');
    });

    it('keeps retrying when setup fails during a reconnect cycle', async () => {
      const pc = await establishToAnswer();
      pc.fireConnectionState('connected');
      pc.fireConnectionState('failed');
      expect(sessionState()).toBe('reconnecting');

      // A setup failure mid-reconnect burns a backoff attempt instead of
      // failing the session. Only initial connects fail fast.
      const retryIce = await nextRequest((m, u) => m === 'GET' && u === ICE_URL, 'reconnect ICE');
      retryIce.flush({title: 'blip'}, {status: 500, statusText: 'Internal Server Error'});

      await vi.waitFor(() => expect(sessionState()).toBe('reconnecting'));
      expect(service.session(TARGET)()?.error()).toBeNull();
    });

    it('closes the peer connection and forgets the session on disconnect', async () => {
      const pc = await establishToAnswer();
      pc.fireConnectionState('connected');

      service.disconnect(TARGET);

      expect(pc.close).toHaveBeenCalled();
      expect(service.session(TARGET)()).toBeNull();
    });
  });

  describe('WebSocket transport', () => {
    beforeEach(() => {
      vi.stubGlobal('WebSocket', FakeWebSocket);
    });

    function sentFrames(ws: FakeWebSocket): Array<Record<string, unknown>> {
      return ws.sent.map(f => JSON.parse(f) as Record<string, unknown>);
    }

    /**
     * Drives the WS flow through the device_info handshake and the client's
     * request-offer (the device never offers before it); returns the socket.
     */
    async function openSocket(): Promise<FakeWebSocket> {
      driveConnect();
      await flushIceConfig();

      const ws = await vi.waitFor(() => {
        const instance = FakeWebSocket.instances[0];
        expect(instance).toBeDefined();
        return instance;
      });
      expect(ws.url.endsWith(`${SIGNALING_OPEN}/ws`)).toBe(true);

      ws.open();
      ws.deliver({device_info: {name: 'cvd-1'}});
      await vi.waitFor(() => expect(ws.sent.length).toBeGreaterThanOrEqual(1));
      return ws;
    }

    it('performs the full negotiation over the socket with correct formats', async () => {
      const ws = await openSocket();

      // request-offer must go out over the socket with array-wrapped urls.
      expect(sentFrames(ws)[0]).toEqual({
        type: 'request-offer',
        ice_servers: DEVICE_SERVERS,
      });

      // Offer arrives payload-wrapped; the stack answers over the socket.
      ws.deliver({payload: {type: 'offer', sdp: 'OFFER_SDP'}});
      const pc = FakeRTCPeerConnection.instances[0];
      await vi.waitFor(() => expect(pc.setRemoteDescription).toHaveBeenCalled());
      await vi.waitFor(() =>
        expect(sentFrames(ws)).toContainEqual({type: 'answer', sdp: 'ANSWER_SDP'})
      );

      // Inbound flat-format candidate is applied.
      ws.deliver({
        payload: {
          type: 'ice-candidate',
          candidate: 'candidate:ws-remote 1 udp 1 10.0.0.3 3478 typ relay',
          mLineIndex: 0,
          mid: '0',
        },
      });
      await vi.waitFor(() => expect(pc.addIceCandidate).toHaveBeenCalled());
      expect(FakeRTCIceCandidate.lastInit).toMatchObject({
        candidate: 'candidate:ws-remote 1 udp 1 10.0.0.3 3478 typ relay',
        sdpMLineIndex: 0,
        sdpMid: '0',
      });

      // Outbound candidate uses the nested format over the socket.
      pc.onicecandidate?.({
        candidate: {
          candidate: 'candidate:ws-local 1 udp 2 10.0.0.4 51000 typ relay',
          sdpMid: '0',
          sdpMLineIndex: 0,
          type: 'relay',
        },
      });
      await vi.waitFor(() =>
        expect(sentFrames(ws)).toContainEqual({
          type: 'ice-candidate',
          candidate: {
            candidate: 'candidate:ws-local 1 udp 2 10.0.0.4 51000 typ relay',
            sdpMid: '0',
            sdpMLineIndex: 0,
          },
        })
      );
    });

    it('closes the socket on disconnect', async () => {
      const ws = await openSocket();
      ws.deliver({payload: {type: 'offer', sdp: 'OFFER_SDP'}});
      await vi.waitFor(() =>
        expect(sentFrames(ws)).toContainEqual({type: 'answer', sdp: 'ANSWER_SDP'})
      );

      service.disconnect(TARGET);
      expect(ws.close).toHaveBeenCalled();
    });
  });
});
