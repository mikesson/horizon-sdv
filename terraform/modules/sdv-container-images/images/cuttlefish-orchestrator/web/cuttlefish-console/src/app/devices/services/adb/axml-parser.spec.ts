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
import {parseAndroidManifest} from './axml-parser';

const ATTR_TYPE_STRING = 3;
const ATTR_TYPE_INT_DEC = 16;

interface AttrSpec {
  name: string;
  value: string | number;
}

/**
 * Builds a minimal binary AndroidManifest.xml: a UTF-8 string pool followed
 * by one <manifest> start-element chunk. Enough to exercise the parser
 * against the real chunk/pool/attribute layout.
 */
function buildManifest(elementName: string, attrs: AttrSpec[], attributeSize = 20): Uint8Array {
  const pool: string[] = [];
  const intern = (s: string): number => {
    const existing = pool.indexOf(s);
    if (existing !== -1) {
      return existing;
    }
    pool.push(s);
    return pool.length - 1;
  };

  const nameIdx = intern(elementName);
  const attrPlan = attrs.map(attr => ({
    nameIdx: intern(attr.name),
    isString: typeof attr.value === 'string',
    valueIdx: typeof attr.value === 'string' ? intern(attr.value) : -1,
    data: typeof attr.value === 'number' ? attr.value : 0,
  }));

  const stringPool = buildUtf8StringPool(pool);
  const element = buildStartElement(nameIdx, attrPlan, attributeSize);

  const total = 8 + stringPool.length + element.length;
  const out = new Uint8Array(total);
  const view = new DataView(out.buffer);
  view.setUint16(0, 0x0003, true); // XML chunk type
  view.setUint16(2, 8, true);
  view.setUint32(4, total, true);
  out.set(stringPool, 8);
  out.set(element, 8 + stringPool.length);
  return out;
}

function buildUtf8StringPool(strings: string[]): Uint8Array {
  const encoder = new TextEncoder();
  const encoded = strings.map(s => encoder.encode(s));

  const dataChunks: Uint8Array[] = [];
  const offsets: number[] = [];
  let dataLen = 0;
  for (const bytes of encoded) {
    offsets.push(dataLen);
    // UTF-8 string: char-count, byte-count (both short form), bytes, NUL.
    const entry = new Uint8Array(2 + bytes.length + 1);
    entry[0] = bytes.length;
    entry[1] = bytes.length;
    entry.set(bytes, 2);
    dataChunks.push(entry);
    dataLen += entry.length;
  }

  const headerSize = 28 + strings.length * 4;
  const stringsStart = headerSize;
  const size = headerSize + dataLen;
  const out = new Uint8Array(size);
  const view = new DataView(out.buffer);
  view.setUint16(0, 0x0001, true); // string pool type
  view.setUint16(2, 28, true);
  view.setUint32(4, size, true);
  view.setUint32(8, strings.length, true);
  view.setUint32(16, 1 << 8, true); // UTF-8 flag
  view.setUint32(20, stringsStart, true);

  offsets.forEach((offset, i) => {
    view.setUint32(28 + i * 4, offset, true);
  });
  let cursor = stringsStart;
  for (const chunk of dataChunks) {
    out.set(chunk, cursor);
    cursor += chunk.length;
  }
  return out;
}

function buildStartElement(
  nameIdx: number,
  attrs: Array<{nameIdx: number; isString: boolean; valueIdx: number; data: number}>,
  attributeSize = 20
): Uint8Array {
  // headerSize 16 (chunk header 8 + node 8); attrExt (20 bytes) then attrs.
  const HEADER_SIZE = 16;
  const attrsBase = HEADER_SIZE + 20;
  const size = attrsBase + attrs.length * attributeSize;
  const out = new Uint8Array(size);
  const view = new DataView(out.buffer);
  view.setUint16(0, 0x0102, true); // start element type
  view.setUint16(2, HEADER_SIZE, true);
  view.setUint32(4, size, true);
  view.setUint32(12, 0xffffffff, true); // comment
  view.setUint32(16, 0xffffffff, true); // ns
  view.setUint32(20, nameIdx, true);
  view.setUint16(24, 20, true); // attributeStart, relative to attrExt
  view.setUint16(26, attributeSize, true);
  view.setUint16(28, attrs.length, true);

  let attrOffset = attrsBase;
  for (const attr of attrs) {
    view.setUint32(attrOffset, 0xffffffff, true); // ns
    view.setUint32(attrOffset + 4, attr.nameIdx, true);
    view.setInt32(attrOffset + 8, attr.isString ? attr.valueIdx : -1, true);
    view.setUint8(attrOffset + 15, attr.isString ? ATTR_TYPE_STRING : ATTR_TYPE_INT_DEC);
    view.setUint32(attrOffset + 16, attr.isString ? 0 : attr.data, true);
    attrOffset += attributeSize;
  }
  return out;
}

describe('parseAndroidManifest', () => {
  it('extracts package, versionCode, versionName from a base manifest', () => {
    const manifest = buildManifest('manifest', [
      {name: 'versionCode', value: 4210000},
      {name: 'versionName', value: '4.21.0'},
      {name: 'package', value: 'com.example.app'},
    ]);

    expect(parseAndroidManifest(manifest)).toEqual({
      package: 'com.example.app',
      versionCode: 4210000,
      versionName: '4.21.0',
      split: undefined,
    });
  });

  it('strides by the declared attributeSize, not a hardcoded 20 bytes', () => {
    const manifest = buildManifest(
      'manifest',
      [
        {name: 'versionCode', value: 7},
        {name: 'package', value: 'com.example.wide'},
      ],
      24
    );

    expect(parseAndroidManifest(manifest)).toMatchObject({
      package: 'com.example.wide',
      versionCode: 7,
    });
  });

  it('reports the split name for a split APK', () => {
    const manifest = buildManifest('manifest', [
      {name: 'package', value: 'com.example.app'},
      {name: 'split', value: 'config.arm64_v8a'},
    ]);

    expect(parseAndroidManifest(manifest)).toMatchObject({
      package: 'com.example.app',
      split: 'config.arm64_v8a',
    });
  });

  it('throws when there is no package name', () => {
    const manifest = buildManifest('manifest', [{name: 'versionCode', value: 1}]);
    expect(() => parseAndroidManifest(manifest)).toThrow(/package/);
  });
});
