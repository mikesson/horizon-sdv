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

import {provideZonelessChangeDetection} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import type {RouterStateSnapshot} from '@angular/router';
import {ActivatedRouteSnapshot, provideRouter, Router, UrlTree} from '@angular/router';
import type {Observable} from 'rxjs';
import {firstValueFrom, isObservable, of, Subject} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {User} from '../models/user.model';
import {AuthService} from '../services/auth.service';
import {AuthStore} from '../store/auth.signal-store';
import {authGuard} from './auth.guard';

describe('authGuard', () => {
  let authService: {loadCurrentUser: ReturnType<typeof vi.fn>; logout: ReturnType<typeof vi.fn>};
  const route = new ActivatedRouteSnapshot();
  const state: RouterStateSnapshot = {url: '/devices', root: route};

  beforeEach(() => {
    authService = {
      loadCurrentUser: vi.fn(() => of({email: 'user@example.com'})),
      logout: vi.fn(() => of(void 0)),
    };
    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        provideRouter([{path: 'unauthorized', children: []}]),
        {provide: AuthService, useValue: authService},
      ],
    });
  });

  function runGuard(): Promise<boolean | UrlTree> {
    const res = TestBed.runInInjectionContext(() => authGuard(route, state));
    if (isObservable(res)) {
      return firstValueFrom(res as Observable<boolean | UrlTree>);
    }
    return Promise.resolve(res as boolean | UrlTree);
  }

  it('allows activation when AuthStore is authenticated', async () => {
    const store = TestBed.inject(AuthStore);
    store.setUser({email: 'dev@example.com'});
    TestBed.flushEffects();

    const result = await runGuard();
    expect(result).toBe(true);
  });

  it('redirects to /unauthorized UrlTree when AuthStore is not authenticated', async () => {
    const store = TestBed.inject(AuthStore);
    const router = TestBed.inject(Router);
    store.setNotAuthenticated();
    TestBed.flushEffects();

    const result = await runGuard();
    expect(result instanceof UrlTree).toBe(true);
    expect(router.serializeUrl(result as UrlTree)).toBe('/unauthorized');
  });

  it('stays pending while initial loading is active and redirects when load fails', async () => {
    const userSubject = new Subject<User>();
    authService.loadCurrentUser.mockReturnValue(userSubject.asObservable());

    const store = TestBed.inject(AuthStore);
    const router = TestBed.inject(Router);
    store.loadUser();
    expect(store.isLoading()).toBe(true);

    let settled = false;
    const pending = runGuard().then(val => {
      settled = true;
      return val;
    });
    TestBed.flushEffects();
    await Promise.resolve();
    expect(settled).toBe(false);

    userSubject.error(new Error('unauthenticated'));
    TestBed.flushEffects();

    const result = await pending;
    expect(settled).toBe(true);
    expect(result instanceof UrlTree).toBe(true);
    expect(router.serializeUrl(result as UrlTree)).toBe('/unauthorized');
  });

  it('resolves to true when initial loading completes with authenticated user', async () => {
    const userSubject = new Subject<User>();
    authService.loadCurrentUser.mockReturnValue(userSubject.asObservable());

    const store = TestBed.inject(AuthStore);
    store.loadUser();
    expect(store.isLoading()).toBe(true);

    let settled = false;
    const pending = runGuard().then(val => {
      settled = true;
      return val;
    });
    TestBed.flushEffects();
    await Promise.resolve();
    expect(settled).toBe(false);

    userSubject.next({email: 'dev@example.com'});
    TestBed.flushEffects();

    const result = await pending;
    expect(settled).toBe(true);
    expect(result).toBe(true);
  });

  it('allows activation on synchronous loading-to-authenticated signal transition', async () => {
    const userSubject = new Subject<User>();
    authService.loadCurrentUser.mockReturnValue(userSubject.asObservable());

    const store = TestBed.inject(AuthStore);
    store.loadUser();
    expect(store.isLoading()).toBe(true);
    expect(store.isAuthenticated()).toBe(false);

    const pending = runGuard();
    TestBed.flushEffects();

    // Mutate both isLoading (true->false) and isAuthenticated (false->true) synchronously.
    store.setUser({email: 'sync@example.com'});
    TestBed.flushEffects();

    const result = await pending;
    expect(result).toBe(true);
    expect(result instanceof UrlTree).toBe(false);
  });
});
