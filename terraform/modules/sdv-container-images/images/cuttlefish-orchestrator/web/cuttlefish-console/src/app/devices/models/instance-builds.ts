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
 * The builds a launch config names per instance. They are read from the stored
 * env_config, so they record what each instance was asked to boot. An instance
 * that names none boots the device build and is left out.
 */

interface InstanceBuildRef {
  readonly instance: string;
  /** `registered` is a CCO build id from `@build`; `url` is `disk.default_build`. */
  readonly kind: 'registered' | 'url';
  readonly value: string;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

export function instanceBuildRefs(
  envConfig: Record<string, unknown> | undefined
): InstanceBuildRef[] {
  const instances = envConfig?.['instances'];
  if (!Array.isArray(instances)) {
    return [];
  }
  const refs: InstanceBuildRef[] = [];
  instances.forEach((raw: unknown, i) => {
    const entry = asRecord(raw);
    if (!entry) {
      return;
    }
    const name = typeof entry['name'] === 'string' ? entry['name'] : `instance ${i + 1}`;
    const id = entry['@build'];
    const url = asRecord(entry['disk'])?.['default_build'];
    if (typeof id === 'string' && id !== '') {
      refs.push({instance: name, kind: 'registered', value: id});
    } else if (typeof url === 'string' && url !== '') {
      refs.push({instance: name, kind: 'url', value: url});
    }
  });
  return refs;
}
