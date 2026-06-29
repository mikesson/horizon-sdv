#!/usr/bin/env bash

# Copyright (c) 2026 Accenture, All Rights Reserved.
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

# Review pre-hook for geminiHookProfile=aaos (AAOS builder gemini-review step).
# Expects REPO_ROOT and GEMINI_ANALYSIS_PATH in the environment; run from the
# gemini-review step after cwd is REPO_ROOT.
# GEMINI_AAOS_LOG_TAIL_MODE: "tail" (default, last 2500 lines) or "fullcopy"
# (copy entire aaos-build.log to aaos-build.log.tail — used by ABFS Jenkins).

set +e

echo "Analysis path: ${GEMINI_ANALYSIS_PATH}"
cd "${REPO_ROOT}" || true
cp -f aaos-build*.* "${GEMINI_ANALYSIS_PATH%/}/" 2>/dev/null || true
cd "${GEMINI_ANALYSIS_PATH%/}" || true
if [[ -f aaos-build.log ]]; then
  if [[ "${GEMINI_AAOS_LOG_TAIL_MODE:-tail}" == "fullcopy" ]]; then
    cp -f aaos-build.log aaos-build.log.tail 2>/dev/null || true
  else
    tail -n 2500 aaos-build.log > aaos-build.log.tail 2>/dev/null || true
  fi
fi
