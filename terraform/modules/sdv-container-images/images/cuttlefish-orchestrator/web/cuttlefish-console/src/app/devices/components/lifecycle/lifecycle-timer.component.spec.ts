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

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import type {DeviceLifecycle, PinReason} from '../../models/device.model';
import {PIN_REASON_LABELS} from '../../models/device.model';
import {LifecycleTimerComponent} from './lifecycle-timer.component';

const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;

/** Every deadline in this file is expressed relative to this instant. */
const NOW = new Date('2026-08-08T12:00:00Z');

interface Emphasis {
  state: string;
  color: string;
  expiringSoon: boolean;
  critical: boolean;
}

function ephemeral(msLeft: number, overrides: Partial<DeviceLifecycle> = {}): DeviceLifecycle {
  return {
    mode: 'ephemeral',
    ttlSeconds: 14400,
    expiresAt: new Date(NOW.getTime() + msLeft),
    extendedCount: 0,
    ...overrides,
  };
}

function pinned(overrides: Partial<DeviceLifecycle> = {}): DeviceLifecycle {
  return {mode: 'pinned', ttlSeconds: 14400, extendedCount: 0, ...overrides};
}

describe('LifecycleTimerComponent', () => {
  let fixture: ComponentFixture<LifecycleTimerComponent>;
  let component: LifecycleTimerComponent;
  let root: HTMLElement;
  let extendRequests: number;
  let expiryNotices: number;

  function mount(
    lifecycle: DeviceLifecycle | undefined,
    inputs: Record<string, unknown> = {}
  ): void {
    fixture = TestBed.createComponent(LifecycleTimerComponent);
    fixture.componentRef.setInput('lifecycle', lifecycle);
    for (const [name, value] of Object.entries(inputs)) {
      fixture.componentRef.setInput(name, value);
    }
    component = fixture.componentInstance;
    extendRequests = 0;
    expiryNotices = 0;
    component.extend.subscribe(() => {
      extendRequests++;
    });
    component.expired.subscribe(() => {
      expiryNotices++;
    });
    // ngOnInit runs here: the first reading is taken before any tick.
    fixture.detectChanges();
    root = fixture.nativeElement as HTMLElement;
  }

  function tick(ms: number): void {
    vi.advanceTimersByTime(ms);
    fixture.detectChanges();
  }

  /** Mounts at a deadline, reads the label, and tears the fixture down again. */
  function labelAt(msLeft: number, overrides: Partial<DeviceLifecycle> = {}): string {
    mount(ephemeral(msLeft, overrides));
    const label = component.timeRemaining();
    fixture.destroy();
    return label;
  }

  function emphasisAt(msLeft: number): Emphasis {
    mount(ephemeral(msLeft));
    const emphasis: Emphasis = {
      state: component.timerState(),
      color: component.progressColor(),
      expiringSoon: component.isExpiringSoon(),
      critical: component.isCritical(),
    };
    fixture.destroy();
    return emphasis;
  }

  function reasonLabel(reason: string): string {
    mount(pinned({pinnedReason: reason as PinReason}));
    const label = component.getPinReasonLabel();
    fixture.destroy();
    return label;
  }

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    TestBed.configureTestingModule({imports: [LifecycleTimerComponent]});
  });

  afterEach(() => {
    fixture.destroy();
    vi.useRealTimers();
  });

  describe('duration formatting', () => {
    it('formats the remaining time by magnitude', () => {
      expect(labelAt(HOUR + 30 * MINUTE + 45 * SECOND)).toBe('1h 30m');
      expect(labelAt(HOUR)).toBe('1h 0m');
      expect(labelAt(HOUR - SECOND)).toBe('59m 59s');
      expect(labelAt(MINUTE)).toBe('1m 0s');
      expect(labelAt(MINUTE - SECOND)).toBe('59s');
      expect(labelAt(SECOND)).toBe('1s');
    });

    it('truncates rather than rounds a part-second remainder', () => {
      expect(labelAt(90 * SECOND + 999)).toBe('1m 30s');
    });

    it('shows 0s, not Expired, while under a second is left', () => {
      mount(ephemeral(500));

      expect(component.timeRemaining()).toBe('0s');
      expect(expiryNotices).toBe(0);
    });
  });

  describe('threshold boundaries', () => {
    it('turns critical at exactly ten minutes and not a second earlier', () => {
      expect(emphasisAt(10 * MINUTE + SECOND).state).toBe('warning');
      expect(emphasisAt(10 * MINUTE).state).toBe('critical');
    });

    it('turns warning at exactly thirty minutes and not a second earlier', () => {
      expect(emphasisAt(30 * MINUTE + SECOND).state).toBe('healthy');
      expect(emphasisAt(30 * MINUTE).state).toBe('warning');
    });

    it('drives the bar colour and both emphasis flags from the state', () => {
      expect(emphasisAt(2 * HOUR)).toEqual({
        state: 'healthy',
        color: 'primary',
        expiringSoon: false,
        critical: false,
      });
      expect(emphasisAt(20 * MINUTE)).toEqual({
        state: 'warning',
        color: 'accent',
        expiringSoon: true,
        critical: false,
      });
      expect(emphasisAt(5 * MINUTE)).toEqual({
        state: 'critical',
        color: 'warn',
        expiringSoon: true,
        critical: true,
      });
    });

    it('marks the label critical in the last ten minutes', () => {
      mount(ephemeral(9 * MINUTE));

      const label = root.querySelector('.time-remaining');
      expect(label?.textContent?.trim()).toBe('9m 0s');
      expect(label?.classList.contains('critical')).toBe(true);
      expect(label?.classList.contains('warning')).toBe(true);
    });
  });

  describe('the deadline itself', () => {
    it('reports Expired at the exact deadline, not a millisecond later', () => {
      mount(ephemeral(0));

      expect(component.timeRemaining()).toBe('Expired');
      expect(component.progressPercent()).toBe(0);
      expect(component.timerState()).toBe('critical');
      expect(expiryNotices).toBe(1);
    });

    it('reports Expired for a deadline already in the past', () => {
      mount(ephemeral(-2 * HOUR));

      expect(component.timeRemaining()).toBe('Expired');
      expect(expiryNotices).toBe(1);
    });

    it('counts down across ticks and announces the expiry as it lands', () => {
      mount(ephemeral(2 * SECOND));
      expect(component.timeRemaining()).toBe('2s');

      tick(SECOND);
      expect(component.timeRemaining()).toBe('1s');
      expect(expiryNotices).toBe(0);

      tick(SECOND);
      expect(component.timeRemaining()).toBe('Expired');
      expect(expiryNotices).toBe(1);
    });

    it('announces the expiry exactly once, however long the row stays open', () => {
      // The announcement is wired to a snackbar, so a repeat is not a cosmetic
      // duplicate. One lapsed row used to make the whole device list unusable.
      mount(ephemeral(0));
      expect(expiryNotices).toBe(1);

      tick(3 * SECOND);

      expect(expiryNotices).toBe(1);
    });

    it('announces again once an extension has been outlived in turn', () => {
      // Latched against the deadline rather than a boolean. An extended device
      // that lapses a second time is a second event, and a latch that never
      // re-arms would swallow it silently.
      mount(ephemeral(0));
      expect(expiryNotices).toBe(1);

      fixture.componentRef.setInput('lifecycle', ephemeral(2 * SECOND));
      fixture.detectChanges();
      expect(expiryNotices).toBe(1);

      tick(2 * SECOND);

      expect(expiryNotices).toBe(2);
    });

    it('carries both emphasis flags on the expiry path', () => {
      // Lapsed is the loudest the timer gets. The earlier version set the
      // state alone, so a row that mounted already expired rendered the word
      // Expired in ordinary type inside a critical container.
      mount(ephemeral(0), {compact: true});

      expect(component.timerState()).toBe('critical');
      expect(component.isCritical()).toBe(true);
      const label = root.querySelector('.compact-label');
      expect(label?.textContent?.trim()).toBe('Expired');
      expect(label?.classList.contains('critical')).toBe(true);
    });
  });

  describe('progress against the original TTL', () => {
    it('scales the bar against the TTL the device was created with', () => {
      mount(ephemeral(HOUR));

      expect(component.progressPercent()).toBe(25);
    });

    it('clamps at 100 when an extension outran the original TTL', () => {
      mount(ephemeral(HOUR, {ttlSeconds: 1800, extendedCount: 2}));

      expect(component.progressPercent()).toBe(100);
    });

    it('falls back to four hours when the device reports no TTL', () => {
      mount(ephemeral(HOUR, {ttlSeconds: 0}));

      expect(component.progressPercent()).toBe(25);
    });
  });

  describe('incomplete lifecycles', () => {
    it('renders neither branch and stays silent without a lifecycle', () => {
      mount(undefined);

      expect(component.timeRemaining()).toBe('-');
      expect(root.querySelector('.countdown-container')).toBeNull();
      expect(root.querySelector('.pinned-container')).toBeNull();

      tick(5 * SECOND);

      expect(expiryNotices).toBe(0);
    });

    it('marks the compact chip absent while the lifecycle has not loaded', () => {
      // A blank chip reads as a layout bug. A hyphen reads as "no answer yet",
      // which is what it is.
      mount(undefined, {compact: true});

      const chip = root.querySelector('.compact-container');
      expect(chip).not.toBeNull();
      expect(chip?.querySelector('.compact-label')?.textContent?.trim()).toBe('-');
    });

    it('says No expiry for an ephemeral device carrying no deadline', () => {
      mount({mode: 'ephemeral', ttlSeconds: 14400, extendedCount: 0});

      expect(component.timeRemaining()).toBe('No expiry');
      expect(component.timerState()).toBe('healthy');
      expect(component.progressPercent()).toBe(100);
      expect(expiryNotices).toBe(0);
    });

    it('accepts a deadline that arrived as a wire string', () => {
      const wire = {
        ...ephemeral(0),
        expiresAt: new Date(NOW.getTime() + 90 * SECOND).toISOString(),
      } as unknown as DeviceLifecycle;

      mount(wire);

      expect(component.timeRemaining()).toBe('1m 30s');
      expect(expiryNotices).toBe(0);
    });
  });

  describe('pinned devices', () => {
    it('shows the pin and its deadline instead of a countdown', () => {
      mount(pinned({pinnedUntil: new Date(NOW.getTime() + 20 * HOUR)}));

      expect(component.timerState()).toBe('pinned');
      expect(root.querySelector('.countdown-container')).toBeNull();
      expect(root.querySelector('.pin-label')?.textContent?.trim()).toBe('Pinned');
      expect(root.querySelector('.pin-expiry')?.textContent?.trim()).toMatch(/^until \S/);
    });

    it('omits the expiry line when the pin has no target date', () => {
      mount(pinned());

      expect(root.querySelector('.pin-label')).not.toBeNull();
      expect(root.querySelector('.pin-expiry')).toBeNull();
    });

    it('never reads a stale ephemeral deadline as an expiry', () => {
      mount(pinned({expiresAt: new Date(NOW.getTime() - HOUR)}));

      expect(component.timerState()).toBe('pinned');
      expect(component.timeRemaining()).toBe('Pinned');

      tick(2 * SECOND);

      expect(expiryNotices).toBe(0);
      expect(component.timerState()).toBe('pinned');
    });

    it('withholds the reason unless the caller asks for it', () => {
      mount(pinned({pinnedReason: 'long_running_ci'}));

      expect(root.querySelector('.pin-reason')).toBeNull();
    });

    it('renders the reason label when the caller asks for it', () => {
      mount(pinned({pinnedReason: 'long_running_ci'}), {showReason: true});

      expect(root.querySelector('.pin-reason')?.textContent?.trim()).toBe(
        'Long-Running CI Pipeline'
      );
    });

    it('labels every reason from the one shared map and blanks the rest', () => {
      // The same strings the create wizard, the pin dialog and the facts card
      // render. A second shorter copy lived in this component and had drifted.
      expect(reasonLabel('overnight_test')).toBe(PIN_REASON_LABELS.overnight_test);
      expect(reasonLabel('memory_leak_debug')).toBe(PIN_REASON_LABELS.memory_leak_debug);
      expect(reasonLabel('long_running_ci')).toBe(PIN_REASON_LABELS.long_running_ci);
      expect(reasonLabel('demo_preparation')).toBe(PIN_REASON_LABELS.demo_preparation);
      expect(reasonLabel('customer_support')).toBe(PIN_REASON_LABELS.customer_support);
      expect(reasonLabel('other')).toBe(PIN_REASON_LABELS.other);
      expect(reasonLabel('spring_cleaning')).toBe('');
      expect(reasonLabel('')).toBe('');
    });

    it('renders a pinned compact chip', () => {
      mount(pinned(), {compact: true});

      const chip = root.querySelector('.compact-container');
      expect(chip?.classList.contains('pinned')).toBe(true);
      expect(chip?.textContent).toContain('Pinned');
      expect(root.querySelector('.pinned-container')).toBeNull();
    });
  });

  describe('extending', () => {
    it('offers the extend button by default and emits on click', () => {
      mount(ephemeral(HOUR));

      const button = root.querySelector<HTMLButtonElement>('.extend-button');
      expect(button).not.toBeNull();
      button?.dispatchEvent(new MouseEvent('click', {bubbles: true}));

      expect(extendRequests).toBe(1);
    });

    it('hides the extend button when the caller withholds it', () => {
      mount(ephemeral(HOUR), {showExtendButton: false});

      expect(root.querySelector('.extend-button')).toBeNull();
    });

    it('never offers the extend button to a pinned device', () => {
      mount(pinned(), {showExtendButton: true});

      expect(root.querySelector('.extend-button')).toBeNull();
    });
  });

  describe('compact mode', () => {
    it('carries the countdown in the chip and drops the full layout', () => {
      mount(ephemeral(5 * MINUTE), {compact: true});

      const chip = root.querySelector('.compact-container');
      expect(chip?.classList.contains('critical')).toBe(true);
      expect(chip?.textContent).toContain('5m 0s');
      expect(root.querySelector('.countdown-container')).toBeNull();
      expect(root.querySelector('mat-progress-bar')).toBeNull();
    });
  });

  describe('the interval itself', () => {
    it('stops ticking once the component is destroyed', () => {
      mount(ephemeral(5 * MINUTE));
      expect(component.timeRemaining()).toBe('5m 0s');

      fixture.destroy();
      vi.advanceTimersByTime(60 * SECOND);

      expect(component.timeRemaining()).toBe('5m 0s');
      expect(expiryNotices).toBe(0);
    });

    it('tears down cleanly when destroyed before it ever rendered', () => {
      // ngOnInit is what arms the interval, so a component the parent drops
      // before its first change detection reaches ngOnDestroy with nothing to
      // disarm. The reading still answers, because it is derived from the
      // input rather than accumulated by the ticks.
      fixture = TestBed.createComponent(LifecycleTimerComponent);
      fixture.componentRef.setInput('lifecycle', ephemeral(5 * MINUTE));
      const timer = fixture.componentInstance;

      expect(() => {
        fixture.destroy();
      }).not.toThrow();
      expect(timer.timeRemaining()).toBe('5m 0s');
    });

    it('picks up a re-bound lifecycle immediately, without waiting for a tick', () => {
      // Every rendered value is computed from the input and one clock signal,
      // so re-binding a row to another device cannot leave the previous
      // device's countdown on screen for up to a second.
      mount(ephemeral(HOUR));
      expect(component.timeRemaining()).toBe('1h 0m');

      fixture.componentRef.setInput('lifecycle', ephemeral(5 * MINUTE));
      fixture.detectChanges();

      expect(component.timeRemaining()).toBe('5m 0s');
    });
  });
});
