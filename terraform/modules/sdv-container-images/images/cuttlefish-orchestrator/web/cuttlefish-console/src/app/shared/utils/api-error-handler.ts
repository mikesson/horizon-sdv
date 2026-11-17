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

import {HttpErrorResponse} from '@angular/common/http';
import {isDevMode} from '@angular/core';
import type {Observable} from 'rxjs';
import {throwError} from 'rxjs';
import {z} from 'zod';
import type {CatalogErrorDetails} from '../errors/api.error';
import {
  ApiError,
  ApiValidationError,
  CatalogValidationError,
  NotFoundError,
} from '../errors/api.error';

/**
 * RFC 9457 "Problem Details" format (used by Huma framework).
 */
interface ProblemDetails {
  type?: string;
  title?: string;
  detail?: string;
  status?: number;
  instance?: string;
  errors?: Array<{message?: string; location?: string}>;
}

/**
 * Legacy error response format.
 */
interface LegacyErrorResponse {
  message?: string;
  error?: string;
}

/**
 * Catalog validation error response with additional fields.
 */
interface CatalogErrorResponse {
  error_type?: string;
  errorType?: string;
  suggestion?: string;
  alternative_zones?: string[];
  alternativeZones?: string[];
  retryable?: boolean;
}

/**
 * Type guard to check if a value is a record object.
 */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * Type guard to check if error body is a ProblemDetails object.
 */
function isProblemDetails(body: unknown): body is ProblemDetails {
  if (!isRecord(body)) {
    return false;
  }
  return 'detail' in body || 'title' in body || 'errors' in body;
}

/**
 * Type guard to check if error body is a legacy error response.
 */
function isLegacyError(body: unknown): body is LegacyErrorResponse {
  if (!isRecord(body)) {
    return false;
  }
  return 'message' in body || 'error' in body;
}

/**
 * Type guard to check if error body contains catalog-specific fields.
 */
function isCatalogError(body: unknown): body is CatalogErrorResponse {
  if (!isRecord(body)) {
    return false;
  }
  return (
    'error_type' in body ||
    'errorType' in body ||
    'suggestion' in body ||
    'alternative_zones' in body ||
    'alternativeZones' in body
  );
}

/**
 * Safely extracts an error message from any error type.
 *
 * This function handles:
 * - Error instances (returns error.message)
 * - HttpErrorResponse (uses extractErrorMessage)
 * - Objects with a message property
 * - Strings (returns the string)
 * - Unknown types (returns the fallback)
 *
 * Use this in catchError handlers to avoid unsafe-any ESLint errors.
 *
 * @param fallback - Fallback message if no message can be extracted.
 *
 * @example
 * catchError(err => {
 *   const message = getErrorMessage(err, 'Operation failed');
 *   snackBar.open(message, 'Close');
 *   return EMPTY;
 * })
 */
export function getErrorMessage(error: unknown, fallback = 'An error occurred'): string {
  if (error instanceof HttpErrorResponse) {
    return extractErrorMessage(error);
  }
  if (error instanceof Error) {
    return error.message;
  }
  if (typeof error === 'string') {
    return error;
  }
  if (isRecord(error) && typeof error['message'] === 'string') {
    return error['message'];
  }
  return fallback;
}

/**
 * Extracts a user-facing error message from diverse backend response formats.
 *
 * STANDARDS COMPLIANCE:
 * Prioritizes the RFC 9457 "Problem Details" format used by the Huma v2 backend.
 * Gracefully degrades to legacy or plain string formats.
 */
export function extractErrorMessage(error: HttpErrorResponse): string {
  const body: unknown = error.error;

  if (typeof body === 'string') {
    return body;
  }

  // RFC 9457 "Problem Details" format (used by Huma framework)
  if (isProblemDetails(body)) {
    // Prefer 'detail' as it contains the specific error message
    if (typeof body.detail === 'string') {
      return body.detail;
    }
    // Fall back to 'title' if no detail
    if (typeof body.title === 'string') {
      return body.title;
    }
  }

  // Legacy format support
  if (isLegacyError(body)) {
    if (typeof body.message === 'string') {
      return body.message;
    }
    if (typeof body.error === 'string') {
      return body.error;
    }
  }

  return error.statusText || 'Unknown error';
}

/**
 * Extracts validation errors from an RFC 9457 response (Huma framework).
 *
 * @returns An array of field-level error messages (e.g., "name: must be at least 3 characters").
 */
export function extractValidationErrors(error: HttpErrorResponse): string[] {
  const body: unknown = error.error;

  if (!(isProblemDetails(body) && Array.isArray(body.errors))) {
    return [];
  }

  return body.errors.map(e => {
    const location = e.location ? `${e.location}: ` : '';
    return `${location}${e.message ?? 'validation error'}`;
  });
}

