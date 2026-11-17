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

import type {OnInit} from '@angular/core';
import {ChangeDetectionStrategy, Component, computed, inject} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar} from '@angular/material/snack-bar';
import {DeviceApiService} from '../../../devices/services/device-api.service';
import {HostApiService} from '../../../hosts/services/host-api.service';
import {QuotaService} from '../../../quota/services/quota.service';
import type {ConfirmDialogData} from '../../../shared/components';
import {
  ConfirmDialogComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {CrossLinkComponent} from '../../../shared/components/cross-link/cross-link.component';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {
  InputDialogComponent,
  type InputDialogData,
} from '../../../shared/components/input-dialog/input-dialog.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {fail} from '../../../shared/store/entity-ops';
import type {ZoneCooldown} from '../../models/capacity.model';
import type {ReclaimableDevice, ReclaimableHost} from '../../models/operator-lens.model';
import {CapacityService} from '../../services/capacity.service';
import {ReclaimableService} from '../../services/reclaimable.service';
import {ZoneHealthApiService} from '../../services/zone-health-api.service';
import {CatalogAdminStore} from '../../store/catalog-admin.signal-store';
import {HeadroomPanelComponent} from './headroom-panel.component';
import {OfferedZonesComponent} from './offered-zones.component';
import {ReclaimablePanelComponent} from './reclaimable-panel.component';
import {SpotCapacityCardComponent} from './spot-capacity-card.component';
import type {CooldownTarget, ZoneToggle} from './zone-board.component';
import {ZoneBoardComponent} from './zone-board.component';

/**
 * The placement-space home: where the fleet can grow right now, how much room
 * Google's regional quota leaves it, what CCO's own admission quota allows, and
 * what can be taken back.
 *
 * Every number and every verdict is server-derived. The page sorts, groups and
 * renders; it computes nothing an operator could be asked to check.
 */
@Component({
  selector: 'app-admin-capacity',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    CrossLinkComponent,
    ErrorBannerComponent,
    HeadroomPanelComponent,
    LoadingStateComponent,
    MatButtonModule,
    MatIconModule,
    OfferedZonesComponent,
    PageHeaderComponent,
    PageLayoutComponent,
    ReclaimablePanelComponent,
    SpotCapacityCardComponent,
    ZoneBoardComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Capacity"
          subtitle="Every zone the fleet can use, and the room each region has left">
          <button type="button" secondary-actions mat-stroked-button (click)="refresh()">
            <mat-icon>refresh</mat-icon>
            Refresh
          </button>
        </app-page-header>
      </div>

      <div page-content>
        @if (capacity.error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (capacity.snapshot(); as snapshot) {
          <div class="kpis">
            <div class="kpi">
              <span class="v num">{{ snapshot.fleet.machines }}</span>
              <span class="l">machines · {{ snapshot.fleet.devices }} devices</span>
              <span class="note">across {{ snapshot.fleet.regions }} regions</span>
            </div>
            <div class="kpi">
              <span class="v num">{{ snapshot.fleet.zonesClear }} of {{ snapshot.fleet.zonesEnabled }}</span>
              <span class="l">enabled zones clear</span>
              <span class="note">{{ boardNote() }}</span>
            </div>
            <div class="kpi">
              <div class="krow">
                <span class="v num">{{ ceiling() }}</span>
                <button type="button" mat-stroked-button (click)="editLimit()">Edit</button>
              </div>
              <span class="l">fleet ceiling, CCO's own admission quota</span>
              <span class="note">
                a hard limit. Nothing, including the autoscaler, provisions past it. Separate
                from Google's quota below.
              </span>
            </div>
            <div class="kpi">
              <span class="v num">{{ reclaimableCount() }}</span>
              <span class="l">reclaimable now</span>
              <span class="note">the fastest capacity is capacity taken back</span>
            </div>
          </div>

          <section class="card">
            <header>
              <span class="eyebrow">Zone board</span>
              <span class="hint">
                One tile per zone. The stripe answers first. Green means place here, amber wait,
                red blocked. Quota verdicts sit on the region rail, because Google accounts quota
                per region, so they hit every zone in it.
              </span>
            </header>
            <app-zone-board
              [regions]="snapshot.regions"
              [zones]="snapshot.zones"
              [catalog]="catalogStore.zones()"
              [curatedShapes]="catalogStore.curatedShapes()"
              [serverNow]="snapshot.now"
              (clearCooldown)="confirmClearCooldown($event)"
              (toggleZone)="setZoneEnabled($event)" />
            <app-offered-zones
              [catalog]="catalogStore.zones()"
              [placed]="snapshot.zones"
              [curatedShapes]="catalogStore.curatedShapes()"
              (toggleZone)="setZoneEnabled($event)" />
          </section>

          <section class="card">
            <header>
              <span class="eyebrow">Regional quota headroom</span>
              <span class="hint">
                Google's regional pools; ranking filters below the reserve margin
                ({{ snapshot.reserveMarginPercent }}% of each limit). Bar = share of the limit in
                use; the numbers are the truth. Dashed means Google publishes nothing for that
                pool until it is first used, and placement treats it as open, never as zero.
              </span>
            </header>
            <app-headroom-panel [pools]="snapshot.pools" />
          </section>

          <div class="split">
            <section class="card">
              <header>
                <span class="eyebrow">Spot capacity</span>
              </header>
              <app-spot-capacity-card [spot]="snapshot.spot" [serverNow]="snapshot.now" />
              <app-cross-link label="Autoscaler" link="/admin/autoscaler" />
            </section>

            <section class="card">
              <header>
                <span class="eyebrow">Reclaimable now</span>
              </header>
              @if (reclaimableService.reclaimable(); as reclaimable) {
                <app-reclaimable-panel
                  [reclaimable]="reclaimable"
                  (unpinDevice)="unpinDevice($event)"
                  (deleteDevice)="confirmDeleteDevice($event)"
                  (deleteHost)="confirmDeleteHost($event)" />
              } @else if (reclaimableService.error(); as error) {
                <app-error-banner [message]="error" />
              } @else if (reclaimableService.loading()) {
                <app-loading-state size="section" />
              }
            </section>
          </div>
        } @else if (capacity.loading()) {
          <app-loading-state />
        }
      </div>
    </app-page-layout>
  `,
  styles: `
    .num {
      font-variant-numeric: tabular-nums;
    }

    .kpis {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      gap: var(--cco-space-12);
      margin-bottom: var(--cco-space-16);
    }

    .kpi {
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-sm);
      padding: var(--cco-space-12) var(--cco-space-16);
      display: grid;
      gap: 1px;
      align-content: start;
    }

    .kpi .v {
      font-size: 22px;
      font-weight: 500;
    }

    .kpi .krow {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--cco-space-8);
    }

    .kpi .l {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }

    .kpi .note {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    section.card {
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      padding: var(--cco-space-16) var(--cco-space-16) var(--cco-space-24);
      display: grid;
      gap: var(--cco-space-12);
      margin-bottom: var(--cco-space-16);
      align-content: start;
    }

    section.card > header {
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-12);
      flex-wrap: wrap;
    }

    .eyebrow {
      font-size: var(--cco-text-label);
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
      font-weight: 500;
    }

    .hint {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
      flex: 1;
      min-width: 260px;
    }

    .split {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(340px, 1fr));
      gap: var(--cco-space-16);
      align-items: start;
    }
  `,
})
export class CapacityComponent implements OnInit {
  protected readonly capacity = inject(CapacityService);
  protected readonly catalogStore = inject(CatalogAdminStore);
  protected readonly reclaimableService = inject(ReclaimableService);
  private readonly quotaService = inject(QuotaService);
  private readonly zoneHealthApi = inject(ZoneHealthApiService);
  private readonly deviceApi = inject(DeviceApiService);
  private readonly hostApi = inject(HostApiService);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);

  /** CCO's own admission quota, as used-of-limit. */
  protected readonly ceiling = computed(() => {
    const quota = this.quotaService.currentQuota();
    return quota ? `${quota.usedHosts} of ${quota.maxHosts}` : '-';
  });

  /** What the zones that are not clear are doing, from the server's counts. */
  protected readonly boardNote = computed(() => {
    const fleet = this.capacity.snapshot()?.fleet;
    if (!fleet) {
      return '';
    }
    const held = [
      [fleet.zonesCooling, 'cooling'],
      [fleet.zonesBlocked, 'blocked'],
      [fleet.zonesConstrained, 'Spot-constrained'],
    ] as const;
    const parts = held.filter(([count]) => count > 0).map(([count, word]) => `${count} ${word}`);
    return parts.length > 0 ? parts.join(' · ') : 'nothing is holding placement back';
  });

  /** The rows the reclaimable read counted; a failed read has no number to show. */
  protected readonly reclaimableCount = computed(() => {
    const reclaimable = this.reclaimableService.reclaimable();
    return reclaimable ? String(reclaimable.total) : '-';
  });

  ngOnInit(): void {
    this.catalogStore.loadAll();
    // httpResources load on first injection; reload only when revisiting. A
    // first load that errored has settled too, so returning to the page retries
    // instead of re-showing the banner it left behind.
    if (this.capacity.snapshot() || this.capacity.error()) {
      this.capacity.refresh();
    }
    if (this.quotaService.currentQuota() || this.quotaService.error()) {
      this.quotaService.refresh();
    }
    if (this.reclaimableService.reclaimable() || this.reclaimableService.error()) {
      this.reclaimableService.refresh();
    }
  }

  protected refresh(): void {
    this.capacity.refresh();
    this.catalogStore.loadAll();
    this.quotaService.refresh();
    this.reclaimableService.refresh();
  }

  /**
   * Enablement is one stored set, so the toggle sends the set it wants. The
   * catalog store owns that write and the board renders from it; the snapshot
   * re-reads once the write has landed, because a zone leaving the placement
   * space changes every verdict the tiles carry.
   */
  protected setZoneEnabled({zone, enabled}: ZoneToggle): void {
    // Both toggles are offered only for zones the catalog holds, since the
    // stored set is composed from its rows.
    const row = this.catalogStore.zones().find(z => z.zone === zone);
    if (!row) {
      return;
    }
    this.catalogStore.setZoneEnabled({
      zone: row,
      isEnabled: enabled,
      onStored: () => this.capacity.refresh(),
    });
  }

  /**
   * Confirmed because the clear is not undoable and reaches further than the
   * tile shows. A quota cell repels its whole region, a preemption cell only
   * Spot specs. Both cases are named before the write, never after.
   */
  protected confirmClearCooldown({zone, region, cooldown}: CooldownTarget): void {
    const failures =
      cooldown.consecutiveFailures === 1
        ? '1 consecutive failure'
        : `${cooldown.consecutiveFailures} consecutive failures`;
    const data: ConfirmDialogData = {
      title: cooldown.inCooldown ? 'Clear this cooldown?' : 'Clear these failures?',
      message:
        `Drops the ${cooldown.machineFamily} ${cooldown.metricLabel} cell in ${zone}, ` +
        `including the ${failures} that lower the zone's ranking score. ` +
        'Placement can pick the zone again straight away, and the cell comes ' +
        'back if the failure repeats.',
      warning: scopeWarning(zone, region, cooldown),
      confirmText: 'Clear',
      confirmColor: 'warn',
      icon: 'ac_unit',
      iconColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (!confirmed) {
          return;
        }
        this.zoneHealthApi.clearCooldown(zone, cooldown.machineFamily).subscribe({
          next: () => {
            this.snackBar.open(`Cleared the ${cooldown.machineFamily} cell in ${zone}`, 'Close', {
              duration: 3000,
            });
            // Only the next snapshot can say what is left in the cell, because
            // the board shows no verdict it derived itself.
            this.capacity.refresh();
          },
          error: err => fail(this.snackBar, 'Failed to clear the cooldown', err),
        });
      });
  }

  protected unpinDevice(device: ReclaimableDevice): void {
    this.deviceApi.unpin(device.id).subscribe({
      next: () => {
        this.snackBar.open(`Unpinned ${device.name}`, 'Close', {duration: 3000});
        this.reclaimableService.refresh();
      },
      error: err => fail(this.snackBar, `Failed to unpin ${device.name}`, err),
    });
  }

  protected confirmDeleteDevice(device: ReclaimableDevice): void {
    const data: ConfirmDialogData = {
      title: 'Delete device',
      message: `Delete the stopped device ${device.name}? This cannot be undone.`,
      confirmText: 'Delete',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (!confirmed) {
          return;
        }
        this.deviceApi.delete(device.id).subscribe({
          next: () => {
            this.snackBar.open(`Deleted ${device.name}`, 'Close', {duration: 3000});
            this.reclaimableService.refresh();
          },
          error: err => fail(this.snackBar, `Failed to delete ${device.name}`, err),
        });
      });
  }

  protected confirmDeleteHost(host: ReclaimableHost): void {
    const data: ConfirmDialogData = {
      title: 'Delete host',
      message:
        `Delete the stopped host ${host.name} in ${host.zone}? Its ` +
        `${host.currentDeviceCount} devices are removed with it.`,
      confirmText: 'Delete',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (!confirmed) {
          return;
        }
        this.hostApi.delete(host.id).subscribe({
          next: () => {
            this.snackBar.open(`Deleted ${host.name}`, 'Close', {duration: 3000});
            this.refresh();
          },
          error: err => fail(this.snackBar, `Failed to delete ${host.name}`, err),
        });
      });
  }

  protected editLimit(): void {
    const quota = this.quotaService.currentQuota();
    const data: InputDialogData = {
      title: 'Edit the fleet ceiling',
      message: quota
        ? `Maximum concurrent hosts across the platform. Currently ${quota.usedHosts} of ${quota.maxHosts} in use.`
        : 'Maximum concurrent hosts across the platform.',
      inputLabel: 'Maximum hosts',
      initialValue: quota ? String(quota.maxHosts) : '',
      confirmText: 'Update',
      required: true,
      icon: 'speed',
      validationPattern: '[0-9]{1,6}',
      validationMessage: 'Enter a whole number',
    };
    this.dialog
      .open(InputDialogComponent, {data, width: '420px'})
      .afterClosed()
      .subscribe((result: string | null) => {
        if (!result) {
          return;
        }
        this.quotaService.updatePlatformQuota(Number(result)).subscribe({
          next: updated => {
            this.snackBar.open(`Fleet ceiling updated to ${updated.maxHosts}`, 'Close', {
              duration: 3000,
            });
            this.refresh();
          },
          error: err => fail(this.snackBar, 'Failed to update the fleet ceiling', err),
        });
      });
  }
}

/** The two cases where a clear reaches past the zone whose tile was clicked. */
function scopeWarning(zone: string, region: string, cd: ZoneCooldown): string | undefined {
  if (cd.regional) {
    return (
      `Quota is accounted per region, so this cell repels all of ${region}. ` +
      `Clearing it reopens every zone in the region for ${cd.machineFamily}, and not only ${zone}.`
    );
  }
  if (cd.bindingMetric === 'preemption') {
    return `This cell repels Spot placement only. Clearing it lets Spot hosts land in ${zone} again.`;
  }
  return undefined;
}
