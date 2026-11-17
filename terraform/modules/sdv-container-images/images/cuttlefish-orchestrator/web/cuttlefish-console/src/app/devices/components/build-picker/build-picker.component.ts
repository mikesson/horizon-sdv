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
import type {Build} from '../../../builds/models/build.model';
import {artifactRoleLabels, isTrackingBuild} from '../../../builds/models/build.model';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';

/** Selectable-card list of registered builds, with loading and empty states. */
@Component({
  selector: 'app-build-picker',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatCardModule, MatIconModule, EmptyStateComponent, LoadingStateComponent],
  template: `
    @if (loading()) {
      <app-loading-state size="inline" label="Loading builds…" />
    } @else if (builds().length === 0) {
      <app-empty-state
        variant="compact"
        icon="warning"
        message="No builds registered. Register a build first."
      />
    } @else {
      <div class="build-list" role="listbox" aria-label="Build image selection">
        @for (build of builds(); track build.id) {
          <mat-card
            class="build-card"
            [class.selected]="selectedId() === build.id"
            role="option"
            [attr.aria-selected]="selectedId() === build.id"
            [tabindex]="0"
            (click)="onSelect(build)"
            (keydown.enter)="onSelect(build)"
            (keydown.space)="onSelect(build); $event.preventDefault()">
            <div class="build-content">
              <mat-icon class="build-icon">{{ build.source === 'url' ? 'cloud' : 'android' }}</mat-icon>
              <div class="build-info">
                <span class="build-name">{{ build.name }}</span>
                <span class="build-meta">
                  <span class="build-type-badge" [class.official]="build.source === 'android_build'">
                    {{ build.source === 'android_build' ? 'AOSP' : 'Custom' }}
                  </span>
                  @if (isTracking(build)) {
                    <span class="tracking-badge">
                      <mat-icon>autorenew</mat-icon>
                      Tracks latest
                    </span>
                  }
                  @if (build.branch) {
                    <span>{{ build.branch }}</span>
                  }
                  @if (build.target) {
                    {{ build.target }}
                  }
                </span>
                @if (roleLabels(build); as roles) {
                  @if (roles.length > 0) {
                    <span class="role-chips">
                      @for (role of roles; track role) {
                        <span class="role-chip">{{ role }}</span>
                      }
                    </span>
                  }
                }
              </div>
              @if (selectedId() === build.id) {
                <mat-icon class="check-icon">check_circle</mat-icon>
              }
            </div>
          </mat-card>
        }
      </div>
    }
  `,
  styles: `
    .build-list {
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    .build-card {
      padding: 12px;
      cursor: pointer;
      border: 2px solid var(--cco-border);
      border-radius: 8px;
    }

    .build-card:hover { border-color: var(--cco-accent); }

    .build-card.selected {
      border-color: var(--cco-accent);
      background: var(--cco-accent-surface);
    }

    .role-chips {
      display: flex;
      flex-wrap: wrap;
      gap: 4px;
      margin-top: 4px;
    }

    .role-chip {
      font-size: 11px;
      padding: 1px 8px;
      border-radius: 4px;
      background: var(--cco-surface-variant);
      color: var(--cco-text-secondary);
    }

    .build-content {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .build-icon {
      color: var(--cco-text-secondary);
      font-size: 20px;
    }

    .build-info {
      flex: 1;
      display: flex;
      flex-direction: column;
    }

    .build-name { font-weight: 500; font-size: 12px; }
    .build-meta { font-size: 11px; color: var(--cco-text-secondary); display: flex; align-items: center; gap: 6px; }

    .build-type-badge {
      font-size: 11px;
      font-weight: 600;
      padding: 1px 8px;
      border-radius: 4px;
      background: var(--cco-surface-variant);
      text-transform: uppercase;
    }

    .tracking-badge {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      font-weight: 600;
      padding: 1px 8px;
      border: 1px solid var(--cco-accent);
      border-radius: 4px;
      color: var(--cco-accent);
      white-space: nowrap;
    }

    .tracking-badge mat-icon {
      font-size: 14px;
      width: 12px;
      height: 12px;
    }

    .build-type-badge.official {
      background: var(--cco-success-surface);
      color: var(--cco-success);
    }

    .check-icon { color: var(--cco-accent); }

  `,
})
export class BuildPickerComponent {
  readonly builds = input.required<Build[]>();
  readonly loading = input(false);
  readonly selectedId = input('');
  readonly selectionChange = output<Build>();

  protected readonly isTracking = isTrackingBuild;
  protected readonly roleLabels = artifactRoleLabels;

  protected onSelect(build: Build): void {
    this.selectionChange.emit(build);
  }
}
