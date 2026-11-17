// @ts-check
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
 * ESLint flat config for Angular 22 with Google TypeScript Style rules.
 *
 * This config focuses on Angular-specific rules since Biome handles
 * general TypeScript/JavaScript linting and formatting.
 *
 * Strategy:
 * - Biome: Formatting + general TypeScript rules (fast, Rust-based)
 * - ESLint: Type-aware linting + Angular-specific rules (template support)
 *
 * @see https://github.com/angular-eslint/angular-eslint
 * @see https://typescript-eslint.io/users/configs/
 * @see https://google.github.io/styleguide/tsguide.html
 */

import angular from 'angular-eslint';
import tseslint from 'typescript-eslint';

const tsFiles = ['**/*.ts'];
const htmlFiles = ['**/*.html'];

export default tseslint.config(
  // ===========================================
  // Global ignores
  // ===========================================
  {
    ignores: [
      'node_modules/',
      'dist/',
      'build/',
      '.angular/',
      'coverage/',
      '**/*.d.ts',
    ],
  },

  // ===========================================
  // TypeScript files - Angular + Google Style
  // ===========================================
  {
    files: tsFiles,
    extends: [
      ...tseslint.configs.recommendedTypeChecked,
      ...tseslint.configs.stylisticTypeChecked,
      ...angular.configs.tsRecommended,
    ],
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    processor: angular.processInlineTemplates,
    rules: {
      // ===========================================
      // Google TypeScript Style Rules
      // ===========================================

      // Require explicit return types on functions (Google style)
      '@typescript-eslint/explicit-function-return-type': [
        'error',
        {
          allowExpressions: true,
          allowTypedFunctionExpressions: true,
          allowHigherOrderFunctions: true,
          allowDirectConstAssertionInArrowFunctions: true,
          allowConciseArrowFunctionExpressionsStartingWithVoid: true,
        },
      ],

      // Require explicit member accessibility modifiers
      '@typescript-eslint/explicit-member-accessibility': [
        'error',
        {
          accessibility: 'no-public',
          overrides: {
            constructors: 'no-public',
            properties: 'off', // Angular uses public for template bindings
          },
        },
      ],

      // Prefer readonly for properties that are never reassigned
      '@typescript-eslint/prefer-readonly': 'error',

      // Naming conventions (Google TypeScript Style)
      '@typescript-eslint/naming-convention': [
        'error',
        // Interfaces should NOT have I prefix (Google style)
        {
          selector: 'interface',
          format: ['PascalCase'],
          custom: {
            regex: '^I[A-Z]',
            match: false,
          },
        },
        // Variables: camelCase, UPPER_CASE for constants, PascalCase for Zod schemas
        {
          selector: 'variable',
          format: ['camelCase', 'UPPER_CASE', 'PascalCase'],
          leadingUnderscore: 'allow',
        },
        // Functions: camelCase
        {
          selector: 'function',
          format: ['camelCase'],
        },
        // Types and classes: PascalCase
        {
          selector: 'typeLike',
          format: ['PascalCase'],
        },
        // Class members: camelCase, allow underscore prefix for private
        {
          selector: 'memberLike',
          modifiers: ['private'],
          format: ['camelCase'],
          leadingUnderscore: 'allow',
        },
        // Static readonly class properties can be UPPER_CASE
        {
          selector: 'classProperty',
          modifiers: ['static', 'readonly'],
          format: ['camelCase', 'UPPER_CASE'],
        },
        // Parameters: camelCase, allow underscore for unused
        {
          selector: 'parameter',
          format: ['camelCase'],
          leadingUnderscore: 'allow',
        },
        // Enum members: PascalCase or UPPER_CASE
        {
          selector: 'enumMember',
          format: ['PascalCase', 'UPPER_CASE'],
        },
      ],

      // Disallow any type (Google style)
      '@typescript-eslint/no-explicit-any': 'error',

      // Require Array<T> over T[] for complex types (Google style)
      '@typescript-eslint/array-type': ['error', {default: 'array-simple'}],

      // Require consistent type assertions
      '@typescript-eslint/consistent-type-assertions': [
        'error',
        {
          assertionStyle: 'as',
          objectLiteralTypeAssertions: 'allow-as-parameter',
        },
      ],

      // Require consistent type imports (improves build performance)
      '@typescript-eslint/consistent-type-imports': [
        'error',
        {
          prefer: 'type-imports',
          disallowTypeAnnotations: false,
          fixStyle: 'separate-type-imports',
        },
      ],

      // Require consistent type exports
      '@typescript-eslint/consistent-type-exports': [
        'error',
        {fixMixedExportsWithInlineTypeSpecifier: true},
      ],

      // Prefer nullish coalescing on nullable object/union types. String and
      // number falsiness is exempt: the Zod-transform fallbacks default ''/0
      // on purpose (absent wire fields), so || is the intended operator there.
      '@typescript-eslint/prefer-nullish-coalescing': [
        'warn',
        {ignorePrimitives: {string: true, number: true}},
      ],

      // Prefer optional chain
      '@typescript-eslint/prefer-optional-chain': 'error',

      // Require Promise-like values to be awaited or returned
      '@typescript-eslint/no-floating-promises': 'error',

      // Require proper handling of promises in conditionals
      '@typescript-eslint/no-misused-promises': [
        'error',
        {
          checksVoidReturn: {
            attributes: false, // Allow async event handlers in templates
          },
        },
      ],

      // Disallow unnecessary type assertions
      '@typescript-eslint/no-unnecessary-type-assertion': 'error',

      // Disallow unused expressions
      '@typescript-eslint/no-unused-expressions': 'error',

      // ===========================================
      // Rules handled by Biome (disabled to avoid conflicts)
      // ===========================================
      '@typescript-eslint/no-unused-vars': 'off',
      '@typescript-eslint/no-empty-function': 'off',
      '@typescript-eslint/no-inferrable-types': 'off',
      'no-console': 'off',

      // ===========================================
      // Angular 22 Modern Practices
      // ===========================================

      // Require standalone components (the Angular 22 default)
      '@angular-eslint/prefer-standalone': 'error',

      // Require component selector
      '@angular-eslint/use-component-selector': 'error',

      // Prefer OnPush change detection (essential for zoneless)
      '@angular-eslint/prefer-on-push-component-change-detection': 'error',

      // Require lifecycle interface implementation
      '@angular-eslint/use-lifecycle-interface': 'error',

      // Disallow async lifecycle methods (Angular doesn't await them)
      '@angular-eslint/no-async-lifecycle-method': 'error',

      // Disallow output with prefix 'on'
      '@angular-eslint/no-output-on-prefix': 'error',

      // Sort component metadata properties
      '@angular-eslint/sort-lifecycle-methods': 'warn',

      // Prefer signals for inputs/outputs
      '@angular-eslint/prefer-signals': 'warn',

      // Signals-era correctness rules (opt-in as of angular-eslint 22.1)
      '@angular-eslint/no-uncalled-signals': 'error',
      '@angular-eslint/computed-must-return': 'error',
      '@angular-eslint/prefer-signal-model': 'error',
      '@angular-eslint/no-implicit-take-until-destroyed': 'error',
      '@angular-eslint/prefer-service-decorator': 'error',
      '@angular-eslint/inject-at-top': 'error',

      // Component selector naming
      '@angular-eslint/component-selector': [
        'error',
        {
          type: 'element',
          prefix: 'app',
          style: 'kebab-case',
        },
      ],

      // Directive selector naming
      '@angular-eslint/directive-selector': [
        'error',
        {
          type: 'attribute',
          prefix: 'app',
          style: 'camelCase',
        },
      ],

      // Disallow empty lifecycle methods
      '@angular-eslint/no-empty-lifecycle-method': 'error',

      // Disallow calling lifecycle methods directly
      '@angular-eslint/no-lifecycle-call': 'error',

      // Pipe naming
      '@angular-eslint/pipe-prefix': [
        'warn',
        {
          prefixes: ['app'],
        },
      ],
    },
  },

  // ===========================================
  // Spec files - relax only what differs in tests
  // ===========================================
  {
    files: ['**/*.spec.ts'],
    rules: {
      // expect(service.method) / vi.fn assignment patterns trip this rule
      // on every mock; TestBed wiring keeps the receiver alive.
      '@typescript-eslint/unbound-method': 'off',
      // Test doubles are type-loose by design (mock events, spy
      // restoration), and the unsafe-* family fires on every one of them.
      '@typescript-eslint/no-unsafe-assignment': 'off',
      '@typescript-eslint/no-unsafe-argument': 'off',
      '@typescript-eslint/no-unsafe-call': 'off',
      '@typescript-eslint/no-unsafe-member-access': 'off',
      '@typescript-eslint/no-unsafe-return': 'off',
    },
  },

  // ===========================================
  // HTML template files
  // ===========================================
  {
    files: htmlFiles,
    extends: [
      ...angular.configs.templateRecommended,
      ...angular.configs.templateAccessibility,
    ],
    rules: {
      // ===========================================
      // Template Modern Practices
      // ===========================================

      // ===========================================
      // Accessibility beyond templateAccessibility
      // ===========================================
      // A <button> with no type defaults to submit, so one inside a form
      // submits it on click. 246 of them were bare.
      '@angular-eslint/template/button-has-type': 'error',

      // There is no landmark rule in this plugin. Duplicate <main> is checked
      // by axe in the browser suite instead, which is where it can actually be
      // seen. aria-current has no rule anywhere; RouterLinkActive's
      // ariaCurrentWhenActive carries it.

      // Prefer new control flow syntax (@if, @for, @switch)
      '@angular-eslint/template/prefer-control-flow': 'error',

      // Prefer self-closing tags
      '@angular-eslint/template/prefer-self-closing-tags': 'error',

      // Disallow duplicate attributes
      '@angular-eslint/template/no-duplicate-attributes': 'error',

      // Disallow negated async pipe
      '@angular-eslint/template/no-negated-async': 'error',

      // Require track function for @for loops
      '@angular-eslint/template/use-track-by-function': 'warn',

      // Prefer ngSrc directive for images
      '@angular-eslint/template/prefer-ngsrc': 'warn',

      // Enforce strict equality in templates
      '@angular-eslint/template/eqeqeq': ['error', {allowNullOrUndefined: true}],

      // Every @switch carries a @default arm (or @default never for
      // compiler-checked exhaustiveness)
      '@angular-eslint/template/require-switch-default': 'error',

      // [outerHTML] bindings bypass Angular's sanitizer-aware innerHTML path
      '@angular-eslint/template/no-outerhtml': 'error',

      // ===========================================
      // Accessibility Rules
      // ===========================================

      // Require alt text for images
      '@angular-eslint/template/alt-text': 'error',

      // Click events should have key events
      '@angular-eslint/template/click-events-have-key-events': 'warn',

      // Interactive elements should be focusable
      '@angular-eslint/template/interactive-supports-focus': 'warn',

      // Labels should have associated control
      '@angular-eslint/template/label-has-associated-control': 'warn',

      // Avoid autofocus
      '@angular-eslint/template/no-autofocus': 'warn',

      // Require ARIA roles have required attributes
      '@angular-eslint/template/role-has-required-aria': 'error',

      // Require valid scope on table elements
      '@angular-eslint/template/table-scope': 'error',

      // Require valid ARIA attributes
      '@angular-eslint/template/valid-aria': 'error',

      // Accessible elements with event handlers
      '@angular-eslint/template/elements-content': 'warn',

      // No positive tabindex
      '@angular-eslint/template/no-positive-tabindex': 'error',
    },
  },
);
