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

import type {Signal} from '@angular/core';
import {computed, effect, inject, untracked} from '@angular/core';
import {MatDialog} from '@angular/material/dialog';
import {MatSnackBar} from '@angular/material/snack-bar';
import type {Adb} from '@yume-chan/adb';
import {firstValueFrom} from 'rxjs';
import type {ConfirmDialogData} from '../../../shared/components';
import {ConfirmDialogComponent} from '../../../shared/components';
import type {InstanceTarget} from '../../models/instance-target';
import {AdbService} from './adb.service';
import type {AdbConnectionState} from './adb-session';

/** Options for {@link AdbPanel.run}: the toast text and an optional refresh. */
interface RunOptions {
  success: string;
  /** Fallback message when the thrown error is not an `Error`. */
  failure?: string;
  /** Runs after a successful op (e.g. reload the directory/package listing). */
  after?: () => void;
}

/**
 * The ADB plumbing every device tool panel (file browser, app manager, …)
 * repeats: a held session claim, a connected-device accessor, and the
 * connect → run → toast (→ reload) wrapper around each mutating operation.
 */
export interface AdbPanel {
  /** Reactive connection state for the panel's device. */
  readonly state: Signal<AdbConnectionState>;
  /** Resolves the claimed, connected `Adb`, or rejects if the session is gone. */
  connectedDevice(): Promise<Adb>;
  /** Connects, runs `op`, and toasts success or the error message. */
  run(op: (device: Adb) => Promise<unknown>, opts: RunOptions): Promise<void>;
  /** Opens the shared confirm dialog; resolves true only on confirm. */
  confirm(data: ConfirmDialogData): Promise<boolean>;
}

/**
 * Wires a device tool panel to its ADB session. Call in an injection context
 * (field initializer / constructor) with the panel's target signal: it acquires
 * a claim for the shown instance, released on target change and on destroy,
 * so one panel never tears down a session others still hold.
 */
export function injectAdbPanel(target: Signal<InstanceTarget>): AdbPanel {
  const adbService = inject(AdbService);
  const snackBar = inject(MatSnackBar);
  const dialog = inject(MatDialog);

  effect(onCleanup => {
    const t = target();
    const claim = untracked(() => adbService.acquire(t));
    onCleanup(() => claim.release());
  });

  const connectedDevice = (): Promise<Adb> => {
    const session = untracked(() => adbService.session(target())());
    if (!session) {
      return Promise.reject(new Error('ADB session not available'));
    }
    return session.ensureConnected();
  };

  return {
    // Re-reads the target so the state tracks the shown instance across changes.
    state: computed(() => adbService.state(target())()),
    connectedDevice,
    async run(
      op: (device: Adb) => Promise<unknown>,
      {success, failure = 'Operation failed', after}
    ) {
      try {
        await op(await connectedDevice());
        snackBar.open(success, 'Close', {duration: 3000});
        after?.();
      } catch (err) {
        const message = err instanceof Error ? err.message : failure;
        snackBar.open(`Error: ${message}`, 'Close', {duration: 5000});
      }
    },
    confirm(data) {
      return firstValueFrom(
        dialog
          .open<ConfirmDialogComponent, ConfirmDialogData, boolean>(ConfirmDialogComponent, {data})
          .afterClosed()
      ).then(confirmed => confirmed === true);
    },
  };
}
