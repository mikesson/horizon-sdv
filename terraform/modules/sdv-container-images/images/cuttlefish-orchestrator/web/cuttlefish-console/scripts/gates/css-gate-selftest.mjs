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
 * Proves the inline-styles CSS gate can fail before trusting a run where it did
 * not.
 *
 * The failure this guards is silent and reports success: point a CSS-in-JS
 * syntax that expects a *tagged* template at Angular's untagged `styles:` and
 * every component parses as an empty stylesheet. Stylelint exits 0 having
 * checked nothing, which is indistinguishable from clean. Two off-the-shelf
 * syntaxes did exactly that here before the custom one was written.
 *
 * So this plants defects rather than reading config, and asserts both
 * directions: the planted ones are caught, and a clean styles block next to
 * TypeScript full of CSS-shaped noise stays silent. Without the second half a
 * syntax that simply handed the whole .ts file to the CSS parser would pass.
 *
 * Runs through stylelint's Node API against a filename that never exists, so a
 * kill mid-run cannot leave a probe component behind in src/.
 */

import path from 'node:path';
import {fileURLToPath} from 'node:url';
import stylelint from 'stylelint';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const configFile = path.join(root, '.stylelintrc.json');

// Never written to disk. The path decides which `.stylelintrc.json` override
// applies, so it has to sit under src/ and end in .ts.
const codeFilename = path.join(root, 'src', '__css-gate-probe.component.ts');

const PLANTED = `
import {Component} from '@angular/core';

@Component({
  selector: 'css-gate-probe',
  template: '',
  styles: \`
    .probe {
      display: flexx;
      color: #ff0000;
      --mdc-checkbox-probe-color: red;
    }
  \`,
})
export class CssGateProbeComponent {}
`;

/**
 * A clean styles block. The TypeScript around it carries strings that a CSS
 * parser would choke on or flag, so a warning here means the extractor is
 * reading the file rather than the block.
 */
const CLEAN = `
import {Component} from '@angular/core';

const NOT_CSS = 'display: flexx; color: #ff0000;';
const ALSO_NOT_CSS = {selector: '.a {', fallback: '#00ff00'};

@Component({
  selector: 'css-gate-probe',
  template: '<div class="probe">{{ NOT_CSS }}</div>',
  styles: \`
    .probe {
      display: flex;
      color: var(--cco-text);
    }
  \`,
})
export class CssGateProbeComponent {}
`;

const EXPECTED = [
  'declaration-property-value-no-unknown', // the styles block is parsed as CSS at all
  'color-no-hex', //                          the src/**/*.ts override is applied
  'property-disallowed-list', //              the repo's own rules reach inside it
];

async function rulesFired(code) {
  const {results} = await stylelint.lint({code, codeFilename, configFile});
  return results.flatMap(r => [
    ...r.warnings.map(w => w.rule),
    // A syntax error is reported here rather than as a warning, and would
    // otherwise read as "no rules fired".
    ...r.parseErrors.map(e => `parse-error: ${e.text}`),
  ]);
}

const failures = [];

const planted = await rulesFired(PLANTED);
for (const rule of EXPECTED) {
  if (!planted.includes(rule)) {
    failures.push(
      `planted defect not caught: ${rule} did not fire. ` +
        `The inline-styles syntax is reporting clean on CSS it should reject.`,
    );
  }
}

const clean = await rulesFired(CLEAN);
if (clean.length > 0) {
  failures.push(
    `clean styles block reported ${clean.join(', ')}. ` +
      `The syntax is linting the TypeScript around the block, not the block.`,
  );
}

if (failures.length > 0) {
  console.error('CSS gate self-test FAILED:');
  for (const f of failures) {
    console.error(`  - ${f}`);
  }
  process.exit(1);
}

console.log(`CSS gate self-test passed (${EXPECTED.length} planted defects caught, clean block silent)`);
