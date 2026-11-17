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

import {DecimalPipe} from '@angular/common';
import {ChangeDetectionStrategy, Component, computed, input, signal} from '@angular/core';
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';

import {RegionFlagPipe} from '../../pipes/region-flag.pipe';

/**
 * Connectivity metrics interface
 */
export interface HudMetrics {
  rtt: number; // Round-trip time in ms
  jitter?: number; // Jitter in ms
  packetLossRate?: number; // Packet loss percentage
  protocol: 'direct' | 'turn-relay'; // Connection type
  bytesReceived?: number; // Total bytes received
  frameRate?: number; // Video frame rate
  resolution?: {width: number; height: number};
  region?: string; // GCP region/zone
}

/**
 * Quality level based on RTT
 */
export type QualityLevel = 'excellent' | 'good' | 'fair' | 'poor' | 'bad';

const QUALITY_LABELS: Record<QualityLevel, string> = {
  excellent: 'Excellent',
  good: 'Good',
  fair: 'Fair',
  poor: 'Poor',
  bad: 'Bad',
};

/**
 * Connectivity HUD Component
 *
 * Displays real-time connectivity information as an overlay:
 * - RTT with quality indicator (5 dots)
 * - Connection protocol (Direct P2P vs TURN Relay)
 * - Region with flag emoji
 * - Video quality (resolution, framerate)
 * - Bandwidth usage
 *
 * Design follows the UX vision for a compact, informative overlay.
 */
