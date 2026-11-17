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

import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {FormField, form} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatSelectModule} from '@angular/material/select';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatTooltipModule} from '@angular/material/tooltip';
import {ActivatedRoute, Router, RouterModule} from '@angular/router';
import type {KpiItem} from '../../../shared/components';
import {
  ActionBarComponent,
  KpiStripComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {collectionNote} from '../../../shared/utils/paged-collection';
import type {DeviceListFilter, LifecycleMode} from '../../models/device.model';
import {DeviceActionsService} from '../../services/device-actions.service';
import {DeviceStore} from '../../store/device.signal-store';
import {buildDeviceKpis} from './device-kpis';
import {DeviceTableComponent} from './device-table.component';

/**
 * Smart container for the device feature: owns list loading, the filter form,
 * and the KPI strip; delegates the table to app-device-table and every row
 * command to DeviceActionsService.
 */
@Component({
  selector: 'app-device-list',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    EmptyStateComponent,
    LoadingStateComponent,
    ErrorBannerComponent,
    RouterModule,
    FormField,
    MatButtonModule,
    MatChipsModule,
    MatIconModule,
    MatFormFieldModule,
    MatSelectModule,
    MatTooltipModule,
    MatSlideToggleModule,
    PageLayoutComponent,
    PageHeaderComponent,
    KpiStripComponent,
    ActionBarComponent,
    DeviceTableComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Devices"
          subtitle="Manage Cuttlefish devices">
          <div primary-action>
            <button type="button" mat-flat-button routerLink="create">
              <mat-icon>add</mat-icon>
              Create Device
            </button>
          </div>
        </app-page-header>
      </div>

      <div page-content>
        <app-kpi-strip [items]="kpiItems()" (cardClick)="onKpiClick($event)" />

        <app-action-bar>
          <div filters>
            <mat-form-field appearance="outline">
              <mat-label>Status</mat-label>
              <mat-select [formField]="filterForm.status">
                <mat-option value="">All Statuses</mat-option>
                <mat-option value="running">Running</mat-option>
                <mat-option value="stopped">Stopped</mat-option>
                <mat-option value="creating">Creating</mat-option>
                <mat-option value="error">Error</mat-option>
              </mat-select>
            </mat-form-field>

            <mat-form-field appearance="outline">
              <mat-label>Lifecycle</mat-label>
              <mat-select [formField]="filterForm.lifecycle">
                <mat-option value="">All</mat-option>
                <mat-option value="ephemeral">Ephemeral</mat-option>
                <mat-option value="pinned">Pinned</mat-option>
              </mat-select>
            </mat-form-field>

            <mat-slide-toggle
                class="my-devices-toggle"
                [checked]="onlyMyDevices()"
                (change)="onMineToggle($event.checked)"
                color="primary">
                My devices only
            </mat-slide-toggle>

            @if (hostFilter(); as hostId) {
              <mat-chip-set class="host-scope">
                <mat-chip (removed)="clearHostFilter()">
                  Host: {{ hostId }}
                  <button type="button" matChipRemove aria-label="Show devices on every host">
                    <mat-icon>cancel</mat-icon>
                  </button>
                </mat-chip>
              </mat-chip-set>
            }
          </div>

          <div actions>
            @if (selectedCount() > 0) {
              <button type="button"
                mat-stroked-button
                color="warn"
                (click)="deleteSelected()"
                matTooltip="Delete selected devices">
                <mat-icon>delete</mat-icon>
                Delete ({{ selectedCount() }})
              </button>
            }
            <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" aria-label="Refresh">
              <mat-icon>refresh</mat-icon>
            </button>
          </div>
        </app-action-bar>

        @if (loading()) {
          <app-loading-state />
        }

        @if (error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (!loading()) {
          <app-device-table
            [devices]="filteredDevices()"
            [displayedColumns]="displayedColumns"
            [selectedIds]="selectedIds()"
            (connectAdb)="actions.connectAdb($event)"
            (copyId)="actions.copyId($event)"
            (startDevice)="actions.start($event)"
            (stopDevice)="actions.stop($event)"
            (deleteDevice)="actions.delete($event)"
            (pin)="actions.pin($event)"
            (unpin)="actions.unpin($event)"
            (extend)="actions.extend($event)"
            (expired)="actions.notifyExpired($event)"
            (toggleRow)="onToggleRow($event)"
            (toggleAll)="onToggleAll($event)" />
        }

        @if (!loading() && truncationNote()) {
          <div class="truncation-hint">{{ truncationNote() }}</div>
        }

        @if (!loading() && filteredDevices().length === 0) {
          <app-empty-state
            icon="devices"
            title="No devices found"
            message="Create a device on an available host to get started"
          >
            <button type="button" action mat-flat-button routerLink="create">
              Create Device
            </button>
          </app-empty-state>
        }
      </div>
    </app-page-layout>
    `,
  styles: `
      .my-devices-toggle {
        margin-left: 12px;
      }

      .host-scope {
        display: inline-flex;
        vertical-align: middle;
      }

      .truncation-hint {
        padding: 8px 4px;
        font-size: 12px;
        color: var(--cco-text-secondary);
      }




    `,
})
export class DeviceListComponent {
  private readonly deviceStore = inject(DeviceStore);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  protected readonly actions = inject(DeviceActionsService);

  private readonly devices = this.deviceStore.entities;
  protected readonly loading = this.deviceStore.loading;
  protected readonly error = this.deviceStore.error;
  private readonly stats = this.deviceStore.stats;

  private readonly filterDraft = signal<{status: string; lifecycle: LifecycleMode | ''}>({
    status: '',
    lifecycle: '',
  });
  protected readonly filters = this.filterDraft.asReadonly();
  protected readonly filterForm = form(this.filterDraft);

  /**
   * Host scope, addressed by the URL rather than by the filter form. It arrives
   * as a deep link from a host page, and a host id has no enumerable set a
   * select could offer. Read from the live params, not the entry snapshot. The
   * router reuses this component when only the query string changes, so a
   * snapshot would leave the list scoped to a host the URL no longer names.
   */
  private readonly queryParams = toSignal(this.route.queryParamMap);
  protected readonly hostFilter = computed(() => this.queryParams()?.get('hostId') ?? undefined);

  /**
   * Mine/All scope, filtered server-side (the loaded page would hide foreign
   * rows a client-side filter thinks it can count). Defaults to Mine, because
   * the console is a human surface; cfctl keeps the fleet-wide default. A
   * host-scoped entry defaults to All instead, since the host page counts every
   * device on the host and Mine would answer a narrower question than the link
   * offered.
   */
  protected readonly onlyMyDevices = signal(!this.hostFilter());

  /**
   * What the collection is loaded with. Status and lifecycle narrow the loaded
   * rows client-side. These two cannot. A page filtered here would hide rows
   * the KPI strip still counts, so changing either re-queries.
   */
  private readonly listFilter = computed<DeviceListFilter>(() => ({
    mine: this.onlyMyDevices(),
    hostId: this.hostFilter(),
  }));

  protected readonly filteredDevices = computed(() => {
    const devices = this.devices();
    const {status, lifecycle} = this.filters();

    return devices.filter(device => {
      if (status && device.status !== status) {
        return false;
      }
      if (lifecycle && device.lifecycle?.mode !== lifecycle) {
        return false;
      }
      return true;
    });
  });

  protected readonly displayedColumns = [
    'select',
    'status',
    'name',
    'host',
    'region',
    'lifecycle',
    'pin',
    'build',
    'gpu',
    'ports',
    'created',
    'actions',
  ];

  /**
   * Raw multi-selection. Stale ids (rows since filtered out or deleted) are
   * harmless: every read intersects with the visible rows, so the derived
   * selection below and the table's checkboxes only ever reflect present rows.
   */
  private readonly selectedIdSet = signal<ReadonlySet<string>>(new Set<string>());
  protected readonly selectedIds = this.selectedIdSet.asReadonly();

  /** The selected devices still present in the current view. */
  protected readonly selectedDevices = computed(() =>
    this.filteredDevices().filter(d => this.selectedIds().has(d.id))
  );
  protected readonly selectedCount = computed(() => this.selectedDevices().length);

  protected readonly kpiItems = computed<KpiItem[]>(() => buildDeviceKpis(this.stats()));

  /**
   * Non-empty when the store holds fewer rows than the server reports. The
   * table and the KPIs then describe less than the fleet, which is the
   * difference between a short list and a wrong number.
   */
  protected readonly truncationNote = computed(() =>
    collectionNote(
      this.devices().length,
      this.deviceStore.total(),
      this.deviceStore.complete(),
      'devices'
    )
  );

  constructor() {
    // Passed as a signal, so a scope change re-queries on its own; the store's
    // switchMap drops the reply to a scope the user has already left.
    this.deviceStore.loadAll(this.listFilter);
  }

  protected refresh(): void {
    this.deviceStore.loadAll(this.listFilter());
  }

  protected onMineToggle(checked: boolean): void {
    this.onlyMyDevices.set(checked);
  }

  /**
   * Widens the list back to every host. The URL is the filter's only home, so
   * clearing it here is a navigation, which is also what makes a reload keep
   * the cleared state.
   */
  protected clearHostFilter(): void {
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: {},
      replaceUrl: true,
    });
  }

  protected onKpiClick(item: KpiItem): void {
    if (item.route) {
      void this.router.navigate([item.route]);
    }
  }

  protected onToggleRow(id: string): void {
    const next = new Set(this.selectedIds());
    if (next.has(id)) {
      next.delete(id);
    } else {
      next.add(id);
    }
    this.selectedIdSet.set(next);
  }

  protected onToggleAll(checked: boolean): void {
    const next = new Set(this.selectedIds());
    for (const device of this.filteredDevices()) {
      if (checked) {
        next.add(device.id);
      } else {
        next.delete(device.id);
      }
    }
    this.selectedIdSet.set(next);
  }

  /**
   * Successful deletes leave the store (removeEntities), so the selection
   * shrinks on its own; anything that failed stays selected for a retry.
   */
  protected deleteSelected(): void {
    this.actions.deleteMany(this.selectedDevices());
  }
}
