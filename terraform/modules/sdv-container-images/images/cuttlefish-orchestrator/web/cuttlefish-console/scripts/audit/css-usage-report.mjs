// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Merges the per-test rule-usage files written by `e2e/fixtures/css-usage.ts`
 * and reports the console's own CSS rules that never matched an element.
 *
 * This is the half of the dead-CSS question `dead-css.mjs` structurally cannot
 * answer: a class assembled at runtime, a rule targeting Material's own DOM, a
 * `@media` block for a viewport nothing renders at. It is the browser's own
 * answer to "did this selector ever match", not a text search.
 *
 * Read the output as a *lower* bound on liveness, never as proof of death: a
 * rule is unused here if the suite never rendered its element. Widen the suite
 * before deleting anything it reports.
 *
 * Usage:
 *   CCO_CSS_USAGE=1 npm run e2e        # populates test-results/
 *   node scripts/audit/css-usage-report.mjs [--all]
 */

import {existsSync, readFileSync, readdirSync, statSync} from 'node:fs';
import {dirname, join, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

import {selectorSources} from '../gates/dead-css.mjs';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const RESULTS = join(ROOT, 'test-results');
const SHOW_ALL = process.argv.includes('--all');

// Angular stamps view encapsulation onto every emitted selector. Stripping it
// is also how a rule is identified as the console's own: Material's runtime CSS
// carries no such attribute.
const ENCAPSULATION = /\[_ngcontent-[\w-]+\]|\[_nghost-[\w-]+\]/g;
const IS_ENCAPSULATED = new RegExp(ENCAPSULATION.source);

function collect(dir) {
  const out = [];
  if (!existsSync(dir)) return out;
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...collect(p));
    else if (name === 'css-usage.json') out.push(p);
  }
  return out;
}

const files = collect(RESULTS);
if (!files.length) {
  console.error('No rule-usage files under test-results/.\n'
    + 'Run the suite with recording on first:  CCO_CSS_USAGE=1 npm run e2e');
  process.exit(1);
}

// Union across tests: a rule used anywhere is used. Keyed by the encapsulation-
// stripped selector, so the same component rule seen under different generated
// attributes counts once.
const rules = new Map();
for (const file of files) {
  for (const rule of JSON.parse(readFileSync(file, 'utf8'))) {
    const own = IS_ENCAPSULATED.test(rule.selector) || rule.sheet.endsWith('styles.css');
    const selector = rule.selector.replace(ENCAPSULATION, '').replace(/\s+/g, ' ').trim();
    const prev = rules.get(selector);
    rules.set(selector, {own: own || prev?.own === true, used: rule.used || prev?.used === true});
  }
}

const index = selectorSources();
/** Files whose styles define a class this selector mentions. */
function attribute(selector) {
  const owners = new Set();
  for (const m of selector.matchAll(/(?<![\w.])\.(-?[a-zA-Z_][\w-]*)/g)) {
    for (const f of index.get(m[1]) ?? []) owners.add(f);
  }
  return owners.size ? [...owners] : ['(unattributed)'];
}

const considered = [...rules.entries()].filter(([, r]) => SHOW_ALL || r.own);
const unused = considered.filter(([, r]) => !r.used);

const byFile = new Map();
for (const [selector] of unused) {
  for (const file of attribute(selector)) {
    if (!byFile.has(file)) byFile.set(file, []);
    byFile.get(file).push(selector);
  }
}

for (const [file, selectors] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
  console.log(`\n\x1b[1m${file}\x1b[0m  (${selectors.length})`);
  for (const selector of selectors.sort()) console.log(`  ${selector}`);
}

const pct = considered.length ? (100 * (considered.length - unused.length)) / considered.length : 0;
console.log(`\n\x1b[1mSummary:\x1b[0m ${unused.length} of ${considered.length} `
  + `${SHOW_ALL ? '' : 'console-authored '}rules never matched an element `
  + `(${pct.toFixed(1)}% matched) across ${files.length} test(s).`);
console.log('A rule is unused here only if the suite never rendered its element. '
  + 'Widen the suite before deleting.');
