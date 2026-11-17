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

import type {Host} from './host.model';

/** The per-request bounds POST /v2/hosts/{id}/extend publishes for `hours`. */
export const HOST_EXTEND_MIN_HOURS = 1;
export const HOST_EXTEND_MAX_HOURS = 168;

/**
 * The teardown margin the server requires between a host's expiry and its VM
 * stop time (GracePeriodSeconds in the host domain).
 */
const HOST_GRACE_MS = 15 * 60 * 1000;

const HOUR_MS = 60 * 60 * 1000;

/**
 * Whether an extend can succeed. The server refuses every other class because
 * the fleet keeper, collapse and batch settlement own those leases, and a
 * protected host has no expiry to raise. The VM-horizon refusal depends on
 * the hours chosen, so the dialog surfaces it instead of this rule.
 */
export function canExtendHost(host: Host): boolean {
  return (
    host.hostClass === 'reserved' &&
    host.status !== 'deleting' &&
    host.status !== 'deleted' &&
    host.lifecycle?.mode !== 'protected' &&
    host.lifecycle?.expiresAt !== undefined
  );
}

/** Whether `hours` is a whole number the server accepts in one request. */
export function isValidExtendHours(hours: number): boolean {
  return (
    Number.isInteger(hours) && hours >= HOST_EXTEND_MIN_HOURS && hours <= HOST_EXTEND_MAX_HOURS
  );
}

/** The expiry the server would set: counted from the later of now and the current expiry. */
export function extendedExpiry(expiresAt: Date, hours: number, now: Date): Date {
  const base = Math.max(expiresAt.getTime(), now.getTime());
  return new Date(base + hours * HOUR_MS);
}

/** Whether the server's VM-horizon check would refuse `expiry`: expiry plus the grace must not pass the stop. */
export function passesVmStop(expiry: Date, vmTerminatesAt: Date): boolean {
  return expiry.getTime() + HOST_GRACE_MS > vmTerminatesAt.getTime();
}
