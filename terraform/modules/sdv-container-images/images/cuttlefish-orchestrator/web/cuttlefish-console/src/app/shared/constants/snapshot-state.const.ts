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
 * The states a device snapshot can report.
 *
 * Mirrors the `enum` the API publishes for `Snapshot.state`;
 * `npm run check:schema-drift` fails when the two diverge. `lost` means the
 * host's local disk was recreated after the snapshot was taken, so the bytes
 * are gone and only the record remains.
 */
export const SNAPSHOT_STATE = {
  CREATING: 'creating',
  READY: 'ready',
  LOST: 'lost',
} as const;

/** A state the API can report for a device snapshot. */
export type SnapshotState = (typeof SNAPSHOT_STATE)[keyof typeof SNAPSHOT_STATE];
