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
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatTooltipModule} from '@angular/material/tooltip';
import type {CapacityRegion, CapacityZone, ZoneCooldown} from '../../models/capacity.model';
import type {AdminZone} from '../../models/catalog-admin.model';
import {shapesLabel, shapesTooltip} from './zone-shapes';

/**
 * A cooldown cell plus where it sits. The region rides along because a quota
 * cell repels the whole region, and the confirm step has to name it.
 */
export interface CooldownTarget {
  zone: string;
  region: string;
  cooldown: ZoneCooldown;
}

/** One zone's enablement decision, as the toggle emits it. */
export interface ZoneToggle {
  zone: string;
  enabled: boolean;
}

interface CooldownAction {
  /**
   * The cell's identity. Tracking by label instead would rebuild the row, and
   * any open menu with it, on every refresh, because the label carries the
   * countdown.
   */
  key: string;
  label: string;
  cooldown: ZoneCooldown;
}

interface Tile {
  zone: CapacityZone;
  /** The verdict as one word plus what qualifies it ("Cooling · 41 min"). */
  word: string;
  counts: string;
  spotLine: string;
  band: 'high' | 'medium' | 'low' | 'none';
  shapes: string;
  shapesTooltip: string;
  /** Enablement as the catalog holds it, the model the toggle writes into. */
  enabled: boolean;
  /**
   * A zone the catalog snapshot does not hold cannot be written. The enabled
   * set is composed from the catalog's rows, so the write would silently drop
   * it. The toggle says so instead of dropping the click.
   */
  toggleHint: string;
  actions: CooldownAction[];
}

interface Rail {
  region: CapacityRegion;
  summary: string;
  chip: string;
  chipLevel: 'ok' | 'warn' | 'bad' | 'muted';
  tiles: Tile[];
}

/**
 * The zone board. One uniform tile per zone of the placement space, grouped by
 * slim region rails that carry the region's quota verdict, because Google
 * accounts quota per region, so it hits every zone under the rail.
 *
 * The stripe answers first and the word answers next. Colour is the
 * at-a-glance layer, and the verdict word beside the zone name is the
 * colour-blind and screen-reader layer. Both come off the server's verdict.
 */
