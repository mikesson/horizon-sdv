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

import {ChangeDetectionStrategy, Component, inject, input} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';
import {FactRowComponent} from '../../../shared/components/fact-row/fact-row.component';
import {LoggingService} from '../../../shared/services';
import type {Host} from '../../models/host.model';

/**
 * The host's configuration and how to reach it. What it is *doing* (status,
 * zone, capacity, timestamps) is the supporting pane's job, next door in
 * app-host-summary.
 */
@Component({
  selector: 'app-host-info-cards',
  imports: [MatButtonModule, MatCardModule, MatIconModule, MatTooltipModule, FactRowComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let h = host();
    <mat-card class="details-card">
      <mat-card-header>
        <mat-card-title>Host Information</mat-card-title>
      </mat-card-header>
      <mat-card-content>
        <app-fact-row label="Machine Type">{{ h.machineType }}</app-fact-row>
        <app-fact-row label="Boot Disk">
          {{ h.bootDiskSizeGb ? h.bootDiskSizeGb + ' GB' : '400 GB (default)' }}
        </app-fact-row>
        @if (h.minCpuPlatform) {
          <app-fact-row label="Min CPU Platform">{{ h.minCpuPlatform }}</app-fact-row>
        }
        @if (h.bootImage) {
          <app-fact-row label="Boot Image">{{ h.bootImage }}</app-fact-row>
        }
        <app-fact-row label="External IP">{{ h.externalIP || 'Not assigned' }}</app-fact-row>
        <app-fact-row label="Internal IP">{{ h.internalIP || 'Not assigned' }}</app-fact-row>
      </mat-card-content>
    </mat-card>

    @if (h.status === 'running') {
      <mat-card class="connection-card">
        <mat-card-header>
          <mat-card-title>Connection</mat-card-title>
        </mat-card-header>
        <mat-card-content>
          <div class="connection-info">
            @if (h.externalIP) {
              <div class="connection-item">
                <span class="label">Host Orchestrator (Public)</span>
                <a [href]="'https://' + h.externalIP + ':2080'" target="_blank" class="external-link">
                  https://{{ h.externalIP }}:2080
                  <mat-icon inline>open_in_new</mat-icon>
                </a>
              </div>
            } @else {
              <div class="connection-item">
                <span class="label">Host Orchestrator (Private Network)</span>
                <div class="private-ip-container">
                  <code>https://{{ h.internalIP }}:2080</code>
                  <span class="note">(Requires VPN or IAP Tunnel)</span>
                </div>
              </div>
            }

            <!-- gcloud addresses the VM, so these need the instance name; the
                 display name would not resolve. It is absent until the host is
                 provisioned. -->
            @if (h.gceInstanceName; as instance) {
              <div class="connection-item">
                <span class="label">Access via IAP Tunnel</span>
                <div class="command-block">
                  <code>gcloud compute start-iap-tunnel {{ instance }} 2080 --zone={{ h.zone }} --local-host-port=localhost:2080</code>
                  <button type="button" mat-icon-button (click)="copyToClipboard('gcloud compute start-iap-tunnel ' + instance + ' 2080 --zone=' + h.zone + ' --local-host-port=localhost:2080')" matTooltip="Copy command" aria-label="Copy command">
                    <mat-icon>content_copy</mat-icon>
                  </button>
                </div>
                <p class="help-text">Then open <a href="https://localhost:2080" target="_blank">https://localhost:2080</a></p>
              </div>

              <div class="connection-item">
                <span class="label">SSH Access</span>
                <div class="command-block">
                  <code>gcloud compute ssh {{ instance }} --zone={{ h.zone }}</code>
                  <button type="button" mat-icon-button (click)="copyToClipboard('gcloud compute ssh ' + instance + ' --zone=' + h.zone)" matTooltip="Copy command" aria-label="Copy command">
                    <mat-icon>content_copy</mat-icon>
                  </button>
                </div>
              </div>
            }
          </div>
        </mat-card-content>
      </mat-card>
    }
  `,
  styles: `
      .details-card,
      .connection-card {
        margin-bottom: 16px;
      }

      .connection-info {
        display: flex;
        flex-direction: column;
        gap: 12px;
      }

      .connection-item {
        display: flex;
        flex-direction: column;
        gap: 4px;
      }

      .connection-item .label {
        font-size: 12px;
        color: var(--cco-text-secondary);
        text-transform: uppercase;
      }

      .connection-item code {
        background: var(--cco-surface-variant);
        padding: 8px 12px;
        border-radius: 4px;
        font-family: monospace;
        font-size: 12px;
        word-break: break-all;
      }

      .command-block {
        display: flex;
        align-items: center;
        gap: 8px;
        background: var(--cco-surface-variant);
        padding: 4px 4px 4px 12px;
        border-radius: 4px;
      }

      .command-block code {
        background: transparent;
        padding: 0;
        flex: 1;
      }

      .private-ip-container {
        display: flex;
        align-items: center;
        gap: 8px;
      }

      .help-text {
        margin: 4px 0 0;
        font-size: 12px;
        color: var(--cco-text-secondary);
      }

      .external-link {
        display: inline-flex;
        align-items: center;
        gap: 4px;
      }

      .note {
        font-size: 12px;
        color: var(--cco-text-secondary);
        font-style: italic;
      }

      a:hover {
        text-decoration: underline;
      }
  `,
})
export class HostInfoCardsComponent {
  private readonly logger = inject(LoggingService);

  readonly host = input.required<Host>();

  copyToClipboard(text: string): void {
    navigator.clipboard.writeText(text).catch((err: unknown) => {
      this.logger.error('HostDetail', 'Could not copy text', err);
    });
  }
}
