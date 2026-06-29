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

# Description:
# This script automates the build process for the OpenBSW project, supporting
# multiple targets and unit testing workflows. It sources the environment
# configuration, defines functions for building and testing, and executes
# steps based on the parameters/environment variables.
#
# Features:
# - Builds POSIX and NXP S32K148 targets.
# - Builds, lists, and runs unit tests.
# - Supports post-build command execution.
#
# Usage:
#   This script is intended to be invoked as part of a CI/CD pipeline or
#   manually to perform builds and tests for OpenBSW.
#
# Remember where this script lives before sourcing bsw_environment.sh. That file switches the shell into ${HOME}/bsw-builds;
# if we waited until after that to resolve paths like ./workloads/.../bsw_build.sh, those paths would no longer exist from there.
# shellcheck disable=SC1091
BSW_BUILD_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${BSW_BUILD_SCRIPT_DIR}/bsw_environment.sh" "$0"

# Upstream exposes only s32k148-rust-gcc (no s32k148-rust-clang); normalize when RTOS_PLATFORM=rust.
if [[ "${RTOS_PLATFORM:-}" == "rust" ]] && [[ "${BUILD_NXP_S32K148:-false}" == "true" ]]; then
    NXP_S32K148_BUILD_CMDLINE="cmake --preset s32k148-rust-gcc && cmake --build --preset s32k148-rust-gcc -j${CMAKE_SYNC_JOBS}"
    NXP_S32K148_ARTIFACT=${NXP_S32K148_ARTIFACT:-build/s32k148-rust-gcc/executables/referenceApp/application/RelWithDebInfo/app.referenceApp.elf}
    export NXP_CC="/opt/arm-gnu-toolchain/bin/arm-none-eabi-gcc"
    export NXP_CXX="/opt/arm-gnu-toolchain/bin/arm-none-eabi-g++"
fi

# Function to build the POSIX target
function build_posix_target() {
    echo "Building POSIX target"
    eval "${POSIX_BUILD_CMDLINE}" | tee -a "${BUILD_LOG_FILE}"
    if [ "${PIPESTATUS[0]}" -ne 0 ]; then
        echo "ERROR: ${POSIX_BUILD_CMDLINE} failed"
        exit 1
    fi
}

# Append default [rust.target_process] when fragment files are missing (sparse checkout, partial workspace).
# Keep in sync with workloads/openbsw/pipelines/tests/posix/target_posix_rust.fragment.toml (functional lines only).
function _append_embedded_rust_target_fragment() {
    local toml="$1"
    cat >> "${toml}" <<'RUST_TARGET_FRAGMENT_EOF'

[rust.target_process]
command_line = "../../build/posix-rust/executables/referenceApp/application/Release/app.referenceApp.elf < /tmp/pty_forwarder > /tmp/pty_forwarder"
restart_if_exited = true
kill_at_end = true
RUST_TARGET_FRAGMENT_EOF
}

# Ensure Rust is listed in the POSIX test config (upstream often omits it) so --app=rust knows which binary to start.
# Merge when RTOS_PLATFORM=rust OR the pytest command explicitly uses --app=rust (Jenkins does not always export RTOS_PLATFORM into the shell).
function merge_posix_rust_pytest_target_toml() {
    if [[ "${RTOS_PLATFORM:-}" != "rust" ]] && [[ "${POSIX_PYTEST_CMDLINE:-}" != *"--app=rust"* ]]; then
        return 0
    fi
    local frag="" frag_ws="" frag_script=""
    if [[ -n "${ORIG_WORKSPACE:-}" ]]; then
        frag_ws="${ORIG_WORKSPACE}/workloads/openbsw/pipelines/tests/posix/target_posix_rust.fragment.toml"
    else
        frag_ws="(ORIG_WORKSPACE unset)"
    fi
    frag_script="${BSW_BUILD_SCRIPT_DIR}/../../tests/posix/target_posix_rust.fragment.toml"
    if [[ -f "${frag_ws}" ]]; then
        frag="${frag_ws}"
    elif [[ -f "${frag_script}" ]]; then
        frag="${frag_script}"
    fi

    local toml="test/pyTest/target_posix.toml"
    if [[ ! -f "${toml}" ]]; then
        echo "WARNING: skip Rust pyTest TOML merge: missing OpenBSW file ${PWD}/${toml} (OPENBSW_GIT_DIR=${OPENBSW_GIT_DIR:-})"
        return 0
    fi
    if grep -q '^\[rust\.target_process\]' "${toml}"; then
        return 0
    fi

    echo "Appending Horizon Rust target stanza to ${toml}"
    printf '\n' >> "${toml}"
    if [[ -n "${frag}" ]]; then
        cat "${frag}" >> "${toml}"
    else
        echo "NOTE: target_posix_rust.fragment.toml not found (tried ${frag_ws} and ${frag_script}); using embedded stanza"
        _append_embedded_rust_target_fragment "${toml}"
    fi
}

