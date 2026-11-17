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
  afterNextRender,
  ChangeDetectionStrategy,
  Component,
  computed,
  ElementRef,
  effect,
  Injector,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import {Router, RouterModule} from '@angular/router';

import type {Device, DeviceInstance} from '../../models/device.model';
import {laneSummary} from '../../models/lane-summary';
import type {Notch} from '../../models/slot-space';
import {radioRail} from '../../models/slot-space';
import type {VsockMembership} from '../../models/vsock-membership';
import {vsockMembership} from '../../models/vsock-membership';
import {InstanceTileComponent} from '../instance-tile/instance-tile.component';
import {SlotRulerComponent} from '../slot-ruler/slot-ruler.component';
import {VsockBandComponent} from '../vsock-band/vsock-band.component';

/** One device's row in the stack, with everything its header renders. */
interface Lane {
  readonly device: Device;
  readonly slots: readonly number[];
  readonly summary: ReturnType<typeof laneSummary>;
  readonly radio: {anchor: string; ports: string} | null;
  readonly vsock: VsockMembership;
}

/**
 * Every group on one host: what shares a wire, what shares a radio, and how
 * much room is left.
 *
 * Vertical is host-wide and horizontal is group-private. Nothing else encodes
 * scope, not colour and not weight. The spine carries the bridges every TAP on
 * the host hangs off; each group's radio rail is drawn inside its own lane with
 * a left inset it never crosses. That gap is the isolation model: two groups on
 * one host are always on the same wire and never on the same radio.
 */
