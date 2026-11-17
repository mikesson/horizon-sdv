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
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';

/**
 * The failed state. It carries a page- or section-level error, never a field
 * validation message. Emitting `retry` renders the retry affordance; leaving
 * it unbound renders text only, which is what every current call site does.
 */
@Component({
  selector: 'app-error-banner',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatIconModule],
  template: `
    <div class="error-banner" role="alert">
      <mat-icon>error_outline</mat-icon>
      <span class="message">{{ message() }}</span>
      @if (retryable()) {
        <button type="button" mat-button (click)="retry.emit()">Retry</button>
      }
    </div>
  `,
  styles: `
    .error-banner {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      background: var(--cco-error-surface);
      color: var(--cco-error);
      padding: var(--cco-space-16);
      border-radius: var(--cco-radius-xs);
      margin-bottom: var(--cco-space-16);
      font-size: var(--cco-text-sm);
    }

    .error-banner mat-icon {
      font-size: var(--cco-icon-lg);
      width: var(--cco-icon-lg);
      height: var(--cco-icon-lg);
      flex-shrink: 0;
    }

    .message {
      flex: 1;
      min-width: 0;
    }
  `,
})
export class ErrorBannerComponent {
  readonly message = input.required<string>();
  readonly retryable = input(false);

  readonly retry = output<void>();
}
