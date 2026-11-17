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

import {inject} from '@angular/core';
import {tapResponse} from '@ngrx/operators';
import {patchState, signalStore, withMethods, withState} from '@ngrx/signals';
import {rxMethod} from '@ngrx/signals/rxjs-interop';
import {pipe} from 'rxjs';
import {switchMap, tap} from 'rxjs/operators';

import {getErrorMessage} from '../../shared/utils';
import type {User} from '../models/user.model';
import {initialAuthState} from '../models/user.model';
import {AuthService} from '../services/auth.service';

/**
 * Signal store for authentication state.
 *
 * SCOPE: Root-provided singleton (accessible globally).
 * ARCHITECTURE: Manages IAP-based identity state.
 *
 * In the IAP-centric architecture:
 * - User identity is loaded from /v2/me after IAP validates the request
 * - Session management is handled by IAP (no local session tracking)
 * - There are no roles or permissions, and authentication is the only gate
 */
export const AuthStore = signalStore(
  {providedIn: 'root'},
  withState(initialAuthState),
  withMethods((store, authApi = inject(AuthService)) => ({
    /**
     * Directly sets the authenticated user state.
     * Used by the app initializer to set state before guards run.
     */
    setUser(user: User): void {
      patchState(store, {
        user,
        isAuthenticated: true,
        isLoading: false,
        error: null,
      });
    },

    /**
     * Marks auth loading as complete without a user.
     * Used when auth check fails but we still need to unblock guards.
     */
    setNotAuthenticated(): void {
      patchState(store, {
        user: null,
        isAuthenticated: false,
        isLoading: false,
        error: null,
      });
    },

    /**
     * Reactive method that loads the current user's identity from the backend.
     * Should be called after IAP authentication is verified.
     * Updates `user`, `isAuthenticated`, and `isLoading` state.
     */
    loadUser: rxMethod<void>(
      pipe(
        tap(() => patchState(store, {isLoading: true, error: null})),
        switchMap(() =>
          authApi.loadCurrentUser().pipe(
            tapResponse({
              next: (user: User) =>
                patchState(store, {
                  user,
                  isAuthenticated: true,
                  isLoading: false,
                }),
              error: (err: unknown) =>
                patchState(store, {
                  user: null,
                  isAuthenticated: false,
                  isLoading: false,
                  error: getErrorMessage(err, 'Failed to load user'),
                }),
            })
          )
        )
      )
    ),

    /**
     * Reactive method that logs out the user and clears the session.
     * Resets local state to initial values.
     *
     * The redirect is handled by authApi.logout() using the IAP
     * CLEAR_LOGIN_COOKIE sign-out flow.
     */
    logout: rxMethod<void>(
      pipe(
        tap(() => patchState(store, {isLoading: true})),
        switchMap(() =>
          authApi.logout().pipe(
            tapResponse({
              next: () => {
                patchState(store, initialAuthState);
              },
              error: (err: unknown) => {
                patchState(store, {isLoading: false, error: getErrorMessage(err, 'Logout failed')});
              },
            })
          )
        )
      )
    ),
  }))
);
