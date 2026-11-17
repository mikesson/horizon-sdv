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

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {MachineConfigOutput, MachineType} from '../../../machine-types';
import {ShapePickerComponent} from './shape-picker.component';

function machineType(
  overrides: Partial<MachineType> & Pick<MachineType, 'id' | 'family' | 'category'>
): MachineType {
  return {
    name: overrides.id,
    architecture: 'x86',
    vcpus: 8,
    memoryGb: 32,
    fit: 3,
    gpuSupport: false,
    maxGpus: 0,
    defaultGpuCount: 0,
    gpuMode: 'none',
    integratedGpuCount: 0,
    isDefault: false,
    isEnabled: true,
    sortOrder: 0,
    ...overrides,
  };
}

describe('ShapePickerComponent', () => {
  let component: ShapePickerComponent;
  let fixture: ComponentFixture<ShapePickerComponent>;

  const catalog: MachineType[] = [
    machineType({
      id: 'n1-standard-8',
      family: 'n1',
      category: 'general_purpose',
      cpuType: 'Intel Skylake',
      gpuMode: 'attachable',
      gpuSupport: true,
      maxGpus: 4,
      defaultGpuType: 'nvidia-tesla-t4',
      isDefault: true,
      sortOrder: 1,
    }),
    machineType({
      id: 'n1-standard-16',
      family: 'n1',
      category: 'general_purpose',
      cpuType: 'Intel Skylake',
      vcpus: 16,
      memoryGb: 64,
      fit: 7,
      gpuMode: 'attachable',
      gpuSupport: true,
      maxGpus: 4,
      defaultGpuType: 'nvidia-tesla-t4',
      sortOrder: 1,
    }),
    machineType({
      id: 'n1-highmem-16',
      family: 'n1',
      category: 'general_purpose',
      cpuType: 'Intel Skylake',
      vcpus: 16,
      memoryGb: 104,
      fit: 12,
      sortOrder: 1,
    }),
    machineType({
      id: 'c4a-standard-8-metal',
      family: 'c4a',
      category: 'compute_optimized',
      architecture: 'arm',
      sortOrder: 2,
    }),
    machineType({
      id: 'n1-withheld-8',
      family: 'n1',
      category: 'general_purpose',
      isEnabled: false,
      sortOrder: 1,
    }),
  ];

  beforeEach(async () => {
    await TestBed.configureTestingModule({imports: [ShapePickerComponent]}).compileComponents();
    fixture = TestBed.createComponent(ShapePickerComponent);
    component = fixture.componentInstance;
    fixture.componentRef.setInput('machineTypes', catalog);
    fixture.detectChanges();
  });

  // The family is the first question now. A regression to a flat list would
  // still pass a "n1-standard-8 is offered" assertion, so what is asserted is
  // that the choice is one entry per series rather than one per shape.
  it('offers one entry per family, in the catalog display order', () => {
    expect(component.families().map(option => option.family)).toEqual(['n1', 'c4a']);
  });

  it('selects the catalog default on load', () => {
    expect(component.selectedId()).toBe('n1-standard-8');
  });

  // The variant is read off the name with the width taken out, so a metal or
  // highmem shape lands under its own heading rather than beside a standard of
  // the same size.
  it('groups the family it is showing by variant', () => {
    expect(component.variantGroups().map(group => group.variant)).toEqual(['highmem', 'standard']);
  });

  it('shows only the selected family, and only what the catalog offers', () => {
    const ids = component.variantGroups().flatMap(group => group.types.map(mt => mt.id));
    // The withheld shape shares its family with the offered ones, so the
    // positive assertion is what stops an empty list satisfying the exclusion.
    expect(ids).toContain('n1-standard-8');
    expect(ids).not.toContain('n1-withheld-8');
    expect(ids).not.toContain('c4a-standard-8-metal');
  });

  it('moves to the first shape of a family the user switches to', () => {
    const emitted: MachineConfigOutput[] = [];
    component.configChange.subscribe(config => emitted.push(config));

    component.selectFamily('c4a');

    expect(component.selectedId()).toBe('c4a-standard-8-metal');
    expect(emitted.at(-1)).toEqual({
      machineType: 'c4a-standard-8-metal',
      gpuType: '',
      gpuCount: 0,
    });
  });

  it('emits a CPU-only config on selection, because GPUs are opt-in', () => {
    const emitted: MachineConfigOutput[] = [];
    component.configChange.subscribe(config => emitted.push(config));

    component.selectMachineType(catalog[1]);

    expect(emitted.at(-1)).toEqual({machineType: 'n1-standard-16', gpuType: '', gpuCount: 0});
  });

  it('re-emits with the GPU the selector attached', () => {
    const emitted: MachineConfigOutput[] = [];
    component.configChange.subscribe(config => emitted.push(config));

    component.selectMachineType(catalog[0]);
    component.onGpuChange({gpuType: 'nvidia-tesla-t4', gpuCount: 2});

    expect(emitted.at(-1)).toEqual({
      machineType: 'n1-standard-8',
      gpuType: 'nvidia-tesla-t4',
      gpuCount: 2,
    });
  });

  // A machine picked before the GPU was attached must not carry that GPU to the
  // next machine: the attachment is a property of the shape it was made on.
  it('drops an attached GPU when another shape is chosen', () => {
    const emitted: MachineConfigOutput[] = [];
    component.configChange.subscribe(config => emitted.push(config));

    component.selectMachineType(catalog[0]);
    component.onGpuChange({gpuType: 'nvidia-tesla-t4', gpuCount: 2});
    component.selectMachineType(catalog[1]);

    expect(emitted.at(-1)).toEqual({machineType: 'n1-standard-16', gpuType: '', gpuCount: 0});
  });
});
