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

# Review post-hook for geminiHookProfile=aaos (AAOS builder gemini-review step).
# Expects REPO_ROOT and GEMINI_ANALYSIS_PATH; cwd is typically GEMINI_ANALYSIS_PATH.

set +e

cd "${GEMINI_ANALYSIS_PATH%/}" || true
repo forall -c 'git checkout -- .; git clean -xfd;' >/dev/null 2>&1 || true
rm -f "${GEMINI_ANALYSIS_PATH%/}"/aaos-build*.* 2>/dev/null || true
rm -f "${GEMINI_ANALYSIS_PATH%/}/aaos-build.log.tail" 2>/dev/null || true
cd "${REPO_ROOT}" || true
