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

import {ChangeDetectionStrategy, Component, input} from '@angular/core';
import {MatIconModule} from '@angular/material/icon';
import {RouterModule} from '@angular/router';

export interface BreadcrumbItem {
  label: string;
  link?: string;
  icon?: string;
}

@Component({
  selector: 'app-breadcrumb',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterModule, MatIconModule],
  template: `
    <nav class="gcp-breadcrumb" aria-label="Breadcrumb">
      @for (item of items(); track item.label; let i = $index; let last = $last) {
        @if (!last) {
          <a [routerLink]="item.link" class="breadcrumb-link">
            @if (item.icon && i === 0) {
              <mat-icon class="breadcrumb-icon">{{ item.icon }}</mat-icon>
            }
            <span>{{ item.label }}</span>
          </a>
          <mat-icon class="separator">chevron_right</mat-icon>
        } @else {
          <span class="breadcrumb-current">
            @if (item.icon && items().length === 1) {
              <mat-icon class="breadcrumb-icon">{{ item.icon }}</mat-icon>
            }
            {{ item.label }}
          </span>
        }
      }
    </nav>
  `,
  styles: `
    .gcp-breadcrumb {
      display: flex;
      align-items: center;
      gap: 4px;
      font-size: 12px;
      color: var(--cco-text-secondary);
      padding: 8px 0;
    }

    .breadcrumb-link {
      display: flex;
      align-items: center;
      gap: 4px;
      color: var(--cco-accent);
      text-decoration: none;
      padding: 4px 8px;
      border-radius: 4px;
      transition: background-color var(--cco-duration-base) ease;
    }

    .breadcrumb-link:hover {
      background-color: var(--cco-accent-surface);
    }

    .breadcrumb-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
    }

    .separator {
      font-size: 16px;
      width: 18px;
      height: 18px;
      color: var(--cco-text-secondary);
    }

    .breadcrumb-current {
      display: flex;
      align-items: center;
      gap: 4px;
      color: var(--cco-text);
      font-weight: 500;
      padding: 4px 8px;
    }
  `,
})
export class BreadcrumbComponent {
  readonly items = input<BreadcrumbItem[]>([]);
}
