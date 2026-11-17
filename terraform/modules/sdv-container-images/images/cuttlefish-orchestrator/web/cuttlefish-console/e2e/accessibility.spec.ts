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
 * Automated accessibility scan of every top-level route.
 *
 * Template lint already covers all 112 inline templates, but it only sees one
 * template at a time. The defects that shipped here were the ones that only
 * exist once a page is assembled: two components each emitting a valid <main>,
 * nested; a nav rail whose links stayed in the tab order after it collapsed.
 * Those need the rendered document, which is what this scans.
 *
 * Scoped to wcag2a/wcag2aa. Axe's "best-practice" rules are opinions rather
 * than conformance and would turn this into a taste argument.
 */

import AxeBuilder from '@axe-core/playwright';
import {expect, test} from './fixtures/api-mock';

const ROUTES = [
  '/',
  '/hosts',
  '/devices',
  '/devices/create',
  '/batches',
  '/builds',
  '/admin/capacity',
  '/admin/control-plane',
  '/admin/autoscaler',
  '/admin/catalog',
  '/admin/activity',
  '/admin/settings',
];

// Both schemes: the console follows the operating system now, so a scan in one
// of them says nothing about the other, and the light ramp is the newer half.
for (const scheme of ['light', 'dark'] as const) {
  test.describe(`${scheme} scheme`, () => {
    test.use({colorScheme: scheme});

    for (const route of ROUTES) {
      test(`no accessibility violations on ${route}`, async ({page}) => {
        await page.goto(route);
        // The rail is on every authenticated route; its presence means the shell
        // and the route's data have both rendered, so the scan sees a real page.
        await expect(page.getByRole('link', {name: 'Hosts', exact: true})).toBeVisible();

        const {violations} = await new AxeBuilder({page})
          .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
          .analyze();

        // Name the rule and the first offending node: a bare count tells whoever
        // sees this go red nothing about what to open.
        const summary = violations
          .map(
            v =>
              `${v.id} (${v.impact}): ${v.help}\n    ${v.nodes.map(n => n.target.join(' ')).join('\n    ')}`
          )
          .join('\n  ');
        expect(
          violations.map(v => v.id),
          `\n  ${summary}`
        ).toEqual([]);
      });
    }
  });
}
