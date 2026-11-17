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
  effect,
  inject,
  input,
  linkedSignal,
  signal,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {Router, RouterLink} from '@angular/router';
import {HostStore} from '../../../hosts/store/host.signal-store';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {FeatureFlagsService} from '../../../shared/services';
import type {CreateDeviceRequest, GPUMode} from '../../models/device.model';
import {DeviceStore} from '../../store/device.signal-store';
import type {AdvancedSubmitEvent} from '../device-create-advanced';
import {DeviceCreateAdvancedComponent} from '../device-create-advanced';
import {DeviceCreateWizardComponent} from '../device-create-wizard';
import type {AdvancedSeed} from './create-intent.mapper';
import {handoffToAdvancedSeed, intentToAdvancedSeed} from './create-intent.mapper';
import {defaultCreateIntent} from './create-intent.model';
import {loadIntentDraft, saveIntentDraft} from './create-intent.storage';
import type {CreateMode} from './create-mode';
import {SDV_PAIR_PRESET} from './create-mode';

/**
 * Device creation in the mode chosen at /devices/create. Owns the create
 * INTENT that the Simple interview edits, persisted to localStorage. The
 * Advanced editor keeps its own draft, so each mode resumes where it was left.
 *
 * Both modes are one route (create/:mode), so the router reuses this instance
 * when the mode changes. That is what carries the Simple answers into
 * Advanced: the seed is set here and read by the editor when it is created.
 */
