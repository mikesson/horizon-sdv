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

import AxeBuilder from '@axe-core/playwright';
import {autoscalerFleetBody} from './fixtures/api-fixtures';
import type {ApiState, Page} from './fixtures/api-mock';
import {expect, test} from './fixtures/api-mock';

/**
 * Serves a fleet of `count` machines for the rest of the test.
 *
 * Registered after the default mocks, so it wins. The canvas picks its
 * fidelity from the machine count alone, and nothing else changes it.
 */
async function fleetOf(page: Page, api: ApiState, count: number): Promise<void> {
  await page.route(/\/v2\/admin\/autoscaler(\?|$)/, route =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(autoscalerFleetBody(api, count)),
    })
  );
}

// "Why is the target 3?" had no answer anywhere. The winner alone is not it:
// without the losing inputs beside it, a schedule that lost the max reads
// identically to one that was never configured.
test('the masthead names the winner and the inputs that lost', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const masthead = page.locator('app-autoscaler-masthead');
  await expect(masthead.getByText('intent 0', {exact: false})).toBeVisible();
  await expect(masthead.getByText('schedule -', {exact: false})).toBeVisible();
  await expect(masthead.getByText('won', {exact: false})).toBeVisible();
  // The ceiling term never disappears, so nobody learns it exists only by
  // being clamped by it.
  await expect(masthead.getByText('ceiling 16 · not reached')).toBeVisible();
});

// Both promises, always, and which one is deciding. Printing only the deciding
// half left an operator who had set the other unable to find it. Ending that
// confusion is why the second bound exists.
test('the masthead states both promises and which one is binding', async ({page}) => {
  await page.goto('/admin/autoscaler');

  // The captions under the two numerals, not the words wherever they appear:
  // the binding line names one of the two promises as well, and a bare text
  // match cannot tell the headline from the sentence about it.
  const captions = page.locator('app-autoscaler-masthead .caption');
  await expect(captions).toHaveText(['machines kept free', 'machines kept running']);
  // The fixture runs three machines against a floor of two with none free, so
  // the free-machine promise is the tighter of the two and the line says so in
  // words rather than by weight alone.
  await expect(
    page.locator('app-autoscaler-masthead').getByText('running · floor 2 → 3 now')
  ).toBeVisible();
  await expect(
    page
      .locator('app-autoscaler-masthead .which')
      .getByText('a machine a device takes is replaced, so this many stay free')
  ).toBeVisible();
});

// The relabel, and the control it made room for. "Standing intent" sold a
// buffer under a name that reads as a floor; an operator read it that way and
// concluded placement was ignoring the fleet.
test('the two machine counts are stated as the different promises they are', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const controls = page.locator('app-autoscaler-controls');
  await expect(controls.getByText('Machines to keep free')).toBeVisible();
  await expect(controls.getByText('Machines to keep running')).toBeVisible();
  await expect(controls.getByText('Standing intent')).toHaveCount(0);
  // Judged against what the fleet is actually running, beside the field.
  await expect(controls.getByText('3 running now')).toBeVisible();
});

// A setting whose explanation only appears under a pointer is a setting half
// the readers cannot learn. Both routes are asserted, and the hidden state is
// asserted first. Otherwise a paragraph that was always visible would pass the
// reveal assertions without revealing anything.
test('every setting explains itself on hover and on focus', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const ask = page.locator('app-autoscaler-controls [aria-describedby="help-floor-default"].ask');
  const explanation = page.locator('app-autoscaler-controls #help-floor-default');
  await expect(explanation).toBeHidden();

  await ask.hover();
  await expect(explanation).toBeVisible();
  await expect(explanation).toContainText('where shrinking stops');

  // Away, then back by keyboard alone: the pointer must not be what made it
  // work, and the affordance must be reachable without one.
  await page.locator('app-autoscaler-masthead').hover();
  await expect(explanation).toBeHidden();

  await ask.focus();
  await expect(explanation).toBeVisible();

  // And the other settings carry one too, each with a control pointing at it.
  // The description reaches a screen reader through that link whether or not
  // anything is revealed.
  for (const id of [
    'help-mode',
    'help-free-default',
    'help-horizon',
    'help-grace',
    'help-windows-default',
    'help-ladder-default',
  ]) {
    const other = page.locator(`app-autoscaler-controls #${id}`);
    await expect(other).toHaveCount(1);
    await expect(other).toBeHidden();
    await expect(
      page.locator(`app-autoscaler-controls [aria-describedby="${id}"]`)
    ).not.toHaveCount(0);
  }
});

