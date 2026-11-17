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
import {beforeEach, describe, expect, it} from 'vitest';
import type {DeviceLifecycle} from '../../models/device.model';
import {PinButtonComponent} from './pin-button.component';

const EPHEMERAL: DeviceLifecycle = {mode: 'ephemeral', ttlSeconds: 14400, extendedCount: 0};
const PINNED: DeviceLifecycle = {mode: 'pinned', ttlSeconds: 14400, extendedCount: 1};

describe('PinButtonComponent', () => {
  let fixture: ComponentFixture<PinButtonComponent>;
  let host: HTMLElement;
  let button: HTMLButtonElement;
  let pins: number;
  let unpins: number;

  function mount(lifecycle: DeviceLifecycle | undefined, disabled = false): void {
    fixture = TestBed.createComponent(PinButtonComponent);
    fixture.componentRef.setInput('lifecycle', lifecycle);
    fixture.componentRef.setInput('disabled', disabled);
    pins = 0;
    unpins = 0;
    fixture.componentInstance.pin.subscribe(() => {
      pins++;
    });
    fixture.componentInstance.unpin.subscribe(() => {
      unpins++;
    });
    fixture.detectChanges();
    host = fixture.nativeElement as HTMLElement;
    const found = host.querySelector<HTMLButtonElement>('button');
    if (!found) {
      throw new Error('pin button did not render');
    }
    button = found;
  }

  function click(): void {
    button.dispatchEvent(new MouseEvent('click', {bubbles: true}));
  }

  beforeEach(() => {
    TestBed.configureTestingModule({imports: [PinButtonComponent]});
  });

  it('offers to pin an ephemeral device', () => {
    mount(EPHEMERAL);

    expect(button.classList.contains('pinned')).toBe(false);
    expect(button.getAttribute('aria-label')).toBe('Pin device (prevent auto-termination)');
    expect(host.querySelector('mat-icon')?.classList.contains('rotate')).toBe(false);
  });

  it('offers to unpin a pinned device', () => {
    mount(PINNED);

    expect(button.classList.contains('pinned')).toBe(true);
    expect(button.getAttribute('aria-label')).toBe('Unpin device (return to ephemeral)');
    expect(host.querySelector('mat-icon')?.classList.contains('rotate')).toBe(true);
  });

  it('treats a device with no lifecycle as unpinned', () => {
    mount(undefined);

    expect(fixture.componentInstance.isPinned()).toBe(false);
    expect(button.getAttribute('aria-label')).toBe('Pin device (prevent auto-termination)');
  });

  it('emits pin, and only pin, from an ephemeral device', () => {
    mount(EPHEMERAL);

    click();

    expect(pins).toBe(1);
    expect(unpins).toBe(0);
  });

  it('emits unpin, and only unpin, from a pinned device', () => {
    mount(PINNED);

    click();

    expect(unpins).toBe(1);
    expect(pins).toBe(0);
  });

  it('keeps the click off the row it sits in', () => {
    // Every consumer nests this button inside a clickable row, card or toolbar,
    // so pinning must never double as selecting or navigating.
    mount(EPHEMERAL);
    let reachedAncestor = 0;
    host.addEventListener('click', () => {
      reachedAncestor++;
    });

    click();

    expect(pins).toBe(1);
    expect(reachedAncestor).toBe(0);
  });

  it('says why the toggle is unavailable while disabled', () => {
    mount(PINNED, true);

    expect(button.disabled).toBe(true);
    expect(button.classList.contains('disabled')).toBe(true);
    // The disabled wording wins over the pinned wording.
    expect(button.getAttribute('aria-label')).toBe('Cannot modify pin status');
  });

  it('emits nothing while disabled, and still swallows the click', () => {
    // The native disabled attribute only stops user activation; the handler
    // carries its own guard, and that is the one a dispatched event reaches.
    mount(PINNED, true);
    let reachedAncestor = 0;
    host.addEventListener('click', () => {
      reachedAncestor++;
    });

    click();

    expect(pins).toBe(0);
    expect(unpins).toBe(0);
    expect(reachedAncestor).toBe(0);
  });

  it('renders the same icon ligature pinned or not', () => {
    // Pinned that reads as unpinned: the class comment promises a filled icon
    // when pinned and an outlined one when ephemeral, but both arms of the
    // template render `push_pin`. Only colour and a 45-degree rotation
    // separate the two states. Pinned here rather than fixed.
    mount(EPHEMERAL);
    const ephemeralLigature = host.querySelector('mat-icon')?.textContent?.trim();

    mount(PINNED);
    const pinnedLigature = host.querySelector('mat-icon')?.textContent?.trim();

    expect(ephemeralLigature).toBe('push_pin');
    expect(pinnedLigature).toBe('push_pin');
  });
});
