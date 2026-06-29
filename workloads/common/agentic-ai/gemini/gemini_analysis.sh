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

# Description:
#   Runs Gemini CLI analysis (single or sequenced). Invoke after gemini_initialise.sh.
#   One prompt file → single run. Two or three prompt files → sequenced (context
#   chaining; order matters): each step is a separate gemini-cli invocation with its
#   own JSON output file (headless_output_stepN_<timestamp>_<random>.json). Step 2
#   appends prior step output; step 3 appends both step1_output.md and step2_output.md
#   when those files exist (optional byte caps via GEMINI_STEP3_PRIOR_*_BYTES).
#   Use GEMINI_PROMPT_FILE + GEMINI_PROMPT_FILE_2 (and optionally _3) for sequenced.
#
# Default prompts (when GEMINI_PROMPT_FILE* are unset): AAOS builder step1 → step2 → step3.
#
# Output:
#   - headless_output_step*.json  Raw Gemini API responses (one file per sequenced step; single-step uses step1).
#   - step*_output.md        Extracted markdown per step (sequenced mode).
#   - gemini-assist/         Proposed fixes and step outputs; archived by Jenkins.
#     Stray remediation *.md in the run cwd matching *proposed_fix*.md are moved here after each
#     successful step when not already under GEMINI_ARTIFACT_PATH.
#
# Requires: jq (extract text from JSON). Environment: gemini_environment.sh.
#
# Sequenced runs (optional env):
#   GEMINI_STEP2_PRIOR_CONTEXT_BYTES — max bytes of step1 appended into step2 composed prompt (unset/0 = full).
#   GEMINI_STEP3_PRIOR_STEP1_BYTES — max bytes of step1 in step3 composed prompt (unset/0 = full).
#   GEMINI_STEP3_PRIOR_STEP2_BYTES — max bytes of step2 in step3 composed prompt (unset/0 = full).
#
set -o pipefail

SCRIPT_DIR="$(dirname "${BASH_SOURCE[0]}")"
source "${SCRIPT_DIR}"/gemini_environment.sh "$0"
cd "${GEMINI_ARTIFACT_WRITE_ROOT}" || exit 1

# --- Default prompts when GEMINI_PROMPT_FILE* are unset (AAOS builder sequenced) ---
REPO_ROOT="${REPO_ROOT:-/workspace}"
DEFAULT_PROMPT_DIR="${REPO_ROOT}/workloads/android/pipelines/builds/aaos_builder/prompt/sequenced"
DEFAULT_P1="${DEFAULT_PROMPT_DIR}/step1_triage.txt"
DEFAULT_P2="${DEFAULT_PROMPT_DIR}/step2_rca.txt"
DEFAULT_P3="${DEFAULT_PROMPT_DIR}/step3_fixes.txt"

RESULT=0

# Normalize prompt env: if value is a readable file path, replace with base64 content
# (so Jenkins can pass prompt content via parameters). In-place update of the named var.
normalize_prompt_var() {
    local var_name="$1"
    local val="${!var_name:-}"
    [ -z "$val" ] && return 0
    if [ -f "$val" ] && [ -r "$val" ]; then
        printf -v "$var_name" '%s' "$(base64 < "$val" | tr -d '\n')"
    fi
}

# Resolve GEMINI_PROMPT_FILE* value to a path: if spec is a path, use it; else decode
# base64 into out_name and return that path. Empty spec → empty string.
resolve_prompt_to_file() {
    local spec="$1"
    local out_name="$2"
    if [ -z "${spec}" ]; then echo ""; return 0; fi
    if [ -f "${spec}" ]; then echo "${spec}"; return 0; fi
    local decoded="${out_name:-step_prompt.txt}"
    if echo "${spec}" | base64 -d > "${decoded}" 2>/dev/null && [ -s "${decoded}" ]; then
        echo "${decoded}"; return 0
    fi
    rm -f "${decoded}"; echo ""; return 0
}

