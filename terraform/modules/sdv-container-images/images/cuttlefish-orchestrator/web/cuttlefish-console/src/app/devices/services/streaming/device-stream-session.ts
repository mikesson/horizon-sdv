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
import type {Signal} from '@angular/core';
import {signal} from '@angular/core';
import {firstValueFrom} from 'rxjs';
import type {LoggingService} from '../../../shared/services';
import type {IceConfig} from '../../models/device.model';
import type {InstanceTarget} from '../../models/instance-target';
import type {DeviceApiService} from '../device-api.service';
import type {DeviceInputService} from '../input/device-input.service';
import type {SignalingTransport} from './signaling-transport';
import {openSignalingTransport} from './signaling-transport';
import {StatsSampler} from './stats-sampler';
import type {
  InboundIceCandidateMessage,
  InboundSignalingMessage,
  OfferMessage,
  StreamState,
} from './streaming.types';
import {parseControlMessage} from './streaming.types';

export interface DeviceStreamSessionDeps {
  api: DeviceApiService;
  http: HttpClient;
  input: DeviceInputService;
  logger: LoggingService;
}

const OFFER_TIMEOUT_MS = 30_000;
/** How long to keep the polling fallback at its active cadence when a
 * signaling message is known to be on its way. */
const EXPECT_SIGNALING_MS = 5000;
const MAX_RECONNECT_ATTEMPTS = 5;
const BASE_RECONNECT_DELAY_MS = 1000;
const MAX_RECONNECT_DELAY_MS = 30_000;
/**
 * Prospective m-sections the browser pre-gathers candidates for. Zero under a
 * relay policy: each pooled session allocates on every TURN server it is
 * given, so a pool is allocations a session will never use.
 */
const CANDIDATE_POOL_SIZE = 10;

/**
 * Cuttlefish publishes each display as a MediaStream whose id is
 * "display_0", "display_1", and so on. The stream id (msid) is transmitted in
 * SDP; remote track labels are not reliable across browsers.
 */
function parseDisplayId(streamId: string): number {
  const match = /display_(\d+)/i.exec(streamId);
  return match ? Number.parseInt(match[1], 10) : 0;
}

/**
 * Video sections the device marked inactive (or rejected with port 0) in a
 * renegotiation offer. This is how Cuttlefish signals a removed display.
 */
function parseInactiveDisplays(sdp: string): Set<number> {
  const inactive = new Set<number>();
  for (const section of sdp.split(/(?=m=)/)) {
    if (!section.startsWith('m=video')) {
      continue;
    }
    if (!(section.includes('a=inactive') || /^m=video\s+0\s+/.test(section))) {
      continue;
    }
    const displayId = /display_(\d+)/i.exec(section)?.[1];
    if (displayId) {
      inactive.add(Number.parseInt(displayId, 10));
    }
  }
  return inactive;
}

/**
 * One live streaming connection to a device.
 *
 * Owns the peer connection, signaling transport, data channels, and their
 * whole lifecycle (negotiation, renegotiation, reconnection backoff).
 * UI-facing state is exposed as read-only signals; the WebRTC machinery
 * itself never lives inside a signal.
 *
 * A credential outliving the session is not something this class arranges:
 * an expiry is baked into the TURN username, so renewing means a new
 * allocation, and re-gathering means rebuilding the connection. Expiry
 * arrives as an ICE failure and is handled as one, by reconnecting, which
 * fetches a fresh configuration.
 *
 * Created by {@link DeviceStreamService.connect}; connection starts
 * immediately. `dispose()` tears everything down.
 */
export class DeviceStreamSession {
  private readonly stateSignal = signal<StreamState>('connecting');
  private readonly streamsSignal = signal<ReadonlyMap<number, MediaStream>>(new Map());
  private readonly audioStreamSignal = signal<MediaStream | null>(null);
  private readonly errorSignal = signal<string | null>(null);
  private readonly rotationSignal = signal(0);
  private readonly bootCountSignal = signal(0);
  private readonly sampler = new StatsSampler();

