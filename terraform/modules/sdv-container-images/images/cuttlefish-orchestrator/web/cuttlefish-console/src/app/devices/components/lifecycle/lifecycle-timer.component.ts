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

import {DatePipe} from '@angular/common';
import type {OnDestroy, OnInit} from '@angular/core';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  input,
  output,
  signal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatProgressBarModule} from '@angular/material/progress-bar';
import {MatTooltipModule} from '@angular/material/tooltip';

import type {DeviceLifecycle} from '../../models/device.model';
import {PIN_REASON_LABELS} from '../../models/device.model';

const WARNING_THRESHOLD_MINUTES = 30;
const CRITICAL_THRESHOLD_MINUTES = 10;

/** Every rendered value, derived together from one lifecycle and one clock. */
interface TimerReading {
  readonly state: 'healthy' | 'warning' | 'critical' | 'pinned';
  readonly label: string;
  readonly percent: number;
  readonly color: 'primary' | 'accent' | 'warn';
  readonly expiringSoon: boolean;
  readonly critical: boolean;
  /** The lapsed deadline, which is what an announcement is latched against. */
  readonly expiredAtMs: number | null;
}

const ABSENT: TimerReading = {
  state: 'healthy',
  // A hyphen rather than blank. A lifecycle this component was handed no value
  // for is a different thing from one with no expiry, and a compact chip
  // rendering empty reads as a layout bug.
  label: '-',
  percent: 100,
  color: 'primary',
  expiringSoon: false,
  critical: false,
  expiredAtMs: null,
};

