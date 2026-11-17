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
 * Records which CSS rules matched an element while a test ran.
 *
 * Off unless `CCO_CSS_USAGE=1`, so the normal suite pays nothing for it.
 *
 * Why it rides the e2e suite rather than walking routes on its own: a rule is
 * only exercised when something renders it, and dialogs, empty states, error
 * banners and truncation notices never render for a visitor who merely loads
 * each route. The spec files already drive all of those. A standalone
 * route-walker would report their CSS as dead, which is the failure mode that
 * makes a coverage number worse than no number.
 *
 * **The unused set has to be derived.** `CSS.stopRuleUsageTracking`
 * reports only the rules that *were* used, so every record comes back
 * `used: true` and taking it at face value yields a 100%-matched report no
 * defect can dent. The denominator therefore comes from the CSSOM: every rule
 * the browser parsed, minus the ones CDP says matched.
 *
 * It is a *matcher* sweep, not a cascade sweep: matched means the selector
 * found an element, not that its declarations won. For that, assert the
 * computed value, as `theme-accent.spec.ts` does.
 *
 * Chromium only; rule-usage tracking is a CDP API.
 */

import {writeFileSync} from 'node:fs';
import type {BrowserContext, Page, TestInfo} from '@playwright/test';

/** Read back by `scripts/audit/css-usage-report.mjs`. */
const CSS_USAGE_FILE = 'css-usage.json';

/** One rule the browser parsed, and whether any element ever matched it. */
interface RuleRecord {
  readonly selector: string;
  readonly used: boolean;
  /** Stylesheet href, or '' for a runtime-injected `<style>`. */
  readonly sheet: string;
}

export const cssUsageEnabled = (): boolean => process.env.CCO_CSS_USAGE === '1';

/**
 * Selector text as the CSSOM would print it. Applied to both sides of the set
 * difference: one comes from a raw slice of stylesheet source and keeps the
 * author's line breaks, the other from `selectorText`, which has already
 * collapsed them.
 */
const normalize = (selector: string): string =>
  selector
    .replace(/\s+/g, ' ')
    .replace(/\s*,\s*/g, ', ')
    .trim();

/** Every rule currently parsed into the document, with its sheet. */
function snapshotRules(page: Page): Promise<Array<{selector: string; sheet: string}>> {
  return page.evaluate(() => {
    const out: Array<{selector: string; sheet: string}> = [];
    const visit = (rules: CSSRuleList, sheet: string): void => {
      for (const rule of Array.from(rules)) {
        const selector = (rule as CSSStyleRule).selectorText;
        if (selector) {
          out.push({selector, sheet});
        }
        // Grouping rules (@media, @supports, @layer) hold the rules that
        // actually match; keyframe rules carry `keyText`, not a selector, and
        // fall out above.
        const nested = (rule as CSSGroupingRule).cssRules;
        if (nested) {
          visit(nested, sheet);
        }
      }
    };
    for (const sheet of Array.from(document.styleSheets)) {
      try {
        visit(sheet.cssRules, sheet.href ?? '');
      } catch {
        // Cross-origin sheet: unreadable by design, and none of ours.
      }
    }
    return out;
  });
}

/**
 * Collects rule usage for one test and writes it to the test's output
 * directory. `npm run audit:css-usage` merges every file across the run.
 */
export async function recordCssUsage(
  context: BrowserContext,
  page: Page,
  testInfo: TestInfo,
  run: () => Promise<void>
): Promise<void> {
  const cdp = await context.newCDPSession(page);

  await cdp.send('DOM.enable');
  await cdp.send('CSS.enable');
  await cdp.send('CSS.startRuleUsageTracking');

  try {
    await run();
  } finally {
    try {
      const parsed = await snapshotRules(page);
      const {ruleUsage} = await cdp.send('CSS.stopRuleUsageTracking');

      // Sheet text is fetched here rather than as each sheet arrives: Angular
      // injects a `<style>` per component, and a round-trip per injection was
      // enough contention to push tests past their expect timeouts under a
      // parallel run. Only the sheets that reported usage are needed, and a
      // sheet already torn down is absent from the CSSOM snapshot too, so both
      // sides of the difference lose it together.
      const text = new Map<string, string>();
      await Promise.all(
        [...new Set(ruleUsage.map(u => u.styleSheetId))].map(async styleSheetId => {
          const got = await cdp
            .send('CSS.getStyleSheetText', {styleSheetId})
            .then(r => r.text)
            .catch(() => '');
          if (got) {
            text.set(styleSheetId, got);
          }
        })
      );

      const matched = new Set<string>();
      for (const usage of ruleUsage) {
        if (!usage.used) {
          continue;
        }
        const sheet = text.get(usage.styleSheetId);
        if (!sheet) {
          continue;
        }
        const selector = sheet.slice(usage.startOffset, usage.endOffset).split('{')[0];
        if (selector.trim()) {
          matched.add(normalize(selector));
        }
      }

      const rules: RuleRecord[] = parsed.map(({selector, sheet}) => ({
        selector: normalize(selector),
        sheet,
        used: matched.has(normalize(selector)),
      }));
      if (rules.length > 0) {
        writeFileSync(testInfo.outputPath(CSS_USAGE_FILE), JSON.stringify(rules));
      }
    } catch {
      // A test that closed its own page loses both the CSSOM and the session;
      // the rest of the run still reports. Silent because this is opt-in
      // tooling rather than a gate.
    }
    // biome-ignore lint/suspicious/noEmptyBlockStatements: detaching a session whose page is already gone is the normal exit, not a failure.
    await cdp.detach().catch(() => {});
  }
}
