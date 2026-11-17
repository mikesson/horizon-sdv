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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  effect,
  inject,
  linkedSignal,
  signal,
} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {MatButtonModule} from '@angular/material/button';
import {MatButtonToggleModule} from '@angular/material/button-toggle';
import {MatCardModule} from '@angular/material/card';
import {MatChipsModule} from '@angular/material/chips';
import {MatIconModule} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTooltipModule} from '@angular/material/tooltip';
import {ActivatedRoute, Router, RouterModule} from '@angular/router';
import {StuckTaskBannerComponent} from '../../../admin/components/stuck-task-banner/stuck-task-banner.component';
import {HostStore} from '../../../hosts/store/host.signal-store';
import {PageHeaderComponent, PageLayoutComponent} from '../../../shared/components';
import {ErrorBannerComponent} from '../../../shared/components/error-banner/error-banner.component';
import {LoadingStateComponent} from '../../../shared/components/loading-state/loading-state.component';
import {DEVICE_STATUS} from '../../../shared/constants';
import {PlatformPolicyService} from '../../../shared/services/platform-policy.service';
import type {Device, DeviceInstance} from '../../models/device.model';
import {bootProgressLine} from '../../models/device.model';
import {getOptimalLayout} from '../../models/display.model';
import {healthyInstanceCount} from '../../models/instance-state';
import {operationVerbLabel} from '../../models/operation.model';
import {instanceReachability} from '../../models/reachability';
import {buildRuler, occupancyHeadline, radioRail, type SlotOccupant} from '../../models/slot-space';
import {vsockMembership} from '../../models/vsock-membership';
import {DeviceActionsService} from '../../services/device-actions.service';
import {HostDevicesService} from '../../services/host-devices.service';
import {DeviceStore} from '../../store/device.signal-store';
import type {InstanceBusy, InstanceVerb} from '../instance-tile/instance-tile.component';
import {InstanceTileComponent} from '../instance-tile/instance-tile.component';
import {ReachabilityPanelComponent} from '../reachability-panel/reachability-panel.component';
import {SlotRulerComponent} from '../slot-ruler/slot-ruler.component';
import {VsockBandComponent} from '../vsock-band/vsock-band.component';
import {DeviceArtifactsCardComponent} from './device-artifacts-card.component';
import {DeviceConnectionCardComponent} from './device-connection-card.component';
import {DeviceFactsCardsComponent} from './device-facts-cards.component';
import {DeviceOperationsCardComponent} from './device-operations-card.component';
import {DeviceRecordingsCardComponent} from './device-recordings-card.component';
import {DeviceSnapshotsCardComponent} from './device-snapshots-card.component';
import {DeviceSummaryComponent} from './device-summary.component';

