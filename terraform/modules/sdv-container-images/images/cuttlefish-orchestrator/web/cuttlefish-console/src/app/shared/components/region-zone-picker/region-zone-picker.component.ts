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
  effect,
  inject,
  input,
  linkedSignal,
  output,
  resource,
  signal,
} from '@angular/core';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import {MatTooltipModule} from '@angular/material/tooltip';
import {firstValueFrom} from 'rxjs';

import type {GeographyGroup, RegionInfo} from '../../models/gcp-regions';
import {getGeographyGroups, getRegionInfo, regionFromZone} from '../../models/gcp-regions';
import type {ZoneAvailability} from '../../models/zone-availability.schema';
import {GceCatalogService} from '../../services/gce-catalog.service';

/**
 * Two-level region and zone picker component.
 *
 * Displays regions grouped by geography (Americas, Europe, Asia Pacific, etc.)
 * and zones filtered by the selected region. Quota information is shown
 * separately below the selectors for clarity.
 *
 * Usage:
 * ```html
 * <app-region-zone-picker
 *   [initialRegion]="'us-central1'"
 *   [initialZone]="'us-central1-a'"
 *   [machineType]="'n1-standard-8'"
 *   [gpuType]="'nvidia-tesla-t4'"
 *   (regionChange)="onRegionChange($event)"
 *   (zoneChange)="onZoneChange($event)">
 * </app-region-zone-picker>
 * ```
 */
