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

export const ADMIN_ROUTES: Routes = [
  {
    path: '',
    canActivate: [authGuard],
    children: [
      {
        path: '',
        redirectTo: 'capacity',
        pathMatch: 'full',
      },
      {
        path: 'capacity',
        loadComponent: () =>
          import('./components/capacity/capacity.component').then(m => m.CapacityComponent),
      },
      {
        path: 'control-plane',
        loadComponent: () =>
          import('./components/operations/operations.component').then(m => m.OperationsComponent),
      },
      {
        path: 'autoscaler',
        loadComponent: () =>
          import('./components/autoscaler/autoscaler.component').then(m => m.AutoscalerComponent),
      },
      {
        path: 'catalog',
        loadComponent: () =>
          import('./components/catalog/catalog.component').then(m => m.CatalogComponent),
      },
      {
        path: 'activity',
        loadComponent: () =>
          import('./components/activity/activity.component').then(m => m.ActivityComponent),
      },
      {
        path: 'settings',
        loadComponent: () =>
          import('./components/settings/settings.component').then(m => m.SettingsComponent),
      },
      // Placement and Artifacts consolidated into Settings; both were
      // bookmarkable admin pages, so they redirect rather than 404.
      {
        path: 'placement',
        redirectTo: 'settings',
      },
      {
        path: 'artifacts',
        redirectTo: 'settings',
      },
      // Pre-catalog deep links.
      {
        path: 'host-images',
        redirectTo: 'catalog',
      },
      // This page was 'operations' until the API took that noun for AIP-151
      // operation handles. Two things called Operations, one of them a
      // dashboard, is the collision worth spending a redirect on.
      {
        path: 'operations',
        redirectTo: 'control-plane',
      },
    ],
  },
];
