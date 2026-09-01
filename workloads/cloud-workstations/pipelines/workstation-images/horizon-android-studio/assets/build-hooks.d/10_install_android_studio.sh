#!/bin/bash

# Copyright (c) 2025-2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#         http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

# Installs Android Studio, the Android command-line tools, and the pinned
# Android SDK package inventory into /opt/google. The runtime overlay hook
# (etc/workstation-startup.d/210_mount-android-sdk.sh) later surfaces
# /opt/google/Android/Sdk at $ANDROID_HOME, merged with any user-installed
# packages.

# shellcheck source=/dev/null
source /google/scripts/common.sh

readonly ANDROID_STUDIO_VERSION="${ANDROID_STUDIO_VERSION:?ANDROID_STUDIO_VERSION must be set}"
readonly ANDROID_STUDIO_SHA256="${ANDROID_STUDIO_SHA256:?ANDROID_STUDIO_SHA256 must be set}"
readonly COMMAND_LINE_TOOLS_VERSION="${COMMAND_LINE_TOOLS_VERSION:?COMMAND_LINE_TOOLS_VERSION must be set}"
readonly CURL_CMD="curl ${CURL_OPTS:--fsSL --retry 3 --connect-timeout 10 --max-time 300}"

command -v sha256sum >/dev/null 2>&1 || error "sha256sum is required"

# Pinned SDK package inventory. Keep in sync with the Dockerfile's build-time
# assertions; do not add a blanket "sdkmanager --update", which would
# introduce unreviewed version drift beyond this declared set.
readonly SDK_PACKAGES=(
  "build-tools;35.0.0"
  "build-tools;35.0.0-rc1"
  "emulator"
  "platforms;android-34"
  "platforms;android-35"
  "platform-tools"
  "sources;android-35"
  "system-images;android-34;google_apis;x86_64"
  "system-images;android-34;google_apis_playstore;x86_64"
)

# Fails the hook when the downloaded artifact does not match the pinned digest.
verify_sha256() {
  local file="${1}" expected="${2}" label="${3}"
  expected="$(echo "${expected}" | sed 's/^sha256://I' | tr 'A-F' 'a-f')"

  local actual
  actual="$(sha256sum "${file}" | awk '{print $1}')"
  if [[ "${actual}" != "${expected}" ]]; then
    error "${label} SHA256 mismatch: expected ${expected}, got ${actual}"
  fi
  echo "  -> ${label} SHA256 verified"
}

install_android_studio() {
  echo "Installing Android Studio ${ANDROID_STUDIO_VERSION}..."
  mkdir -p /opt/google

  local tarball
  tarball=$(mktemp /tmp/android-studio.XXXXXX.tar.gz)
  local download_url="https://redirector.gvt1.com/edgedl/android/studio/ide-zips/${ANDROID_STUDIO_VERSION}/android-studio-${ANDROID_STUDIO_VERSION}-linux.tar.gz"
  ${CURL_CMD} "${download_url}" -o "${tarball}"
  verify_sha256 "${tarball}" "${ANDROID_STUDIO_SHA256}" "Android Studio ${ANDROID_STUDIO_VERSION}"
  tar -xzf "${tarball}" -C /opt/google/
  rm -f "${tarball}"

  if [[ ! -x /opt/google/android-studio/bin/studio.sh ]]; then
    error "Android Studio launcher missing after extraction: /opt/google/android-studio/bin/studio.sh"
  fi
}

install_command_line_tools() {
  echo "Installing Android command-line tools ${COMMAND_LINE_TOOLS_VERSION}..."

  local zip_file
  zip_file=$(mktemp /tmp/commandlinetools.XXXXXX.zip)
  local download_url="https://dl.google.com/android/repository/commandlinetools-linux-${COMMAND_LINE_TOOLS_VERSION}_latest.zip"
  ${CURL_CMD} "${download_url}" -o "${zip_file}"
  unzip -q "${zip_file}" -d /opt/google/
  rm -f "${zip_file}"

  if [[ ! -x /opt/google/cmdline-tools/bin/sdkmanager ]]; then
    error "sdkmanager missing after extraction: /opt/google/cmdline-tools/bin/sdkmanager"
  fi

  # Folder that houses the "default" Android packages bundled with this
  # image. At startup, an overlay mount surfaces both these SDKs and any
  # SDKs the user has subsequently installed at $ANDROID_HOME.
  mkdir -p /opt/google/Android/Sdk
  chmod o+wr /opt/google/Android/Sdk
}

# Runs sdkmanager with non-interactive license acceptance via `yes`.
# Under `set -o pipefail`, `yes` exits 141 (SIGPIPE) when sdkmanager closes
# stdin after a successful install; that must not fail the hook. Fail only
# when sdkmanager itself returns non-zero (PIPESTATUS[1]).
run_sdkmanager() {
  local -a args=("$@")
  set +o pipefail
  yes | /opt/google/cmdline-tools/bin/sdkmanager \
    --sdk_root=/opt/google/Android/Sdk "${args[@]}"
  local statuses=("${PIPESTATUS[@]}")
  set -o pipefail

  local sdk_status="${statuses[1]}"
  if [[ "${sdk_status}" -ne 0 ]]; then
    error "sdkmanager ${args[*]} failed with exit ${sdk_status}"
  fi
}

install_sdk_packages() {
  echo "Installing Android SDK packages: ${SDK_PACKAGES[*]}"

  # Use Android Studio's bundled JBR explicitly. The base image is not
  # guaranteed to ship a JDK, and pinning to the Studio-bundled runtime keeps
  # sdkmanager's Java version consistent with the IDE it serves.
  local jbr_home="/opt/google/android-studio/jbr"
  if [[ ! -x "${jbr_home}/bin/java" ]]; then
    error "Android Studio bundled JBR not found: ${jbr_home}/bin/java"
  fi
  export JAVA_HOME="${jbr_home}"

  # Accept licenses once up front so install prompts do not block.
  run_sdkmanager --licenses >/dev/null

  local package
  for package in "${SDK_PACKAGES[@]}"; do
    echo "  -> Installing ${package}"
    run_sdkmanager --install "${package}"
  done

  # Grant the workstation user permissions to the bundled SDK.
  chown -R "${WORKSTATION_UID}:users" /opt/google/Android/Sdk

  if [[ ! -x /opt/google/Android/Sdk/emulator/emulator ]]; then
    error "emulator binary missing after sdkmanager install: /opt/google/Android/Sdk/emulator/emulator"
  fi
}

configure_studio_settings() {
  echo "Configuring Android Studio VM options and Gemini settings..."

  local vmoptions=/opt/google/android-studio/bin/studio64.vmoptions
  local idea_properties=/opt/google/android-studio/bin/idea.properties

  check_file "${vmoptions}"
  check_file "${idea_properties}"

  {
    echo "-Ddisable.android.first.run=true"
    echo "-Ddisable.config.import=true"
    echo "-Didea.trust.all.projects=true"
    echo "-Duse.gemini.enterprise=true"
  } >> "${vmoptions}"

  echo "studiobot.chat.use.compose.for.ui=false" >> "${idea_properties}"
}

main() {
  install_android_studio
  install_command_line_tools
  install_sdk_packages
  configure_studio_settings
  log "Android Studio ${ANDROID_STUDIO_VERSION} and Android SDK installation complete."
}

main "$@"
