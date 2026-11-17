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
import {DeviceCreateChooserComponent} from './components/device-create-chooser/device-create-chooser.component';
import {
  createModeMatch,
  redirectToChooser,
  skipChooserWithoutAdvanced,
} from './components/device-create-page/create-mode';
import {DeviceCreatePageComponent} from './components/device-create-page/device-create-page.component';
import {DeviceDetailComponent} from './components/device-detail/device-detail.component';
import {DeviceListComponent} from './components/device-list/device-list.component';
import {DevicePlayerComponent} from './components/device-player/device-player.component';

export const DEVICE_ROUTES: Routes = [
  {
    path: '',
    component: DeviceListComponent,
    canActivate: [authGuard],
  },
  {
    path: 'create',
    canActivate: [authGuard],
    children: [
      {
        path: '',
        component: DeviceCreateChooserComponent,
        canActivate: [skipChooserWithoutAdvanced],
      },
      {
        // One route for both modes, so moving between them reuses the page
        // and the Simple answers it holds reach Advanced in memory.
        path: ':mode',
        component: DeviceCreatePageComponent,
        canMatch: [createModeMatch],
      },
      {path: '**', redirectTo: redirectToChooser},
    ],
  },
  {
    path: ':id',
    component: DeviceDetailComponent,
    canActivate: [authGuard],
  },
  {
    path: ':id/player',
    component: DevicePlayerComponent,
    canActivate: [authGuard],
  },
];
