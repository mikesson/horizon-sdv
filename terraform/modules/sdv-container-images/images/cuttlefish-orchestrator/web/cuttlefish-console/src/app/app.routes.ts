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
import {UserProfileComponent} from './auth/components/user-profile/user-profile.component';
import {authGuard} from './auth/guards/auth.guard';
import {DashboardComponent} from './dashboard/dashboard.component';
import {UnauthorizedComponent} from './shared/components/unauthorized/unauthorized.component';

export const routes: Routes = [
  // Dashboard requires authentication
  {path: '', component: DashboardComponent, pathMatch: 'full', canActivate: [authGuard]},
  // Login happens at the edge: Cloud Run IAP authenticates the browser
  // before any request reaches the SPA. There is no Angular login route.
  {path: 'profile', component: UserProfileComponent, canActivate: [authGuard]},
  // Shown when authentication state cannot be established
  {path: 'unauthorized', component: UnauthorizedComponent},
  // Feature module routes (guards are in the feature route files)
  {
    path: 'hosts',
    loadChildren: () => import('./hosts/hosts.routes').then(m => m.HOST_ROUTES),
  },
  {
    path: 'devices',
    loadChildren: () => import('./devices/devices.routes').then(m => m.DEVICE_ROUTES),
  },
  {
    path: 'builds',
    loadChildren: () => import('./builds/builds.routes').then(m => m.BUILD_ROUTES),
  },
  {
    path: 'batches',
    loadChildren: () => import('./batches/batches.routes').then(m => m.BATCH_ROUTES),
  },
  // Admin routes
  {
    path: 'admin',
    loadChildren: () => import('./admin/admin.routes').then(m => m.ADMIN_ROUTES),
  },
];