# Function to run pytest for POSIX target
function run_pytest_posix_target() {
    echo "Running POSIX pytest"
    eval "${POSIX_PYTEST_CMDLINE}" | tee -a "${PYTEST_RESULTS_FILE}"
    local pytest_st="${PIPESTATUS[0]}"
    # Failed runs exit before post-build copy; still copy the test log to the Jenkins workspace so people (and AI Review) can open it there.
    if [[ -n "${ORIG_WORKSPACE:-}" ]] && [[ -f "${PYTEST_RESULTS_FILE}" ]]; then
        cp -f "${PYTEST_RESULTS_FILE}" "${ORIG_WORKSPACE}/" || true
    fi
    if [ "${pytest_st}" -ne 0 ]; then
        echo "ERROR: ${POSIX_PYTEST_CMDLINE} failed"
        exit 1
    fi
}

# Function to build unit tests
function build_unit_tests() {
    echo "Building unit tests"
    eval "${UNIT_TESTS_CMDLINE}" | tee -a "${BUILD_LOG_FILE}"
    if [ "${PIPESTATUS[0]}" -ne 0 ]; then
        echo "ERROR: ${UNIT_TESTS_CMDLINE} failed"
        exit 1
    fi
}

# Function to list unit tests
function list_unit_tests() {
    echo "List unit tests"
    eval "${LIST_UNIT_TESTS_CMDLINE}" | tee -a "${UNIT_TESTS_LIST_FILE}"
    if [ "${PIPESTATUS[0]}" -ne 0 ]; then
        echo "ERROR: ${LIST_UNIT_TESTS_CMDLINE} failed"
        exit 1
    fi
}

# Function to generate documentation
function build_documentation() {
    echo "Building Documentation"
    eval "${BUILD_DOCUMENTATION_CMDLINE}" | tee -a "${BUILD_LOG_FILE}"
    if [ "${PIPESTATUS[0]}" -ne 0 ]; then
        echo "ERROR: ${BUILD_DOCUMENTATION_CMDLINE} failed"
        exit 1
    fi
}

# Function to run unit tests
function run_unit_tests() {
    echo "Running unit tests"
    eval "${RUN_UNIT_TESTS_CMDLINE}" | tee -a "${UNIT_TESTS_RESULTS_FILE}"
    if [ "${PIPESTATUS[0]}" -ne 0 ]; then
        echo "ERROR: ${RUN_UNIT_TESTS_CMDLINE} failed"
        exit 1
    fi
}

# Function to build the NXP S32K148 target
function build_nxp_target() {
    echo "Building NXP S32K148 target"

    # Override CC/CXX
    export CC="${NXP_CC}"
    export CXX="${NXP_CXX}"
    eval "${NXP_S32K148_BUILD_CMDLINE}" | tee -a "${BUILD_LOG_FILE}"
    if [ "${PIPESTATUS[0]}" -ne 0 ]; then
        echo "ERROR: ${NXP_S32K148_BUILD_CMDLINE} failed"
        exit 1
    fi
}

# Change directory to the root of the OpenBSW repository
cd "${OPENBSW_GIT_DIR}" || exit

# List available tests.
if ${LIST_UNIT_TESTS}; then
    list_unit_tests
fi

# Create documentation
if ${BUILD_DOCUMENTATION}; then
    build_documentation
fi

# Build and run unit tests if enabled
if ${BUILD_UNIT_TESTS}; then
    build_unit_tests
fi

# Run unit tests (assumes Build was run)
if ${RUN_UNIT_TESTS}; then
    run_unit_tests
fi

# Build the POSIX target if enabled
if ${BUILD_POSIX}; then
    build_posix_target
    # Merge [rust.target_process] into target_posix.toml for RTOS_PLATFORM=rust so
    # posix.tgz (post-build tar includes test/pyTest) supports pytest --app=rust even
    # when POSIX_PYTEST is disabled in CI.
    merge_posix_rust_pytest_target_toml
fi

# Run POSIX pytest if enabled (merge again here: Jenkins runs this in a separate stage with BUILD_POSIX=false)
if ${POSIX_PYTEST}; then
    merge_posix_rust_pytest_target_toml
    run_pytest_posix_target
fi

# Build the NXP S32K148 target if enabled
if ${BUILD_NXP_S32K148}; then
    build_nxp_target
fi

# Execute post build commands if any
if [ "${#POST_BUILD_COMMANDS[@]}" -gt 0 ]; then
    echo "Post build commands:"
    for command in "${POST_BUILD_COMMANDS[@]}"; do
        echo "${command}"
        eval "${command}"
    done
fi

exit 0