  readonly state: Signal<StreamState> = this.stateSignal.asReadonly();
  /** Video streams keyed by display id. Retained across reconnects. */
  readonly streams: Signal<ReadonlyMap<number, MediaStream>> = this.streamsSignal.asReadonly();
  readonly audioStream: Signal<MediaStream | null> = this.audioStreamSignal.asReadonly();
  /** Failure description when state() is 'failed'. */
  readonly error: Signal<string | null> = this.errorSignal.asReadonly();
  /** Guest screen rotation in degrees, from VIRTUAL_DEVICE_SCREEN_CHANGED. */
  readonly rotation: Signal<number> = this.rotationSignal.asReadonly();
  /** Bumped on VIRTUAL_DEVICE_BOOT_STARTED, so consumers retry ADB on reboot. */
  readonly bootCount: Signal<number> = this.bootCountSignal.asReadonly();
  readonly metrics = this.sampler.metrics;

  private pc: RTCPeerConnection | null = null;
  private transport: SignalingTransport | null = null;
  /** Bumped on teardown so messages queued from a superseded transport are
   * ignored instead of acting on the reconnected peer connection. */
  private signalingGeneration = 0;
  private inputChannel: RTCDataChannel | null = null;
  private controlChannel: RTCDataChannel | null = null;
  private inputRegistered = false;
  private pendingCandidates: InboundIceCandidateMessage[] = [];
  private negotiated = false;
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private offerTimer: ReturnType<typeof setTimeout> | null = null;
  private disposed = false;

  constructor(
    readonly target: InstanceTarget,
    private readonly deps: DeviceStreamSessionDeps
  ) {
    void this.establish();
  }

  /**
   * Hints that signaling messages are expected shortly (a display change
   * triggers a device renegotiation offer), so the polling fallback picks
   * them up without the idle-cadence delay. Mirrors upstream
   * expectStreamChange/expectMessagesSoon.
   */
  expectSignalingSoon(): void {
    this.transport?.expectMessagesSoon(EXPECT_SIGNALING_MS);
  }

  /** Stops the session permanently and releases all resources. */
  dispose(): void {
    if (this.disposed) {
      return;
    }
    this.disposed = true;
    this.teardownConnection();
    this.stateSignal.set('disconnected');
  }

  private async establish(): Promise<void> {
    this.stateSignal.set('connecting');
    this.errorSignal.set(null);

    try {
      const ice = await firstValueFrom(this.deps.api.getIceConfig(this.target.deviceId));
      if (this.disposed) {
        return;
      }

      const generation = this.signalingGeneration;
      this.transport = await openSignalingTransport(
        this.target,
        message => {
          if (generation === this.signalingGeneration) {
            this.onSignalingMessage(message);
          }
        },
        {http: this.deps.http, logger: this.deps.logger}
      );
      if (this.disposed) {
        this.transport.close();
        this.transport = null;
        return;
      }

      this.pc = this.createPeerConnection(ice);
      this.armOfferTimeout();

      // Tells the device which ICE servers to use and prompts its offer, since
      // the device is the offerer in the Cuttlefish flow. It gets the DEVICE
      // leg: the addresses a host VM can route to, which are not the ones
      // this end just configured.
      await this.transport.send({
        type: 'request-offer',
        ice_servers: ice.deviceServers,
      });
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : 'Connection failed';
      if (this.reconnectAttempts > 0) {
        // Mid-reconnect setup failure (transient TURN/signaling error):
        // keep burning backoff attempts instead of surfacing failure early.
        this.deps.logger.warn('Streaming', 'Reconnect setup failed, retrying', {message});
        this.teardownConnection();
        this.scheduleReconnect();
      } else {
        this.fail(message);
      }
    }
  }