// A revealed explanation is DOM the route-level scan never sees, exactly like
// the expanded cluster. Both schemes, because every colour role carries two
// values and neither run is a proxy for the other.
for (const scheme of ['light', 'dark'] as const) {
  test(`the revealed explanations have no accessibility violations in ${scheme}`, async ({
    page,
  }) => {
    await page.emulateMedia({colorScheme: scheme});
    await page.goto('/admin/autoscaler');
    await page
      .locator('app-autoscaler-controls [aria-describedby="help-floor-default"].ask')
      .hover();
    await expect(page.locator('app-autoscaler-controls #help-floor-default')).toBeVisible();

    const {violations} = await new AxeBuilder({page})
      .include('app-autoscaler-controls')
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();

    expect(
      violations.map(v => v.id),
      `\n  ${violations.map(v => `${v.id}: ${v.help}`).join('\n  ')}`
    ).toEqual([]);
  });
}

// The two states that are easiest to conflate, on one machine in the fixture.
// An unbacked slot is a claim about the machine's IMAGE; a slot held by a dead
// row is a claim about a CREATE, and drawing them alike blames the wrong thing.
test('a slot held by a dead row is not drawn as an unbacked one', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const machine = page.locator('app-topology-machine').filter({hasText: 'cf-pool-8d0'});
  await expect(machine.locator('.slot[data-kind="error-held"]')).toHaveCount(1);
  await expect(machine.locator('.slot[data-kind="unbacked"]')).toHaveCount(2);
  // Never a mark without its word.
  await expect(machine.locator('.slot[data-kind="unbacked"]').first()).toContainText('unbacked');
});

// The card states what the machine can RUN, and names the promise only where it
// exceeds it. A phrase on every card stops being read on the one that needs it.
test('the card states backed slots, and the promise only on excess', async ({page}) => {
  await page.goto('/admin/autoscaler');

  await expect(
    page
      .locator('app-topology-machine')
      .filter({hasText: 'cf-pool-8d0'})
      .getByText('1 of 10 backed · 12 promised')
  ).toBeVisible();
  await expect(
    page
      .locator('app-topology-machine')
      .filter({hasText: 'cf-auto-7a3'})
      .getByText('0 of 3 backed', {exact: false})
  ).toBeVisible();
});

// origin=auto IS the autoscaler's. Excluding it (on the intuition that its
// fleet means only what it bought itself) would make this page disagree with
// the keeper's own arithmetic.
test('counts a declaratively provisioned machine as the autoscaler’s', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const machine = page.locator('.owned app-topology-machine').filter({hasText: 'cf-auto-7a3'});
  await expect(machine.getByText('ready for a device')).toBeVisible();
  // And a person's machine is present, outside the rail, stated as theirs.
  await expect(page.locator('.strip').getByText('User-made')).toBeVisible();
  await expect(page.locator('.strip app-topology-machine')).toHaveCount(1);
});

// "Why isn't this idle machine counted?" had no answer in the console at all,
// and the drawing cannot say it: an idle machine that is not free looks exactly
// like one that is.
test('says which term excluded a machine that is idle and not free', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const machine = page.locator('app-topology-machine').filter({hasText: 'cf-pool-3c9'});
  await expect(machine.getByText('not free', {exact: true})).toBeVisible();
  await expect(machine.getByText('degraded, so placement refuses it')).toBeVisible();
});

