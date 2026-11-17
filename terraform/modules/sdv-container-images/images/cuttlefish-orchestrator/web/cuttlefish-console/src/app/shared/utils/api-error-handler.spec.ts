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
import {firstValueFrom} from 'rxjs';
import {describe, expect, it} from 'vitest';
import {z} from 'zod';
import {
  ApiError,
  ApiValidationError,
  CatalogValidationError,
  NotFoundError,
} from '../errors/api.error';
import {
  createApiErrorHandler,
  extractCatalogErrorDetails,
  extractErrorMessage,
  extractValidationErrors,
  formatZodIssues,
} from './api-error-handler';

describe('extractErrorMessage', () => {
  it('should extract string error', () => {
    const error = new HttpErrorResponse({
      error: 'Simple error message',
      status: 500,
    });
    expect(extractErrorMessage(error)).toBe('Simple error message');
  });

  it('should extract RFC 9457 detail field (Huma format)', () => {
    const error = new HttpErrorResponse({
      error: {
        type: 'about:blank',
        title: 'Bad Request',
        status: 400,
        detail: 'Device name must be alphanumeric',
      },
      status: 400,
    });
    expect(extractErrorMessage(error)).toBe('Device name must be alphanumeric');
  });

  it('should fall back to RFC 9457 title if no detail', () => {
    const error = new HttpErrorResponse({
      error: {
        type: 'about:blank',
        title: 'Not Found',
        status: 404,
      },
      status: 404,
    });
    expect(extractErrorMessage(error)).toBe('Not Found');
  });

  it('should extract legacy message field', () => {
    const error = new HttpErrorResponse({
      error: {
        message: 'Legacy error message',
      },
      status: 500,
    });
    expect(extractErrorMessage(error)).toBe('Legacy error message');
  });

  it('should extract legacy error field', () => {
    const error = new HttpErrorResponse({
      error: {
        error: 'Another legacy format',
      },
      status: 500,
    });
    expect(extractErrorMessage(error)).toBe('Another legacy format');
  });

  it('should fall back to statusText', () => {
    const error = new HttpErrorResponse({
      error: {},
      status: 500,
      statusText: 'Internal Server Error',
    });
    expect(extractErrorMessage(error)).toBe('Internal Server Error');
  });

  it('should return Unknown error as last resort', () => {
    const error = new HttpErrorResponse({
      error: {},
      status: 0,
      statusText: '',
    });
    expect(extractErrorMessage(error).toLowerCase()).toBe('unknown error');
  });
});

describe('extractValidationErrors', () => {
  it('should extract RFC 9457 validation errors (Huma format)', () => {
    const error = new HttpErrorResponse({
      error: {
        type: 'about:blank',
        title: 'Unprocessable Entity',
        status: 422,
        detail: 'validation failed',
        errors: [
          {location: 'body.name', message: 'must be at least 3 characters'},
          {location: 'body.host_id', message: 'must be a valid UUID'},
        ],
      },
      status: 422,
    });

    const errors = extractValidationErrors(error);
    expect(errors).toHaveLength(2);
    expect(errors[0]).toBe('body.name: must be at least 3 characters');
    expect(errors[1]).toBe('body.host_id: must be a valid UUID');
  });

  it('should handle errors without location', () => {
    const error = new HttpErrorResponse({
      error: {
        errors: [{message: 'general validation error'}],
      },
      status: 422,
    });

    const errors = extractValidationErrors(error);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBe('general validation error');
  });

  it('should handle errors without message', () => {
    const error = new HttpErrorResponse({
      error: {
        errors: [{location: 'body.field'}],
      },
      status: 422,
    });

    const errors = extractValidationErrors(error);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBe('body.field: validation error');
  });

  it('should return empty array when no errors field', () => {
    const error = new HttpErrorResponse({
      error: {message: 'error'},
      status: 400,
    });

    expect(extractValidationErrors(error)).toEqual([]);
  });

  it('should return empty array when errors is not an array', () => {
    const error = new HttpErrorResponse({
      error: {errors: 'not an array'},
      status: 400,
    });

    expect(extractValidationErrors(error)).toEqual([]);
  });
});

describe('formatZodIssues', () => {
  it('should format Zod issues with path', () => {
    const issues = [
      {
        code: 'invalid_type',
        expected: 'string',
        path: ['name'],
        message: 'Expected string, received number',
      },
      {
        code: 'too_small',
        minimum: 1,
        path: ['nested', 'field'],
        message: 'String must contain at least 1 character(s)',
      },
    ] as z.ZodIssue[];

    const formatted = formatZodIssues(issues);
    expect(formatted).toBe(
      'name: Expected string, received number; nested.field: String must contain at least 1 character(s)'
    );
  });

  it('should handle empty path as root', () => {
    const issues = [
      {
        code: 'invalid_type',
        expected: 'object',
        path: [],
        message: 'Expected object, received null',
      },
    ] as z.ZodIssue[];

    const formatted = formatZodIssues(issues);
    expect(formatted).toBe('root: Expected object, received null');
  });
});

