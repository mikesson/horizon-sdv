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

import type {DeviceWire} from './fixtures/api-fixtures';
import {device} from './fixtures/api-fixtures';
import {expect, test} from './fixtures/api-mock';
import {installStreamingMocks, installWebRTCStub} from './fixtures/webrtc-stub';

// A device is one leased, placed and billed row holding N Android VMs. These
// arms assert the console addresses each of them by name, and that a
// single-instance device is the degenerate case that gained no new furniture.

/** A phone beside a watch, the workload cvd groups exist for. */
function phoneAndWatch(): DeviceWire {
  return device({
    instance_count: 2,
    instances: [
      {
        name: 'phone',
        state: 'Running',
        webrtc_device_id: 'cvdgroup-phone-1',
        adb_port: 6520,
        slot: 1,
        displays: ['720 x 1280 ( 320 )'],
      },
      {
        name: 'watch',
        state: 'Running',
        webrtc_device_id: 'cvdgroup-watch-2',
        adb_port: 6521,
        slot: 2,
        displays: ['320 x 320 ( 240 )'],
      },
    ],
  });
}

test('a group renders one tile per instance and re-scopes on select', async ({page, api}) => {
  api.devices = [
    device({
      instance_count: 3,
      // Slots 2, 5 and 8 on purpose. cvd takes instance ids host-globally and
      // lowest-free-first, one at a time, so a group placed on a host that has
      // been churning holds whichever were free. A fixture numbered 1, 2, 3
      // cannot tell a view that reads the slot from one that reads the array
      // position. The ADB ports move with them; the two are the same fact.
      instances: [
        {name: 'ivi', state: 'Running', adb_port: 6521, slot: 2, displays: ['1920 x 1080 ( 160 )']},
        {
          name: 'cluster',
          state: 'Running',
          adb_port: 6524,
          slot: 5,
          displays: ['1280 x 720 ( 160 )'],
        },
        {name: 'watch', state: 'Boot failed', adb_port: 6527, slot: 8},
      ],
    }),
  ];

  await page.goto('/devices/device-1');

  // COUNT, not visibility: a locator that matches nothing passes toBeHidden,
  // which is how "renders from instances[0] only" would slip through.
  const tiles = page.getByRole('button', {name: 'Instance actions'});
  await expect(tiles).toHaveCount(3);

  // Each instance is named and carries its own observed facts, so the tiles
  // are three different things rather than one repeated.
  await expect(page.getByRole('button', {name: 'ivi', exact: true})).toBeVisible();
  await expect(page.getByText('320 x 320 ( 240 )')).toBeHidden();
  await expect(page.getByText('1280 x 720 ( 160 )')).toBeVisible();
  // The tile writes the canonical word, not the raw wire state: Prepare failed
  // and Boot failed both read "failed" because they route to the same recreate,
  // and which one it was lives in the operation's error.
  await expect(page.getByText('failed', {exact: true})).toBeVisible();

  // The count is server-derived from the same instances the backend folded
  // into the device's status. That is the only accurate answer where a green
  // device could otherwise hide a dead sibling.
  await expect(page.getByText('2 of 3 healthy')).toBeVisible();

  // The upper card block is scoped to the first instance until another is
  // picked. The heading that states the scope is also the control that
  // changes it, so the scope is never something only a remembered click knows.
  const scope = page.getByRole('radiogroup', {name: 'Instance'});
  await expect(scope.getByRole('radio', {name: 'ivi'})).toBeChecked();

  await scope.getByRole('radio', {name: 'cluster'}).click();
  await expect(scope.getByRole('radio', {name: 'cluster'})).toBeChecked();
  await expect(scope.getByRole('radio', {name: 'ivi'})).not.toBeChecked();

  // A tile click re-scopes too. The gesture stays, and it gained a witness
  // that is itself a control.
  await page.getByRole('button', {name: 'watch', exact: true}).click();
  await expect(scope.getByRole('radio', {name: 'watch'})).toBeChecked();

  await expect(page.getByRole('heading', {name: 'Device', exact: true})).toBeVisible();
});

test('a tile opens the player already focused on its own instance', async ({page, api}) => {
  api.devices = [phoneAndWatch()];
  api.openOperations = [];

  await page.goto('/devices/device-1');

  // Second tile, so a player that ignored the request would land on `phone`
  // and pass a check that only asserted the route.
  await page.getByRole('button', {name: 'Instance actions'}).nth(1).click();
  await page.getByRole('menuitem', {name: 'Open in player'}).click();

  await expect(page).toHaveURL(/\/devices\/device-1\/player\?instance=watch/);
});

test('a single-instance device grows no instances section', async ({page, api}) => {
  api.devices = [device()];

  await page.goto('/devices/device-1');
  await expect(page.getByText('Screen recordings')).toBeVisible();

  // No tiles, no dividers, no health line: every group affordance is a section
  // that is simply absent at N=1.
  await expect(page.getByRole('button', {name: 'Instance actions'})).toHaveCount(0);
  await expect(page.getByRole('heading', {name: 'Instances'})).toHaveCount(0);
  await expect(page.getByRole('heading', {name: /^Instance · /})).toHaveCount(0);
  await expect(page.getByText(/\d+ of \d+ healthy/)).toHaveCount(0);

  // And the per-instance verbs stay in the header, where they are unambiguous.
  await expect(page.getByRole('button', {name: 'Device actions'})).toBeVisible();

  // The slot scale is not a group affordance and does not leave with them. A
  // lone instance still occupies one of the host's slots, still shares the wire
  // with every other group there, and has no other way to see the host filling
  // up around it.
  await expect(page.getByRole('list', {name: /Instance slots on/})).toBeVisible();
});

