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
 * Runtime CSS/JS coverage. Drives a real browser through every route and reports
 * how much CSS/JS the page never executed. It is the only reliable way to find
 * dead styles that are applied dynamically (`[class.x]`, `[class]="signal()"`)
 * and so are invisible to static analysis.
 *
 * Prereqs (one-time):
 *   npx playwright install chromium
 *   npm start              # serve the app (dev auth, no IAP) in another shell
 *
 * Run:
 *   node scripts/audit/coverage.mjs                 # against http://localhost:4200
 *   BASE_URL=https://… node scripts/audit/coverage.mjs
 *
 * Chromium-only, because the V8 coverage API isn't exposed by WebKit/Firefox.
 */

import {chromium} from 'playwright';

const BASE = process.env.BASE_URL ?? 'http://localhost:4200';

// Representative of every distinct template/style surface. Add routes as the app
// grows; unauthenticated/dev-mode serving is assumed (no IAP in front).
const ROUTES = [
  '/',
  '/hosts',
  '/hosts/create',
  '/devices',
  '/devices/create',
  '/builds',
  '/builds/create',
  '/admin/host-images',
];

const pct = (used, total) => (total === 0 ? 0 : (used / total) * 100);

async function main() {
  const browser = await chromium.launch();
  const page = await browser.newPage();

  await page.coverage.startCSSCoverage({resetOnNavigation: false});
  await page.coverage.startJSCoverage({resetOnNavigation: false});

  for (const route of ROUTES) {
    try {
      await page.goto(BASE + route, {waitUntil: 'networkidle', timeout: 20_000});
      await page.waitForTimeout(600); // let lazy content/animations settle
    } catch (err) {
      console.warn(`  ! ${route}: ${err.message.split('\n')[0]}`);
    }
  }

  const css = await page.coverage.stopCSSCoverage();
  const js = await page.coverage.stopJSCoverage();
  await browser.close();

  report('CSS', css, 'css');
  report('JS', js, 'js');
}

// Per-entry covered map. CSS entries are Puppeteer-style ({text, ranges:[{start,end}]});
// JS entries are the raw V8 shape ({source, functions:[{ranges:[{startOffset,endOffset,count}]}]}),
// where ranges are nested and an inner range overrides its parent's count.
function entryCoverage(entry, kind) {
  const text = kind === 'css' ? entry.text : entry.source;
  const len = text ? text.length : 0;
  const covered = new Uint8Array(len);
  if (!len) {
    return {len, covered};
  }
  if (kind === 'css') {
    for (const r of entry.ranges) {
      for (let i = r.start; i < r.end && i < len; i++) covered[i] = 1;
    }
    return {len, covered};
  }
  // Apply outermost ranges first so a nested range's count wins for its extent.
  const ranges = entry.functions.flatMap((f) => f.ranges);
  ranges.sort((a, b) => a.startOffset - b.startOffset || b.endOffset - a.endOffset);
  for (const r of ranges) {
    const hit = r.count > 0 ? 1 : 0;
    for (let i = r.startOffset; i < r.endOffset && i < len; i++) covered[i] = hit;
  }
  return {len, covered};
}

function report(label, entries, kind) {
  // A file fetched on N routes yields N entries; union their usage by URL so a
  // line used on any route counts as used (and each file is reported once).
  const byUrl = new Map();
  for (const entry of entries) {
    const {len, covered} = entryCoverage(entry, kind);
    if (!len) {
      continue;
    }
    let rec = byUrl.get(entry.url);
    if (!rec || rec.total !== len) {
      rec = {total: len, covered: new Uint8Array(len)};
      byUrl.set(entry.url, rec);
    }
    for (let i = 0; i < len; i++) {
      if (covered[i]) rec.covered[i] = 1;
    }
  }

  let totalBytes = 0;
  let usedBytes = 0;
  const perFile = [];
  for (const [url, rec] of byUrl) {
    const used = rec.covered.reduce((sum, b) => sum + b, 0);
    totalBytes += rec.total;
    usedBytes += used;
    if (rec.total > 2000) {
      perFile.push({url: url.replace(BASE, ''), unused: rec.total - used, usedPct: pct(used, rec.total)});
    }
  }

  console.log(`\n\x1b[1m${label} coverage: ${pct(usedBytes, totalBytes).toFixed(1)}% used `
    + `(${(((totalBytes - usedBytes) / 1024)).toFixed(0)} KB unused of ${(totalBytes / 1024).toFixed(0)} KB)\x1b[0m`);

  perFile
    .sort((a, b) => b.unused - a.unused)
    .slice(0, 12)
    .forEach(f => console.log(
      `  ${f.usedPct.toFixed(0).padStart(3)}% used  ${(f.unused / 1024).toFixed(0).padStart(4)} KB dead  ${f.url}`));
}

main().catch(err => {
  console.error(err.message);
  console.error('\nIs the app running? Try: npm start  (and: npx playwright install chromium)');
  process.exit(1);
});
