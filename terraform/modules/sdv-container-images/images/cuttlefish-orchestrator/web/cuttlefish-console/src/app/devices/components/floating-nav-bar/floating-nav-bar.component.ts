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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {Adb} from '@yume-chan/adb';
import {LoggingService} from '../../../shared/services';
import {AdbService} from '../../services/adb/adb.service';
import {exec} from '../../services/adb/adb-exec';
import {DeviceInputService} from '../../services/input/device-input.service';

/** How a requested screenshot should be taken. */
export type ScreenshotKind = 'focused' | 'all' | 'exact';

/**
 * Device control sidebar for Android device interaction.
 *
 * Provides system navigation buttons (Power, Home, Back, Recent Apps),
 * screenshot capture, screen rotation, and volume controls.
 * Always visible as a vertical sidebar on the left side of the video.
 *
 * @example
 * ```html
 * <app-floating-nav-bar
 *   [deviceId]="device.id"
 *   (screenshotRequested)="onScreenshotRequested($event)"
 * />
 * ```
 */
@Component({
  selector: 'app-floating-nav-bar',
  imports: [MatIconModule, MatButtonModule, MatMenuModule, MatTooltipModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    '(document:keydown)': 'onKeyDown($event)',
  },
  template: `
    <div class="control-sidebar">
      <!-- Power -->
      <button type="button"
        mat-icon-button
        (click)="power()"
        matTooltip="Power (Alt+P)"
        matTooltipPosition="right"
        class="control-button" aria-label="Power (Alt+P)">
        <mat-icon>power_settings_new</mat-icon>
      </button>

      <div class="separator"></div>

      <!-- Navigation -->
      <button type="button"
        mat-icon-button
        (click)="back()"
        matTooltip="Back (Alt+B)"
        matTooltipPosition="right"
        class="control-button" aria-label="Back (Alt+B)">
        <mat-icon>arrow_back</mat-icon>
      </button>
      <button type="button"
        mat-icon-button
        (click)="home()"
        matTooltip="Home (Alt+H)"
        matTooltipPosition="right"
        class="control-button" aria-label="Home (Alt+H)">
        <mat-icon>home</mat-icon>
      </button>
      <button type="button"
        mat-icon-button
        (click)="recent()"
        matTooltip="Recent Apps (Alt+R)"
        matTooltipPosition="right"
        class="control-button" aria-label="Recent Apps (Alt+R)">
        <mat-icon>menu</mat-icon>
      </button>

      <div class="separator"></div>

      <!-- Rotation -->
      <button type="button"
        mat-icon-button
        (click)="rotateLeft()"
        matTooltip="Rotate Left"
        matTooltipPosition="right"
        class="control-button"
        [disabled]="rotating()" aria-label="Rotate Left">
        <mat-icon>rotate_left</mat-icon>
      </button>
      <button type="button"
        mat-icon-button
        (click)="rotate()"
        matTooltip="Rotate Right (Alt+O)"
        matTooltipPosition="right"
        class="control-button"
        [disabled]="rotating()" aria-label="Rotate Right (Alt+O)">
        <mat-icon>rotate_right</mat-icon>
      </button>

      <div class="separator"></div>

      <!-- Volume -->
      <button type="button"
        mat-icon-button
        (click)="volumeUp()"
        matTooltip="Volume Up"
        matTooltipPosition="right"
        class="control-button" aria-label="Volume Up">
        <mat-icon>volume_up</mat-icon>
      </button>
      <button type="button"
        mat-icon-button
        (click)="volumeDown()"
        matTooltip="Volume Down"
        matTooltipPosition="right"
        class="control-button" aria-label="Volume Down">
        <mat-icon>volume_down</mat-icon>
      </button>
      <button type="button"
        mat-icon-button
        (click)="mute()"
        matTooltip="Mute"
        matTooltipPosition="right"
        class="control-button" aria-label="Mute">
        <mat-icon>volume_off</mat-icon>
      </button>

      <div class="separator"></div>

      <!-- Screenshot: click = instant frame capture; menu = power options -->
      <button type="button"
        mat-icon-button
        (click)="screenshotRequested.emit('focused')"
        matTooltip="Screenshot (Alt+S)"
        matTooltipPosition="right"
        class="control-button"
        [disabled]="screenshotBusy()" aria-label="Screenshot (Alt+S)">
        <mat-icon>photo_camera</mat-icon>
      </button>
      <button type="button"
        mat-icon-button
        [matMenuTriggerFor]="screenshotMenu"
        matTooltip="Screenshot options"
        matTooltipPosition="right"
        class="control-button screenshot-options"
        [disabled]="screenshotBusy()" aria-label="Screenshot options">
        <mat-icon>expand_more</mat-icon>
      </button>
      <mat-menu #screenshotMenu="matMenu" class="dark-menu-panel">
        <button type="button" mat-menu-item (click)="screenshotRequested.emit('all')">
          <mat-icon>photo_library</mat-icon>
          <span>Capture all displays</span>
        </button>
        <button type="button" mat-menu-item (click)="screenshotRequested.emit('exact')">
          <mat-icon>high_quality</mat-icon>
          <span>Pixel-exact (via ADB)</span>
        </button>
      </mat-menu>
    </div>
  `,
  styles: `
      :host {
        display: block;
        position: absolute;
        left: 8px;
        top: 50%;
        transform: translateY(-50%);
        z-index: 100;
      }

      .control-sidebar {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 2px;
        padding: 8px 4px;
        background: rgba(30, 58, 95, 0.95);
        border-radius: 8px;
        border: 1px solid rgba(61, 106, 151, 0.6);
        box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
      }

      .separator {
        width: 28px;
        height: 1px;
        background: rgba(255, 255, 255, 0.15);
        margin: 4px 0;
      }

      .control-button {
        width: 36px;
        height: 36px;
        color: rgba(255, 255, 255, 0.85);
        transition: background-color var(--cco-duration-base) ease, color var(--cco-duration-base) ease;
      }

      .control-button:hover:not(:disabled) {
        color: var(--cco-text);
        background: rgba(255, 255, 255, 0.15);
      }

      .control-button:active:not(:disabled) {
        background: rgba(255, 255, 255, 0.25);
      }

      .control-button:disabled {
        color: rgba(255, 255, 255, 0.3);
      }

      .control-button mat-icon {
        font-size: 20px;
        width: 20px;
        height: 20px;
      }
    `,
})
export class FloatingNavBarComponent {
  private readonly adbService = inject(AdbService);
  private readonly inputService = inject(DeviceInputService);
  private readonly logger = inject(LoggingService);

