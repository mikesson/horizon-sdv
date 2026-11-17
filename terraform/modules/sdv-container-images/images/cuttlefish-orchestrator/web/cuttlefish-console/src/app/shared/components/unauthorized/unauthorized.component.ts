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

import {ChangeDetectionStrategy, Component} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {RouterLink} from '@angular/router';

/**
 * Component displayed when a user tries to access a route they don't have permission for.
 */
@Component({
  selector: 'app-unauthorized',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterLink, MatCardModule, MatButtonModule, MatIconModule],
  template: `
    <div class="unauthorized-container">
      <mat-card class="unauthorized-card">
        <mat-card-content>
          <div class="icon-container">
            <mat-icon class="error-icon">lock</mat-icon>
          </div>
          <h1>Access Denied</h1>
          <p>You don't have permission to access this page.</p>
          <p class="hint">
            If you believe this is an error, please contact your administrator.
          </p>
          <div class="actions">
            <a mat-flat-button routerLink="/">
              <mat-icon>home</mat-icon>
              Go to Dashboard
            </a>
          </div>
        </mat-card-content>
      </mat-card>
    </div>
  `,
  styles: `
      .unauthorized-container {
        min-height: 80vh;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 24px;
      }

      .unauthorized-card {
        max-width: 480px;
        text-align: center;
        padding: 32px;
      }

      .icon-container {
        margin-bottom: 24px;
      }

      .error-icon {
        font-size: 64px;
        width: 64px;
        height: 64px;
        color: var(--cco-error);
      }

      h1 {
        margin: 0 0 16px;
        font-size: 24px;
        color: var(--cco-text);
      }

      p {
        margin: 0 0 8px;
        color: var(--cco-text-secondary);
      }

      .hint {
        font-size: 14px;
        color: var(--cco-text-secondary);
        margin-bottom: 24px;
      }

      .actions {
        display: flex;
        justify-content: center;
        gap: 16px;
      }

      .actions mat-icon {
        margin-right: 8px;
      }
    `,
})
export class UnauthorizedComponent {}
