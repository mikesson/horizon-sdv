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
 * Network-layer mock for the CCO backend. The SPA validates every response
 * with Zod (throws on mismatch), so the served bodies are byte-shaped like the
 * real API: snake_case keys, required pagination fields, valid enums.
 *
 * This file is transport only, meaning Playwright route registration and
 * stateful request handling. The data and every response body live in
 * `api-fixtures.ts`, which is what lets `scripts/gates/check-fixture-shapes.ts`
 * parse the exact served shapes through the console's own schemas.
 *
 * The mock is stateful. POSTs append to the in-memory collections so a created
 * resource shows up in subsequent list/detail reads, letting a full create
 * journey be asserted end to end without a backend.
 */

import {test as base, type Page, type Route} from '@playwright/test';
import {
  type ApiState,
  type ArtifactBucketsWire,
  adminFamiliesBody,
  adminStatsBody,
  adminZonesBody,
  auditPageBody,
  autoscalerBody,
  type BatchWire,
  type BuildWire,
  batch,
  batchAdmissionBody,
  batchMembersBody,
  booleanSettingBody,
  bugreportBody,
  build,
  byCreator,
  capacityBody,
  catalogFreshnessBody,
  collection,
  controlPlaneBody,
  createdBatchBody,
  createdBuildBody,
  createdDeviceBody,
  createdHostBody,
  type DeviceWire,
  defaultState,
  defaultUpstreamBuildBody,
  device,
  devicePlanBody,
  dockerHostBody,
  enabledZonesBody,
  type FamilyShapeWire,
  failedTasksBody,
  fleetSettingsBody,
  type HostWire,
  host,
  hostImageConfigListBody,
  iceConfigBody,
  type Json,
  type LifetimesWire,
  lifetimesBody,
  type MachineTypeWire,
  machineTypeListBody,
  numberSettingBody,
  operationsBody,
  problemBody,
  provisioningStatsBody,
  reclaimableBody,
  recordingFetchBody,
  recordingsBody,
  resolveMachineTypeBody,
  screenshotBody,
  shapeLadderRefusal,
  snapshotListBody,
  spotMinUptimeBody,
  standbyRecommendationBody,
  storageBucketsBody,
  storageObjectsBody,
  stringSettingBody,
  taskStatsBody,
  verifyAccessBody,
  zoneAvailabilityBody,
} from './api-fixtures';
import {cssUsageEnabled, recordCssUsage} from './css-usage';

function fulfill(route: Route, body: unknown, status = 200): Promise<void> {
  return route.fulfill({status, contentType: 'application/json', body: JSON.stringify(body)});
}

function idFromUrl(url: string, segment: string): string {
  return url.split(segment)[1].split(/[?/]/)[0];
}

/**
 * Every family's shapes as one sequence. A per-shape curation write lands on
 * both readings of that shape, the served entry and the family's matrix row,
 * because the two are one row on the server.
 */
function familyShapes(state: ApiState): FamilyShapeWire[] {
  return state.machineFamilies.flatMap(family => family.shapes ?? []);
}

/** The two moves the per-shape curation PATCH carries. */
interface ShapePatch {
  is_enabled?: boolean;
  is_default?: boolean;
}

/** Applies a per-shape patch to both readings of the shape. */
function applyShapeCuration(state: ApiState, target: MachineTypeWire, patch: ShapePatch): void {
  if (patch.is_enabled !== undefined) {
    target.is_enabled = patch.is_enabled;
    for (const shape of familyShapes(state)) {
      if (shape.machine_type === target.id) {
        shape.is_excluded = !patch.is_enabled;
      }
    }
  }
  if (patch.is_default !== undefined) {
    for (const mt of state.machineTypes) {
      mt.is_default = false;
    }
    target.is_default = patch.is_default;
    for (const shape of familyShapes(state)) {
      shape.is_default = shape.machine_type === target.id && patch.is_default;
    }
  }
}

