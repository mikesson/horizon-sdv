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

import {z} from 'zod';
import type {IceServer} from '../../models/device.model';

/** Lifecycle state of a device streaming session. */
export type StreamState = 'connecting' | 'connected' | 'reconnecting' | 'failed' | 'disconnected';

/**
 * WebRTC connection quality metrics, sampled from
 * RTCPeerConnection.getStats() while a session is live.
 */
export interface ConnectivityMetrics {
  /** Round-trip time in milliseconds. */
  rtt: number;
  /** Jitter in milliseconds. */
  jitter: number;
  /** Total packets lost since connection start. */
  packetsLost: number;
  /** Packet loss as a percentage (0-100). */
  packetLossRate: number;
  /** Connection type: direct P2P or relayed through TURN. */
  protocol: 'direct' | 'turn-relay';
  /** Local ICE candidate type (host, srflx, relay). */
  localCandidate: string;
  /** Remote ICE candidate type. */
  remoteCandidate: string;
  /** Total bytes received. */
  bytesReceived: number;
  /** Total bytes sent. */
  bytesSent: number;
  /** Current video frame rate. */
  frameRate: number;
  /** Current video resolution. */
  resolution: {width: number; height: number};
  /** Current bitrate in kbps. */
  bitrate: number;
  /** Timestamp when metrics were collected. */
  timestamp: number;
}

// ---------------------------------------------------------------------------
// Signaling wire messages.
//
// The Cuttlefish webrtc_streamer uses asymmetric ICE formats: candidates
// arrive flat ({candidate: "...", mid, mLineIndex}) but must be sent nested
// ({candidate: {candidate, sdpMid, sdpMLineIndex}}). Inbound messages are
// Zod-validated at the transport boundary; unknown types are dropped there.
// ---------------------------------------------------------------------------

const OfferMessageSchema = z.object({
  type: z.literal('offer'),
  sdp: z.string(),
});

const InboundIceCandidateSchema = z.object({
  type: z.literal('ice-candidate'),
  // Normally a flat string; some senders nest an RTCIceCandidateInit.
  candidate: z.union([
    z.string(),
    z.object({
      candidate: z.string(),
      sdpMid: z.string().nullish(),
      sdpMLineIndex: z.number().nullish(),
    }),
  ]),
  mLineIndex: z.number().optional(),
  mid: z.string().optional(),
});

const ErrorMessageSchema = z.object({
  type: z.literal('error'),
  message: z.string().optional(),
  error: z.string().optional(),
});

const InboundSignalingMessageSchema = z.discriminatedUnion('type', [
  OfferMessageSchema,
  InboundIceCandidateSchema,
  ErrorMessageSchema,
]);

export type OfferMessage = z.infer<typeof OfferMessageSchema>;
export type InboundIceCandidateMessage = z.infer<typeof InboundIceCandidateSchema>;
export type InboundSignalingMessage = z.infer<typeof InboundSignalingMessageSchema>;

export type OutboundSignalingMessage =
  // The device leg, verbatim: the backend already answers in the shape the
  // device parses, so nothing maps it on the way out.
  | {type: 'request-offer'; ice_servers: IceServer[]}
  | {type: 'answer'; sdp: string}
  | {
      // The device requires all three fields; null mid/index must be
      // filtered before reaching the wire.
      type: 'ice-candidate';
      candidate: {candidate: string; sdpMid: string; sdpMLineIndex: number};
    };

/**
 * Unwraps a raw signaling payload and validates it into a typed message.
 *
 * Both transports deliver device messages wrapped as
 * {message_type: "device_msg", payload: {...}}; direct payloads are accepted
 * too. Returns null for unknown or malformed messages (callers log and drop).
 */
export function parseInboundMessage(raw: unknown): InboundSignalingMessage | null {
  let candidate = raw;
  if (candidate && typeof candidate === 'object' && 'payload' in candidate) {
    candidate = candidate.payload;
  }
  const result = InboundSignalingMessageSchema.safeParse(candidate);
  return result.success ? result.data : null;
}

// ---------------------------------------------------------------------------
// Device-control data channel messages (device -> client).
// ---------------------------------------------------------------------------

const DeviceControlMessageSchema = z.object({
  event: z.string(),
  // Known fields declared (dot access under noPropertyAccessFromIndexSignature);
  // other metadata keys pass through untyped.
  metadata: z.looseObject({rotation: z.number().optional()}).optional(),
});

export type DeviceControlMessage = z.infer<typeof DeviceControlMessageSchema>;

/** Parses a device-control channel frame; null for malformed/unknown shapes. */
export function parseControlMessage(raw: unknown): DeviceControlMessage | null {
  if (typeof raw !== 'string') {
    return null;
  }
  let json: unknown;
  try {
    json = JSON.parse(raw);
  } catch {
    return null;
  }
  const result = DeviceControlMessageSchema.safeParse(json);
  return result.success ? result.data : null;
}
