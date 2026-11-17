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
 * Minimal parser for Android binary XML (`AndroidManifest.xml` inside an
 * APK). Walks the chunk stream, resolves the string pool, and extracts the
 * few `<manifest>` attributes an install preview needs. It is not a general
 * AXML decoder.
 */

const CHUNK_STRING_POOL = 0x0001;
const CHUNK_START_ELEMENT = 0x0102;

const STRING_POOL_UTF8_FLAG = 1 << 8;

const ATTR_TYPE_STRING = 3;
const ATTR_TYPE_INT_DEC = 16;
const ATTR_TYPE_INT_HEX = 17;

export interface ManifestInfo {
  package: string;
  versionCode?: number;
  versionName?: string;
  /** Present on split APKs (`split="config.arm64_v8a"`); absent on the base. */
  split?: string;
}

interface ParsedAttribute {
  name: string;
  value: string | number;
}

/** Extracts manifest metadata from a decoded `AndroidManifest.xml`. */
export function parseAndroidManifest(bytes: Uint8Array): ManifestInfo {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let strings: string[] = [];

  // Skip the outer XML chunk header (8 bytes) and scan sibling chunks.
  let offset = 8;
  while (offset + 8 <= bytes.length) {
    const type = view.getUint16(offset, true);
    const size = view.getUint32(offset + 4, true);
    if (size < 8 || offset + size > bytes.length) {
      break;
    }

    if (type === CHUNK_STRING_POOL) {
      strings = parseStringPool(view, bytes, offset);
    } else if (type === CHUNK_START_ELEMENT) {
      const element = parseStartElement(view, offset, strings);
      if (element.name === 'manifest') {
        return toManifestInfo(element.attributes);
      }
    }
    offset += size;
  }

  throw new Error('No <manifest> element found');
}

function toManifestInfo(attributes: ParsedAttribute[]): ManifestInfo {
  const find = (name: string): string | number | undefined =>
    attributes.find(a => a.name === name)?.value;

  const packageName = find('package');
  if (typeof packageName !== 'string') {
    throw new Error('Manifest has no package name');
  }

  const versionCode = find('versionCode');
  const versionName = find('versionName');
  const split = find('split');
  return {
    package: packageName,
    versionCode: typeof versionCode === 'number' ? versionCode : undefined,
    versionName: typeof versionName === 'string' ? versionName : undefined,
    split: typeof split === 'string' && split.length > 0 ? split : undefined,
  };
}

function parseStringPool(view: DataView, bytes: Uint8Array, chunkOffset: number): string[] {
  const stringCount = view.getUint32(chunkOffset + 8, true);
  const flags = view.getUint32(chunkOffset + 16, true);
  const stringsStart = view.getUint32(chunkOffset + 20, true);
  const isUtf8 = (flags & STRING_POOL_UTF8_FLAG) !== 0;

  const stringDataBase = chunkOffset + stringsStart;
  const offsetsBase = chunkOffset + 28;
  const result: string[] = [];

  for (let i = 0; i < stringCount; i++) {
    const stringOffset = view.getUint32(offsetsBase + i * 4, true);
    result.push(readPoolString(view, bytes, stringDataBase + stringOffset, isUtf8));
  }
  return result;
}

function readPoolString(
  view: DataView,
  bytes: Uint8Array,
  offset: number,
  isUtf8: boolean
): string {
  if (isUtf8) {
    // UTF-8: character-count length, then byte-count length, then bytes.
    let cursor = offset;
    const [, afterCharLen] = readUtf8Length(view, cursor);
    cursor = afterCharLen;
    const [byteLength, afterByteLen] = readUtf8Length(view, cursor);
    cursor = afterByteLen;
    return new TextDecoder('utf-8').decode(bytes.subarray(cursor, cursor + byteLength));
  }

  // UTF-16: length in 16-bit units, then UTF-16LE code units.
  let charLength = view.getUint16(offset, true);
  let cursor = offset + 2;
  if (charLength & 0x8000) {
    charLength = ((charLength & 0x7fff) << 16) | view.getUint16(cursor, true);
    cursor += 2;
  }
  return new TextDecoder('utf-16le').decode(bytes.subarray(cursor, cursor + charLength * 2));
}

function readUtf8Length(view: DataView, offset: number): [number, number] {
  let length = view.getUint8(offset);
  if (length & 0x80) {
    length = ((length & 0x7f) << 8) | view.getUint8(offset + 1);
    return [length, offset + 2];
  }
  return [length, offset + 1];
}

function parseStartElement(
  view: DataView,
  chunkOffset: number,
  strings: string[]
): {name: string; attributes: ParsedAttribute[]} {
  // Start-element chunk: 8-byte chunk header (headerSize covers it plus the
  // 8-byte node fields = 16), then the attrExt struct:
  //   +16 ns, +20 name, +24 attributeStart, +26 attributeSize,
  //   +28 attributeCount. attributeStart is relative to the attrExt start
  //   (chunkOffset + headerSize).
  const headerSize = view.getUint16(chunkOffset + 2, true);
  const nameIndex = view.getUint32(chunkOffset + 20, true);
  const attributeStart = view.getUint16(chunkOffset + 24, true);
  // The declared struct size is the stride; aapt may grow it beyond the
  // 20 bytes this parser reads.
  const attributeSize = Math.max(view.getUint16(chunkOffset + 26, true), 20);
  const attributeCount = view.getUint16(chunkOffset + 28, true);

  const attributes: ParsedAttribute[] = [];
  let attrOffset = chunkOffset + headerSize + attributeStart;
  for (let i = 0; i < attributeCount; i++) {
    const nameIdx = view.getUint32(attrOffset + 4, true);
    const rawValueIdx = view.getInt32(attrOffset + 8, true);
    const valueType = view.getUint8(attrOffset + 15);
    const data = view.getUint32(attrOffset + 16, true);

    const name = strings[nameIdx] ?? '';
    let value: string | number;
    if (valueType === ATTR_TYPE_STRING) {
      value = rawValueIdx >= 0 ? (strings[rawValueIdx] ?? '') : '';
    } else if (valueType === ATTR_TYPE_INT_DEC || valueType === ATTR_TYPE_INT_HEX) {
      value = data;
    } else {
      value = data;
    }
    attributes.push({name, value});
    attrOffset += attributeSize;
  }

  return {name: strings[nameIndex] ?? '', attributes};
}
