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

import {inject} from '@angular/core';
import {MatSnackBar} from '@angular/material/snack-bar';
import {tapResponse} from '@ngrx/operators';
import {patchState, signalStore, withMethods, withState} from '@ngrx/signals';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {concatMap, forkJoin, pipe, switchMap, tap} from 'rxjs';
import type {MachineType} from '../../machine-types/models';
import {MachineTypeApiService} from '../../machine-types/services/machine-type-api.service';
import {fail} from '../../shared/store/entity-ops';
import {getErrorMessage} from '../../shared/utils';
import type {AdminZone, MachineTypeAdminPatch} from '../models/catalog-admin.model';
import type {AdminFamily, FamilyShape, MachineFamilyPatch} from '../models/machine-family.model';
import {CatalogAdminApiService} from '../services/catalog-admin-api.service';

/**
 * Lays a per-shape patch onto one matrix row. The election clears every other
 * row's, so this runs over every shape of every family rather than only the one
 * that was clicked.
 */
function applyShapePatch(
  shape: FamilyShape,
  machineType: string,
  patch: MachineTypeAdminPatch
): FamilyShape {
  if (shape.machineType === machineType) {
    return {
      ...shape,
      isExcluded: patch.isEnabled === undefined ? shape.isExcluded : !patch.isEnabled,
      isDefault: patch.isDefault ?? shape.isDefault,
    };
  }
  return patch.isDefault && shape.isDefault ? {...shape, isDefault: false} : shape;
}

interface CatalogAdminState {
  /** The curation view: every curated family and the shapes discovery found. */
  families: AdminFamily[];
  machineTypes: MachineType[];
  zones: AdminZone[];
  /** The machine types per-zone availability is reported against. */
  curatedShapes: string[];
  loading: boolean;
  error: string | null;
}

const initialState: CatalogAdminState = {
  families: [],
  machineTypes: [],
  zones: [],
  curatedShapes: [],
  loading: false,
  error: null,
};

/**
 * Machine-type and zone curation state for the Catalog page. Inline toggles
 * are optimistic: state flips immediately and reverts to the pre-mutation
 * snapshot on error (the host-store idiom).
 */
export const CatalogAdminStore = signalStore(
  {providedIn: 'root'},
  withState(initialState),
  withMethods(store => {
    const catalogApi = inject(CatalogAdminApiService);
    const machineTypeApi = inject(MachineTypeApiService);
    const snackBar = inject(MatSnackBar);

    const replaceMachineType = (updated: MachineType): void =>
      patchState(store, {
        machineTypes: store.machineTypes().map(mt => (mt.id === updated.id ? updated : mt)),
      });

    return {
      loadAll: rxMethod<void>(
        pipe(
          tap(() => patchState(store, {loading: true, error: null})),
          switchMap(() =>
            forkJoin({
              families: catalogApi.listFamilies(),
              machineTypes: machineTypeApi.list(),
              zoneList: catalogApi.listZones(),
            }).pipe(
              tapResponse({
                next: ({families, machineTypes, zoneList}) =>
                  patchState(store, {
                    families,
                    machineTypes,
                    zones: zoneList.zones,
                    curatedShapes: zoneList.curatedShapes,
                    loading: false,
                  }),
                error: err =>
                  patchState(store, {
                    loading: false,
                    error: getErrorMessage(err, 'Failed to load catalog'),
                  }),
              })
            )
          )
        )
      ),

      /**
       * Curates one family. The write is optimistic on the row, then both
       * listings are reloaded: admitting a family makes the server fetch its
       * shapes from Compute Engine, and the shape matrix that appears is
       * discovery's answer, not something this store could have predicted. The
       * served list rides along because the per-shape curation acts resolve
       * against it. Left stale, a new family's exclusion clicks would land on
       * nothing.
       */
      patchFamily: rxMethod<{family: AdminFamily; patch: MachineFamilyPatch}>(
        pipe(
          concatMap(({family, patch}) => {
            const snapshot = store.families();
            patchState(store, {
              families: snapshot.map(f => (f.family === family.family ? {...f, ...patch} : f)),
            });
            return catalogApi.patchFamily(family.family, patch).pipe(
              switchMap(() =>
                forkJoin({
                  families: catalogApi.listFamilies(),
                  machineTypes: machineTypeApi.list(),
                })
              ),
              tapResponse({
                next: ({families, machineTypes}) => patchState(store, {families, machineTypes}),
                error: err => {
                  patchState(store, {families: snapshot});
                  fail(snackBar, `Failed to update the ${family.family} family`, err);
                },
              })
            );
          })
        )
      ),

      patchMachineType: rxMethod<{machineType: MachineType; patch: MachineTypeAdminPatch}>(
        pipe(
          concatMap(({machineType, patch}) => {
            const machineSnapshot = store.machineTypes();
            const familySnapshot = store.families();
            // Optimistic on both readings of the same shape: the served entry
            // and the family's matrix row. Electing a default also clears the
            // previous one, mirroring the backend invariant.
            patchState(store, {
              machineTypes: machineSnapshot.map(mt => {
                if (mt.id === machineType.id) {
                  return {
                    ...mt,
                    isEnabled: patch.isEnabled ?? mt.isEnabled,
                    isDefault: patch.isDefault ?? mt.isDefault,
                  };
                }
                if (patch.isDefault && mt.isDefault) {
                  return {...mt, isDefault: false};
                }
                return mt;
              }),
              families: familySnapshot.map(f => ({
                ...f,
                shapes: f.shapes.map(shape => applyShapePatch(shape, machineType.id, patch)),
              })),
            });
            return catalogApi.patchMachineType(machineType.id, patch).pipe(
              tapResponse({
                next: updated => replaceMachineType(updated),
                error: err => {
                  patchState(store, {
                    machineTypes: machineSnapshot,
                    families: familySnapshot,
                  });
                  fail(snackBar, `Failed to update ${machineType.id}`, err);
                },
              })
            );
          })
        )
      ),

      // Enablement is one stored set, so a toggle sends the whole set it wants
      // and the server answers with what it stored. concatMap serializes the
      // toggles: two in flight at once would each carry a set built before the
      // other landed, and the second would undo the first.
      //
      // onStored runs once the server has answered, for callers whose own reads
      // are derived from the enabled set: refetching them before the write
      // lands would re-render the state the toggle just left.
      setZoneEnabled: rxMethod<{zone: AdminZone; isEnabled: boolean; onStored?: () => void}>(
        pipe(
          concatMap(({zone, isEnabled, onStored}) => {
            const snapshot = store.zones();
            const wanted = snapshot
              .filter(z => (z.zone === zone.zone ? isEnabled : z.isEnabled))
              .map(z => z.zone);
            patchState(store, {
              zones: snapshot.map(z => (z.zone === zone.zone ? {...z, isEnabled} : z)),
            });
            return catalogApi.setEnabledZones(wanted).pipe(
              tapResponse({
                next: stored => {
                  patchState(store, {
                    zones: store.zones().map(z => ({...z, isEnabled: stored.includes(z.zone)})),
                  });
                  onStored?.();
                },
                error: err => {
                  patchState(store, {zones: snapshot});
                  fail(snackBar, `Failed to update ${zone.zone}`, err);
                },
              })
            );
          })
        )
      ),
    };
  })
);
