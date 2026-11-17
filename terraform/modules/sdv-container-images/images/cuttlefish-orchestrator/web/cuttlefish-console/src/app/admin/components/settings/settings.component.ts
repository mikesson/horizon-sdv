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
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  inject,
  signal,
} from '@angular/core';
import {takeUntilDestroyed} from '@angular/core/rxjs-interop';
import {MatButtonModule} from '@angular/material/button';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import {MatTooltipModule} from '@angular/material/tooltip';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {SettingsCardComponent} from '../../../shared/components/settings-card/settings-card.component';
import type {SwitchStatus} from '../../../shared/components/settings-card/settings-switch-row.component';
import {SettingsSwitchRowComponent} from '../../../shared/components/settings-card/settings-switch-row.component';
import type {PlatformLifetimes} from '../../../shared/models/platform-lifetimes.schema';
import {StorageApiService} from '../../../storage/services/storage-api.service';
import type {
  ArtifactSettings,
  DefaultsSettings,
  PlacementSettings,
  SpotSettings,
} from '../../models/settings.model';
import {SPOT_UPTIME_TIERS} from '../../models/settings.model';
import {SettingsAdminApiService} from '../../services/settings-admin-api.service';
import {SettingsSectionStore} from './settings-section.store';

/** The per-bucket write probe's state. */
type ProbeState = 'idle' | 'checking' | 'ok' | 'failed';

/**
 * Every runtime setting on one page, sections ordered by how often they
 * change, each loading and failing on its own.
 *
 * Independent per section rather than one request. Consolidating the page
 * into one call would mean a single failing endpoint blanks every setting,
 * which is strictly worse than the separate pages this replaces.
 */
