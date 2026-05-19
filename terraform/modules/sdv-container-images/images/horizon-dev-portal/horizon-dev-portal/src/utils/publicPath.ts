// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
import type { AppConfig } from '../types';

function cfg(): AppConfig {
  if (typeof window !== 'undefined' && window.APP_CONFIG) {
    return window.APP_CONFIG;
  }
  return {};
}

/**
 * Browser URL path prefix where this SPA is mounted (leading slash, no trailing slash).
 * Empty string when the app is served at the site root.
 * Populated from `publicPath` in config.js / `PUBLIC_PATH` in the Go proxy (Helm `config.publicPath`).
 */
export function getRouterBasename(): string {
  const raw = cfg().publicPath;
  if (raw === undefined || raw === '') {
    return '';
  }
  let p = String(raw).trim();
  if (!p.startsWith('/')) {
    p = `/${p}`;
  }
  p = p.replace(/\/+$/, '');
  return p === '/' ? '' : p;
}

/** Same-origin base for API proxy URLs (`/api/mm`, `/api/horizon`). */
export function getAppOriginBase(): string {
  if (typeof window === 'undefined') {
    return '';
  }
  const p = getRouterBasename();
  return `${window.location.origin}${p}`;
}