/**
 * Registers all backend route handlers on the page. Playwright checks the
 * most-recently-added matching route first, so registration goes
 * general → specific: the `/v2/` catch-all is the final fallback, collection
 * routes sit under item routes.
 */
async function installApiMocks(page: Page, state: ApiState): Promise<void> {
  // Catch-all safety net. It keeps an unmocked endpoint from 404ing into a Zod
  // failure. List-shaped so the common case parses; singletons are handled
  // below. Every hit is recorded: a page that leaned on this fallback rendered
  // against a shape no schema blessed, and state.unhandledRequests is how a
  // spec finds out.
  await page.route(/\/v2\//, route => {
    const url = new URL(route.request().url());
    state.unhandledRequests.push(`${route.request().method()} ${url.pathname}`);
    return fulfill(route, collection([]));
  });

  await page.route(/\/v2\/me(\?|$)/, route => fulfill(route, state.user));
  await page.route(/\/v2\/quota(\?|$)/, route => fulfill(route, state.quota));
  await page.route(/\/v2\/zones\/availability(\?|$)/, route =>
    fulfill(route, zoneAvailabilityBody(state))
  );
  await page.route(/\/v2\/machine-types(\?|$)/, route =>
    fulfill(route, machineTypeListBody(state))
  );
  // Resolving one name is how a custom shape gets its size, and a refusal is an
  // answer rather than a failure, so it comes back as the 404 the backend maps
  // ErrNotFound to, carrying Compute Engine's own sentence.
  await page.route(/\/v2\/machine-types\/[^/?]+/, route => {
    const id = decodeURIComponent(idFromUrl(route.request().url(), '/v2/machine-types/'));
    const answer = resolveMachineTypeBody(state, id);
    return answer.shape
      ? fulfill(route, answer.shape)
      : fulfill(route, problemBody(answer.refusal, 404), 404);
  });
  await page.route(/\/v2\/admin\/host-image-configs(\?|$)/, route =>
    fulfill(route, hostImageConfigListBody(state))
  );
  await page.route(/\/v2\/admin\/system-config\/default-gpu-machine-type(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      state.settings.gpu_machine_type = (route.request().postDataJSON() as {value: string}).value;
    }
    return fulfill(route, stringSettingBody(state.settings.gpu_machine_type, true));
  });

  // The lifetime policy feeds the Settings card, both create wizards, the
  // host form, and the extend dialog; the density key rides the Defaults card.
  await page.route(/\/v2\/admin\/system-config\/lifetimes(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as Partial<LifetimesWire>;
      state.settings.lifetimes = {...state.settings.lifetimes, ...body, is_set: true};
    }
    return fulfill(route, lifetimesBody(state));
  });

  await page.route(/\/v2\/admin\/system-config\/docker-host(\?|$)/, route =>
    fulfill(route, dockerHostBody())
  );

  // Three routes, because the two lists have their own PUT. The patterns are
  // disjoint rather than ordered: `(\?|$)` anchors the tail, so the /fleet one
  // does not also match /fleet/schedules, which is what a bare prefix would
  // have done, swallowing the PUT and leaving the spec asserting against a body
  // nothing ever wrote.
  await page.route(/\/v2\/admin\/system-config\/fleet\/schedules(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as {
        schedules: ApiState['settings']['fleet_schedules'];
      };
      state.settings.fleet_schedules = body.schedules;
    }
    return fulfill(route, fleetSettingsBody(state));
  });
  // The shape order, which the server may REFUSE: the catalog decides what can
  // run Cuttlefish here, so a rung it does not admit comes back as a 400 naming
  // the rule. Storing every list would leave the refusal path unreachable from a
  // browser, and it is the half of this setting that has to be rendered.
  await page.route(/\/v2\/admin\/system-config\/fleet\/ladder(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as {shape_ladder: string[]};
      const refusal = shapeLadderRefusal(state, body.shape_ladder);
      if (refusal) {
        return fulfill(route, problemBody(refusal), 400);
      }
      state.settings.fleet_shape_ladder = body.shape_ladder;
    }
    return fulfill(route, fleetSettingsBody(state));
  });
  await page.route(/\/v2\/admin\/system-config\/fleet(\?|$)/, route =>
    fulfill(route, fleetSettingsBody(state))
  );

  await page.route(/\/v2\/admin\/system-config\/default-upstream-build(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as {branch: string; target: string};
      state.settings.upstream_branch = body.branch;
      state.settings.upstream_target = body.target;
    }
    return fulfill(route, defaultUpstreamBuildBody(state));
  });

  // Host-create preselects the operator's zone pin when one is set; the
  // Settings page reads all three placement.* policies.
  // is_set distinguishes an operator's value from the compiled default; the
  // zone pin is stored here and the quota margin is not, so the page has one
  // of each to render.
  await page.route(/\/v2\/admin\/system-config\/zone-pin(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      state.settings.zone_pin = (route.request().postDataJSON() as {value: string}).value;
    }
    return fulfill(route, stringSettingBody(state.settings.zone_pin, true));
  });
  await page.route(/\/v2\/admin\/system-config\/quota-margin(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      state.settings.quota_margin = (route.request().postDataJSON() as {value: number}).value;
      state.settings.quota_margin_is_set = true;
    }
    return fulfill(
      route,
      numberSettingBody(state.settings.quota_margin, state.settings.quota_margin_is_set)
    );
  });
  await page.route(/\/v2\/admin\/system-config\/v1-zone-hint(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      state.settings.v1_zone_hint = (route.request().postDataJSON() as {value: boolean}).value;
    }
    return fulfill(route, booleanSettingBody(state.settings.v1_zone_hint, true));
  });
  await page.route(/\/v2\/admin\/system-config\/spot-auto-default(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      state.settings.spot_auto_default = (route.request().postDataJSON() as {value: boolean}).value;
    }
    return fulfill(route, booleanSettingBody(state.settings.spot_auto_default, true));
  });
  await page.route(/\/v2\/admin\/system-config\/spot-min-uptime(\?|$)/, route =>
    fulfill(route, spotMinUptimeBody())
  );
  await page.route(/\/v2\/admin\/stats(\?|$)/, route => fulfill(route, adminStatsBody(state)));
  await page.route(/\/v2\/admin\/tasks\/stats(\?|$)/, route =>
    fulfill(route, taskStatsBody(state))
  );
  await page.route(/\/v2\/admin\/tasks\/failed(\?|$)/, route => {
    const url = new URL(route.request().url());
    const resourceId = url.searchParams.get('resource_id');
    const tasks = resourceId
      ? state.failedTasks.filter(t => t.resource_id === resourceId)
      : state.failedTasks;
    return fulfill(route, failedTasksBody(tasks));
  });
  // Clearing drops the cell, so the next snapshot poll is what removes the
  // badge. The page never takes it off optimistically.
  await page.route(/\/v2\/admin\/zone-health\/[^/]+\/[^/?]+/, route => {
    const segments = route.request().url().split('/');
    const family = decodeURIComponent(segments[segments.length - 1]);
    const i = state.zoneCooldowns.findIndex(c => c.machine_family === family);
    if (i >= 0) {
      state.zoneCooldowns.splice(i, 1);
    }
    return route.fulfill({status: 204, body: ''});
  });
  await page.route(/\/v2\/admin\/control-plane(\?|$)/, route =>
    fulfill(route, controlPlaneBody(state))
  );
  await page.route(/\/v2\/admin\/capacity(\?|$)/, route => fulfill(route, capacityBody(state)));

  // Anchored tails and the longer path first, for the reason the /fleet pair
  // above is written that way: a bare prefix would let /autoscaler swallow
  // /autoscaler/recommendation and serve the wrong body.
  await page.route(/\/v2\/admin\/autoscaler\/recommendation(\?|$)/, route =>
    fulfill(route, standbyRecommendationBody())
  );
  await page.route(/\/v2\/admin\/autoscaler(\?|$)/, route => fulfill(route, autoscalerBody(state)));

  await page.route(/\/v2\/admin\/catalog\/freshness(\?|$)/, route =>
    fulfill(route, catalogFreshnessBody())
  );

  // The enabled set is replaced whole, so the mock stores the set it is sent
  // and answers with it. A mock that echoed the request without applying it
  // would keep a broken read-modify-write looking green.
  await page.route(/\/v2\/admin\/zones\/enabled(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      const wanted = (route.request().postDataJSON() as {zones: string[]}).zones;
      for (const z of state.adminZones) {
        z.is_enabled = wanted.includes(z.zone);
      }
    }
    return fulfill(route, enabledZonesBody(state));
  });
  await page.route(/\/v2\/admin\/zones(\?|$)/, route => fulfill(route, adminZonesBody(state)));

  // The curation PATCH applies rather than echoes, so a spec that reloads the
  // listing sees what the write actually stored, for the same reason the zone
  // PUT applies its set.
  await page.route(/\/v2\/admin\/machine-families\/[^/?]+/, route => {
    const family = idFromUrl(route.request().url(), '/v2/admin/machine-families/');
    const target = state.machineFamilies.find(f => f.family === family);
    if (target && route.request().method() === 'PATCH') {
      const body = route.request().postDataJSON() as {
        is_enabled?: boolean;
        sort_order?: number;
        max_gpus?: number;
        default_gpu_type?: string;
        default_gpu_count?: number;
      };
      Object.assign(target, body);
    }
    return fulfill(route, target ?? {});
  });
  await page.route(/\/v2\/admin\/machine-families(\?|$)/, route =>
    fulfill(route, adminFamiliesBody(state))
  );

  await page.route(/\/v2\/admin\/machine-types\/[^/?]+/, route => {
    const id = idFromUrl(route.request().url(), '/v2/admin/machine-types/');
    const target = state.machineTypes.find(mt => mt.id === id) ?? state.machineTypes[0];
    if (route.request().method() === 'PATCH') {
      applyShapeCuration(state, target, route.request().postDataJSON() as ShapePatch);
    }
    return fulfill(route, target);
  });

  await page.route(/\/v2\/admin\/audit(\?|$)/, route => {
    const url = new URL(route.request().url());
    const action = url.searchParams.get('action');
    const actor = url.searchParams.get('actor_email');
    const resourceType = url.searchParams.get('resource_type');
    const resourceId = url.searchParams.get('resource_id');
    const kind = url.searchParams.get('kind');
    const entries = state.auditEntries.filter(
      e =>
        (!action || e.action === action) &&
        (!actor || e.actor_email === actor) &&
        (!resourceType || e.resource_type === resourceType) &&
        (!resourceId || e.resource_id === resourceId) &&
        (!kind || e.metadata?.kind === kind)
    );
    return fulfill(route, auditPageBody(entries));
  });

  await page.route(/\/v2\/admin\/system-config\/artifact-buckets(\?|$)/, route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as Partial<ArtifactBucketsWire>;
      state.artifactBuckets.bugreport_bucket = body.bugreport_bucket ?? '';
      state.artifactBuckets.recording_bucket = body.recording_bucket ?? '';
      state.artifactBuckets.capture_on_error = body.capture_on_error ?? false;
    }
    return fulfill(route, state.artifactBuckets);
  });

  // Single-object delete; registered before the listing route so the more
  // specific /objects/<name> path wins.
  await page.route(/\/v2\/storage\/buckets\/[^/]+\/objects\/.+/, route => {
    if (route.request().method() !== 'DELETE') {
      return route.fallback();
    }
    const name = decodeURIComponent(route.request().url().split('/objects/')[1].split('?')[0]);
    const i = state.artifactObjects.findIndex(o => o.name === name);
    if (i >= 0) {
      state.artifactObjects.splice(i, 1);
    }
    return route.fulfill({status: 204, body: ''});
  });

  // Fleet-wide operations. done=false is the Control Plane panel's read; a
  // resource_id query is the device detail card's.
  await page.route(/\/v2\/operations(\?|$)/, route => {
    const url = new URL(route.request().url());
    const resource = url.searchParams.get('resource_id');
    const items = resource
      ? state.openOperations.filter(o => o.metadata.resource_id === resource)
      : state.openOperations;
    return fulfill(route, operationsBody(items));
  });

  // Object listing under the artifact prefix (the device artifacts card).
  await page.route(/\/v2\/storage\/buckets\/[^/]+\/objects(\?|$)/, route => {
    const url = new URL(route.request().url());
    const prefix = url.searchParams.get('prefix') ?? '';
    const objects = state.artifactObjects.filter(o => o.name.startsWith(prefix));
    return fulfill(route, storageObjectsBody(objects));
  });

  await page.route(/\/v2\/storage\/buckets(\?|$)/, route => fulfill(route, storageBucketsBody()));

  await page.route(/\/v2\/storage\/verify-access(\?|$)/, route =>
    fulfill(route, verifyAccessBody())
  );

  await page.route(/\/v2\/admin\/reclaimable(\?|$)/, route => fulfill(route, reclaimableBody()));

  await page.route(/\/v2\/admin\/provisioning-stats(\?|$)/, route =>
    fulfill(route, provisioningStatsBody())
  );

  // Collections (GET list + POST create).
  await page.route(/\/v2\/hosts(\?|$)/, route => {
    const req = route.request();
    if (req.method() === 'POST') {
      const body = req.postDataJSON() as Partial<HostWire> & Json;
      state.idempotencyKeys.host.push(req.headers()['idempotency-key'] ?? '');
      const created = createdHostBody(state, body);
      state.hosts.push(created);
      return fulfill(route, created, 201);
    }
    const url = new URL(req.url());
    return fulfill(route, collection(byCreator(state.hosts, url, state)));
  });

  await page.route(/\/v2\/batches(\?|$)/, route => {
    const req = route.request();
    if (req.method() === 'POST') {
      const body = req.postDataJSON() as Partial<BatchWire> & Json;
      state.idempotencyKeys.batch.push(req.headers()['idempotency-key'] ?? '');
      const count = body.count as number;
      const admission = batchAdmissionBody(state, count, (body.requirements ?? {}) as Json);
      if (body.validate_only) {
        // A plan short of the floor is a rejection, not a smaller plan.
        if ((admission.admitted ?? 0) < (body.min_ready as number)) {
          return fulfill(
            route,
            {
              title: 'Bad Request',
              detail: `insufficient capacity: can admit ${admission.admitted} of ${count} devices`,
            },
            400
          );
        }
        return fulfill(route, admission);
      }
      const created = createdBatchBody(state, body, admission);
      state.batches.push(created);
      return fulfill(route, created, 201);
    }
    const items = byCreator(state.batches, new URL(req.url()), state);
    return fulfill(route, {items, total: items.length});
  });

  await page.route(/\/v2\/devices(\?|$)/, route => {
    const req = route.request();
    if (req.method() === 'POST') {
      const body = req.postDataJSON() as Partial<DeviceWire> & Json;
      // Recorded before the dry-run branch so a spec can see that a plan
      // carried no key while the create that followed it did.
      state.idempotencyKeys.device.push(req.headers()['idempotency-key'] ?? '');

      if (body.validate_only) {
        return fulfill(route, devicePlanBody(state, body));
      }

      const created = createdDeviceBody(state, body);
      state.devices.push(created);
      return fulfill(route, created, 201);
    }
    const url = new URL(req.url());
    return fulfill(route, collection(byCreator(state.devices, url, state)));
  });

  await page.route(/\/v2\/builds(\?|$)/, route => {
    const req = route.request();
    if (req.method() === 'POST') {
      const created = createdBuildBody(state, req.postDataJSON() as Partial<BuildWire> & Json);
      state.builds.push(created);
      return fulfill(route, created, 201);
    }
    return fulfill(route, collection(state.builds));
  });

  // Items: GET by id, action verbs (POST .../start etc.) that echo the
  // resource, and stateful DELETE (removes from the collection → 204) so a
  // deleted row actually disappears from a subsequent list read.
  //
  // The pattern is unanchored on purpose, because the POST verbs depend on the
  // item route answering sub-paths. A GET or DELETE at a sub-path is a resource
  // this mock does not model. GET keeps the historical entity echo but is
  // recorded as unhandled; DELETE answers 204 WITHOUT splicing, because
  // removing the parent on a sub-resource delete would be the mock inventing a
  // cascade.
  const itemRoute = async (
    key: 'hosts' | 'devices' | 'builds' | 'batches',
    segment: string,
    factory: () => HostWire | DeviceWire | BuildWire | BatchWire
  ): Promise<void> => {
    await page.route(new RegExp(`${segment.replace(/\//g, '\\/')}[^/?]+`), route => {
      const coll: Array<{id?: string}> = state[key];
      const after = route.request().url().split(segment)[1];
      const id = after.split(/[?/]/)[0];
      const sub = after.slice(id.length).split('?')[0];
      const method = route.request().method();
      if (method === 'DELETE') {
        if (!sub) {
          const i = coll.findIndex(x => x.id === id);
          if (i >= 0) {
            coll.splice(i, 1);
          }
        } else {
          state.unhandledRequests.push(`DELETE ${segment}${id}${sub}`);
        }
        return route.fulfill({status: 204, body: ''});
      }
      if (sub && method === 'GET') {
        state.unhandledRequests.push(`GET ${segment}${id}${sub}`);
      }
      return fulfill(route, coll.find(x => x.id === id) ?? coll[0] ?? factory());
    });
  };
  await itemRoute('hosts', '/v2/hosts/', host);
  await itemRoute('devices', '/v2/devices/', device);
  await itemRoute('builds', '/v2/builds/', build);
  await itemRoute('batches', '/v2/batches/', batch);
  // Registered after the device item route so the more specific bugreport
  // path wins (Playwright matches newest-first). Captures land as objects in
  // the mocked artifact bucket, so the artifacts card sees them on refresh.
  await page.route(/\/v2\/devices\/[^/?]+\/bugreport/, route => {
    const id = idFromUrl(route.request().url(), '/v2/devices/');
    const dev = state.devices.find(d => d.id === id) ?? {name: 'unknown', id};
    const bucket = state.artifactBuckets.bugreport_bucket || state.artifactBuckets.default_bucket;
    const object = `artifacts/${String(dev.name)}-${String(dev.id)}/bugreports/20260615-100000.zip`;
    state.artifactObjects.push({
      name: object,
      display_name: '20260615-100000.zip',
      bucket,
      size: 4_200_000,
      content_type: 'application/zip',
      updated_at: '2026-06-15T10:00:00Z',
    });
    return fulfill(route, bugreportBody(bucket, object));
  });

  // Screen recordings: start/stop/list/fetch. Registered after the device
  // item route so the more specific /recordings path wins. A recording belongs
  // to one Android VM, so the route carries the instance.
  await page.route(/\/v2\/devices\/[^/?]+\/instances\/[^/?]+\/recordings/, route => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    const id = idFromUrl(req.url(), '/v2/devices/');
    const dev = state.devices.find(d => d.id === id) ?? {name: 'unknown', id};
    if (req.method() === 'POST' && path.endsWith('/recordings/start')) {
      return fulfill(route, {status: 'running'});
    }
    if (req.method() === 'POST' && path.endsWith('/recordings/stop')) {
      return fulfill(route, {status: 'running'});
    }
    if (req.method() === 'POST' && path.endsWith('/fetch')) {
      const name = decodeURIComponent(path.split('/recordings/')[1].replace(/\/fetch$/, ''));
      const bucket = state.artifactBuckets.recording_bucket || state.artifactBuckets.default_bucket;
      const object = `artifacts/${String(dev.name)}-${String(dev.id)}/recordings/${name}`;
      return fulfill(route, recordingFetchBody(bucket, object));
    }
    return fulfill(route, recordingsBody(state));
  });

  // A display capture, per instance. Without it the catch-all answers with a
  // list shape and the player renders a broken image for every instance it is
  // not streaming, which no assertion on the strip would notice.
  await page.route(/\/v2\/devices\/[^/?]+\/instances\/[^/?]+\/displays\/\d+\/screenshot/, route =>
    fulfill(route, screenshotBody())
  );

  // The snapshots inventory used to fall through to the device item route and
  // be answered with a device entity, a shape SnapshotListSchema can never
  // parse. Registered after the item route so it wins.
  await page.route(/\/v2\/devices\/[^/?]+\/snapshots(\?|$)/, route =>
    fulfill(route, snapshotListBody())
  );

  // Served with a real shape so the streaming session's parse succeeds; the
  // WebRTC connection itself is stubbed by webrtc-stub.ts where a spec cares.
  await page.route(/\/v2\/devices\/[^/?]+\/ice-config(\?|$)/, route =>
    fulfill(route, iceConfigBody())
  );

  // Registered after the batch item route so the more specific members path
  // wins (Playwright matches newest-first).
  await page.route(/\/v2\/batches\/[^/?]+\/devices/, route => fulfill(route, batchMembersBody()));
}

