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

import type {Signal} from '@angular/core';
import {signal} from '@angular/core';
import type {ConnectivityMetrics} from './streaming.types';

const SAMPLE_INTERVAL_MS = 1000;

interface CandidatePairStats extends RTCStats {
  type: 'candidate-pair';
  state?: RTCStatsIceCandidatePairState;
  currentRoundTripTime?: number;
  localCandidateId?: string;
  remoteCandidateId?: string;
}

interface IceCandidateStats extends RTCStats {
  id: string;
  candidateType?: RTCIceCandidateType;
}

interface InboundRtpStats extends RTCStats {
  type: 'inbound-rtp';
  kind?: string;
  jitter?: number;
  packetsLost?: number;
  packetsReceived?: number;
  bytesReceived?: number;
  framesPerSecond?: number;
  frameWidth?: number;
  frameHeight?: number;
}

interface OutboundRtpStats extends RTCStats {
  type: 'outbound-rtp';
  bytesSent?: number;
}

function isCandidatePairStats(report: RTCStats): report is CandidatePairStats {
  return report.type === 'candidate-pair';
}

function candidateType(stats: RTCStatsReport, id: string | undefined): string {
  const candidate = id ? (stats.get(id) as IceCandidateStats | undefined) : undefined;
  return candidate?.candidateType ?? '';
}

/**
 * Multi-display sessions carry one video inbound-rtp report per display:
 * the totals sum across them, while jitter/fps/resolution follow the
 * largest stream.
 */
interface VideoAggregate {
  jitter: number;
  packetsLost: number;
  packetsReceived: number;
  bytesReceived: number;
  frameRate: number;
  resolution: {width: number; height: number};
  primaryArea: number;
}

function newVideoAggregate(): VideoAggregate {
  return {
    jitter: 0,
    packetsLost: 0,
    packetsReceived: 0,
    bytesReceived: 0,
    frameRate: 0,
    resolution: {width: 0, height: 0},
    primaryArea: -1,
  };
}

function accumulateVideo(totals: VideoAggregate, report: InboundRtpStats): void {
  totals.packetsLost += report.packetsLost ?? 0;
  totals.packetsReceived += report.packetsReceived ?? 0;
  totals.bytesReceived += report.bytesReceived ?? 0;

  const area = (report.frameWidth ?? 0) * (report.frameHeight ?? 0);
  if (area > totals.primaryArea) {
    totals.primaryArea = area;
    totals.jitter = report.jitter ? report.jitter * 1000 : 0;
    totals.frameRate = report.framesPerSecond ?? 0;
    if (report.frameWidth && report.frameHeight) {
      totals.resolution = {width: report.frameWidth, height: report.frameHeight};
    }
  }
}

function isInboundRtpStats(report: RTCStats): report is InboundRtpStats {
  return report.type === 'inbound-rtp';
}

function isOutboundRtpStats(report: RTCStats): report is OutboundRtpStats {
  return report.type === 'outbound-rtp';
}

/**
 * Samples RTCPeerConnection.getStats() into a reactive metrics signal.
 *
 * One sampler per streaming session; the owning session starts it once
 * negotiation completes and stops it on teardown.
 */
export class StatsSampler {
  private readonly metricsSignal = signal<ConnectivityMetrics | null>(null);
  readonly metrics: Signal<ConnectivityMetrics | null> = this.metricsSignal.asReadonly();

  private interval: ReturnType<typeof setInterval> | null = null;
  private lastBytesReceived = 0;
  private lastTimestamp = 0;

  start(pc: RTCPeerConnection): void {
    this.stop();
    this.lastBytesReceived = 0;
    this.lastTimestamp = Date.now();
    this.interval = setInterval(() => {
      void this.sample(pc);
    }, SAMPLE_INTERVAL_MS);
  }

  stop(): void {
    if (this.interval) {
      clearInterval(this.interval);
      this.interval = null;
    }
    this.metricsSignal.set(null);
  }

  private async sample(pc: RTCPeerConnection): Promise<void> {
    try {
      const stats = await pc.getStats();
      this.metricsSignal.set(this.parse(stats));
    } catch {
      // Stats are best-effort; a failed sample keeps the previous value.
    }
  }

  private parse(stats: RTCStatsReport): ConnectivityMetrics {
    let rtt = 0;
    let bytesSent = 0;
    let protocol: 'direct' | 'turn-relay' = 'direct';
    let localCandidate = '';
    let remoteCandidate = '';
    const video = newVideoAggregate();

    stats.forEach((report: RTCStats) => {
      if (isCandidatePairStats(report) && report.state === 'succeeded') {
        rtt = report.currentRoundTripTime ? report.currentRoundTripTime * 1000 : 0;
        localCandidate = candidateType(stats, report.localCandidateId);
        remoteCandidate = candidateType(stats, report.remoteCandidateId);
        if (localCandidate === 'relay') {
          protocol = 'turn-relay';
        }
      }

      if (isInboundRtpStats(report) && report.kind === 'video') {
        accumulateVideo(video, report);
      }

      if (isOutboundRtpStats(report)) {
        bytesSent += report.bytesSent ?? 0;
      }
    });

    const {jitter, packetsLost, packetsReceived, bytesReceived, frameRate, resolution} = video;

    const now = Date.now();
    const elapsedSeconds = (now - this.lastTimestamp) / 1000;
    let bitrate = 0;
    if (elapsedSeconds > 0 && bytesReceived > this.lastBytesReceived) {
      bitrate = ((bytesReceived - this.lastBytesReceived) * 8) / elapsedSeconds / 1000;
    }
    this.lastBytesReceived = bytesReceived;
    this.lastTimestamp = now;

    const packetLossRate =
      packetsReceived > 0 ? (packetsLost / (packetsLost + packetsReceived)) * 100 : 0;

    return {
      rtt,
      jitter,
      packetsLost,
      packetLossRate,
      protocol,
      localCandidate,
      remoteCandidate,
      bytesReceived,
      bytesSent,
      frameRate,
      resolution,
      bitrate,
      timestamp: now,
    };
  }
}
