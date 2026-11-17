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
 * Shell-execution primitives over a connected `Adb` device.
 *
 * Plain functions so features compose only what they use; get the device
 * from `AdbSession.ensureConnected()`.
 */

import type {Adb, AdbNoneProtocolPtyProcess, AdbShellProtocolPtyProcess} from '@yume-chan/adb';

export interface AdbExecResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

/**
 * Executes a shell command and returns the full result.
 *
 * Prefers shell protocol (Android 7+). The none-protocol fallback combines
 * stdout/stderr and cannot report exit codes.
 */
export async function execFull(device: Adb, command: string): Promise<AdbExecResult> {
  const shellProtocol = device.subprocess.shellProtocol;
  if (shellProtocol) {
    return shellProtocol.spawnWaitText(command);
  }

  const output = await device.subprocess.noneProtocol.spawnWaitText(command);
  return {stdout: output, stderr: '', exitCode: 0};
}

/** Executes a shell command and returns stdout only. */
export async function exec(device: Adb, command: string): Promise<string> {
  const result = await execFull(device, command);
  return result.stdout;
}

/** Creates an interactive shell session (PTY). */
export function shellPty(
  device: Adb,
  options?: {command?: string; terminalType?: string}
): Promise<AdbShellProtocolPtyProcess | AdbNoneProtocolPtyProcess> {
  const shellProtocol = device.subprocess.shellProtocol;
  if (shellProtocol) {
    return shellProtocol.pty({
      command: options?.command,
      terminalType: options?.terminalType ?? 'xterm-256color',
    });
  }

  return device.subprocess.noneProtocol.pty(options?.command);
}

/** Takes a screenshot; screencap outputs PNG. */
export async function screenshot(device: Adb): Promise<Uint8Array> {
  const shellProtocol = device.subprocess.shellProtocol;
  if (shellProtocol) {
    const result = await shellProtocol.spawnWait('screencap -p');
    return result.stdout;
  }

  return device.subprocess.noneProtocol.spawnWait('screencap -p');
}
