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
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';

const terminalInstances: Array<{
  open: ReturnType<typeof vi.fn>;
  loadAddon: ReturnType<typeof vi.fn>;
  onData: ReturnType<typeof vi.fn>;
  onResize: ReturnType<typeof vi.fn>;
  write: ReturnType<typeof vi.fn>;
  writeln: ReturnType<typeof vi.fn>;
  clear: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
  focus: ReturnType<typeof vi.fn>;
  rows: number;
  cols: number;
}> = [];

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    open = vi.fn();
    loadAddon = vi.fn();
    onData = vi.fn(() => ({dispose: vi.fn()}));
    onResize = vi.fn(() => ({dispose: vi.fn()}));
    write = vi.fn();
    writeln = vi.fn();
    clear = vi.fn();
    dispose = vi.fn();
    focus = vi.fn();
    rows = 24;
    cols = 80;
    constructor() {
      terminalInstances.push(this);
    }
  },
}));

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit = vi.fn();
    dispose = vi.fn();
  },
}));

vi.mock('@xterm/addon-search', () => ({
  SearchAddon: class {
    findNext = vi.fn();
    dispose = vi.fn();
  },
}));

vi.mock('@xterm/addon-webgl', () => ({
  WebglAddon: class {
    onContextLoss = vi.fn();
    dispose = vi.fn();
  },
}));

import {LoggingService} from '../../../shared/services';
import {AdbService} from '../../services/adb/adb.service';
import {ShellTerminalComponent} from './shell-terminal.component';

describe('ShellTerminalComponent', () => {
  const releaseClaim = vi.fn();
  const ensureConnected = vi.fn();
  const adbService = {
    acquire: vi.fn(() => ({
      session: {ensureConnected},
      release: releaseClaim,
    })),
  };
  const logger = {error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn()};

  let fixture: ComponentFixture<ShellTerminalComponent>;

  beforeEach(() => {
    vi.clearAllMocks();
    terminalInstances.length = 0;
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe = vi.fn();
        unobserve = vi.fn();
        disconnect = vi.fn();
      }
    );
  });

  afterEach(() => {
    fixture?.destroy();
    vi.unstubAllGlobals();
  });

  it('stays disconnected on mount when autoConnect is false and renders status bar', async () => {
    fixture = TestBed.configureTestingModule({
      imports: [ShellTerminalComponent],
      providers: [
        provideZonelessChangeDetection(),
        {provide: AdbService, useValue: adbService},
        {provide: LoggingService, useValue: logger},
      ],
    }).createComponent(ShellTerminalComponent);

    fixture.componentRef.setInput('deviceId', 'dev-1');
    fixture.componentRef.setInput('instance', 'cvd-1');
    fixture.componentRef.setInput('autoConnect', false);
    fixture.detectChanges();
    await fixture.whenStable();

    expect((fixture.nativeElement as HTMLElement).textContent).toContain('Disconnected');
    expect(terminalInstances.length).toBe(1);
    expect(terminalInstances[0].open).toHaveBeenCalled();
    expect(adbService.acquire).not.toHaveBeenCalled();
  });

  it('connects via AdbService + shellProtocol PTY, streams stdout, and records exitCode', async () => {
    let resolveExited!: (code: number) => void;
    const exited = new Promise<number>(resolve => {
      resolveExited = resolve;
    });
    const output = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('android-shell# '));
        controller.close();
      },
    });
    const writtenChunks: Uint8Array[] = [];
    const input = new WritableStream<Uint8Array>({
      write(chunk) {
        writtenChunks.push(chunk);
      },
    });
    const kill = vi.fn(() => Promise.resolve());
    const resize = vi.fn(() => Promise.resolve());
    const ptyMock = vi.fn(() => Promise.resolve({output, input, exited, resize, kill}));
    ensureConnected.mockResolvedValue({
      subprocess: {
        shellProtocol: {pty: ptyMock},
      },
    });

    fixture = TestBed.configureTestingModule({
      imports: [ShellTerminalComponent],
      providers: [
        provideZonelessChangeDetection(),
        {provide: AdbService, useValue: adbService},
        {provide: LoggingService, useValue: logger},
      ],
    }).createComponent(ShellTerminalComponent);

    fixture.componentRef.setInput('deviceId', 'dev-1');
    fixture.componentRef.setInput('instance', 'cvd-1');
    fixture.componentRef.setInput('autoConnect', true);
    fixture.detectChanges();

    resolveExited(7);
    await new Promise(resolve => setTimeout(resolve, 25));
    fixture.detectChanges();

    expect(adbService.acquire).toHaveBeenCalledWith({deviceId: 'dev-1', instance: 'cvd-1'});
    expect(ensureConnected).toHaveBeenCalled();
    expect(ptyMock).toHaveBeenCalled();
    expect(terminalInstances[0].write).toHaveBeenCalled();
    expect((fixture.nativeElement as HTMLElement).textContent).toContain('Exit: 7');

    fixture.destroy();
    expect(releaseClaim).toHaveBeenCalled();
  });
});
