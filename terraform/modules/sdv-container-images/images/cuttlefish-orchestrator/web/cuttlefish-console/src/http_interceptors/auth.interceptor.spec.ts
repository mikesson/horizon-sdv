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

import {
  HTTP_INTERCEPTORS,
  HttpClient,
  provideHttpClient,
  withInterceptorsFromDi,
} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {TestBed} from '@angular/core/testing';
import {MatSnackBar} from '@angular/material/snack-bar';
import {Subject} from 'rxjs';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {browserLocation} from '../app/shared/utils';
import {AuthInterceptor} from './auth.interceptor';

const REAUTH_TS_KEY = 'cco_reauth_ts';

describe('AuthInterceptor', () => {
  let http: HttpClient;
  let httpMock: HttpTestingController;
  let onAction: Subject<void>;
  let snackBarOpen: ReturnType<typeof vi.fn>;
  let assignSpy: ReturnType<typeof vi.spyOn>;
  let reloadSpy: ReturnType<typeof vi.spyOn>;
  let baseEl: HTMLBaseElement | null = null;
  const originalPath = `${window.location.pathname}${window.location.search}${window.location.hash}`;

  const mountBase = (href: string): void => {
    baseEl?.remove();
    baseEl = document.createElement('base');
    baseEl.setAttribute('href', href);
    document.head.appendChild(baseEl);
  };

  beforeEach(() => {
    window.sessionStorage.removeItem(REAUTH_TS_KEY);
    onAction = new Subject<void>();
    snackBarOpen = vi.fn(() => ({onAction: () => onAction.asObservable()}));
    assignSpy = vi.spyOn(browserLocation, 'assign').mockReturnValue(undefined);
    reloadSpy = vi.spyOn(browserLocation, 'reload').mockReturnValue(undefined);

    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(withInterceptorsFromDi()),
        provideHttpClientTesting(),
        {provide: HTTP_INTERCEPTORS, useClass: AuthInterceptor, multi: true},
        {provide: MatSnackBar, useValue: {open: snackBarOpen}},
      ],
    });

    http = TestBed.inject(HttpClient);
    httpMock = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    httpMock.verify();
    baseEl?.remove();
    baseEl = null;
    window.sessionStorage.removeItem(REAUTH_TS_KEY);
    window.history.replaceState(null, '', originalPath || '/');
    vi.restoreAllMocks();
  });

  it('attaches X-Requested-With header on outgoing requests', () => {
    http.get('/v2/devices').subscribe();

    const req = httpMock.expectOne('/v2/devices');
    expect(req.request.headers.get('X-Requested-With')).toBe('XMLHttpRequest');
    req.flush([]);
  });

  it('redirects 401 responses to subpath oauth2/start with return path', () => {
    mountBase('/cuttlefish-orchestrator/');
    window.history.pushState(null, '', '/cuttlefish-orchestrator/devices/d1?tab=logs');

    let capturedStatus = 0;
    http.get('/cuttlefish-orchestrator/v2/devices').subscribe({
      error: (err: {status: number}) => {
        capturedStatus = err.status;
      },
    });

    const req = httpMock.expectOne('/cuttlefish-orchestrator/v2/devices');
    req.flush('Unauthorized', {status: 401, statusText: 'Unauthorized'});

    expect(capturedStatus).toBe(401);
    expect(assignSpy).toHaveBeenCalledTimes(1);
    expect(assignSpy).toHaveBeenCalledWith(
      `/cuttlefish-orchestrator/oauth2/start?rd=${encodeURIComponent('/cuttlefish-orchestrator/devices/d1?tab=logs')}`
    );
    expect(snackBarOpen).not.toHaveBeenCalled();
  });

  it('suppresses automatic 401 redirect during cooldown and offers snackbar re-login', () => {
    mountBase('/cuttlefish-orchestrator/');
    window.history.pushState(null, '', '/cuttlefish-orchestrator/hosts');
    window.sessionStorage.setItem(REAUTH_TS_KEY, String(Date.now()));

    let errored = false;
    http.get('/cuttlefish-orchestrator/v2/hosts').subscribe({
      error: () => {
        errored = true;
      },
    });

    const req = httpMock.expectOne('/cuttlefish-orchestrator/v2/hosts');
    req.flush('Unauthorized', {status: 401, statusText: 'Unauthorized'});

    expect(errored).toBe(true);
    expect(assignSpy).not.toHaveBeenCalled();
    expect(snackBarOpen).toHaveBeenCalledWith(
      'Authentication required. Please sign in again.',
      'Sign in',
      {duration: 10000}
    );

    onAction.next();
    expect(assignSpy).toHaveBeenCalledWith(
      `/cuttlefish-orchestrator/oauth2/start?rd=${encodeURIComponent('/cuttlefish-orchestrator/hosts')}`
    );
    expect(window.sessionStorage.getItem(REAUTH_TS_KEY)).toBeNull();
  });

  it('shows refresh prompt on status 0 network errors and reloads on action', () => {
    let errored = false;
    http.get('/v2/me').subscribe({
      error: () => {
        errored = true;
      },
    });

    const req = httpMock.expectOne('/v2/me');
    req.error(new ProgressEvent('error'), {status: 0, statusText: 'Unknown Error'});

    expect(errored).toBe(true);
    expect(snackBarOpen).toHaveBeenCalledWith(
      'Session expired or connection lost. Please refresh the page to reconnect.',
      'Refresh',
      {duration: 10000}
    );

    onAction.next();
    expect(reloadSpy).toHaveBeenCalledTimes(1);
  });
});
