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

/**
 * Instance Card Component
 *
 * Displays a CVD instance as a compact card with summary info and actions.
 * Used in the multi-instance list for the Advanced configuration mode.
 */

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTooltipModule} from '@angular/material/tooltip';

import type {TemplateName} from '../../models/cvd-config.schema';

/**
 * Template icon mapping
 */
const TEMPLATE_ICONS: Record<TemplateName, string> = {
  phone: 'smartphone',
  tablet: 'tablet',
  tv: 'tv',
  wearable: 'watch',
  auto: 'directions_car',
  foldable: 'flip',
  slim: 'memory',
  go: 'phone_android',
};

/**
 * Template display names
 */
const TEMPLATE_NAMES: Record<TemplateName, string> = {
  phone: 'Phone',
  tablet: 'Tablet',
  tv: 'Android TV',
  wearable: 'Wear OS',
  auto: 'Android Auto',
  foldable: 'Foldable',
  slim: 'Slim',
  go: 'Android Go',
};

/**
 * Instance data for display
 */
export interface InstanceCardData {
  id: string;
  name: string;
  template: TemplateName;
  cpus: number;
  memoryMb: number;
  /** Which build the instance boots: its own, or the device build. */
  build: string;
}

@Component({
  selector: 'app-instance-card',
  imports: [MatCardModule, MatIconModule, MatButtonModule, MatTooltipModule, MatMenuModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <mat-card
      class="instance-card"
      [class.selected]="selected()"
      (click)="cardSelect.emit()">
      <div class="card-content">
        <div [class]="['template-icon', instance().template]">
          <mat-icon>{{ templateIcon() }}</mat-icon>
        </div>
        <div class="instance-info">
          <span class="instance-name">{{ instance().name }}</span>
          <span class="instance-type">{{ templateName() }}</span>
        </div>
        <div class="instance-specs">
          <span>{{ instance().cpus }} vCPU</span>
          <span>{{ memoryLabel() }}</span>
        </div>
        <span class="instance-build" [matTooltip]="instance().build">{{ instance().build }}</span>
      </div>
      <div class="card-actions">
        <!-- The guard belongs on the trigger, not on a wrapper div: opening the
             menu must not also select the card. stopPropagation does not stop
             same-element listeners, so matMenuTriggerFor still opens. -->
        <button type="button"
          mat-icon-button
          [matMenuTriggerFor]="actionsMenu"
          matTooltip="Actions"
          class="action-button"
          (click)="$event.stopPropagation()" aria-label="Actions">
          <mat-icon>more_vert</mat-icon>
        </button>
        <mat-menu #actionsMenu="matMenu">
          <button type="button" mat-menu-item (click)="cardDuplicate.emit()">
            <mat-icon>content_copy</mat-icon>
            <span>Duplicate</span>
          </button>
          <button type="button"
            mat-menu-item
            (click)="cardDelete.emit()"
            [disabled]="!canDelete()">
            <mat-icon>delete</mat-icon>
            <span>Delete</span>
          </button>
        </mat-menu>
      </div>
    </mat-card>
  `,
  styles: `
    :host {
      display: block;
    }

    .instance-card {
      display: flex;
      flex-direction: column;
      padding: 12px;
      min-width: 140px;
      max-width: 160px;
      cursor: pointer;
      border: 2px solid var(--cco-border);
      border-radius: 12px;
      transition: all var(--cco-duration-slow);
      position: relative;
    }

    .instance-card:hover {
      border-color: var(--cco-accent);
      box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
    }

    .instance-card.selected {
      border-color: var(--cco-accent);
      background: var(--cco-accent-surface);
    }

    .card-content {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 8px;
    }

    .template-icon {
      width: 48px;
      height: 48px;
      border-radius: 50%;
      display: flex;
      align-items: center;
      justify-content: center;
      background: var(--cco-surface-variant);
    }

    .template-icon mat-icon {
      font-size: 24px;
      width: 24px;
      height: 24px;
      color: var(--cco-accent);
    }

    .template-icon.phone { background: var(--cco-accent-surface); }
    .template-icon.tablet { background: var(--cco-error-surface); }
    .template-icon.tv { background: var(--cco-success-surface); }
    .template-icon.wearable { background: var(--cco-warning-surface); }
    .template-icon.auto { background: var(--cco-surface-variant); }
    .template-icon.foldable { background: var(--cco-accent-surface); }
    .template-icon.slim { background: var(--cco-surface-variant); }
    .template-icon.go { background: var(--cco-success-surface); }

    .instance-info {
      display: flex;
      flex-direction: column;
      align-items: center;
      text-align: center;
    }

    .instance-name {
      font-weight: 500;
      font-size: 12px;
      color: var(--cco-text);
      max-width: 120px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .instance-type {
      font-size: 11px;
      color: var(--cco-text-secondary);
    }

    .instance-specs {
      display: flex;
      gap: 8px;
      font-size: 11px;
      color: var(--cco-text-secondary);
      padding: 4px 8px;
      background: var(--cco-surface-variant);
      border-radius: 4px;
    }

    .instance-build {
      max-width: 136px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-size: 11px;
      color: var(--cco-text-secondary);
    }

    .card-actions {
      position: absolute;
      top: 4px;
      right: 4px;
    }

    .action-button {
      width: 28px;
      height: 28px;
      opacity: 0;
      transition: opacity var(--cco-duration-slow);
    }

    .action-button mat-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .instance-card:hover .action-button {
      opacity: 1;
    }
  `,
})
export class InstanceCardComponent {
  /**
   * Instance data to display
   */
  readonly instance = input.required<InstanceCardData>();

  /**
   * Whether this card is selected
   */
  readonly selected = input<boolean>(false);

  /**
   * Whether delete is allowed (false if last instance)
   */
  readonly canDelete = input<boolean>(true);

  /**
   * Emitted when card is clicked (select)
   */
  readonly cardSelect = output<void>();

  /**
   * Emitted when duplicate action is clicked
   */
  readonly cardDuplicate = output<void>();

  /**
   * Emitted when delete action is clicked
   */
  readonly cardDelete = output<void>();

  /**
   * Template icon for display
   */
  readonly templateIcon = computed(() => TEMPLATE_ICONS[this.instance().template]);

  /**
   * Template display name
   */
  readonly templateName = computed(() => TEMPLATE_NAMES[this.instance().template]);

  /** Megabytes where gigabytes would need a fraction (the SDV Core runs 2096 MB). */
  protected readonly memoryLabel = computed(() => {
    const mb = this.instance().memoryMb;
    return mb % 1024 === 0 ? `${mb / 1024}GB` : `${mb}MB`;
  });
}
