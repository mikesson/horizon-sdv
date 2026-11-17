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

import type {
  HttpErrorResponse,
  HttpEvent,
  HttpHandler,
  HttpInterceptor,
  HttpRequest,
} from '@angular/common/http';
import {Injectable, inject} from '@angular/core';
import {MatSnackBar} from '@angular/material/snack-bar';
import type {Observable} from 'rxjs';
import {throwError} from 'rxjs';
import {catchError} from 'rxjs/operators';
import {browserLocation, resolveBasePath} from '../app/shared/utils';

const REAUTH_COOLDOWN_MS = 15_000;
const REAUTH_TS_KEY = 'cco_reauth_ts';

/**
 * AuthInterceptor handles oauth2-proxy / session authentication for the console.
 *
 * Key responsibilities:
 * 1. Adds X-Requested-With header so upstream auth proxies return 401 instead of 302 for AJAX
 * 2. Handles 401 errors by redirecting to oauth2-proxy sign-in with a loop-guard cooldown
 * 3. Handles CORS/network errors (status 0) which may indicate expired proxy cookies
 */
@Injectable()
export class AuthInterceptor implements HttpInterceptor {
  private readonly snackBar = inject(MatSnackBar);
  private isRedirecting = false;

  intercept(request: HttpRequest<unknown>, next: HttpHandler): Observable<HttpEvent<unknown>> {
    // Add X-Requested-With header to identify AJAX requests so oauth2-proxy returns 401
    // rather than an opaque HTML 302 login redirect on expired sessions.
    const modifiedRequest = request.clone({
      setHeaders: {'X-Requested-With': 'XMLHttpRequest'},
    });

    return next.handle(modifiedRequest).pipe(
      catchError((error: HttpErrorResponse) => {
        if (error.status === 401) {
          this.handleUnauthorized();
          return throwError(() => error);
        }

        // Handle status 0 (CORS/network failure on opaque proxy redirect or expired session).
        if (error.status === 0) {
          this.snackBar
            .open(
              'Session expired or connection lost. Please refresh the page to reconnect.',
              'Refresh',
              {duration: 10000}
            )
            .onAction()
            .subscribe(() => {
              this.reloadPage();
            });
        }

        return throwError(() => error);
      })
    );
  }

  private handleUnauthorized(): void {
    const startUrl = this.buildSignInUrl();
    if (this.isRedirecting || this.isInReauthCooldown()) {
      this.snackBar
        .open('Authentication required. Please sign in again.', 'Sign in', {duration: 10000})
        .onAction()
        .subscribe(() => {
          this.clearReauthCooldown();
          this.navigate(startUrl);
        });
      return;
    }

    this.recordReauthAttempt();
    this.isRedirecting = true;
    this.navigate(startUrl);
  }

  private buildSignInUrl(): string {
    const currentTarget = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    return `${resolveBasePath('/oauth2/start')}?rd=${encodeURIComponent(currentTarget)}`;
  }

  private isInReauthCooldown(): boolean {
    try {
      const raw = window.sessionStorage.getItem(REAUTH_TS_KEY);
      if (!raw) {
        return false;
      }
      const lastAttempt = Number(raw);
      return Number.isFinite(lastAttempt) && Date.now() - lastAttempt < REAUTH_COOLDOWN_MS;
    } catch {
      return false;
    }
  }

  private recordReauthAttempt(): void {
    try {
      window.sessionStorage.setItem(REAUTH_TS_KEY, String(Date.now()));
    } catch {
      // Ignore storage errors in restricted browsing modes.
    }
  }

  private clearReauthCooldown(): void {
    try {
      window.sessionStorage.removeItem(REAUTH_TS_KEY);
    } catch {
      // Ignore storage errors in restricted browsing modes.
    }
  }

  private navigate(url: string): void {
    browserLocation.assign(url);
  }

  private reloadPage(): void {
    browserLocation.reload();
  }
}
