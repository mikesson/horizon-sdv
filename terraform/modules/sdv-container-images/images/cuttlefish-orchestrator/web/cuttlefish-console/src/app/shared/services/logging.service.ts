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

import {isDevMode, Service} from '@angular/core';

/**
 * Simple logging service that gates debug output in production.
 *
 * Uses native console methods with environment-based filtering:
 * - debug: Only in development mode (hidden by default in DevTools)
 * - info/warn/error: Always logged
 *
 * @example
 * ```typescript
 * private readonly logger = inject(LoggingService);
 *
 * this.logger.debug('WebRTC', 'ICE candidate', {candidate});
 * this.logger.error('WebRTC', 'Connection failed', error);
 * ```
 */
@Service()
export class LoggingService {
  private readonly isProduction = !isDevMode();

  /**
   * Logs debug-level messages. Only outputs in development mode.
   * Uses console.debug which is hidden by default in DevTools.
   *
   * @param data - Optional structured data to log
   */
  debug(tag: string, message: string, data?: unknown): void {
    if (!this.isProduction) {
      if (data !== undefined) {
        // biome-ignore lint/suspicious/noConsole: console.debug is this service's dev-only channel, hidden by default in DevTools.
        console.debug(`[${tag}] ${message}`, data);
      } else {
        // biome-ignore lint/suspicious/noConsole: console.debug is this service's dev-only channel, hidden by default in DevTools.
        console.debug(`[${tag}] ${message}`);
      }
    }
  }

  /**
   * Logs info-level messages. Always outputs.
   *
   * @param data - Optional structured data to log
   */
  info(tag: string, message: string, data?: unknown): void {
    if (data !== undefined) {
      console.info(`[${tag}] ${message}`, data);
    } else {
      console.info(`[${tag}] ${message}`);
    }
  }

  /**
   * Logs warning-level messages. Always outputs.
   *
   * @param data - Optional structured data to log
   */
  warn(tag: string, message: string, data?: unknown): void {
    if (data !== undefined) {
      console.warn(`[${tag}] ${message}`, data);
    } else {
      console.warn(`[${tag}] ${message}`);
    }
  }

  /**
   * Logs error-level messages. Always outputs.
   *
   * @param data - Optional structured data to log (typically an Error object)
   */
  error(tag: string, message: string, data?: unknown): void {
    if (data !== undefined) {
      console.error(`[${tag}] ${message}`, data);
    } else {
      console.error(`[${tag}] ${message}`);
    }
  }
}
