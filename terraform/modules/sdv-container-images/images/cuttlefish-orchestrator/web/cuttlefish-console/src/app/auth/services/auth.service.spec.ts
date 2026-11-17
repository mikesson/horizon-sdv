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

import {provideHttpClient} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {LoggingService} from '../../shared/services';
import {browserLocation} from '../../shared/utils';
import type {User} from '../models/user.model';
import {AuthService} from './auth.service';

describe('AuthService', () => {
  let baseEl: HTMLBaseElement | null = null;
  let assignSpy: ReturnType<typeof vi.spyOn>;

  const mountBase = (href: string): void => {
    baseEl?.remove();
    baseEl = document.createElement('base');
    baseEl.setAttribute('href', href);
    document.head.appendChild(baseEl);
  };

  beforeEach(() => {
    assignSpy = vi.spyOn(browserLocation, 'assign').mockReturnValue(undefined);
  });

  afterEach(() => {
    baseEl?.remove();
    baseEl = null;
    vi.restoreAllMocks();
  });

  const setup = (): {service: AuthService; httpMock: HttpTestingController} => {
    TestBed.configureTestingModule({
      providers: [
        AuthService,
        provideHttpClient(),
        provideHttpClientTesting(),
        {provide: LoggingService, useValue: {error: vi.fn()}},
      ],
    });
    return {
      service: TestBed.inject(AuthService),
      httpMock: TestBed.inject(HttpTestingController),
    };
  };

  it('loads current user from subpath baseUrl when base href is configured', () => {
    mountBase('/cuttlefish-orchestrator/');
    const {service, httpMock} = setup();

    let loaded: User | undefined;
    service.loadCurrentUser().subscribe(user => {
      loaded = user;
    });

    const req = httpMock.expectOne('/cuttlefish-orchestrator/v2/me');
    expect(req.request.method).toBe('GET');
    req.flush({email: 'dev@example.com', display_name: 'Dev User'});
    httpMock.verify();

    expect(loaded).toEqual({email: 'dev@example.com', displayName: 'Dev User'});
  });

  it('redirects logout to oauth2/sign_out with encoded base href return path', () => {
    mountBase('/cuttlefish-orchestrator/');
    const {service, httpMock} = setup();

    service.logout().subscribe();
    httpMock.verify();

    expect(assignSpy).toHaveBeenCalledWith(
      '/cuttlefish-orchestrator/oauth2/sign_out?rd=%2Fcuttlefish-orchestrator%2F'
    );
  });

  it('uses root oauth2/sign_out when no base tag is mounted', () => {
    const {service, httpMock} = setup();

    service.logout().subscribe();
    httpMock.verify();

    expect(assignSpy).toHaveBeenCalledWith('/oauth2/sign_out?rd=%2F');
  });
});
