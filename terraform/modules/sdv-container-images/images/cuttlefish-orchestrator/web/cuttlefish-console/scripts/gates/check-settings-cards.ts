#!/usr/bin/env npx tsx
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
 * A toggle must apply on flip; a form batches behind an explicit Save. Mixing
 * them leaves the user unable to tell whether flipping the switch did
 * anything.
 *
 * A DOM assertion over the rendered page cannot catch this: settings-card
 * structurally admits one variant per card, so such a test passes forever and
 * only fails if someone hand-rolls a card outside the component, which is
 * exactly the drift worth catching. So this scans the templates instead, and
 * it scans all of them: the drift it exists for is a card written somewhere
 * the settings feature does not own.
 */

import {readdirSync, readFileSync} from 'node:fs';
import {join} from 'node:path';

const SOURCE_ROOT = 'src/app';

const CARD = 'app-settings-card';

/** Every .ts under the console source tree, which is where templates live. */
function sourceFiles(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, {withFileTypes: true})) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      found.push(...sourceFiles(path));
    } else if (entry.name.endsWith('.ts') && !entry.name.endsWith('.spec.ts')) {
      found.push(path);
    }
  }
  return found;
}

/**
 * Splits a template into card blocks. The variant is read from the opening tag
 * alone. Read from the whole block, any nested component carrying a `variant`
 * attribute would be mistaken for the card's own.
 */
function cardBlocks(source: string): Array<{variant: string; body: string}> {
  const blocks: Array<{variant: string; body: string}> = [];
  const open = new RegExp(`<${CARD}\\b`, 'g');
  let match = open.exec(source);
  for (; match !== null; match = open.exec(source)) {
    const end = source.indexOf(`</${CARD}>`, match.index);
    if (end < 0) {
      continue;
    }
    const tagEnd = source.indexOf('>', match.index);
    const openingTag = source.slice(match.index, tagEnd < 0 ? match.index : tagEnd);
    blocks.push({
      variant: /variant="([a-z]+)"/.exec(openingTag)?.[1] ?? 'form',
      body: source.slice(match.index, end),
    });
  }
  return blocks;
}

let failures = 0;
let scanned = 0;
for (const file of sourceFiles(SOURCE_ROOT)) {
  const source = readFileSync(file, 'utf8');
  if (!source.includes(`<${CARD}`)) {
    continue;
  }
  for (const {variant, body} of cardBlocks(source)) {
    scanned++;
    const hasSwitch = /<app-settings-switch|<mat-slide-toggle/.test(body);
    if (variant === 'form' && hasSwitch) {
      console.error(`${file}: a form-variant card contains a switch`);
      failures++;
    }
    if (variant === 'immediate' && body.includes('(save)=')) {
      console.error(`${file}: an immediate-variant card wires a Save handler`);
      failures++;
    }
  }
}

if (failures > 0) {
  console.error(`\n${failures} card(s) mix an immediate switch with a batched Save.`);
  process.exit(1);
}
if (scanned === 0) {
  console.error(`No <${CARD}> found under ${SOURCE_ROOT}: this check is scanning nothing.`);
  process.exit(1);
}
console.log(`Settings cards: ${scanned} scanned, none mixes a switch with a Save button.`);
