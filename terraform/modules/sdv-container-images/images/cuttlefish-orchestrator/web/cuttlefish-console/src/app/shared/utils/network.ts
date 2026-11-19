/**
 * Copyright 2026 Google LLC
 * SPDX-License-Identifier: Apache-2.0
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

const DECIMAL_OCTET_PATTERN = /^(?:0|[1-9]\d{0,2})$/;

/**
 * Returns true strictly for valid dotted-decimal RFC1918 private IPv4
 * addresses (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16).
 */
export function isPrivateIPv4(ip: string): boolean {
  const parts = ip.trim().split('.');
  if (parts.length !== 4) {
    return false;
  }

  const octets = parts.map(part =>
    DECIMAL_OCTET_PATTERN.test(part) ? Number.parseInt(part, 10) : -1
  );
  if (octets.some(octet => octet < 0 || octet > 255)) {
    return false;
  }

  const [first, second] = octets;
  return (
    first === 10 ||
    (first === 172 && second >= 16 && second <= 31) ||
    (first === 192 && second === 168)
  );
}
