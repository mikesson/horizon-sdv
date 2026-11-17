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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {DeviceInstance} from '../../models/device.model';
import {InstanceMarkComponent} from '../instance-mark/instance-mark.component';

/** The interaction verbs a single Android VM answers. */
export type InstanceVerb =
  | 'restart'
  | 'powerwash'
  | 'powerbtn'
  | 'screenshot'
  | 'record-start'
  | 'record-stop';

/** The operation currently holding the device, as the operation row records it. */
export interface InstanceBusy {
  /** The verb's label, e.g. `powerwash`. */
  readonly verb: string;
  /** The instance it is actuating; absent for a device-wide verb. */
  readonly instance?: string;
}

/**
 * One Android VM of a device's CVD group, as the host reported it.
 *
 * Everything on the tile is observed, never desired: the name, the wire state,
 * the host's own rendering of the instance's displays. The per-instance shape
 * from the create request is absent. Matching a desired-spec array to an
 * observed one by position is the assumption this whole programme exists to
 * remove, and the display geometry already tells a watch from a phone.
 *
 * Distinct from `instance-card/`, which is create-flow draft state and shares
 * nothing with this but the noun.
 */
@Component({
  selector: 'app-instance-tile',
  imports: [
    InstanceMarkComponent,
    MatButtonModule,
    MatIconModule,
    MatMenuModule,
    MatProgressSpinnerModule,
    MatTooltipModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let inst = instance();
    <!-- The tile is a container, not a control: it holds two independent
         actions (scope the page to this instance, act on it), and one button
         nested inside another is neither valid ARIA nor reachable by keyboard.
         The name is the selector; the overflow is its sibling. -->
    <div class="tile" [class.selected]="selected()">
      <div class="tile-head">
        <button
          type="button"
          class="name"
          [attr.aria-pressed]="selected()"
          (click)="tileSelect.emit()"
        >
          {{ inst.name }}
        </button>
        @if (showVerbs()) {
          <button
            type="button"
            mat-icon-button
            class="tile-menu"
            [matMenuTriggerFor]="verbs"
            [disabled]="!!busy()"
            matTooltip="Instance actions"
            aria-label="Instance actions"
          >
            <mat-icon>more_vert</mat-icon>
          </button>
        }
        <mat-menu #verbs="matMenu">
          <button type="button" mat-menu-item (click)="openPlayer.emit()">
            <mat-icon>play_circle</mat-icon>
            <span>Open in player</span>
          </button>
          <button type="button" mat-menu-item (click)="verb.emit('restart')">
            <mat-icon>restart_alt</mat-icon>
            <span>Restart</span>
          </button>
          <button type="button" mat-menu-item (click)="verb.emit('powerwash')">
            <mat-icon>restore</mat-icon>
            <span>Powerwash</span>
          </button>
          <button type="button" mat-menu-item (click)="verb.emit('powerbtn')">
            <mat-icon>power_settings_new</mat-icon>
            <span>Power button</span>
          </button>
          <button type="button" mat-menu-item (click)="verb.emit('screenshot')">
            <mat-icon>photo_camera</mat-icon>
            <span>Screenshot</span>
          </button>
          <button type="button" mat-menu-item (click)="verb.emit('record-start')">
            <mat-icon>videocam</mat-icon>
            <span>Start recording</span>
          </button>
          <button type="button" mat-menu-item (click)="verb.emit('record-stop')">
            <mat-icon>videocam_off</mat-icon>
            <span>Stop recording</span>
          </button>
        </mat-menu>
      </div>

      <div class="state">
        <app-instance-mark [state]="inst.state" />
      </div>

      <!-- A wait that names what holds it reads as a system working; an
           anonymous disabled menu reads as a broken one. Written on the tile
           rather than in the menu's tooltip, because a tooltip on a disabled
           button never fires. -->
      @if (busyLine(); as line) {
        <div class="held" [class.waiting]="!heldHere()">
          @if (heldHere()) {
            <mat-spinner diameter="14" />
          }
          <span>{{ line }}</span>
        </div>
      }

      <p class="displays">{{ displayLine() }}</p>

      <!-- The port is informational. The host firewall opens the first
           instance's only, and every instance is reached through the
           operator's WebSocket instead. So it is shown, never offered as a
           copyable adb connect string. -->
      <p class="adb">
        @if (inst.adbPort) {
          adb port {{ inst.adbPort }}
        } @else {
          adb port unreported
        }
      </p>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }

    .tile {
      display: flex;
      flex-direction: column;
      gap: 6px;
      padding: 12px;
      min-width: 168px;
      border: 1px solid var(--cco-border);
      border-radius: 12px;
      background: var(--cco-surface);
    }

    .tile.selected {
      border-color: var(--cco-accent);
      background: var(--cco-accent-surface);
    }

    .tile-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
    }

    .name {
      flex: 1;
      min-width: 0;
      padding: 0;
      background: none;
      border: none;
      color: inherit;
      font: inherit;
      font-weight: 500;
      text-align: left;
      cursor: pointer;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .name:hover {
      color: var(--cco-accent);
    }

    .tile-menu {
      flex-shrink: 0;
    }

    .state {
      display: flex;
      align-items: center;
      font-size: 12px;
    }

    .held {
      display: flex;
      align-items: center;
      gap: 6px;
      font-size: 12px;
      color: var(--cco-accent);
    }

    .held.waiting {
      color: var(--cco-text-secondary);
    }

    .displays,
    .adb {
      margin: 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    /* Ports and geometries sit in a column of tiles; proportional digits make
       the grid shimmy every time a value changes. */
    .adb {
      font-variant-numeric: tabular-nums;
    }
  `,
})
export class InstanceTileComponent {
  readonly instance = input.required<DeviceInstance>();
  readonly selected = input(false);

  /**
   * The operation in flight on this device, if any. Admission allows one
   * exclusive operation per device, not per instance, so a live restart on any
   * sibling closes the menu on all of them, which is the truth and better
   * than a 409 toast after the click.
   */
  readonly busy = input<InstanceBusy | null>(null);

  /**
   * Whether the tile offers the per-instance verbs.
   *
   * Withheld where the caller cannot see what holds them. Admission allows one
   * exclusive operation per device, and a menu that cannot know an operation is
   * running is a menu that hands the user a 409. A surface without the
   * operation feed shows the tile and routes to the page that has it, instead
   * of offering a verb it cannot accurately enable or disable.
   */
  readonly showVerbs = input(true);

  readonly tileSelect = output<void>();
  readonly openPlayer = output<void>();
  readonly verb = output<InstanceVerb>();

  /** Whether the in-flight verb is actuating this tile's own instance. */
  protected readonly heldHere = computed(
    () => !!this.busy() && this.busy()?.instance === this.instance().name
  );

  /**
   * Why this tile's menu is closed: the verb, and where it is running. A
   * device-wide verb names no instance, which is accurate. It really is passing
   * every one of them through.
   */
  protected readonly busyLine = computed(() => {
    const busy = this.busy();
    if (!busy) {
      return '';
    }
    if (busy.instance === this.instance().name) {
      return `${busy.verb} running`;
    }
    return busy.instance ? `waiting on ${busy.instance}` : `${busy.verb} running on this device`;
  });

  /**
   * The host's own rendering of the instance's displays, e.g.
   * "720 x 1280 ( 320 )". Passed through rather than reformatted. It is the
   * one string that distinguishes a watch from a phone at a glance, and
   * re-deriving it would only be a chance to get it wrong.
   */
  protected readonly displayLine = computed(() => {
    const displays = this.instance().displays ?? [];
    return displays.length > 0 ? displays.join(', ') : 'No displays reported';
  });
}
