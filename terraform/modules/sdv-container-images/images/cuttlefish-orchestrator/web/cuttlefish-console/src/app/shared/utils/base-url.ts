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

function normalizedOrigin(): string {
  const origin = typeof globalThis.location !== 'undefined' ? globalThis.location.origin : '';
  return origin && origin !== 'null' ? origin : 'http://localhost';
}

function resolvedBaseUrl(): string {
  const origin = normalizedOrigin();
  const doc = typeof globalThis.document !== 'undefined' ? globalThis.document : undefined;
  const baseHref = doc?.querySelector('base')?.getAttribute('href');
  if (baseHref === null || baseHref === undefined) {
    return `${origin}/`;
  }
  const fallbackTarget = baseHref.length > 0 ? baseHref : '/';
  const rawBase =
    doc && /^https?:\/\//i.test(doc.baseURI)
      ? doc.baseURI
      : new URL(fallbackTarget, `${origin}/`).toString();
  const parsed = new URL(rawBase);
  parsed.search = '';
  parsed.hash = '';
  if (!parsed.pathname.endsWith('/')) {
    parsed.pathname = `${parsed.pathname}/`;
  }
  return parsed.toString();
}

/**
 * Resolves an app-relative path (API endpoint, docs URL, or SPA route for
 * window.open) against the active HTML `<base href>` (`document.baseURI`).
 *
 * - Production (`--base-href /cuttlefish-orchestrator/`), even on a nested SPA route:
 *   `resolveBasePath('/v2/devices')` -> `'/cuttlefish-orchestrator/v2/devices'`
 * - Local `ng serve` (`<base href="/">`) or Vitest JSDOM (no `<base>` tag):
 *   `resolveBasePath('/v2/devices')` -> `'/v2/devices'`
 * - Idempotent if already prefixed; leaves `http(s)://` URLs untouched.
 */
export function resolveBasePath(relativePath: string): string {
  if (/^https?:\/\//i.test(relativePath)) {
    return relativePath;
  }
  const baseUrl = resolvedBaseUrl();
  const basePathname = new URL(baseUrl).pathname;
  const cleaned = relativePath.replace(/^\/+/, '');
  const parsedRelative = new URL(`/${cleaned}`, normalizedOrigin());
  if (
    basePathname !== '/' &&
    (parsedRelative.pathname.startsWith(basePathname) ||
      `${parsedRelative.pathname}/` === basePathname)
  ) {
    return `${parsedRelative.pathname}${parsedRelative.search}${parsedRelative.hash}`;
  }
  const resolved = new URL(cleaned, baseUrl);
  return `${resolved.pathname}${resolved.search}${resolved.hash}`;
}

/**
 * Resolves an app-relative endpoint into a `ws:` or `wss:` URL anchored on
 * `<base href>` and the active window origin.
 */
export function resolveWebSocketUrl(relativePath: string): string {
  const url = new URL(resolveBasePath(relativePath), normalizedOrigin());
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  return url.toString();
}

/**
 * Browser navigation wrapper so auth flows can trigger full-page redirects and
 * reloads in production while staying cleanly mockable under Vitest.
 */
export const browserLocation = {
  assign: (url: string): void => {
    window.location.assign(url);
  },
  reload: (): void => {
    window.location.reload();
  },
};
