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

import type {VsockMembership} from '../../models/vsock-membership';

/**
 * A group's vsock reachability, drawn as the default rather than the exception.
 *
 * Untagged VMs mesh, so a group that configured nothing has full connectivity.
 * Rendering that as blank space would read as isolation, which is its exact
 * opposite. The band is therefore loudest in the case where nothing was
 * configured at all.
 */
@Component({
  selector: 'app-vsock-band',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (membership().layer === 'off') {
      <!-- Outlined, because with no daemon there is no medium here, and the
           filled band means presence. -->
      <div class="band absent">
        <span class="band-label">{{ membership().label }}</span>
        <span class="band-caveat">{{ membership().caveat }}</span>
      </div>
    } @else if (membership().kind !== 'none') {
      <div class="band" [class.mixed]="membership().kind === 'mixed'">
        <span class="band-label">{{ membership().label }}</span>

        @if (membership().kind === 'mixed') {
          <ul class="members">
            @for (row of rows(); track row.name) {
              <li [class.excluded]="row.excluded">
                <span class="member-name">{{ row.name }}</span>
                <span class="member-tags">{{ row.membership }}</span>
              </li>
            }
          </ul>
        }

        @if (membership().caveat; as caveat) {
          <span class="band-caveat">{{ caveat }}</span>
        }
      </div>
    }
  `,
  styles: `
    :host {
      display: block;
    }

    /* Filled, because the mesh is presence and an outline reads as an empty
       container. */
    .band {
      border-radius: var(--cco-radius-sm);
      background: var(--cco-surface-variant);
      padding: var(--cco-space-8);
    }

    /* No fill, because there is no medium to depict. The dashed outline is the
       same vocabulary the ruler uses for a slot that is not occupied. */
    .band.absent {
      border: 1px dashed var(--cco-border);
      background: none;
    }

    .band-label {
      display: block;
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    /* The caveat is what stops the label above it from being read as settled,
       so it is never smaller than the label it qualifies. */
    .band-caveat {
      display: block;
      margin-block-start: var(--cco-space-4);
      max-width: 65ch;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .members {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-8);
      margin: var(--cco-space-8) 0 0;
      padding: 0;
      list-style: none;
    }

    .members li {
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-4);
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-sm);
    }

    .member-name {
      color: var(--cco-text);
    }

    .member-tags {
      color: var(--cco-text-secondary);
    }

    /* An untagged VM beside a tagged one cannot reach it. That is a fact about
       this instance, not something to infer from where a bracket stops. */
    .members li.excluded .member-tags {
      color: var(--cco-warning);
    }
  `,
})
export class VsockBandComponent {
  readonly membership = input.required<VsockMembership>();

  protected readonly rows = computed(() => {
    const membership = this.membership();
    return [...membership.badges.entries()].map(([name, text]) => ({
      name,
      membership: text,
      excluded: membership.untagged.includes(name),
    }));
  });
}
