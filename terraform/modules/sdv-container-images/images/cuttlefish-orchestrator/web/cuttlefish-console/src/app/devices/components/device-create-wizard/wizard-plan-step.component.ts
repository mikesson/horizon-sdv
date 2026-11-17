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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import type {Build} from '../../../builds/models/build.model';
import type {Host} from '../../../hosts/models/host.model';
import type {PlanItem} from '../../../shared/components/plan-preview/plan-preview.component';
import {PlanPreviewComponent} from '../../../shared/components/plan-preview/plan-preview.component';
import {StepHeaderComponent} from '../../../shared/components/step-header/step-header.component';
import type {Resolution} from '../../../shared/models/requirements.model';
import type {CreateIntent} from '../device-create-page/create-intent.model';

/**
 * Step 4 (Plan): the server-resolved dry run. What the platform will do
 * (which build, place or provision) plus any degradation warnings, rendered
 * before anything is committed. The plan is advisory; launch re-resolves
 * authoritatively inside the placement transaction.
 */
@Component({
  selector: 'app-wizard-plan-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatIconModule, PlanPreviewComponent, StepHeaderComponent],
  template: `
    <app-step-header icon="route" title="Plan" description="What the platform will do when you launch" />

    <app-plan-preview
      [items]="planItems()"
      [warnings]="resolution()?.warnings ?? []"
      [loading]="planning()"
      [error]="planError()"
    >
      @if (customizable()) {
        <button type="button" mat-button class="customize-link" (click)="customize.emit()">
          <mat-icon>tune</mat-icon>
          Customize in Advanced
        </button>
      }
    </app-plan-preview>
  `,
  styles: `
    .customize-link {
      margin-top: var(--cco-space-16);
      color: var(--cco-accent);
    }

    .customize-link mat-icon {
      font-size: var(--cco-icon-md);
      width: var(--cco-icon-md);
      height: var(--cco-icon-md);
      margin-right: var(--cco-space-4);
    }
  `,
})
export class WizardPlanStepComponent {
  readonly intent = input.required<CreateIntent>();
  readonly planning = input.required<boolean>();
  readonly resolution = input.required<Resolution | null>();
  readonly planError = input.required<string | null>();
  readonly selectedBuild = input.required<Build | null>();
  readonly selectedHost = input.required<Host | null>();
  /** Off where the deployment has Advanced mode disabled. */
  readonly customizable = input(false);

  readonly customize = output<void>();

  /** The plan is launchable when it resolved without error. */
  readonly launchable = computed(() => !this.planning() && this.planError() === null);

  protected readonly planItems = computed<PlanItem[]>(() => [
    this.deviceItem(),
    this.buildItem(),
    this.hostItem(),
  ]);

  private deviceItem(): PlanItem {
    const intent = this.intent();
    const usage =
      intent.usage === 'interactive'
        ? 'Interactive (browser view & control)'
        : 'Headless (ADB / automation)';
    const duration = intent.pinned ? 'pinned' : `${intent.ttlHours}h session`;
    // The GPU constraint and the architecture are the answers that can get the
    // create rejected outright, so the plan says them back before launch. Both
    // ride the requirements, which only an automatically placed create sends:
    // on a chosen host neither reaches the server, so neither is claimed here.
    const parts = [usage];
    if (intent.host.kind === 'auto') {
      if (intent.gpuRequired) {
        parts.push('GPU required');
      }
      if (intent.architecture === 'arm') {
        parts.push('ARM');
      }
    }
    parts.push(duration);
    return {
      icon: 'smartphone',
      label: 'Device',
      value: intent.name,
      detail: parts.join(' · '),
    };
  }

  private buildItem(): PlanItem {
    const res = this.resolution();
    const intent = this.intent();
    if (res) {
      return {
        icon: 'android',
        label: 'Build',
        value: res.buildName ?? this.selectedBuild()?.name ?? 'Registered build',
        detail:
          res.buildAction === 'register'
            ? 'Will be registered in Builds (tracks the latest build on its branch)'
            : 'Registered build',
        action: res.buildAction === 'register',
      };
    }
    if (intent.build.kind === 'track') {
      return {
        icon: 'android',
        label: 'Build',
        value: intent.trackBranch,
        detail: `Latest on ${intent.trackTarget}, tracked`,
      };
    }
    return {
      icon: 'android',
      label: 'Build',
      value: this.selectedBuild()?.name ?? 'Registered build',
      detail: 'Registered build',
    };
  }

  private hostItem(): PlanItem {
    const res = this.resolution();
    if (res) {
      // machineType and zone are absent until placement picks them; a template
      // literal would render the string "undefined" where the old interpolation
      // rendered nothing.
      const shape = [res.machineType, res.zone].every(Boolean)
        ? `Will provision ${res.machineType} in ${res.zone} (~1 min)`
        : 'Will provision a new host (~1 min)';
      // The pool the server's own derivation answered, which decides both the
      // hosts this device may be placed on and the shape a new one takes.
      const pool = res.pool ? ` · ${res.pool} pool` : '';
      return res.hostAction === 'provision'
        ? {icon: 'dns', label: 'Host', value: 'New host', detail: `${shape}${pool}`, action: true}
        : {
            icon: 'dns',
            label: 'Host',
            value: res.hostName || res.hostId || 'Selected host',
            detail: `Placed on existing capacity${pool}`,
          };
    }
    const host = this.selectedHost();
    return {
      icon: 'dns',
      label: 'Host',
      value: host?.name ?? 'Selected host',
      detail: host
        ? `${host.currentDevices} of ${host.maxDevices} device slots in use` +
          ', capacity and quota verified'
        : 'Capacity and quota verified',
    };
  }
}
