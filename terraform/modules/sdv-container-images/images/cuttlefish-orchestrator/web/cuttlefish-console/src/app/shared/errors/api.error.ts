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

import type {z} from 'zod';

// V8 engines (Chrome, Node.js) have captureStackTrace but it's not in lib.es
// Using a type alias for the constructor opt to avoid the `Function` type lint error
type ErrorConstructorType = abstract new (...args: never) => Error;
declare global {
  interface ErrorConstructor {
    captureStackTrace?(targetObject: object, constructorOpt?: ErrorConstructorType): void;
  }
}

/**
 * Base error for all API-related errors.
 *
 * Use this for HTTP errors, network failures, and general API issues.
 */
export class ApiError extends Error {
  override readonly name: string = 'ApiError';

  constructor(
    message: string,
    readonly statusCode?: number,
    readonly originalError?: unknown
  ) {
    super(message);
    // Maintains proper stack trace for where our error was thrown (V8 only)
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, ApiError);
    }
  }

  /**
   * Check if this is a client error (4xx)
   */
  isClientError(): boolean {
    return this.statusCode !== undefined && this.statusCode >= 400 && this.statusCode < 500;
  }

  /**
   * Check if this is a server error (5xx)
   */
  isServerError(): boolean {
    return this.statusCode !== undefined && this.statusCode >= 500 && this.statusCode < 600;
  }

  /**
   * Check if this is a network/connectivity error
   */
  isNetworkError(): boolean {
    return this.statusCode === undefined || this.statusCode === 0;
  }
}

/**
 * Error thrown when API response fails Zod schema validation.
 *
 * This indicates the backend returned data in an unexpected format.
 */
export class ApiValidationError extends ApiError {
  override readonly name: string = 'ApiValidationError';

  constructor(
    message: string,
    readonly issues: z.ZodIssue[]
  ) {
    super(message, 422);
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, ApiValidationError);
    }
  }

  /**
   * Get a formatted string of all validation issues
   */
  getIssuesSummary(): string {
    return this.issues.map(issue => `${issue.path.join('.')}: ${issue.message}`).join('; ');
  }
}

/**
 * Error thrown when a requested resource is not found.
 */
export class NotFoundError extends ApiError {
  override readonly name: string = 'NotFoundError';

  constructor(resource: string, id?: string) {
    const message = id ? `${resource} with ID '${id}' not found` : `${resource} not found`;
    super(message, 404);
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, NotFoundError);
    }
  }
}

/**
 * Structured error details for catalog validation failures.
 */
export interface CatalogErrorDetails {
  /** Classified error type (e.g., 'quota_exceeded', 'resource_unavailable'). */
  errorType?: string;
  suggestion?: string;
  alternativeZones?: string[];
  retryable?: boolean;
}

/**
 * Error thrown when GCE catalog validation fails during host creation.
 *
 * This error contains structured information to help users understand
 * why their host configuration is invalid and what they can do to fix it.
 */
export class CatalogValidationError extends ApiError {
  override readonly name: string = 'CatalogValidationError';

  constructor(
    message: string,
    readonly details: CatalogErrorDetails
  ) {
    super(message, 400);
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, CatalogValidationError);
    }
  }

  /**
   * Get a human-readable label for the error type.
   */
  getErrorTypeLabel(): string {
    const labels: Record<string, string> = {
      quota_exceeded: 'Quota Exceeded',
      resource_unavailable: 'Resource Unavailable',
      zone_unavailable: 'Zone Unavailable',
      permission_denied: 'Permission Denied',
      configuration_error: 'Configuration Error',
      network_error: 'Network Error',
      internal_error: 'Internal Error',
    };
    return labels[this.details.errorType || ''] || 'Validation Error';
  }

  /**
   * Check if alternative zones are available.
   */
  hasAlternativeZones(): boolean {
    return (this.details.alternativeZones?.length ?? 0) > 0;
  }
}