# Write Gemini response text into out_file from JSON.
# Args: out_file [explicit_json_path]
# If explicit_json_path is set and readable, use it; else pick the newest headless_output*.json by mtime.
# If jq is available, extract text via .response, then .candidates[0].content.parts[0].text,
# then .outputText / .text / .result; if NDJSON/streaming, merge with jq -rs.
# If extraction is empty, overwrite out_file with the last 100000 bytes of the JSON for debugging.
extract_last_output_to() {
    local out_file="$1"
    local explicit_json="${2:-}"
    local latest=""
    if [ -n "${explicit_json}" ] && [ -f "${explicit_json}" ]; then
        latest="${explicit_json}"
    else
        for f in headless_output*.json; do
            [ -f "$f" ] || continue
            if [ -z "$latest" ] || [ "$f" -nt "$latest" ]; then latest="$f"; fi
        done
    fi
    if [ -z "${latest}" ] || [ ! -f "${latest}" ]; then
        echo -e "${RED}No Gemini JSON output found to extract.${NC}"
        echo "(Step output missing)" > "${out_file}"; return 1
    fi
    if command -v jq >/dev/null 2>&1; then
        jq -r '.response // .candidates[0].content.parts[0].text // .outputText // .text // .result // empty' "${latest}" 2>/dev/null | head -c 500000 > "${out_file}"
        [ ! -s "${out_file}" ] && jq -rs '[.[] | .response? // .candidates[0].content.parts[0].text? // .outputText? // .text? // empty] | add // empty' "${latest}" 2>/dev/null | head -c 500000 > "${out_file}"
    fi
    if [ ! -s "${out_file}" ]; then
        echo -e "${ORANGE}Could not extract text with jq; writing raw tail of ${latest}${NC}"
        tail -c 100000 "${latest}" > "${out_file}" || true
    fi
    echo -e "${GREEN}Wrote step output to ${out_file} (from ${latest})${NC}"
    return 0
}

