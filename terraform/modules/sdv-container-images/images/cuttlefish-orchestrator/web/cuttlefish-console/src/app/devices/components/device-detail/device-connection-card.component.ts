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
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatDividerModule} from '@angular/material/divider';
import {MatIconModule} from '@angular/material/icon';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterModule} from '@angular/router';
import {resolveBasePath} from '../../../shared/utils';
import type {Device} from '../../models/device.model';

/**
 * Connection instructions for a running device: ADB (with IAP-tunnel steps
 * for private hosts), the WebRTC player, and Host Orchestrator access.
 */
@Component({
  selector: 'app-device-connection-card',
  imports: [
    RouterModule,
    MatButtonModule,
    MatCardModule,
    MatDividerModule,
    MatIconModule,
    MatTooltipModule,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let d = device();
<mat-card class="connection-card">
              <mat-card-header>
                <mat-card-title>Connection</mat-card-title>
              </mat-card-header>
              <mat-card-content>
                <div class="connection-info">
                  <!-- ADB Connection -->
                  @if (adbPort(); as port) {
                    <div class="connection-group">
                      <div class="group-header">
                        <mat-icon>terminal</mat-icon>
                        <span>ADB Access</span>
                      </div>

                      @if (!directlyReachable()) {
                        <!-- The host firewall opens the FIRST instance's ADB port
                             and no other, so printing a dialable command for a
                             sibling would be a command that always times out. -->
                        <div class="tunnel-instruction">
                          <p class="instruction-text">
                            Instance <code>{{ instance() }}</code> listens on port {{ port }},
                            which the host firewall does not open. Reach it through the player's
                            ADB, which bridges every instance over the host orchestrator's
                            WebSocket.
                          </p>
                        </div>
                      } @else if (isPrivateIP(getHostIP(d))) {
                        <!-- Private IP / Prod Mode -->
                        <div class="tunnel-instruction">
                          <p class="instruction-text">
                            This device is on a private network. Start an IAP tunnel first:
                          </p>
                          <div class="command-block">
                            <code>gcloud compute start-iap-tunnel {{ d.hostName }} {{ port }} --zone={{ d.hostZone }} --local-host-port=localhost:{{ port }}</code>
                            <button type="button" mat-icon-button (click)="copyToClipboard('gcloud compute start-iap-tunnel ' + d.hostName + ' ' + port + ' --zone=' + d.hostZone + ' --local-host-port=localhost:' + port)" matTooltip="Copy tunnel command" aria-label="Copy tunnel command">
                              <mat-icon>content_copy</mat-icon>
                            </button>
                          </div>
                        </div>

                        <div class="connection-item">
                          <span class="label">Connect</span>
                          <code>adb connect localhost:{{ port }}</code>
                          <button type="button" mat-icon-button (click)="copyToClipboard('adb connect localhost:' + port)" matTooltip="Copy connect command" aria-label="Copy connect command">
                            <mat-icon>content_copy</mat-icon>
                          </button>
                        </div>
                      } @else {
                        <!-- Public IP / Dev Mode -->
                        <div class="connection-item">
                          <span class="label">Connect</span>
                          <code>adb connect {{ getHostIP(d) }}:{{ port }}</code>
                          <button type="button" mat-icon-button (click)="copyToClipboard('adb connect ' + getHostIP(d) + ':' + port)" matTooltip="Copy command" aria-label="Copy command">
                            <mat-icon>content_copy</mat-icon>
                          </button>
                        </div>
                      }
                    </div>
                  }

                  <mat-divider />

                  <!-- WebRTC Connection -->
                  <div class="connection-group">
                    <div class="group-header">
                      <mat-icon>desktop_windows</mat-icon>
                      <span>WebRTC Stream</span>
                    </div>

                    <div class="connection-item">
                      <span class="label">Open</span>
                      <a
                        [routerLink]="['/devices', d.id, 'player']"
                        [queryParams]="{instance: instance()}"
                        class="external-link player-link"
                      >
                        Open player
                        <mat-icon inline>play_circle</mat-icon>
                      </a>
                      <button type="button" mat-icon-button (click)="openPlayerNewTab(d)" matTooltip="Open in new tab" aria-label="Open in new tab">
                        <mat-icon>open_in_new</mat-icon>
                      </button>
                    </div>

                    <p class="connection-note">
                      The integrated player uses TURN relay for secure streaming to private hosts.
                    </p>
                  </div>

                  <mat-divider />

                  <!-- Host Orchestrator Access -->
                  <div class="connection-group">
                    <div class="group-header">
                      <mat-icon>settings</mat-icon>
                      <span>Host Orchestrator</span>
                    </div>

                    @if (d.hostExternalIp && !isPrivateIP(d.hostExternalIp)) {
                      <div class="connection-item">
                        <span class="label">Open</span>
                        <a [href]="'http://' + d.hostExternalIp + ':2080/'" target="_blank" class="external-link">
                          Host Orchestrator UI
                          <mat-icon inline>open_in_new</mat-icon>
                        </a>
                      </div>
                    } @else {
                      <!-- No CCO route proxies the HO UI; a private host is reached through an IAP tunnel. -->
                      <div class="tunnel-instruction">
                        <p class="instruction-text">
                          This host is on a private network. Start an IAP tunnel, then open
                          <code>http://localhost:2080/</code>:
                        </p>
                        <div class="command-block">
                          <code>gcloud compute start-iap-tunnel {{ d.hostName }} 2080 --zone={{ d.hostZone }} --local-host-port=localhost:2080</code>
                          <button type="button" mat-icon-button (click)="copyToClipboard('gcloud compute start-iap-tunnel ' + d.hostName + ' 2080 --zone=' + d.hostZone + ' --local-host-port=localhost:2080')" matTooltip="Copy tunnel command" aria-label="Copy tunnel command">
                            <mat-icon>content_copy</mat-icon>
                          </button>
                        </div>
                      </div>
                    }

                    <p class="connection-note">
                      Access the Host Orchestrator web UI to manage CVDs directly on the host.
                    </p>
                  </div>
                </div>
              </mat-card-content>
            </mat-card>
  `,
  styles: `


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
        align-items: center;
        gap: 8px;
      }

      .connection-item .label {
        font-size: 12px;
        color: var(--cco-text-secondary);
        text-transform: uppercase;
        min-width: 60px;
      }

      .connection-item code {
        background: var(--cco-surface-variant);
        padding: 8px 12px;
        border-radius: 4px;
        font-family: monospace;
        font-size: 12px;
        flex: 1;
        word-break: break-all;
      }

      .connection-group {
        display: flex;
        flex-direction: column;
        gap: 12px;
        padding: 8px 0;
      }

      .group-header {
        display: flex;
        align-items: center;
        gap: 8px;
        color: var(--cco-text-secondary);
        font-weight: 500;
        font-size: 14px;
      }

      .tunnel-instruction {
        background: var(--cco-surface-variant);
        padding: 12px;
        border-radius: 4px;
        border-left: 4px solid var(--cco-accent);
      }

      .instruction-text {
        margin: 0 0 8px;
        font-size: 12px;
        color: var(--cco-text);
      }

      .command-block {
        display: flex;
        align-items: center;
        gap: 8px;
        background: var(--cco-surface);
        padding: 4px 4px 4px 12px;
        border-radius: 4px;
        border: 1px solid var(--cco-border);
      }

      .command-block code {
        background: transparent;
        padding: 0;
        flex: 1;
        font-size: 12px;
      }

      .external-link {
        display: inline-flex;
        align-items: center;
        gap: 4px;
      }

      .player-link {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        padding: 8px 16px;
        background: var(--cco-accent);
        color: white;
        border-radius: 4px;
        text-decoration: none;
        font-weight: 500;
      }

      .player-link:hover {
        background: var(--cco-accent);
        text-decoration: none;
      }

      .connection-note {
        margin: 8px 0 0;
        font-size: 12px;
        color: var(--cco-text-secondary);
        font-style: italic;
      }
  `,
})
export class DeviceConnectionCardComponent {
  private readonly snackBar = inject(MatSnackBar);

  readonly device = input.required<Device>();

  /** The Android VM these instructions are for. */
  readonly instance = input.required<string>();

  private readonly selected = computed(() =>
    this.device().instances?.find(i => i.name === this.instance())
  );

  /**
   * The selected instance's own ADB port, falling back to the device's flat
   * field for a row the host has not answered for yet.
   */
  protected readonly adbPort = computed(() => this.selected()?.adbPort ?? this.device().adbPort);

  /**
   * Whether the port above can actually be dialled. The host firewall opens
   * 6520 (the first instance's) and nothing above it, and widening it would
   * add seven unauthenticated ports per host to duplicate a reach the
   * WebSocket bridge already provides.
   */
  protected readonly directlyReachable = computed(() => {
    const instances = this.device().instances ?? [];
    return instances.length === 0 || instances[0]?.name === this.instance();
  });

  getHostIP(device: Device): string {
    return device.hostIp ?? 'Loading...';
  }

  isPrivateIP(ip: string): boolean {
    return ip.startsWith('10.') || ip.startsWith('192.168.') || ip.startsWith('172.');
  }

  copyToClipboard(text: string): void {
    void navigator.clipboard.writeText(text);
    this.snackBar.open('Copied to clipboard', 'Dismiss', {duration: 2000});
  }

  openPlayerNewTab(device: Device): void {
    const instance = encodeURIComponent(this.instance());
    window.open(resolveBasePath(`/devices/${device.id}/player?instance=${instance}`), '_blank');
  }
}