@Component({
  selector: 'app-admin-settings',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSelectModule,
    MatTooltipModule,
    ErrorBannerComponent,
    LoadingStateComponent,
    PageHeaderComponent,
    PageLayoutComponent,
    SettingsCardComponent,
    SettingsSwitchRowComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Settings"
          subtitle="What the platform picks when a caller does not say" />
      </div>

      <div page-content>
        <!-- Lifetimes are one card, because the backend couples them. A device
             whose expiry outlasts its host raises the host's lease, so the
             two durations are one policy rather than two fields. -->
        <app-settings-card
          title="Session and lease lifetimes"
          subtitle="How long devices and hosts live when nobody says otherwise"
          [dirty]="lifetimes.dirty()"
          [saving]="lifetimes.saving()"
          (save)="saveLifetimes()">
          @if (lifetimes.loading()) {
            <app-loading-state size="section" />
          } @else if (lifetimes.error(); as message) {
            <app-error-banner [message]="message" />
          } @else if (lifetimes.draft(); as draft) {
            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Device session (hours)</mat-label>
                <input
                  matInput
                  type="number"
                  min="1"
                  [max]="draft.maxDeviceTtlHours"
                  [value]="draft.deviceTtlHours"
                  (input)="lifetimes.patch({deviceTtlHours: number($event)})">
                <mat-hint>Granted when a create names no TTL; a request may ask up to {{ draft.maxDeviceTtlHours }} h</mat-hint>
              </mat-form-field>
              <span class="provenance">{{ provenance(draft.isSet) }}</span>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" class="compact" subscriptSizing="dynamic">
                <mat-label>Extensions</mat-label>
                <input
                  matInput
                  type="number"
                  min="0"
                  [max]="draft.maxExtendCount"
                  [value]="draft.extendCount"
                  (input)="lifetimes.patch({extendCount: number($event)})">
                <mat-hint>0 = none: pin it or lose it</mat-hint>
              </mat-form-field>
              <span class="extend-times">×</span>
              <mat-form-field appearance="outline" class="compact" subscriptSizing="dynamic">
                <mat-label>Hours each</mat-label>
                <input
                  matInput
                  type="number"
                  min="1"
                  [max]="draft.maxExtendHours"
                  [value]="draft.extendHours"
                  (input)="lifetimes.patch({extendHours: number($event)})">
              </mat-form-field>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Host lease (hours)</mat-label>
                <input
                  matInput
                  type="number"
                  min="1"
                  [max]="draft.maxHostTtlHours"
                  [value]="draft.hostTtlHours"
                  (input)="lifetimes.patch({hostTtlHours: number($event)})">
                <mat-hint>Where a fresh host's clock starts; raising the {{ draft.maxHostTtlHours }} h cap is a deploy</mat-hint>
              </mat-form-field>
            </div>

            <!-- The consequence, derived live. The lease-raise is the one
                 mechanic nobody would guess from the field labels. -->
            <div class="derived">
              <p>{{ longestSession() }}</p>
              <p>{{ leaseNote() }}</p>
            </div>
          }
        </app-settings-card>

        <!-- Placement -->
        <app-settings-card
          title="Placement"
          subtitle="Where automatic placement is allowed to put hosts"
          [dirty]="placement.dirty()"
          [saving]="placement.saving()"
          (save)="savePlacement()">
          @if (placement.loading()) {
            <app-loading-state size="section" />
          } @else if (placement.error(); as message) {
            <app-error-banner [message]="message" />
          } @else if (placement.draft(); as draft) {
            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Zone pin</mat-label>
                <input
                  matInput
                  [value]="draft.zonePin"
                  placeholder="us-central1-a"
                  (input)="placement.patch({zonePin: value($event)})">
                <mat-hint>Empty ranks across the whole enabled catalog</mat-hint>
              </mat-form-field>
              <span class="provenance">{{ provenance(draft.zonePinIsSet) }}</span>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Quota reserve margin (%)</mat-label>
                <input
                  matInput
                  type="number"
                  [value]="draft.quotaMarginPercent"
                  (input)="placement.patch({quotaMarginPercent: number($event)})">
                <mat-hint>Headroom ranking keeps back on every regional quota</mat-hint>
              </mat-form-field>
              <span class="provenance">{{ provenance(draft.quotaMarginIsSet) }}</span>
            </div>
          }
        </app-settings-card>

        @if (placement.draft(); as draft) {
          <app-settings-card title="Legacy API" variant="immediate">
            <app-settings-switch-row
              label="The legacy /v1 zone token is a placement hint"
              hint="Off pins legacy creates to the literal zone they name, losing dynamic placement."
              [checked]="draft.v1ZoneIsHint"
              [status]="v1HintState()"
              (toggled)="setV1ZoneHint($event)" />
            @if (v1HintError(); as message) {
              <app-error-banner [message]="message" />
            }
          </app-settings-card>
        }

        <!-- Spot capacity -->
        <app-settings-card
          title="Spot capacity"
          subtitle="How much estimated uptime Spot placement demands"
          [dirty]="spot.dirty()"
          [saving]="spot.saving()"
          (save)="saveSpot()">
          @if (spot.loading()) {
            <app-loading-state size="section" />
          } @else if (spot.error(); as message) {
            <app-error-banner [message]="message" />
          } @else if (spot.draft(); as draft) {
            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Minimum uptime for single devices</mat-label>
                <mat-select
                  [value]="draft.minUptimeSingleSeconds"
                  (selectionChange)="spot.patch({minUptimeSingleSeconds: $event.value})">
                  @for (tier of tiers; track tier.value) {
                    <mat-option [value]="tier.value">{{ tier.label }}</mat-option>
                  }
                </mat-select>
              </mat-form-field>
              <span class="provenance">{{ provenance(draft.minUptimeIsSet) }}</span>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Minimum uptime for batch members</mat-label>
                <mat-select
                  [value]="draft.minUptimeBatchSeconds"
                  (selectionChange)="spot.patch({minUptimeBatchSeconds: $event.value})">
                  @for (tier of tiers; track tier.value) {
                    <mat-option [value]="tier.value">{{ tier.label }}</mat-option>
                  }
                </mat-select>
                <mat-hint>A CI shard dying at minute 9 of 10 wastes the whole run</mat-hint>
              </mat-form-field>
            </div>
          }
        </app-settings-card>

        @if (spot.draft(); as draft) {
          <app-settings-card title="Spot by default" variant="immediate">
            <app-settings-switch-row
              label="Headless devices default to Spot hosts"
              hint="Applies to auto capacity only; interactive intent always resolves to standard."
              [checked]="draft.autoDefault"
              [status]="spotDefaultState()"
              (toggled)="setSpotAutoDefault($event)" />
            @if (spotDefaultError(); as message) {
              <app-error-banner [message]="message" />
            }
          </app-settings-card>
        }

        <!-- Defaults -->
        <app-settings-card
          title="Defaults for new resources"
          subtitle="What a create resolves to when the caller names nothing"
          [dirty]="defaults.dirty()"
          [saving]="defaults.saving()"
          (save)="saveDefaults()">
          @if (defaults.loading()) {
            <app-loading-state size="section" />
          } @else if (defaults.error(); as message) {
            <app-error-banner [message]="message" />
          } @else if (defaults.draft(); as draft) {
            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>GPU host machine type</mat-label>
                <input
                  matInput
                  [value]="draft.gpuMachineType"
                  placeholder="g2-standard-8"
                  (input)="defaults.patch({gpuMachineType: value($event)})">
                <mat-hint>Empty uses the first enabled GPU-capable catalog type</mat-hint>
              </mat-form-field>
              <span class="provenance">{{ provenance(draft.gpuMachineTypeIsSet) }}</span>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Upstream branch</mat-label>
                <input
                  matInput
                  [value]="draft.upstreamBranch"
                  (input)="defaults.patch({upstreamBranch: value($event)})">
              </mat-form-field>
              <span class="provenance">{{ provenance(draft.upstreamBuildIsSet) }}</span>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Upstream target</mat-label>
                <input
                  matInput
                  [value]="draft.upstreamTarget"
                  (input)="defaults.patch({upstreamTarget: value($event)})">
                <mat-hint>Used when a declarative create finds no registered builds</mat-hint>
              </mat-form-field>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Legacy docker-host machine type</mat-label>
                <input
                  matInput
                  [value]="draft.dockerMachineType"
                  placeholder="n1-standard-8"
                  (input)="defaults.patch({dockerMachineType: value($event)})">
                <mat-hint>The GCE shape a legacy docker-only host create (OmniLab ATS) provisions</mat-hint>
              </mat-form-field>
              <span class="provenance">{{ provenance(draft.dockerHostIsSet) }}</span>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Legacy docker-host lease (hours)</mat-label>
                <input
                  matInput
                  type="number"
                  min="1"
                  [max]="draft.dockerMaxTtlHours"
                  [value]="draft.dockerTtlHours"
                  (input)="defaults.patch({dockerTtlHours: number($event)})">
                <mat-hint>Its whole lifetime, and nothing can extend it; cap {{ draft.dockerMaxTtlHours }}h</mat-hint>
              </mat-form-field>
            </div>
          }
        </app-settings-card>

        <!-- Artifacts -->
        <app-settings-card
          title="Artifacts"
          [subtitle]="artifactsSubtitle()"
          [dirty]="artifacts.dirty()"
          [saving]="artifacts.saving()"
          (save)="saveArtifacts()">
          @if (artifacts.loading()) {
            <app-loading-state size="section" />
          } @else if (artifacts.error(); as message) {
            <app-error-banner [message]="message" />
          } @else if (artifacts.draft(); as draft) {
            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Bugreport bucket</mat-label>
                <input
                  matInput
                  [value]="draft.bugreportBucket"
                  (input)="artifacts.patch({bugreportBucket: value($event)})">
                <mat-hint>{{ effectiveBucket(draft.bugreportBucket, draft.defaultBucket) }}</mat-hint>
              </mat-form-field>
              <button type="button"
                mat-stroked-button
                [disabled]="!effective(draft.bugreportBucket, draft.defaultBucket)"
                (click)="verify('bugreport', effective(draft.bugreportBucket, draft.defaultBucket))">
                {{ probeLabel(probes().bugreport) }}
              </button>
              <span class="provenance">{{ provenance(draft.artifactsIsSet) }}</span>
            </div>

            <div class="field-row">
              <mat-form-field appearance="outline" subscriptSizing="dynamic">
                <mat-label>Recording bucket</mat-label>
                <input
                  matInput
                  [value]="draft.recordingBucket"
                  (input)="artifacts.patch({recordingBucket: value($event)})">
                <mat-hint>{{ effectiveBucket(draft.recordingBucket, draft.defaultBucket) }}</mat-hint>
              </mat-form-field>
              <button type="button"
                mat-stroked-button
                [disabled]="!effective(draft.recordingBucket, draft.defaultBucket)"
                (click)="verify('recording', effective(draft.recordingBucket, draft.defaultBucket))">
                {{ probeLabel(probes().recording) }}
              </button>
            </div>
          }
        </app-settings-card>

        @if (artifacts.draft(); as draft) {
          <app-settings-card title="Automatic capture" variant="immediate">
            <app-settings-switch-row
              label="Capture a bugreport when a device errors"
              hint="A no-op until a bucket above is configured."
              [checked]="draft.captureOnError"
              [status]="captureState()"
              (toggled)="setCaptureOnError($event)" />
            @if (captureError(); as message) {
              <app-error-banner [message]="message" />
            }
          </app-settings-card>
        }
      </div>
    </app-page-layout>
  `,
  styles: `
    app-settings-card {
      display: block;
      margin-bottom: var(--cco-space-24);
    }

    .field-row {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
      margin-bottom: var(--cco-space-16);
    }

    .field-row mat-form-field {
      flex: 1;
      max-width: 420px;
    }

    /* Stated at rest rather than on hover: whether an operator chose the value. */
    .provenance {
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
      white-space: nowrap;
    }

    .field-row mat-form-field.compact {
      max-width: 160px;
    }

    .extend-times {
      color: var(--cco-text-secondary);
    }

    /* The consequences of the values above, restated as the mechanic. */
    .derived {
      max-width: 560px;
      padding: var(--cco-space-12) var(--cco-space-16);
      border-left: 2px solid var(--cco-border);
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .derived p {
      margin: 0;
    }

    .derived p + p {
      margin-top: var(--cco-space-8);
    }
  `,
})
export class SettingsComponent implements OnInit {
  private readonly api = inject(SettingsAdminApiService);
  private readonly storageApi = inject(StorageApiService);
  private readonly destroyRef = inject(DestroyRef);

  protected readonly tiers = SPOT_UPTIME_TIERS;

  protected readonly lifetimes = new SettingsSectionStore<PlatformLifetimes>(
    () => this.api.getLifetimes(),
    this.destroyRef
  );
  protected readonly placement = new SettingsSectionStore<PlacementSettings>(
    () => this.api.getPlacement(),
    this.destroyRef
  );
  protected readonly spot = new SettingsSectionStore<SpotSettings>(
    () => this.api.getSpot(),
    this.destroyRef
  );
  protected readonly defaults = new SettingsSectionStore<DefaultsSettings>(
    () => this.api.getDefaults(),
    this.destroyRef
  );
  protected readonly artifacts = new SettingsSectionStore<ArtifactSettings>(
    () => this.api.getArtifacts(),
    this.destroyRef
  );

  protected readonly v1HintState = signal<SwitchStatus>('idle');
  protected readonly spotDefaultState = signal<SwitchStatus>('idle');
  protected readonly captureState = signal<SwitchStatus>('idle');
  // A flip's failure belongs to its own card; the section's error banner
  // replaces the sibling form's fields.
  protected readonly v1HintError = signal<string | null>(null);
  protected readonly spotDefaultError = signal<string | null>(null);
  protected readonly captureError = signal<string | null>(null);
  protected readonly probes = signal<{bugreport: ProbeState; recording: ProbeState}>({
    bugreport: 'idle',
    recording: 'idle',
  });

  /**
   * The two derived lines under the lifetimes card, moving as the operator
   * types. Grace comes down the wire, because retyping 15 minutes here would be
   * a fresh copy of the constant this card exists to retire.
   */
  protected readonly longestSession = computed(() => {
    const draft = this.lifetimes.draft();
    if (!draft) {
      return '';
    }
    if (draft.extendCount <= 0) {
      return `Longest possible session: ${draft.deviceTtlHours} h, with no extensions once created.`;
    }
    const total = draft.deviceTtlHours + draft.extendCount * draft.extendHours;
    return (
      `Longest possible session: ${total} h ` +
      `(${draft.deviceTtlHours} h + ${draft.extendCount} × ${draft.extendHours} h of extensions).`
    );
  });

  protected readonly leaseNote = computed(() => {
    const draft = this.lifetimes.draft();
    if (!draft) {
      return '';
    }
    const graceMinutes = Math.round(draft.gracePeriodSeconds / 60);
    return (
      `A host carrying a device stays leased until the device's promise plus a ` +
      `${graceMinutes}-minute teardown margin. The host lease bounds only idle and standby hosts.`
    );
  });

  protected readonly artifactsSubtitle = computed(() => {
    const bucket = this.artifacts.draft()?.defaultBucket;
    return bucket
      ? `Deployment default: ${bucket}`
      : 'No deployment bucket is configured; capture needs an override below';
  });

  ngOnInit(): void {
    this.lifetimes.load();
    this.placement.load();
    this.spot.load();
    this.defaults.load();
    this.artifacts.load();
  }

  protected value(event: Event): string {
    return (event.target as HTMLInputElement).value;
  }

  protected number(event: Event): number {
    return Number((event.target as HTMLInputElement).value);
  }

  protected provenance(isSet: boolean): string {
    return isSet ? 'stored' : 'default';
  }

  protected effective(override: string, fallback: string): string {
    return override || fallback;
  }

  protected probeLabel(state: ProbeState): string {
    switch (state) {
      case 'checking':
        return 'Checking…';
      case 'ok':
        return 'Writable';
      case 'failed':
        return 'Not writable';
      default:
        return 'Verify write access';
    }
  }

  /** IAM misconfiguration is invisible until a capture fails hours later. */
  protected verify(kind: 'bugreport' | 'recording', bucket: string): void {
    this.probes.update(p => ({...p, [kind]: 'checking'}));
    this.storageApi
      .verifyAccess(bucket, undefined, undefined, true)
      .pipe(takeUntilDestroyed(this.destroyRef))
      .subscribe({
        next: res => this.probes.update(p => ({...p, [kind]: res.canProceed ? 'ok' : 'failed'})),
        error: () => this.probes.update(p => ({...p, [kind]: 'failed'})),
      });
  }

  protected effectiveBucket(override: string, fallback: string): string {
    if (override) {
      return `Effective: ${override}`;
    }
    return fallback ? `Effective: ${fallback} (deployment default)` : 'Not configured';
  }

  protected saveLifetimes(): void {
    this.lifetimes.save((next, baseline) => this.api.saveLifetimes(next, baseline));
  }

  protected savePlacement(): void {
    this.placement.save((next, baseline) => this.api.savePlacement(next, baseline));
  }

  protected saveSpot(): void {
    this.spot.save((next, baseline) => this.api.saveSpot(next, baseline));
  }

  protected saveDefaults(): void {
    this.defaults.save((next, baseline) => this.api.saveDefaults(next, baseline));
  }

  protected saveArtifacts(): void {
    this.artifacts.save((next, baseline) => this.api.saveArtifacts(next, baseline));
  }

  protected setV1ZoneHint(value: boolean): void {
    this.placement.toggle(
      {v1ZoneIsHint: value, v1ZoneHintIsSet: true},
      this.api.saveV1ZoneHint(value),
      this.v1HintState,
      this.v1HintError
    );
  }

  protected setSpotAutoDefault(value: boolean): void {
    this.spot.toggle(
      {autoDefault: value, autoDefaultIsSet: true},
      this.api.saveSpotAutoDefault(value),
      this.spotDefaultState,
      this.spotDefaultError
    );
  }

  protected setCaptureOnError(value: boolean): void {
    const baseline = this.artifacts.baseline();
    if (!baseline) {
      return;
    }
    this.artifacts.toggle(
      {captureOnError: value, artifactsIsSet: true},
      // The endpoint carries all three keys, so the body comes from the
      // baseline. Built from the draft it would persist bucket text the
      // operator typed but never saved.
      this.api.saveArtifacts({...baseline, captureOnError: value}, baseline),
      this.captureState,
      this.captureError
    );
  }
}
