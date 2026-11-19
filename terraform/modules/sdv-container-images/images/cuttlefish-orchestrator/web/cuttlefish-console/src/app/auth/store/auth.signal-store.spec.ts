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
import {of, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {User} from '../models/user.model';
import {AuthService} from '../services/auth.service';
import {AuthStore} from './auth.signal-store';

describe('AuthStore', () => {
  let authService: {
    loadCurrentUser: ReturnType<typeof vi.fn>;
    logout: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    authService = {
      loadCurrentUser: vi.fn(),
      logout: vi.fn(() => of(void 0)),
    };
    TestBed.configureTestingModule({
      providers: [provideZonelessChangeDetection(), {provide: AuthService, useValue: authService}],
    });
  });

  it('starts in loading unauthenticated initial state', () => {
    const store = TestBed.inject(AuthStore);
    expect(store.isLoading()).toBe(true);
    expect(store.isAuthenticated()).toBe(false);
    expect(store.user()).toBeNull();
    expect(store.error()).toBeNull();
  });

  it('sets authenticated state synchronously with setUser and clears with setNotAuthenticated', () => {
    const store = TestBed.inject(AuthStore);
    const user: User = {email: 'alice@example.com', displayName: 'Alice'};

    store.setUser(user);
    expect(store.isAuthenticated()).toBe(true);
    expect(store.isLoading()).toBe(false);
    expect(store.user()).toEqual(user);

    store.setNotAuthenticated();
    expect(store.isAuthenticated()).toBe(false);
    expect(store.isLoading()).toBe(false);
    expect(store.user()).toBeNull();
  });

  it('loads current user from AuthService on success', () => {
    const user: User = {email: 'bob@example.com'};
    authService.loadCurrentUser.mockReturnValue(of(user));
    const store = TestBed.inject(AuthStore);

    store.loadUser();

    expect(authService.loadCurrentUser).toHaveBeenCalledTimes(1);
    expect(store.isAuthenticated()).toBe(true);
    expect(store.isLoading()).toBe(false);
    expect(store.user()).toEqual(user);
    expect(store.error()).toBeNull();
  });

  it('marks unauthenticated and records error when loadUser fails', () => {
    authService.loadCurrentUser.mockReturnValue(throwError(() => new Error('IAP expired')));
    const store = TestBed.inject(AuthStore);

    store.loadUser();

    expect(store.isAuthenticated()).toBe(false);
    expect(store.isLoading()).toBe(false);
    expect(store.user()).toBeNull();
    expect(store.error()).toContain('IAP expired');
  });

  it('delegates logout to AuthService and clears user session', () => {
    const store = TestBed.inject(AuthStore);
    store.setUser({email: 'carol@example.com'});

    store.logout();

    expect(authService.logout).toHaveBeenCalledTimes(1);
    expect(store.isAuthenticated()).toBe(false);
    expect(store.user()).toBeNull();
  });
});