  private createPeerConnection(ice: IceConfig): RTCPeerConnection {
    // The policy is the deployment's, not this component's: 'relay' only where
    // device media never meets the internet at the host, so a direct pair
    // cannot form and gathering one would only delay the connection.
    const pc = new RTCPeerConnection({
      iceServers: ice.clientServers,
      iceTransportPolicy: ice.policy,
      iceCandidatePoolSize: ice.policy === 'relay' ? 0 : CANDIDATE_POOL_SIZE,
    });

    // Channel order must match the upstream Cuttlefish client.
    this.inputChannel = this.createChannel(pc, 'input-channel', {
      onOpen: () => this.registerInputWhenReady(),
    });
    this.createChannel(pc, 'sensors-channel');
    // Created but unused: ADB has its own WebSocket to the orchestrator, so
    // no media relay ever carries an install. The channel stays because the
    // device builds its side from this exact sequence.
    this.createChannel(pc, 'adb-channel');
    this.createChannel(pc, 'bluetooth-channel');
    this.createChannel(pc, 'location-channel');
    this.createChannel(pc, 'kml-locations-channel');
    this.createChannel(pc, 'gpx-locations-channel');
    this.createChannel(pc, 'lights-channel');
    this.createChannel(pc, 'camera-data-channel');

    pc.ondatachannel = event => {
      const channel = event.channel;
      if (channel.label !== 'device-control') {
        this.deps.logger.debug('Streaming', `Ignoring data channel: ${channel.label}`);
        return;
      }
      this.controlChannel = channel;
      channel.onopen = () => this.registerInputWhenReady();
      channel.onmessage = event => this.onControlMessage(event.data);
      channel.onerror = err =>
        this.deps.logger.error('Streaming', 'device-control channel error', err);
    };

    pc.onicecandidate = event => {
      if (event.candidate) {
        void this.sendLocalCandidate(event.candidate);
      }
    };

    pc.onicecandidateerror = event => {
      this.deps.logger.warn('Streaming', 'ICE candidate error', {
        errorCode: event.errorCode,
        errorText: event.errorText,
        url: event.url,
      });
    };

    pc.onconnectionstatechange = () => this.onConnectionStateChange(pc.connectionState);

    pc.oniceconnectionstatechange = () => {
      if (pc.iceConnectionState !== 'failed') {
        return;
      }
      // Common causes: firewall blocking ports, TURN unreachable, or
      // expired credentials. There is no in-session recovery: request-offer
      // makes the device REBUILD its peer connection (new DTLS/ICE), which
      // can never apply to this established one. Reconnect from scratch,
      // which sends request-offer on a fresh connection.
      this.deps.logger.warn('Streaming', 'ICE connection failed - reconnecting');
      this.scheduleReconnect();
    };

    pc.ontrack = event => this.onTrack(event);

    return pc;
  }

  private createChannel(
    pc: RTCPeerConnection,
    label: string,
    hooks?: {
      onOpen?: (channel: RTCDataChannel) => void;
      onClose?: (channel: RTCDataChannel) => void;
    }
  ): RTCDataChannel {
    const channel = pc.createDataChannel(label);
    channel.binaryType = 'arraybuffer';
    channel.onopen = () => {
      this.deps.logger.debug('Streaming', `Data channel opened: ${label}`);
      hooks?.onOpen?.(channel);
    };
    channel.onclose = () => {
      this.deps.logger.debug('Streaming', `Data channel closed: ${label}`);
      hooks?.onClose?.(channel);
    };
    channel.onerror = err =>
      this.deps.logger.error('Streaming', `Data channel error [${label}]`, err);
    return channel;
  }

  /**
   * Registers input forwarding once BOTH the client-created input channel and
   * the device-created control channel are open. Their opening order is not
   * guaranteed, so each open event calls back in here.
   */
  private registerInputWhenReady(): void {
    if (this.inputRegistered) {
      return;
    }
    if (this.inputChannel?.readyState !== 'open' || this.controlChannel?.readyState !== 'open') {
      return;
    }
    this.deps.input.registerSession(this.target.deviceId, {
      input: this.inputChannel,
      control: this.controlChannel,
    });
    this.inputRegistered = true;
    // Activate the primary display for input.
    this.deps.input.sendRefreshDisplay(this.target.deviceId, 0);
  }

  /** Device-initiated control events (rotation, boot, display power). */
  private onControlMessage(raw: unknown): void {
    const message = parseControlMessage(raw);
    if (!message) {
      this.deps.logger.debug('Streaming', 'Dropping unrecognized control message', raw);
      return;
    }
    switch (message.event) {
      case 'VIRTUAL_DEVICE_SCREEN_CHANGED': {
        const rotation = message.metadata?.rotation;
        if (rotation !== undefined) {
          this.rotationSignal.set(rotation);
        }
        break;
      }
      case 'VIRTUAL_DEVICE_BOOT_STARTED':
        this.bootCountSignal.update(count => count + 1);
        break;
      case 'VIRTUAL_DEVICE_DISPLAY_POWER_MODE_CHANGED':
        // A stream change (and possibly a renegotiation offer) follows.
        this.expectSignalingSoon();
        break;
      default:
        this.deps.logger.debug('Streaming', `Device control event: ${message.event}`);
        break;
    }
  }

