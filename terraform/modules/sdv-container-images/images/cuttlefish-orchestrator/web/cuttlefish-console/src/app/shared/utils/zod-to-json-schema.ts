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
 * Zod to JSON Schema Conversion Utilities
 *
 * Converts Zod schemas to JSON Schema format for use with CodeMirror's
 * codemirror-json-schema extension, enabling:
 * - Real-time validation
 * - Autocomplete suggestions
 * - Hover tooltips with field descriptions
 *
 * Uses Zod v4's native z.toJSONSchema() for conversion.
 */

import {z} from 'zod';
import {EnvironmentSpecificationSchema} from '../../devices/models/cvd-config.schema';

/**
 * JSON Schema type for use with CodeMirror and other tools.
 * Compatible with JSON Schema Draft 2020-12.
 */
export type JsonSchema = Record<string, unknown>;

/**
 * Converts a Zod schema to JSON Schema format using Zod v4's native support.
 *
 * Uses Draft 2020-12 by default. Metadata added via .meta() on Zod schemas
 * is automatically included in the JSON Schema output (title, description, examples).
 *
 * @returns JSON Schema compatible object (Draft 2020-12)
 */
function convertZodToJsonSchema<T extends z.ZodTypeAny>(schema: T): JsonSchema {
  return z.toJSONSchema(schema, {
    target: 'draft-2020-12', // Latest JSON Schema draft
    unrepresentable: 'any', // Convert unsupported types to {} instead of throwing
    cycles: 'ref', // Use $defs for circular references
    reused: 'ref', // Extract reused schemas as $defs for cleaner output
  });
}

/**
 * Pre-generated JSON Schema for CVD Environment Specification.
 *
 * This is the schema used by CodeMirror for validation and autocomplete
 * in the Advanced CVD Configuration mode.
 *
 * Cached at module load time for performance.
 */
const CVD_CONFIG_JSON_SCHEMA: JsonSchema = convertZodToJsonSchema(EnvironmentSpecificationSchema);

/**
 * Gets the JSON Schema for CVD configuration.
 *
 * Returns the pre-generated schema for use with CodeMirror's
 * codemirror-json-schema extension.
 */
export function getCvdConfigJsonSchema(): JsonSchema {
  return CVD_CONFIG_JSON_SCHEMA;
}
