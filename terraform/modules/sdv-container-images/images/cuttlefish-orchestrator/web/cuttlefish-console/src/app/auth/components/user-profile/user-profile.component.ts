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

import {ChangeDetectionStrategy, Component, inject} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatDividerModule} from '@angular/material/divider';
import {MatIconModule} from '@angular/material/icon';
import {MatListModule} from '@angular/material/list';
import {RouterModule} from '@angular/router';

import {AuthStore} from '../../store/auth.signal-store';

@Component({
  selector: 'app-user-profile',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    RouterModule,
    MatCardModule,
    MatButtonModule,
    MatIconModule,
    MatListModule,
    MatDividerModule,
  ],
  template: `
    @if (user(); as user) {
      <div class="profile-container">
        <mat-card class="profile-card">
          <mat-card-header>
            <div mat-card-avatar class="profile-avatar">
              <mat-icon>account_circle</mat-icon>
            </div>
            <mat-card-title>{{ user.displayName || user.email }}</mat-card-title>
            <mat-card-subtitle>{{ user.email }}</mat-card-subtitle>
          </mat-card-header>
          <mat-card-content>
            <mat-list>
              <mat-list-item>
                <mat-icon matListItemIcon>badge</mat-icon>
                <span matListItemTitle>Email</span>
                <span matListItemLine>{{ user.email }}</span>
              </mat-list-item>
            </mat-list>
          </mat-card-content>
          <mat-card-actions>
            <button type="button" mat-button color="warn" (click)="logout()">
              <mat-icon>logout</mat-icon>
              Sign Out
            </button>
          </mat-card-actions>
        </mat-card>
        <!-- Quick Links -->
        <mat-card class="links-card">
          <mat-card-header>
            <mat-card-title>Quick Links</mat-card-title>
          </mat-card-header>
          <mat-card-content>
            <mat-list>
              <a mat-list-item routerLink="/admin/host-images">
                <mat-icon matListItemIcon>perm_media</mat-icon>
                <span matListItemTitle>Host Images</span>
                <span matListItemLine>Manage host image configuration</span>
              </a>
            </mat-list>
          </mat-card-content>
        </mat-card>
      </div>
    }
    `,
  styles: `
      .profile-container {
        padding: 24px;
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 24px;
        max-width: 1200px;
        margin: 0 auto;
      }

      @container page (max-width: 960px) {
        .profile-container {
          grid-template-columns: 1fr;
        }
      }

      .profile-card {
        grid-column: 1 / -1;
      }

      .profile-avatar {
        background: var(--cco-accent-surface);
        display: flex;
        align-items: center;
        justify-content: center;
      }

      .profile-avatar mat-icon {
        font-size: 48px;
        width: 40px;
        height: 40px;
        color: var(--cco-accent);
      }

      .links-card a {
        text-decoration: none;
        color: inherit;
      }
    `,
})
export class UserProfileComponent {
  private readonly authStore = inject(AuthStore);

  readonly user = this.authStore.user;

  logout(): void {
    this.authStore.logout();
  }
}
