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

import {z} from 'zod';

/**
 * Artifact settings: per-type bucket overrides ('' = unset), the deployment
 * default they fall back to, and the capture-on-error toggle.
 */
export interface ArtifactBuckets {
  bugreportBucket: string;
  recordingBucket: string;
  defaultBucket: string;
  captureOnError: boolean;
  /** Whether an operator stored any artifact setting. */
  isSet: boolean;
}

export const ArtifactBucketsSchema = z
  .object({
    bugreport_bucket: z.string(),
    recording_bucket: z.string(),
    default_bucket: z.string(),
    capture_on_error: z.boolean(),
    // Optional so a response predating the field degrades to "defaulted"
    // rather than failing the read.
    is_set: z.boolean().optional().default(false),
  })
  .transform(
    (d): ArtifactBuckets => ({
      bugreportBucket: d.bugreport_bucket,
      recordingBucket: d.recording_bucket,
      defaultBucket: d.default_bucket,
      captureOnError: d.capture_on_error,
      isSet: d.is_set,
    })
  );
