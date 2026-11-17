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
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {FleetSchedule} from '../../models/control-plane.model';

/** Weekday labels in `time.Weekday` order, which is what the wire carries. */
const WEEKDAYS = [
  {day: 0, label: 'Sun'},
  {day: 1, label: 'Mon'},
  {day: 2, label: 'Tue'},
  {day: 3, label: 'Wed'},
  {day: 4, label: 'Thu'},
  {day: 5, label: 'Fri'},
  {day: 6, label: 'Sat'},
];

/**
 * The IANA zones this browser knows, which is the same database the backend
 * validates against. A picker rather than a text field because the server's
 * refusal of an unknown zone arrives as a 400 after Save, and a typo in a zone
 * name is otherwise indistinguishable from a zone that was renamed.
 */
function timeZones(): string[] {
  const supported = Intl.supportedValuesOf('timeZone');
  return supported.includes('UTC') ? supported : ['UTC', ...supported];
}

/** The browser's own zone, which is the right guess for a window someone is
 * adding while looking at their own working day. Never 'Local', which names a
 * server property the backend refuses, because it would mean different instants
 * in different places. */
function localZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
}

/**
 * The schedule windows of one tier, edited in place.
 *
 * Fully controlled: every change emits a NEW array rather than mutating the one
 * it was given. The draft is compared against the server's copy structurally, so
 * mutating in place would leave both sides equal and Save would never appear.
 */
@Component({
  selector: 'app-schedule-editor',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSelectModule,
    MatTooltipModule,
  ],
  template: `
    @for (window of schedules(); track $index) {
      <div class="window">
        <div class="window-head">
          <mat-form-field appearance="outline" subscriptSizing="dynamic" class="grow">
            <mat-label>Name</mat-label>
            <input
              matInput
              [value]="window.name"
              (input)="rename($index, $event)">
          </mat-form-field>
          <button
            type="button"
            mat-icon-button
            matTooltip="Remove this window"
            [attr.aria-label]="'Remove ' + window.name"
            (click)="remove($index)">
            <mat-icon>delete</mat-icon>
          </button>
        </div>

        <div class="window-row">
          <mat-form-field appearance="outline" subscriptSizing="dynamic" class="narrow">
            <mat-label>Hosts</mat-label>
            <input
              matInput
              type="number"
              min="0"
              [max]="maxHosts()"
              [value]="window.hosts"
              (input)="setHosts($index, $event)">
          </mat-form-field>

          <mat-form-field appearance="outline" subscriptSizing="dynamic" class="narrow">
            <mat-label>Opens at</mat-label>
            <input
              matInput
              type="time"
              [value]="window.start"
              (input)="setStart($index, $event)">
          </mat-form-field>

          <mat-form-field appearance="outline" subscriptSizing="dynamic" class="narrow">
            <mat-label>For (minutes)</mat-label>
            <input
              matInput
              type="number"
              min="1"
              max="10080"
              [value]="window.durationSec / 60"
              (input)="setDuration($index, $event)">
          </mat-form-field>

          <mat-form-field appearance="outline" subscriptSizing="dynamic" class="grow">
            <mat-label>Time zone</mat-label>
            <mat-select
              [value]="window.location"
              (valueChange)="setLocation($index, $event)">
              @for (zone of zones; track zone) {
                <mat-option [value]="zone">{{ zone }}</mat-option>
              }
            </mat-select>
          </mat-form-field>
        </div>

        <div class="days">
          @for (weekday of weekdays; track weekday.day) {
            <button
              type="button"
              class="day"
              [class.on]="window.days.includes(weekday.day)"
              [attr.aria-pressed]="window.days.includes(weekday.day)"
              (click)="toggleDay($index, weekday.day)">
              {{ weekday.label }}
            </button>
          }
          <span class="days-note">{{ daysNote(window) }}</span>
        </div>
      </div>
    }

    <div class="actions">
      <button type="button" mat-stroked-button [disabled]="!canAdd()" (click)="add()">
        <mat-icon>add</mat-icon>
        Add a window
      </button>
      <span class="count">{{ countNote() }}</span>
    </div>
  `,
  styles: `
    .window {
      padding: var(--cco-space-12) var(--cco-space-16);
      margin-bottom: var(--cco-space-12);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
    }

    .window-head,
    .window-row {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
    }

    .window-row {
      flex-wrap: wrap;
      margin-top: var(--cco-space-8);
    }

    mat-form-field.grow {
      flex: 1;
      min-width: 200px;
    }

    mat-form-field.narrow {
      width: 130px;
    }

    .days {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      margin-top: var(--cco-space-12);
      flex-wrap: wrap;
    }

    .day {
      padding: var(--cco-space-4) var(--cco-space-12);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
      background: transparent;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
      cursor: pointer;
    }

    .day.on {
      border-color: var(--cco-accent);
      color: var(--cco-text);
      font-weight: 600;
    }

    .days-note,
    .count {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .actions {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
    }
  `,
})
export class ScheduleEditorComponent {
  readonly schedules = input.required<FleetSchedule[]>();
  readonly maxSchedules = input.required<number>();
  readonly maxHosts = input.required<number>();
  readonly schedulesChange = output<FleetSchedule[]>();

  protected readonly weekdays = WEEKDAYS;
  protected readonly zones = timeZones();

  protected readonly canAdd = computed(() => this.schedules().length < this.maxSchedules());

  protected readonly countNote = computed(() => {
    const used = this.schedules().length;
    return used === 0
      ? 'No windows. Machines are kept free by the number you set and by what demand showed.'
      : `${used} of ${this.maxSchedules()} windows`;
  });

  /** Empty days is every day, which is the stored meaning rather than a gap. */
  protected daysNote(window: FleetSchedule): string {
    return window.days.length === 0 ? 'every day' : '';
  }

  protected add(): void {
    this.schedulesChange.emit([
      ...this.schedules(),
      {
        name: 'New window',
        hosts: 1,
        start: '09:00',
        durationSec: 3600,
        days: [],
        location: localZone(),
      },
    ]);
  }

  protected remove(index: number): void {
    this.schedulesChange.emit(this.schedules().filter((_, i) => i !== index));
  }

  protected rename(index: number, event: Event): void {
    this.patch(index, {name: (event.target as HTMLInputElement).value});
  }

  protected setHosts(index: number, event: Event): void {
    this.patch(index, {hosts: Number((event.target as HTMLInputElement).value)});
  }

  protected setStart(index: number, event: Event): void {
    this.patch(index, {start: (event.target as HTMLInputElement).value});
  }

  /** Minutes on the surface, seconds on the wire. The bound is a minute, so
   * the conversion is exact in both directions and never rounds. */
  protected setDuration(index: number, event: Event): void {
    this.patch(index, {durationSec: Number((event.target as HTMLInputElement).value) * 60});
  }

  protected setLocation(index: number, zone: string): void {
    this.patch(index, {location: zone});
  }

  protected toggleDay(index: number, day: number): void {
    const days = this.schedules()[index].days;
    // Numeric sort explicitly. The default comparator is lexicographic, which
    // happens to be right for 0-6 and would stop being right the moment this
    // list held anything wider.
    const next = days.includes(day)
      ? days.filter(d => d !== day)
      : [...days, day].sort((a, b) => a - b);
    this.patch(index, {days: next});
  }

  private patch(index: number, change: Partial<FleetSchedule>): void {
    this.schedulesChange.emit(
      this.schedules().map((window, i) => (i === index ? {...window, ...change} : window))
    );
  }
}