  readonly deviceId = input.required<string>();
  readonly instance = input.required<string>();

  /** The Android VM this panel talks to; ADB sessions are per instance. */
  private readonly target = computed(() => ({
    deviceId: this.deviceId(),
    instance: this.instance(),
  }));

  /** True while the player is executing a requested capture. */
  readonly screenshotBusy = input(false);

  readonly screenshotRequested = output<ScreenshotKind>();

  readonly rotating = signal(false);

  /**
   * Send a control panel button command via WebRTC control channel.
   * This matches the Host Orchestrator implementation.
   *
   * The Cuttlefish device expects: {command: string, button_state: 'down'|'up'}
   */
  private sendControlButton(command: string, state: 'down' | 'up'): void {
    const success = this.inputService.sendControlMessage(this.deviceId(), {
      command,
      button_state: state,
    });
    if (!success) {
      this.logger.warn('ControlSidebar', `Control channel not available for ${command}`);
    }
  }

  private pressControlButton(command: string): void {
    this.sendControlButton(command, 'down');
    // Small delay before releasing
    setTimeout(() => {
      this.sendControlButton(command, 'up');
    }, 50);
  }

  home(): void {
    this.pressControlButton('home');
  }

  back(): void {
    this.pressControlButton('back');
  }

  recent(): void {
    this.pressControlButton('menu');
  }

  power(): void {
    this.pressControlButton('power');
  }

  volumeUp(): void {
    this.pressControlButton('volumeup');
  }

  volumeDown(): void {
    this.pressControlButton('volumedown');
  }

  mute(): void {
    // Volume mute is handled differently - it's a local audio toggle in HO
    // For now, send volumedown as a substitute
    this.pressControlButton('volumedown');
  }

  /** The device player page holds the ADB claim; this only joins it. */
  private connectedDevice(): Promise<Adb> {
    const session = this.adbService.session(this.target())();
    if (!session) {
      return Promise.reject(new Error('ADB session not available'));
    }
    return session.ensureConnected();
  }

  /** Current rotation state (0=portrait, 1=landscape, 2=reverse portrait, 3=reverse landscape). */
  private currentRotation = 0;

  /**
   * Rotates the screen 90 degrees clockwise.
   * Uses Cuttlefish sensor injection like Host Orchestrator.
   */
  async rotate(): Promise<void> {
    if (this.rotating()) {
      return;
    }

    this.rotating.set(true);

    try {
      // Rotate 90 degrees clockwise (0 -> 1 -> 2 -> 3 -> 0)
      this.currentRotation = (this.currentRotation + 1) % 4;

      // Use Cuttlefish sensor injection command (same as Host Orchestrator)
      await exec(
        await this.connectedDevice(),
        `/vendor/bin/cuttlefish_sensor_injection rotate ${this.currentRotation}`
      );
    } catch (error) {
      this.logger.error('ControlSidebar', 'Rotate failed', error);
    } finally {
      this.rotating.set(false);
    }
  }

  /**
   * Rotates the screen 90 degrees counter-clockwise.
   * Uses Cuttlefish sensor injection like Host Orchestrator.
   */
  async rotateLeft(): Promise<void> {
    if (this.rotating()) {
      return;
    }

    this.rotating.set(true);

    try {
      // Rotate 90 degrees counter-clockwise (0 -> 3 -> 2 -> 1 -> 0)
      this.currentRotation = (this.currentRotation + 3) % 4;

      // Use Cuttlefish sensor injection command (same as Host Orchestrator)
      await exec(
        await this.connectedDevice(),
        `/vendor/bin/cuttlefish_sensor_injection rotate ${this.currentRotation}`
      );
    } catch (error) {
      this.logger.error('ControlSidebar', 'Rotate Left failed', error);
    } finally {
      this.rotating.set(false);
    }
  }

  protected onKeyDown(event: KeyboardEvent): void {
    // Only handle Alt+key combinations
    if (!event.altKey) {
      return;
    }

    switch (event.key.toLowerCase()) {
      case 'h':
        event.preventDefault();
        this.home();
        break;
      case 'b':
        event.preventDefault();
        this.back();
        break;
      case 'r':
        event.preventDefault();
        this.recent();
        break;
      case 's':
        event.preventDefault();
        this.screenshotRequested.emit('focused');
        break;
      case 'o':
        event.preventDefault();
        void this.rotate();
        break;
      case 'p':
        event.preventDefault();
        this.power();
        break;
      default:
        break;
    }
  }
}