@Component({
  selector: 'app-region-zone-picker',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatFormFieldModule, MatSelectModule, MatInputModule, MatIconModule, MatTooltipModule],
  template: `
    <div class="region-zone-picker">
      <!-- Region Dropdown -->
      <mat-form-field appearance="outline" class="region-field">
        <mat-label>Region</mat-label>
        <mat-select
          [value]="selectedRegion()"
          (selectionChange)="onRegionSelected($event.value)"
          panelClass="region-select-panel">
          <!-- Custom trigger to display region with eco icon -->
          <mat-select-trigger>
            @if (selectedRegionInfo(); as region) {
              <span class="region-trigger">
                <span>{{ region.displayName }}</span>
                <span class="region-id-trigger">({{ region.region }})</span>
                @if (region.lowCO2) {
                  <mat-icon class="low-co2-trigger">eco</mat-icon>
                }
              </span>
            }
          </mat-select-trigger>
          <!-- Search input at top -->
          <div class="search-container">
            <mat-icon class="search-icon">search</mat-icon>
            <input
              class="search-input"
              placeholder="Search regions..."
              [value]="regionSearch()"
              (input)="onRegionSearch($event)"
              (click)="$event.stopPropagation()">
          </div>

          @for (group of filteredGeographyGroups(); track group.geography) {
            <mat-optgroup [label]="group.label">
              @for (region of group.regions; track region.region) {
                <mat-option [value]="region.region" [disabled]="!isRegionAvailable(region.region)">
                  <div class="region-option">
                    <span class="region-name">{{ region.displayName }}</span>
                    <span class="region-id">({{ region.region }})</span>
                    @if (region.lowCO2) {
                      <mat-icon class="low-co2" matTooltip="Low CO2 region">eco</mat-icon>
                    }
                    @if (!isRegionAvailable(region.region)) {
                      <span class="no-zones">No zones</span>
                    }
                  </div>
                </mat-option>
              }
            </mat-optgroup>
          }
        </mat-select>
      </mat-form-field>

      <!-- Zone Dropdown -->
      <mat-form-field appearance="outline" class="zone-field">
        <mat-label>Zone</mat-label>
        <mat-select [value]="selectedZone()" (selectionChange)="onZoneSelected($event.value)">
          <!-- Custom trigger to display only zone suffix -->
          <mat-select-trigger>
            {{ getZoneSuffix(selectedZone()) }}
          </mat-select-trigger>

          @for (zone of filteredZones(); track zone.zone) {
            <mat-option
              [value]="zone.zone"
              [disabled]="!zone.isEnabled || zone.status === 'unavailable'">
              <div class="zone-option">
                <span [class]="['zone-indicator', zone.status]"></span>
                <span class="zone-suffix">{{ getZoneSuffix(zone.zone) }}</span>
                @if (zone.status === 'degraded') {
                  <mat-icon class="zone-warning" matTooltip="Zone experiencing issues">warning</mat-icon>
                }
                @if (zone.status === 'unavailable') {
                  <span class="zone-unavailable">Unavailable</span>
                }
              </div>
            </mat-option>
          }
          @if (filteredZones().length === 0) {
            <mat-option disabled>
              <span class="no-zones-message">No zones available in this region</span>
            </mat-option>
          }
        </mat-select>
      </mat-form-field>
    </div>

    <!-- Availability Info Card -->
    @if (selectedZoneInfo(); as zone) {
      <div class="availability-card" [class.has-warning]="hasQuotaWarning()">
        <div class="availability-header">
          <mat-icon class="availability-icon">check_circle</mat-icon>
          <span class="availability-title">Zone availability</span>
        </div>
        <div class="availability-content">
          <!-- Machine Type Availability -->
          <div class="availability-item">
            <mat-icon class="item-icon success">memory</mat-icon>
            <span class="item-label">Machine type:</span>
            <span class="item-value">
              @if (zone.machineTypeAvailable !== false) {
                Available
              } @else {
                <span class="unavailable">Not available in this zone</span>
              }
            </span>
          </div>

          <!-- GPU Availability -->
          @if (gpuType()) {
            <div class="availability-item">
              @if (zone.gpuAvailable) {
                <mat-icon class="item-icon success">developer_board</mat-icon>
                <span class="item-label">GPU quota:</span>
                <span class="item-value">
                  @if (zone.gpuMaxCount) {
                    Up to {{ zone.gpuMaxCount }} GPUs available
                  } @else {
                    Available
                  }
                </span>
              } @else {
                <mat-icon class="item-icon warning">warning</mat-icon>
                <span class="item-label">GPU:</span>
                <span class="item-value unavailable">{{ formatGpuType(gpuType()) }} not available in this zone</span>
              }
            </div>
          }
        </div>
      </div>
    }
  `,
  styles: `
    .region-zone-picker {
      display: flex;
      gap: 16px;
      width: 100%;
    }

    .region-field {
      flex: 1;
      min-width: 200px;
    }

    .zone-field {
      flex: 0 0 120px;
    }

    /* Search container inside dropdown */
    .search-container {
      display: flex;
      align-items: center;
      padding: 8px 16px;
      border-bottom: 1px solid var(--cco-border);
      position: sticky;
      top: 0;
      background: var(--cco-surface);
      z-index: 1;
    }

    .search-icon {
      color: var(--cco-text-secondary);
      font-size: 20px;
      width: 20px;
      height: 20px;
      margin-right: 8px;
    }

    .search-input {
      border: none;
      outline: none;
      font-size: 14px;
      width: 100%;
      background: transparent;
    }

    /* Region option styling */
    .region-option {
      display: flex;
      align-items: center;
      gap: 8px;
      width: 100%;
    }

    .region-name {
      font-weight: 500;
    }

    .region-id {
      color: var(--cco-text-secondary);
      font-size: 12px;
    }

    .low-co2 {
      color: var(--cco-success);
      font-size: 16px;
      width: 16px;
      height: 16px;
      margin-left: auto;
    }

    /* Region trigger styling (selected value display) */
    .region-trigger {
      display: flex;
      align-items: center;
      gap: 4px;
    }

    .region-id-trigger {
      color: var(--cco-text-secondary);
      font-size: 12px;
    }

    .low-co2-trigger {
      color: var(--cco-success);
      font-size: 16px;
      width: 16px;
      height: 16px;
      margin-left: 4px;
    }

    .no-zones {
      font-size: 11px;
      color: var(--cco-text-secondary);
      margin-left: auto;
    }

    /* Zone option styling */
    .zone-option {
      display: flex;
      align-items: center;
      gap: 8px;
      width: 100%;
    }

    .zone-indicator {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      flex-shrink: 0;
    }

    .zone-indicator.available {
      background-color: var(--cco-success);
    }

    .zone-indicator.degraded {
      background-color: var(--cco-warning);
    }

    .zone-indicator.unavailable {
      background-color: var(--cco-error);
    }

    .zone-suffix {
      font-weight: 500;
      text-transform: lowercase;
    }

    .zone-unavailable {
      font-size: 11px;
      color: var(--cco-error);
      margin-left: auto;
    }

    .zone-warning {
      color: var(--cco-warning);
      font-size: 16px;
      width: 16px;
      height: 16px;
      margin-left: auto;
    }

    .no-zones-message {
      color: var(--cco-text-secondary);
      font-style: italic;
    }

    /* Availability Card */
    .availability-card {
      margin-top: 16px;
      padding: 12px 16px;
      background: var(--cco-surface-variant);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      border-left: 4px solid var(--cco-success);
    }

    .availability-card.has-warning {
      border-left-color: var(--cco-warning);
      background: var(--cco-warning-surface);
    }

    .availability-header {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 12px;
    }

    .availability-icon {
      color: var(--cco-success);
      font-size: 20px;
      width: 20px;
      height: 20px;
    }

    .has-warning .availability-icon {
      color: var(--cco-warning);
    }

    .availability-title {
      font-size: 12px;
      font-weight: 500;
      color: var(--cco-text);
      text-transform: uppercase;
      letter-spacing: 0.5px;
    }

    .availability-content {
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    .availability-item {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 12px;
    }

    .item-icon {
      font-size: 16px;
      width: 16px;
      height: 16px;
    }

    .item-icon.success {
      color: var(--cco-success);
    }

    .item-icon.warning {
      color: var(--cco-warning);
    }

    .item-label {
      color: var(--cco-text-secondary);
      min-width: 100px;
    }

    .item-value {
      color: var(--cco-text);
      font-weight: 500;
    }

    .item-value.unavailable {
      color: var(--cco-error);
    }

    /* Responsive */
    @container page (max-width: 600px) {
      .region-zone-picker {
        flex-direction: column;
      }

      .region-field,
      .zone-field {
        width: 100%;
        flex: 1;
      }
    }
  `,
})
export class RegionZonePickerComponent {
  private readonly gceCatalog = inject(GceCatalogService);