@Component({
  selector: 'app-zone-board',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatIconModule, MatMenuModule, MatSlideToggleModule, MatTooltipModule],
  template: `
    @for (rail of rails(); track rail.region.region) {
      <div class="rail">
        <span class="rname mono">{{ rail.region.region }}</span>
        <span [class]="['chip', 'num', rail.chipLevel]">{{ rail.chip }}</span>
        <span class="rsum num">{{ rail.summary }}</span>
      </div>
      <div class="tiles">
        @for (tile of rail.tiles; track tile.zone.zone) {
          <div [class]="['tile', tile.zone.verdict]">
            <div class="trow">
              <span class="zname mono">{{ tile.zone.zone }}</span>
              <span class="spacer"></span>
              <span class="word" [title]="tile.zone.verdictDetail">{{ tile.word }}</span>
              @if (tile.actions.length > 0) {
                <button type="button"
                  mat-icon-button
                  class="tile-menu"
                  [matMenuTriggerFor]="cooldownMenu"
                  [attr.aria-label]="'Cooldown actions for ' + tile.zone.zone">
                  <mat-icon>more_vert</mat-icon>
                </button>
                <mat-menu #cooldownMenu>
                  @for (action of tile.actions; track action.key) {
                    <button type="button"
                      mat-menu-item
                      (click)="
                        clearCooldown.emit({
                          zone: tile.zone.zone,
                          region: tile.zone.region,
                          cooldown: action.cooldown,
                        })
                      ">
                      <mat-icon>ac_unit</mat-icon>
                      <span>{{ action.label }}</span>
                    </button>
                  }
                </mat-menu>
              }
            </div>
            <span class="count num">{{ tile.counts }}</span>
            <span class="sig">
              <span [class]="['dot', tile.band]"></span>
              {{ tile.spotLine }}
            </span>
            <div class="foot">
              <span class="shapes" [matTooltip]="tile.shapesTooltip">{{ tile.shapes }}</span>
              <mat-slide-toggle
                [checked]="tile.enabled"
                [disabled]="tile.toggleHint !== ''"
                [matTooltip]="tile.toggleHint"
                [attr.aria-label]="'Place into ' + tile.zone.zone"
                (change)="toggleZone.emit({zone: tile.zone.zone, enabled: $event.checked})" />
            </div>
            @if (tile.zone.verdictDetail) {
              <span class="why">{{ tile.zone.verdictDetail }}</span>
            }
          </div>
        }
      </div>
    }

    <div class="legend">
      <span><span class="swatch clear"></span>clear: place here</span>
      <span><span class="swatch cooling"></span>cooling: recent failure, timer shown</span>
      <span><span class="swatch blocked"></span>blocked: regional quota wall</span>
      <span><span class="swatch constrained"></span>constrained: Spot only, on-demand unaffected</span>
      <span><span class="swatch disabled"></span>disabled: operator choice</span>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }

    .num {
      font-variant-numeric: tabular-nums;
    }

    .mono {
      font-family: var(--cco-font-mono);
    }

    .rail {
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-8);
      flex-wrap: wrap;
      border-top: 1px solid var(--cco-border);
      padding-top: var(--cco-space-12);
      margin-top: var(--cco-space-4);
    }

    .rail:first-of-type {
      border-top: none;
      padding-top: 0;
      margin-top: 0;
    }

    .rname {
      font-size: var(--cco-text-body);
      font-weight: 500;
    }

    .rsum {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }

    .chip {
      display: inline-flex;
      align-items: center;
      gap: var(--cco-space-4);
      font-size: var(--cco-text-label);
      font-weight: 500;
      border-radius: var(--cco-radius-pill);
      padding: 1px 10px;
    }

    .chip.ok {
      color: var(--cco-success);
      background: var(--cco-success-surface);
    }

    .chip.warn {
      color: var(--cco-warning);
      background: var(--cco-warning-surface);
    }

    .chip.bad {
      color: var(--cco-error);
      background: var(--cco-error-surface);
    }

    .chip.muted {
      color: var(--cco-text-secondary);
      background: var(--cco-surface-variant);
    }

    .tiles {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
      gap: var(--cco-space-8);
      margin-top: var(--cco-space-8);
    }

    .tile {
      border: 1px solid var(--cco-border);
      border-left-width: 4px;
      border-left-color: var(--cco-text-disabled);
      border-radius: var(--cco-radius-sm);
      background: var(--cco-surface-variant);
      padding: var(--cco-space-8) var(--cco-space-12);
      display: grid;
      gap: var(--cco-space-4);
      align-content: start;
    }

    .tile.clear {
      border-left-color: var(--cco-success);
    }

    .tile.cooling {
      border-left-color: var(--cco-warning);
    }

    .tile.blocked {
      border-left-color: var(--cco-error);
    }

    .tile.constrained {
      border-left-color: var(--cco-accent);
    }

    .tile.disabled {
      color: var(--cco-text-secondary);
    }

    .trow {
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-8);
    }

    .zname {
      font-size: var(--cco-text-sm);
    }

    .spacer {
      flex: 1;
    }

    .word {
      font-size: var(--cco-text-label);
      font-weight: 500;
      font-variant-numeric: tabular-nums;
      letter-spacing: 0.03em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .tile.clear .word {
      color: var(--cco-success);
    }

    .tile.cooling .word {
      color: var(--cco-warning);
    }

    .tile.blocked .word {
      color: var(--cco-error);
    }

    .tile.constrained .word {
      color: var(--cco-accent);
    }

    /* Sized down to the row's rhythm. A default icon button is 40px and would
       set the tile header's height on its own. */
    .tile-menu {
      width: 24px;
      height: 24px;
      margin: -4px -8px -4px 0;
      color: var(--cco-text-secondary);

      mat-icon {
        font-size: var(--cco-icon-sm);
        width: var(--cco-icon-sm);
        height: var(--cco-icon-sm);
      }
    }

    .count {
      font-size: var(--cco-text-title);
      font-weight: 500;
    }

    .sig {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
      display: flex;
      align-items: center;
      gap: var(--cco-space-4);
    }

    .dot {
      width: 7px;
      height: 7px;
      border-radius: 50%;
      flex: none;
      background: transparent;
      border: 1px solid var(--cco-text-disabled);
    }

    .dot.high {
      background: var(--cco-success);
      border-color: var(--cco-success);
    }

    .dot.medium {
      background: var(--cco-warning);
      border-color: var(--cco-warning);
    }

    .dot.low {
      background: var(--cco-error);
      border-color: var(--cco-error);
    }

    .foot {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--cco-space-8);
      margin-top: var(--cco-space-2);
    }

    .shapes {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .why {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .legend {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
      display: flex;
      gap: var(--cco-space-16);
      flex-wrap: wrap;
      margin-top: var(--cco-space-16);
    }

    .legend span {
      display: inline-flex;
      align-items: center;
      gap: var(--cco-space-4);
    }

    .swatch {
      width: 10px;
      height: 10px;
      border-radius: 3px;
      flex: none;
    }

    .swatch.clear {
      background: var(--cco-success);
    }

    .swatch.cooling {
      background: var(--cco-warning);
    }

    .swatch.blocked {
      background: var(--cco-error);
    }

    .swatch.constrained {
      background: var(--cco-accent);
    }

    .swatch.disabled {
      background: var(--cco-text-disabled);
    }
  `,
})
export class ZoneBoardComponent {
  readonly regions = input.required<CapacityRegion[]>();
  readonly zones = input.required<CapacityZone[]>();
  /**
   * Every zone Compute Engine offers, with the curated shapes it stocks and the
   * enablement an operator has written. The toggle renders from here rather
   * than from the snapshot, because this is the model the write lands in.
   */
  readonly catalog = input.required<AdminZone[]>();
  /** The machine types a zone's offer is counted against, as the server curates them. */
  readonly curatedShapes = input.required<string[]>();
  /** Server time of the snapshot; cooldown countdowns are computed against it. */
  readonly serverNow = input.required<Date>();

