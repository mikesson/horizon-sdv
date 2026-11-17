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
 * A filled button's label against the container the viewer actually sees,
 * hovered as well as at rest.
 *
 * Hover is the state that matters here. Material paints it as a translucent
 * layer over the container rather than as a new `background-color`, so the
 * hovered colour is never in `getComputedStyle`, and a probe that reads the
 * authored container passes on the exact defect this file exists for. The
 * console used to repaint the *elevated* variant to look filled, with
 * declarations that tie Material's own rule on specificity and lose, so only
 * the heavier `:hover` rule landed. That put an accent container under the
 * accent label Material had left in place, at 1.14:1.
 *
 * Both schemes, because the accent pair inverts between them and a value that
 * is legible in one says nothing about the other.
 */

import type {Locator, Page} from '@playwright/test';
import {expect, test} from './fixtures/api-mock';

/** WCAG 2.x AA for text below 18.66px bold / 24px regular, which every label is. */
const MIN_CONTRAST = 4.5;

type Rgb = readonly [number, number, number];

function parse(value: string): {rgb: Rgb; alpha: number} {
  const parts = value.match(/[\d.]+/g)?.map(Number) ?? [];
  return {rgb: [parts[0] ?? 0, parts[1] ?? 0, parts[2] ?? 0], alpha: parts[3] ?? 1};
}

function luminance([r, g, b]: Rgb): number {
  const channel = (v: number): number => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrast(a: Rgb, b: Rgb): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** `over` at `alpha` composited onto `under`. */
function composite(under: Rgb, over: Rgb, alpha: number): Rgb {
  return [0, 1, 2].map(i => under[i] * (1 - alpha) + over[i] * alpha) as unknown as Rgb;
}

interface Paint {
  readonly label: string;
  readonly container: string;
  readonly layerColor: string;
  readonly layerOpacity: string;
}

function read(button: Locator): Promise<Paint> {
  return button.evaluate((el): Paint => {
    const own = getComputedStyle(el);
    const ripple = el.querySelector('.mat-mdc-button-persistent-ripple');
    const layer = ripple ? getComputedStyle(ripple, '::before') : null;
    return {
      label: own.color,
      container: own.backgroundColor,
      layerColor: layer?.backgroundColor ?? 'rgba(0, 0, 0, 0)',
      layerOpacity: layer?.opacity ?? '0',
    };
  });
}

function ratio(paint: Paint, withLayer: boolean): number {
  const container = parse(paint.container).rgb;
  const layer = parse(paint.layerColor);
  const painted = withLayer
    ? composite(container, layer.rgb, layer.alpha * Number(paint.layerOpacity))
    : container;
  return contrast(parse(paint.label).rgb, painted);
}

async function assertReadable(page: Page, what: string, button: Locator): Promise<void> {
  await expect(button, `${what}: no button matched, so this probe asserts nothing`).toHaveCount(1);

  const rest = await read(button);
  expect
    .soft(ratio(rest, false), `${what} at rest (${rest.label} on ${rest.container})`)
    .toBeGreaterThanOrEqual(MIN_CONTRAST);

  await button.hover();
  // The state layer transitions in; a read mid-flight samples a weaker one.
  await page.waitForTimeout(300);
  const hovered = await read(button);
  expect
    .soft(
      ratio(hovered, true),
      `${what} hovered (${hovered.label} on ${hovered.container} + ` +
        `${hovered.layerColor} at ${hovered.layerOpacity})`
    )
    .toBeGreaterThanOrEqual(MIN_CONTRAST);
}

for (const colorScheme of ['dark', 'light'] as const) {
  test.describe(colorScheme, () => {
    test.use({colorScheme});

    test('the primary action stays readable, at rest and hovered', async ({page, api}) => {
      api.devices = [];
      await page.goto('/devices');

      // Two call sites with the same markup and different ancestors: the page
      // header used to carry a rule that repainted whatever it held, and it was
      // the only reason any primary button looked right.
      await assertReadable(
        page,
        'empty-state CTA',
        page.locator('app-empty-state button.mat-mdc-unelevated-button')
      );
      await assertReadable(
        page,
        'page-header CTA',
        page.locator('[primary-action] button.mat-mdc-unelevated-button')
      );
    });

    test('the destructive action stays readable, at rest and hovered', async ({page}) => {
      await page.goto('/hosts/host-1');
      await assertReadable(
        page,
        'host Stop',
        page.locator('[primary-action] button.mat-mdc-unelevated-button.mat-warn')
      );
    });
  });
}
