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
 * Quota scope indicating where the quota applies. It is always the single
 * global platform scope.
 */
export type QuotaScope = 'platform';

/**
 * Represents resource quota and usage statistics.
 *
 * Currently tracks **Host** usage, as Devices are limited by the capacity of allocated Hosts.
 * This entity matches the backend `QuotaResponse` DTO.
 */
export interface Quota {
  /** Scope of the quota (always platform). */
  scope: QuotaScope;
  /** Identifier for the scope (always global). */
  scopeId: string;
  /** Hard limit on the number of active Hosts allowed. */
  maxHosts: number;
  /** Current count of active Hosts. */
  usedHosts: number;
  /** Remaining Host capacity before limit is reached. */
  available: number;
  /** Usage percentage (0-100). */
  percentage: number;
  /** Peak host usage recorded within the last 7 days (0 = none recorded). */
  highWaterUsedHosts: number;
  /** When the peak was recorded; null when no watermark exists yet. */
  highWaterAt: Date | null;
}

/**
 * Payload for updating a quota limit.
 */
export interface UpdateQuotaRequest {
  /** New maximum host limit. */
  maxHosts: number;
}

/**
 * Returns a human-readable label for the quota scope.
 */
export function getQuotaScopeLabel(_scope: QuotaScope): string {
  return 'Platform';
}
