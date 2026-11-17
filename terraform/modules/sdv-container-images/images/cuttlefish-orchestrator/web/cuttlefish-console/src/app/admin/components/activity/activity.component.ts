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
import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {FormField, form} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatChipsModule} from '@angular/material/chips';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSelectModule} from '@angular/material/select';
import type {ParamMap, Params} from '@angular/router';
import {ActivatedRoute, Router} from '@angular/router';
import {
  ActionBarComponent,
  PageHeaderComponent,
  PageLayoutComponent,
} from '../../../shared/components';
import {EmptyStateComponent} from '../../../shared/components/empty-state/empty-state.component';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {AUDIT_ACTIONS, AUDIT_KINDS} from '../../models/audit.model';
import {ActivityStore} from '../../store/activity.signal-store';
import {ActivityTableComponent} from './activity-table.component';

type TimeRange = '' | '1h' | '24h' | '7d' | 'custom';

interface ActivityFilterDraft {
  actorEmail: string;
  action: string;
  /** Metadata kind ('' = every category; 'system_config' = settings changes). */
  kind: string;
  resourceType: string;
  /** Exact resource id, which gives the whole history of one host, device or batch. */
  resourceId: string;
  /** Preset lookback; '' = all time, 'custom' = the from/to pair. */
  timeRange: TimeRange;
  /** `datetime-local` values in the viewer's zone; read only when custom. */
  from: string;
  to: string;
}

const TIME_RANGES: ReadonlyArray<{value: TimeRange; label: string}> = [
  {value: '', label: 'All time'},
  {value: '1h', label: 'Last hour'},
  {value: '24h', label: 'Last 24 hours'},
  {value: '7d', label: 'Last 7 days'},
  {value: 'custom', label: 'Custom range'},
];

const TIME_RANGE_MILLIS: Record<'1h' | '24h' | '7d', number> = {
  '1h': 3_600_000,
  '24h': 86_400_000,
  '7d': 604_800_000,
};

const EMPTY_DRAFT: ActivityFilterDraft = {
  actorEmail: '',
  action: '',
  kind: '',
  resourceType: '',
  resourceId: '',
  timeRange: '',
  from: '',
  to: '',
};

function isTimeRange(value: string): value is TimeRange {
  return TIME_RANGES.some(range => range.value === value);
}

function draftFromParams(params: ParamMap): ActivityFilterDraft {
  const range = params.get('range') ?? '';
  return {
    actorEmail: params.get('actor') ?? '',
    action: params.get('action') ?? '',
    kind: params.get('kind') ?? '',
    resourceType: params.get('type') ?? '',
    resourceId: params.get('resource') ?? '',
    timeRange: isTimeRange(range) ? range : '',
    from: params.get('from') ?? '',
    to: params.get('to') ?? '',
  };
}

/** Only the fields in play; a cleared filter leaves a bare URL. */
function paramsFromDraft(draft: ActivityFilterDraft): Params {
  const custom = draft.timeRange === 'custom';
  const params: Params = {
    actor: draft.actorEmail || null,
    action: draft.action || null,
    kind: draft.kind || null,
    type: draft.resourceType || null,
    resource: draft.resourceId.trim() || null,
    range: draft.timeRange || null,
    from: custom ? draft.from || null : null,
    to: custom ? draft.to || null : null,
  };
  for (const key of Object.keys(params)) {
    if (params[key] === null) {
      delete params[key];
    }
  }
  return params;
}

/** A `datetime-local` value is zoneless; the Date constructor reads it as local. */
function parseLocal(value: string): Date | undefined {
  if (!value) {
    return undefined;
  }
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed;
}

function timeWindow(draft: ActivityFilterDraft): {startTime?: Date; endTime?: Date} {
  if (draft.timeRange === 'custom') {
    return {startTime: parseLocal(draft.from), endTime: parseLocal(draft.to)};
  }
  return {
    startTime: draft.timeRange
      ? new Date(Date.now() - TIME_RANGE_MILLIS[draft.timeRange])
      : undefined,
  };
}

/**
 * Audit-log viewer. Filters are server-side and applied explicitly; the list
 * appends pages via "Load more" (the log grows downward, page numbers don't
 * fit an append-only view).
 */
