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

import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import type {FleetSchedule} from '../../models/control-plane.model';

const DAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const HOURS = 24;

/** One cell of the grid: the highest floor any window puts on that hour. */
interface Cell {
  readonly day: number;
  readonly hour: number;
  readonly hosts: number;
}

/**
 * The week a schedule set produces, as a grid.
 *
 * A list of windows hides all three of its failure modes (overlaps, gaps, and
 * the daylight-saving shift that moves a window against UTC twice a year) and
 * every one of them shows up immediately when the week is drawn. Windows only
 * ever RAISE the free-machine count, so the number you set is the baseline they
 * rise above, and is drawn as the base of every cell.
 *
 * It renders the DRAFT, in each window's own clock. When windows disagree about
 * their zone, "overlap" on this grid means overlap in wall-clock terms, which
 * is not the same as overlap in real time. The server composes the live target,
 * and that number is served, never derived here.
 */
@Component({
  selector: 'app-schedule-week',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="grid" role="img" [attr.aria-label]="summary()">
      <span class="corner"></span>
      @for (label of dayLabels; track label) {
        <span class="day-label">{{ label }}</span>
      }
      @for (row of rows(); track row.hour) {
        <span class="hour-label">{{ row.label }}</span>
        @for (cell of row.cells; track cell.day) {
          <span
            class="cell"
            [class.raised]="cell.hosts > baseline()"
            [style.--cco-cell-fill]="fill(cell.hosts)"
            [title]="cellTitle(cell)"></span>
        }
      }
    </div>
    <p class="legend">{{ summary() }}</p>
    @if (zoneNote(); as note) {
      <p class="legend caveat">{{ note }}</p>
    }
    @for (hint of dstHints(); track hint) {
      <p class="legend caveat">{{ hint }}</p>
    }
  `,
  styles: `
    :host {
      display: block;
    }

    .grid {
      display: grid;
      grid-template-columns: 2.5rem repeat(7, 1fr);
      gap: 1px;
      max-width: 34rem;
    }

    .day-label,
    .hour-label {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
      font-variant-numeric: tabular-nums;
    }

    .day-label {
      text-align: center;
    }

    .hour-label {
      text-align: end;
      padding-right: var(--cco-space-4);
      line-height: 10px;
    }

    .cell {
      /* Declared with its empty default and overridden per cell by a style
         binding; see the note on the fleet band's notch span. */
      --cco-cell-fill: 0;

      height: 10px;
      background: var(--cco-surface-variant);
      border: 1px solid var(--cco-border);
    }

    /* Accent-surface blocks, never a hue of their own. The palette is spent on
       status, and a schedule window is not a status. */
    .cell.raised {
      background: color-mix(
        in srgb,
        var(--cco-accent) calc(var(--cco-cell-fill) * 1%),
        var(--cco-surface-variant)
      );
      border-color: var(--cco-accent);
    }

    .legend {
      margin: var(--cco-space-4) 0 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .caveat {
      color: var(--cco-warning);
    }
  `,
})
export class ScheduleWeekComponent {
  readonly schedules = input.required<FleetSchedule[]>();
  /** The floor windows rise above; they never lower the target below it. */
  readonly baseline = input.required<number>();

  protected readonly dayLabels = DAY_LABELS;

  private readonly cells = computed<Cell[]>(() => {
    const floor = this.baseline();
    const grid: Cell[] = [];
    for (let day = 0; day < DAY_LABELS.length; day++) {
      for (let hour = 0; hour < HOURS; hour++) {
        grid.push({day, hour, hosts: Math.max(floor, windowFloor(this.schedules(), day, hour))});
      }
    }
    return grid;
  });

  protected readonly rows = computed(() =>
    Array.from({length: HOURS}, (_, hour) => ({
      hour,
      label: hour % 6 === 0 ? `${String(hour).padStart(2, '0')}:00` : '',
      cells: this.cells().filter(cell => cell.hour === hour),
    }))
  );

  private readonly peak = computed(() =>
    this.cells().reduce((highest, cell) => Math.max(highest, cell.hosts), 0)
  );

  protected readonly summary = computed(() => {
    const windows = this.schedules().length;
    if (windows === 0) {
      return `No windows. ${this.baseline()} machines are kept free all week.`;
    }
    return `${windows} ${windows === 1 ? 'window' : 'windows'} · machines kept free reaches ${this.peak()} at its highest, above the ${this.baseline()} you set`;
  });

  /** Stated rather than hidden. With mixed zones the grid is wall-clock time. */
  protected readonly zoneNote = computed(() => {
    const zones = new Set(this.schedules().map(w => w.location));
    return zones.size > 1
      ? `Windows span ${zones.size} time zones, and this grid draws each in its own clock, so two blocks that look adjacent here may not be adjacent in real time.`
      : '';
  });

  /**
   * A window pinned to a zone that changes offset moves against UTC twice a
   * year, so the fleet warms an hour late every spring. Hinted per window rather
   * than computed into the grid, because the shift is real and which side of it
   * today falls on is not what an operator is deciding here.
   */
  protected readonly dstHints = computed(() =>
    [...new Set(this.schedules().map(w => w.location))]
      .filter(zone => shiftsWithinAYear(zone))
      .map(
        zone =>
          `${zone} changes its UTC offset during the year, so a window pinned to it opens an hour earlier or later against UTC after each transition.`
      )
  );

  protected fill(hosts: number): number {
    const peak = Math.max(1, this.peak());
    return Math.round((hosts / peak) * 60);
  }

  protected cellTitle(cell: Cell): string {
    return `${DAY_LABELS[cell.day]} ${String(cell.hour).padStart(2, '0')}:00 · floor ${cell.hosts}`;
  }
}

/** The highest floor any window puts on one (day, hour) of the week. */
function windowFloor(schedules: readonly FleetSchedule[], day: number, hour: number): number {
  let floor = 0;
  for (const window of schedules) {
    // An empty day list means EVERY day, which is the stored meaning rather
    // than a missing value to be read as none.
    if (window.days.length > 0 && !window.days.includes(day)) {
      continue;
    }
    const startHour = Number.parseInt(window.start.slice(0, 2), 10);
    const startMinute = Number.parseInt(window.start.slice(3, 5), 10);
    if (Number.isNaN(startHour) || Number.isNaN(startMinute)) {
      continue;
    }
    const startMinutes = startHour * 60 + startMinute;
    const cellMinutes = hour * 60;
    const elapsed = cellMinutes - startMinutes;
    // A window may run past midnight; the wrap is what makes an overnight
    // window show up on the grid at all rather than being clipped at the edge.
    const within = elapsed >= 0 && elapsed < window.durationSec / 60;
    const wrapped = elapsed < 0 && elapsed + 24 * 60 < window.durationSec / 60;
    if (within || wrapped) {
      floor = Math.max(floor, window.hosts);
    }
  }
  return floor;
}

/**
 * Whether a zone changes its UTC offset during the year. Sampled at midwinter
 * and midsummer, which is where every observed-offset rule differs; a zone with
 * no seasonal rule reports the same offset at both.
 */
function shiftsWithinAYear(zone: string): boolean {
  try {
    const year = new Date().getUTCFullYear();
    return offsetAt(zone, Date.UTC(year, 0, 15)) !== offsetAt(zone, Date.UTC(year, 6, 15));
  } catch {
    // An unknown zone is the schedule editor's problem and not this hint's. The
    // server refuses it on save and says so there.
    return false;
  }
}

function offsetAt(zone: string, at: number): string {
  return (
    new Intl.DateTimeFormat('en-US', {timeZone: zone, timeZoneName: 'longOffset'})
      .formatToParts(new Date(at))
      .find(part => part.type === 'timeZoneName')?.value ?? ''
  );
}
