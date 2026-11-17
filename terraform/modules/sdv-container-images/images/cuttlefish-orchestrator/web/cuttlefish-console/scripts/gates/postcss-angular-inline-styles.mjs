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
 * Stylelint custom syntax for Angular's inline component styles.
 *
 * The off-the-shelf CSS-in-JS syntaxes (postcss-lit, postcss-styled-syntax)
 * key off a *tagged* template (css`…`, styled.div`…`). Angular's is untagged:
 *
 *   @Component({ styles: `.a { color: red }` })
 *
 * Point either of them at this codebase and every file parses as an empty
 * stylesheet, stylelint reports zero problems, and ~9,300 lines look linted
 * while nothing is checked. That failure is silent and reports success, so
 * `npm run lint:css` runs `css-gate-selftest.mjs` first: it plants defects
 * through this syntax and fails if they are not caught. Break the regex below
 * and stylelint still exits 0 on every real component. The self-test is the
 * only thing that goes red.
 *
 * Rather than parse a fragment and fix up every node's offset afterwards, this
 * blanks the surrounding TypeScript to spaces and keeps the newlines. The
 * result is a valid stylesheet whose line and column numbers are already the
 * ones in the original file.
 */

import {parse as parseScss} from 'postcss-scss';

/** Blanks a span to spaces, preserving newlines so positions do not shift. */
function blank(text) {
  return text.replace(/[^\n]/g, ' ');
}

/** Index of the backtick closing the template opened at `from`, or -1. */
function closingBacktick(source, from) {
  for (let i = from; i < source.length; i++) {
    if (source[i] === '\\') {
      i++;
    } else if (source[i] === '`') {
      return i;
    }
  }
  return -1;
}

export function parse(source, options) {
  const opener = /(^|[^\w$])styles\s*:\s*`/g;
  const spans = [];

  for (let m = opener.exec(source); m !== null; m = opener.exec(source)) {
    const start = m.index + m[0].length;
    const end = closingBacktick(source, start);
    if (end === -1) {
      break;
    }
    spans.push([start, end]);
    opener.lastIndex = end + 1;
  }

  let css = blank(source);
  for (const [start, end] of spans) {
    // A ${…} interpolation is not CSS and would fail to parse; blank it and
    // leave the rest of the block checkable.
    const body = source.slice(start, end).replace(/\$\{[^}]*\}/g, blank);
    css = css.slice(0, start) + body + css.slice(end);
  }

  return parseScss(css, options);
}

export function stringify(node, builder) {
  node.toString(builder);
}
