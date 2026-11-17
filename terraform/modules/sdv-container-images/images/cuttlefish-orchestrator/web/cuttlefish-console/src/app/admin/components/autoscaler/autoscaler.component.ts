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
import {ChangeDetectionStrategy, Component, inject} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {DEFAULT_FLEET_TIER} from '../../models/control-plane.model';
import {AUTOSCALER_POLL_MS, AutoscalerStore} from '../../store/autoscaler.signal-store';
import {AutoscalerControlsComponent} from './autoscaler-controls.component';
import {AutoscalerMastheadComponent} from './autoscaler-masthead.component';
import {DemandBandComponent} from './demand-band.component';
import {LimitsStripComponent} from './limits-strip.component';
import type {CanvasSection} from './topology-canvas.component';
import {TopologyCanvasComponent} from './topology-canvas.component';

/**
 * The fleet autoscaler page. It holds every autoscaler setting and a picture of
 * the fleet the autoscaler manages.
 *
 * It is a page of its own because the target's arithmetic connects things that
 * were on three different surfaces. The mode and the standing intent were on
 * the operations page, the measurement and the windows on the settings page,
 * and the fleet itself nowhere at all.
 *
 * The settings were moved here and removed from their old pages, because two
 * editors for one value is a defect. The settings page lost its fleet section
 * and the operations card shrank to a summary that links here.
 */
@Component({
  selector: 'app-admin-autoscaler',
  changeDetection: ChangeDetectionStrategy.OnPush,
  providers: [AutoscalerStore],
  imports: [
    MatButtonModule,
    MatIconModule,
    ErrorBannerComponent,
    LoadingStateComponent,
    PageHeaderComponent,
    PageLayoutComponent,
    AutoscalerControlsComponent,
    AutoscalerMastheadComponent,
    DemandBandComponent,
    LimitsStripComponent,
    TopologyCanvasComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Autoscaler"
          subtitle="What the fleet is holding, why the target says so, and what is permitted to act on it">
          <button type="button" secondary-actions mat-stroked-button (click)="toggleLive()">
            <mat-icon>{{ store.live() ? 'pause' : 'play_arrow' }}</mat-icon>
            {{ store.live() ? 'Live · ' + pollSeconds + 's' : 'Paused' }}
          </button>
          <button type="button" secondary-actions mat-stroked-button (click)="store.refresh()">
            <mat-icon>refresh</mat-icon>
            Refresh
          </button>
        </app-page-header>
      </div>

      <div page-content>
        @if (store.error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (store.snapshot(); as snapshot) {
          <section class="card masthead-card">
            <app-autoscaler-masthead [snapshot]="snapshot" />
          </section>

          <h2 class="section-title" id="machines">The machines CCO manages</h2>
          <section class="card">
            <app-topology-canvas [snapshot]="snapshot" (jump)="scrollTo($event)" />
          </section>

          <h2 class="section-title" id="window">What the window saw</h2>
          <section class="card">
            <app-demand-band
              [snapshot]="snapshot"
              [recommendation]="store.recommendation()"
              [error]="store.recommendationError()"
              [storedStandby]="store.standby()"
              (adopt)="adopt($event)" />
          </section>

          <h2 class="section-title" id="controls">Controls</h2>
          <section class="card">
            <app-autoscaler-controls />
          </section>

          <app-limits-strip [limits]="snapshot.limits" />
        } @else if (store.loading()) {
          <app-loading-state />
        }
      </div>
    </app-page-layout>
  `,
  styles: `
    .card {
      display: block;
      padding: var(--cco-space-16) var(--cco-space-24);
      margin-bottom: var(--cco-space-16);
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
    }

    .masthead-card {
      padding-block: var(--cco-space-24);
    }

    .section-title {
      margin: var(--cco-space-24) 0 var(--cco-space-12);
      font-size: var(--cco-text-body);
      font-weight: 500;
      color: var(--cco-text-secondary);
      scroll-margin-top: var(--cco-space-16);
    }
  `,
})
export class AutoscalerComponent implements OnInit {
  protected readonly store = inject(AutoscalerStore);
  protected readonly pollSeconds = AUTOSCALER_POLL_MS / 1000;

  ngOnInit(): void {
    this.store.startPolling();
    this.store.loadPolicy();
    // Loaded once rather than polled. A week's statistic does not move between
    // two five-second ticks, and the scan behind it is not free.
    this.store.loadRecommendation();
  }

  protected toggleLive(): void {
    this.store.toggleLive();
    if (this.store.live()) {
      this.store.refresh();
    }
  }

  /**
   * Adopting the window's suggestion moves the free-machine count and nothing
   * else. The suggestion is a peak of concurrent misses, which is a statement
   * about buffering and says nothing about how large the fleet should be. The
   * mode and the floor are carried through unchanged because the endpoint
   * replaces all three.
   *
   * It writes into the DEFAULT pool, because that is the population the
   * recommendation was measured over. It is a statistic across the whole
   * observation window rather than a per-pool one, and the pool every create
   * still lands in is the only one it describes.
   */
  protected adopt(standby: number): void {
    const policy = this.store.policy();
    if (policy) {
      this.store.saveAutoscaler(policy.mode, DEFAULT_FLEET_TIER, standby, this.store.floor());
    }
  }

  protected scrollTo(section: CanvasSection): void {
    document.getElementById(section)?.scrollIntoView({behavior: 'smooth', block: 'start'});
  }
}
