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

import type {ElementRef} from '@angular/core';
import {
  afterNextRender,
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import {MatTooltipModule} from '@angular/material/tooltip';
import {FitAddon} from '@xterm/addon-fit';
import {SearchAddon} from '@xterm/addon-search';
import {WebglAddon} from '@xterm/addon-webgl';
import {Terminal} from '@xterm/xterm';

import type {
  MaybeConsumable,
  WritableStreamDefaultWriter,
  ReadableStream as YumeReadableStream,
  WritableStream as YumeWritableStream,
} from '@yume-chan/stream-extra';
import {LoggingService} from '../../../shared/services';
import type {AdbClaim} from '../../services/adb/adb.service';
import {AdbService} from '../../services/adb/adb.service';
import {shellPty} from '../../services/adb/adb-exec';
import {TERMINAL_OPTIONS} from '../terminal-config';

interface ShellPtyProcess {
  readonly input: YumeWritableStream<MaybeConsumable<Uint8Array>>;
  readonly output: YumeReadableStream<Uint8Array>;
  readonly exited: Promise<number>;
  resize?(rows: number, cols: number): Promise<void>;
  sigint(): Promise<void>;
  kill(): void | Promise<void>;
}

/**
 * Interactive shell terminal using xterm.js and ADB.
 *
 * Features:
 * - Full PTY support with shell protocol
 * - Terminal resize handling
 * - Copy/paste support
 * - SIGINT (Ctrl+C) handling
 * - Connection status indicator
 *
 * @example
 * ```html
 * <app-shell-terminal [deviceId]="device.id" />
 * ```
 */
@Component({
  selector: 'app-shell-terminal',
  imports: [MatIconModule, MatButtonModule, MatTooltipModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="shell-container">
      <!-- Toolbar -->
      <div class="toolbar">
        <span class="title">Shell</span>

        <div class="spacer"></div>

        <button type="button" mat-icon-button (click)="clear()" matTooltip="Clear" aria-label="Clear">
          <mat-icon>delete</mat-icon>
        </button>

        <button type="button"
          mat-icon-button
          (click)="toggleConnection()"
          [matTooltip]="connected() ? 'Disconnect' : 'Connect'" [attr.aria-label]="connected() ? 'Disconnect' : 'Connect'">
          <mat-icon>{{ connected() ? 'stop' : 'play_circle' }}</mat-icon>
        </button>
      </div>

      <!-- Terminal -->
      <div class="terminal-wrapper" #terminalContainer></div>

      <!-- Status -->
      <div class="status-bar">
        <span class="status-indicator" [class.connected]="connected()"></span>
        <span>{{ connected() ? 'Connected' : 'Disconnected' }}</span>
        @if (exitCode() !== null) {
          <span class="exit-code">Exit: {{ exitCode() }}</span>
        }
      </div>
    </div>
  `,
  styles: `
      :host {
        display: block;
        height: 100%;
      }

      .shell-container {
        display: flex;
        flex-direction: column;
        height: 100%;
        background: var(--cco-canvas);
      }

      .toolbar {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        background: var(--cco-surface);
        border-bottom: 1px solid var(--cco-border);
        flex-shrink: 0;
      }

      .title {
        color: var(--cco-text);
        font-weight: 500;
        font-size: 14px;
      }

      .spacer {
        flex: 1;
      }

      .toolbar button {
        color: var(--cco-text-secondary);
      }

      .toolbar button:hover {
        color: var(--cco-text);
      }

      .terminal-wrapper {
        flex: 1;
        min-height: 0;
        padding: 4px;
      }

      .status-bar {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 4px 12px;
        background: var(--cco-surface);
        border-top: 1px solid var(--cco-border);
        font-size: 11px;
        color: var(--cco-text-secondary);
        flex-shrink: 0;
      }

      .status-indicator {
        width: 8px;
        height: 8px;
        border-radius: 50%;
        background: var(--cco-text-disabled);
      }

      .status-indicator.connected {
        background: var(--cco-success);
      }

      .exit-code {
        margin-left: auto;
      }
    `,
})
export class ShellTerminalComponent {
  private readonly adbService = inject(AdbService);
  private readonly logger = inject(LoggingService);
  private readonly destroyRef = inject(DestroyRef);

  readonly deviceId = input.required<string>();
  readonly instance = input.required<string>();

  /** The Android VM this panel talks to; ADB sessions are per instance. */
  private readonly target = computed(() => ({
    deviceId: this.deviceId(),
    instance: this.instance(),
  }));

  readonly autoConnect = input(true);

  protected readonly terminalContainer =
    viewChild.required<ElementRef<HTMLDivElement>>('terminalContainer');

  protected readonly connected = signal(false);
  protected readonly exitCode = signal<number | null>(null);

  private terminal!: Terminal;
  private fitAddon!: FitAddon;
  private searchAddon!: SearchAddon;
  private resizeObserver?: ResizeObserver;
  private shellProcess: ShellPtyProcess | null = null;
  private inputWriter: WritableStreamDefaultWriter<Uint8Array> | null = null;
  private claim: AdbClaim | null = null;

  constructor() {
    // Shell + ADB claim follow the device; the cleanup tears down the old
    // shell on device change and on destroy. The initial connect happens in
    // afterNextRender once the terminal exists.
    effect(onCleanup => {
      this.deviceId();
      untracked(() => {
        if (this.terminal && this.autoConnect()) {
          void this.connect();
        }
      });
      onCleanup(() => {
        this.disconnect();
        this.claim?.release();
        this.claim = null;
      });
    });

    afterNextRender(() => {
      this.initTerminal();
      if (this.autoConnect()) {
        void this.connect();
      }
    });

    this.destroyRef.onDestroy(() => {
      this.resizeObserver?.disconnect();
      this.terminal?.dispose();
    });
  }

  private initTerminal(): void {
    this.terminal = new Terminal({
      ...TERMINAL_OPTIONS,
      cursorBlink: true,
      cursorStyle: 'block',
    });
    this.fitAddon = new FitAddon();
    this.searchAddon = new SearchAddon();

    this.terminal.loadAddon(this.fitAddon);
    this.terminal.loadAddon(this.searchAddon);

    // Try to load WebGL addon for better performance
    try {
      const webglAddon = new WebglAddon();
      webglAddon.onContextLoss(() => {
        webglAddon.dispose();
      });
      this.terminal.loadAddon(webglAddon);
    } catch {
      this.logger.warn('ShellTerminal', 'WebGL addon not available, using canvas renderer');
    }

    this.terminal.open(this.terminalContainer().nativeElement);
    this.fitAddon.fit();

    this.terminal.onData(data => {
      void this.sendInput(data);
    });

    this.terminal.onResize(({rows, cols}) => {
      void this.sendResize(rows, cols);
    });

    // Resize on container size change
    this.resizeObserver = new ResizeObserver(() => {
      this.fitAddon.fit();
    });
    this.resizeObserver.observe(this.terminalContainer().nativeElement);
  }

  async connect(): Promise<void> {
    if (this.connected()) {
      return;
    }

    try {
      this.terminal.writeln('\x1b[90mConnecting to shell...\x1b[0m');
      this.exitCode.set(null);

      this.claim ??= this.adbService.acquire(this.target());
      const device = await this.claim.session.ensureConnected();
      const process = (await shellPty(device)) as ShellPtyProcess;
      this.shellProcess = process;
      this.connected.set(true);

      const writer = process.input.getWriter() as WritableStreamDefaultWriter<Uint8Array>;
      this.inputWriter = writer;

      const {rows, cols} = this.terminal;
      await this.sendResize(rows, cols);

      await this.readShellOutput(process);
    } catch (error) {
      this.logger.error('ShellTerminal', 'Failed to connect', error);
      const message = error instanceof Error ? error.message : String(error);
      this.terminal.writeln(`\x1b[31mFailed to connect: ${message}\x1b[0m`);
      this.connected.set(false);
    }
  }

  private async readShellOutput(process: ShellPtyProcess): Promise<void> {
    const reader = process.output.getReader();

    try {
      while (true) {
        const {done, value} = await reader.read();
        if (done) {
          break;
        }

        this.terminal.write(value);
      }
    } catch (error) {
      this.logger.error('ShellTerminal', 'Output stream error', error);
    } finally {
      reader.releaseLock();
      this.connected.set(false);

      try {
        const code = await process.exited;
        this.exitCode.set(code);
        this.terminal.writeln(`\x1b[90m\r\nShell exited with code ${code}\x1b[0m`);
      } catch {
        // The shell was torn down before exiting cleanly; no exit code to show.
      }
    }
  }

  private async sendInput(data: string): Promise<void> {
    if (!(this.inputWriter && this.connected())) {
      return;
    }

    try {
      const encoded = new TextEncoder().encode(data);
      await this.inputWriter.write(encoded);
    } catch (error) {
      this.logger.error('ShellTerminal', 'Failed to send input', error);
    }
  }

  private async sendResize(rows: number, cols: number): Promise<void> {
    if (!(this.shellProcess?.resize && this.connected())) {
      return;
    }

    try {
      await this.shellProcess.resize(rows, cols);
    } catch (error) {
      this.logger.error('ShellTerminal', 'Failed to resize', error);
    }
  }

  disconnect(): void {
    if (this.inputWriter) {
      void this.inputWriter.close().catch(() => {
        // Best-effort close; the stream may already be gone.
      });
    }
    this.inputWriter = null;

    // Kill shell process
    if (this.shellProcess) {
      const result = this.shellProcess.kill();
      if (result instanceof Promise) {
        void result.catch(() => {
          // Best-effort kill; the process may already have exited.
        });
      }
      this.shellProcess = null;
    }

    this.connected.set(false);
  }

  toggleConnection(): void {
    if (this.connected()) {
      this.disconnect();
    } else {
      void this.connect();
    }
  }

  clear(): void {
    this.terminal.clear();
  }

  /**
   * Sends SIGINT (Ctrl+C) to the shell.
   */
  async sendSigint(): Promise<void> {
    if (!(this.shellProcess && this.connected())) {
      return;
    }

    try {
      await this.shellProcess.sigint();
    } catch (error) {
      this.logger.error('ShellTerminal', 'Failed to send SIGINT', error);
    }
  }

  /**
   * Pastes text into the terminal.
   */
  paste(text: string): void {
    void this.sendInput(text);
  }
}
