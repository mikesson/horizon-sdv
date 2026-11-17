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

import {provideZonelessChangeDetection} from '@angular/core';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {MAT_DIALOG_DATA, MatDialogRef} from '@angular/material/dialog';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {HostExtendDialogData} from './host-extend-dialog.component';
import {HostExtendDialogComponent} from './host-extend-dialog.component';

describe('HostExtendDialogComponent', () => {
  const dialogRef = {close: vi.fn()};
  let fixture: ComponentFixture<HostExtendDialogComponent>;

  function render(data: HostExtendDialogData): HTMLElement {
    TestBed.configureTestingModule({
      imports: [HostExtendDialogComponent],
      providers: [
        provideZonelessChangeDetection(),
        {provide: MatDialogRef, useValue: dialogRef},
        {provide: MAT_DIALOG_DATA, useValue: data},
      ],
    });
    fixture = TestBed.createComponent(HostExtendDialogComponent);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  function type(root: HTMLElement, value: string): void {
    const input = root.querySelector<HTMLInputElement>('input');
    if (!input) {
      throw new Error('no hours input rendered');
    }
    input.value = value;
    input.dispatchEvent(new Event('input'));
    fixture.detectChanges();
  }

  function extendButton(root: HTMLElement): HTMLButtonElement {
    const btn = Array.from(root.querySelectorAll('button')).find(
      b => (b.textContent ?? '').trim() === 'Extend'
    );
    if (!btn) {
      throw new Error('no extend button rendered');
    }
    return btn;
  }

  beforeEach(() => {
    vi.clearAllMocks();
  });

  const data: HostExtendDialogData = {
    hostName: 'test-host',
    expiresAt: new Date('2099-01-01T12:00:00Z'),
  };

  it('closes with the hours entered', () => {
    const root = render(data);
    type(root, '48');
    extendButton(root).click();
    expect(dialogRef.close).toHaveBeenCalledWith(48);
  });

  it.each(['0', '169', '1.5', ''])('blocks %j, which the server would 422', value => {
    const root = render(data);
    type(root, value);
    expect(extendButton(root).disabled).toBe(true);
    expect(root.textContent).toContain('Enter a whole number from 1 to 168');
    expect(root.textContent).not.toContain('New expiry');
  });

  it('accepts both ends of the range', () => {
    const root = render(data);
    type(root, '1');
    expect(extendButton(root).disabled).toBe(false);
    type(root, '168');
    expect(extendButton(root).disabled).toBe(false);
  });

  it('previews the expiry counted from the current one', () => {
    const root = render(data);
    type(root, '48');
    expect(root.querySelector('.preview')?.textContent).toContain('Jan 3, 2099');
  });

  it('warns when the new expiry passes the VM stop time', () => {
    const root = render({...data, vmTerminatesAt: new Date('2099-01-02T12:00:00Z')});
    type(root, '12');
    expect(root.textContent).not.toContain('passes the VM stop time');
    type(root, '48');
    expect(root.textContent).toContain('passes the VM stop time');
  });

  it('warns when the new expiry lands inside the 15-minute grace before the VM stop', () => {
    const root = render({...data, vmTerminatesAt: new Date('2099-01-02T12:10:00Z')});
    type(root, '24');
    expect(root.textContent).toContain('passes the VM stop time');
    type(root, '23');
    expect(root.textContent).not.toContain('passes the VM stop time');
  });
});
