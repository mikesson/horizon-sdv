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

import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import {LoggingService} from '../../../shared/services';
import {DeviceInputService} from './device-input.service';

function createMockDataChannel(): RTCDataChannel {
  return {
    readyState: 'open',
    send: vi.fn(),
  } as unknown as RTCDataChannel;
}

describe('DeviceInputService', () => {
  let service: DeviceInputService;
  const deviceId = 'test-device-123';

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        DeviceInputService,
        {
          provide: LoggingService,
          useValue: {
            debug: vi.fn(),
            info: vi.fn(),
            warn: vi.fn(),
            error: vi.fn(),
          },
        },
      ],
    });
    service = TestBed.inject(DeviceInputService);
  });

  describe('session management', () => {
    it('should register and unregister sessions', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();

      service.registerSession(deviceId, {input, control});
      expect(service.sendTouch(deviceId, {type: 'down', x: 0, y: 0})).toBe(true);

      service.unregisterSession(deviceId);
      expect(service.sendTouch(deviceId, {type: 'down', x: 0, y: 0})).toBe(false);
    });
  });

  describe('sendTouch', () => {
    it('should send touch event in Cuttlefish format', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      const result = service.sendTouch(deviceId, {
        type: 'down',
        x: 540,
        y: 960,
        pointerId: 0,
        displayId: 0,
      });

      expect(result).toBe(true);
      expect(input.send).toHaveBeenCalledWith(
        JSON.stringify({
          type: 'multi-touch',
          id: [0],
          x: [540],
          y: [960],
          down: 1,
          device_label: 'display_0',
        })
      );
    });

    it('should convert up event to down: 0', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      service.sendTouch(deviceId, {
        type: 'up',
        x: 100,
        y: 200,
      });

      expect(input.send).toHaveBeenCalledWith(expect.stringContaining('"down":0'));
    });

    it('should return false if no session registered', () => {
      const result = service.sendTouch('unknown', {type: 'down', x: 0, y: 0});
      expect(result).toBe(false);
    });

    it('should return false if channel not open', () => {
      const input: RTCDataChannel = {...createMockDataChannel(), readyState: 'closed'};
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      const result = service.sendTouch(deviceId, {type: 'down', x: 0, y: 0});
      expect(result).toBe(false);
    });
  });

  describe('sendKey', () => {
    it('sends the DOM code string, not a numeric key code', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      const result = service.sendKey(deviceId, {type: 'down', code: 'KeyA'});

      expect(result).toBe(true);
      expect(input.send).toHaveBeenCalledWith(
        JSON.stringify({
          type: 'keyboard',
          keycode: 'KeyA',
          event_type: 'keydown',
        })
      );
    });

    it('maps up to keyup', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      service.sendKey(deviceId, {type: 'up', code: 'Enter'});

      expect(input.send).toHaveBeenCalledWith(
        JSON.stringify({
          type: 'keyboard',
          keycode: 'Enter',
          event_type: 'keyup',
        })
      );
    });

    it('returns false without a registered session', () => {
      expect(service.sendKey('unknown', {type: 'down', code: 'KeyA'})).toBe(false);
    });
  });

  describe('sendControlMessage', () => {
    it('should send a control command on the control channel', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      const result = service.sendControlMessage(deviceId, {
        command: 'home',
        button_state: 'down',
      });

      expect(result).toBe(true);
      expect(control.send).toHaveBeenCalledWith(
        JSON.stringify({
          command: 'home',
          button_state: 'down',
        })
      );
    });
  });

  describe('display management', () => {
    it('should send add-display command', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      service.addDisplay(deviceId, 1080, 1920, 320, 60);

      expect(control.send).toHaveBeenCalledWith(
        JSON.stringify({
          command: 'add-display',
          width: 1080,
          height: 1920,
          dpi: 320,
          refresh_rate_hz: 60,
        })
      );
    });

    it('should send remove-display with the bare display number, not display_N', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      service.removeDisplay(deviceId, 1);

      // The device forwards display_id to crosvm, which expects a number.
      expect(control.send).toHaveBeenCalledWith(
        JSON.stringify({
          command: 'remove-display',
          display_id: '1',
        })
      );
    });

    it('should send refresh_display command', () => {
      const input = createMockDataChannel();
      const control = createMockDataChannel();
      service.registerSession(deviceId, {input, control});

      service.sendRefreshDisplay(deviceId, 0);

      expect(control.send).toHaveBeenCalledWith(
        JSON.stringify({
          command: 'display',
          refresh_display: 0,
        })
      );
    });
  });
});
