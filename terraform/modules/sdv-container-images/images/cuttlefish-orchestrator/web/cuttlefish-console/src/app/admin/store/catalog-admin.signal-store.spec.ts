/**
 * Copyright 2026 Google LLC
 * SPDX-License-Identifier: Apache-2.0
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

import {provideZonelessChangeDetection} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {MatSnackBar} from '@angular/material/snack-bar';
import {of, Subject, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {MachineType} from '../../machine-types/models';
import {MachineTypeApiService} from '../../machine-types/services';
import type {AdminZone} from '../models/catalog-admin.model';
import type {AdminFamily} from '../models/machine-family.model';
import {CatalogAdminApiService} from '../services/catalog-admin-api.service';
import {CatalogAdminStore} from './catalog-admin.signal-store';

function makeMachineType(overrides: Partial<MachineType> = {}): MachineType {
  return {
    id: 'n1-standard-8',
    family: 'n1',
    name: 'N1 Standard 8',
    category: 'general_purpose',
    architecture: 'x86',
    vcpus: 8,
    memoryGb: 30,
    fit: 3,
    gpuSupport: false,
    gpuMode: 'none',
    maxGpus: 0,
    defaultGpuType: '',
    defaultGpuCount: 0,
    integratedGpuCount: 0,
    isDefault: true,
    isEnabled: true,
    sortOrder: 100,
    ...overrides,
  };
}

function makeFamily(overrides: Partial<AdminFamily> = {}): AdminFamily {
  return {
    family: 'n1',
    name: 'N1 General Purpose',
    category: 'general_purpose',
    architecture: 'x86',
    gpuMode: 'none',
    maxGpus: 0,
    defaultGpuType: '',
    defaultGpuCount: 0,
    isEnabled: true,
    sortOrder: 10,
    shapeCount: 2,
    viableCount: 2,
    shapes: [
      {
        machineType: 'n1-standard-8',
        vcpus: 8,
        memoryGb: 30,
        fit: 3,
        viable: true,
        isDefault: true,
        isExcluded: false,
        maxDevicesOverride: 0,
      },
      {
        machineType: 'n1-standard-16',
        vcpus: 16,
        memoryGb: 60,
        fit: 6,
        viable: true,
        isDefault: false,
        isExcluded: false,
        maxDevicesOverride: 0,
      },
    ],
    ...overrides,
  };
}

function makeZone(overrides: Partial<AdminZone> = {}): AdminZone {
  return {
    zone: 'europe-west4-a',
    region: 'europe-west4',
    status: 'UP',
    isEnabled: true,
    offeredShapes: ['n1-standard-8'],
    shapesReported: true,
    ...overrides,
  };
}

describe('CatalogAdminStore', () => {
  const api = {
    listFamilies: vi.fn(),
    listZones: vi.fn(),
    patchFamily: vi.fn(),
    patchMachineType: vi.fn(),
    setEnabledZones: vi.fn(),
  };
  const machineTypeApi = {
    list: vi.fn(),
  };
  const snackBar = {
    open: vi.fn(),
  };

  let store: InstanceType<typeof CatalogAdminStore>;

  beforeEach(() => {
    vi.clearAllMocks();
    api.listFamilies.mockReturnValue(of([makeFamily()]));
    api.listZones.mockReturnValue(
      of({
        zones: [
          makeZone({zone: 'europe-west4-a', isEnabled: true}),
          makeZone({zone: 'us-central1-a', region: 'us-central1', isEnabled: false}),
        ],
        curatedShapes: ['n1-standard-8'],
      })
    );
    machineTypeApi.list.mockReturnValue(
      of([
        makeMachineType({id: 'n1-standard-8', isDefault: true}),
        makeMachineType({id: 'n1-standard-16', vcpus: 16, isDefault: false}),
      ])
    );

    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        CatalogAdminStore,
        {provide: CatalogAdminApiService, useValue: api},
        {provide: MachineTypeApiService, useValue: machineTypeApi},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
    store = TestBed.inject(CatalogAdminStore);
  });

  it('loads families, machine types, and zones together via loadAll()', () => {
    store.loadAll();

    expect(store.loading()).toBe(false);
    expect(store.error()).toBeNull();
    expect(store.families().length).toBe(1);
    expect(store.machineTypes().length).toBe(2);
    expect(store.zones().map(z => z.zone)).toEqual(['europe-west4-a', 'us-central1-a']);
    expect(store.curatedShapes()).toEqual(['n1-standard-8']);
  });

  it('records load error when any catalog source fails', () => {
    api.listFamilies.mockReturnValue(throwError(() => new Error('catalog offline')));

    store.loadAll();

    expect(store.loading()).toBe(false);
    expect(store.error()).toContain('catalog offline');
  });

  it('applies optimistic family patch and reloads shapes on success', () => {
    store.loadAll();
    const family = store.families()[0];
    const updatedFamily = makeFamily({isEnabled: false});
    api.patchFamily.mockReturnValue(of(updatedFamily));
    api.listFamilies.mockReturnValue(of([updatedFamily]));

    store.patchFamily({family, patch: {isEnabled: false}});

    expect(api.patchFamily).toHaveBeenCalledWith('n1', {isEnabled: false});
    expect(store.families()[0].isEnabled).toBe(false);
    expect(machineTypeApi.list).toHaveBeenCalledTimes(2);
  });

  it('reverts optimistic family change and shows snackbar on patch failure', () => {
    store.loadAll();
    const family = store.families()[0];
    api.patchFamily.mockReturnValue(throwError(() => new Error('forbidden')));

    store.patchFamily({family, patch: {isEnabled: false}});

    expect(store.families()[0].isEnabled).toBe(true);
    expect(snackBar.open).toHaveBeenCalled();
  });

  it('promotes a new default shape across machineTypes and family shapes', () => {
    store.loadAll();
    const target = store.machineTypes().find(m => m.id === 'n1-standard-16');
    if (!target) {
      throw new Error('expected n1-standard-16 machine type');
    }
    const promoted = makeMachineType({id: 'n1-standard-16', vcpus: 16, isDefault: true});
    api.patchMachineType.mockReturnValue(of(promoted));

    store.patchMachineType({machineType: target, patch: {isDefault: true}});

    expect(api.patchMachineType).toHaveBeenCalledWith('n1-standard-16', {isDefault: true});
    expect(store.machineTypes().find(m => m.id === 'n1-standard-8')?.isDefault).toBe(false);
    expect(store.machineTypes().find(m => m.id === 'n1-standard-16')?.isDefault).toBe(true);
    const shapes = store.families()[0].shapes;
    expect(shapes.find(s => s.machineType === 'n1-standard-8')?.isDefault).toBe(false);
    expect(shapes.find(s => s.machineType === 'n1-standard-16')?.isDefault).toBe(true);
  });

  it('reverts both machineTypes and family shapes when patchMachineType fails', () => {
    store.loadAll();
    const target = store.machineTypes().find(m => m.id === 'n1-standard-16');
    if (!target) {
      throw new Error('expected n1-standard-16 machine type');
    }
    api.patchMachineType.mockReturnValue(throwError(() => new Error('conflict')));

    store.patchMachineType({machineType: target, patch: {isDefault: true}});

    expect(store.machineTypes().find(m => m.id === 'n1-standard-8')?.isDefault).toBe(true);
    expect(store.families()[0].shapes.find(s => s.machineType === 'n1-standard-8')?.isDefault).toBe(
      true
    );
    expect(snackBar.open).toHaveBeenCalled();
  });

  it('serializes zone toggles, syncs returned enabled zones, and invokes onStored', () => {
    store.loadAll();
    const usZone = store.zones().find(z => z.zone === 'us-central1-a');
    const euZone = store.zones().find(z => z.zone === 'europe-west4-a');
    if (!(usZone && euZone)) {
      throw new Error('expected configured zones');
    }
    const first = new Subject<string[]>();
    const second = new Subject<string[]>();
    api.setEnabledZones.mockReturnValueOnce(first).mockReturnValueOnce(second);
    const onStored = vi.fn();

    store.setZoneEnabled({zone: usZone, isEnabled: true, onStored});
    store.setZoneEnabled({zone: euZone, isEnabled: false});

    expect(api.setEnabledZones).toHaveBeenCalledTimes(1);
    expect(api.setEnabledZones).toHaveBeenCalledWith(['europe-west4-a', 'us-central1-a']);

    first.next(['europe-west4-a', 'us-central1-a']);
    first.complete();
    expect(onStored).toHaveBeenCalledTimes(1);

    expect(api.setEnabledZones).toHaveBeenCalledTimes(2);
    expect(api.setEnabledZones).toHaveBeenNthCalledWith(2, ['us-central1-a']);
    second.next(['us-central1-a']);
    second.complete();

    expect(store.zones().find(z => z.zone === 'europe-west4-a')?.isEnabled).toBe(false);
    expect(store.zones().find(z => z.zone === 'us-central1-a')?.isEnabled).toBe(true);
  });

  it('reverts optimistic zone update on setEnabledZones error', () => {
    store.loadAll();
    const usZone = store.zones().find(z => z.zone === 'us-central1-a');
    if (!usZone) {
      throw new Error('expected us-central1-a zone');
    }
    api.setEnabledZones.mockReturnValue(throwError(() => new Error('policy denied')));

    store.setZoneEnabled({zone: usZone, isEnabled: true});

    expect(store.zones().find(z => z.zone === 'us-central1-a')?.isEnabled).toBe(false);
    expect(snackBar.open).toHaveBeenCalled();
  });
});
