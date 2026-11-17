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

/** Translates raw `pm install` failures into actionable guidance. */

/** A remediation the UI can offer as a one-click action. */
type InstallRemedy =
  | {kind: 'retry-downgrade'; label: string}
  | {kind: 'uninstall-retry'; label: string}
  | {kind: 'retry-test'; label: string}
  | {kind: 'retry-all-splits'; label: string};

export interface ClassifiedInstallError {
  /** Plain-language explanation. */
  message: string;
  /** The raw `Failure [...]` code, when present. */
  code?: string;
  remedy?: InstallRemedy;
}

const FAILURE_CODE = /Failure \[([A-Z0-9_]+)/;

export function classifyInstallError(error: unknown): ClassifiedInstallError {
  const raw = error instanceof Error ? error.message : String(error);
  const code = FAILURE_CODE.exec(raw)?.[1];

  switch (code) {
    case 'INSTALL_FAILED_VERSION_DOWNGRADE':
      return {
        code,
        message: 'A newer version is already installed.',
        remedy: {kind: 'retry-downgrade', label: 'Install anyway (downgrade)'},
      };
    case 'INSTALL_FAILED_UPDATE_INCOMPATIBLE':
      return {
        code,
        message:
          'The installed app was signed with a different key (e.g. release vs debug). It must be uninstalled first.',
        remedy: {kind: 'uninstall-retry', label: 'Uninstall & reinstall'},
      };
    case 'INSTALL_FAILED_TEST_ONLY':
      return {
        code,
        message: 'This APK is marked test-only.',
        remedy: {kind: 'retry-test', label: 'Install as test app'},
      };
    case 'INSTALL_FAILED_MISSING_SPLIT':
      return {
        code,
        message: 'The device needs splits that were filtered out for its ABI/density.',
        remedy: {kind: 'retry-all-splits', label: 'Install all splits'},
      };
    case 'INSTALL_FAILED_INSUFFICIENT_STORAGE':
      return {code, message: 'Not enough free storage on the device.'};
    case 'INSTALL_FAILED_OLDER_SDK':
      return {
        code,
        message: "The app's minSdkVersion is higher than the device's Android version.",
      };
    case 'INSTALL_FAILED_NO_MATCHING_ABIS':
      return {code, message: "The APK has no native libraries for the device's CPU architecture."};
    case 'INSTALL_PARSE_FAILED_NO_CERTIFICATES':
      return {code, message: 'The APK is not signed.'};
    default:
      return {code, message: code ? `Install failed: ${code}` : raw || 'Installation failed'};
  }
}