@Component({
  selector: 'app-device-create-page',
  imports: [
    MatButtonModule,
    MatIconModule,
    RouterLink,
    PageLayoutComponent,
    PageHeaderComponent,
    DeviceCreateWizardComponent,
    DeviceCreateAdvancedComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Create Device"
          [subtitle]="subtitle()"
          [showBackButton]="true"
          (back)="goBack()">
          @if (advancedModeEnabled) {
            <a secondary-actions
              mat-button
              class="change-mode"
              routerLink="/devices/create"
              [queryParams]="handoffParams()">
              <mat-icon>swap_horiz</mat-icon>
              Change mode
            </a>
          }
          @if (advancedModeEnabled && mode() === 'simple') {
            <button primary-action type="button" mat-stroked-button (click)="continueInAdvanced()">
              <mat-icon>tune</mat-icon>
              Continue in Advanced
            </button>
          }
        </app-page-header>
      </div>

      <div page-content>
        @if (mode() === 'simple') {
          <app-device-create-wizard
            [(intent)]="intent"
            [customizable]="advancedModeEnabled"
            (customize)="continueInAdvanced()" />
        } @else {
          <app-device-create-advanced
            [availableHosts]="availableHosts()"
            [seed]="advancedSeed()"
            [creating]="creating()"
            (canceled)="goBack()"
            (submitted)="onAdvancedSubmit($event)" />
        }
      </div>
    </app-page-layout>
  `,
  styles: `
    :host {
      display: block;
    }
  `,
})
export class DeviceCreatePageComponent implements OnInit {
  private readonly router = inject(Router);
  private readonly deviceStore = inject(DeviceStore);
  private readonly hostStore = inject(HostStore);

  protected readonly advancedModeEnabled = inject(FeatureFlagsService).advancedCvdConfigEnabled;

  /** Route param; the route only matches the modes this deployment offers. */
  readonly mode = input.required<CreateMode>();

  /** Route query params (withComponentInputBinding): handoffs and the chooser's preset. */
  readonly hostId = input<string>();
  readonly buildId = input<string>();
  readonly preset = input<string>();

  /** The starting state the Advanced editor reads when it is created. */
  protected readonly advancedSeed = signal<AdvancedSeed | null>(null);

  /** The shared create intent; restored from the persisted draft. */
  readonly intent = signal(loadIntentDraft() ?? defaultCreateIntent());

  readonly availableHosts = this.hostStore.availableForDevice;

  /** A create is in flight; the advanced editor disables its submit on it. */
  protected readonly creating = this.deviceStore.loading;

  protected readonly subtitle = computed(() => {
    if (!this.advancedModeEnabled) {
      return 'Launch a Cuttlefish device';
    }
    return this.mode() === 'simple' ? 'Simple mode' : 'Advanced mode';
  });

  /** The handoff params that still mean something on the chooser or in the other mode. */
  protected readonly handoffParams = computed(() => ({
    hostId: this.hostId() || undefined,
    buildId: this.buildId() || undefined,
  }));

  constructor() {
    this.hostStore.loadAll({});

    // Debounced draft autosave; best-effort, cleared on launch.
    effect(onCleanup => {
      const intent = this.intent();
      const timer = setTimeout(() => saveIntentDraft(intent), 800);
      onCleanup(() => clearTimeout(timer));
    });
  }

  ngOnInit(): void {
    // Handoffs: host-detail's "Create Device" and build-list's launch
    // action arrive with the question pre-answered.
    const hostId = this.hostId();
    const buildId = this.buildId();
    if (hostId) {
      this.intent.update(intent => ({...intent, host: {kind: 'host', id: hostId}}));
    }
    if (buildId) {
      this.intent.update(intent => ({...intent, build: {kind: 'registered', id: buildId}}));
    }

    const preset = this.preset();
    this.advancedSeed.set(
      handoffToAdvancedSeed({
        hostId,
        buildId,
        sdvPair: this.mode() === 'advanced' && preset === SDV_PAIR_PRESET,
      })
    );
    // The preset is applied once. Left in the URL, a reload would apply it
    // again over a pair the user has since edited.
    if (preset !== undefined) {
      void this.router.navigate(['/devices/create', this.mode()], {
        queryParams: this.handoffParams(),
        replaceUrl: true,
      });
    }
  }

  /** Seeded eject: the interview answers become the Advanced starting point. */
  continueInAdvanced(): void {
    this.advancedSeed.set(intentToAdvancedSeed(this.intent()));
    void this.router.navigate(['/devices/create', 'advanced'], {
      queryParams: this.handoffParams(),
    });
  }

  /** The advanced editor's last submitted body; the key is derived from it. */
  private readonly submittedRequest = signal<CreateDeviceRequest | null>(null);

  /**
   * Retry-safety for the advanced create. Device names are not unique and
   * nothing else dedupes, so a re-send after a failed-looking response would
   * otherwise create a second device. The backend fingerprints the request
   * against the key and 422s a reuse with different content, so an edited
   * config mints a new key while an unchanged one resubmits under the old.
   */
  private readonly idempotencyKey = linkedSignal<string, string>({
    source: () => JSON.stringify(this.submittedRequest()),
    computation: () => crypto.randomUUID(),
  });

  goBack(): void {
    void this.router.navigate(['/devices']);
  }

  onAdvancedSubmit(event: AdvancedSubmitEvent): void {
    const instance = event.config.instances[0];
    if (!instance || this.creating()) {
      return;
    }

    const request: CreateDeviceRequest = {
      // The editor's own device name, never its first instance's. A device
      // is the whole group, and at N>1 an instance name would label a
      // phone-and-watch pair "phone".
      name: event.name,
      gpuMode: event.gpuMode as GPUMode,
      mode: 'advanced',
      advancedConfig: {
        envConfig: event.config as unknown as Record<string, unknown>,
      },
      ttlHours: event.lifecycle.ttlHours,
      pinned: event.lifecycle.pinned,
      pinnedReason: event.lifecycle.pinnedReason || undefined,
      pinnedReasonDetail: event.lifecycle.pinnedReasonDetail || undefined,
      tags: event.lifecycle.tags.length > 0 ? event.lifecycle.tags : undefined,
    };

    if (event.hostChoice.kind === 'host') {
      request.hostId = event.hostChoice.id;
      request.buildId = event.buildChoice.kind === 'registered' ? event.buildChoice.id : undefined;
    } else {
      request.requirements = {
        interaction: event.interaction,
        capacity: event.capacity !== 'auto' ? event.capacity : undefined,
        build:
          event.buildChoice.kind === 'registered'
            ? {kind: 'registered', id: event.buildChoice.id}
            : undefined,
      };
    }

    // Recording the request is what lets the key be keyed on it. The editor
    // builds its config here instead of holding it as a signal, so the
    // submitted body is the only stable thing to derive from.
    this.submittedRequest.set(request);
    this.deviceStore.create({request, idempotencyKey: this.idempotencyKey()});
  }
}
