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

import {ChangeDetectionStrategy, Component, computed, inject, input} from '@angular/core';
import {MatIconModule} from '@angular/material/icon';
import {Router, RouterLink} from '@angular/router';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {SDV_PAIR_PRESET} from '../device-create-page/create-mode';

interface ModeCard {
  id: string;
  path: 'simple' | 'advanced';
  preset?: string;
  /**
   * Whether a handoff build becomes the device build. An SDV pair takes a
   * build per instance, and a device build would fill both instances' gaps.
   */
  takesBuild: boolean;
  icon: string;
  title: string;
  summary: string;
  facts: readonly string[];
}

const CARDS: readonly ModeCard[] = [
  {
    id: 'simple',
    path: 'simple',
    takesBuild: true,
    icon: 'checklist',
    title: 'Simple',
    summary: 'Answer a few questions and CCO picks the host, build and settings.',
    facts: [
      'One Android device',
      'Pick a host or build only if you want to',
      'Shows the plan before launch',
    ],
  },
  {
    id: 'advanced',
    path: 'advanced',
    takesBuild: true,
    icon: 'tune',
    title: 'Advanced',
    summary: 'Full CVD configuration, as a form or as the JSON config.',
    facts: [
      'Several instances in one group',
      'A build per instance, displays and GPU mode',
      'Bootconfig arguments and the raw JSON',
    ],
  },
  {
    id: 'sdv',
    path: 'advanced',
    preset: SDV_PAIR_PRESET,
    takesBuild: false,
    icon: 'directions_car',
    title: 'AAOS SDV Core + IVI',
    summary: 'An Android Automotive SDV pair, set up for you in Advanced mode.',
    facts: [
      'Two instances on a host of their own',
      'You pick the Core and IVI builds',
      'Opens Advanced with the SDV preset applied',
    ],
  },
];

/**
 * The first step of device creation: which editor to open. A handoff link's
 * host travels on into the chosen mode, and its build into the modes that
 * take a device build.
 */
@Component({
  selector: 'app-device-create-chooser',
  imports: [MatIconModule, RouterLink, PageLayoutComponent, PageHeaderComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Create Device"
          subtitle="Choose how you want to set it up"
          [showBackButton]="true"
          (back)="goBack()" />
      </div>

      <div page-content>
        <ul class="mode-cards" aria-label="Ways to create a device">
          @for (card of cards(); track card.id) {
            <li>
              <a
                class="mode-card"
                [routerLink]="['/devices/create', card.path]"
                [queryParams]="card.queryParams"
                [attr.aria-labelledby]="'create-mode-' + card.id + '-title'"
                [attr.aria-describedby]="'create-mode-' + card.id + '-summary'">
                <mat-icon class="mode-card__icon">{{ card.icon }}</mat-icon>
                <h2 class="mode-card__title" [id]="'create-mode-' + card.id + '-title'">
                  {{ card.title }}
                </h2>
                <p class="mode-card__summary" [id]="'create-mode-' + card.id + '-summary'">
                  {{ card.summary }}
                </p>
                <ul class="mode-card__facts">
                  @for (fact of card.facts; track fact) {
                    <li>{{ fact }}</li>
                  }
                </ul>
                <span class="mode-card__go" aria-hidden="true">
                  Start
                  <mat-icon>arrow_forward</mat-icon>
                </span>
              </a>
            </li>
          }
        </ul>
      </div>
    </app-page-layout>
  `,
  styles: `
    .mode-cards {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: var(--cco-space-16);
      max-width: 1100px;
      margin: 0;
      padding: 0;
      list-style: none;
    }

    .mode-cards > li {
      display: flex;
    }

    .mode-card {
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-8);
      flex: 1;
      padding: var(--cco-space-24);
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: var(--cco-radius-lg);
      color: var(--cco-text);
      text-decoration: none;
      transition:
        background-color var(--cco-duration-base) ease,
        border-color var(--cco-duration-base) ease;
    }

    .mode-card:hover {
      background: var(--cco-surface-hover);
      border-color: var(--cco-accent);
    }

    .mode-card__icon {
      font-size: var(--cco-icon-display);
      width: var(--cco-icon-display);
      height: var(--cco-icon-display);
      color: var(--cco-accent);
    }

    .mode-card__title {
      margin: var(--cco-space-8) 0 0;
      font-size: var(--cco-text-title);
      font-weight: 500;
    }

    .mode-card__summary {
      margin: 0;
      font-size: var(--cco-text-body);
      color: var(--cco-text);
    }

    .mode-card__facts {
      margin: 0;
      padding-left: var(--cco-space-16);
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .mode-card__facts li + li {
      margin-top: var(--cco-space-4);
    }

    .mode-card__go {
      display: inline-flex;
      align-items: center;
      gap: var(--cco-space-4);
      margin-top: auto;
      padding-top: var(--cco-space-8);
      font-size: var(--cco-text-body);
      font-weight: 500;
      color: var(--cco-accent);
    }

    .mode-card__go mat-icon {
      font-size: var(--cco-icon-md);
      width: var(--cco-icon-md);
      height: var(--cco-icon-md);
    }

    @container page (max-width: 899px) {
      .mode-cards {
        grid-template-columns: minmax(0, 1fr);
      }
    }
  `,
})
export class DeviceCreateChooserComponent {
  private readonly router = inject(Router);

  /** Route query params (withComponentInputBinding): handoffs from other pages. */
  readonly hostId = input<string>();
  readonly buildId = input<string>();

  protected readonly cards = computed(() => {
    const hostId = this.hostId() || undefined;
    const buildId = this.buildId() || undefined;
    return CARDS.map(card => ({
      ...card,
      queryParams: {hostId, buildId: card.takesBuild ? buildId : undefined, preset: card.preset},
    }));
  });

  protected goBack(): void {
    void this.router.navigate(['/devices']);
  }
}