@Component({
  selector: 'app-host-lanes',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterModule, SlotRulerComponent, InstanceTileComponent, VsockBandComponent],
  template: `
    <app-slot-ruler
      scope="host"
      [notches]="notches()"
      [headline]="headline()"
      [hostName]="hostName()"
      [highlighted]="highlightedSlots()"
      (slotSelect)="revealSlot($event)"
      (highlight)="highlightSlot($event)"
    />

    @if (chrome()) {
      <p class="bridges" aria-hidden="true">cvd-ebr · cvd-wbr</p>
    }

    <ul class="lanes" [class.chrome]="chrome()">
      @for (lane of lanes(); track lane.device.id) {
        <li
          class="lane"
          animate.enter="lane-in"
          animate.leave="lane-out"
          [class.focused]="lane.device.id === focusedLane()"
        >
          <div class="lane-head">
            <button
              type="button"
              class="disclose"
              [attr.aria-expanded]="isExpanded(lane.device.id)"
              [attr.aria-controls]="'lane-' + lane.device.id"
              (click)="toggle(lane.device.id)"
              (mouseenter)="focusLane(lane)"
              (mouseleave)="focusLane(null)"
              (focus)="focusLane(lane)"
              (blur)="focusLane(null)"
            >
              <span class="chevron" [class.open]="isExpanded(lane.device.id)" aria-hidden="true"
                >›</span
              >
              <span class="visually-hidden">{{ laneOutline(lane) }}</span>
            </button>

            <a class="lane-name" [routerLink]="['/devices', lane.device.id]">{{
              lane.device.name
            }}</a>

            <span class="lane-status" [style.color]="lane.summary.statusColor">{{
              lane.summary.statusWord
            }}</span>

            @if (lane.summary.exception) {
              <span class="lane-exception" [style.color]="lane.summary.exceptionColor"
                >· {{ lane.summary.exception }}</span
              >
            } @else if (!isExpanded(lane.device.id) && lane.summary.slots) {
              <span class="lane-slots">· {{ lane.summary.slots }}</span>
            }
          </div>

          @if (isExpanded(lane.device.id)) {
            <div class="lane-body" [id]="'lane-' + lane.device.id">
              <!-- Full tiles, but without the verb menu: this page carries no
                   operation feed, so it cannot tell a busy instance from a free
                   one. Selecting a tile lands on the device page scoped to that
                   very instance, which is where the verbs and the busy state
                   both live. -->
              <div class="instance-grid">
                @for (instance of lane.device.instances ?? []; track instance.name) {
                  <app-instance-tile
                    [instance]="instance"
                    [showVerbs]="false"
                    (tileSelect)="openInstance(lane.device.id, instance)"
                  />
                }
              </div>

              <app-vsock-band [membership]="lane.vsock" />

              @if (lane.radio; as rail) {
                <p class="radio-rail">
                  <span class="rail-label">{{ rail.anchor }}</span>
                  <span class="rail-ports">{{ rail.ports }}</span>
                </p>
              }
            </div>
          }
        </li>
      }
    </ul>
  `,
  styles: `
    :host {
      display: block;
    }

    .bridges {
      margin: var(--cco-space-16) 0 0;
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .lanes {
      margin: var(--cco-space-8) 0 0;
      padding: 0;
      list-style: none;
      display: flex;
      flex-direction: column;
      gap: var(--cco-space-16);
    }

    /* One group draws no lane chrome. The spine names a distinction that does
       not exist on a host with a single tenant, and drawing it there makes the
       common exclusive-host case look over-built. */
    .lanes.chrome {
      border-inline-start: 2px solid var(--cco-border);
      padding-inline-start: var(--cco-space-16);
    }

    .lane {
      border: 1px solid var(--cco-card-border);
      border-radius: var(--cco-radius-lg);
      background: var(--cco-surface);
      padding: var(--cco-space-8);
    }

    /* Accent the focused lane alone. The spine runs past every lane and so
       names none of them. */
    .lanes.chrome .lane.focused {
      border-color: var(--cco-accent);
      border-inline-start-width: 3px;
    }

    /* A group arriving or leaving is a real event on this page, so it is
       announced rather than swapped in silently. Its notches return to free on
       their own, because the ruler is rebuilt from the occupants. */
    @keyframes lane-in {
      from { opacity: 0; transform: translateY(-4px); }
      to { opacity: 1; transform: none; }
    }

    @keyframes lane-out {
      from { opacity: 1; }
      to { opacity: 0; }
    }

    .lane-in {
      animation: lane-in var(--cco-duration-fast) ease-out;
    }

    .lane-out {
      animation: lane-out var(--cco-duration-fast) ease-in;
    }

    @media (prefers-reduced-motion: reduce) {
      .lane-in,
      .lane-out,
      .chevron {
        animation: none;
        transition: none;
      }
    }

    .lane-head {
      display: flex;
      align-items: baseline;
      gap: var(--cco-space-8);
      flex-wrap: wrap;
    }

    .disclose {
      border: 0;
      background: none;
      padding: 0 var(--cco-space-4);
      cursor: pointer;
      color: var(--cco-text-secondary);
    }

    .chevron {
      display: inline-block;
      transition: transform var(--cco-duration-fast) ease;
    }

    .chevron.open {
      transform: rotate(90deg);
    }

    @media (prefers-reduced-motion: reduce) {
      .chevron {
        transition: none;
      }
    }

    .lane-name {
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-body);
      color: var(--cco-text);
    }

    .lane-status,
    .lane-exception,
    .lane-slots {
      font-size: var(--cco-text-body);
    }

    .lane-slots {
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      color: var(--cco-text-secondary);
    }

    .lane-body {
      margin-block-start: var(--cco-space-8);
    }

    .instance-grid {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-8);
    }

    /* The inset is the isolation model, so it does not compress. A rail that
       reached the spine would claim the radio is host-wide, which is the one
       thing it is not. */
    .radio-rail {
      display: flex;
      flex-direction: column;
      gap: 2px;
      margin: var(--cco-space-8) 0 0 var(--cco-space-24);
      padding-block-start: var(--cco-space-8);
      border-block-start: 3px solid var(--cco-text-secondary);
    }

    .rail-label {
      font-size: var(--cco-text-label);
      letter-spacing: 0.06em;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
    }

    .rail-ports {
      font-family: var(--cco-font-mono);
      font-variant-numeric: tabular-nums;
      font-size: var(--cco-text-sm);
      color: var(--cco-text-secondary);
    }

    .visually-hidden {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
    }
  `,
})
export class HostLanesComponent {
  private readonly router = inject(Router);
  private readonly element = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly injector = inject(Injector);

  readonly devices = input.required<readonly Device[]>();
  readonly notches = input.required<readonly Notch[]>();
  readonly headline = input.required<string>();
  readonly hostName = input<string>('this host');
  /**
   * The lane a shared URL arrived pointing at, by device id. Opened and
   * scrolled to once. A link that only expanded a lane below the fold would
   * land the reader on a page that looks unchanged.
   */
  readonly openLane = input<string | null>(null);

