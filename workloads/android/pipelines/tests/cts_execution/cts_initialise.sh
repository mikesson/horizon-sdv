#!/usr/bin/env bash

# Copyright (c) 2024-2026 Accenture, All Rights Reserved.
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

# Description:
# Initialise Android CTS on the Cuttlefish host. CF bake installs /opt/android-cts_<ver>/android-cts
# (see cf_host_initialise.sh). This script symlinks CTS_ROOT (/opt/android-cts) to /opt/android-cts_<ver>
# so Tradefed sees ${CTS_ROOT}/android-cts/{tools,...} (same layout as AOSP prebuilts / Jenkins).

# Include common functions and variables.
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")"/cts_environment.sh "$0"

# If a CTS zip was unpacked flat (${CTS_ROOT}/tools), nest under android-cts/ for Tradefed.
_cts_nest_flat_bundle_if_needed() {
    local root="$1"
    [[ -d "${root}" ]] || return 0
    # Never rearrange a symlinked CTS_ROOT (baked image); download installs a real directory tree.
    [[ -L "${root}" ]] && return 0
    if [[ -d "${root}/android-cts/tools" ]] || [[ ! -d "${root}/tools" ]]; then
        return 0
    fi
    local staging
    staging="$(mktemp -d "${root}/.cts-nest-staging.XXXXXX")" || return 1
    shopt -s nullglob
    local f
    for f in "${root}"/*; do
        case "${f}" in
            */android-cts) continue ;;
            */.cts-nest-staging.*) continue ;;
        esac
        mv "${f}" "${staging}/"
    done
    shopt -u nullglob
    mkdir -p "${root}/android-cts"
    shopt -s nullglob
    for f in "${staging}"/*; do
        mv "${f}" "${root}/android-cts/"
    done
    shopt -u nullglob
    rmdir "${staging}" 2>/dev/null || true
}

_cts_opt_parent() {
    dirname "${CTS_ROOT}"
}

_cts_ensure_parent_writable() {
    local parent
    parent="$(_cts_opt_parent)"
    if mkdir -p "${CTS_ROOT}" 2>/dev/null && [[ -w "${CTS_ROOT}" || -w "${parent}" ]]; then
        return 0
    fi
    if command -v sudo >/dev/null 2>&1; then
        sudo mkdir -p "${parent}" 2>/dev/null || true
        sudo chown "$(id -u):$(id -g)" "${parent}" 2>/dev/null || true
    fi
    [[ -w "${parent}" ]]
}

_cts_link_versioned_opt() {
    local _opt_root="/opt/android-cts_${ANDROID_VERSION}"
    local _tree="${_opt_root}/android-cts"
    if [[ ! -d "${_tree}/tools" ]]; then
        echo -e "${RED}[cts] ERROR: Baked CTS is missing for ANDROID_VERSION=${ANDROID_VERSION} (expected ${_tree}/tools).${NC}" >&2
        echo -e "${ORANGE}[cts]       The Cuttlefish instance image does not contain ${_opt_root} from the template bake.${NC}" >&2
        echo -e "${ORANGE}[cts]       Remediation:${NC}" >&2
        echo -e "${ORANGE}[cts]         - Ensure CTS is included in CF instance template builds (set CTS_ANDROID_${ANDROID_VERSION}_URL in the template bake / Packer pipeline).${NC}" >&2
        echo -e "${ORANGE}[cts]         - Rebuild CF instance templates after changing where Android CTS is installed (e.g. under /opt).${NC}" >&2
        echo -e "${ORANGE}[cts]         - Or set CTS_DOWNLOAD_URL to install CTS at runtime instead of symlinking the baked tree.${NC}" >&2
        exit 1
    fi
    echo -e "${GREEN}[cts] Symlink ${CTS_ROOT} -> ${_opt_root} (ANDROID_VERSION=${ANDROID_VERSION}; Tradefed uses ${CTS_ROOT}/android-cts/tools).${NC}"
    if command -v sudo >/dev/null 2>&1; then
        sudo mkdir -p "$(_cts_opt_parent)" 2>/dev/null || true
        sudo rm -rf "${CTS_ROOT}" 2>/dev/null || true
        sudo ln -sfn "${_opt_root}" "${CTS_ROOT}"
        sudo mkdir -p "${CTS_ROOT}/android-cts/results"
        sudo chmod 1777 "${CTS_ROOT}/android-cts/results" 2>/dev/null || true
    else
        mkdir -p "$(_cts_opt_parent)" 2>/dev/null || true
        rm -rf "${CTS_ROOT}" 2>/dev/null || true
        ln -sfn "${_opt_root}" "${CTS_ROOT}"
        mkdir -p "${CTS_ROOT}/android-cts/results"
        chmod 1777 "${CTS_ROOT}/android-cts/results" 2>/dev/null || true
    fi
}

function cts_initialise() {
    if [ -n "${CTS_DOWNLOAD_URL}" ]; then
        echo -e "${GREEN}[cts] Installing Android CTS from ${CTS_DOWNLOAD_URL} under ${CTS_ROOT}${NC}"
        _cts_ensure_parent_writable || {
            echo -e "${RED}[cts] ERROR: cannot write $(_cts_opt_parent) for CTS install.${NC}" >&2
            exit 1
        }
        local parent zen
        parent="$(_cts_opt_parent)"
        zen="$(basename "${CTS_ROOT}")"
        cd "${parent}" || exit 1
        if command -v sudo >/dev/null 2>&1; then
            sudo rm -rf "${zen}" 2>/dev/null || true
        else
            rm -rf "${zen}" 2>/dev/null || true
        fi
        case "${CTS_DOWNLOAD_URL}" in
            gs://*)
                gcloud storage cp "${CTS_DOWNLOAD_URL}" android-cts-download.zip
                ;;
            *)
                wget -nv "${CTS_DOWNLOAD_URL}" -O android-cts-download.zip > /dev/null 2>&1
                ;;
        esac
        unzip -q android-cts-download.zip > /dev/null 2>&1
        rm -f android-cts-download.zip
        if [[ -d "${parent}/android-cts" ]] && [[ "${zen}" != "android-cts" ]]; then
            mv "${parent}/android-cts" "${zen}"
        fi
        _cts_nest_flat_bundle_if_needed "${CTS_ROOT}"
        local _tfh
        _tfh="$(_cts_tradefed_home)"
        if [[ ! -d "${_tfh}/tools" ]]; then
            echo -e "${RED}[cts] ERROR: archive did not produce ${_tfh}/tools (CTS_ROOT=${CTS_ROOT}).${NC}" >&2
            exit 1
        fi
        if command -v sudo >/dev/null 2>&1; then
            sudo chown -R "$(id -u):$(id -g)" "${CTS_ROOT}" 2>/dev/null || true
        fi
        chmod -R a+rX "${CTS_ROOT}" 2>/dev/null || true
        find "${_tfh}/tools" -type f \( -name '*.sh' -o -name 'cts-tradefed' \) -exec chmod a+x {} \; 2>/dev/null || true
        [[ -d "${_tfh}/jdk/bin" ]] && chmod a+x "${_tfh}"/jdk/bin/* 2>/dev/null || true
        mkdir -p "${_tfh}/results"
        chmod 1777 "${_tfh}/results" 2>/dev/null || sudo chmod 1777 "${_tfh}/results" 2>/dev/null || true
        echo -e "${GREEN}[cts] Installed from ${CTS_DOWNLOAD_URL}.${NC}"
    else
        _cts_link_versioned_opt
    fi

    local _tfh
    _tfh="$(_cts_tradefed_home)"
    if [[ -f "${HOME}/.bashrc" ]] && ! grep -qF "${_tfh}/jdk/bin" "${HOME}/.bashrc" 2>/dev/null; then
        echo "export PATH=${PATH}:${_tfh}/jdk/bin" >> "${HOME}/.bashrc"
    fi

    echo "Java file type:"
    file "${_tfh}/jdk/bin/java" || true
}

cts_initialise
