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
/**
 * Module overview HTML uses prefers-color-scheme; the portal iframe follows MUI palette instead.
 * Inject theme CSS + data-hp-theme on <html> so CSS variables match the Developer Portal light/dark toggle.
 */
const OVERVIEW_THEME_BRIDGE = `
html[data-hp-theme="dark"] {
  color-scheme: dark;
  --bg: #202124;
  --paper: #292a2d;
  --text: #e8eaed;
  --muted: #9aa0a6;
  --primary: #8ab4f8;
  --border: #3c4043;
  --hero-end: rgba(26, 115, 232, 0.12);
  --hero-start: rgba(66, 133, 244, 0.28);
}
html[data-hp-theme="light"] {
  color-scheme: light;
  --bg: #f8f9fa;
  --paper: #ffffff;
  --text: #202124;
  --muted: #5f6368;
  --primary: #1a73e8;
  --border: #e8eaed;
  --hero-end: rgba(26, 115, 232, 0.08);
  --hero-start: rgba(66, 133, 244, 0.22);
}
`.trim();

export function injectOverviewPortalTheme(html: string, mode: 'light' | 'dark'): string {
  const bridge = `<style type="text/css" data-hp-overview-theme="1">${OVERVIEW_THEME_BRIDGE}</style>`;
  const withHtml = html.replace(/<html(\s[^>]*)?>/i, (full, g1: string | undefined) => {
    const rest = g1 ?? '';
    if (/\sdata-hp-theme=/i.test(rest)) {
      return full.replace(/\sdata-hp-theme="[^"]*"/i, ` data-hp-theme="${mode}"`);
    }
    return `<html${rest} data-hp-theme="${mode}">`;
  });
  if (/<\/head>/i.test(withHtml)) {
    return withHtml.replace(/<\/head>/i, `${bridge}</head>`);
  }
  return bridge + withHtml;
}
