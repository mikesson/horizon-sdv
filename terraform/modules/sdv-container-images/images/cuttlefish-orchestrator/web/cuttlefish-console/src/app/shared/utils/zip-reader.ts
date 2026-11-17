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
 * Dependency-free ZIP reading over an in-memory byte buffer.
 *
 * Parses the central directory once, then produces entry bytes on demand.
 * Stored entries come back as subarray copies, deflated entries go through the
 * platform's `DecompressionStream`. Enough of the format for Android app
 * bundles (`.apks`, `.xapk`, `.apkm`). Zip64 archives are rejected with a
 * clear error rather than mis-parsed.
 */

// Signatures as read little-endian: "PK\x05\x06", "PK\x01\x02", etc.
const EOCD_SIGNATURE = 0x06054b50;
const CENTRAL_FILE_SIGNATURE = 0x02014b50;
const LOCAL_FILE_SIGNATURE = 0x04034b50;
const ZIP64_EOCD_LOCATOR_SIGNATURE = 0x07064b50;

const METHOD_STORE = 0;
const METHOD_DEFLATE = 8;

const EOCD_MIN_SIZE = 22;
const MAX_COMMENT_SIZE = 0xffff;

export interface ZipEntry {
  name: string;
  compressedSize: number;
  uncompressedSize: number;
  /** True for directory entries (name ends with "/"). */
  isDirectory: boolean;
}

export class ZipArchive {
  private constructor(
    private readonly bytes: Uint8Array,
    private readonly view: DataView,
    private readonly entries: readonly InternalEntry[]
  ) {}

  /** Opens an archive from its full bytes (e.g. `await file.arrayBuffer()`). */
  static open(data: Uint8Array): ZipArchive {
    const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
    const eocd = findEndOfCentralDirectory(data, view);
    if (eocd.entryCount === 0xffff || eocd.centralDirectoryOffset === 0xffffffff) {
      throw new Error('Zip64 archives are not supported (archive too large)');
    }
    return new ZipArchive(data, view, parseCentralDirectory(data, view, eocd));
  }

  list(): ZipEntry[] {
    return this.entries.map(entry => ({
      name: entry.name,
      compressedSize: entry.compressedSize,
      uncompressedSize: entry.uncompressedSize,
      isDirectory: entry.name.endsWith('/'),
    }));
  }

  /** Returns the decompressed bytes of one entry. */
  async read(name: string): Promise<Uint8Array> {
    const entry = this.entries.find(e => e.name === name);
    if (!entry) {
      throw new Error(`Entry not found: ${name}`);
    }

    const dataStart = this.resolveDataOffset(entry);
    const compressed = this.bytes.subarray(dataStart, dataStart + entry.compressedSize);

    if (entry.method === METHOD_STORE) {
      return compressed.slice();
    }
    if (entry.method === METHOD_DEFLATE) {
      const body = new Response(toArrayBuffer(compressed)).body;
      if (!body) {
        throw new Error(`Deflate stream unavailable for ${name}`);
      }
      const inflated = body.pipeThrough(new DecompressionStream('deflate-raw'));
      return new Uint8Array(await new Response(inflated).arrayBuffer());
    }
    throw new Error(`Unsupported compression method ${entry.method} for ${name}`);
  }

  /**
   * The central directory records the local-header offset, but the local
   * header's own name/extra lengths can differ, so re-read it to find where
   * the entry's data actually starts.
   */
  private resolveDataOffset(entry: InternalEntry): number {
    if (this.view.getUint32(entry.localHeaderOffset, true) !== LOCAL_FILE_SIGNATURE) {
      throw new Error(`Bad local header for ${entry.name}`);
    }
    const nameLength = this.view.getUint16(entry.localHeaderOffset + 26, true);
    const extraLength = this.view.getUint16(entry.localHeaderOffset + 28, true);
    return entry.localHeaderOffset + 30 + nameLength + extraLength;
  }
}

/** Copies bytes into a standalone ArrayBuffer (a valid `BodyInit`). */
function toArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.length);
  copy.set(bytes);
  return copy.buffer;
}

interface InternalEntry {
  name: string;
  method: number;
  compressedSize: number;
  uncompressedSize: number;
  localHeaderOffset: number;
}

interface EndOfCentralDirectory {
  entryCount: number;
  centralDirectorySize: number;
  centralDirectoryOffset: number;
}

function findEndOfCentralDirectory(bytes: Uint8Array, view: DataView): EndOfCentralDirectory {
  if (bytes.length < EOCD_MIN_SIZE) {
    throw new Error('Not a ZIP archive (too small)');
  }

  // The EOCD sits at the end, after an optional trailing comment.
  const scanStart = Math.max(0, bytes.length - EOCD_MIN_SIZE - MAX_COMMENT_SIZE);
  for (let offset = bytes.length - EOCD_MIN_SIZE; offset >= scanStart; offset--) {
    if (view.getUint32(offset, true) !== EOCD_SIGNATURE) {
      continue;
    }
    if (offset >= 20 && view.getUint32(offset - 20, true) === ZIP64_EOCD_LOCATOR_SIGNATURE) {
      throw new Error('Zip64 archives are not supported (archive too large)');
    }
    return {
      entryCount: view.getUint16(offset + 10, true),
      centralDirectorySize: view.getUint32(offset + 12, true),
      centralDirectoryOffset: view.getUint32(offset + 16, true),
    };
  }
  throw new Error('Not a ZIP archive (no end-of-central-directory record)');
}

function parseCentralDirectory(
  bytes: Uint8Array,
  view: DataView,
  eocd: EndOfCentralDirectory
): InternalEntry[] {
  const decoder = new TextDecoder();
  const entries: InternalEntry[] = [];

  let offset = eocd.centralDirectoryOffset;
  for (let i = 0; i < eocd.entryCount; i++) {
    if (view.getUint32(offset, true) !== CENTRAL_FILE_SIGNATURE) {
      throw new Error('Corrupt central directory');
    }
    const method = view.getUint16(offset + 10, true);
    const compressedSize = view.getUint32(offset + 20, true);
    const uncompressedSize = view.getUint32(offset + 24, true);
    const nameLength = view.getUint16(offset + 28, true);
    const extraLength = view.getUint16(offset + 30, true);
    const commentLength = view.getUint16(offset + 32, true);
    const localHeaderOffset = view.getUint32(offset + 42, true);
    const name = decoder.decode(bytes.subarray(offset + 46, offset + 46 + nameLength));

    entries.push({name, method, compressedSize, uncompressedSize, localHeaderOffset});
    offset += 46 + nameLength + extraLength + commentLength;
  }
  return entries;
}