// A still drawing has at least six causes and they are indistinguishable
// without this. Off is a configuration, not a fault, and the sentence says so.
test('the autoscaler says why nothing is happening', async ({page}) => {
  await page.goto('/admin/autoscaler');

  await expect(
    page.getByText('A target is composed every cycle and nothing is permitted to act on it')
  ).toBeVisible();
  await expect(page.locator('.perms').getByText('provisioning not permitted')).toBeVisible();
  await expect(page.locator('.perms').getByText('lowering a lease not permitted')).toBeVisible();
});

// Sixteen machines is the full-card budget; past it the words carry the
// capacity the card area stops encoding. Chosen by count alone, so the same
// snapshot draws identically on every refresh.
test('at twenty machines the fleet renders compact cards', async ({page, api}) => {
  await fleetOf(page, api, 20);
  await page.goto('/admin/autoscaler');

  await expect(page.locator('app-topology-machine')).toHaveCount(20);
  await expect(page.locator('.slot')).toHaveCount(0);
  await expect(page.locator('.cluster')).toHaveCount(0);
});

// Past forty-eight, one tile per state with the count as the dominant figure.
// It opens into full cards rather than a second machine listing.
test('at sixty machines the fleet renders clusters that expand', async ({page, api}) => {
  await fleetOf(page, api, 60);
  await page.goto('/admin/autoscaler');

  const tile = page.locator('.cluster');
  await expect(tile).toHaveCount(1);
  await expect(tile).toContainText('60');
  await expect(page.locator('app-topology-machine')).toHaveCount(0);

  // From the keyboard: a tile that only opens under a pointer hides its members
  // from half the readers.
  await tile.focus();
  await page.keyboard.press('Enter');

  await expect(tile).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('.members app-topology-machine')).toHaveCount(60);
  await expect(page.locator('.members .slot').first()).toBeVisible();
});

// The cluster DOM exists only past the breakpoint, so the route-level scan
// never sees it. Both schemes, because every colour role carries two values and
// neither run is a proxy for the other.
for (const scheme of ['light', 'dark'] as const) {
  test(`the clustered canvas has no accessibility violations in ${scheme}`, async ({page, api}) => {
    await page.emulateMedia({colorScheme: scheme});
    await fleetOf(page, api, 60);
    await page.goto('/admin/autoscaler');
    await page.locator('.cluster').click();
    await expect(page.locator('.members app-topology-machine').first()).toBeVisible();

    const {violations} = await new AxeBuilder({page})
      .include('app-topology-canvas')
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();

    expect(
      violations.map(v => v.id),
      `\n  ${violations.map(v => `${v.id}: ${v.help}`).join('\n  ')}`
    ).toEqual([]);
  });
}

// The suggestion is never a bare figure: the interval says when, and the split
// says how much of the window a buffer could ever have answered.
test('the window’s suggestion shows its derivation', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const band = page.locator('app-demand-band');
  await expect(band.getByText('the last 7 days suggest standby 3')).toBeVisible();
  await expect(
    band.getByText('peak of 3 concurrent servable misses', {exact: false})
  ).toBeVisible();
  await expect(band.getByText('41 misses, 29 servable', {exact: false})).toBeVisible();
});

// Both terms, never a percentage: four of ten and four of four hundred are
// different facts, and a ratio erases which it was.
test('states the GPU degradation as both of its terms', async ({page}) => {
  await page.goto('/admin/autoscaler');

  await expect(
    page.getByText('2 of 5 creates that wanted a GPU got software rendering')
  ).toBeVisible();
});

// THE property the three endpoints exist for. A whole-policy write from one
// surface carries a stale copy of the others and reverts them. The worst case
// is the window list, which a horizon edit would silently clear.
test('a horizon edit writes tuning alone and leaves the windows stored', async ({page, api}) => {
  await page.goto('/admin/autoscaler');
  await expect(page.getByLabel('Demand window (seconds)')).toHaveValue('3600');

  const schedulePuts: string[] = [];
  page.on('request', r => {
    if (r.method() === 'PUT' && r.url().includes('/fleet/schedules')) {
      schedulePuts.push(r.url());
    }
  });

  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/fleet/tuning') && r.method() === 'PUT'
  );
  await page.getByLabel('Demand window (seconds)').fill('1800');
  await page.getByRole('button', {name: 'Save measurement and drain'}).click();
  await put;

  expect(schedulePuts).toEqual([]);
  expect(api.settings.fleet_schedules).toHaveLength(1);
});