@Component({
  selector: 'app-device-detail',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    LoadingStateComponent,
    ErrorBannerComponent,
    RouterModule,
    MatCardModule,
    MatButtonModule,
    MatButtonToggleModule,
    MatIconModule,
    MatChipsModule,
    MatMenuModule,
    MatTooltipModule,
    PageLayoutComponent,
    PageHeaderComponent,
    DeviceConnectionCardComponent,
    DeviceArtifactsCardComponent,
    DeviceOperationsCardComponent,
    DeviceRecordingsCardComponent,
    DeviceSnapshotsCardComponent,
    DeviceSummaryComponent,
    DeviceFactsCardsComponent,
    InstanceTileComponent,
    SlotRulerComponent,
    VsockBandComponent,
    ReachabilityPanelComponent,
    StuckTaskBannerComponent,
  ],
  template: `
    <app-page-layout variant="detail">
      <div page-header>
        <app-page-header
          [title]="device()?.name || 'Device Details'"
          [subtitle]="device()?.id || ''"
          [showBackButton]="true"
          (back)="goBack()">
          <div primary-action>
            @if (device(); as device) {
              @if (device.status === DEVICE_STATUS.STOPPED) {
                <button type="button"
                  mat-flat-button
                  (click)="actions.start(device)"
                >
                  <mat-icon>play_arrow</mat-icon>
                  Start
                </button>
              }
              @if (device.status === DEVICE_STATUS.RUNNING) {
                <button type="button"
                  mat-flat-button
                  color="warn"
                  (click)="actions.stop(device)"
                >
                  <mat-icon>stop</mat-icon>
                  Stop
                </button>
                <!-- Interaction verbs act on ONE Android VM. With a single
                     instance the header is unambiguous; with a group the tiles
                     carry them, and a header copy would have to pick a member
                     silently, the exact lie this layout exists to remove. -->
                @if (!multiInstance()) {
                  <button type="button"
                    mat-icon-button
                    [matMenuTriggerFor]="deviceOpsMenu"
                    matTooltip="Device actions"
                    aria-label="Device actions"
                  >
                    <mat-icon>more_vert</mat-icon>
                  </button>
                  <mat-menu #deviceOpsMenu="matMenu">
                    <button type="button" mat-menu-item (click)="runVerb(device, selectedInstance(), 'restart')">
                      <mat-icon>restart_alt</mat-icon>
                      Restart
                    </button>
                    <button type="button" mat-menu-item (click)="runVerb(device, selectedInstance(), 'powerwash')">
                      <mat-icon>restore</mat-icon>
                      Powerwash
                    </button>
                    <button type="button" mat-menu-item (click)="runVerb(device, selectedInstance(), 'powerbtn')">
                      <mat-icon>power_settings_new</mat-icon>
                      Power button
                    </button>
                    <button type="button" mat-menu-item (click)="runVerb(device, selectedInstance(), 'screenshot')">
                      <mat-icon>photo_camera</mat-icon>
                      Screenshot
                    </button>
                    <button type="button" mat-menu-item (click)="runVerb(device, selectedInstance(), 'record-start')">
                      <mat-icon>videocam</mat-icon>
                      Start recording
                    </button>
                    <button type="button" mat-menu-item (click)="runVerb(device, selectedInstance(), 'record-stop')">
                      <mat-icon>videocam_off</mat-icon>
                      Stop recording
                    </button>
                  </mat-menu>
                }
              }
              <button type="button"
                mat-button
                color="warn"
                (click)="actions.delete(device, {navigateToListAfter: true})"
              >
                <mat-icon>delete</mat-icon>
                Delete
              </button>
            }
          </div>
        </app-page-header>
      </div>

      @if (device(); as device) {
        <div page-sidebar>
          <app-device-summary
            [device]="device"
            (pin)="actions.pin(device)"
            (unpin)="actions.unpin(device)" />
        </div>
      }

      <div page-content>
        <!-- Loading State -->
        @if (loading()) {
          <app-loading-state />
        }

        <!-- Error State -->
        @if (error(); as error) {
          <app-error-banner [message]="error" />
        }

        <!-- Device Details -->
        @if (device(); as device) {
          <div class="device-content">
            <!-- Status indicator row -->
            <div class="device-status-row">
              <div [class]="'status-indicator ' + device.status"></div>
              <span class="status-label">{{ device.status }}</span>
              @if (waitingOnHost(); as host) {
                <a class="waiting-host-chip" [routerLink]="['/hosts', host.id]">
                  <mat-icon>hourglass_top</mat-icon>
                  Waiting for host {{ host.name }} ({{ host.status }})
                </a>
              }
              @if (bootProgress(); as progress) {
                <span class="boot-progress-chip" [class.looping]="bootLooping()">
                  <mat-icon>{{ bootLooping() ? 'sync_problem' : 'monitor_heart' }}</mat-icon>
                  {{ progress }}
                </span>
              }
              @if (healthLine(); as health) {
                <span class="health-chip">
                  <mat-icon>flag</mat-icon>
                  {{ health }}
                </span>
              }
              @if (device.batchId; as batchId) {
                <a class="batch-chip" [routerLink]="['/batches', batchId]">
                  <mat-icon>dataset</mat-icon>
                  Batch member
                </a>
              }
            </div>
          <app-stuck-task-banner [resourceId]="device.id" />

          @if (multiInstance()) {
            <section class="instances-section">
              <div class="section-head">
                <h2>Instances</h2>
                <a
                  [routerLink]="['/devices', device.id, 'player']"
                  [queryParams]="{instance: selectedInstance()}"
                  >Open player</a
                >
              </div>
              <div [class]="['instance-grid', tileLayout()]">
                @for (inst of instances(); track inst.name) {
                  <app-instance-tile
                    [instance]="inst"
                    [selected]="inst.name === selectedInstance()"
                    [busy]="activeVerb()"
                    (tileSelect)="scopeTo(inst.name)"
                    (openPlayer)="openPlayer(device, inst.name)"
                    (verb)="runVerb(device, inst.name, $event)"
                  />
                }
              </div>
            </section>
          }

          <!-- The slot space is a HOST property, so it is drawn whatever the
               group size: a lone instance still sits at one of the host's
               slots, still shares the wire with every other group there, and
               still cannot see its host's fragmentation any other way. What a
               single instance drops is the lane chrome, not the ruler. -->
          @if (rulerNotches(); as notches) {
            <section class="slot-space">
              <app-slot-ruler
                [notches]="notches"
                [headline]="rulerHeadline()"
                [hostName]="hostName()"
                [selected]="selectedSlot()"
                [highlighted]="highlightedSlots()"
                (slotSelect)="selectSlot($event)"
                (highlight)="highlightedSlot.set($event)"
              />
              <app-vsock-band [membership]="vsock()" />
              @if (radio(); as rail) {
                <p class="radio-rail">
                  <span class="rail-label">{{ rail.anchor }}</span>
                  <span class="rail-ports">{{ rail.ports }}</span>
                </p>
              }
            </section>
          }

          @if (multiInstance()) {
            <!-- The scope switch sits in the heading that states the scope.
                 A tile click re-scopes too, but then the gesture and its
                 consequence are a page apart with only a heading as witness.
                 The cards below carry per-instance commands, so what names
                 the scope has to be able to change it. -->
            <div class="scope-divider instance-scope">
              <h2 id="instance-scope">Instance</h2>
              <mat-button-toggle-group
                aria-labelledby="instance-scope"
                [value]="selectedInstance()"
                (change)="scopeTo($event.value)"
              >
                @for (inst of instances(); track inst.name) {
                  <mat-button-toggle [value]="inst.name">{{ inst.name }}</mat-button-toggle>
                }
              </mat-button-toggle-group>
            </div>
          }

          <!-- Instance-bound, never a legend: every value comes from the
               selected instance's row, so it is drawn only once one is
               selected and it all changes when another is. -->
          @if (reachability(); as reach) {
            <app-reachability-panel [reachability]="reach" />
          }

          @if (device.status === DEVICE_STATUS.RUNNING) {
            <app-device-connection-card [device]="device" [instance]="selectedInstance()" />
          }

          <app-device-recordings-card [device]="device" [instance]="selectedInstance()" />

          @if (multiInstance()) {
            <h2 class="scope-divider">Device</h2>
          }

          <app-device-facts-cards [device]="device" />

          <app-device-operations-card
            [operations]="operations()"
            [instanceCount]="instances().length" />

          <app-device-artifacts-card [device]="device" />

          <app-device-snapshots-card [device]="device" [snapshots]="snapshots()" />

          </div>
        }
      </div>
    </app-page-layout>
  `,
  styles: `
      /* Container layout is handled by PageLayoutComponent. */

      .waiting-host-chip {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        margin-left: 12px;
        padding: 4px 12px;
        background: var(--cco-accent-surface);
        color: var(--cco-accent);
        border-radius: 16px;
        font-size: 12px;
        text-decoration: none;
      }

      .boot-progress-chip {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        margin-left: 12px;
        padding: 4px 12px;
        background: var(--cco-accent-surface);
        color: var(--cco-accent);
        border-radius: 16px;
        font-size: 12px;
      }

      .boot-progress-chip.looping {
        background: var(--cco-error-surface);
        color: var(--cco-error);
      }

      .waiting-host-chip:hover {
        text-decoration: underline;
      }

      .waiting-host-chip mat-icon {
        font-size: 16px;
        width: 16px;
        height: 16px;
      }

      .health-chip {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        margin-left: 12px;
        padding: 4px 12px;
        background: var(--cco-surface-variant);
        color: var(--cco-text-secondary);
        border-radius: 16px;
        font-size: 12px;
        font-variant-numeric: tabular-nums;
      }

      .health-chip mat-icon {
        font-size: 16px;
        width: 16px;
        height: 16px;
      }

      .instances-section {
        margin-bottom: 16px;
      }

      .section-head {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        gap: 12px;
      }

      .section-head h2,
      .scope-divider {
        font-size: 13px;
        font-weight: 500;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        color: var(--cco-text-secondary);
        margin: 0;
      }

      /* The dividers are the whole teaching mechanism. A rule and a heading
         say which scope the cards under them act on, with no tooltip and no
         second component. */
      .scope-divider {
        margin: 24px 0 12px;
        padding-top: 12px;
        border-top: 1px solid var(--cco-border);
      }

      .instance-scope {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 12px;
      }

      .instance-scope h2 {
        font: inherit;
        margin: 0;
      }

      .instance-scope mat-button-toggle-group {
        --mat-standard-button-toggle-height: 28px;

        text-transform: none;
      }

      .instance-grid {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        margin-top: 12px;
      }

      .instance-grid.grid app-instance-tile {
        flex: 1 1 220px;
        max-width: 320px;
      }

      /* Past three, tiles are for scanning rather than reading, so they stop
         growing and wrap into a denser strip. */
      .instance-grid.primary-thumbnails app-instance-tile {
        flex: 0 0 auto;
      }

      .slot-space {
        display: block;
        margin-block-start: var(--cco-space-16);
      }

      app-slot-ruler {
        display: block;
      }

      /* The rail is group-private and the left inset is what says so. It is the
         only geometry carrying the isolation model, so it does not compress. */
      .radio-rail {
        display: flex;
        flex-direction: column;
        gap: 2px;
        margin: 12px 0 0 var(--cco-space-24);
        padding-block-start: 8px;
        border-block-start: 3px solid var(--cco-text-secondary);
      }

      .radio-rail .rail-label {
        font-size: var(--cco-text-label);
        letter-spacing: 0.06em;
        text-transform: uppercase;
        color: var(--cco-text-secondary);
      }

      .radio-rail .rail-ports {
        font-family: var(--cco-font-mono);
        font-variant-numeric: tabular-nums;
        font-size: var(--cco-text-sm);
        color: var(--cco-text-secondary);
      }

      .batch-chip {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        margin-left: 12px;
        padding: 4px 12px;
        background: var(--cco-surface-variant);
        color: var(--cco-text-secondary);
        border-radius: 16px;
        font-size: 12px;
        text-decoration: none;
      }

      .batch-chip:hover {
        text-decoration: underline;
      }

      .batch-chip mat-icon {
        font-size: 16px;
        width: 16px;
        height: 16px;
      }

      .device-status-row {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 16px;
      }

      .status-label {
        text-transform: capitalize;
        font-weight: 500;
        color: var(--cco-text-secondary);
      }



      .status-indicator {
        width: 16px;
        height: 16px;
        border-radius: 50%;
        background: var(--cco-text-secondary);
      }

      .status-indicator.running {
        background: var(--cco-success);
      }

      .status-indicator.stopped {
        background: var(--cco-warning);
      }

      .status-indicator.creating {
        background: var(--cco-accent);
        animation: pulse 1.5s infinite;
      }

      .status-indicator.error {
        background: var(--cco-error);
      }

      @keyframes pulse {
        0% { opacity: 1; }
        50% { opacity: 0.5; }
        100% { opacity: 1; }
      }





      /* M3 Accessible Chip Styling: light container + dark text (4.5:1+ contrast) */
      mat-chip.running {
        background: var(--cco-success-surface) !important;
        color: var(--cco-success) !important;
      }

      mat-chip.stopped {
        background: var(--cco-warning-surface) !important;
        color: var(--cco-warning) !important;
      }

      mat-chip.creating {
        background: var(--cco-accent-surface) !important;
        color: var(--cco-accent) !important;
      }

      mat-chip.error {
        background: var(--cco-error-surface) !important;
        color: var(--cco-error) !important;
      }

      mat-chip.gfxstream {
        background: var(--cco-success-surface) !important;
        color: var(--cco-success) !important;
      }

      mat-chip.drm_virgl {
        background: var(--cco-accent-surface) !important;
        color: var(--cco-accent) !important;
      }

      mat-chip.swiftshader {
        background: var(--cco-warning-surface) !important;
        color: var(--cco-warning) !important;
      }


      a {
        color: var(--cco-accent);
        text-decoration: none;
      }

      a:hover {
        text-decoration: underline;
      }
  `,
})
export class DeviceDetailComponent {
  private readonly deviceStore = inject(DeviceStore);
  private readonly hostStore = inject(HostStore);
  private readonly hostDevices = inject(HostDevicesService);
  private readonly policy = inject(PlatformPolicyService);

  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);
  protected readonly actions = inject(DeviceActionsService);

  /**
   * What the host image can actually back, served rather than retyped. A
   * respin that changes it must not need a console edit to be drawn right.
   */
  private readonly backedSlots = computed(() => this.policy.lifetimes().hostImageBackedSlots);

  // Expose constants for template
  protected readonly DEVICE_STATUS = DEVICE_STATUS;

  // Reactive route parameter using toSignal (no subscription leak)
  private readonly routeParams = toSignal(this.route.paramMap);

  // Computed device ID from route
  private readonly deviceId = computed(() => this.routeParams()?.get('id') ?? null);

  protected readonly device = this.deviceStore.selectedDevice;
  /** Recent restart/powerwash handles for this device, newest first. */
  protected readonly operations = this.deviceStore.operations;
  protected readonly snapshots = this.deviceStore.snapshots;
  protected readonly loading = this.deviceStore.loading;
  protected readonly error = this.deviceStore.error;

  /** The Android VMs the host reported, in its own listing order. */
  protected readonly instances = computed<DeviceInstance[]>(() => this.device()?.instances ?? []);

  protected readonly multiInstance = computed(() => this.instances().length > 1);

  /**
   * Which instance the upper card block is scoped to.
   *
   * A linked signal rather than a plain one: the device polls, so the array
   * identity changes constantly and a selection held in a plain signal would
   * survive a rename or a removal and point at nothing. It keeps the user's
   * pick while that instance still exists and falls back to the first.
   */
  protected readonly selectedInstance = linkedSignal<DeviceInstance[], string>({
    source: this.instances,
    computation: (instances, previous) =>
      previous && instances.some(i => i.name === previous.value)
        ? previous.value
        : (instances[0]?.name ?? ''),
  });

  protected readonly tileLayout = computed(() => getOptimalLayout(this.instances().length));

  /**
   * How much of the group is up, shown only where the device's own folded
   * status could hide a dead sibling. Both numbers come off the row the server
   * sent. The fold that decided the status counts the same instances.
   */
  protected readonly healthLine = computed(() => {
    const instances = this.instances();
    if (instances.length <= 1) {
      return null;
    }
    return `${healthyInstanceCount(instances)} of ${instances.length} healthy`;
  });

  /**
   * The host's display name, falling back to the host id when the host row has
   * not been read yet, so the link never renders blank.
   */
  protected readonly hostName = computed(() => {
    const hostId = this.device()?.hostId;
    return hostId ? (this.hostStore.entityMap()[hostId]?.name ?? hostId) : '';
  });

  /**
   * The host's slot scale, seated from the slots the instances themselves
   * reported.
   *
   * Drawn only once some instance on the host has a resolved slot. An empty
   * occupant set means either the read failed or every row predates the stored
   * slot, and both would render as a host of entirely free notches, a claim
   * about the host rather than the absence of one. Undrawn beats wrong, because
   * on a fragmented host a guessed layout mis-seats every notch after the first
   * hole.
   */
  protected readonly rulerNotches = computed(() => {
    const device = this.device();
    const host = device?.hostId ? this.hostStore.entityMap()[device.hostId] : undefined;
    if (!(device && host)) {
      return null;
    }
    const occupants: SlotOccupant[] = this.hostDevices.occupants();
    if (occupants.length === 0) {
      return null;
    }
    return buildRuler(host.maxDevices, this.backedSlots(), occupants, device.id);
  });

  protected readonly rulerHeadline = computed(() => {
    const hostId = this.device()?.hostId;
    const host = hostId ? this.hostStore.entityMap()[hostId] : undefined;
    return host
      ? occupancyHeadline(this.hostDevices.usedSlots(), this.backedSlots(), host.maxDevices)
      : '';
  });

  /** The slot the page's one instance selection currently names. */
  protected readonly selectedSlot = computed(
    () => this.instances().find(i => i.name === this.selectedInstance())?.slot ?? null
  );

  /** Which notch the ruler-to-tile correspondence is lighting, hover or focus. */
  protected readonly highlightedSlot = signal<number | null>(null);

  protected readonly highlightedSlots = computed(() => {
    const slot = this.highlightedSlot();
    return slot === null ? [] : [slot];
  });

  /**
   * Who can reach whom over vsock, as the launch config declared it.
   *
   * Read from the stored env_config, never from the host's observation. The
   * partition is a configuration fact, and CCO does not watch vsock traffic.
   * A group that configured nothing still draws. Untagged VMs mesh, so blank
   * space there would wrongly read as isolation.
   */
  protected readonly vsock = computed(() =>
    vsockMembership(
      this.instances().map(instance => instance.name),
      this.device()?.envConfig
    )
  );

  /**
   * This group's simulated radio, named by the slot its ports derive from.
   *
   * Every port here is the group's lowest slot minus one, added to a base, so
   * the anchor has to be stated. Allocation fragments, which makes the anchor
   * unguessable from the order the instances appear in. One named slot explains
   * every port on the page.
   */
  private readonly rail = computed(() =>
    radioRail(this.instances().map(instance => instance.slot ?? 0))
  );

  protected readonly radio = computed(() => {
    const rail = this.rail();
    if (!rail) {
      return null;
    }
    return {
      anchor: `Radio · anchored at slot ${String(rail.anchorSlot).padStart(2, '0')}`,
      ports: `netsim · rootcanal :${rail.rootcanalHci} · wmediumd · pica :${rail.picaUci}`,
    };
  });

  /**
   * What can address the instance the page is scoped to.
   *
   * Reads the same `selectedInstance` every other per-instance card reads, so
   * the panel can never describe an instance no control on the page names.
   *
   * The host's population comes from the host-wide device read rather than
   * from this device's own row, because the wire is shared with every other
   * group on the host. That is the fact the panel exists to state, and the one
   * a device-local count cannot see.
   */
  protected readonly reachability = computed(() => {
    const device = this.device();
    const instances = this.instances();
    const instance = instances.find(i => i.name === this.selectedInstance());
    if (!(device && instance)) {
      return null;
    }
    return instanceReachability({
      instance,
      deviceName: device.name,
      hostName: this.hostName() || 'this host',
      peers: instances.filter(i => i.name !== instance.name).map(i => i.name),
      radio: this.rail(),
      vsockLabel: this.vsock().label,
      vsockLayer: this.vsock().layer,
      hostInstances: this.hostDevices.usedSlots(),
    });
  });

  /**
   * A notch selects through `selectedInstance` rather than beside it.
   *
   * That signal already scopes the page. The header's verb menu, the player
   * link, the connection and recordings cards and the scope toggle all read it,
   * so a second selection here would let those cards address an instance no
   * control on the page names. A foreign notch names no instance of this
   * device's and therefore changes nothing.
   */
  protected selectSlot(slot: number): void {
    const mine = this.instances().find(instance => instance.slot === slot);
    if (mine) {
      this.scopeTo(mine.name);
    }
  }

  /**
   * Scopes the page to one instance and leaves the URL saying so.
   *
   * Every control that changes the scope routes through here, so a copied link
   * always names the instance on screen. The parameter is the SLOT rather than
   * the instance name. A slot is what the ruler, the radio ports and the TAP
   * are all keyed on, and it survives a group being recreated under the same
   * instance names.
   */
  protected scopeTo(name: string): void {
    this.selectedInstance.set(name);
    const slot = this.instances().find(instance => instance.name === name)?.slot;
    void this.router.navigate([], {
      relativeTo: this.route,
      queryParams: {slot: slot && slot > 0 ? slot : null},
      queryParamsHandling: 'merge',
      replaceUrl: true,
    });
  }

  /**
   * The verb running somewhere on this device, and the instance it holds.
   *
   * Admission allows one exclusive operation per DEVICE, so restarting one
   * instance while powerwashing another is refused. This closes every tile's
   * menu rather than one, which is what the server would enforce anyway. It
   * carries the holder's name because the operation row records it. A disabled
   * menu that says which instance is busy reads as a system working, and an
   * anonymous one reads as a broken page.
   */
  protected readonly activeVerb = computed<InstanceBusy | null>(() => {
    const op = this.deviceStore.activeOperations()[0];
    return op ? {verb: operationVerbLabel(op), instance: op.metadata.instanceName} : null;
  });

  /**
   * The host this device is waiting on: set while the device is coming up on
   * a host that is not running yet. Drives the waiting chip; a host that
   * fails provisioning surfaces through the device's own error path instead.
   */
  protected readonly waitingOnHost = computed(() => {
    const device = this.device();
    if (!device || (device.status !== 'pending' && device.status !== 'creating')) {
      return null;
    }
    const host = device.hostId ? this.hostStore.entityMap()[device.hostId] : undefined;
    if (!host) {
      return null;
    }
    const waiting =
      host.status === 'pending' || host.status === 'provisioning' || host.status === 'starting';
    return waiting ? host : null;
  });

  /**
   * The guest's observed boot progress: what the boot watch saw in the CVD's
   * own logs, refreshed with the row. A boot loop keeps showing after the
   * settle to error, and the evidence line is the diagnosis.
   */
  protected readonly bootProgress = computed(() => {
    const device = this.device();
    if (!device?.bootObservation) {
      return undefined;
    }
    return bootProgressLine(device.status, device.bootObservation);
  });

  protected readonly bootLooping = computed(
    () => this.device()?.bootObservation?.verdict === 'looping'
  );

  private readonly queryParams = toSignal(this.route.queryParamMap);

  /** The slot a shared link arrived on, already applied. */
  private appliedSlot: string | null = null;

  private watchedHostId: string | null = null;
  private hostWatch: {destroy: () => void} | null = null;

  constructor() {
    // Reactively load device when route changes (replaces ngOnInit subscription)
    effect(onCleanup => {
      const id = this.deviceId();
      if (id) {
        this.deviceStore.selectDevice(id);
        this.deviceStore.loadOne(id);
        this.deviceStore.loadSnapshots(id);
        // Restart and powerwash leave the device row untouched, so their outcome
        // exists only on their operation handles. This poll has its own stop
        // condition (every operation settled) because the device watch below
        // stops at a terminal STATUS, and `running` is terminal throughout.
        const opWatch = this.deviceStore.watchOperations(id);
        onCleanup(() => opWatch.destroy());
        // Keep the page live while the device is transitional. The store is
        // root-scoped, so the poll must be destroyed with this view. An
        // uncancelled watch outlives the page and polls in the background.
        const watch = this.deviceStore.watch(id);
        onCleanup(() => watch.destroy());
        this.watchedHostId = null;
      }
    });

    // Load the device's host once, whatever the device's status. The waiting
    // chip and the occupancy line both read it, and a page reached by URL has
    // an empty host store. While the device is still coming up, also watch it
    // so the chip tracks provisioning -> running truthfully. The watch is held
    // in a field, not in effect onCleanup, because this effect re-runs on every
    // device poll update and must not cancel it mid-provision.
    effect(() => {
      const device = this.device();
      if (!device?.hostId || this.watchedHostId === device.hostId) {
        return;
      }
      this.watchedHostId = device.hostId;
      this.hostStore.loadOne(device.hostId);
      // The ruler needs every instance placed on this host, beyond this
      // device's own. The host row carries occupancy as a scalar, and a scale
      // drawn from a count has to invent the positions of everyone else's
      // slots.
      this.hostDevices.load(device.hostId);
      if (device.status === 'pending' || device.status === 'creating') {
        this.hostWatch?.destroy();
        this.hostWatch = this.hostStore.watch(device.hostId);
      }
    });
    // Applied when the instances land rather than on navigation: the device
    // read is a separate request, so the parameter is routinely known before
    // there is any instance to resolve it against. A slot this device does not
    // hold leaves the scope alone. The link is stale, not a reason to move
    // the reader somewhere arbitrary.
    effect(() => {
      const raw = this.queryParams()?.get('slot') ?? null;
      const instances = this.instances();
      if (raw === null || instances.length === 0 || this.appliedSlot === raw) {
        return;
      }
      this.appliedSlot = raw;
      const match = instances.find(instance => instance.slot === Number(raw));
      if (match) {
        this.selectedInstance.set(match.name);
      }
    });

    inject(DestroyRef).onDestroy(() => this.hostWatch?.destroy());
  }

  /**
   * Runs one interaction verb against a named instance.
   *
   * The name comes from the tile the menu was opened on, never from the
   * current selection. Opening a tile's menu does not select that tile, so
   * reading the selection here would fire the verb at a different Android VM
   * than the one the user clicked.
   */
  protected runVerb(device: Device, instance: string, verb: InstanceVerb): void {
    switch (verb) {
      case 'restart':
        this.actions.restart(device, instance);
        break;
      case 'powerwash':
        this.actions.powerwash(device, instance);
        break;
      case 'powerbtn':
        this.actions.powerbtn(device, instance);
        break;
      case 'screenshot':
        this.actions.screenshot(device, instance);
        break;
      case 'record-start':
        this.actions.recordStart(device, instance);
        break;
      case 'record-stop':
        this.actions.recordStop(device, instance);
        break;
      default:
        break;
    }
  }

  /** Opens the player already focused on one instance, not on the group's first. */
  protected openPlayer(device: Device, instance: string): void {
    void this.router.navigate(['/devices', device.id, 'player'], {queryParams: {instance}});
  }

  protected goBack(): void {
    void this.router.navigate(['/devices']);
  }
}
