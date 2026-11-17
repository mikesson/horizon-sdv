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

import {expect, test} from './fixtures/api-mock';
import {E2E_DEVICE_SERVERS, installStreamingMocks, installWebRTCStub} from './fixtures/webrtc-stub';

// The player streams through a stubbed RTCPeerConnection (real media cannot
// flow in CI) over mocked HTTP long-poll signaling. See fixtures/webrtc-stub.

test('streams a device through the WebRTC pipeline', async ({page, pageErrors}) => {
  await installWebRTCStub(page);
  const streaming = await installStreamingMocks(page);

  await page.goto('/devices/device-1/player');
  await expect(page.getByRole('heading', {name: 'my-device'})).toBeVisible();

  // Negotiation completed: the display_0 track renders as a display tile.
  await expect(page.getByText('Display 0')).toBeVisible();

  // The wire carried exactly the negotiation we expect: a request-offer, then
  // our answer. The request-offer carries the DEVICE leg from /ice-config.
  // The client leg would be a list the device has no route to.
  expect(streaming.connects).toBe(1);
  expect(streaming.forwards.map(f => f.type)).toEqual(['request-offer', 'answer']);
  expect(streaming.forwards[0].ice_servers).toEqual(E2E_DEVICE_SERVERS);

  expect(pageErrors).toEqual([]);
});

test('stream toggle tears down and re-establishes the connection', async ({page}) => {
  await installWebRTCStub(page);
  const streaming = await installStreamingMocks(page);

  await page.goto('/devices/device-1/player');
  await expect(page.getByText('Display 0')).toBeVisible();

  await page.getByRole('switch').click();
  await expect(page.getByText('WebRTC stream is disabled')).toBeVisible();
  await expect(page.getByText('Display 0')).toBeHidden();

  // Re-enabling negotiates a fresh connection (second signaling connect).
  await page.getByRole('switch').click();
  await expect(page.getByText('Display 0')).toBeVisible();
  await expect(() => expect(streaming.connects).toBe(2)).toPass();
});

test('streams on a STUN-only configuration', async ({page}) => {
  await installWebRTCStub(page);
  const streaming = await installStreamingMocks(page);

  // A deployment with no relay is not a failure to recover from: the endpoint
  // answers with STUN alone and the session negotiates on it. (Live-fire
  // 2026-07-08: a TURN-mandatory path was the console black screen.)
  await page.route(/\/v2\/devices\/[^/]+\/ice-config(\?|$)/, route =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        client_servers: [{urls: ['stun:stun.e2e.example:19302']}],
        device_servers: [{urls: ['stun:stun.e2e.example:19302']}],
        ice_transport_policy: 'all',
      }),
    })
  );

  await page.goto('/devices/device-1/player');
  await expect(page.getByText('Display 0')).toBeVisible();

  const iceServers = streaming.forwards[0].ice_servers as Array<Record<string, unknown>>;
  expect(iceServers).toEqual([{urls: ['stun:stun.e2e.example:19302']}]);
});

test('connection failure shows the error state and retry recovers', async ({page}) => {
  await installWebRTCStub(page);
  await installStreamingMocks(page);

  // First signaling connect fails; later ones fall through to the good mock.
  let failConnect = true;
  await page.route(/\/v2\/devices\/[^/]+\/instances\/[^/]+\/signaling\/connect$/, route => {
    if (!failConnect) {
      return route.fallback();
    }
    failConnect = false;
    return route.fulfill({
      status: 500,
      contentType: 'application/problem+json',
      body: JSON.stringify({title: 'Error', status: 500, detail: 'Simulated failure'}),
    });
  });

  await page.goto('/devices/device-1/player');

  // An initial connect failure is surfaced instead of an eternal spinner.
  await expect(page.getByText('Streaming connection failed')).toBeVisible();

  // Once the backend recovers, Retry brings the stream up.
  await page.getByRole('button', {name: 'Retry'}).click();
  await expect(page.getByText('Display 0')).toBeVisible();
});

test('screenshot button surfaces a clear error when no frame has arrived', async ({page}) => {
  await installWebRTCStub(page);
  await installStreamingMocks(page);

  await page.goto('/devices/device-1/player');
  await expect(page.getByText('Display 0')).toBeVisible();

  // The stubbed track never produces frames (videoWidth stays 0), so the
  // instant canvas capture must fail loudly instead of downloading junk.
  await page.getByRole('button').filter({hasText: 'photo_camera'}).click();
  await expect(page.getByText(/No video frame available yet/)).toBeVisible();
});
