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
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {RouterModule} from '@angular/router';
import {PlatformStatsService} from '../admin/services/platform-stats.service';
import {TaskAdminApiService} from '../admin/services/task-admin-api.service';
import {AuthStore} from '../auth/store/auth.signal-store';
import {BuildStore} from '../builds/store/build.signal-store';
import {DeviceStore} from '../devices/store/device.signal-store';
import {QuotaGaugeComponent} from '../quota/components/quota-gauge/quota-gauge.component';
import {QuotaService} from '../quota/services/quota.service';
import {EmptyStateComponent} from '../shared/components/empty-state/empty-state.component';
import {resolveBasePath} from '../shared/utils';

/** Hosts/devices come from server-truth /v2/admin/stats; builds from the store. */
interface DashboardStats {
  hosts: {total: number; running: number};
  devices: {total: number; running: number};
  builds: {total: number};
}

@Component({
  selector: 'app-dashboard',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    RouterModule,
    MatCardModule,
    MatButtonModule,
    MatIconModule,
    EmptyStateComponent,
    QuotaGaugeComponent,
  ],
  template: `
    @let s = stats();

    <header class="hero">
      <mat-icon class="hero__logo">cloud</mat-icon>
      <h1 class="hero__title">Welcome</h1>
      <p class="hero__product">Cloud Cuttlefish Orchestrator</p>
      @if (user(); as u) {
        <p class="hero__user">Signed in as {{ u.email }}</p>
      }
    </header>

    <div class="body">
      <section>
        <h2 class="section-title">Platform status</h2>
        <div class="status-grid">
          <div class="status-card">
            <app-quota-gauge [quota]="quotaService.currentQuota()" />
          </div>
          <a
            class="card status-card queue-card"
            routerLink="/admin/control-plane"
            [class.attention]="queueFailed() > 0">
            <mat-icon class="card__icon" [class.attention-icon]="queueFailed() > 0">
              {{ queueFailed() > 0 ? 'error_outline' : 'check_circle' }}
            </mat-icon>
            <span class="card__text">
              <span class="card__label">{{ queueLabel() }}</span>
              <span class="card__sub">{{ queueSub() }}</span>
            </span>
          </a>
        </div>
      </section>

      <section>
        <h2 class="section-title">Quick access</h2>
        <div class="grid">
          <a class="card" routerLink="/devices">
            <mat-icon class="card__icon">phone_android</mat-icon>
            <span class="card__text">
              <span class="card__label">Devices</span>
              <span class="card__sub">{{ s.devices.total }} total · {{ s.devices.running }} running</span>
            </span>
          </a>
          <a class="card" routerLink="/builds">
            <mat-icon class="card__icon">build</mat-icon>
            <span class="card__text">
              <span class="card__label">Builds</span>
              <span class="card__sub">{{ s.builds.total }} registered</span>
            </span>
          </a>
          <a class="card" routerLink="/hosts">
            <mat-icon class="card__icon">dns</mat-icon>
            <span class="card__text">
              <span class="card__label">Hosts</span>
              <span class="card__sub">{{ s.hosts.total }} total · {{ s.hosts.running }} running</span>
            </span>
          </a>
          <a class="card" routerLink="/admin/catalog">
            <mat-icon class="card__icon">category</mat-icon>
            <span class="card__text">
              <span class="card__label">Catalog</span>
              <span class="card__sub">Host image configuration</span>
            </span>
          </a>

          <a class="card" routerLink="/devices/create">
            <mat-icon class="card__icon">add_to_queue</mat-icon>
            <span class="card__text">
              <span class="card__label">Create Device</span>
              <span class="card__sub">Launch a Cuttlefish device</span>
            </span>
          </a>
          <a class="card" routerLink="/builds/create">
            <mat-icon class="card__icon">cloud_upload</mat-icon>
            <span class="card__text">
              <span class="card__label">Register Build</span>
              <span class="card__sub">Add an Android build</span>
            </span>
          </a>
          <a class="card" routerLink="/hosts/create">
            <mat-icon class="card__icon">add_circle</mat-icon>
            <span class="card__text">
              <span class="card__label">Create Host</span>
              <span class="card__sub">Deploy a new host VM</span>
            </span>
          </a>
          <a class="card" [href]="docsUrl" target="_blank" rel="noopener noreferrer">
            <mat-icon class="card__icon">api</mat-icon>
            <span class="card__text">
              <span class="card__label">API Documentation</span>
              <span class="card__sub">REST API reference</span>
            </span>
            <mat-icon class="card__ext">open_in_new</mat-icon>
          </a>
        </div>
      </section>

      <section>
        <h2 class="section-title">Recent devices</h2>
        @if (recentDevices().length > 0) {
          <div class="recent-list">
            @for (device of recentDevices(); track device.id) {
              <a class="recent-item" routerLink="/devices">
                <mat-icon
                  class="recent-item__status"
                  [class.running]="device.status === 'running'"
                  [class.stopped]="device.status === 'stopped'">
                  {{ device.status === 'running' ? 'play_circle' : 'pause_circle' }}
                </mat-icon>
                <span class="recent-item__details">
                  <span class="recent-item__name">{{ device.name }}</span>
                  <span class="recent-item__build">{{ device.buildTarget || '-' }}</span>
                </span>
                <span [class]="['status-pill', device.status]">{{ device.status }}</span>
              </a>
            }
          </div>
        } @else {
          <app-empty-state
            icon="phone_android"
            message="No devices yet. Create your first device to get started."
          >
            <button type="button" action mat-flat-button routerLink="/devices/create">
              Create Device
            </button>
          </app-empty-state>
        }
      </section>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }

    .hero {
      padding: 32px 32px;
      background: var(--cco-surface-variant);
      border-bottom: 1px solid var(--cco-border);
    }

    .hero__logo {
      font-size: 32px;
      width: 32px;
      height: 32px;
      color: var(--cco-accent);
    }

    .hero__title {
      margin: 12px 0 0;
      font-size: 28px;
      font-weight: 400;
      color: var(--cco-text);
    }

    .hero__product {
      margin: 4px 0 0;
      font-size: 16px;
      color: var(--cco-text-secondary);
    }

    .hero__user {
      margin: 12px 0 0;
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .body {
      display: flex;
      flex-direction: column;
      gap: 32px;
      padding: 24px 32px;
    }

    .section-title {
      margin: 0 0 16px;
      font-size: 16px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
      gap: 16px;
    }

    .status-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(280px, 420px));
      gap: 16px;
    }

    .status-card {
      padding: 16px 24px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
    }

    .queue-card .card__icon {
      color: var(--cco-success);
    }

    .queue-card .attention-icon {
      color: var(--cco-error);
    }

    .queue-card.attention {
      border-color: var(--cco-error);
    }

    .card {
      display: flex;
      align-items: center;
      gap: 16px;
      padding: 16px 24px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      text-decoration: none;
      transition: background-color var(--cco-duration-base) ease, border-color var(--cco-duration-base) ease;
    }

    .card:hover {
      background: var(--cco-surface-variant);
      border-color: var(--cco-text-secondary);
    }

    .card__icon {
      flex-shrink: 0;
      color: var(--cco-accent);
    }

    .card__text {
      display: flex;
      flex-direction: column;
      gap: 2px;
      min-width: 0;
    }

    .card__label {
      font-size: 14px;
      font-weight: 500;
      color: var(--cco-text);
    }

    .card__sub {
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .card__ext {
      margin-left: auto;
      font-size: 16px;
      width: 16px;
      height: 16px;
      color: var(--cco-text-secondary);
    }

    .recent-list {
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    .recent-item {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 12px 16px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      text-decoration: none;
      transition: background-color var(--cco-duration-base) ease, border-color var(--cco-duration-base) ease;
    }

    .recent-item:hover {
      background: var(--cco-surface-variant);
      border-color: var(--cco-text-secondary);
    }

    .recent-item__status.running {
      color: var(--cco-success);
    }

    .recent-item__status.stopped {
      color: var(--cco-text-secondary);
    }

    .recent-item__details {
      display: flex;
      flex-direction: column;
      min-width: 0;
    }

    .recent-item__name {
      font-weight: 500;
      color: var(--cco-text);
    }

    .recent-item__build {
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    .status-pill {
      margin-left: auto;
      padding: 4px 12px;
      border-radius: var(--cco-radius-pill);
      font-size: 12px;
      font-weight: 500;
      text-transform: capitalize;
    }

    .status-pill.running {
      background: var(--cco-success-surface);
      color: var(--cco-success);
    }

    .status-pill.stopped {
      background: var(--cco-surface-variant);
      color: var(--cco-text-secondary);
    }

    .status-pill.starting,
    .status-pill.stopping {
      background: var(--cco-warning-surface);
      color: var(--cco-warning);
    }



  `,
})
export class DashboardComponent implements OnInit {
  private readonly deviceStore = inject(DeviceStore);
  private readonly buildStore = inject(BuildStore);
  private readonly platformStats = inject(PlatformStatsService);
  private readonly taskAdminApi = inject(TaskAdminApiService);
  protected readonly quotaService = inject(QuotaService);

