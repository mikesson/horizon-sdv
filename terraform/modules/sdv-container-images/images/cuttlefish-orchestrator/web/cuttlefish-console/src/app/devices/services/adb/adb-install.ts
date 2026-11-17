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
 * APK installation over a connected `Adb` device.
 *
 * A single APK streams directly into `cmd package install -S` (one pass, no
 * temp file); multiple files install as splits of one app via a package
 * manager session. android-bin handles the per-Android-version fallbacks.
 * The whole upload is cancellable and reports byte-level progress.
 */

import type {Adb} from '@yume-chan/adb';
import type {PackageManagerInstallOptions} from '@yume-chan/android-bin';
import {PackageManager, PackageManagerInstallSession} from '@yume-chan/android-bin';
import type {MaybeConsumable, ReadableStream as YumeReadableStream} from '@yume-chan/stream-extra';
import type {InstallFile} from './install-plan';

export interface InstallProgress {
  fileName: string;
  sentBytes: number;
  totalBytes: number;
  /** All bytes sent; the device is verifying and committing the install. */
  committing: boolean;
}

export interface InstallOptions {
  /** Allow installing an older versionCode over a newer one (`-d`). */
  downgrade?: boolean;
  /** Allow test-only APKs (`-t`). */
  allowTest?: boolean;
  signal?: AbortSignal;
  onProgress?: (progress: InstallProgress) => void;
}

function toPackageManagerOptions(options: InstallOptions): Partial<PackageManagerInstallOptions> {
  // `-r` (replace existing) is implied: android-bin adds it unless
  // `skipExisting` is set.
  return {
    grantRuntimePermissions: true,
    requestDowngrade: options.downgrade,
    allowTest: options.allowTest,
  };
}

/** Installs one app from its base (+ split) files. */
export async function installApks(
  device: Adb,
  files: readonly InstallFile[],
  options: InstallOptions = {}
): Promise<void> {
  if (files.length === 0) {
    throw new Error('No APK files to install');
  }
  options.signal?.throwIfAborted();

  const totalBytes = files.reduce((sum, file) => sum + file.size, 0);
  let sentBytes = 0;
  const trackedStream = (file: InstallFile): YumeReadableStream<MaybeConsumable<Uint8Array>> =>
    countBytes(file.blob, options.signal, bytes => {
      sentBytes += bytes;
      options.onProgress?.({
        fileName: file.name,
        sentBytes,
        totalBytes,
        committing: sentBytes >= totalBytes,
      });
    });

  const packageManager = new PackageManager(device);
  const pmOptions = toPackageManagerOptions(options);

  if (files.length === 1) {
    const file = files[0];
    await packageManager.installStream(file.size, trackedStream(file), pmOptions);
    return;
  }

  const session = await PackageManagerInstallSession.create(packageManager, pmOptions);
  try {
    for (const file of files) {
      options.signal?.throwIfAborted();
      await session.addSplitStream(splitNameFor(file.name), file.size, trackedStream(file));
    }
    await session.commit();
  } catch (error) {
    await session.abandon().catch(() => {
      // The original failure (or cancellation) is what matters.
    });
    throw error;
  }
}

/** pm install-write split names: keep to safe filename characters. */
export function splitNameFor(fileName: string): string {
  return fileName.replace(/\.apk$/i, '').replace(/[^a-zA-Z0-9._-]/g, '_') || 'base';
}

function countBytes(
  blob: Blob,
  signal: AbortSignal | undefined,
  onChunk: (bytes: number) => void
): YumeReadableStream<MaybeConsumable<Uint8Array>> {
  const source = blob.stream();
  return source.pipeThrough(
    new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, controller) {
        if (signal?.aborted) {
          throw signal.reason instanceof Error ? signal.reason : new Error('Install cancelled');
        }
        onChunk(chunk.byteLength);
        controller.enqueue(chunk);
      },
    })
  ) as unknown as YumeReadableStream<MaybeConsumable<Uint8Array>>;
}
