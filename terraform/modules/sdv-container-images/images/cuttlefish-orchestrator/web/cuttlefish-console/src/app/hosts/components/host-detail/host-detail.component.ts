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
import {ChangeDetectionStrategy, Component, computed, DestroyRef, inject} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {ActivatedRoute, Router, RouterModule} from '@angular/router';
import {StuckTaskBannerComponent} from '../../../admin/components/stuck-task-banner/stuck-task-banner.component';
import {HostLanesComponent} from '../../../devices/components/host-lanes/host-lanes.component';
import {buildRuler, occupancyHeadline} from '../../../devices/models/slot-space';
import {HostDevicesService} from '../../../devices/services/host-devices.service';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import {canExtendHost} from '../../models/host-extend';
import {HostActionsService} from '../../services/host-actions.service';
import {HostStore} from '../../store/host.signal-store';
import {HostAlertCardsComponent} from './host-alert-cards.component';
import {HostInfoCardsComponent} from './host-info-cards.component';
import {HostSummaryComponent} from './host-summary.component';

@Component({
  selector: 'app-host-detail',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LoadingStateComponent,
    ErrorBannerComponent,
    RouterModule,
    MatCardModule,
    MatButtonModule,
    MatIconModule,
    PageLayoutComponent,
    PageHeaderComponent,
    HostAlertCardsComponent,
    HostInfoCardsComponent,
    HostSummaryComponent,
    HostLanesComponent,
    StuckTaskBannerComponent,
  ],
  template: `
    <app-page-layout variant="detail">
      <div page-header>
        <app-page-header
          [title]="host()?.name || 'Host Details'"
          [subtitle]="host()?.id || ''"
          [showBackButton]="true"
          (back)="goBack()">
          <div primary-action>
            @if (host(); as host) {
              @if (host.status === 'stopped') {
                <button type="button"
                  mat-flat-button
                  (click)="actions.start(host)"
                  >
                  <mat-icon>play_arrow</mat-icon>
                  Start
                </button>
              }
              @if (host.status === 'running') {
                <button type="button"
                  mat-flat-button
                  color="warn"
                  (click)="actions.stop(host)"
                  >
                  <mat-icon>stop</mat-icon>
                  Stop
                </button>
              }
              @if (host.status === 'preempted' || (host.status === 'error' && host.error?.retryable)) {
                <button type="button"
                  mat-flat-button
                  (click)="actions.retry(host)"
                  >
                  <mat-icon>refresh</mat-icon>
                  Retry
                </button>
              }
              @if (canExtend()) {
                <button type="button" mat-button (click)="actions.extend(host)">
                  <mat-icon>more_time</mat-icon>
                  Extend
                </button>
              }
              <button type="button" mat-button (click)="actions.sync(host)">
                <mat-icon>sync</mat-icon>
                Sync
              </button>
              <button type="button"
                mat-button
                color="warn"
                (click)="actions.delete(host)"
                >
                <mat-icon>delete</mat-icon>
                Delete
              </button>
            }
          </div>
        </app-page-header>
      </div>

      @if (host(); as host) {
        <div page-sidebar>
          <app-host-summary [host]="host" (editCapacity)="actions.editCapacity(host)" />
        </div>
      }

      <div page-content>
        <!-- Loading State -->
        @if (loading()) {
          <app-loading-state />
        }

        <!-- Error State -->
        @if (error(); as error) {
          <app-error-banner [message]="error" />
        }

        <!-- Host Details -->
        @if (host(); as host) {
          <div class="host-content">
            <!-- Status indicator next to name -->
            <div class="host-status-row">
              <div [class]="'status-indicator ' + host.status"></div>
              @if (host.gceInstanceName) {
                <span class="instance-name">GCE instance: {{ host.gceInstanceName }}</span>
              }
              @if (host.incarnation) {
                <!-- The -i<N> suffix on the instance name is a rebuild, not a rename. -->
                <span class="instance-name">rebuild #{{ host.incarnation }}</span>
              }
            </div>
          <app-stuck-task-banner [resourceId]="host.id" />

          <app-host-alert-cards
            [host]="host"
            (retryRequested)="actions.retry(host, $event)"
            (deleteRequested)="actions.delete(host)" />

          <app-host-info-cards [host]="host" />

          <!-- Devices on this Host -->
          <mat-card class="devices-card">
            <mat-card-header>
              <mat-card-title>Devices</mat-card-title>
              <button type="button"
                mat-button
                color="primary"
                [routerLink]="['/devices/create']"
                [queryParams]="{hostId: host.id}"
                >
                <mat-icon>add</mat-icon>
                Create Device
              </button>
            </mat-card-header>
            <mat-card-content>
              @if (host.currentDevices === 0) {
                <p class="empty-devices">
                  No devices on this host.
                  <a [routerLink]="['/devices/create']" [queryParams]="{hostId: host.id}">
                    Create one now
                  </a>
                </p>
              } @else {
                @if (notches(); as slotNotches) {
                  <app-host-lanes
                    [devices]="lanes()"
                    [notches]="slotNotches"
                    [headline]="headline()"
                    [hostName]="host.name"
                    [openLane]="laneParam()"
                    (laneToggle)="rememberLane($event)"
                  />
                }
                <p class="as-list">
                  <a [routerLink]="['/devices']" [queryParams]="{hostId: host.id}">View as list</a>
                </p>
              }
            </mat-card-content>
          </mat-card>
          </div>
        }
      </div>
    </app-page-layout>
    `,
  styles: `
      /* Note: Container layout handled by PageLayoutComponent */

      .host-status-row {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 16px;
      }



      .instance-name {
        font-family: 'Roboto Mono', monospace;
        color: var(--cco-text-secondary);
        font-size: 12px;
      }

      .status-indicator {
        width: 16px;
        height: 16px;
        border-radius: 50%;
        background: var(--cco-text-secondary);
      }

      .status-indicator.running {
        background: var(--cco-success);
      }

      .status-indicator.stopped {
        background: var(--cco-warning);
      }

      .status-indicator.error {
        background: var(--cco-error);
      }

      .status-indicator.deleted {
        background: var(--cco-text-secondary);
      }

      @keyframes pulse {
        0% { opacity: 1; }
        50% { opacity: 0.5; }
        100% { opacity: 1; }
      }

      .devices-card mat-card-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
      }

      .empty-devices {
        color: var(--cco-text-secondary);
      }

      a {
        color: var(--cco-accent);
        text-decoration: none;
      }

      a:hover {
        text-decoration: underline;
      }

      .status-indicator.preempted {
        background: var(--cco-warning);
        animation: pulse 1.5s infinite;
      }

      .status-indicator.provisioning,
      .status-indicator.starting,
      .status-indicator.stopping,
      .status-indicator.deleting {
        background: var(--cco-accent);
        animation: pulse 1.5s infinite;
      }

      .devices-card {
        margin-bottom: 16px;
      }
  `,
})
export class HostDetailComponent implements OnInit {
  private readonly hostStore = inject(HostStore);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  private readonly destroyRef = inject(DestroyRef);
  protected readonly actions = inject(HostActionsService);
  private readonly hostDevices = inject(HostDevicesService);
  private readonly policy = inject(PlatformPolicyService);
  private readonly queryParams = toSignal(this.route.queryParamMap);

