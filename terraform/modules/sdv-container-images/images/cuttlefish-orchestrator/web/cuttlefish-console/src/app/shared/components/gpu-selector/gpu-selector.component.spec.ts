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
import type {MachineType} from '../../../machine-types';
import type {GpuSelection} from './gpu-selector.component';
import {GpuSelectorComponent} from './gpu-selector.component';

function machineType(
  overrides: Partial<MachineType> & Pick<MachineType, 'id' | 'gpuMode'>
): MachineType {
  return {
    family: 'n1',
    name: overrides.id,
    category: 'general_purpose',
    architecture: 'x86',
    vcpus: 8,
    memoryGb: 32,
    fit: 3,
    gpuSupport: true,
    maxGpus: 4,
    defaultGpuCount: 0,
    integratedGpuCount: 0,
    isDefault: false,
    isEnabled: true,
    sortOrder: 0,
    ...overrides,
  };
}

describe('GpuSelectorComponent', () => {
  let component: GpuSelectorComponent;
  let fixture: ComponentFixture<GpuSelectorComponent>;
  let emitted: GpuSelection[];

  beforeEach(async () => {
    await TestBed.configureTestingModule({imports: [GpuSelectorComponent]}).compileComponents();
    fixture = TestBed.createComponent(GpuSelectorComponent);
    component = fixture.componentInstance;
    emitted = [];
    component.selectionChange.subscribe(s => emitted.push(s));
  });

  function setMachine(mt: MachineType | null): void {
    fixture.componentRef.setInput('machineType', mt);
    fixture.detectChanges();
  }

  it('reports the machine type gpu mode', () => {
    setMachine(machineType({id: 'e2', gpuMode: 'none'}));
    expect(component.gpuMode()).toBe('none');
    setMachine(machineType({id: 'g2', gpuMode: 'integrated'}));
    expect(component.gpuMode()).toBe('integrated');
  });

  it('is CPU-only by default and emits nothing until toggled (attachable)', () => {
    setMachine(machineType({id: 'n1', gpuMode: 'attachable', defaultGpuType: 'nvidia-tesla-t4'}));
    expect(component.attach()).toBe(false);
    expect(emitted).toHaveLength(0);
  });

  it('emits the default GPU when attach is toggled on, and clears it off', () => {
    setMachine(machineType({id: 'n1', gpuMode: 'attachable', defaultGpuType: 'nvidia-tesla-t4'}));
    component.onToggle(true);
    expect(emitted.at(-1)).toEqual({gpuType: 'nvidia-tesla-t4', gpuCount: 1});
    component.onToggle(false);
    expect(emitted.at(-1)).toEqual({gpuType: '', gpuCount: 0});
  });

  it('bounds valid counts by the machine max_gpus', () => {
    setMachine(
      machineType({id: 'n1', gpuMode: 'attachable', defaultGpuType: 'nvidia-tesla-t4', maxGpus: 2})
    );
    // T4 offers [1,2,4] but max_gpus=2 caps it.
    expect(component.validCounts()).toEqual([1, 2]);
  });

  it('resets the attach toggle when the machine type changes', () => {
    setMachine(machineType({id: 'n1-a', gpuMode: 'attachable', defaultGpuType: 'nvidia-tesla-t4'}));
    component.onToggle(true);
    expect(component.attach()).toBe(true);
    setMachine(machineType({id: 'n1-b', gpuMode: 'attachable', defaultGpuType: 'nvidia-tesla-t4'}));
    expect(component.attach()).toBe(false);
  });

  it('never emits an attachment for integrated machines', () => {
    setMachine(
      machineType({
        id: 'g2',
        gpuMode: 'integrated',
        integratedGpuType: 'nvidia-l4',
        integratedGpuCount: 1,
      })
    );
    // No toggle is rendered; a forced emit still yields no attachment.
    component.onCountChange(2);
    expect(emitted.at(-1)).toEqual({gpuType: '', gpuCount: 0});
  });
});
