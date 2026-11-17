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

/**
 * The count-versus-ceiling rule is enforced twice, here and in the handler,
 * because the dialog cannot know what the server will accept and the server
 * cannot grey out a button.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {MAT_DIALOG_DATA, MatDialogRef} from '@angular/material/dialog';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {AdminFamily} from '../../models/machine-family.model';
import {GpuPolicyDialogComponent} from './gpu-policy-dialog.component';

function attachable(overrides: Partial<AdminFamily> = {}): AdminFamily {
  const base: Partial<AdminFamily> = {
    family: 'n1',
    name: 'N1 General Purpose',
    gpuMode: 'attachable',
    maxGpus: 4,
    defaultGpuType: 'nvidia-tesla-t4',
    defaultGpuCount: 0,
    ...overrides,
  };
  return base as AdminFamily;
}

describe('GpuPolicyDialogComponent', () => {
  const dialogRef = {close: vi.fn()};

  function render(data: AdminFamily): ComponentFixture<GpuPolicyDialogComponent> {
    TestBed.configureTestingModule({
      providers: [
        {provide: MAT_DIALOG_DATA, useValue: data},
        {provide: MatDialogRef, useValue: dialogRef},
      ],
    });
    const fixture = TestBed.createComponent(GpuPolicyDialogComponent);
    fixture.detectChanges();
    return fixture;
  }

  beforeEach(() => {
    vi.clearAllMocks();
    TestBed.resetTestingModule();
  });

  it('seeds the form from the stored policy', () => {
    const root = render(attachable({maxGpus: 2, defaultGpuCount: 1})).nativeElement as HTMLElement;
    const count = root.querySelector<HTMLInputElement>('input[type="number"]');
    expect(count?.value).toBe('1');
  });

  it('refuses a default count above the ceiling', () => {
    const fixture = render(attachable({maxGpus: 2, defaultGpuCount: 1}));
    const root = fixture.nativeElement as HTMLElement;
    const count = root.querySelector<HTMLInputElement>('input[type="number"]');
    if (!count) {
      throw new Error('the default-count field did not render');
    }

    count.value = '3';
    count.dispatchEvent(new Event('input'));
    fixture.detectChanges();

    const update = Array.from(root.querySelectorAll('button')).find(
      b => b.textContent?.trim() === 'Update'
    );
    expect(update?.disabled).toBe(true);
    expect(root.textContent).toContain('must be between 0 and the maximum');
  });

  it('closes with the edited policy', () => {
    const fixture = render(attachable({maxGpus: 4, defaultGpuCount: 0}));
    const root = fixture.nativeElement as HTMLElement;
    const count = root.querySelector<HTMLInputElement>('input[type="number"]');
    if (!count) {
      throw new Error('the default-count field did not render');
    }

    count.value = '2';
    count.dispatchEvent(new Event('input'));
    fixture.detectChanges();

    Array.from(root.querySelectorAll('button'))
      .find(b => b.textContent?.trim() === 'Update')
      ?.click();

    expect(dialogRef.close).toHaveBeenCalledWith({
      maxGpus: 4,
      defaultGpuType: 'nvidia-tesla-t4',
      defaultGpuCount: 2,
    });
  });
});