function countdownLabel(remainingSeconds: number): string {
  const hours = Math.floor(remainingSeconds / 3600);
  const minutes = Math.floor((remainingSeconds % 3600) / 60);
  const seconds = remainingSeconds % 60;
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  return minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`;
}

/** Default TTL for a lifecycle that reported none, so the bar has a denominator. */
const FALLBACK_TTL_SECONDS = 14400;

function readingFor(lifecycle: DeviceLifecycle | undefined, nowMs: number): TimerReading {
  if (!lifecycle) {
    return ABSENT;
  }
  if (lifecycle.mode === 'pinned') {
    return {...ABSENT, state: 'pinned', label: 'Pinned'};
  }
  if (!lifecycle.expiresAt) {
    return {...ABSENT, label: 'No expiry'};
  }

  const expiresAt =
    lifecycle.expiresAt instanceof Date ? lifecycle.expiresAt : new Date(lifecycle.expiresAt);
  const remainingMs = expiresAt.getTime() - nowMs;

  if (remainingMs <= 0) {
    // Lapsed is the loudest the timer gets, so it carries BOTH emphasis flags.
    // The earlier version set the state alone, which left an expired row
    // rendering as un-emphasised unless it happened to pass through the
    // critical window first.
    return {
      state: 'critical',
      label: 'Expired',
      percent: 0,
      color: 'warn',
      expiringSoon: true,
      critical: true,
      expiredAtMs: expiresAt.getTime(),
    };
  }

  const remainingSeconds = Math.floor(remainingMs / 1000);
  const totalSeconds = lifecycle.ttlSeconds || FALLBACK_TTL_SECONDS;
  const percent = Math.min(100, Math.max(0, (remainingSeconds / totalSeconds) * 100));
  const label = countdownLabel(remainingSeconds);
  const remainingMinutes = remainingSeconds / 60;

  if (remainingMinutes <= CRITICAL_THRESHOLD_MINUTES) {
    return {
      state: 'critical',
      label,
      percent,
      color: 'warn',
      expiringSoon: true,
      critical: true,
      expiredAtMs: null,
    };
  }
  if (remainingMinutes <= WARNING_THRESHOLD_MINUTES) {
    return {
      state: 'warning',
      label,
      percent,
      color: 'accent',
      expiringSoon: true,
      critical: false,
      expiredAtMs: null,
    };
  }
  return {
    state: 'healthy',
    label,
    percent,
    color: 'primary',
    expiringSoon: false,
    critical: false,
    expiredAtMs: null,
  };
}

/**
 * Lifecycle Timer Component
 *
 * Displays the countdown timer for ephemeral devices or pin status for persistent devices.
 *
 * Visual states:
 * - Ephemeral with time: Shows countdown bar and time remaining
 * - Ephemeral expiring soon: Shows warning state (amber)
 * - Ephemeral expired: Shows error state (red)
 * - Pinned: Shows pin icon with expiry date
 */
@Component({
  selector: 'app-lifecycle-timer',
  imports: [DatePipe, MatIconModule, MatButtonModule, MatTooltipModule, MatProgressBarModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div [class]="['lifecycle-timer', timerState()]" [class.compact]="compact()">
      <!-- Compact Mode: Chip-style display for dense tables -->
      @if (compact()) {
        <div [class]="['compact-container', timerState()]">
          @if (lifecycle()?.mode === 'pinned') {
            <mat-icon class="compact-icon pinned">push_pin</mat-icon>
            <span class="compact-label">Pinned</span>
          } @else {
            <mat-icon class="compact-icon">schedule</mat-icon>
            <span class="compact-label" [class.warning]="isExpiringSoon()" [class.critical]="isCritical()">
              {{ timeRemaining() }}
            </span>
          }
        </div>
      } @else {
        <!-- Ephemeral Device: Show countdown -->
        @if (lifecycle()?.mode === 'ephemeral') {
          <div class="countdown-container">
            <mat-icon class="timer-icon">schedule</mat-icon>
            <div class="countdown-info">
              <span class="time-remaining" [class.warning]="isExpiringSoon()" [class.critical]="isCritical()">
                {{ timeRemaining() }}
              </span>
              <mat-progress-bar aria-label="Time remaining"
                mode="determinate"
                [value]="progressPercent()"
                [color]="progressColor()"
                class="ttl-bar"
              />
            </div>
            @if (showExtendButton()) {
              <button type="button"
                mat-icon-button
                class="extend-button"
                (click)="onExtendClick()"
                matTooltip="Extend session" aria-label="Extend session">
                <mat-icon>add_circle_outline</mat-icon>
              </button>
            }
          </div>
        }

        <!-- Pinned Device: Show pin status -->
        @if (lifecycle()?.mode === 'pinned') {
          <div class="pinned-container">
            <mat-icon class="pin-icon pinned">push_pin</mat-icon>
            <div class="pin-info">
              <span class="pin-label">Pinned</span>
              @if (lifecycle()?.pinnedUntil) {
                <span class="pin-expiry">
                  until {{ lifecycle()?.pinnedUntil | date:'MMM d, HH:mm' }}
                </span>
              }
              @if (lifecycle()?.pinnedReason && showReason()) {
                <span class="pin-reason">
                  {{ getPinReasonLabel() }}
                </span>
              }
            </div>
          </div>
        }
      }
    </div>
    `,
  styles: `
    .lifecycle-timer {
      display: flex;
      align-items: center;
      min-width: 140px;
    }

    .countdown-container {
      display: flex;
      align-items: center;
      gap: 8px;
      width: 100%;
    }

    .timer-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-text-secondary);
    }

    .countdown-info {
      flex: 1;
      display: flex;
      flex-direction: column;
      gap: 2px;
    }

    .time-remaining {
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .time-remaining.warning {
      color: var(--cco-warning);
    }

    .time-remaining.critical {
      color: var(--cco-error);
      animation: pulse 1s infinite;
    }

    @keyframes pulse {
      0%, 100% { opacity: 1; }
      50% { opacity: 0.6; }
    }

    .ttl-bar {
      height: 4px;
      border-radius: 2px;
    }

    /* Progress bar inner styling lives in global styles.scss. */

    .extend-button {
      width: 28px;
      height: 28px;
      line-height: 28px;
    }

    .extend-button mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-accent);
    }

    .pinned-container {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    .pin-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .pin-icon.pinned {
      color: var(--cco-accent);
      transform: rotate(45deg);
    }

    .pin-info {
      display: flex;
      flex-direction: column;
    }

    .pin-label {
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-accent);
    }

    .pin-expiry {
      font-size: 11px;
      color: var(--cco-text-secondary);
    }

    .pin-reason {
      font-size: 11px;
      color: var(--cco-text-secondary);
      font-style: italic;
    }

    /* State-based styling */
    .lifecycle-timer.healthy .timer-icon {
      color: var(--cco-success);
    }

    .lifecycle-timer.warning .timer-icon {
      color: var(--cco-warning);
    }

    .lifecycle-timer.critical .timer-icon {
      color: var(--cco-error);
    }

    /* Compact mode - chip-style display for dense tables */
    .lifecycle-timer.compact {
      min-width: auto;
    }

    .compact-container {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 4px 12px;
      border-radius: 16px;
      font-size: 12px;
      font-weight: 500;
      background: var(--cco-success-surface);
      color: var(--cco-success);
    }

    .compact-container.pinned {
      background: var(--cco-accent-surface);
      color: var(--cco-accent);
    }

    .compact-container.warning {
      background: var(--cco-warning-surface);
      color: var(--cco-warning);
    }

    .compact-container.critical {
      background: var(--cco-error-surface);
      color: var(--cco-error);
    }

    .compact-icon {
      font-size: 14px;
      width: 14px;
      height: 14px;
    }

    .compact-icon.pinned {
      transform: rotate(45deg);
    }

    .compact-label {
      white-space: nowrap;
    }

    .compact-label.warning {
      color: var(--cco-warning);
    }

    .compact-label.critical {
      color: var(--cco-error);
    }
  `,
})
export class LifecycleTimerComponent implements OnInit, OnDestroy {
  // Signal inputs
  readonly lifecycle = input<DeviceLifecycle | undefined>();
  readonly showExtendButton = input(true);
  readonly showReason = input(false);
  readonly compact = input(false);

