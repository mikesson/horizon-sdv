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

import {inject, Service} from '@angular/core';
import {LoggingService} from '../../../shared/services';
import type {KeyEvent, TouchEvent} from './input.types';

/**
 * Registered data channels for a device session.
 */
export interface DeviceChannels {
  input: RTCDataChannel;
  control: RTCDataChannel;
}

/**
 * Sends touch and control input to Cuttlefish devices over the WebRTC data
 * channels a streaming session registers, formatted per the Cuttlefish
 * input protocol.
 */
@Service()
export class DeviceInputService {
  private readonly logger = inject(LoggingService);

  private readonly channels = new Map<string, DeviceChannels>();

  /**
   * Registers data channels for a device session.
   */
  registerSession(deviceId: string, channels: DeviceChannels): void {
    this.channels.set(deviceId, channels);
    this.logger.debug('DeviceInput', `Registered channels for ${deviceId}`);
  }

  /**
   * Unregisters channels for a device session.
   */
  unregisterSession(deviceId: string): void {
    this.channels.delete(deviceId);
    this.logger.debug('DeviceInput', `Unregistered channels for ${deviceId}`);
  }

  /**
   * Sends a touch event to the device.
   *
   * Uses Cuttlefish's multi-touch format:
   * `{type: "multi-touch", id: [...], x: [...], y: [...], down: 0|1, device_label: "display_N"}`
   *
   * @returns true if sent successfully
   */
  sendTouch(deviceId: string, event: TouchEvent): boolean {
    const inputChannel = this.getInputChannel(deviceId);
    if (!inputChannel) {
      return false;
    }

    const displayLabel = this.getDisplayLabel(event.displayLabel, event.displayId);
    const message = JSON.stringify({
      type: 'multi-touch',
      id: [event.pointerId ?? 0],
      x: [Math.round(event.x)],
      y: [Math.round(event.y)],
      down: event.type === 'up' ? 0 : 1,
      device_label: displayLabel,
    });

    inputChannel.send(message);
    return true;
  }

  /**
   * Sends a keyboard event to the device.
   *
   * Mirrors the upstream Cuttlefish web client:
   * `{type: "keyboard", keycode: "<DOM code>", event_type: "keydown"|"keyup"}`.
   * `keycode` carries the DOM code string, not a numeric key code.
   *
   * @returns true if sent successfully
   */
  sendKey(deviceId: string, event: KeyEvent): boolean {
    const inputChannel = this.getInputChannel(deviceId);
    if (!inputChannel) {
      return false;
    }

    const message = JSON.stringify({
      type: 'keyboard',
      keycode: event.code,
      event_type: event.type === 'down' ? 'keydown' : 'keyup',
    });

    inputChannel.send(message);
    return true;
  }

  /**
   * Sends a raw control message to the device.
   *
   * @returns true if sent successfully
   */
  sendControlMessage(deviceId: string, message: Record<string, unknown>): boolean {
    const controlChannel = this.getControlChannel(deviceId);
    if (!controlChannel) {
      this.logger.warn('DeviceInput', 'Control channel not available');
      return false;
    }

    const jsonMessage = JSON.stringify(message);
    this.logger.debug('DeviceInput', 'Sending control message', message);
    controlChannel.send(jsonMessage);
    return true;
  }

  /**
   * Sends a refresh_display command to activate a display for input.
   *
   * @returns true if sent successfully
   */
  sendRefreshDisplay(deviceId: string, displayNumber: number): boolean {
    return this.sendControlMessage(deviceId, {
      command: 'display',
      refresh_display: displayNumber,
    });
  }

  /**
   * Sends an add-display command to create a new display.
   *
   * @param dpi - Display DPI (default: 320)
   * @param refreshRateHz - Refresh rate in Hz (default: 60)
   * @returns true if sent successfully
   */
  addDisplay(
    deviceId: string,
    width: number,
    height: number,
    dpi = 320,
    refreshRateHz = 60
  ): boolean {
    return this.sendControlMessage(deviceId, {
      command: 'add-display',
      width,
      height,
      dpi,
      refresh_rate_hz: refreshRateHz,
    });
  }

  /**
   * Sends a remove-display command to remove a display.
   *
   * @param displayNumber - The bare display number; the device passes it as
   *   a crosvm display id, so it must NOT be prefixed with "display_".
   * @returns true if sent successfully
   */
  removeDisplay(deviceId: string, displayNumber: number): boolean {
    return this.sendControlMessage(deviceId, {
      command: 'remove-display',
      display_id: String(displayNumber),
    });
  }

  /**
   * Gets the input channel for a device if available and open.
   */
  private getInputChannel(deviceId: string): RTCDataChannel | null {
    const deviceChannels = this.channels.get(deviceId);
    if (!deviceChannels?.input) {
      this.logger.warn('DeviceInput', 'No input channel for device', {deviceId});
      return null;
    }
    if (deviceChannels.input.readyState !== 'open') {
      this.logger.warn('DeviceInput', 'Input channel not open', {
        readyState: deviceChannels.input.readyState,
      });
      return null;
    }
    return deviceChannels.input;
  }

  /**
   * Gets the control channel for a device if available and open.
   */
  private getControlChannel(deviceId: string): RTCDataChannel | null {
    const deviceChannels = this.channels.get(deviceId);
    if (!deviceChannels?.control) {
      return null;
    }
    if (deviceChannels.control.readyState !== 'open') {
      return null;
    }
    return deviceChannels.control;
  }

  /**
   * Gets the display label from label or ID.
   */
  private getDisplayLabel(label?: string, displayId?: number): string {
    if (label) {
      return label;
    }
    if (displayId !== undefined) {
      return `display_${displayId}`;
    }
    return 'display_0';
  }
}
