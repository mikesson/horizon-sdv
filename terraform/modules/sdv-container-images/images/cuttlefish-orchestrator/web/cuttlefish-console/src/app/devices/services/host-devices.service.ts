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

import {computed, inject, Service, signal} from '@angular/core';
import {tapResponse} from '@ngrx/operators';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {pipe, switchMap, tap} from 'rxjs';

import {getErrorMessage} from '../../shared/utils';
import type {Device} from '../models/device.model';
import {lowestSlot, type SlotOccupant} from '../models/slot-space';
import {DeviceApiService} from './device-api.service';

interface HostDevicesState {
  readonly hostId: string | null;
  readonly devices: readonly Device[];
  readonly loading: boolean;
  readonly error: string | null;
}

const EMPTY: HostDevicesState = {hostId: null, devices: [], loading: false, error: null};

/**
 * Every device on one host, with the slots its instances hold.
 *
 * This is a separate collection rather than a selector over `DeviceStore`
 * because that store is root-scoped and its entity collection belongs to the
 * device list page: filtering it by host would empty the list a user had
 * already scoped for themselves, and re-filtering on the way back would race
 * their own filter. The host reconciler's counters cannot substitute either.
 * `currentDevices` and `maxDevices` are scalars, and a slot layout needs the
 * set, not the count.
 */
@Service()
export class HostDevicesService {
  private readonly api = inject(DeviceApiService);
  private readonly state = signal<HostDevicesState>(EMPTY);

  readonly devices = computed(() => this.state().devices);
  readonly loading = computed(() => this.state().loading);
  readonly error = computed(() => this.state().error);

  /**
   * Which instance sits in which slot, across the whole host.
   *
   * An instance whose slot never resolved is left out rather than defaulted:
   * CCO stores zero for an instance the host described without a serial, and
   * seating that at slot 0, or at its array position, mis-seats every notch
   * after it on a fragmented host.
   */
  readonly occupants = computed<SlotOccupant[]>(() =>
    this.devices().flatMap(device =>
      (device.instances ?? []).flatMap(instance => {
        const slot = instance.slot ?? 0;
        return slot > 0
          ? [{slot, deviceId: device.id, deviceName: device.name, instanceName: instance.name}]
          : [];
      })
    )
  );

  /**
   * The host's devices in lane order, by lowest owned slot.
   *
   * Slots do not move when a state flips, so a refresh cannot reorder the
   * stack under someone reading it. Devices holding no resolved slot sort last
   * and keep their relative order.
   */
  readonly lanes = computed(() =>
    [...this.devices()].sort(
      (a, b) =>
        lowestSlot((a.instances ?? []).map(i => i.slot ?? 0)) -
        lowestSlot((b.instances ?? []).map(i => i.slot ?? 0))
    )
  );

  /** Instance slots taken across the host, counting only resolved ones. */
  readonly usedSlots = computed(() => this.occupants().length);

  readonly load = rxMethod<string>(
    pipe(
      tap(hostId => this.state.set({...EMPTY, hostId, loading: true})),
      switchMap(hostId =>
        this.api.list({hostId}).pipe(
          tapResponse({
            next: response =>
              this.state.update(current => ({
                ...current,
                devices: response.items,
                loading: false,
              })),
            // A failed read leaves the ruler undrawn rather than drawn wrong:
            // an empty set would render every slot free, which is a claim about
            // the host rather than an absence of one.
            error: (err: unknown) =>
              this.state.update(current => ({
                ...current,
                devices: [],
                loading: false,
                error: getErrorMessage(err, 'Failed to load the host’s devices'),
              })),
          })
        )
      )
    )
  );
}