// Both bounds ride one body, because the endpoint replaces the group. A write
// carrying only the edited number would set the other to zero, and the fleet
// would silently stop holding anything the next time somebody touched either.
test('an edit to one machine count carries the other', async ({page, api}) => {
  await page.goto('/admin/autoscaler');
  // By role, not by label: the setting's question mark carries the same words
  // in its own accessible name, and a label match finds both.
  const floor = page.getByRole('spinbutton', {name: 'Machines to keep running'});
  await expect(floor).toHaveValue('2');

  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/fleet/autoscaler') && r.method() === 'PUT'
  );
  await floor.fill('4');
  await page.getByRole('button', {name: 'Apply machine counts'}).click();
  const body = (await put).postDataJSON();

  expect(body).toEqual({mode: 'off', pool: 'default', standby: 0, floor: 4});
  expect(api.settings.fleet_schedules).toHaveLength(1);
});

// A window is authored here and saved by its own endpoint. The assertion is on
// what the server kept, not on the request going out: a PUT that reached the
// wrong path, or carried seconds where the wire wants minutes converted, would
// still be observed leaving.
test('adds a schedule window through its own endpoint', async ({page, api}) => {
  await page.goto('/admin/autoscaler');
  await expect(page.getByLabel('Demand window (seconds)')).toHaveValue('3600');

  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/fleet/schedules') && r.method() === 'PUT'
  );
  await page.getByRole('button', {name: 'Add a window'}).click();
  await page.getByRole('button', {name: 'Save windows'}).click();
  await put;

  expect(api.settings.fleet_schedules).toHaveLength(2);
  expect(api.settings.fleet_schedules[1]).toMatchObject({
    name: 'New window',
    hosts: 1,
    start: '09:00',
    duration_sec: 3600,
  });
});

// The fourth scope, and the one that checks the SEQUENCE. A write that reached
// the wrong path, or that sorted the rungs on the way out, would still be
// observed leaving, so the assertion is on what the server kept.
test('reordering the shape order writes it alone, and the server keeps it', async ({page, api}) => {
  await page.goto('/admin/autoscaler');
  const controls = page.locator('app-autoscaler-controls');
  await expect(controls.getByText('when stocked out').first()).toBeVisible();

  const otherPuts: string[] = [];
  page.on('request', r => {
    if (r.method() === 'PUT' && /\/fleet\/(autoscaler|tuning|schedules)$/.test(r.url())) {
      otherPuts.push(r.url());
    }
  });

  const put = page.waitForRequest(
    r => r.url().endsWith('/system-config/fleet/ladder') && r.method() === 'PUT'
  );
  await page.getByRole('button', {name: 'Move n1-standard-8 up'}).click();
  await page.getByRole('button', {name: 'Add a shape'}).click();
  await page.getByRole('button', {name: 'Save shape order'}).click();
  await put;

  await expect
    .poll(() => api.settings.fleet_shape_ladder)
    .toEqual(['n1-standard-8', 'n2-standard-8', 'n2-standard-16']);
  expect(otherPuts).toEqual([]);
  expect(api.settings.fleet_schedules).toHaveLength(1);
});

// The shape is picked from the catalog rather than typed: the catalog is the
// only thing that knows which shapes can run Cuttlefish here, and a typo is
// otherwise indistinguishable from a shape that was never admitted.
test('the preferred shape is chosen from the shapes the catalog admits', async ({page, api}) => {
  await page.goto('/admin/autoscaler');

  await page.getByRole('button', {name: 'n2-standard-8, change this shape'}).click();
  await expect(page.getByRole('menuitem', {name: 'n2-standard-16'})).toBeVisible();
  await page.getByRole('menuitem', {name: 'n2-standard-16'}).click();
  await page.getByRole('button', {name: 'Save shape order'}).click();

  await expect
    .poll(() => api.settings.fleet_shape_ladder)
    .toEqual(['n2-standard-16', 'n1-standard-8']);
});