  readonly initialRegion = input<string>('');

  readonly initialZone = input<string>('');

  readonly machineType = input<string>('');

  readonly gpuType = input<string>('');

  readonly regionChange = output<string>();

  readonly zoneChange = output<string>();

  // Derived-but-writable: adopt the parent's initial region/zone when supplied,
  // but stay user-writable (onRegionSelected/onZoneSelected set them) and keep
  // the current selection when the inputs are empty, never resetting it. This is
  // the linkedSignal form of the old "copy input into signal via effect".
  protected readonly selectedRegion = linkedSignal<{zone: string; region: string}, string>({
    source: () => ({zone: this.initialZone(), region: this.initialRegion()}),
    computation: (src, prev) => {
      if (src.zone) {
        return regionFromZone(src.zone);
      }
      if (src.region) {
        return src.region;
      }
      return prev?.value ?? '';
    },
  });
  protected readonly selectedZone = linkedSignal<string, string>({
    source: () => this.initialZone(),
    computation: (zone, prev) => zone || (prev?.value ?? ''),
  });
  protected readonly regionSearch = signal<string>('');

  /** Zone availability, re-fetched whenever the machine/GPU selection changes. */
  private readonly zonesResource = resource({
    params: () => ({
      machineType: this.machineType() || undefined,
      gpuType: this.gpuType() || undefined,
    }),
    loader: ({params}) =>
      firstValueFrom(this.gceCatalog.getZoneAvailability(params.machineType, params.gpuType)),
  });

  private readonly allZones = computed(() =>
    this.zonesResource.hasValue() ? this.zonesResource.value() : []
  );

  private readonly availableRegions = computed(() => {
    const regions = new Set<string>();
    for (const zone of this.allZones()) {
      if (zone.isEnabled && zone.status !== 'unavailable') {
        regions.add(zone.region);
      }
    }
    return regions;
  });

