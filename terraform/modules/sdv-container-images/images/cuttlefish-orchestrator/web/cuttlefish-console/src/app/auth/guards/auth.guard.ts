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
import {toObservable} from '@angular/core/rxjs-interop';
import type {CanActivateFn} from '@angular/router';
import {Router} from '@angular/router';
import {combineLatest} from 'rxjs';
import {filter, map, take} from 'rxjs/operators';
import {AuthStore} from '../store/auth.signal-store';

/**
 * Guard that requires authentication.
 *
 * FLOW:
 * 1. Uses combineLatest to synchronize isLoading and isAuthenticated states.
 * 2. Waits for `isLoading` to become false (initial auth check complete).
 * 3. Checks `isAuthenticated` status from the SAME emission.
 * 4. Redirects to /unauthorized if no identity could be established.
 *
 * Cloud Run IAP performs login at the edge, so an unauthenticated browser
 * normally never reaches the SPA. Landing here without an identity means
 * /v2/me failed (e.g. backend unavailable), so we show the unauthorized
 * page rather than redirect-looping through IAP.
 *
 * NOTE: Using combineLatest ensures we check both values from the same state update,
 * avoiding race conditions where isLoading becomes false but isAuthenticated
 * hasn't been updated yet in the observable.
 */
export const authGuard: CanActivateFn = () => {
  const authStore = inject(AuthStore);
  const router = inject(Router);

  const isLoading = toObservable(authStore.isLoading);
  const isAuthenticated = toObservable(authStore.isAuthenticated);

  return combineLatest([isLoading, isAuthenticated]).pipe(
    filter(([isLoading]) => !isLoading),
    take(1),
    map(([, isAuthenticated]) => {
      if (!isAuthenticated) {
        return router.createUrlTree(['/unauthorized']);
      }
      return true;
    })
  );
};
