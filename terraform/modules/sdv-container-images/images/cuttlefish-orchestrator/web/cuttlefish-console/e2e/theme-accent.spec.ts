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
 * Every Material control that carries the accent must compute the GCP blue, not
 * the generated Material 3 palette.
 *
 * This gates on *computed colour*, never on token names. A custom property
 * renamed to a name Material still does not read leaves zero `--mdc-*` in the
 * stylesheet and goes on rendering periwinkle. Only the rendered pixel
 * distinguishes the two.
 *
 * It runs against every match, not the first, because the same variant renders
 * two different colours depending on whether a CCO class happens to be
 * applied. Asserting on `.first()` would pass or fail on DOM order.
 *
 * The selectors are Material's own internals, taken from the rules in
 * `@angular/material/fesm2022/*.mjs` that read each token. They are private API
 * and will move on a major upgrade; that is acceptable here because this file
 * exists to detect exactly that kind of silent break.
 */

import {expect, test} from './fixtures/api-mock';

// These assert the dark palette, so the OS scheme has to be pinned: with
// the appearance switcher in place the console follows it, and Playwright
// defaults to light.
test.use({colorScheme: 'dark'});

/** `$gcp-primary` / `$gcp-blue-300`, the Cloud Console's own dark-theme accent. */
const ACCENT = 'rgb(138, 180, 248)';

/** `mat.$blue-palette` at dark tone 80, what leaks through when a token is dead. */
const M3_LAVENDER = 'rgb(190, 194, 255)';

interface Probe {
  /** Reported on failure; names the control and the part that carries the accent. */
  readonly label: string;
  readonly selector: string;
  readonly property: string;
  readonly pseudo?: string;
}

interface RouteProbes {
  readonly route: string;
  /** Ticks a control so its selected accent is on screen. */
  readonly prepare?: 'check-first-checkbox';
  readonly probes: readonly Probe[];
}

// `:enabled` mirrors Material's own rules. Without it these match disabled
// controls too, which are grey by design, so the assertion could never be
// satisfied by any correct palette.
const SELECTED_CHECKBOX =
  '.mdc-checkbox__native-control:enabled:checked ~ .mdc-checkbox__background';
const SELECTED_SWITCH = 'button.mdc-switch:enabled.mdc-switch--selected';

// Buttons carry a theme class. `mat-warn` is excluded from the accent set
// because a warn button is supposed to be red.
const UNTHEMED = ':not(.mat-warn):not(.mat-accent):not(:disabled)';

const ROUTE_PROBES: readonly RouteProbes[] = [
  {
    route: '/devices',
    prepare: 'check-first-checkbox',
    probes: [
      {label: 'checkbox, selected box', selector: SELECTED_CHECKBOX, property: 'border-color'},
      {
        label: 'slide-toggle, selected track',
        selector: `${SELECTED_SWITCH} .mdc-switch__track`,
        property: 'background-color',
        pseudo: '::after',
      },
      {
        label: 'progress bar, active indicator',
        selector: '.mdc-linear-progress__bar-inner',
        property: 'border-top-color',
      },
      {
        // The console ships no elevated button: its primary action is the
        // filled variant, which reads the accent straight off --mat-sys-primary.
        label: 'filled button, container',
        selector: `button.mat-mdc-unelevated-button${UNTHEMED}`,
        property: 'background-color',
      },
    ],
  },
  {
    route: '/devices/create/simple',
    probes: [
      {
        label: 'slider, active track',
        selector: '.mdc-slider__track--active_fill',
        property: 'border-color',
      },
      {
        label: 'text button, label',
        selector: `button.mat-mdc-button${UNTHEMED}`,
        property: 'color',
      },
      {
        label: 'outlined button, label',
        selector: `button.mat-mdc-outlined-button${UNTHEMED}`,
        property: 'color',
      },
    ],
  },
  {
    route: '/hosts/create',
    probes: [
      {
        label: 'filled button, container',
        selector: `button.mat-mdc-unelevated-button${UNTHEMED}`,
        property: 'background-color',
      },
    ],
  },
  {
    route: '/admin/catalog?tab=machine-types',
    probes: [
      // The family switch is the curation control on this page; the radio it
      // replaced was the last one any probed route rendered. The track paints
      // its selected colour on ::after. The element itself is transparent, so
      // a probe without the pseudo reads rgba(0,0,0,0) whatever the theme says.
      {
        label: 'slide-toggle, selected track',
        selector: `${SELECTED_SWITCH} .mdc-switch__track`,
        property: 'background-color',
        pseudo: '::after',
      },
      {
        label: 'tab header, active indicator',
        selector: '.mdc-tab-indicator--active .mdc-tab-indicator__content--underline',
        property: 'border-color',
      },
      {
        label: 'tab header, active label',
        selector: '.mdc-tab--active .mdc-tab__text-label',
        property: 'color',
      },
    ],
  },
];

