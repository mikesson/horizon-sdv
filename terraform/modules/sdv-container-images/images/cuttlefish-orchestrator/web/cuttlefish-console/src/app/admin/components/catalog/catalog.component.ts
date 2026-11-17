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
import {toSignal} from '@angular/core/rxjs-interop';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MatIconModule} from '@angular/material/icon';
import {MatTabsModule} from '@angular/material/tabs';
import {MatTooltipModule} from '@angular/material/tooltip';
import {ActivatedRoute, Router} from '@angular/router';
import {map} from 'rxjs/operators';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {CrossLinkComponent} from '../../../shared/components/cross-link/cross-link.component';
import type {CustomShapeBuilderData} from '../../../shared/components/custom-shape-builder/custom-shape-builder.component';
import {CustomShapeBuilderComponent} from '../../../shared/components/custom-shape-builder/custom-shape-builder.component';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {
  InputDialogComponent,
  type InputDialogData,
} from '../../../shared/components/input-dialog/input-dialog.component';
import type {AdminFamily} from '../../models/machine-family.model';
import {CatalogAdminApiService} from '../../services/catalog-admin-api.service';
import {CatalogAdminStore} from '../../store/catalog-admin.signal-store';
import {HostImageConfigStore} from '../../store/host-image-config.signal-store';
import {HostImageConfigComponent} from '../host-image-config/host-image-config.component';
import type {CapacityChange} from './family-capacity.component';
import type {ExclusionChange} from './family-exclusions.component';
import type {GpuPolicyResult} from './gpu-policy-dialog.component';
import {GpuPolicyDialogComponent} from './gpu-policy-dialog.component';
import type {DefaultShapeChoice, FamilyToggle} from './machine-family-table.component';
import {MachineFamilyTableComponent} from './machine-family-table.component';

/** Tab order; the index maps to the ?tab= query value for deep links. */
const TABS = ['host-images', 'machine-types'] as const;

/**
 * Curation home for what users can pick: host images and machine types.
 * The active tab is URL-synced (?tab=) so sync-error badges and docs can link
 * straight to a tab.
 *
 * Where a shape can RUN belongs to the Capacity page. Zones carry a placement
 * verdict composed from four planes, and they are curated there so the verdict
 * is beside them.
 */
@Component({
  selector: 'app-admin-catalog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    CrossLinkComponent,
    ErrorBannerComponent,
    MatButtonModule,
    MatIconModule,
    MatTabsModule,
    MatTooltipModule,
    HostImageConfigComponent,
    MachineFamilyTableComponent,
    PageHeaderComponent,
    PageLayoutComponent,
  ],
  template: `
    <app-page-layout variant="dashboard">
      <div page-header>
        <app-page-header
          title="Catalog"
          subtitle="What users can pick: host images and machine types">
          <button type="button" secondary-actions mat-stroked-button (click)="refresh()">
            <mat-icon>refresh</mat-icon>
            Refresh
          </button>
        </app-page-header>
      </div>

      <div page-content>
        @if (freshnessView(); as freshness) {
          <div class="freshness-bar">
            <mat-icon class="freshness-icon">sync</mat-icon>
            <span class="freshness-chip">Catalog {{ freshness.ageLabel }}</span>
            @for (scope of freshness.partial; track scope) {
              <span
                class="freshness-chip error"
                matTooltip="Compute Engine did not report this scope, so the catalog is silent about it rather than knowing it is empty">
                silent: {{ scope }}
              </span>
            }
            @if (freshness.partial.length === 0) {
              <span class="freshness-scopes">Every scope reported</span>
            }
          </div>
        }

        <div class="cross-links">
          <app-cross-link label="Where these shapes can run" link="/admin/capacity" />
          <app-cross-link label="Zone pin and ranking policy" link="/admin/placement" />
        </div>

        @if (store.error(); as error) {
          <app-error-banner [message]="error" />
        }

        <mat-tab-group
          class="catalog-tabs"
          [selectedIndex]="selectedIndex()"
          (selectedIndexChange)="onTabChange($event)">
          <mat-tab label="Host Images">
            <div class="tab-body">
              <app-host-image-config />
            </div>
          </mat-tab>
          <mat-tab label="Machine Types">
            <div class="tab-body">
              <div class="tab-lede">
                <p>
                  Enabling a family is the whole of machine curation. Compute Engine reports every
                  shape under it, already sized. Open a family to see what it holds.
                </p>
                <button type="button" mat-stroked-button (click)="buildCustomShape()">
                  <mat-icon>tune</mat-icon>
                  Custom shape
                </button>
              </div>
              <app-machine-family-table
                [families]="store.families()"
                (toggleEnabled)="toggleFamily($event)"
                (chooseDefaultShape)="chooseDefaultShape($event)"
                (changeSortOrder)="editSortOrder($event)"
                (editGpuPolicy)="editGpuPolicy($event)"
                (exclusionChange)="changeExclusion($event)"
                (capacityChange)="changeCapacity($event)" />
            </div>
          </mat-tab>
        </mat-tab-group>
      </div>
    </app-page-layout>
  `,
  styles: `
    .freshness-bar {
      display: flex;
      align-items: center;
      flex-wrap: wrap;
      gap: 8px;
      padding: 8px 12px;
      margin-bottom: 16px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .freshness-icon {
      font-size: 16px;
      width: 16px;
      height: 16px;
    }

    .freshness-chip {
      padding: 2px 8px;
      border-radius: 10px;
      background: var(--cco-surface-variant);
    }

    .freshness-chip.error {
      background: var(--cco-error-surface);
      color: var(--cco-error);
    }

    .freshness-scopes {
      margin-left: auto;
    }


    .cross-links {
      display: flex;
      gap: var(--cco-space-16);
      justify-content: flex-end;
      margin-bottom: var(--cco-space-8);
    }

    .catalog-tabs {
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
    }

    .tab-body {
      padding: 24px;
    }

    .tab-lede {
      display: flex;
      align-items: center;
      gap: var(--cco-space-16);
      margin-bottom: var(--cco-space-16);
    }

    .tab-lede p {
      margin: 0;
      flex: 1;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }
  `,
})
export class CatalogComponent implements OnInit {
  protected readonly store = inject(CatalogAdminStore);
  protected readonly catalogApi = inject(CatalogAdminApiService);
  private readonly hostImageStore = inject(HostImageConfigStore);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly dialog = inject(MatDialog);