/**
 * Force a route to fail with an RFC 9457 problem+json error. Registered after
 * the default mocks, so it wins (Playwright checks the newest match first).
 * Call inside a test before navigating to exercise error UI.
 */
export async function mockError(page: Page, pattern: RegExp, status = 500): Promise<void> {
  // page.route resolves to a Disposable that unregisters the handler. Dropping
  // it is the point, because the route stays for the rest of the test. So this
  // awaits rather than returning it, and the signature stays ours rather than
  // Playwright's.
  await page.route(pattern, route =>
    route.fulfill({
      status,
      contentType: 'application/problem+json',
      body: JSON.stringify({title: 'Error', status, detail: 'Simulated failure'}),
    })
  );
}

interface Fixtures {
  api: ApiState;
  pageErrors: string[];
  // biome-ignore lint/suspicious/noConfusingVoidType: an auto fixture supplies nothing and calls use() with no argument, which `undefined` would forbid.
  cssUsage: void;
}

export const test = base.extend<Fixtures>({
  // Uncaught exceptions surfaced to the page, the hard signal that something
  // broke. Collected for every test; assert emptiness where it matters.
  pageErrors: async ({page}, use) => {
    const errors: string[] = [];
    page.on('pageerror', err => errors.push(err.message));
    await use(errors);
  },
  // Auto-installed so every test boots against the mocked backend (the app
  // calls /v2/me before the first route renders).
  api: [
    async ({page}, use) => {
      const state = defaultState();
      await installApiMocks(page, state);
      await use(state);
    },
    {auto: true},
  ],
  // Records which CSS rules matched something while the test ran. Inert unless
  // CCO_CSS_USAGE=1, so the normal suite is unaffected.
  cssUsage: [
    async ({context, page}, use, testInfo) => {
      if (!cssUsageEnabled()) {
        await use();
        return;
      }
      await recordCssUsage(context, page, testInfo, () => use());
    },
    {auto: true},
  ],
});

// Specs take their whole Playwright surface from this module, so the types
// their helper signatures need come through here too.
export {expect, type Locator, type Page} from '@playwright/test';
// The factories and state type stay importable from here so the specs' single
// import point survives the fixtures/transport split.
export {
  type ApiState,
  batch,
  build,
  device,
  extendedHostBody,
  host,
  leasedHost,
  problemBody,
} from './api-fixtures';
