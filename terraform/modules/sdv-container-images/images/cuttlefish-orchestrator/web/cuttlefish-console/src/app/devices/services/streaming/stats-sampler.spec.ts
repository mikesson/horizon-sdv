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

import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {StatsSampler} from './stats-sampler';

type StatsEntry = {id?: string} & Record<string, unknown>;

function statsReport(reports: StatsEntry[]): RTCStatsReport {
  const map = new Map(reports.map((r, i) => [r.id ?? String(i), r]));
  return map;
}

function fakePc(reports: StatsEntry[]): RTCPeerConnection {
  return {
    getStats: vi.fn(() => Promise.resolve(statsReport(reports))),
  } as unknown as RTCPeerConnection;
}

const VIDEO_RTP = {
  id: 'rtp-1',
  type: 'inbound-rtp',
  kind: 'video',
  jitter: 0.005,
  packetsLost: 10,
  packetsReceived: 990,
  bytesReceived: 500_000,
  framesPerSecond: 30,
  frameWidth: 720,
  frameHeight: 1280,
};

describe('StatsSampler', () => {
  let sampler: StatsSampler;

  beforeEach(() => {
    vi.useFakeTimers();
    sampler = new StatsSampler();
  });

  afterEach(() => {
    sampler.stop();
    vi.useRealTimers();
  });

  it('starts with null metrics and clears them on stop', async () => {
    expect(sampler.metrics()).toBeNull();

    sampler.start(fakePc([VIDEO_RTP]));
    await vi.advanceTimersByTimeAsync(1000);
    expect(sampler.metrics()).not.toBeNull();

    sampler.stop();
    expect(sampler.metrics()).toBeNull();
  });

  it('parses RTT, jitter, loss, and video stats', async () => {
    sampler.start(
      fakePc([
        {
          id: 'pair-1',
          type: 'candidate-pair',
          state: 'succeeded',
          currentRoundTripTime: 0.042,
          localCandidateId: 'local-1',
          remoteCandidateId: 'remote-1',
        },
        {id: 'local-1', type: 'local-candidate', candidateType: 'relay'},
        {id: 'remote-1', type: 'remote-candidate', candidateType: 'host'},
        VIDEO_RTP,
        {id: 'out-1', type: 'outbound-rtp', bytesSent: 12_345},
      ])
    );
    await vi.advanceTimersByTimeAsync(1000);

    const metrics = sampler.metrics();
    expect(metrics).toMatchObject({
      rtt: 42,
      jitter: 5,
      packetsLost: 10,
      packetLossRate: 1,
      protocol: 'turn-relay',
      localCandidate: 'relay',
      remoteCandidate: 'host',
      bytesReceived: 500_000,
      bytesSent: 12_345,
      frameRate: 30,
      resolution: {width: 720, height: 1280},
    });
  });

  it('reports direct protocol for non-relay candidates', async () => {
    sampler.start(
      fakePc([
        {
          id: 'pair-1',
          type: 'candidate-pair',
          state: 'succeeded',
          currentRoundTripTime: 0.01,
          localCandidateId: 'local-1',
          remoteCandidateId: 'remote-1',
        },
        {id: 'local-1', type: 'local-candidate', candidateType: 'host'},
        {id: 'remote-1', type: 'remote-candidate', candidateType: 'host'},
      ])
    );
    await vi.advanceTimersByTimeAsync(1000);

    expect(sampler.metrics()?.protocol).toBe('direct');
  });

  it('derives bitrate from byte deltas across samples', async () => {
    let bytes = 0;
    const pc = {
      getStats: vi.fn(() => {
        bytes += 125_000; // 1 Mbit per second at the 1s cadence
        return Promise.resolve(statsReport([{...VIDEO_RTP, bytesReceived: bytes}]));
      }),
    } as unknown as RTCPeerConnection;

    sampler.start(pc);
    await vi.advanceTimersByTimeAsync(1000);
    await vi.advanceTimersByTimeAsync(1000);

    const metrics = sampler.metrics();
    // (125000 bytes * 8) / 1s / 1000 = 1000 kbps
    expect(metrics?.bitrate).toBeCloseTo(1000, 0);
  });

  it('keeps the previous sample when getStats rejects', async () => {
    const pc = fakePc([VIDEO_RTP]);
    sampler.start(pc);
    await vi.advanceTimersByTimeAsync(1000);
    const first = sampler.metrics();
    expect(first).not.toBeNull();

    (pc.getStats as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('gone'));
    await vi.advanceTimersByTimeAsync(1000);

    expect(sampler.metrics()).toBe(first);
  });

  it('aggregates multi-display video stats: totals sum, largest stream leads', async () => {
    const secondDisplay = {
      id: 'rtp-2',
      type: 'inbound-rtp',
      kind: 'video',
      jitter: 0.02,
      packetsLost: 30,
      packetsReceived: 1970,
      bytesReceived: 2_000_000,
      framesPerSecond: 60,
      frameWidth: 1920,
      frameHeight: 1080,
    };
    // Report order must not matter; the larger display leads either way.
    sampler.start(fakePc([VIDEO_RTP, secondDisplay]));
    await vi.advanceTimersByTimeAsync(1000);

    const metrics = sampler.metrics();
    expect(metrics).toMatchObject({
      packetsLost: 40,
      bytesReceived: 2_500_000,
      resolution: {width: 1920, height: 1080},
      frameRate: 60,
      jitter: 20,
    });
    // Loss rate over the combined counters: 40 / (40 + 2960).
    expect(metrics?.packetLossRate).toBeCloseTo((40 / 3000) * 100, 5);
  });
});
