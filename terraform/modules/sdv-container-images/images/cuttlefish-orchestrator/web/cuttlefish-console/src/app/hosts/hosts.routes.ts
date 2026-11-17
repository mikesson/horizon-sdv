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

import type {Routes} from '@angular/router';
import {authGuard} from '../auth/guards/auth.guard';
import {HostCreateComponent} from './components/host-create/host-create.component';
import {HostDetailComponent} from './components/host-detail/host-detail.component';
import {HostListComponent} from './components/host-list/host-list.component';

export const HOST_ROUTES: Routes = [
  {
    path: '',
    component: HostListComponent,
    canActivate: [authGuard],
  },
  {
    path: 'create',
    component: HostCreateComponent,
    canActivate: [authGuard],
  },
  {
    path: ':id',
    component: HostDetailComponent,
    canActivate: [authGuard],
  },
];
