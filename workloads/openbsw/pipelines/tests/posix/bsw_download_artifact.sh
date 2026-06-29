#!/usr/bin/env bash

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

# Description
# Download the OpenBSW POSIX application ready for test.

# Include common functions and variables.
# shellcheck disable=SC1091
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/bsw_environment.sh" "$0"

# Ensure pyTest can drive posix-rust builds until upstream ships [rust.target_process] in target_posix.toml.
function bsw_merge_posix_rust_target() {
    local toml="${HOME}/posix/test/pyTest/target_posix.toml"
    local frag="${SCRIPT_DIR}/target_posix_rust.fragment.toml"
    if [[ ! -f "${toml}" ]] || [[ ! -f "${frag}" ]]; then
        echo "WARNING: skip Rust pyTest merge (missing ${toml} or ${frag})"
        return 0
    fi
    if grep -q '^\[rust\.target_process\]' "${toml}"; then
        echo "target_posix.toml already defines [rust.target_process]"
        return 0
    fi
    echo "Appending Horizon Rust target fragment to target_posix.toml"
    printf '\n' >> "${toml}"
    cat "${frag}" >> "${toml}"
}

# Download OpenBSW artifacts
function bsw_download_artifacts() {

    case "${OPENBSW_DOWNLOAD_URL}" in
        gs://*)
            echo "Copying artifacts from ${OPENBSW_DOWNLOAD_URL}"
            gcloud storage cp -r "${OPENBSW_DOWNLOAD_URL}" "${HOME}" || true
            ;;
        *)
            echo "WARNING: only GCS bucket access is supported"
            ;;
    esac

    # Unpack POSIX application, tools and pyTest artifacts
    cd "${HOME}"/posix || exit
    tar -zxf posix.tgz
    rm -rf posix.tgz
    bsw_merge_posix_rust_target
    cd - || exit
}

bsw_download_artifacts