  /** Which lane the reader opened or closed, so the URL can follow them. */
  readonly laneToggle = output<{deviceId: string; expanded: boolean}>();

  /** Expanded lanes, keyed by device id, never by index, which the API reorders. */
  private readonly expanded = signal<ReadonlySet<string>>(new Set());
  private readonly focused = signal<Lane | null>(null);
  private readonly pinnedSlot = signal<number | null>(null);

  protected readonly focusedLane = computed(() => this.focused()?.device.id ?? null);

  protected readonly highlightedSlots = computed<readonly number[]>(() => {
    const lane = this.focused();
    if (lane) {
      return lane.slots;
    }
    const slot = this.pinnedSlot();
    return slot === null ? [] : [slot];
  });

  protected readonly lanes = computed<Lane[]>(() =>
    this.devices().map(device => {
      const instances = device.instances ?? [];
      const slots = instances.map(instance => instance.slot ?? 0).filter(slot => slot > 0);
      const rail = radioRail(slots);
      return {
        device,
        slots,
        summary: laneSummary(device),
        vsock: vsockMembership(
          instances.map(instance => instance.name),
          device.envConfig
        ),
        radio: rail && {
          anchor: `Radio · anchored at slot ${String(rail.anchorSlot).padStart(2, '0')}`,
          ports: `netsim · rootcanal :${rail.rootcanalHci} · wmediumd · pica :${rail.picaUci}`,
        },
      };
    })
  );

  protected readonly chrome = computed(() => this.lanes().length > 1);

  protected isExpanded(deviceId: string): boolean {
    return this.expanded().has(deviceId);
  }

  protected toggle(deviceId: string): void {
    const next = new Set(this.expanded());
    if (!next.delete(deviceId)) {
      next.add(deviceId);
    }
    this.expanded.set(next);
    this.laneToggle.emit({deviceId, expanded: next.has(deviceId)});
  }

  /** The lane already honoured, so a re-render does not re-scroll the reader. */
  private openedLane: string | null = null;

  constructor() {
    // Deferred to the lanes arriving: the device read is a separate request, so
    // the parameter is routinely known before there is any lane to open.
    effect(() => {
      const target = this.openLane();
      const lanes = this.lanes();
      if (!target || this.openedLane === target) {
        return;
      }
      if (!lanes.some(lane => lane.device.id === target)) {
        return;
      }
      this.openedLane = target;
      if (!this.isExpanded(target)) {
        this.toggle(target);
      }
      afterNextRender(
        () => {
          this.element.nativeElement
            .querySelector(`#lane-${CSS.escape(target)}`)
            ?.scrollIntoView({block: 'nearest', behavior: 'smooth'});
        },
        {injector: this.injector}
      );
    });
  }

  /**
   * Hover and focus do the same thing, and neither moves focus. A keyboard user
   * tabbing the lane headers has to get the correspondence a pointer gives.
   */
  protected focusLane(lane: Lane | null): void {
    this.focused.set(lane);
  }

  protected highlightSlot(slot: number | null): void {
    this.pinnedSlot.set(slot);
  }

  /**
   * Opens the device page already scoped to the instance that was clicked.
   *
   * The slot rather than the name, because that is what the device page's own
   * deep link resolves and what survives a group being recreated under the
   * same instance names.
   */
  protected openInstance(deviceId: string, instance: DeviceInstance): void {
    void this.router.navigate(['/devices', deviceId], {
      queryParams: instance.slot && instance.slot > 0 ? {slot: instance.slot} : {},
    });
  }

  /** Activating a notch opens the lane that owns it. */
  protected revealSlot(slot: number): void {
    const owner = this.lanes().find(lane => lane.slots.includes(slot));
    if (owner && !this.isExpanded(owner.device.id)) {
      this.toggle(owner.device.id);
    }
  }

  /**
   * The drawing is decoration and is hidden from assistive technology; this
   * sentence is what carries the same facts, so it has to say all of them.
   */
  protected laneOutline(lane: Lane): string {
    const summary = lane.summary;
    const parts = [lane.device.name, summary.slots || 'no slots resolved', summary.statusWord];
    if (summary.exception) {
      parts.push(summary.exception);
    }
    if (lane.radio) {
      parts.push(lane.radio.anchor.toLowerCase());
    }
    return parts.join(', ');
  }
}