describe('createApiErrorHandler', () => {
  it('should handle Zod validation errors', async () => {
    const handler = createApiErrorHandler('Device');
    const schema = z.object({name: z.string()});
    let zodError: z.ZodError | undefined;
    try {
      schema.parse({name: undefined});
    } catch (e) {
      zodError = e as z.ZodError;
    }
    if (!zodError) {
      throw new Error('schema.parse did not throw a ZodError');
    }

    try {
      await firstValueFrom(handler(zodError));
      expect.fail('Should have thrown');
    } catch (error) {
      expect(error).toBeInstanceOf(ApiValidationError);
      expect((error as ApiValidationError).message).toContain('API response validation failed');
      expect((error as ApiValidationError).message).toContain('name:');
    }
  });

  it('should handle 404 errors as NotFoundError', async () => {
    const handler = createApiErrorHandler('Device');
    const httpError = new HttpErrorResponse({
      error: {detail: 'Device not found'},
      status: 404,
    });

    try {
      await firstValueFrom(handler(httpError));
      expect.fail('Should have thrown');
    } catch (error) {
      expect(error).toBeInstanceOf(NotFoundError);
      expect((error as NotFoundError).message).toContain('Device');
    }
  });

  it('should handle 422 errors with validation details', async () => {
    const handler = createApiErrorHandler('Device');
    const httpError = new HttpErrorResponse({
      error: {
        title: 'Unprocessable Entity',
        detail: 'Validation failed',
        errors: [{location: 'body.name', message: 'too short'}],
      },
      status: 422,
    });

    try {
      await firstValueFrom(handler(httpError));
      expect.fail('Should have thrown');
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect((error as ApiError).message).toContain('Validation failed');
      expect((error as ApiError).message).toContain('body.name: too short');
    }
  });

  it('should handle generic HTTP errors', async () => {
    const handler = createApiErrorHandler('Device');
    const httpError = new HttpErrorResponse({
      error: {detail: 'Internal server error'},
      status: 500,
    });

    try {
      await firstValueFrom(handler(httpError));
      expect.fail('Should have thrown');
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect((error as ApiError).statusCode).toBe(500);
    }
  });

  it('should handle unknown errors', async () => {
    const handler = createApiErrorHandler('Device');

    try {
      await firstValueFrom(handler(new Error('Something unexpected')));
      expect.fail('Should have thrown');
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect((error as ApiError).message).toBe('An unexpected error occurred');
    }
  });

  it('should use custom service name for logging', async () => {
    const handler = createApiErrorHandler('Device', 'CustomService');
    const httpError = new HttpErrorResponse({
      error: {detail: 'Error'},
      status: 500,
    });

    // Just verify it doesn't throw - logging is a side effect
    try {
      await firstValueFrom(handler(httpError));
    } catch {
      // Expected
    }
  });

  it('should handle catalog validation errors with structured details', async () => {
    const handler = createApiErrorHandler('Host');
    const httpError = new HttpErrorResponse({
      error: {
        detail: 'GPU nvidia-tesla-t4 is not available in zone us-central1-a',
        error_type: 'resource_unavailable',
        suggestion: 'Try a different zone or GPU type',
        alternative_zones: ['us-central1-b', 'us-central1-c'],
        retryable: true,
      },
      status: 400,
    });

    try {
      await firstValueFrom(handler(httpError));
      expect.fail('Should have thrown');
    } catch (error) {
      expect(error).toBeInstanceOf(CatalogValidationError);
      const catalogError = error as CatalogValidationError;
      expect(catalogError.message).toContain('nvidia-tesla-t4');
      expect(catalogError.details.errorType).toBe('resource_unavailable');
      expect(catalogError.details.suggestion).toBe('Try a different zone or GPU type');
      expect(catalogError.details.alternativeZones).toEqual(['us-central1-b', 'us-central1-c']);
      expect(catalogError.details.retryable).toBe(true);
    }
  });
});

describe('extractCatalogErrorDetails', () => {
  it('should extract catalog error details with snake_case fields', () => {
    const error = new HttpErrorResponse({
      error: {
        detail: 'Resource unavailable',
        error_type: 'quota_exceeded',
        suggestion: 'Request quota increase',
        alternative_zones: ['zone-a', 'zone-b'],
        retryable: false,
      },
      status: 400,
    });

    const details = extractCatalogErrorDetails(error);
    expect(details).toBeDefined();
    expect(details?.errorType).toBe('quota_exceeded');
    expect(details?.suggestion).toBe('Request quota increase');
    expect(details?.alternativeZones).toEqual(['zone-a', 'zone-b']);
    expect(details?.retryable).toBe(false);
  });

  it('should extract catalog error details with camelCase fields', () => {
    const error = new HttpErrorResponse({
      error: {
        detail: 'Zone unavailable',
        errorType: 'zone_unavailable',
        suggestion: 'Select a different zone',
        alternativeZones: ['europe-west4-a'],
        retryable: true,
      },
      status: 400,
    });

    const details = extractCatalogErrorDetails(error);
    expect(details).toBeDefined();
    expect(details?.errorType).toBe('zone_unavailable');
    expect(details?.suggestion).toBe('Select a different zone');
    expect(details?.alternativeZones).toEqual(['europe-west4-a']);
    expect(details?.retryable).toBe(true);
  });

  it('should return undefined when no catalog-specific fields present', () => {
    const error = new HttpErrorResponse({
      error: {
        detail: 'Generic error',
        message: 'Something went wrong',
      },
      status: 500,
    });

    const details = extractCatalogErrorDetails(error);
    expect(details).toBeUndefined();
  });

  it('should return undefined when error body is null', () => {
    const error = new HttpErrorResponse({
      error: null,
      status: 500,
    });

    const details = extractCatalogErrorDetails(error);
    expect(details).toBeUndefined();
  });

  it('should handle partial catalog error details', () => {
    const error = new HttpErrorResponse({
      error: {
        detail: 'Error with only suggestion',
        suggestion: 'Try again later',
      },
      status: 400,
    });

    const details = extractCatalogErrorDetails(error);
    expect(details).toBeDefined();
    expect(details?.suggestion).toBe('Try again later');
    expect(details?.errorType).toBeUndefined();
    expect(details?.alternativeZones).toBeUndefined();
  });
});