  private readonly queryTab = toSignal(this.route.queryParamMap.pipe(map(p => p.get('tab'))));

  protected readonly selectedIndex = computed(() => {
    const index = TABS.indexOf((this.queryTab() ?? '') as (typeof TABS)[number]);
    return index >= 0 ? index : 0;
  });

  // Snapshot age and the scopes Compute Engine did not report, which is what
  // the catalog knows, rather than when a background job last ran.
  protected readonly freshnessView = computed(() => {
    const freshness = this.catalogApi.freshness();
    if (!freshness) {
      return null;
    }
    return {
      ageLabel: freshness.builtAt ? `built ${relativeTime(freshness.builtAt)}` : 'not built yet',
      partial: freshness.partial,
    };
  });

  ngOnInit(): void {
    // The zones tab moved to Capacity, so a bookmark of it lands there rather
    // than silently on Host Images. admin.routes.ts extends the same courtesy
    // to the pages that were consolidated into Settings.
    if (this.queryTab() === 'zones') {
      void this.router.navigate(['/admin/capacity'], {replaceUrl: true});
      return;
    }
    this.store.loadAll();
    if (this.catalogApi.freshness()) {
      this.catalogApi.refreshFreshness();
    }
  }

  protected onTabChange(index: number): void {
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: {tab: TABS[index]},
      queryParamsHandling: 'merge',
      replaceUrl: true,
    });
  }

  protected refresh(): void {
    this.store.loadAll();
    this.catalogApi.refreshFreshness();
    this.hostImageStore.loadAll({forceReload: true});
  }

  protected toggleFamily(toggle: FamilyToggle): void {
    this.store.patchFamily({family: toggle.family, patch: {isEnabled: toggle.isEnabled}});
  }

  /**
   * The default is elected onto a shape, so it is a per-shape write even though
   * the control that moves it is scoped to one family's rows.
   */
  protected chooseDefaultShape(choice: DefaultShapeChoice): void {
    const machineType = this.store.machineTypes().find(mt => mt.id === choice.machineType);
    if (machineType) {
      this.store.patchMachineType({machineType, patch: {isDefault: true}});
    }
  }

  protected changeExclusion(change: ExclusionChange): void {
    const machineType = this.store.machineTypes().find(mt => mt.id === change.machineType);
    if (machineType) {
      this.store.patchMachineType({machineType, patch: {isEnabled: !change.excluded}});
    }
  }

  /** The per-shape capacity: the operator's number, or 0 to restore the arithmetic. */
  protected changeCapacity(change: CapacityChange): void {
    const machineType = this.store.machineTypes().find(mt => mt.id === change.machineType);
    if (machineType) {
      this.store.patchMachineType({machineType, patch: {maxDevices: change.maxDevices}});
    }
  }

  protected editGpuPolicy(family: AdminFamily): void {
    this.dialog
      .open(GpuPolicyDialogComponent, {data: family, width: '460px'})
      .afterClosed()
      .subscribe((result: GpuPolicyResult | undefined) => {
        if (result) {
          this.store.patchFamily({family, patch: result});
        }
      });
  }

  protected editSortOrder(family: AdminFamily): void {
    const data: InputDialogData = {
      title: `Display order for ${family.family}`,
      message: 'Lower numbers sort first in pickers.',
      inputLabel: 'Sort order',
      initialValue: String(family.sortOrder),
      confirmText: 'Update',
      required: true,
      icon: 'sort',
      validationPattern: '[0-9]{1,4}',
      validationMessage: 'Enter a whole number',
    };
    this.dialog
      .open(InputDialogComponent, {data, width: '420px'})
      .afterClosed()
      .subscribe((result: string | null) => {
        if (result !== null && result !== undefined && result !== '') {
          this.store.patchFamily({family, patch: {sortOrder: Number(result)}});
        }
      });
  }

  /**
   * The builder resolves a name through Compute Engine and hands it back. It
   * commits nothing on its own. A custom shape needs no catalog row, because
   * the family it is built on is already curated, and the name is what a create
   * carries.
   */
  protected buildCustomShape(): void {
    const enabled = this.store.families().filter(family => family.isEnabled);
    const data: CustomShapeBuilderData = {
      families: enabled.map(family => family.family),
      initialFamily: enabled[0]?.family ?? '',
    };
    this.dialog.open(CustomShapeBuilderComponent, {data, width: '520px'});
  }
}

/** "5m ago" / "3h ago" / "2d ago"; freshness is coarse by nature. */
function relativeTime(date: Date): string {
  const minutes = Math.floor((Date.now() - date.getTime()) / 60_000);
  if (minutes < 1) {
    return 'just now';
  }
  if (minutes < 60) {
    return `${minutes}m ago`;
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return `${hours}h ago`;
  }
  return `${Math.floor(hours / 24)}d ago`;
}