  /**
   * The lane a shared link names. Sharing a diagnosis is what the lane stack
   * is for, and a URL is how that travels.
   */
  protected readonly laneParam = computed(() => this.queryParams()?.get('lane') ?? null);

  /**
   * Keeps the URL on the lane the reader is actually looking at, so the link
   * they copy is the diagnosis they are describing. It replaces rather than
   * pushes, because expanding a lane is not a navigation, and a back button
   * that walked one collapse at a time would be worse than none.
   */
  protected rememberLane(event: {deviceId: string; expanded: boolean}): void {
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: {lane: event.expanded ? event.deviceId : null},
      queryParamsHandling: 'merge',
      replaceUrl: true,
    });
  }

  /**
   * What the host image can actually back, served rather than retyped. A
   * respin that changes it must not need a console edit to be drawn right.
   */
  private readonly backedSlots = computed(() => this.policy.lifetimes().hostImageBackedSlots);
  protected readonly host = this.hostStore.selectedHost;
  protected readonly loading = this.hostStore.loading;
  protected readonly error = this.hostStore.error;
  protected readonly canExtend = computed(() => {
    const host = this.host();
    return host ? canExtendHost(host) : false;
  });

  protected readonly lanes = this.hostDevices.lanes;

  /**
   * The host's slot scale.
   *
   * Undrawn until some instance on the host has a resolved slot. An empty
   * occupant set would render every slot free, which is a claim about the host
   * rather than the absence of one, and a guessed layout mis-seats every notch
   * after the first hole.
   */
  protected readonly notches = computed(() => {
    const host = this.host();
    const occupants = this.hostDevices.occupants();
    if (!host || occupants.length === 0) {
      return null;
    }
    return buildRuler(host.maxDevices, this.backedSlots(), occupants);
  });

  protected readonly headline = computed(() => {
    const host = this.host();
    return host
      ? occupancyHeadline(this.hostDevices.usedSlots(), this.backedSlots(), host.maxDevices)
      : '';
  });

  ngOnInit(): void {
    const id = this.route.snapshot.paramMap.get('id');
    if (id) {
      this.hostStore.selectHost(id);
      this.hostStore.loadOne(id);
      // The card needs the occupant set rather than the counter, because
      // `currentDevices` is a scalar and a layout cannot be drawn from a count.
      this.hostDevices.load(id);
      // Keep the page live while the host is provisioning/starting. This is
      // the "watch it come up" step of the manual journey. The store is
      // root-scoped, so the poll must die with this view.
      const watch = this.hostStore.watch(id);
      this.destroyRef.onDestroy(() => watch.destroy());
    }
  }

  protected goBack(): void {
    void this.router.navigate(['/hosts']);
  }
}