test('a live verb closes the instance menus and names what holds them', async ({page, api}) => {
  api.devices = [phoneAndWatch()];
  api.openOperations = [
    {
      name: 'operations/op-live',
      done: false,
      metadata: {
        verb: 'device.powerwash',
        resource_type: 'device',
        resource_id: 'device-1',
        create_time: new Date().toISOString(),
        instance_name: 'phone',
      },
    },
  ];

  // Admission allows one exclusive operation per DEVICE, so a powerwash on the
  // phone refuses a verb on the watch. The console says so up front rather
  // than letting the user earn a 409. It also says which instance is holding
  // it, because the operation row records that and an anonymous wait reads as
  // a broken page.
  await page.goto('/devices/device-1');
  const menus = page.getByRole('button', {name: 'Instance actions'});
  await expect(menus).toHaveCount(2);
  await expect(menus.first()).toBeDisabled();
  await expect(menus.nth(1)).toBeDisabled();

  // The holder says what it is doing; the sibling says who it is waiting on.
  // Counts are the assertion. A line rendered on every tile names nothing.
  await expect(page.getByText('powerwash running')).toHaveCount(1);
  await expect(page.getByText('waiting on phone')).toHaveCount(1);

  // With nothing in flight the same menus open.
  api.openOperations = [];
  await page.reload();
  await expect(menus.first()).toBeEnabled();
  await expect(page.getByText('powerwash running')).toHaveCount(0);
  await menus.first().click();
  await expect(page.getByRole('menuitem', {name: 'Powerwash'})).toBeVisible();
});

test('the player streams the instance the URL names and stills the rest', async ({page, api}) => {
  api.devices = [phoneAndWatch()];
  await installWebRTCStub(page);
  const streaming = await installStreamingMocks(page);

  await page.goto('/devices/device-1/player?instance=watch');
  await expect(page.getByText('Display 0')).toBeVisible();

  // The named instance is the one negotiated with. Both spellings of the route
  // answer with success, so the requested path is the only witness.
  expect(streaming.connectedInstances).toEqual(['watch']);

  // Exactly one still: the focused instance is on screen live, and the other
  // costs one screenshot rather than a second WebRTC session.
  const still = page.getByAltText('Screen of phone');
  await expect(still).toBeVisible();
  await expect(page.getByAltText(/^Screen of /)).toHaveCount(1);

  // The live instance shows its actual video in the strip, not a badge. It
  // already holds decoded media; rendering a label beside it made the instance
  // being paid for with a peer connection the one you could see least of.
  await expect(page.locator('.instance-strip video')).toHaveCount(1);

  // A still must not be able to pass for a live stream. A frozen tile that
  // looks live reads as a working device, and a hung one reads as fine. It is
  // dimmed, and it says how old it is.
  await expect(still).toHaveCSS('opacity', '0.55');
  await expect(page.getByText(/\d+[smh] ago/)).toBeVisible();

  // The guest's own proportions, not a uniform box: the watch is square and
  // the phone is portrait, which is the heterogeneity a group exists for.
  const watchFrame = page.getByRole('button', {name: /watch/}).locator('.frame');
  const phoneFrame = still.locator('..');
  const watchBox = await watchFrame.boundingBox();
  const phoneBox = await phoneFrame.boundingBox();
  if (!(watchBox && phoneBox)) {
    throw new Error('instance thumbnails are not laid out');
  }
  expect(watchBox.width / watchBox.height).toBeCloseTo(1, 1);
  expect(phoneBox.width).toBeLessThan(watchBox.width);
});

test('the player switcher connects the instance it names', async ({page, api}) => {
  api.devices = [phoneAndWatch()];
  await installWebRTCStub(page);
  const streaming = await installStreamingMocks(page);

  await page.goto('/devices/device-1/player');
  await expect(page.getByText('Display 0')).toBeVisible();
  expect(streaming.connectedInstances).toEqual(['phone']);

  await page.getByRole('button', {name: 'phone', exact: true}).click();
  await page.getByRole('menuitem', {name: 'watch'}).click();

  await expect(() => expect(streaming.connectedInstances).toEqual(['phone', 'watch'])).toPass();
  await expect(page).toHaveURL(/instance=watch/);
});

test('the player can put a whole small group on screen at once', async ({page, api}) => {
  api.devices = [phoneAndWatch()];
  await installWebRTCStub(page);
  const streaming = await installStreamingMocks(page);

  await page.goto('/devices/device-1/player');
  await expect(page.getByText('Display 0')).toBeVisible();
  expect(streaming.connectedInstances).toEqual(['phone']);

  await page.getByRole('button', {name: 'Stream every instance'}).click();

  // Both negotiated and both rendering, the pairing case the grid exists for.
  await expect(() => expect(streaming.connectedInstances).toEqual(['phone', 'watch'])).toPass();
  await expect(page.locator('.instance-strip video')).toHaveCount(2);

  // With nothing left to connect the affordance retires rather than lying.
  await expect(page.getByRole('button', {name: 'Stream every instance'})).toHaveCount(0);
});

test('the list stops reporting one instance as the whole device', async ({page, api}) => {
  api.devices = [phoneAndWatch()];

  await page.goto('/devices');
  await expect(page.getByText('2 instances')).toBeVisible();

  // gpu_mode and the ADB port belong to an instance. The row carried the first
  // one's as if it were the device's; now it declines to answer.
  await expect(page.getByText('ADB: 6520')).toHaveCount(0);
  // exact, because an accessible-name match is a substring match by default
  // and most cells on this row carry a hyphen somewhere in their text.
  await expect(page.getByRole('cell', {name: '-', exact: true})).toHaveCount(2);
});