@Component({
  selector: 'app-admin-activity',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    FormField,
    MatButtonModule,
    MatChipsModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatSelectModule,
    ActionBarComponent,
    ActivityTableComponent,
    EmptyStateComponent,
    ErrorBannerComponent,
    LoadingStateComponent,
    PageHeaderComponent,
    PageLayoutComponent,
  ],
  template: `
    <app-page-layout variant="list">
      <div page-header>
        <app-page-header
          title="Activity"
          subtitle="Who did what: the audit trail for every destructive and admin action">
          <button type="button" secondary-actions mat-stroked-button (click)="apply()">
            <mat-icon>refresh</mat-icon>
            Refresh
          </button>
        </app-page-header>
      </div>

      <div page-content>
        <app-action-bar>
          <div filters>
            <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
              <mat-label>Actor</mat-label>
              <input matInput [formField]="filterForm.actorEmail" placeholder="user@example.com" />
            </mat-form-field>

            <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
              <mat-label>Category</mat-label>
              <mat-select [formField]="filterForm.kind">
                @for (kind of kinds; track kind.value) {
                  <mat-option [value]="kind.value">{{ kind.label }}</mat-option>
                }
              </mat-select>
            </mat-form-field>

            <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
              <mat-label>Action</mat-label>
              <mat-select [formField]="filterForm.action">
                <mat-option value="">All actions</mat-option>
                @for (action of actions; track action) {
                  <mat-option [value]="action">{{ action }}</mat-option>
                }
              </mat-select>
            </mat-form-field>

            <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
              <mat-label>Resource type</mat-label>
              <mat-select [formField]="filterForm.resourceType">
                <mat-option value="">All types</mat-option>
                @for (type of resourceTypes; track type) {
                  <mat-option [value]="type">{{ type }}</mat-option>
                }
              </mat-select>
            </mat-form-field>

            <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
              <mat-label>Resource ID</mat-label>
              <input matInput [formField]="filterForm.resourceId" placeholder="host-a1b2c3" />
            </mat-form-field>

            <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
              <mat-label>Time range</mat-label>
              <mat-select [formField]="filterForm.timeRange">
                @for (range of timeRanges; track range.value) {
                  <mat-option [value]="range.value">{{ range.label }}</mat-option>
                }
              </mat-select>
            </mat-form-field>

            @if (customRange()) {
              <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
                <mat-label>From</mat-label>
                <input matInput type="datetime-local" [formField]="filterForm.from" />
              </mat-form-field>

              <mat-form-field appearance="outline" class="filter-field" subscriptSizing="dynamic">
                <mat-label>To</mat-label>
                <input matInput type="datetime-local" [formField]="filterForm.to" />
              </mat-form-field>
            }
          </div>

          <div actions>
            @if (rangeInverted()) {
              <span class="range-hint">End is before start</span>
            }
            <button type="button" mat-flat-button [disabled]="rangeInverted()" (click)="apply()">
              Apply
            </button>
            <button type="button" mat-button (click)="clear()">Clear</button>
          </div>
        </app-action-bar>

        @if (store.error(); as error) {
          <app-error-banner [message]="error" />
        }

        @if (store.loading() && store.entries().length === 0) {
          <app-loading-state />
        } @else if (store.entries().length === 0) {
          <app-empty-state
            icon="history"
            title="No activity"
            message="No audit entries match the current filter."
          />
        } @else {
          <app-activity-table [entries]="store.entries()" />

          <div class="load-more">
            @if (store.hasMore()) {
              <button type="button" mat-stroked-button [disabled]="store.loading()" (click)="store.loadMore()">
                @if (store.loading()) {
                  Loading…
                } @else {
                  Load more ({{ store.entries().length }} of {{ store.total() }})
                }
              </button>
            } @else {
              <span class="all-loaded">All {{ store.total() }} entries loaded</span>
            }
          </div>
        }
      </div>
    </app-page-layout>
  `,
  styles: `
    .filter-field {
      width: 200px;
    }

    .load-more {
      display: flex;
      justify-content: center;
      padding: var(--cco-space-16);
    }

    .all-loaded {
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .range-hint {
      align-self: center;
      font-size: var(--cco-text-sm);
      color: var(--cco-error);
    }
  `,
})
export class ActivityComponent implements OnInit {
  protected readonly store = inject(ActivityStore);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);

  protected readonly actions = AUDIT_ACTIONS;
  protected readonly kinds = AUDIT_KINDS;
  protected readonly resourceTypes = ['host', 'device', 'quota', 'system', 'task'];

  protected readonly timeRanges = TIME_RANGES;

  private readonly filterDraft = signal<ActivityFilterDraft>(
    draftFromParams(this.route.snapshot.queryParamMap)
  );
  protected readonly filterForm = form(this.filterDraft);

  protected readonly customRange = computed(() => this.filterDraft().timeRange === 'custom');
  protected readonly rangeInverted = computed(() => {
    const {startTime, endTime} = timeWindow(this.filterDraft());
    return startTime !== undefined && endTime !== undefined && startTime > endTime;
  });

  ngOnInit(): void {
    this.apply();
  }

  protected apply(): void {
    const draft = this.filterDraft();
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: paramsFromDraft(draft),
      replaceUrl: true,
    });
    this.store.load({
      actorEmail: draft.actorEmail || undefined,
      action: draft.action || undefined,
      kind: draft.kind || undefined,
      resourceType: draft.resourceType || undefined,
      resourceId: draft.resourceId.trim() || undefined,
      ...timeWindow(draft),
    });
  }

  protected clear(): void {
    this.filterDraft.set(EMPTY_DRAFT);
    this.apply();
  }
}
