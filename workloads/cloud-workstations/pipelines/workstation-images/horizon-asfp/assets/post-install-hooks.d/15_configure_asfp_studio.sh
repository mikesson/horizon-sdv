#!/bin/bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

# shellcheck source=/dev/null
source /google/scripts/common.sh

# Mirror horizon-android-studio configure_studio_settings: skip first-run /
# import / trust prompts so autostart reaches the IDE instead of license
# wizards. (SDK license acceptance via sdkmanager lives in the Android Studio
# image; ASfP does not install cmdline-tools here.)

readonly VMOPTIONS="/opt/android-studio-for-platform-canary/bin/studio64.vmoptions"
readonly IDEA_PROPERTIES="/opt/android-studio-for-platform-canary/bin/idea.properties"

append_unique() {
  local file="${1}"
  local line="${2}"
  if [[ -f "${file}" ]] && ! grep -qxF "${line}" "${file}"; then
    echo "${line}" >> "${file}"
  fi
}

configure_asfp_studio() {
  if [[ ! -f "${VMOPTIONS}" ]]; then
    warn "${VMOPTIONS} not found; skipping ASfP first-run configuration"
    return 0
  fi

  log "Configuring ASfP to skip first-run / license import prompts..."
  append_unique "${VMOPTIONS}" "-Ddisable.android.first.run=true"
  append_unique "${VMOPTIONS}" "-Ddisable.config.import=true"
  append_unique "${VMOPTIONS}" "-Didea.trust.all.projects=true"
  # JetBrains launcher often ignores JAVA_TOOL_OPTIONS; without this AWT
  # picks WLGraphicsEnvironment on the headless Wayland session and fails.
  append_unique "${VMOPTIONS}" "-Dawt.toolkit.name=XToolkit"

  if [[ -f "${IDEA_PROPERTIES}" ]]; then
    append_unique "${IDEA_PROPERTIES}" "studiobot.chat.use.compose.for.ui=false"
  fi

  log_event BUILD_HOOK_COMPLETED "ASfP first-run configuration applied"
}

main() {
  configure_asfp_studio
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
