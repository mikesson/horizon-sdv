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

import {
  ChangeDetectionStrategy,
  Component,
  provideZonelessChangeDetection,
  signal,
} from '@angular/core';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {AdbConnectionState} from '../../services/adb/adb-session';
import {AdbToolPanelComponent} from './adb-tool-panel.component';

@Component({
  selector: 'app-test-adb-tool-host',
  standalone: true,
  imports: [AdbToolPanelComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <app-adb-tool-panel
      [adbState]="adbState()"
      [isLoading]="isLoading()"
      [error]="error()"
      [waitingHint]="'Connect ADB first'"
      [loadingLabel]="'Working on it'"
      [dropLabel]="'Drop APK here'"
      (retry)="retries = retries + 1"
      (filesDropped)="dropped = $event"
    >
      <div toolbar>Toolbar Content</div>
      <div class="main-body">Panel Body</div>
      <div footer>Footer Content</div>
      <div overlay>Overlay Content</div>
    </app-adb-tool-panel>
  `,
})
class TestHostComponent {
  readonly adbState = signal<AdbConnectionState>('disconnected');
  readonly isLoading = signal(false);
  readonly error = signal<string | null>(null);
  retries = 0;
  dropped: File[] | null = null;
}

describe('AdbToolPanelComponent', () => {
  let fixture: ComponentFixture<TestHostComponent>;
  let host: TestHostComponent;

  beforeEach(() => {
    TestBed.configureTestingModule({
      imports: [TestHostComponent],
      providers: [provideZonelessChangeDetection()],
    });
    fixture = TestBed.createComponent(TestHostComponent);
    host = fixture.componentInstance;
    fixture.detectChanges();
  });

  function text(): string {
    return (fixture.nativeElement as HTMLElement).textContent ?? '';
  }

  it('shows waiting state when ADB is not connected', () => {
    expect(text()).toContain('Connect ADB first');
    expect(text()).not.toContain('Panel Body');
  });

  it('shows loading state with custom loadingLabel', () => {
    host.adbState.set('connected');
    host.isLoading.set(true);
    fixture.detectChanges();

    expect(text()).toContain('Working on it');
    expect(text()).not.toContain('Panel Body');
  });

  it('shows error banner and emits retry when Retry button is clicked', () => {
    host.adbState.set('connected');
    host.error.set('ADB transport lost');
    fixture.detectChanges();

    expect(text()).toContain('ADB transport lost');
    const retryBtn = (fixture.nativeElement as HTMLElement).querySelector('button');
    expect(retryBtn).not.toBeNull();
    retryBtn?.click();
    expect(host.retries).toBe(1);
  });

  it('projects toolbar, body, footer, overlay when connected and emits filesDropped on drop', () => {
    host.adbState.set('connected');
    fixture.detectChanges();

    expect(text()).toContain('Toolbar Content');
    expect(text()).toContain('Panel Body');
    expect(text()).toContain('Footer Content');
    expect(text()).toContain('Overlay Content');

    const root = fixture.nativeElement as HTMLElement;
    const target =
      root.querySelector('app-adb-tool-panel > div') ??
      root.querySelector('app-adb-tool-panel') ??
      root;

    const dragOver = new Event('dragover', {bubbles: true, cancelable: true});
    target.dispatchEvent(dragOver);
    expect(dragOver.defaultPrevented).toBe(true);
    fixture.detectChanges();
    expect(text()).toContain('Drop APK here');

    const file = new File(['abc'], 'app.apk', {type: 'application/vnd.android.package-archive'});
    const fakeFiles = [file] as unknown as FileList;
    const dropEvent = Object.assign(new Event('drop', {bubbles: true, cancelable: true}), {
      dataTransfer: {files: fakeFiles},
    });
    target.dispatchEvent(dropEvent);
    fixture.detectChanges();

    expect(dropEvent.defaultPrevented).toBe(true);
    expect(host.dropped).toEqual([file]);
    expect(text()).not.toContain('Drop APK here');
  });
});