  readonly geographyGroups = getGeographyGroups();

  readonly filteredGeographyGroups = computed((): GeographyGroup[] => {
    const search = this.regionSearch().toLowerCase();
    if (!search) {
      return this.geographyGroups;
    }

    return this.geographyGroups
      .map(group => ({
        ...group,
        regions: group.regions.filter(
          r =>
            r.displayName.toLowerCase().includes(search) ||
            r.region.toLowerCase().includes(search) ||
            r.country.toLowerCase().includes(search)
        ),
      }))
      .filter(group => group.regions.length > 0);
  });

  readonly filteredZones = computed((): ZoneAvailability[] => {
    const region = this.selectedRegion();
    if (!region) {
      return [];
    }
    return this.allZones()
      .filter(z => z.region === region)
      .sort((a, b) => a.zone.localeCompare(b.zone));
  });

  readonly selectedZoneInfo = computed((): ZoneAvailability | undefined => {
    const zoneId = this.selectedZone();
    if (!zoneId) {
      return undefined;
    }
    return this.allZones().find(z => z.zone === zoneId);
  });

  protected readonly selectedRegionInfo = computed((): RegionInfo | undefined => {
    const region = this.selectedRegion();
    return region ? getRegionInfo(region) : undefined;
  });

  /** Check if there are any quota warnings for the selected configuration. */
  readonly hasQuotaWarning = computed((): boolean => {
    const zone = this.selectedZoneInfo();
    if (!zone) {
      return false;
    }
    if (zone.machineTypeAvailable === false) {
      return true;
    }
    if (this.gpuType() && !zone.gpuAvailable) {
      return true;
    }
    return false;
  });

  constructor() {
    // Parent-driven selection is handled by the selectedRegion/selectedZone
    // linkedSignals above. This stays an effect rather than a computed. Once
    // availability loads, auto-select the first available region if the parent
    // supplied none, notifying the parent like a user selection (the emit is
    // the escape-to-output edge a computed/linkedSignal can't express).
    effect(() => {
      const zones = this.allZones();
      if (zones.length === 0 || this.selectedRegion()) {
        return;
      }
      const firstAvailable = zones.find(z => z.isEnabled && z.status !== 'unavailable');
      if (firstAvailable) {
        this.onRegionSelected(firstAvailable.region);
      }
    });
  }

  protected onRegionSelected(region: string): void {
    this.selectedRegion.set(region);
    this.regionChange.emit(region);
    // Auto-select first available zone in the region
    this.autoSelectZone(region);
  }

  protected onZoneSelected(zone: string): void {
    this.selectedZone.set(zone);
    if (zone) {
      this.zoneChange.emit(zone);
    }
  }

  protected onRegionSearch(event: Event): void {
    this.regionSearch.set((event.target as HTMLInputElement).value);
  }

  isRegionAvailable(region: string): boolean {
    return this.availableRegions().has(region);
  }

  /** Gets the zone suffix (e.g., "a" from "us-central1-a"). */
  getZoneSuffix(zone: string): string {
    if (!zone) {
      return '';
    }
    const lastDash = zone.lastIndexOf('-');
    return lastDash !== -1 ? zone.substring(lastDash + 1) : zone;
  }

  formatGpuType(gpuType: string): string {
    const gpuLabels: Record<string, string> = {
      'nvidia-tesla-t4': 'NVIDIA T4',
      'nvidia-l4': 'NVIDIA L4',
      'nvidia-tesla-v100': 'NVIDIA V100',
      'nvidia-a100-40gb': 'NVIDIA A100',
      'nvidia-h100-80gb': 'NVIDIA H100',
      'nvidia-tesla-p4': 'NVIDIA P4',
    };
    return gpuLabels[gpuType] || gpuType;
  }

  private autoSelectZone(region: string): void {
    const zones = this.allZones()
      .filter(z => z.region === region && z.isEnabled && z.status !== 'unavailable')
      .sort((a, b) => a.zone.localeCompare(b.zone));

    this.onZoneSelected(zones.length > 0 ? zones[0].zone : '');
  }
}