  readonly clearCooldown = output<CooldownTarget>();
  readonly toggleZone = output<ZoneToggle>();

  private readonly catalogByZone = computed(
    () => new Map(this.catalog().map(zone => [zone.zone, zone]))
  );

  protected readonly rails = computed<Rail[]>(() => {
    const nowMs = this.serverNow().getTime();
    const catalog = this.catalogByZone();
    const curated = this.curatedShapes().length;
    return this.regions().map(region => ({
      region,
      summary: railSummary(region),
      chip: railChip(region),
      chipLevel: railChipLevel(region.quotaState),
      tiles: this.zones()
        .filter(zone => zone.region === region.region)
        .map(zone => tileOf(zone, nowMs, catalog.get(zone.zone), curated)),
    }));
  });
}

function railSummary(region: CapacityRegion): string {
  const zones =
    region.zonesEnabled === 1 ? '1 zone enabled' : `${region.zonesEnabled} zones enabled`;
  return `${region.machines} machines · ${region.devices} devices · ${zones}`;
}

function railChip(region: CapacityRegion): string {
  const pool = `${region.quotaPool} ${grouped(region.quotaUsage)} of ${grouped(region.quotaLimit)}`;
  switch (region.quotaState) {
    case 'comfortable':
      return 'quota comfortable';
    case 'tight':
      return `quota tight, ${pool}`;
    case 'blocked':
      return `quota spent, ${pool}`;
    default:
      return 'quota unreported';
  }
}

/** Thousands separators, matching the DecimalPipe the headroom bars use. */
function grouped(count: number): string {
  return count.toLocaleString('en-US');
}

function railChipLevel(state: CapacityRegion['quotaState']): Rail['chipLevel'] {
  switch (state) {
    case 'comfortable':
      return 'ok';
    case 'tight':
      return 'warn';
    case 'blocked':
      return 'bad';
    default:
      return 'muted';
  }
}

function tileOf(
  zone: CapacityZone,
  nowMs: number,
  catalog: AdminZone | undefined,
  curated: number
): Tile {
  return {
    zone,
    word: verdictWord(zone, nowMs),
    counts: countsLabel(zone),
    spotLine: spotLine(zone),
    band: zone.spot?.band ?? 'none',
    shapes: catalog ? shapesLabel(catalog, curated) : 'shapes unreported',
    shapesTooltip: catalog
      ? shapesTooltip(catalog)
      : 'The catalog snapshot does not hold this zone',
    enabled: catalog ? catalog.isEnabled : zone.enabled,
    toggleHint: catalog
      ? ''
      : 'The zone catalog has not reported this zone, so enablement cannot be written for it',
    actions: zone.cells.flatMap(cell => (cell.cooldown ? [cooldownAction(cell.cooldown)] : [])),
  };
}

/** The verdict as the tile states it, with the countdown a cooldown carries. */
function verdictWord(zone: CapacityZone, nowMs: number): string {
  switch (zone.verdict) {
    case 'clear':
      return 'Clear';
    case 'cooling':
      return zone.coolingUntil
        ? `Cooling · ${formatDuration(zone.coolingUntil.getTime() - nowMs)}`
        : 'Cooling';
    case 'blocked':
      return 'Blocked · quota';
    case 'constrained':
      return 'Constrained · Spot';
    case 'disabled':
      return 'Disabled';
    default:
      return 'No signal';
  }
}

/** Both terms, never a percentage. Machines and the devices they carry. */
function countsLabel(zone: CapacityZone): string {
  const machines = `${zone.machines} ${zone.machines === 1 ? 'machine' : 'machines'}`;
  const devices = `${zone.devices} ${zone.devices === 1 ? 'device' : 'devices'}`;
  const arriving = zone.arriving > 0 ? ` · ${zone.arriving} arriving` : '';
  return `${machines} · ${devices}${arriving}`;
}

function spotLine(zone: CapacityZone): string {
  const spot = zone.spot;
  if (!spot) {
    return 'Spot: no signal';
  }
  const uptime =
    spot.uptimeTierSeconds > 0 ? ` · about ${uptimeLabel(spot.uptimeTierSeconds)}` : '';
  return `Spot ${spot.band}${uptime}`;
}

/** The advice API's three tiers, as the durations they stand for. */
function uptimeLabel(seconds: number): string {
  if (seconds >= 3600) {
    return '60 min uptime';
  }
  return `${Math.round(seconds / 60)} min uptime`;
}

/** An active cooldown repels placement; a lapsed cell still decays the score. */
function cooldownAction(cd: ZoneCooldown): CooldownAction {
  // The countdown being over changes what is left to clear. What remains is the
  // failure count that decays the score, and no longer a timer.
  const what = cd.inCooldown ? 'cooldown' : 'failures';
  return {
    key: `${cd.machineFamily}/${cd.bindingMetric}`,
    label: `Clear the ${cd.machineFamily} ${cd.metricLabel} ${what}`,
    cooldown: cd,
  };
}

/** "45 sec" / "41 min". Cooldowns cap at an hour, so minutes carry the top. */
function formatDuration(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) {
    return `${seconds} sec`;
  }
  return `${Math.ceil(seconds / 60)} min`;
}
