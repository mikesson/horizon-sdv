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
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import type {KpiItem} from '../../../shared/components';
import {
  ConfirmDialogComponent,
  type ConfirmDialogData,
  KpiStripComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import type {FleetPanel, SweepPolicy} from '../../models/control-plane.model';
import {BACKGROUND_PLANE_LEASE} from '../../models/control-plane.model';
import type {ProvisioningOutcome} from '../../models/operator-lens.model';
import {successRate} from '../../models/operator-lens.model';
import type {FailedTask} from '../../models/task-admin.model';
import {ProvisioningStatsService} from '../../services/provisioning-stats.service';
import {TaskAdminApiService} from '../../services/task-admin-api.service';
import {ControlPlaneStore, POLL_INTERVAL_MS} from '../../store/control-plane.signal-store';
import {TaskAdminStore} from '../../store/task-admin.signal-store';
import {ConnectivityPanelComponent} from './connectivity-panel.component';
import {LiveWorkComponent} from './live-work.component';
import {OperationsPanelComponent} from './operations-panel.component';
import type {PipelineModel, PipelineSection} from './pipeline-flow.component';
import {PipelineFlowComponent} from './pipeline-flow.component';
import {QueueSparklineComponent} from './queue-sparkline.component';
import {ReconcilerPanelComponent} from './reconciler-panel.component';
import {TaskTableComponent} from './task-table.component';

@Component({
  selector: 'app-admin-operations',
  changeDetection: ChangeDetectionStrategy.OnPush,
  providers: [ControlPlaneStore],
  imports: [
    LoadingStateComponent,
    ErrorBannerComponent,
    MatButtonModule,
    MatIconModule,
    MatProgressSpinnerModule,
    ConnectivityPanelComponent,
    KpiStripComponent,
    PageHeaderComponent,
    PageLayoutComponent,
    LiveWorkComponent,
    OperationsPanelComponent,
    PipelineFlowComponent,
    QueueSparklineComponent,
    ReconcilerPanelComponent,
    TaskTableComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Control Plane"
          subtitle="The control plane live: task queue, workers, reconciliation, and dead-letter remediation">
          <button type="button" secondary-actions mat-stroked-button (click)="toggleLive()">
            <mat-icon>{{ cp.live() ? 'pause' : 'play_arrow' }}</mat-icon>
            {{ cp.live() ? 'Live · ' + pollSeconds + 's' : 'Paused' }}
          </button>
          <button type="button" secondary-actions mat-stroked-button (click)="refresh()">
            <mat-icon>refresh</mat-icon>
            Refresh
          </button>
        </app-page-header>
      </div>

      <div page-content>
        @if (cp.error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (pipelineModel(); as pm) {
          <section class="flow-card">
            <div class="flow-head">
              <div class="flow-titles">
                <span class="card-title">Pipeline</span>
                <span class="card-subtitle">
                  Every create, delete, and repair travels this pipe as a durable task,
                  committed with the resource it acts on, delivered at least once
                </span>
              </div>
              <app-queue-sparkline [samples]="cp.history()" />
            </div>
            <app-pipeline-flow
              [m]="pm"
              [pollSeconds]="pollSeconds"
              (sectionClick)="scrollTo($event)" />
          </section>

          <app-kpi-strip [items]="kpis()" />

          <h2 class="section-title" id="reconcilers">Reconciliation loops</h2>
          <app-reconciler-panel
            [leases]="cp.snapshot()?.leaders ?? []"
            [sweeps]="sweeps()"
            [fleet]="fleet()"
            [serverNow]="pm.serverNow" />

          @if (cp.snapshot()?.connectivity; as conn) {
            <h2 class="section-title" id="connectivity">Connectivity</h2>
            <app-connectivity-panel [conn]="conn" />
          }

          <h2 class="section-title" id="live-work">Working set</h2>
          <div class="work-row">
            <app-live-work
              [inFlight]="cp.inFlight()"
              [waiting]="cp.waiting()"
              [clockOffsetMs]="cp.clockOffsetMs()" />
            <app-operations-panel
              [operations]="cp.openOperations()"
              [serverNow]="pm.serverNow"
              [error]="cp.operationsError()" />
          </div>

        } @else if (cp.loading()) {
          <app-loading-state />
        }

        <h2 class="section-title" id="dlq">Dead-lettered tasks</h2>
        @if (store.error(); as error) {
          <app-error-banner [message]="error" />
        }
        @if (store.loading()) {
          <div class="loading small"><mat-spinner aria-label="Loading" diameter="28" /></div>
        } @else if (store.tasks().length === 0) {
          <p class="dlq-empty">
            <mat-icon>check_circle</mat-icon>
            Nothing here. Every task that failed was retried successfully.
          </p>
        } @else {
          <app-task-table
            [tasks]="store.tasks()"
            (retryTask)="retry($event)"
            (deleteTask)="confirmDelete($event)" />
        }
      </div>
    </app-page-layout>
  `,
  styles: `



    .flow-card {
      padding: 16px 24px 8px;
      margin-bottom: 16px;
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
    }

    .flow-head {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: 16px;
      margin-bottom: 8px;
    }

    .flow-titles {
      display: flex;
      flex-direction: column;
      gap: 2px;
    }

    .card-title {
      font-weight: 500;
    }

    .card-subtitle {
      font-size: 12px;
      color: var(--cco-text-secondary);
    }

    /*
      The operations panel is the third column beside the working set's two.
      Below 1280px the page cannot give three columns a readable width, so it
      drops beneath them full-width; live-work collapses its own pair at 900px.
      Control Plane is a desktop operations surface, and 1440px is the target.
    */
    .work-row {
      display: grid;
      grid-template-columns: 2fr 1fr;
      gap: var(--cco-space-16);
      align-items: start;
    }

    @container page (max-width: 1280px) {
      .work-row {
        grid-template-columns: 1fr;
      }
    }

    .section-title {
      font-size: 14px;
      font-weight: 500;
      margin: 24px 0 12px;
      color: var(--cco-text-secondary);
      scroll-margin-top: 16px;
    }

    .dlq-empty {
      display: flex;
      align-items: center;
      gap: 8px;
      margin: 0;
      font-size: 12px;
      color: var(--cco-text-secondary);

      mat-icon {
        color: var(--cco-success);
        font-size: 18px;
        width: 18px;
        height: 18px;
      }
    }
  `,
})
export class OperationsComponent implements OnInit {
  protected readonly store = inject(TaskAdminStore);
  protected readonly cp = inject(ControlPlaneStore);
  private readonly taskAdminApi = inject(TaskAdminApiService);
  private readonly provisioningStats = inject(ProvisioningStatsService);
  private readonly dialog = inject(MatDialog);

  protected readonly pollSeconds = POLL_INTERVAL_MS / 1000;

  protected readonly pipelineModel = computed<(PipelineModel & {serverNow: Date}) | null>(() => {
    const snap = this.cp.snapshot();
    if (!snap) {
      return null;
    }
    const nowMs = snap.now.getTime();
    // The one lease the periodic plane elects on. A row named anything else is
    // a lease no running loop gates on, so it says nothing about whether the
    // loops are running.
    const plane = snap.leaders.find(l => l.name === BACKGROUND_PLANE_LEASE);
    return {
      pending: snap.queue.pending,
      oldestPendingAgeSeconds: snap.queue.oldestPendingAgeSeconds,
      inFlight: snap.queue.leased,
      workersPerInstance: snap.workersPerInstance,
      knownInstances: this.cp.knownInstances(),
      retrying: this.cp.retryingCount(),
      deadLettered: snap.queue.failed,
      planeLed: Boolean(plane?.holder && plane.expiry && plane.expiry.getTime() > nowMs),
      activity: this.cp.edgeActivity(),
      serverNow: snap.now,
    };
  });

  protected readonly kpis = computed<KpiItem[]>(() => {
    const provisioning = this.provisioningStats.stats();
    return [
      {
        label: 'Device creates (7d)',
        value: provisioning?.devices.created ?? '-',
        icon: 'phone_android',
        breakdown: provisioning ? outcomeBreakdown(provisioning.devices) : undefined,
        colorClass: isDegraded(provisioning?.devices) ? 'error' : undefined,
      },
      {
        label: 'Host creates (7d)',
        value: provisioning?.hosts.created ?? '-',
        icon: 'dns',
        breakdown: provisioning ? outcomeBreakdown(provisioning.hosts) : undefined,
        colorClass: isDegraded(provisioning?.hosts) ? 'error' : undefined,
      },
      {
        label: 'Instances seen',
        value: this.cp.knownInstances() || '-',
        icon: 'grid_view',
        breakdown: 'via queue + leader leases',
      },
    ];
  });

  /** Before the first snapshot lands, every loop reads as detect-only. */
  protected readonly sweeps = computed<SweepPolicy>(
    () =>
      this.cp.snapshot()?.sweeps ?? {
        deleteOrphanVms: false,
        orphanCvds: false,
        zombieRecreate: false,
        reaperDryRun: false,
        isSet: false,
      }
  );

  /** Before the first snapshot lands, the autoscaler reads as off and empty. */
  protected readonly fleet = computed<FleetPanel>(
    () =>
      this.cp.snapshot()?.fleet ?? {
        status: {
          mode: 'off',
          target: 0,
          standby: 0,
          draining: 0,
          misses: 0,
          demand: {
            servable: 0,
            unservable: 0,
            gpuRequired: 0,
            needsLargerHost: 0,
            oversizedGroup: 0,
          },
          schedule: 0,
          floor: 0,
          total: 0,
          signal: 'standby',
          limitedBy: '',
          partial: false,
        },
        policy: {
          mode: 'off',
          tiers: {},
          floor: 0,
          demandHorizonSec: 0,
          drainGraceSec: 0,
          shapeLadder: [],
          isSet: false,
        },
        deviceSlots: 0,
        gpuDegradation: {degraded: 0, wanted: 0},
      }
  );

  ngOnInit(): void {
    this.cp.startPolling();
    this.store.loadAll();
    // The stats httpResource loads on first injection; reload when revisiting.
    if (this.provisioningStats.stats()) {
      this.provisioningStats.refresh();
    }
  }

  protected toggleLive(): void {
    this.cp.toggleLive();
    if (this.cp.live()) {
      this.cp.refresh();
    }
  }

  protected refresh(): void {
    this.cp.refresh();
    this.store.loadAll();
    this.taskAdminApi.refreshStats();
    this.provisioningStats.refresh();
  }

  protected scrollTo(section: PipelineSection): void {
    const id = section === 'queue' || section === 'workers' ? 'live-work' : section;
    document.getElementById(id)?.scrollIntoView({behavior: 'smooth', block: 'start'});
  }

  // Retry is an idempotent requeue, so there is no confirmation, matching house
  // policy of confirming only destructive actions.
  protected retry(task: FailedTask): void {
    this.store.retry(task);
    this.cp.refresh();
  }

  protected confirmDelete(task: FailedTask): void {
    const data: ConfirmDialogData = {
      title: 'Delete task',
      message:
        `Permanently remove the ${task.type} task for resource ` +
        `${task.resourceId.slice(0, 8)}? The task will never run again and the ` +
        'underlying resource will not be repaired.',
      confirmText: 'Delete',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    };
    this.dialog
      .open(ConfirmDialogComponent, {data})
      .afterClosed()
      .subscribe((confirmed: boolean) => {
        if (confirmed) {
          this.store.delete(task);
          this.cp.refresh();
        }
      });
  }
}

/** Sub-line under the create count: how many of the window's creates hurt now. */
function outcomeBreakdown(outcome: ProvisioningOutcome): string {
  if (outcome.created === 0) {
    return 'none this week';
  }
  return outcome.errored === 0 ? 'none in error' : `${outcome.errored} now in error`;
}

/** Degraded = anything errored recently and success dipped below 90%. */
function isDegraded(outcome: ProvisioningOutcome | undefined): boolean {
  if (!outcome) {
    return false;
  }
  const rate = successRate(outcome);
  return rate !== null && rate < 90;
}
