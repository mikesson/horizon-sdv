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

import {ChangeDetectionStrategy, Component, computed, input} from '@angular/core';
import {MatIconModule} from '@angular/material/icon';
import type {Connectivity} from '../../models/control-plane.model';

/** What each ingress mode means for a stream, stated rather than inferred. */
const INGRESS_CONSEQUENCE: Record<Connectivity['ingressMode'], string> = {
  public: 'Host VMs have external IPs; media flows host-to-client directly, TURN optional.',
  private: 'Host VMs are VPC-internal; clients reach them only over internal routes.',
  relay: 'Host VMs are internal-only; all device media relays through TURN.',
};

/**
 * The media plane, read-only. Terraform owns every value here (they must
 * agree with resources it creates), so this card states facts an operator
 * debugging a stream failure otherwise cannot see, and it offers no controls.
 * Never renders a credential: server names, realm and TTL only.
 */
@Component({
  selector: 'app-connectivity-panel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatIconModule],
  template: `
    @if (conn(); as c) {
      <div class="card">
        @for (problem of c.problems; track problem) {
          <div class="problem">
            <mat-icon>error</mat-icon>
            <span>{{ problem }}</span>
          </div>
        }

        <div class="fact">
          <span class="label">Ingress</span>
          <div>
            <span class="value mode">{{ c.ingressMode }}</span>
            <p class="consequence">{{ consequence() }}</p>
          </div>
        </div>

        <div class="fact">
          <span class="label">Client transport</span>
          <div>
            <span class="value">{{ c.transportPolicy }}</span>
            <p class="consequence">
              @if (c.transportPolicy === 'relay') {
                Browsers and CLIs are pinned to relayed candidates; direct pairs are never gathered.
              } @else {
                Direct, server-reflexive and relayed candidates all compete.
              }
            </p>
          </div>
        </div>

        <div class="fact">
          <span class="label">STUN</span>
          <div>
            @for (server of c.stunServers; track server) {
              <span class="value uri">{{ server }}</span>
            } @empty {
              <span class="value none">none</span>
            }
          </div>
        </div>

        <!-- Two lists, one set of machines: the entire subtlety of relay mode
             is that the same relays are named at two addresses. -->
        <div class="fact turn">
          <span class="label">TURN</span>
          <div class="turn-legs">
            <div>
              <span class="leg-title">Client leg (external)</span>
              @for (server of c.turnClientServers; track server) {
                <span class="value uri">{{ server }}</span>
              } @empty {
                <span class="value none">none</span>
              }
            </div>
            <div>
              <span class="leg-title">Device leg (internal)</span>
              @for (server of c.turnDeviceServers; track server) {
                <span class="value uri">{{ server }}</span>
              } @empty {
                <span class="value none">none</span>
              }
            </div>
          </div>
        </div>

        @if (c.turnRealm || c.credentialsMintable) {
          <div class="fact">
            <span class="label">Credentials</span>
            <div>
              <span class="value">
                @if (c.credentialsMintable) {
                  minted per device{{ c.turnRealm ? ', realm ' + c.turnRealm : '' }},
                  {{ ttlHours() }} h lifetime
                } @else {
                  not mintable, no shared secret deployed
                }
              </span>
            </div>
          </div>
        }
      </div>
    }
  `,
  styles: `
    .card {
      padding: var(--cco-space-16);
      background: var(--cco-surface);
      border: 1px solid var(--cco-border);
      border-radius: 8px;
      max-width: 720px;
    }

    .problem {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      margin-bottom: var(--cco-space-12);
      color: var(--cco-error);
      font-size: var(--cco-text-body);
    }

    .problem mat-icon {
      flex-shrink: 0;
    }

    .fact {
      display: flex;
      gap: var(--cco-space-16);
      padding: var(--cco-space-8) 0;
    }

    .fact + .fact {
      border-top: 1px solid var(--cco-border);
    }

    .label {
      width: 120px;
      flex-shrink: 0;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
      padding-top: 2px;
    }

    .value {
      display: block;
      color: var(--cco-text);
      font-size: var(--cco-text-body);
    }

    .value.mode {
      text-transform: uppercase;
      letter-spacing: 0.04em;
      font-weight: 500;
    }

    .value.uri {
      font-family: 'Roboto Mono', monospace;
      font-size: var(--cco-text-label);
    }

    .value.none {
      color: var(--cco-text-secondary);
    }

    .consequence {
      margin: 2px 0 0;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
    }

    .turn-legs {
      display: flex;
      gap: var(--cco-space-24);
      flex-wrap: wrap;
    }

    .leg-title {
      display: block;
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-label);
      margin-bottom: 2px;
    }
  `,
})
export class ConnectivityPanelComponent {
  readonly conn = input.required<Connectivity>();

  protected readonly consequence = computed(() => INGRESS_CONSEQUENCE[this.conn().ingressMode]);

  protected readonly ttlHours = computed(() => Math.round(this.conn().credentialTtlSeconds / 3600));
}