  protected readonly user = inject(AuthStore).user;
  protected readonly docsUrl = resolveBasePath('/docs');

  protected readonly stats = computed<DashboardStats>(() => {
    const platform = this.platformStats.stats();
    return {
      hosts: {
        total: platform?.hosts.total ?? 0,
        running: platform?.hosts.active ?? 0,
      },
      devices: {
        total: platform?.devices.total ?? 0,
        running: platform?.devices.active ?? 0,
      },
      builds: {total: this.buildStore.entities().length},
    };
  });

  protected readonly queueFailed = computed(() => this.taskAdminApi.queueStats()?.failed ?? 0);

  protected readonly queueLabel = computed(() => {
    const failed = this.queueFailed();
    return failed > 0
      ? `${failed} failed ${failed === 1 ? 'task' : 'tasks'}`
      : 'Task queue healthy';
  });

  protected readonly queueSub = computed(() => {
    const stats = this.taskAdminApi.queueStats();
    return stats
      ? `${stats.pending} pending · ${stats.leased} in progress`
      : 'Background provisioning queue';
  });

  protected readonly recentDevices = computed(() =>
    [...this.deviceStore.entities()]
      .sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
      .slice(0, 5)
  );

  ngOnInit(): void {
    this.deviceStore.loadAll({});
    this.buildStore.loadAll({});
    // The httpResources load on first injection; reload when revisiting.
    if (this.platformStats.stats()) {
      this.platformStats.refresh();
    }
    if (this.taskAdminApi.queueStats()) {
      this.taskAdminApi.refreshStats();
    }
    if (this.quotaService.currentQuota()) {
      this.quotaService.refresh();
    }
  }
}
