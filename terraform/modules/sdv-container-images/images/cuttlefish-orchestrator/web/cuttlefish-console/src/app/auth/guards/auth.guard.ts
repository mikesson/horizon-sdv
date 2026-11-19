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

import {computed, inject} from '@angular/core';
import {toObservable} from '@angular/core/rxjs-interop';
import type {CanActivateFn} from '@angular/router';
import {Router} from '@angular/router';
import {filter, map, take} from 'rxjs/operators';
import {AuthStore} from '../store/auth.signal-store';

/**
 * Guard that requires authentication.
 *
 * FLOW:
 * 1. Derives a single atomic computed signal `{isLoading, isAuthenticated}` so
 *    synchronous signal updates never trigger split emissions across observables.
 * 2. Waits for `isLoading` to become false (initial auth check complete).
 * 3. Redirects to /unauthorized if no identity could be established.
 *
 * Cloud Run IAP performs login at the edge, so an unauthenticated browser
 * normally never reaches the SPA. Landing here without an identity means
 * /v2/me failed (e.g. backend unavailable), so we show the unauthorized
 * page rather than redirect-looping through IAP.
 */
export const authGuard: CanActivateFn = () => {
  const authStore = inject(AuthStore);
  const router = inject(Router);

  const authState = computed(() => ({
    isLoading: authStore.isLoading(),
    isAuthenticated: authStore.isAuthenticated(),
  }));

  return toObservable(authState).pipe(
    filter(state => !state.isLoading),
    take(1),
    map(state => (state.isAuthenticated ? true : router.createUrlTree(['/unauthorized'])))
  );
};
