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

import {ChangeDetectionStrategy, Component, input, output} from '@angular/core';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import type {TemplateName} from '../../models/cvd-config.schema';

interface TemplateOption {
  id: TemplateName;
  name: string;
  icon: string;
  description: string;
  specs: string;
}

const TEMPLATE_OPTIONS: TemplateOption[] = [
  {
    id: 'phone',
    name: 'Phone',
    icon: 'smartphone',
    description: 'Standard smartphone form factor',
    specs: '720x1280, 320dpi, 4GB RAM',
  },
  {
    id: 'tablet',
    name: 'Tablet',
    icon: 'tablet',
    description: 'Large screen tablet',
    specs: '2560x1800, 320dpi, 4GB RAM',
  },
  {
    id: 'tv',
    name: 'Android TV',
    icon: 'tv',
    description: 'Television / large display',
    specs: '1920x1080, 213dpi, 2GB RAM',
  },
  {
    id: 'wearable',
    name: 'Wear OS',
    icon: 'watch',
    description: 'Smartwatch form factor',
    specs: '450x450, 320dpi, 1.5GB RAM',
  },
  {
    id: 'auto',
    name: 'Android Auto',
    icon: 'directions_car',
    description: 'Automotive head unit',
    specs: 'Dual display, 120dpi, 4GB RAM',
  },
  {
    id: 'foldable',
    name: 'Foldable',
    icon: 'flip',
    description: 'Foldable phone with dual displays',
    specs: '1768x2208 + 832x2268, 4GB RAM',
  },
  {
    id: 'slim',
    name: 'Slim',
    icon: 'memory',
    description: 'Minimal resources for testing',
    specs: '720x1280, 320dpi, 2GB RAM',
  },
  {
    id: 'go',
    name: 'Android Go',
    icon: 'phone_android',
    description: 'Low-resource Android Go edition',
    specs: '720x1280, 320dpi, 2GB RAM',
  },
];

/** Grid of the 8 built-in CVD device templates as selectable cards. */
@Component({
  selector: 'app-template-picker',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatCardModule, MatIconModule],
  template: `
    <div class="template-grid" role="listbox" aria-label="Device template selection">
      @for (template of options; track template.id) {
        <mat-card
          class="template-card"
          [class.selected]="selected() === template.id"
          role="option"
          [attr.aria-selected]="selected() === template.id"
          [attr.aria-label]="template.name + ' - ' + template.description"
          tabindex="0"
          (click)="selectionChange.emit(template.id)"
          (keydown.enter)="selectionChange.emit(template.id)"
          (keydown.space)="selectionChange.emit(template.id); $event.preventDefault()">
          <div class="template-content">
            <mat-icon class="template-icon" aria-hidden="true">{{ template.icon }}</mat-icon>
            <div class="template-info">
              <span class="template-name">{{ template.name }}</span>
            </div>
            @if (selected() === template.id) {
              <mat-icon class="check-icon" aria-hidden="true">check_circle</mat-icon>
            }
          </div>
        </mat-card>
      }
    </div>
  `,
  styles: `
    .template-grid {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: 8px;
    }

    .template-card {
      padding: 8px;
      cursor: pointer;
      border: 2px solid var(--cco-border);
      border-radius: 8px;
      transition: all var(--cco-duration-slow);
    }

    .template-card:hover {
      border-color: var(--cco-accent);
    }

    .template-card.selected {
      border-color: var(--cco-accent);
      background: var(--cco-accent-surface);
    }

    .template-content {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 4px;
    }

    .template-icon {
      color: var(--cco-accent);
      font-size: 20px;
      width: 20px;
      height: 20px;
    }

    .template-name {
      font-size: 11px;
      font-weight: 500;
    }

    .check-icon {
      color: var(--cco-accent);
      font-size: 16px;
      width: 16px;
      height: 16px;
    }
  `,
})
export class TemplatePickerComponent {
  readonly selected = input.required<TemplateName>();
  readonly selectionChange = output<TemplateName>();

  protected readonly options = TEMPLATE_OPTIONS;
}
