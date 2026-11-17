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
 * Launch Template Gate
 *
 * Upstream cuttlefish resolves @import to one of eight form-factor templates
 * and then throws the merged result away (the write-back is commented out),
 * so both stacks carry their own transcription of those literals: the backend
 * applies them to the payload, and this console previews them in the create
 * editor. Two hand-copied tables with nothing linking them drift, and the
 * symptom is a device that boots with a layout the editor did not show.
 *
 * The backend owns the shared subset as testdata, regenerated and held by
 * TestLaunchTemplatesGolden; this gate holds TEMPLATE_DEFAULTS against the
 * same file. Editing either table alone fails one of the two.
 *
 * Only what both stacks own is compared. The console additionally pins
 * refresh_rate_hertz and states use_sdcard on every template, where upstream
 * sets neither. Those are additions, and not divergence.
 *
 * Usage: npm run check:launch-templates  (a leg of `npm run lint`)
 */

import {readFileSync} from 'node:fs';
import {dirname, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {TEMPLATE_DEFAULTS} from '../../src/app/devices/models/cvd-config.schema';

interface GoldenDisplay {
  width: number;
  height: number;
  dpi: number;
}

interface GoldenTemplate {
  memory_mb: number;
  use_sdcard: boolean;
  displays: GoldenDisplay[];
}

const GOLDEN = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../../internal/features/device/cvd/testdata/launch_templates.json'
);

function loadGolden(): Record<string, GoldenTemplate> {
  const raw = readFileSync(GOLDEN, 'utf8');
  const parsed = JSON.parse(raw) as Record<string, GoldenTemplate>;
  if (Object.keys(parsed).length === 0) {
    // An empty object would make every comparison below vacuously pass.
    throw new Error(`${GOLDEN} holds no templates`);
  }
  return parsed;
}

/** TEMPLATE_DEFAULTS projected onto the subset the backend also owns. */
function consoleView(): Record<string, GoldenTemplate> {
  const out: Record<string, GoldenTemplate> = {};
  for (const [name, tmpl] of Object.entries(TEMPLATE_DEFAULTS)) {
    out[name] = {
      memory_mb: tmpl.vm?.memory_mb ?? 0,
      use_sdcard: tmpl.vm?.use_sdcard ?? true,
      displays: (tmpl.graphics?.displays ?? []).map(d => ({
        width: d.width,
        height: d.height,
        dpi: d.dpi,
      })),
    };
  }
  return out;
}

function main(): void {
  const golden = loadGolden();
  const fromConsole = consoleView();
  const errors: string[] = [];

  const goldenNames = Object.keys(golden).sort();
  const consoleNames = Object.keys(fromConsole).sort();
  if (goldenNames.join(',') !== consoleNames.join(',')) {
    errors.push(
      `template names differ\n  backend: ${goldenNames.join(', ')}\n  console: ${consoleNames.join(', ')}`
    );
  }

  for (const name of goldenNames) {
    const want = golden[name];
    const got = fromConsole[name];
    if (!got) {
      continue; // already reported by the name-set check
    }
    const wantJson = JSON.stringify(want);
    const gotJson = JSON.stringify(got);
    if (wantJson !== gotJson) {
      errors.push(`template "${name}" differs\n  backend: ${wantJson}\n  console: ${gotJson}`);
    }
  }

  if (errors.length > 0) {
    process.stderr.write(
      `check-launch-templates: the two copies of upstream's launch templates disagree.\n\n` +
        `${errors.join('\n\n')}\n\n` +
        `Fix whichever copy is wrong. If the backend table changed on purpose, ` +
        `TEMPLATE_DEFAULTS in src/app/devices/models/cvd-config.schema.ts must follow.\n`
    );
    process.exit(1);
  }

  process.stdout.write(`check-launch-templates: ${goldenNames.length} templates agree\n`);
}

main();