/**
 * Material animates the selected colour in. A computed style read mid-flight
 * returns the interpolated value, the unselected colour, which is neither the
 * accent nor the M3 default and makes the failure message point at the wrong
 * defect. Durations only; no colour is affected.
 */
async function freezeTransitions(page: import('@playwright/test').Page): Promise<void> {
  await page.addStyleTag({
    content: `*, *::before, *::after {
      transition-duration: 0s !important;
      animation-duration: 0s !important;
    }`,
  });
}

/** Reads `property` off every match, so one stray element cannot hide behind another. */
function computedAll(
  page: import('@playwright/test').Page,
  probe: Probe
): Promise<readonly string[]> {
  return page.evaluate(
    ({selector, property, pseudo}) =>
      [...document.querySelectorAll(selector)].map(el =>
        getComputedStyle(el, pseudo).getPropertyValue(property)
      ),
    {selector: probe.selector, property: probe.property, pseudo: probe.pseudo ?? null}
  );
}

for (const {route, prepare, probes} of ROUTE_PROBES) {
  test(`accent renders GCP blue on ${route}`, async ({page}) => {
    await page.goto(route);
    await freezeTransitions(page);

    if (prepare === 'check-first-checkbox') {
      const box = page.locator('.mat-mdc-checkbox input:not(:checked)').first();
      await expect(box).toBeVisible();
      await box.click({force: true});
    }

    // Soft, so one wrong colour cannot mask a later probe that matches nothing.
    // A silently vacuous probe is the failure this file exists to prevent, and
    // it is only visible when every probe reports.
    for (const probe of probes) {
      // Also the wait for the route to finish rendering: this retries until the
      // control exists. Reading computed styles without it samples an empty page
      // and every probe "passes" having asserted on nothing.
      await expect
        .soft(
          page.locator(probe.selector),
          `${probe.label}: nothing matched "${probe.selector}", so this probe asserts nothing`
        )
        .not.toHaveCount(0);

      for (const value of await computedAll(page, probe)) {
        expect.soft(value, `${probe.label} (${probe.property})`).toBe(ACCENT);
      }
    }
  });
}

// A warn button falls through to the same generated palette as everything else,
// so it renders periwinkle rather than red. The exact red is still in flux, so
// assert only that the M3 default is gone, which no accidental change satisfies.
test('warn buttons do not fall through to the Material 3 palette', async ({page}) => {
  await page.goto('/devices');
  await freezeTransitions(page);

  const box = page.locator('.mat-mdc-checkbox input:not(:checked)').first();
  await expect(box).toBeVisible();
  await box.click({force: true});

  const warn = page.locator('button.mat-mdc-button-base.mat-warn');
  await expect(warn, 'no warn button rendered, so this test asserts nothing').not.toHaveCount(0);

  const colors = await page.evaluate(() =>
    [...document.querySelectorAll('button.mat-mdc-button-base.mat-warn')].map(
      el => getComputedStyle(el).color
    )
  );
  for (const color of colors) {
    expect(color, 'warn button label').not.toBe(M3_LAVENDER);
  }
});