  private onSignalingMessage(message: InboundSignalingMessage): void {
    switch (message.type) {
      case 'offer':
        void this.handleOffer(message);
        break;
      case 'ice-candidate':
        void this.handleRemoteCandidate(message);
        break;
      case 'error':
        this.fail(message.message ?? message.error ?? 'Signaling error from device');
        break;
      default:
        break;
    }
  }

  private async handleOffer(offer: OfferMessage): Promise<void> {
    const pc = this.pc;
    if (!pc) {
      return;
    }
    this.clearOfferTimer();
    const isRenegotiation = this.negotiated;
    const existingDisplays = new Set(this.streamsSignal().keys());

    try {
      await pc.setRemoteDescription({type: 'offer', sdp: offer.sdp});
      await this.drainPendingCandidates(pc);

      const answer = await pc.createAnswer();
      await pc.setLocalDescription(answer);
      await this.transport?.send({type: 'answer', sdp: answer.sdp ?? ''});

      if (isRenegotiation) {
        // Renegotiation happens when displays are added or removed.
        this.removeStaleDisplays(existingDisplays, offer.sdp);
      } else {
        this.negotiated = true;
        this.sampler.start(pc);
      }
    } catch (error: unknown) {
      if (isRenegotiation) {
        this.deps.logger.error('Streaming', 'Renegotiation failed', error);
      } else {
        this.fail(error instanceof Error ? error.message : 'Negotiation failed');
      }
    }
  }

  private async handleRemoteCandidate(message: InboundIceCandidateMessage): Promise<void> {
    const pc = this.pc;
    if (!pc) {
      return;
    }
    if (!pc.remoteDescription) {
      this.pendingCandidates.push(message);
      return;
    }
    await this.addRemoteCandidate(pc, message);
  }

  private async drainPendingCandidates(pc: RTCPeerConnection): Promise<void> {
    const queued = this.pendingCandidates;
    this.pendingCandidates = [];
    for (const message of queued) {
      await this.addRemoteCandidate(pc, message);
    }
  }

  private async addRemoteCandidate(
    pc: RTCPeerConnection,
    message: InboundIceCandidateMessage
  ): Promise<void> {
    const candidate =
      typeof message.candidate === 'string' ? message.candidate : message.candidate.candidate;
    try {
      await pc.addIceCandidate(
        new RTCIceCandidate({
          candidate,
          sdpMLineIndex: message.mLineIndex ?? null,
          sdpMid: message.mid ?? null,
        })
      );
    } catch (error: unknown) {
      this.deps.logger.warn('Streaming', 'Failed to add remote ICE candidate', {error, message});
    }
  }

  private async sendLocalCandidate(candidate: RTCIceCandidate): Promise<void> {
    // The device's parser hard-requires all three fields: Firefox emits an
    // empty candidate near end-of-gathering, and null mid/index cannot be
    // represented on the wire. Both would error the whole connection.
    if (!candidate.candidate || candidate.sdpMid === null || candidate.sdpMLineIndex === null) {
      return;
    }
    try {
      await this.transport?.send({
        type: 'ice-candidate',
        candidate: {
          candidate: candidate.candidate,
          sdpMid: candidate.sdpMid,
          sdpMLineIndex: candidate.sdpMLineIndex,
        },
      });
    } catch (error: unknown) {
      this.deps.logger.warn('Streaming', 'Failed to send ICE candidate', error);
    }
  }

  private onTrack(event: RTCTrackEvent): void {
    const stream = event.streams[0];
    if (!stream) {
      return;
    }
    if (event.track.kind === 'video') {
      const displayId = parseDisplayId(stream.id);
      this.streamsSignal.update(current => new Map(current).set(displayId, stream));
      // Stream-identity binding: a late ended event from a torn-down
      // connection's track must not remove the entry a reconnect re-added.
      event.track.onended = () => this.removeDisplay(displayId, stream);
      stream.onremovetrack = () => this.removeDisplay(displayId, stream);
    } else if (event.track.kind === 'audio') {
      this.audioStreamSignal.set(stream);
    }
  }

  /** Removes a display; with `expected` set, only while the map still holds
   * that exact stream (track-event callers pass the stream they belong to). */
  private removeDisplay(displayId: number, expected?: MediaStream): void {
    const current = this.streamsSignal();
    const existing = current.get(displayId);
    if (!existing || (expected !== undefined && existing !== expected)) {
      return;
    }
    const next = new Map(current);
    next.delete(displayId);
    this.streamsSignal.set(next);
  }

