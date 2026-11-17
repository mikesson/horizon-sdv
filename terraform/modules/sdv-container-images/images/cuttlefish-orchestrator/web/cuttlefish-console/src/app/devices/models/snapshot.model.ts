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

import type {SnapshotState} from '../../shared/constants';

/**
 * One entry in a device's snapshot inventory.
 *
 * Host-pinned: the bytes live on the device's host VM and die with its disk
 * (re-provision, preemption, expiry). `lost` is that record. Restore works
 * onto the same stopped device only.
 */
export interface Snapshot {
  id: string;
  deviceId: string;
  hostId: string;
  state: SnapshotState;
  createdBy?: string;
  createdAt: Date;
  updatedAt: Date;
}
