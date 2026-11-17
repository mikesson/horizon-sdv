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

import {HttpClient} from '@angular/common/http';
import {inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {of} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {z} from 'zod';
import {LoggingService} from '../../shared/services';
import {browserLocation, resolveBasePath} from '../../shared/utils';
import type {User} from '../models/user.model';
import {UserSchema} from '../models/user.schema';

/**
 * Manages the global authentication state for the application.
 *
 * ARCHITECTURE: Cloud Run IAP authenticates every request at the edge.
 * - IAP handles login and all session management (no backend sessions).
 * - The backend middleware validates the IAP assertion and exposes the
 *   verified identity via /v2/me.
 * - There are no roles or permissions, and any authenticated user has full
 *   access.
 */
@Service()
export class AuthService {
  private readonly http = inject(HttpClient);
  private readonly logger = inject(LoggingService);

  private readonly baseUrl = resolveBasePath('/v2');

  /**
   * Loads the current user's identity from the backend's /v2/me endpoint.
   *
   * MECHANISM: The backend middleware validates the IAP assertion present
   * on the request.
   * VALIDATION: Uses UserSchema to runtime-validate the response and
   * transform snake_case to camelCase.
   */
  loadCurrentUser(): Observable<User> {
    return this.http.get<unknown>(`${this.baseUrl}/me`).pipe(
      map(response => UserSchema.parse(response)),
      catchError(this.handleError)
    );
  }

  /**
   * Logout and clear the edge proxy session.
   *
   * Redirects to oauth2-proxy's sign-out endpoint so the session cookie is
   * cleared and the user is returned to the application root.
   */
  logout(): Observable<void> {
    const redirectTo = encodeURIComponent(resolveBasePath('/'));
    browserLocation.assign(`${resolveBasePath('/oauth2/sign_out')}?rd=${redirectTo}`);
    return of(void 0);
  }

  private readonly handleError = (error: unknown): Observable<never> => {
    if (error instanceof z.ZodError) {
      this.logger.error('Auth', 'API validation error', error.issues);
      throw new Error('Received invalid user data');
    }
    throw error;
  };
}