  /**
   * After renegotiation, drops displays whose streams no longer carry a live
   * video track or that the offer marked inactive. Judged from the display
   * map's own streams, because receiver track labels are not reliable
   * identifiers.
   */
  private removeStaleDisplays(existingDisplays: ReadonlySet<number>, sdp: string): void {
    const inactive = parseInactiveDisplays(sdp);
    const current = this.streamsSignal();
    for (const displayId of existingDisplays) {
      const stream = current.get(displayId);
      if (!stream) {
        continue;
      }
      const live = stream.getVideoTracks().some(track => track.readyState === 'live');
      if (!live || inactive.has(displayId)) {
        this.removeDisplay(displayId, stream);
      }
    }
  }

  private onConnectionStateChange(state: RTCPeerConnectionState): void {
    this.deps.logger.debug('Streaming', `PeerConnection state: ${state}`, {
      deviceId: this.target.deviceId,
    });
    switch (state) {
      case 'connected':
        this.stateSignal.set('connected');
        this.reconnectAttempts = 0;
        this.transport?.setIdle(true);
        break;
      case 'disconnected':
      case 'failed':
        this.scheduleReconnect();
        break;
      case 'closed':
        if (!this.disposed) {
          this.stateSignal.set('disconnected');
        }
        break;
      case 'connecting':
        this.stateSignal.set('connecting');
        break;
      default:
        break;
    }
  }

  private scheduleReconnect(): void {
    if (this.disposed || this.reconnectTimer) {
      return;
    }
    if (this.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      this.fail(
        `Connection lost after ${MAX_RECONNECT_ATTEMPTS} attempts. Please refresh the page.`
      );
      return;
    }

    this.stateSignal.set('reconnecting');
    const delay = Math.min(
      BASE_RECONNECT_DELAY_MS * 2 ** this.reconnectAttempts,
      MAX_RECONNECT_DELAY_MS
    );
    this.reconnectAttempts++;
    this.deps.logger.debug(
      'Streaming',
      `Reconnect attempt ${this.reconnectAttempts}/${MAX_RECONNECT_ATTEMPTS} in ${delay}ms`
    );

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (this.disposed || this.stateSignal() !== 'reconnecting') {
        return;
      }
      this.teardownConnection();
      void this.establish();
    }, delay);
  }

  private armOfferTimeout(): void {
    this.clearOfferTimer();
    this.offerTimer = setTimeout(() => {
      if (!this.negotiated) {
        this.fail('Did not receive offer from Cuttlefish');
      }
    }, OFFER_TIMEOUT_MS);
  }

  private fail(message: string): void {
    if (this.disposed) {
      return;
    }
    this.deps.logger.error('Streaming', 'Session failed', {
      deviceId: this.target.deviceId,
      message,
    });
    this.teardownConnection();
    this.errorSignal.set(message);
    this.stateSignal.set('failed');
  }

  private teardownConnection(): void {
    this.signalingGeneration++;
    this.clearOfferTimer();
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.sampler.stop();
    if (this.inputRegistered) {
      this.deps.input.unregisterSession(this.target.deviceId);
      this.inputRegistered = false;
    }
    this.transport?.close();
    this.transport = null;

    // Detach every handler that mutates session state before closing: a
    // late event from the old connection (browsers differ on close-event
    // delivery) must not clobber the state of a reconnected session.
    for (const channel of [this.inputChannel, this.controlChannel]) {
      if (channel) {
        channel.onopen = null;
        channel.onclose = null;
        channel.onerror = null;
        channel.onmessage = null;
      }
    }
    if (this.pc) {
      this.pc.ondatachannel = null;
      this.pc.ontrack = null;
      this.pc.onicecandidate = null;
      this.pc.onicecandidateerror = null;
      this.pc.onconnectionstatechange = null;
      this.pc.oniceconnectionstatechange = null;
      this.pc.close();
      this.pc = null;
    }
    this.inputChannel = null;
    this.controlChannel = null;
    this.pendingCandidates = [];
    this.negotiated = false;
  }

  private clearOfferTimer(): void {
    if (this.offerTimer) {
      clearTimeout(this.offerTimer);
      this.offerTimer = null;
    }
  }
}