# Run one prompt: cat prompt_file into GEMINI_COMMAND_LINE, tee to out_json. On success,
# move stray markdown artifacts from cwd into GEMINI_ARTIFACT_PATH (gemini-assist):
# - gemini*.md when gemini-assist/ did not exist (legacy fallback)
# - *proposed_fix*.md when written to cwd instead of under dest
# On failure, zip /tmp/gemini-client-error-*.json for debugging. Returns CLI exit status.
run_one_prompt() {
    local prompt_file="$1"
    local out_json="${2:-${GEMINI_OUTPUT_FILE_NAME}}"
    if [ -z "${prompt_file}" ] || [ ! -f "${prompt_file}" ]; then return 1; fi
    echo -e "${GREEN}Starting analysis using ${prompt_file}${NC}"
    cat "${prompt_file}" | eval "${GEMINI_COMMAND_LINE}" | tee "${out_json}"
    RESULT=${PIPESTATUS[1]}
    if [ "${RESULT}" -eq 0 ]; then
        echo -e "${GREEN}Completed analysis using ${prompt_file}${NC}"
        local dest="${GEMINI_ARTIFACT_PATH:-gemini-assist}"
        # If Gemini wrote gemini*.md to cwd but gemini-assist/ doesn't exist, create it and move files
        if [ ! -d "${dest}" ]; then
            if compgen -G "${GEMINI_ARTIFACT_FILES_WILDCARD}" > /dev/null; then
                mkdir -p "${dest}"
                # shellcheck disable=SC2086
                mv ${GEMINI_ARTIFACT_FILES_WILDCARD} "${dest}" 2>/dev/null || true
                echo -e "${ORANGE}Directory ${dest} created and file(s) moved.${NC}"
            fi
        fi
        # Remediation outputs: *proposed_fix*.md in cwd (covers proposed_fix_*, gemini_proposed_fixes_*, etc.).
        mkdir -p "${dest}"
        local stray
        shopt -s nullglob
        for stray in *proposed_fix*.md; do
            [ -f "${stray}" ] || continue
            case "${stray}" in
                */*) continue ;;
            esac
            if [ -e "${dest}/${stray}" ]; then
                echo -e "${ORANGE}Skip move: ${dest}/${stray} already exists; leaving ${stray} in place.${NC}"
                continue
            fi
            if mv "${stray}" "${dest}/" 2>/dev/null; then
                echo -e "${ORANGE}Moved ${stray} to ${dest}/${NC}"
            fi
        done
        shopt -u nullglob
    fi
    # On failure, archive client error JSON for debugging (workspace and GEMINI_ANALYSIS_PATH)
    if [ "${RESULT}" -ne 0 ] && compgen -G "/tmp/gemini-client-error-*.json" > /dev/null; then
        zip -j /tmp/gemini-client-error.zip /tmp/gemini-client-error-*.json 2>/dev/null || true
        cp /tmp/gemini-client-error.zip "${GEMINI_ARTIFACT_WRITE_ROOT}"/gemini-client-error.zip 2>/dev/null || true
        # So Argo storage step can archive it (shared aaos-cache volume)
        if [ -n "${GEMINI_ANALYSIS_PATH:-}" ] && [ -d "${GEMINI_ANALYSIS_PATH}" ]; then
            cp /tmp/gemini-client-error.zip "${GEMINI_ANALYSIS_PATH%/}"/gemini-client-error.zip 2>/dev/null || true
        fi
    fi
    return "${RESULT}"
}

# --- Resolve prompt files: env (or base64 decode) or default AAOS sequenced ---
USE_DEFAULT_PROMPTS=0
if [ -z "${GEMINI_PROMPT_FILE:-}" ] && [ -z "${GEMINI_PROMPT_FILE_2:-}" ] && [ -z "${GEMINI_PROMPT_FILE_3:-}" ]; then
    USE_DEFAULT_PROMPTS=1
fi
normalize_prompt_var GEMINI_PROMPT_FILE
normalize_prompt_var GEMINI_PROMPT_FILE_2
normalize_prompt_var GEMINI_PROMPT_FILE_3

p1_file="$(resolve_prompt_to_file "${GEMINI_PROMPT_FILE}" "step1_prompt.txt")"
[ -z "$p1_file" ] && [ "${USE_DEFAULT_PROMPTS}" -eq 1 ] && p1_file="${DEFAULT_P1}"
p2_file="$(resolve_prompt_to_file "${GEMINI_PROMPT_FILE_2}" "step2_prompt.txt")"
[ -z "$p2_file" ] && [ "${USE_DEFAULT_PROMPTS}" -eq 1 ] && p2_file="${DEFAULT_P2}"
p3_file="$(resolve_prompt_to_file "${GEMINI_PROMPT_FILE_3}" "step3_prompt.txt")"
[ -z "$p3_file" ] && [ "${USE_DEFAULT_PROMPTS}" -eq 1 ] && p3_file="${DEFAULT_P3}"

# Build the list of available prompt steps in order.
prompt_steps=()
[ -n "${p1_file}" ] && [ -f "${p1_file}" ] && prompt_steps+=("1:${p1_file}")
[ -n "${p2_file}" ] && [ -f "${p2_file}" ] && prompt_steps+=("2:${p2_file}")
[ -n "${p3_file}" ] && [ -f "${p3_file}" ] && prompt_steps+=("3:${p3_file}")

if [ "${#prompt_steps[@]}" -eq 0 ]; then
    echo -e "${RED}No valid prompt file found. Set GEMINI_PROMPT_FILE* or ensure defaults exist.${NC}"
    exit 1
fi

if [ "${#prompt_steps[@]}" -eq 1 ]; then
    # Single prompt: one run, no step chaining
    first_entry="${prompt_steps[0]}"
    first_prompt="${first_entry#*:}"
    step_start=${SECONDS}
    _ts="$(date +%Y%m%d_%H%M%S)_${RANDOM}"
    step_json="headless_output_step1_${_ts}.json"
    export GEMINI_OUTPUT_FILE_NAME="${step_json}"
    run_one_prompt "${first_prompt}" "${step_json}" || RESULT=$?
    extract_last_output_to "step1_output.md" "${step_json}" || true
    echo -e "${GREEN}Step 1 took $((SECONDS - step_start))s${NC}"
else
    # Sequenced: 2 or 3 steps; each step gets previous step output appended as context
    prev_output=""
    generated_outputs=()

    for entry in "${prompt_steps[@]}"; do
        step_num="${entry%%:*}"
        prompt_path="${entry#*:}"
        run_input="${prompt_path}"

        # Step 3: include both step1 and step2 markdown so the fix step sees full prior chain (with optional byte caps).
        if [ "${step_num}" = "3" ] && [ -f step1_output.md ] && [ -f step2_output.md ]; then
            composed="step${step_num}_composed.txt"
            step3_b1="${GEMINI_STEP3_PRIOR_STEP1_BYTES:-}"
            step3_b2="${GEMINI_STEP3_PRIOR_STEP2_BYTES:-}"
            {
                cat "${prompt_path}"
                echo ""
                echo "---"
                echo "## Context from previous step(s)"
                echo ""
                echo "### step1_output.md"
                echo ""
                if [ -n "${step3_b1}" ] && [ "${step3_b1}" != "0" ] 2>/dev/null; then
                    head -c "${step3_b1}" step1_output.md 2>/dev/null || true
                else
                    cat step1_output.md 2>/dev/null || true
                fi
                echo ""
                echo "### step2_output.md"
                echo ""
                if [ -n "${step3_b2}" ] && [ "${step3_b2}" != "0" ] 2>/dev/null; then
                    head -c "${step3_b2}" step2_output.md 2>/dev/null || true
                else
                    cat step2_output.md 2>/dev/null || true
                fi
            } > "${composed}"
            run_input="${composed}"
        elif [ -n "${prev_output}" ] && [ -f "${prev_output}" ]; then
            composed="step${step_num}_composed.txt"
            # Step 2: optional cap on appended step1 text (GEMINI_STEP2_PRIOR_CONTEXT_BYTES). Unset or 0 = no cap.
            # Example: some jobs set 131072 via Jenkins/Argo to limit prompt size.
            step2_prior_max="${GEMINI_STEP2_PRIOR_CONTEXT_BYTES:-}"
            {
                cat "${prompt_path}"
                echo ""
                echo "---"
                echo "## Context from previous step(s)"
                echo ""
                if [ "${step_num}" = "2" ] && [ -n "${step2_prior_max}" ] && [ "${step2_prior_max}" != "0" ] 2>/dev/null; then
                    head -c "${step2_prior_max}" "${prev_output}" 2>/dev/null || true
                else
                    cat "${prev_output}" 2>/dev/null || true
                fi
            } > "${composed}"
            run_input="${composed}"
        fi

        step_start=${SECONDS}
        _ts="$(date +%Y%m%d_%H%M%S)_${RANDOM}"
        step_json="headless_output_step${step_num}_${_ts}.json"
        export GEMINI_OUTPUT_FILE_NAME="${step_json}"
        run_one_prompt "${run_input}" "${step_json}" || { RESULT=$?; exit "${RESULT}"; }
        step_output="step${step_num}_output.md"
        extract_last_output_to "${step_output}" "${step_json}" || true
        echo -e "${GREEN}Step ${step_num} took $((SECONDS - step_start))s${NC}"
        prev_output="${step_output}"
        generated_outputs+=("${step_output}")
    done

    # Publish step outputs into gemini-assist/ for Jenkins archiveArtifacts / storage
    dest_dir="${GEMINI_ARTIFACT_PATH:-gemini-assist}"
    if [[ "$dest_dir" != /* ]]; then dest_dir="${PWD}/${dest_dir}"; fi
    mkdir -p "${dest_dir}"
    for f in "${generated_outputs[@]}"; do
        [ -f "${f}" ] && cp -f "${f}" "${dest_dir}/" && echo -e "${GREEN}Copied ${f} to ${dest_dir}${NC}"
    done

    [ -n "${GEMINI_ANALYSIS_PATH:-}" ] && [ -d "${GEMINI_ARTIFACT_PATH:-gemini-assist}" ] && echo -e "${GREEN}Sequenced analysis complete; artifacts in ${GEMINI_ARTIFACT_PATH:-gemini-assist}${NC}"
fi

exit "${RESULT}"
