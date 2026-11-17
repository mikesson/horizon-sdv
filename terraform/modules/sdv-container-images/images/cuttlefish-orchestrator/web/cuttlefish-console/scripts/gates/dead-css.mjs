// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Static dead-CSS detector for a console whose styles live in three places with
 * three different scoping rules.
 *
 * Standard unused-CSS tools (PurgeCSS, `stylelint-no-unused-selectors`) assume a
 * component is a template file plus a co-located stylesheet. This console keeps
 * both inline in the `@Component` decorator and applies classes dynamically
 * (`[class.x]`, `[class]="expr"`), which those tools can't follow.
 *
 * Findings come in two strengths, and they exit differently:
 *
 *   CERTAIN:     a custom property nothing reads, a `var()` of a property
 *                nothing declares, a token name Material no longer reads, a
 *                deleted light-ramp hex. Each is a closed question over text
 *                this repo owns both sides of, so these always fail.
 *   HEURISTIC:   a class selector no sibling markup mentions. The matcher
 *                cannot see a name assembled at runtime, so these are ratcheted
 *                against `.deadcss-allow` rather than failing outright.
 *
 * What this cannot decide, by construction: whether a `--mat-*` override is
 * live. CCO declares those for Material to read, so absence of a local `var()`
 * is the expected state and proves nothing either way. Only a rendered pixel
 * separates a live override from a dead one, which is what
 * `e2e/theme-accent.spec.ts` and `npm run audit:css-usage` do instead.
 *
 * Usage:
 *   node scripts/audit/dead-css.mjs            # exit 1 on any finding that counts
 *   node scripts/audit/dead-css.mjs --report   # print everything, always exit 0
 */

