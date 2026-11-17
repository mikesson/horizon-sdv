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

/** A single breadcrumb: a display name and the absolute path it navigates to. */
export interface PathPart {
  name: string;
  path: string;
}

/**
 * Splits an absolute path into cumulative breadcrumbs, always leading with the
 * root (empty name → `/`). `/sdcard/Download` → root, `sdcard` (`/sdcard`),
 * `Download` (`/sdcard/Download`).
 */
export function toPathParts(currentPath: string): PathPart[] {
  const parts = currentPath.split('/').filter(Boolean);
  const result: PathPart[] = [{name: '', path: '/'}];
  let path = '';
  for (const part of parts) {
    path += `/${part}`;
    result.push({name: part, path});
  }
  return result;
}

/** The parent directory of an absolute path; `/` stays `/`. */
export function parentPath(currentPath: string): string {
  if (currentPath === '/') {
    return '/';
  }
  const parts = currentPath.split('/').filter(Boolean);
  parts.pop();
  return `/${parts.join('/')}` || '/';
}
