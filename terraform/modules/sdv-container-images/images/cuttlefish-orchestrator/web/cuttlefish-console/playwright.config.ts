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

import {defineConfig, devices} from '@playwright/test';

const PORT = 4200;
// Point at an already-running instance (e.g. a deployed preview) to skip the
// local dev server. The API is mocked at the network layer regardless, so the
// target only needs to serve the SPA.
const BASE_URL = process.env['E2E_BASE_URL'] ?? `http://localhost:${PORT}`;
const usingExternalTarget = Boolean(process.env['E2E_BASE_URL']);

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env['CI'],
  retries: process.env['CI'] ? 1 : 0,
  workers: process.env['CI'] ? 2 : undefined,
  reporter: process.env['CI']
    ? [['github'], ['html', {open: 'never'}]]
    : [['list'], ['html', {open: 'never'}]],
  // CSS rule-usage recording enables the CSS domain on every page, and Chromium
  // then reports every stylesheet mutation Angular makes. That is enough extra
  // work to blow a five-second wait on a heavy route. Raised only while
  // recording, so the gate the suite normally runs as keeps its tighter bound.
  // 5s was under the dev server's worst case. It compiles a route chunk on
  // demand, and on a four-core box running two workers with video and trace
  // recording on, a cold route left <main> empty past the deadline. A green
  // suite and a red one differed by which test drew the slow route, not by
  // anything in the app. Raised for every mode; the recording mode still needs
  // more room than the rest.
  expect: {timeout: process.env['CCO_CSS_USAGE'] ? 20_000 : 10_000},
  use: {
    baseURL: BASE_URL,
    // Not `on-first-retry`: there is no hosted CI, so the run that matters is
    // the local one, and it has no retries. A failure there produced a video
    // and a screenshot of the symptom and no trace of the cause. Recording
    // costs a run that is already recording video; three unexplained failures
    // that left nothing to read cost more.
    trace: 'retain-on-first-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  projects: [{name: 'chromium', use: {...devices['Desktop Chrome']}}],
  // ng serve is slow to first-compile; give it room. Reused by default so a
  // repeat run is fast, but never under the gate: whatever already holds :4200
  // may be serving another worktree, and a suite that passes against someone
  // else's tree is worse than one that refuses to start. scripts/ci.sh sets
  // CCO_E2E_OWN_SERVER for exactly that reason. CI would also switch on
  // retries and the github reporter, which this repo does not want locally
  // (see the trace note above).
  webServer: usingExternalTarget
    ? undefined
    : {
        command: 'npm start',
        url: BASE_URL,
        timeout: 180_000,
        reuseExistingServer: !process.env['CI'] && !process.env['CCO_E2E_OWN_SERVER'],
        stdout: 'ignore',
        stderr: 'pipe',
      },
});
