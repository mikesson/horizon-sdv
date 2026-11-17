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
import {toSignal} from '@angular/core/rxjs-interop';
import {of} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import type {PlatformLifetimes} from '../models/platform-lifetimes.schema';
import {COMPILED_LIFETIMES, PlatformLifetimesSchema} from '../models/platform-lifetimes.schema';
import {resolveBasePath} from '../utils';

/**
 * The platform's lifetime policy, fetched once per app session and shared by
 * every create and extend surface. A failed fetch or parse falls back to the
 * compiled defaults. A wizard rendering the stock 4-hour preset beats one
 * rendering nothing.
 */
@Service()
export class PlatformPolicyService {
  private readonly http = inject(HttpClient);

  readonly lifetimes = toSignal(
    this.http.get<unknown>(resolveBasePath('/v2/admin/system-config/lifetimes')).pipe(
      map((raw): PlatformLifetimes => {
        const parsed = PlatformLifetimesSchema.safeParse(raw);
        return parsed.success ? parsed.data : COMPILED_LIFETIMES;
      }),
      catchError(() => of(COMPILED_LIFETIMES))
    ),
    {initialValue: COMPILED_LIFETIMES}
  );
}
