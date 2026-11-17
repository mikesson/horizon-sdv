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
 * Pins the player's input-capture semantics: keys forward to the device
 * while a display is focused (Escape releases capture, blur releases held
 * keys), and touches follow the upstream pointer-event model, with per-pointer
 * tracking and pointerleave/pointercancel releasing the touch so the
 * device never sees a stuck contact.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {LoggingService} from '../../../shared/services';
import {DeviceInputService} from '../../services/input/device-input.service';
import {VideoDisplayComponent} from './video-display.component';

describe('VideoDisplayComponent input capture', () => {
  let fixture: ComponentFixture<VideoDisplayComponent>;
  let host: HTMLElement;
  let sendKey: ReturnType<typeof vi.fn>;
  let sendTouch: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    sendKey = vi.fn(() => true);
    sendTouch = vi.fn(() => true);
    TestBed.configureTestingModule({
      imports: [VideoDisplayComponent],
      providers: [
        {provide: DeviceInputService, useValue: {sendKey, sendTouch}},
        {
          provide: LoggingService,
          useValue: {debug: vi.fn(), warn: vi.fn(), error: vi.fn()},
        },
      ],
    });
    fixture = TestBed.createComponent(VideoDisplayComponent);
    fixture.componentRef.setInput('deviceId', 'device-1');
    fixture.detectChanges();
    host = fixture.nativeElement as HTMLElement;
  });

  function key(type: 'keydown' | 'keyup', code: string, repeat = false): KeyboardEvent {
    const event = new KeyboardEvent(type, {code, repeat, cancelable: true});
    host.dispatchEvent(event);
    return event;
  }

  it('is focusable so a click can capture the keyboard', () => {
    expect(host.getAttribute('tabindex')).toBe('0');
  });

  // The click lives on the host, not an inner div, so the clickable surface is
  // the same element that is focusable and handles keys. The thumbnail strip in
  // multi-display-container relies on this output instead of wrapping each
  // preview in its own click handler, so a regression here silently stops
  // thumbnails switching the focused display.
  it('emits focusDisplay when the host is clicked', () => {
    fixture.componentRef.setInput('displayId', 3);
    fixture.detectChanges();

    const focused: number[] = [];
    fixture.componentInstance.focusDisplay.subscribe((id: number) => focused.push(id));

    host.dispatchEvent(new MouseEvent('click', {bubbles: true}));
    expect(focused).toEqual([3]);
  });

  it('forwards keydown and keyup with the DOM code and consumes the event', () => {
    const down = key('keydown', 'KeyA');
    const up = key('keyup', 'KeyA');

    expect(sendKey).toHaveBeenNthCalledWith(1, 'device-1', {type: 'down', code: 'KeyA'});
    expect(sendKey).toHaveBeenNthCalledWith(2, 'device-1', {type: 'up', code: 'KeyA'});
    expect(down.defaultPrevented).toBe(true);
    expect(up.defaultPrevented).toBe(true);
  });

  it('never forwards Escape, and releases capture instead', () => {
    const down = key('keydown', 'Escape');
    key('keyup', 'Escape');

    expect(sendKey).not.toHaveBeenCalled();
    // Escape must keep its browser behavior (dialog dismissal etc.).
    expect(down.defaultPrevented).toBe(false);
  });

  it('releases held keys on blur so the device never sees a stuck key', () => {
    key('keydown', 'KeyW');
    key('keydown', 'KeyW', true); // OS auto-repeat: forwarded, tracked once
    key('keydown', 'ShiftLeft');
    sendKey.mockClear();

    host.dispatchEvent(new Event('blur'));

    expect(sendKey.mock.calls).toEqual(
      expect.arrayContaining([
        ['device-1', {type: 'up', code: 'KeyW'}],
        ['device-1', {type: 'up', code: 'ShiftLeft'}],
      ])
    );
    expect(sendKey).toHaveBeenCalledTimes(2);
  });

  it('does not double-release keys the device already saw released', () => {
    key('keydown', 'KeyA');
    key('keyup', 'KeyA');
    sendKey.mockClear();

    host.dispatchEvent(new Event('blur'));

    expect(sendKey).not.toHaveBeenCalled();
  });

  describe('pointer capture', () => {
    let video: HTMLVideoElement;

    function pointer(type: string, pointerId: number): void {
      const event = new Event(type, {bubbles: true, cancelable: true});
      Object.assign(event, {pointerId, offsetX: 10, offsetY: 20});
      video.dispatchEvent(event);
    }

    beforeEach(() => {
      fixture.componentRef.setInput('focused', true);
      fixture.detectChanges();
      const element = host.querySelector('video');
      if (!element) {
        throw new Error('video element not rendered');
      }
      video = element;
    });

    it('sends down/move/up for a drag, carrying the pointer id', () => {
      pointer('pointerdown', 7);
      pointer('pointermove', 7);
      pointer('pointerup', 7);

      expect(sendTouch.mock.calls.map(call => call[1].type)).toEqual(['down', 'move', 'up']);
      expect(sendTouch.mock.calls.every(call => call[1].pointerId === 7)).toBe(true);
    });

    it('ignores hover moves that were never preceded by a down', () => {
      pointer('pointermove', 7);
      expect(sendTouch).not.toHaveBeenCalled();
    });

    it('releases the touch when the pointer leaves the element mid-drag', () => {
      pointer('pointerdown', 7);
      pointer('pointerleave', 7);
      pointer('pointerleave', 7);

      expect(sendTouch.mock.calls.map(call => call[1].type)).toEqual(['down', 'up']);
    });

    it('tracks simultaneous pointers independently', () => {
      pointer('pointerdown', 1);
      pointer('pointerdown', 2);
      pointer('pointerup', 1);
      pointer('pointermove', 2);

      expect(sendTouch.mock.calls.map(call => [call[1].pointerId, call[1].type])).toEqual([
        [1, 'down'],
        [2, 'down'],
        [1, 'up'],
        [2, 'move'],
      ]);
    });

    it('ignores pointerdown while the display is not focused', () => {
      fixture.componentRef.setInput('focused', false);
      fixture.detectChanges();

      pointer('pointerdown', 7);
      expect(sendTouch).not.toHaveBeenCalled();
    });
  });
});