import {existsSync, readFileSync, readdirSync, statSync} from 'node:fs';
import {dirname, join, relative, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(HERE, '../..');
const SRC = join(ROOT, 'src');
const ALLOW_FILE = join(ROOT, '.deadcss-allow');

const rel = p => relative(ROOT, p);

/** A Sass `@use` target, if it names a file in this repo rather than a package. */
function sassTarget(from, ref) {
  const name = ref.split('/').pop();
  const dir = join(dirname(from), ref.slice(0, ref.length - name.length));
  for (const file of [`_${name}.scss`, `${name}.scss`]) {
    const p = join(dir, file);
    if (existsSync(p)) return p;
  }
  return null;
}

/**
 * Stylesheets attached to no component. Their scope is the whole app rather
 * than one component's encapsulated DOM, so they take the opposite usage rule
 * below.
 *
 * Derived from the builder's `styles` array rather than listed here: a second
 * copy of that list drifts the moment a sheet is added to it, and this audit
 * then cannot see the file it exists to check. Third-party entries are dropped,
 * because their class names are not ours to delete, and the `@use` graph is
 * followed because `styles.scss` is four partials behind one entry.
 */
function globalSheets() {
  const config = JSON.parse(readFileSync(join(ROOT, 'angular.json'), 'utf8'));
  const sheets = [];
  const visit = p => {
    if (!existsSync(p) || sheets.includes(rel(p))) return;
    sheets.push(rel(p));
    for (const m of readFileSync(p, 'utf8').matchAll(/@(?:use|forward)\s+['"]([^'"]+)['"]/g)) {
      const target = sassTarget(p, m[1]);
      if (target) visit(target);
    }
  };
  for (const project of Object.values(config.projects)) {
    for (const entry of project.architect?.build?.options?.styles ?? []) {
      const input = typeof entry === 'string' ? entry : entry.input;
      if (input?.startsWith('src/')) visit(join(ROOT, input));
    }
  }
  return sheets;
}

const GLOBAL_SHEETS = globalSheets();

// Class prefixes that target framework-rendered DOM, not authored markup. We
// can't verify these from source: Angular Material/CDK, CodeMirror, xterm.js.
const FRAMEWORK_PREFIXES = ['mat-', 'mdc-', 'cdk-', 'ng-', 'cm-', 'xterm'];

// Light-ramp hexes deleted from the palette. The Sass build already fails on an
// undefined `$gcp-blue-50`, but nothing stops the literal being typed back in,
// and every one of these reached a light-on-light pair as a literal first.
const DELETED_LIGHT_RAMP = ['#e8f0fe', '#d2e3fc', '#f1f3f4', '#dadce0'];

// Light-theme values that are almost always bugs in this dark-only console.
// White is only flagged as a *background* at >=0.5 opacity. Low-opacity white
// borders/text/tints are the normal way to render subtle edges on dark and are
// intentional in the bespoke-dark subtrees.
const SUSPICIOUS = [
  {re: /background[^;{]*(\bwhite\b|#fff(fff)?\b|rgba\(\s*255,\s*255,\s*255,\s*(0?\.[5-9]\d*|1(\.0+)?)\s*\))/i, msg: 'near-white background (bug on dark)'},
  {re: /rgba\(\s*26,\s*115,\s*232/, msg: 'old LIGHT primary #1a73e8'},
  {re: /rgba\(\s*60,\s*64,\s*67/, msg: 'light-theme grey shadow (invisible on dark)'},
];

function walk(dir, suffix) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...walk(p, suffix));
    else if (name.endsWith(suffix) && !name.endsWith('.spec.ts')) out.push(p);
  }
  return out;
}

const stripComments = s => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

// A print stylesheet is the one place a white background is right on a dark
// console, and `styles.scss` has one. Dropped before the value checks so the
// only near-white finding in the tree stops being a false positive.
const stripPrintBlocks = s => s.replace(/@media\s+print\s*\{(?:[^{}]|\{[^{}]*\})*\}/g, '');

// All backtick-delimited template literals. Angular templates use `{{ }}`, not
// `${ }`, and the inline CSS is static, so naive backtick splitting is safe.
const backtickLiterals = src => [...src.matchAll(/`([^`]*)`/gs)].map(m => m[1]);

const isCss = s => /\{[^}]*[:;][^}]*\}/.test(s) && /(^|\s)(\.[\w-]+|:host|@media)/.test(s);

/** `.foo` selectors, ignoring decimals (`0.5`), units (`1.5px`) and chained dots. */
function classSelectors(css) {
  const names = new Set();
  for (const m of css.matchAll(/(?<![\w.])\.(-?[a-zA-Z_][\w-]*)/g)) {
    if (!FRAMEWORK_PREFIXES.some(p => m[1].startsWith(p))) names.add(m[1]);
  }
  return names;
}

const usedAsClass = (haystack, name) =>
  new RegExp(`(?<![\\w-])${name.replace(/-/g, '\\-')}(?![\\w-])`).test(haystack);

function externalFile(fromPath, ref) {
  const p = resolve(dirname(fromPath), ref);
  return existsSync(p) ? readFileSync(p, 'utf8') : '';
}

/** Inline CSS plus any `styleUrl` target, for one component file. */
function componentCss(file, src) {
  const external = [...src.matchAll(/styleUrls?\s*:\s*\[?\s*['"]([^'"]+)['"]/g)]
    .map(m => externalFile(file, m[1])).join('\n');
  return {blocks: backtickLiterals(src).filter(isCss), external};
}

/**
 * Custom-property declarations carrying a given prefix.
 *
 * The lookbehind is what makes this correct. `.btn--outlined:hover` contains
 * the exact characters of a `--outlined:` declaration, and a BEM-heavy codebase
 * has hundreds of them. Requiring the `--` to start a token drops every one.
 */
function declaredProperties(text, prefix) {
  const esc = prefix.replace(/-/g, '\\-');
  return [...text.matchAll(new RegExp(`(?<![\\w\\-&.%#])(${esc}[\\w-]*)\\s*:`, 'g'))].map(m => m[1]);
}

const readProperties = text => [...text.matchAll(/var\(\s*(--[\w-]+)/g)].map(m => m[1]);

/**
 * `var(--cco-x, <fallback>)` uses. Every one of them is unreachable: the token
 * layer declares `--cco-*` on `:root`, and this pass fails when it does not, so
 * the fallback arm can never be taken. What it can do is go stale. Four of the
 * eighteen already held colours the palette had moved off, which reads as the
 * rendered value and is not.
 */
const fallbackReads = text =>
  // The fallback arm matches one level of nesting, because the commonest one in
  // this tree is another `var()`, and stopping at the first `)` would print
  // half a token name and make the finding unreadable.
  [...text.matchAll(/var\(\s*(--cco-[\w-]+)\s*,\s*((?:[^()]|\([^()]*\))*)\)/g)].map(m => ({
    name: m[1],
    fallback: m[2].trim(),
  }));

/**
 * Every `.class` the console's own styles define, mapped to the files defining
 * it. Exported so the runtime sweep can attribute an unused emitted rule to
 * source, where the rule's own selector text is a compiler-generated
 * `[_ngcontent-ng-c123]`.
 */
export function selectorSources() {
  const index = new Map();
  const add = (sel, file) => {
    if (!index.has(sel)) index.set(sel, new Set());
    index.get(sel).add(rel(file));
  };
  for (const file of walk(SRC, '.component.ts')) {
    const {blocks, external} = componentCss(file, readFileSync(file, 'utf8'));
    for (const sel of classSelectors(stripComments(`${blocks.join('\n')}\n${external}`))) {
      add(sel, file);
    }
  }
  for (const sheet of GLOBAL_SHEETS) {
    const p = join(ROOT, sheet);
    if (!existsSync(p)) continue;
    for (const sel of classSelectors(stripComments(readFileSync(p, 'utf8')))) add(sel, p);
  }
  return index;
}

function loadAllow() {
  if (!existsSync(ALLOW_FILE)) return new Set();
  return new Set(readFileSync(ALLOW_FILE, 'utf8').split('\n')
    .map(line => line.replace(/#.*$/, '').trim())
    .filter(Boolean)
    .map(line => line.split(/\s+/).slice(0, 2).join('\t')));
}

/** Per component: a class defined in one component cannot style a sibling. */
function componentFindings() {
  const heuristic = [];
  const suspicious = [];
  for (const file of walk(SRC, '.component.ts')) {
    const src = readFileSync(file, 'utf8');
    const {blocks, external} = componentCss(file, src);
    const tmplRef = src.match(/templateUrl\s*:\s*['"]([^'"]+)['"]/)?.[1];

    const allCss = stripComments(`${blocks.join('\n')}\n${external}`);
    if (!allCss.trim()) continue;

    // Usage = everything EXCEPT the CSS blocks (so a selector definition can't
    // count as its own usage), plus any external template.
    let usage = src;
    for (const block of blocks) usage = usage.replace(block, '');
    usage += `\n${tmplRef ? externalFile(file, tmplRef) : ''}`;

    for (const sel of [...classSelectors(allCss)].sort()) {
      if (!usedAsClass(usage, sel)) heuristic.push({file: rel(file), sel});
    }
    for (const {re, msg} of SUSPICIOUS) {
      if (re.test(stripPrintBlocks(allCss))) suspicious.push({file: rel(file), msg});
    }
  }
  return {heuristic, suspicious};
}

/**
 * Global sheets take the opposite scoping rule: a class there is alive if any
 * template anywhere mentions it. Nothing else in the toolchain opens these files
 * at all, because they are not reachable from a `styleUrl`, so a per-component
 * walk never sees them.
 */
function globalSheetFindings(appText) {
  const heuristic = [];
  const suspicious = [];
  for (const sheet of GLOBAL_SHEETS) {
    const p = join(ROOT, sheet);
    if (!existsSync(p)) continue;
    const css = stripComments(readFileSync(p, 'utf8'));
    for (const sel of [...classSelectors(css)].sort()) {
      if (!usedAsClass(appText, sel)) heuristic.push({file: sheet, sel});
    }
    const values = stripPrintBlocks(css);
    for (const {re, msg} of SUSPICIOUS) if (re.test(values)) suspicious.push({file: sheet, msg});
  }
  return {heuristic, suspicious};
}

/**
 * Custom properties, both directions. Only `--cco-*` is judged for liveness:
 * this repo writes and reads those, so a missing counterpart is a closed
 * question. `--mat-*` is absent on purpose, because CCO declares those for
 * Material to read and no local search can see that consumer.
 */
function tokenFindings(files) {
  const declared = new Map();
  const read = new Map();
  const certain = [];

  for (const file of files) {
    const text = readFileSync(file, 'utf8');
    for (const name of declaredProperties(text, '--cco-')) {
      if (!declared.has(name)) declared.set(name, rel(file));
    }
    for (const name of readProperties(text)) {
      if (!read.has(name)) read.set(name, rel(file));
    }
    const fallbacks = new Map(fallbackReads(text).map(f => [`${f.name}, ${f.fallback}`, f]));
    for (const key of fallbacks.keys()) {
      certain.push({file: rel(file),
        msg: `var(${key}): the fallback arm is unreachable; read the token alone`});
    }
    for (const name of new Set(declaredProperties(text, '--mdc-'))) {
      certain.push({file: rel(file), msg: `${name}: Material v22 reads no --mdc-* name; it is inert`});
    }
    for (const hex of DELETED_LIGHT_RAMP) {
      if (new RegExp(`${hex}\\b`, 'i').test(text)) {
        certain.push({file: rel(file), msg: `${hex} is a deleted light-ramp value`});
      }
    }
  }

  for (const [name, file] of declared) {
    if (!read.has(name)) certain.push({file, msg: `${name} declared but no var() reads it`});
  }
  for (const [name, file] of read) {
    if (name.startsWith('--cco-') && !declared.has(name)) {
      certain.push({file, msg: `var(${name}) reads a property nothing declares`});
    }
  }
  return certain.sort((a, b) => a.file.localeCompare(b.file) || a.msg.localeCompare(b.msg));
}

function main() {
  const reportOnly = process.argv.includes('--report');
  const allow = loadAllow();
  const bold = s => `\x1b[1m${s}\x1b[0m`;

  // The global sheets are named explicitly: the walk is by extension, and one
  // of them is plain `.css`.
  const styleFiles = [...new Set([...walk(SRC, '.ts'), ...walk(SRC, '.scss'),
    ...GLOBAL_SHEETS.map(sheet => join(ROOT, sheet))])];
  // Markup only: a global class referenced from another stylesheet is styled
  // twice, not used once, so `.scss` stays out of the usage corpus.
  const appText = [...walk(SRC, '.ts'), ...walk(SRC, '.html')]
    .map(f => readFileSync(f, 'utf8'))
    .join('\n');

  const comp = componentFindings();
  const glob = globalSheetFindings(appText);
  const certain = tokenFindings(styleFiles);
  const heuristic = [...comp.heuristic, ...glob.heuristic];
  const fresh = heuristic.filter(f => !allow.has(`${f.file}\t.${f.sel}`));

  if (certain.length) {
    console.log(bold('\ncustom properties and palette, closed questions never allow-listed:'));
    for (const {file, msg} of certain) console.log(`  \x1b[31m${msg}\x1b[0m\n    ${file}`);
  }

  const byFile = new Map();
  for (const f of reportOnly ? heuristic : fresh) {
    if (!byFile.has(f.file)) byFile.set(f.file, []);
    byFile.get(f.file).push(f.sel);
  }
  if (byFile.size) {
    console.log(bold('\nclass selectors no sibling markup mentions:'));
    for (const [file, sels] of byFile) {
      console.log(`  ${file}\n    \x1b[31m${sels.map(s => `.${s}`).join('  ')}\x1b[0m`);
    }
  }

  const seen = new Set();
  for (const {file, msg} of [...comp.suspicious, ...glob.suspicious]) {
    if (seen.has(`${file}${msg}`)) continue;
    seen.add(`${file}${msg}`);
    console.log(`\n\x1b[33msuspicious value:\x1b[0m ${msg}\n  ${file}`);
  }

  const dupes = [...selectorSources().entries()]
    .filter(([sel, files]) => files.size >= 3
      && !['selected', 'active', 'full-width', 'disabled'].includes(sel))
    .sort((a, b) => b[1].size - a[1].size)
    .slice(0, 15);
  if (dupes.length) {
    console.log(bold('\ncross-component duplicate selectors (extract to a shared class?):'));
    for (const [sel, files] of dupes) console.log(`  .${sel} is defined in ${files.size} places`);
  }

  console.log(`\n${bold('Summary:')} ${certain.length} certain, `
    + `${fresh.length} unallowed selector(s) of ${heuristic.length}, `
    + `${seen.size} suspicious value(s), ${dupes.length} duplicated selector group(s).`);

  if (reportOnly) return;
  if (certain.length || fresh.length) {
    console.log('\nDelete them, or record a selector in .deadcss-allow with the reason it cannot '
      + 'be seen statically. Certain findings are never allow-listed.');
    process.exit(1);
  }
}

if (resolve(process.argv[1] ?? '') === resolve(fileURLToPath(import.meta.url))) main();
