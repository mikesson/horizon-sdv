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
 * The AOSP branch that publishes fetchable Cuttlefish artifacts under the 2026
 * trunk-stable model, and the phone target. Prefilled wherever a create offers
 * to track a branch. Device and batch creates both use them, which is why they
 * are not owned by either.
 */
export const DEFAULT_TRACK_BRANCH = 'aosp-android-latest-release';
export const DEFAULT_TRACK_TARGET = 'aosp_cf_x86_64_only_phone-userdebug';