@Component({
  selector: 'app-connectivity-hud',
  imports: [DecimalPipe, MatIconModule, MatTooltipModule, RegionFlagPipe],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="connectivity-hud" [class.expanded]="expanded()" [class.compact]="!expanded()" [class.inline]="inline()">
      <!-- Compact View (always visible) -->
      <button
        type="button"
        class="hud-compact"
        [attr.aria-expanded]="expanded()"
        aria-label="Connection quality details"
        (click)="toggleExpanded()">
        <span [class]="['quality-dots', qualityLevel()]">
          <span class="dot"></span>
          <span class="dot"></span>
          <span class="dot"></span>
          <span class="dot"></span>
          <span class="dot"></span>
        </span>
        <span class="rtt">{{ metrics()?.rtt | number:'1.0-0' }}ms</span>
        <mat-icon class="expand-icon">{{ expanded() ? 'expand_less' : 'expand_more' }}</mat-icon>
      </button>

      <!-- Expanded View -->
      @if (expanded()) {
        <div class="hud-expanded">
          <div class="hud-section">
            <div class="hud-label">Connection</div>
            <div class="hud-row">
              <mat-icon [class]="['protocol-icon', metrics()?.protocol ?? '']">
                {{ metrics()?.protocol === 'direct' ? 'wifi' : 'cell_tower' }}
              </mat-icon>
              <span [class]="['protocol-text', metrics()?.protocol ?? '']">
                {{ metrics()?.protocol === 'direct' ? 'Direct P2P' : 'TURN Relay' }}
              </span>
            </div>
          </div>
          @if (metrics()?.region) {
            <div class="hud-section">
              <div class="hud-label">Region</div>
              <div class="hud-value">{{ metrics()?.region | appRegionFlag:'full' }}</div>
            </div>
          }
          <div class="hud-section">
            <div class="hud-label">Latency</div>
            <div class="hud-row">
              <span [class]="['latency-value', qualityLevel()]">
                {{ metrics()?.rtt | number:'1.0-0' }}ms
              </span>
              <span class="quality-label">{{ getQualityLabel() }}</span>
            </div>
            @if (metrics()?.jitter) {
              <div class="hud-detail">
                Jitter: {{ metrics()?.jitter | number:'1.1-1' }}ms
              </div>
            }
          </div>
          @if (metrics()?.resolution) {
            <div class="hud-section">
              <div class="hud-label">Quality</div>
              <div class="hud-value">
                {{ metrics()?.resolution?.width }}x{{ metrics()?.resolution?.height }}
                @if (metrics()?.frameRate) {
                  <span>@ {{ metrics()?.frameRate | number:'1.0-0' }}fps</span>
                }
              </div>
            </div>
          }
          @if (metrics()?.packetLossRate !== undefined) {
            <div class="hud-section">
              <div class="hud-label">Packet Loss</div>
              <div class="hud-value" [class.warning]="metrics()?.packetLossRate! > 1" [class.error]="metrics()?.packetLossRate! > 5">
                {{ metrics()?.packetLossRate | number:'1.2-2' }}%
              </div>
            </div>
          }
          @if (metrics()?.bytesReceived) {
            <div class="hud-section">
              <div class="hud-label">Data Received</div>
              <div class="hud-value">{{ formatBytes(metrics()?.bytesReceived!) }}</div>
            </div>
          }
        </div>
      }
    </div>
    `,
  styles: `
    /* GCP Quality Colors (matching _gcp-variables.scss) */
    :host {
      --gcp-quality-excellent: var(--cco-success);
      --gcp-quality-good: var(--cco-success);
      --gcp-quality-fair: var(--cco-warning);
      --gcp-quality-poor: var(--cco-warning);
      --gcp-quality-bad: var(--cco-error);
      --gcp-success: var(--cco-success);
      --gcp-success-light: var(--cco-success);
      --gcp-warning: var(--cco-warning);
      --gcp-warning-light: var(--cco-warning);
      --gcp-error: var(--cco-error);
      --gcp-text-disabled: var(--cco-text-secondary);
    }

    .connectivity-hud {
      position: absolute;
      top: 12px;
      right: 12px;
      background: rgba(32, 33, 36, 0.85);
      backdrop-filter: blur(8px);
      border-radius: 8px;
      color: white;
      font-size: 12px;
      user-select: none;
      z-index: 100;
      min-width: 120px;
    }

    /* Inline mode for header placement */
    .connectivity-hud.inline {
      position: static;
      background: transparent;
      backdrop-filter: none;
      border-radius: 0;
      min-width: auto;
    }

    .hud-compact {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 8px 12px;
      cursor: pointer;
      /* A <button type="button"> now, so the UA defaults have to be undone. */
      width: 100%;
      border: none;
      background: none;
      color: inherit;
      font: inherit;
    }

    .hud-compact:hover {
      background: rgba(255, 255, 255, 0.05);
      border-radius: 8px;
    }

    .quality-dots {
      display: flex;
      gap: 2px;
    }

    .dot {
      width: 5px;
      height: 12px;
      background: var(--gcp-quality-excellent);
      border-radius: 2px;
      transition: background var(--cco-duration-slow);
    }

    /* Quality levels - progressively dim dots */
    .quality-dots.excellent .dot { background: var(--gcp-quality-excellent); }

    .quality-dots.good .dot { background: var(--gcp-quality-good); }
    .quality-dots.good .dot:nth-child(5) { background: var(--cco-border); }

    .quality-dots.fair .dot { background: var(--gcp-quality-fair); }
    .quality-dots.fair .dot:nth-child(n+4) { background: var(--cco-border); }

    .quality-dots.poor .dot { background: var(--gcp-quality-poor); }
    .quality-dots.poor .dot:nth-child(n+3) { background: var(--cco-border); }

    .quality-dots.bad .dot { background: var(--gcp-quality-bad); }
    .quality-dots.bad .dot:nth-child(n+2) { background: var(--cco-border); }

    .rtt {
      font-family: 'Roboto Mono', monospace;
      font-weight: 500;
    }

    .expand-icon {
      font-size: 16px;
      width: 16px;
      height: 16px;
      color: var(--gcp-text-disabled);
    }

    .hud-expanded {
      padding: 8px 12px 12px;
      border-top: 1px solid rgba(255, 255, 255, 0.1);
    }

    .hud-section {
      margin-top: 8px;
    }

    .hud-section:first-child {
      margin-top: 0;
    }

    .hud-label {
      font-size: 11px;
      color: var(--gcp-text-disabled);
      text-transform: uppercase;
      letter-spacing: 0.5px;
      margin-bottom: 2px;
    }

    .hud-row {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .hud-value {
      font-family: 'Roboto Mono', monospace;
    }

    .hud-detail {
      font-size: 11px;
      color: var(--gcp-text-disabled);
      margin-top: 2px;
    }

    .protocol-icon {
      font-size: 16px;
      width: 16px;
      height: 16px;
    }

    .protocol-icon.direct { color: var(--gcp-success); }
    .protocol-icon.turn-relay { color: var(--gcp-warning); }

    .protocol-text {
      font-weight: 500;
    }

    .protocol-text.direct { color: var(--gcp-success-light); }
    .protocol-text.turn-relay { color: var(--gcp-warning-light); }

    .latency-value {
      font-size: 16px;
      font-weight: 600;
      font-family: 'Roboto Mono', monospace;
    }

    .latency-value.excellent { color: var(--gcp-quality-excellent); }
    .latency-value.good { color: var(--gcp-quality-good); }
    .latency-value.fair { color: var(--gcp-quality-fair); }
    .latency-value.poor { color: var(--gcp-quality-poor); }
    .latency-value.bad { color: var(--gcp-quality-bad); }

    .quality-label {
      color: var(--gcp-text-disabled);
      font-size: 11px;
    }

    .warning { color: var(--gcp-warning); }
    .error { color: var(--gcp-error); }
  `,
})
export class ConnectivityHudComponent {
  // Signal inputs
  readonly metrics = input<HudMetrics>();
  readonly inline = input(false);

  // Local state (writable signal for toggle behavior)
  readonly expanded = signal(false);

  // Computed signals
  readonly qualityLevel = computed<QualityLevel>(() => {
    const m = this.metrics();
    if (!m) {
      return 'fair';
    }
    const rtt = m.rtt;
    if (rtt < 20) {
      return 'excellent';
    }
    if (rtt < 50) {
      return 'good';
    }
    if (rtt < 100) {
      return 'fair';
    }
    if (rtt < 200) {
      return 'poor';
    }
    return 'bad';
  });

  toggleExpanded(): void {
    this.expanded.update(v => !v);
  }

  getQualityLabel(): string {
    return QUALITY_LABELS[this.qualityLevel()];
  }

  formatBytes(bytes: number): string {
    if (bytes < 1024) {
      return `${bytes} B`;
    }
    if (bytes < 1024 * 1024) {
      return `${(bytes / 1024).toFixed(1)} KB`;
    }
    if (bytes < 1024 * 1024 * 1024) {
      return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    }
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }
}