/**
 * Extracts catalog validation error details from an RFC 9457 response.
 *
 * The backend returns structured error information for catalog validation failures:
 * - error_type: The classified error type (quota_exceeded, resource_unavailable, etc.)
 * - suggestion: An actionable suggestion for the user
 * - alternative_zones: Zones where the resource is available
 * - retryable: Whether the operation can be retried
 *
 * @returns CatalogErrorDetails if present, undefined otherwise.
 */
export function extractCatalogErrorDetails(
  error: HttpErrorResponse
): CatalogErrorDetails | undefined {
  const body: unknown = error.error;

  if (!isCatalogError(body)) {
    return undefined;
  }

  const errorType = body.error_type ?? body.errorType;
  const suggestion = body.suggestion;
  const alternativeZones = body.alternative_zones ?? body.alternativeZones;
  const retryable = body.retryable;

  // Only return details if we have at least one catalog-specific field
  if (!(errorType ?? suggestion ?? alternativeZones)) {
    return undefined;
  }

  return {
    errorType,
    suggestion,
    alternativeZones: Array.isArray(alternativeZones) ? alternativeZones : undefined,
    retryable: typeof retryable === 'boolean' ? retryable : undefined,
  };
}

/**
 * Formats Zod validation issues into a single human-readable string.
 * Used when the frontend schema validation fails (drift detection).
 */
export function formatZodIssues(issues: z.ZodIssue[]): string {
  return issues.map(issue => `${issue.path.join('.') || 'root'}: ${issue.message}`).join('; ');
}

/**
 * Creates a reusable error handler for API services.
 *
 * ARCHITECTURAL PATTERN:
 * This factory function centralizes error handling logic to ensure consistent behavior
 * across all vertical slices (Devices, Hosts, Auth, etc.). It standardizes:
 * - Logging (console.error with context)
 * - Error Transformation (HttpErrorResponse -> Typed ApiError)
 * - Schema Validation failures (ZodError -> ApiValidationError)
 *
 * Usage:
 * ```typescript
 * @Service()
 * export class DeviceApiService {
 *   private readonly handleError = createApiErrorHandler('Device');
 *   // ...
 * }
 * ```
 *
 * @param resourceName - The name of the resource (e.g., 'Device', 'Host') for error messages.
 * @param serviceName - Optional service name for logging (defaults to `${resourceName}ApiService`).
 */
export function createApiErrorHandler(
  resourceName: string,
  serviceName?: string
): (error: unknown) => Observable<never> {
  const logPrefix = serviceName ?? `${resourceName}ApiService`;

  // An error-classification ladder: Zod, then HTTP by status and by RFC 9457
  // detail shape. The score tracks how many error kinds the API can return, and
  // each arm maps one of them to one typed error. Extracting arms would hide
  // that mapping, and the mapping is what a reader comes here to see.
  // biome-ignore lint/complexity/noExcessiveCognitiveComplexity: one arm per error kind.
  return (error: unknown): Observable<never> => {
    if (error instanceof z.ZodError) {
      const issueDetails = formatZodIssues(error.issues);
      const message = `API response validation failed: ${issueDetails}`;
      if (isDevMode()) {
        console.error(`[${logPrefix}] Schema validation failed: ${issueDetails}`);
        console.error(`[${logPrefix}] Raw issues:`, JSON.stringify(error.issues, null, 2));
      }
      return throwError(() => new ApiValidationError(message, error.issues));
    }

    if (error instanceof HttpErrorResponse) {
      const message = extractErrorMessage(error);
      if (isDevMode()) {
        console.error(`[${logPrefix}] HTTP ${error.status}:`, message);
      }

      // Log validation errors from RFC 9457 response (Huma framework)
      const validationErrors = extractValidationErrors(error);
      if (validationErrors.length > 0 && isDevMode()) {
        console.error(`[${logPrefix}] Validation errors:`, validationErrors);
      }

      if (error.status === 404) {
        return throwError(() => new NotFoundError(resourceName));
      }

      // Check for catalog validation errors (400 Bad Request with structured details)
      const catalogDetails = extractCatalogErrorDetails(error);
      if (catalogDetails && error.status === 400) {
        if (isDevMode()) {
          console.error(`[${logPrefix}] Catalog validation error:`, catalogDetails);
        }
        return throwError(() => new CatalogValidationError(message, catalogDetails));
      }

      // For 422 Unprocessable Entity (validation errors), include field details
      if (error.status === 422 && validationErrors.length > 0) {
        const fullMessage = `${message}: ${validationErrors.join('; ')}`;
        return throwError(() => new ApiError(fullMessage, error.status, error));
      }

      return throwError(() => new ApiError(message, error.status, error));
    }

    // Unknown error - wrap it
    if (isDevMode()) {
      console.error(`[${logPrefix}] Unexpected error:`, error);
    }
    return throwError(() => new ApiError('An unexpected error occurred', undefined, error));
  };
}