  // Signal outputs
  readonly extend = output<void>();
  readonly expired = output<void>();

  /**
   * The clock. It is the only thing the interval touches. Everything rendered
   * is derived from it, so a tick cannot push one value and forget another.
   */
  private readonly nowMs = signal(Date.now());

  private readonly reading = computed(() => readingFor(this.lifecycle(), this.nowMs()));

  readonly timeRemaining = computed(() => this.reading().label);
  readonly progressPercent = computed(() => this.reading().percent);
  readonly progressColor = computed(() => this.reading().color);
  readonly timerState = computed(() => this.reading().state);
  readonly isExpiringSoon = computed(() => this.reading().expiringSoon);
  readonly isCritical = computed(() => this.reading().critical);

  /**
   * The deadline already announced, which is what stops the announcement from
   * repeating. Latched against the deadline rather than a boolean so that an
   * extension (a new `expiresAt`) re-arms it without any reset of its own.
   */
  private announcedFor: number | null = null;

  private intervalId?: ReturnType<typeof setInterval>;

  constructor() {
    // Announcing is a transition, not a state, so it is emitted once per lapsed
    // deadline. Emitting it from the tick instead, which is what this replaced,
    // fired it every second for as long as the row stayed mounted, and it is
    // wired to a snackbar.
    effect(() => {
      const expiredAtMs = this.reading().expiredAtMs;
      if (expiredAtMs === null || this.announcedFor === expiredAtMs) {
        return;
      }
      this.announcedFor = expiredAtMs;
      this.expired.emit();
    });
  }

  ngOnInit(): void {
    this.intervalId = setInterval(() => this.nowMs.set(Date.now()), 1000);
  }

  ngOnDestroy(): void {
    if (this.intervalId) {
      clearInterval(this.intervalId);
    }
  }

  onExtendClick(): void {
    this.extend.emit();
  }

  /**
   * The pin reason in words, from the one map the create wizard, the pin dialog
   * and the facts card all read. A second shorter copy lived here and had
   * drifted. The same reason rendered as "Overnight Test" beside "Overnight
   * Stability Test" depending on which surface you were looking at.
   */
  getPinReasonLabel(): string {
    const reason = this.lifecycle()?.pinnedReason;
    return reason ? (PIN_REASON_LABELS[reason] ?? '') : '';
  }
}
