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

/** Routing rules for /devices/create, its chooser and its two modes. */

import {inject} from '@angular/core';
import type {CanActivateFn, CanMatchFn, RedirectFunction} from '@angular/router';
import {Router} from '@angular/router';
import {FeatureFlagsService} from '../../../shared/services';

export type CreateMode = 'simple' | 'advanced';

/** The ?preset= value the chooser's SDV card sends to Advanced. */
export const SDV_PAIR_PRESET = 'sdv-pair';

/** Matches a mode segment this deployment offers; anything else falls through. */
export const createModeMatch: CanMatchFn = (_route, segments) => {
  const mode = segments[0]?.path;
  return (
    mode === 'simple' ||
    (mode === 'advanced' && inject(FeatureFlagsService).advancedCvdConfigEnabled)
  );
};

/** With Advanced mode off there is nothing to choose, so the chooser is skipped. */
export const skipChooserWithoutAdvanced: CanActivateFn = route =>
  inject(FeatureFlagsService).advancedCvdConfigEnabled ||
  inject(Router).createUrlTree(['/devices/create/simple'], {queryParams: route.queryParams});

/**
 * Sends an unknown mode back to the chooser. A string redirect would drop the
 * query string, and with it a handoff's host or build.
 */
export const redirectToChooser: RedirectFunction = ({queryParams}) =>
  inject(Router).createUrlTree(['/devices/create'], {queryParams});
