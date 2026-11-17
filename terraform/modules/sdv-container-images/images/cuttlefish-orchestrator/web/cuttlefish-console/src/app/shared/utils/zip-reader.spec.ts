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

import {describe, expect, it} from 'vitest';
import {ZipArchive} from './zip-reader';

/** Builds real ZIP bytes so the reader is tested against the actual format. */
async function buildZip(
  files: Array<{name: string; content: Uint8Array; deflate: boolean}>
): Promise<Uint8Array> {
  const encoder = new TextEncoder();
  const parts: Uint8Array[] = [];
  const central: Uint8Array[] = [];
  let offset = 0;

  for (const file of files) {
    const nameBytes = encoder.encode(file.name);
    const stored = file.deflate ? await deflateRaw(file.content) : file.content;
    const method = file.deflate ? 8 : 0;
    const crc = crc32(file.content);

    const local = new Uint8Array(30 + nameBytes.length);
    const lv = new DataView(local.buffer);
    lv.setUint32(0, 0x04034b50, true);
    lv.setUint16(4, 20, true);
    lv.setUint16(8, method, true);
    lv.setUint32(14, crc, true);
    lv.setUint32(18, stored.length, true);
    lv.setUint32(22, file.content.length, true);
    lv.setUint16(26, nameBytes.length, true);
    local.set(nameBytes, 30);

    const localHeaderOffset = offset;
    parts.push(local, stored);
    offset += local.length + stored.length;

    const cd = new Uint8Array(46 + nameBytes.length);
    const cv = new DataView(cd.buffer);
    cv.setUint32(0, 0x02014b50, true);
    cv.setUint16(10, method, true);
    cv.setUint32(16, crc, true);
    cv.setUint32(20, stored.length, true);
    cv.setUint32(24, file.content.length, true);
    cv.setUint16(28, nameBytes.length, true);
    cv.setUint32(42, localHeaderOffset, true);
    cd.set(nameBytes, 46);
    central.push(cd);
  }

  const centralSize = central.reduce((sum, c) => sum + c.length, 0);
  const centralOffset = offset;

  const eocd = new Uint8Array(22);
  const ev = new DataView(eocd.buffer);
  ev.setUint32(0, 0x06054b50, true);
  ev.setUint16(8, files.length, true);
  ev.setUint16(10, files.length, true);
  ev.setUint32(12, centralSize, true);
  ev.setUint32(16, centralOffset, true);

  return concat([...parts, ...central, eocd]);
}

function concat(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, c) => sum + c.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

async function deflateRaw(data: Uint8Array): Promise<Uint8Array> {
  const buffer = new Uint8Array(data.length);
  buffer.set(data);
  const body = new Response(buffer.buffer).body;
  if (!body) {
    throw new Error('Response body unavailable');
  }
  const stream = body.pipeThrough(new CompressionStream('deflate-raw'));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

function crc32(data: Uint8Array): number {
  let crc = 0xffffffff;
  for (const byte of data) {
    crc ^= byte;
    for (let i = 0; i < 8; i++) {
      crc = crc & 1 ? (crc >>> 1) ^ 0xedb88320 : crc >>> 1;
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

describe('ZipArchive', () => {
  it('lists entries with sizes and directory flags', async () => {
    const zip = await buildZip([
      {name: 'base.apk', content: new TextEncoder().encode('BASEAPKDATA'), deflate: false},
      {name: 'META-INF/', content: new Uint8Array(0), deflate: false},
    ]);
    const archive = ZipArchive.open(zip);

    const entries = archive.list();
    expect(entries.map(e => e.name)).toEqual(['base.apk', 'META-INF/']);
    expect(entries[0]).toMatchObject({uncompressedSize: 11, isDirectory: false});
    expect(entries[1].isDirectory).toBe(true);
  });

  it('reads a stored entry as its exact bytes', async () => {
    const content = new TextEncoder().encode('stored payload');
    const archive = ZipArchive.open(await buildZip([{name: 'a.apk', content, deflate: false}]));

    expect(Array.from(await archive.read('a.apk'))).toEqual(Array.from(content));
  });

  it('inflates a deflated entry back to the original bytes', async () => {
    const content = new TextEncoder().encode('x'.repeat(5000));
    const archive = ZipArchive.open(
      await buildZip([{name: 'config.arm64.apk', content, deflate: true}])
    );

    expect(Array.from(await archive.read('config.arm64.apk'))).toEqual(Array.from(content));
  });

  it('resolves data offsets correctly with multiple entries', async () => {
    const first = new TextEncoder().encode('first');
    const second = new TextEncoder().encode('the second entry is longer');
    const archive = ZipArchive.open(
      await buildZip([
        {name: 'base.apk', content: first, deflate: true},
        {name: 'split_config.xxhdpi.apk', content: second, deflate: false},
      ])
    );

    expect(Array.from(await archive.read('base.apk'))).toEqual(Array.from(first));
    expect(Array.from(await archive.read('split_config.xxhdpi.apk'))).toEqual(Array.from(second));
  });

  it('rejects non-ZIP data', () => {
    expect(() => ZipArchive.open(new Uint8Array(10))).toThrow(/ZIP/);
  });

  it('throws a clear error reading an unknown entry', async () => {
    const archive = ZipArchive.open(
      await buildZip([{name: 'a.apk', content: new Uint8Array([1]), deflate: false}])
    );
    await expect(archive.read('missing.apk')).rejects.toThrow(/not found/);
  });
});