// The order is a picture of what provisioning is doing, not a list of what an
// operator typed: the shape it prefers is out of capacity everywhere it could
// go, and the one below it is what machines are being built as.
test('the order says what the fleet is building and what it fell past', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const controls = page.locator('app-autoscaler-controls');
  await expect(controls.getByText('stocked out · 2 of 2 zones')).toBeVisible();
  await expect(controls.getByText('building here now')).toBeVisible();
  await expect(controls.getByText('3 slots per machine').first()).toBeVisible();

  // And the same order, once, on the drawing of the machines it produces.
  await expect(page.locator('app-topology-canvas .shapes-note')).toHaveText(
    'n2-standard-8 stocked out, fell back to n1-standard-8'
  );
});

// Refusing is the half of this setting that matters, and the refusal names a
// RUNG, so it is rendered beside the order instead of only thrown at a
// snackbar that is gone before the list can be read against it.
test('a shape the server refuses is stated beside the order', async ({page, api}) => {
  await page.goto('/admin/autoscaler');
  await expect(page.getByRole('button', {name: 'Move n1-standard-8 up'})).toBeVisible();

  // The catalog moved under an open page. Someone disabled the shape this order
  // prefers. The picker offered it because that is what it had read, and the
  // server is what decides. A client-side check cannot cover that case.
  api.machineTypes = api.machineTypes.map(mt =>
    mt.id === 'n2-standard-8' ? {...mt, is_enabled: false} : mt
  );

  await page.getByRole('button', {name: 'Move n1-standard-8 up'}).click();
  await page.getByRole('button', {name: 'Save shape order'}).click();

  await expect(page.locator('app-autoscaler-controls .failure')).toContainText(
    '"n2-standard-8" is disabled in the machine-type catalog'
  );
  // And nothing was stored: a refused order must not half-apply.
  expect(api.settings.fleet_shape_ladder).toEqual(['n2-standard-8', 'n1-standard-8']);
});

// The editor is DOM the route-level scan never sees in this state, a revealed
// explanation over a list of pickers. Both schemes, because every colour role
// carries two values and neither run is a proxy for the other.
for (const scheme of ['light', 'dark'] as const) {
  test(`the shape order editor has no accessibility violations in ${scheme}`, async ({page}) => {
    await page.emulateMedia({colorScheme: scheme});
    await page.goto('/admin/autoscaler');
    await expect(page.getByRole('button', {name: 'Move n1-standard-8 up'})).toBeVisible();
    await page
      .locator('app-autoscaler-controls [aria-describedby="help-ladder-default"].ask')
      .hover();
    await expect(page.locator('app-autoscaler-controls #help-ladder-default')).toBeVisible();

    const {violations} = await new AxeBuilder({page})
      .include('app-autoscaler-controls')
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();

    expect(
      violations.map(v => v.id),
      `\n  ${violations.map(v => `${v.id}: ${v.help}`).join('\n  ')}`
    ).toEqual([]);
  });
}

// The compiled facts are stated as facts. Rendering them is what stops the next
// person adding environment variables for them.
test('states the compiled limits as facts rather than controls', async ({page}) => {
  await page.goto('/admin/autoscaler');

  const strip = page.locator('app-limits-strip');
  await expect(strip.getByText('Changing one is a deploy, not a setting.')).toBeVisible();
  await expect(strip.getByText('provisions / cycle')).toBeVisible();
  await expect(strip.locator('input')).toHaveCount(0);
});

// The card on the operations page is a summary now. A second editor for one
// value is the defect this page removed.
test('the Control Plane card summarises and links here rather than editing', async ({page}) => {
  await page.goto('/admin/control-plane');

  const card = page.locator('app-reconciler-panel .card').filter({hasText: 'Fleet keeper'});
  await expect(card.getByRole('link', {name: 'Autoscaler'})).toBeVisible();
  await expect(card.locator('.mode-select')).toHaveCount(0);
  await expect(card.locator('.floor-input')).toHaveCount(0);
});
