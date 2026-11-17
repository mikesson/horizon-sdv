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
 * Logcat access over a connected `Adb` device, using the binary (`-B`)
 * format: structured entries with exact nanosecond timestamps instead of
 * text lines that need format guessing.
 */

import type {Adb} from '@yume-chan/adb';
import {escapeArg} from '@yume-chan/adb';
import type {AndroidLogEntry} from '@yume-chan/android-bin';
import {AndroidLogPriority, deserializeAndroidLogEntry, LogcatFormat} from '@yume-chan/android-bin';
import type {ReadableStream as YumeReadableStream} from '@yume-chan/stream-extra';
import {BufferedTransformStream, WrapReadableStream, WritableStream} from '@yume-chan/stream-extra';
import type {LogEntry, LogLevel} from '../logcat-model';
import {exec} from './adb-exec';

/**
 * The buffers a developer can select. These strings are the exact lowercase
 * names `logcat -b` expects, passed to the command verbatim. (Do NOT route
 * through `@yume-chan/android-bin`'s `Logcat.joinLogId`: it capitalizes them
 * via the LogId enum keys (`Main,System,Crash`), which logcat rejects,
 * yielding an error and zero log output.)
 */
export type LogBufferId = 'main' | 'system' | 'crash' | 'radio' | 'events' | 'kernel';

export const LOG_BUFFERS: ReadonlyArray<{id: LogBufferId; label: string}> = [
  {id: 'main', label: 'Main'},
  {id: 'system', label: 'System'},
  {id: 'crash', label: 'Crash'},
  {id: 'radio', label: 'Radio'},
  {id: 'events', label: 'Events'},
  {id: 'kernel', label: 'Kernel'},
];

/** Matches `adb logcat`'s default buffer set. */
export const DEFAULT_LOG_BUFFERS: readonly LogBufferId[] = ['main', 'system', 'crash'];

const PRIORITY_TO_LEVEL: Partial<Record<AndroidLogPriority, LogLevel>> = {
  [AndroidLogPriority.Verbose]: 'V',
  [AndroidLogPriority.Debug]: 'D',
  [AndroidLogPriority.Info]: 'I',
  [AndroidLogPriority.Warn]: 'W',
  [AndroidLogPriority.Error]: 'E',
  [AndroidLogPriority.Fatal]: 'F',
};

const NANOSECONDS_PER_MILLISECOND = 1_000_000n;

export interface LogcatSource {
  buffers: readonly LogBufferId[];
  /** Restrict to one process (resolve with {@link resolveAppPid}). */
  pid?: number;
}

/**
 * Opens a live binary logcat stream. Cancel the reader to stop it.
 *
 * Spawns over the shell-protocol (shell_v2) service when available, falling
 * back to none-protocol. That is the same preference the rest of the ADB layer
 * uses, and what the working Files/Apps/Shell tabs use over the WebRTC
 * adb-channel. shell_v2 demultiplexes stdout and stderr from one packet stream
 * and awaits each enqueue, so an undrained stderr backpressures the whole demux
 * and freezes stdout. Stderr must therefore be drained. Aborting on cancel
 * closes the socket, which ends the drain.
 */
export function openLogcat(device: Adb, source: LogcatSource): YumeReadableStream<AndroidLogEntry> {
  const args = ['logcat', '-B', '-b', source.buffers.join(',')];
  if (source.pid !== undefined) {
    args.push('--pid', source.pid.toString());
  }

  const abort = new AbortController();
  return new WrapReadableStream({
    start: async () => {
      const shell = device.subprocess.shellProtocol;
      if (shell) {
        const process = await shell.spawn(args, abort.signal);
        void process.stderr.pipeTo(new WritableStream()).catch(() => undefined);
        return process.stdout;
      }
      return (await device.subprocess.noneProtocol.spawn(args, abort.signal)).output;
    },
    cancel: () => abort.abort(),
  }).pipeThrough(new BufferedTransformStream(deserializeAndroidLogEntry));
}

/** Maps a structured logcat entry to the viewer's model. */
export function toLogEntry(entry: AndroidLogEntry, id: number): LogEntry {
  const timestamp = new Date(Number(entry.timestamp / NANOSECONDS_PER_MILLISECOND));
  return {
    id,
    timestamp,
    timeLabel: formatTimeLabel(timestamp),
    level: PRIORITY_TO_LEVEL[entry.priority] ?? 'I',
    tag: entry.tag,
    message: entry.message,
    pid: entry.pid,
    tid: entry.tid,
    raw: entry.toString(LogcatFormat.ThreadTime),
  };
}

function formatTimeLabel(date: Date): string {
  const h = date.getHours().toString().padStart(2, '0');
  const m = date.getMinutes().toString().padStart(2, '0');
  const s = date.getSeconds().toString().padStart(2, '0');
  const ms = date.getMilliseconds().toString().padStart(3, '0');
  return `${h}:${m}:${s}.${ms}`;
}

/** Resolves a package name to its running pid, or null when not running. */
export async function resolveAppPid(device: Adb, packageName: string): Promise<number | null> {
  const output = await exec(device, `pidof -s ${escapeArg(packageName)}`);
  const pid = Number.parseInt(output.trim(), 10);
  return Number.isNaN(pid) ? null : pid;
}

/** Clears the device-side log buffers (`logcat -c`). */
export async function clearLogBuffers(device: Adb, buffers: readonly LogBufferId[]): Promise<void> {
  const arg = buffers.length > 0 ? ` -b ${buffers.join(',')}` : '';
  await exec(device, `logcat -c${arg}`);
}
