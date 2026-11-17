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

/** File-sync operations over a connected `Adb` device. */

import type {Adb} from '@yume-chan/adb';
import {escapeArg, LinuxFileType} from '@yume-chan/adb';
import {ReadableStream as YumeReadableStream} from '@yume-chan/stream-extra';

import {execFull} from './adb-exec';

export interface FileEntry {
  name: string;
  path: string;
  size: number;
  isDirectory: boolean;
  isFile: boolean;
  isSymlink: boolean;
  mode: number;
  mtime: Date;
}

/** Lists directory contents. */
export async function listDirectory(device: Adb, path: string): Promise<FileEntry[]> {
  const sync = await device.sync();

  try {
    const entries = await sync.readdir(path);
    return entries.map(entry => ({
      name: entry.name,
      path: `${path}/${entry.name}`.replace(/\/+/g, '/'),
      size: Number(entry.size),
      isDirectory: entry.type === LinuxFileType.Directory,
      isFile: entry.type === LinuxFileType.File,
      isSymlink: entry.type === LinuxFileType.Link,
      mode: entry.mode,
      mtime: new Date(Number(entry.mtime) * 1000),
    }));
  } finally {
    await sync.dispose();
  }
}

/** Reads a file's full content. */
export async function readFile(device: Adb, path: string): Promise<Uint8Array> {
  const sync = await device.sync();

  try {
    const stream = sync.read(path);
    const reader = stream.getReader();
    const chunks: Uint8Array[] = [];

    while (true) {
      const {done, value} = await reader.read();
      if (done) {
        break;
      }
      chunks.push(value);
    }

    const totalLength = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
    const result = new Uint8Array(totalLength);
    let offset = 0;
    for (const chunk of chunks) {
      result.set(chunk, offset);
      offset += chunk.length;
    }

    return result;
  } finally {
    await sync.dispose();
  }
}

/** Writes a file to the device. */
export async function writeFile(
  device: Adb,
  path: string,
  content: Uint8Array,
  options?: {permission?: number; mtime?: number}
): Promise<void> {
  const sync = await device.sync();

  try {
    const stream = new YumeReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(content);
        controller.close();
      },
    });

    await sync.write({
      filename: path,
      file: stream,
      permission: options?.permission ?? 0o644,
      mtime: options?.mtime ?? Math.floor(Date.now() / 1000),
    });
  } finally {
    await sync.dispose();
  }
}

/** Deletes a file. */
export async function deleteFile(device: Adb, path: string): Promise<void> {
  await mutate(device, `rm -f ${escapeArg(path)}`);
}

/** Deletes a directory and everything in it. */
export async function deleteDirectory(device: Adb, path: string): Promise<void> {
  await mutate(device, `rm -rf ${escapeArg(path)}`);
}

/** Creates a directory. */
export async function createDirectory(device: Adb, path: string, parents = false): Promise<void> {
  await mutate(device, parents ? `mkdir -p ${escapeArg(path)}` : `mkdir ${escapeArg(path)}`);
}

/**
 * Runs a mutating command and surfaces its failure. A permission-denied
 * `rm` prints to stderr and exits nonzero, which `exec`'s stdout-only view
 * would silently swallow.
 */
async function mutate(device: Adb, command: string): Promise<void> {
  const result = await execFull(device, command);
  if (result.exitCode !== 0) {
    throw new Error(result.stderr.trim() || result.stdout.trim() || `Command failed: ${command}`);
  }
}
