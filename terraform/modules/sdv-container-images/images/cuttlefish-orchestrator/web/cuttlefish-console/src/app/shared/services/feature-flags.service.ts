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

/**
 * Feature Flags Service
 *
 * Provides type-safe access to feature flags from environment configuration.
 * Feature flags control the availability of experimental or gated features.
 */

import {Service} from '@angular/core';
import {environment} from '../../../environments/environment';

/**
 * Feature flag definitions
 */
export interface FeatureFlags {
  /**
   * Enable advanced CVD configuration mode with full JSON editing,
   * multi-instance support, and form ↔ JSON bidirectional sync.
   */
  advancedCvdConfig: boolean;
}

/**
 * Service for checking feature flags at runtime.
 *
 * @example
 * ```typescript
 * @Component({...})
 * class MyComponent {
 *   private featureFlags = inject(FeatureFlagsService);
 *
 *   showAdvancedMode = this.featureFlags.isEnabled('advancedCvdConfig');
 * }
 * ```
 */
@Service()
export class FeatureFlagsService {
  private readonly flags: FeatureFlags;

  constructor() {
    this.flags = environment.features;
  }

  /**
   * Checks if a feature flag is enabled.
   *
   * @returns True if the feature is enabled
   */
  isEnabled(flag: keyof FeatureFlags): boolean {
    return this.flags[flag] ?? false;
  }

  /**
   * Gets all feature flags.
   */
  getAllFlags(): FeatureFlags {
    return {...this.flags};
  }

  /**
   * Checks if advanced CVD configuration mode is enabled.
   */
  get advancedCvdConfigEnabled(): boolean {
    return this.flags.advancedCvdConfig;
  }
}
