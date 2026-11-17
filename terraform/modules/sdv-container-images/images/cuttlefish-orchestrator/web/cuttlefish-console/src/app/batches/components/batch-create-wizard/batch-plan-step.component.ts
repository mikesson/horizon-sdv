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

import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import type {PlanItem} from '../../../shared/components/plan-preview/plan-preview.component';
import {PlanPreviewComponent} from '../../../shared/components/plan-preview/plan-preview.component';
import {StepHeaderComponent} from '../../../shared/components/step-header/step-header.component';
import type {BatchAdmission} from '../../models/batch.model';
import type {AdmissionPolicy, BatchIntent} from './batch-intent.model';
import {effectiveMinReady, memberNameRange} from './batch-intent.model';

const POLICY_LABEL: Record<AdmissionPolicy, string> = {
  atomic: 'Every device, or none',
  all: 'Every device to start',
  partial: 'At least some',
};

const POLICY_ICON: Record<AdmissionPolicy, string> = {
  atomic: 'all_inclusive',
  all: 'done_all',
  partial: 'done',
};

/**
 * Step 4 (Plan): the server-resolved dry run, showing how many devices fit, on
 * how many hosts, running which build. A plan that cannot reach the admission
 * floor comes back as an error rather than a smaller plan, so the error state
 * here is "this batch would be rejected", not "something went wrong".
 */
@Component({
  selector: 'app-batch-plan-step',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [PlanPreviewComponent, StepHeaderComponent],
  template: `
    <app-step-header
      icon="route"
      title="Plan"
      description="What the platform will do when you launch" />

    <app-plan-preview
      [items]="planItems()"
      [warnings]="warnings()"
      [loading]="planning()"
      [error]="planError()"
      errorTitle="This batch would be rejected"
      loadingLabel="Planning the batch…">
      @if (placementReasons().length > 0) {
        <details class="ranking">
          <summary>Why this zone</summary>
          <ul>
            @for (reason of placementReasons(); track reason) {
              <li>{{ reason }}</li>
            }
          </ul>
        </details>
      }
    </app-plan-preview>
  `,
  styles: `
    .ranking {
      margin-top: var(--cco-space-16);
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .ranking summary {
      cursor: pointer;
      color: var(--cco-accent);
    }

    .ranking ul {
      margin: var(--cco-space-8) 0 0;
      padding-left: var(--cco-space-24);
      line-height: 1.6;
    }
  `,
})
export class BatchPlanStepComponent {
  readonly intent = input.required<BatchIntent>();
  readonly planning = input.required<boolean>();
  readonly admission = input.required<BatchAdmission | null>();
  readonly planError = input.required<string | null>();

  protected readonly placementReasons = computed(
    () => this.admission()?.resolution?.placementReasons ?? []
  );

  /**
   * The resolution's warnings reach the console twice, since the handler
   * returns the same slice as `warnings` and inside `resolution`, so only the
   * top level is read. The short-fleet note is composed here from two server
   * numbers; nothing on this step is counted client-side.
   */
  protected readonly warnings = computed<string[]>(() => {
    const admission = this.admission();
    if (!admission) {
      return [];
    }
    const short =
      admission.admitted < admission.requested
        ? [
            `Host quota fits only ${admission.admitted} of the ${admission.requested} devices ` +
              'requested. The batch will start at that size and will not grow later.',
          ]
        : [];
    return [...short, ...admission.warnings];
  });

  protected readonly planItems = computed<PlanItem[]>(() => [
    this.fleetItem(),
    this.buildItem(),
    this.hostsItem(),
    this.policyItem(),
  ]);

  private fleetItem(): PlanItem {
    const intent = this.intent();
    const admission = this.admission();
    const size = admission?.admitted ?? intent.count;
    const short = admission !== null && admission.admitted < admission.requested;
    const usage = intent.usage === 'interactive' ? 'Interactive' : 'Headless';
    const architecture = intent.architecture === 'arm' ? ' · ARM' : '';
    return {
      icon: 'dataset',
      label: 'Fleet',
      value: short ? `${size} of ${admission.requested} devices` : `${size} devices`,
      detail:
        `${memberNameRange(intent.namePrefix, size)} · ${usage}${architecture} · ` +
        `${intent.ttlHours}h session`,
    };
  }

  private buildItem(): PlanItem {
    const res = this.admission()?.resolution;
    return {
      icon: 'android',
      label: 'Build',
      value: res?.buildName ?? 'Platform default',
      detail:
        res?.buildAction === 'register'
          ? 'Will be registered in Builds (tracks the latest build on its branch)'
          : 'Registered build. Every member runs this exact one',
      action: res?.buildAction === 'register',
    };
  }

  private hostsItem(): PlanItem {
    const admission = this.admission();
    const res = admission?.resolution;
    const newHosts = admission?.newHosts ?? 0;
    // machineType and zone are absent until placement picks them.
    const shape = [res?.machineType, res?.zone].every(Boolean)
      ? `${res?.machineType} in ${res?.zone}`
      : 'shape picked at launch';
    const economics = res?.capacityModel ? ` · ${res.capacityModel}` : '';
    // Echoed in both branches, because the constraint decided which hosts were
    // eligible, and on the pack branch nothing else on this step reveals it.
    const gpu = this.intent().gpuRequired ? ' · GPU required' : '';
    // The pool the server's own derivation answered. The batch's hosts join it
    // and its members pack onto its machines.
    const pool = res?.pool ? ` · ${res.pool} pool` : '';
    if (newHosts === 0) {
      return {
        icon: 'dns',
        label: 'Hosts',
        value: 'Existing capacity',
        detail: `Every member packs onto hosts that are already running${pool}${gpu}`,
      };
    }
    return {
      icon: 'dns',
      label: 'Hosts',
      value: newHosts === 1 ? '1 new host' : `${newHosts} new hosts`,
      detail: `Will provision ${shape}${economics}${pool}${gpu}`,
      action: true,
    };
  }

  private policyItem(): PlanItem {
    const intent = this.intent();
    const placement =
      intent.placement === 'isolated' ? 'dedicated hosts' : 'packed onto existing capacity';
    return {
      icon: POLICY_ICON[intent.admission],
      label: 'Policy',
      value: POLICY_LABEL[intent.admission],
      detail: `${placement} · ${this.toleranceDetail()}`,
    };
  }

  private toleranceDetail(): string {
    const intent = this.intent();
    if (intent.admission === 'atomic') {
      return intent.readyTimeoutSeconds > 0
        ? `every member running within ${intent.readyTimeoutSeconds}s or the batch tears down`
        : 'the batch tears down if any member fails';
    }
    if (intent.admission === 'partial') {
      return `at least ${effectiveMinReady(intent)} must be admitted`;
    }
    return 'all must be admitted; later failures do not tear the batch down';
  }
}
