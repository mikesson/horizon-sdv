<!-- Copyright (c) 2026 Accenture, All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

        http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Per-stage Gemini CLI runs (token budget mitigation)

## Context

Sequenced AI Review (`gemini_analysis.sh`) builds **composed prompts** for each step and appends **prior step output** as context. In **constrained** Gemini deployments, **total tokens per analysis** are capped. Any stage—**triage**, **RCA**, or **fix**—can approach or exceed limits when skills, workspace rules, and large `test-results/` excerpts accumulate in **one** long request chain.

## Proposal

Run **`gemini` / Gemini CLI** as **separate processes per stage** (“per agent”), not one unbounded multi-step conversation:

1. **Triage** — one invocation; write **`step1_output.md`** (and any allowed artifacts) to disk; **exit**.
2. **RCA** — new invocation; input = **RCA prompt** + **`step1_output.md`** (optionally **capped**, e.g. existing `GEMINI_STEP2_PRIOR_CONTEXT_BYTES` pattern).
3. **Fix** — new invocation; input = **fix prompt** + **`step1_output.md`** + **`step2_output.md`** (with caps if needed).

Each stage gets a **fresh context window** for that call. Handoff is **explicit file-based**, not an ever-growing single prompt.

## Benefits

- **Lower peak tokens per API call** than stuffing full history into one composed blob.
- **Isolation**: failure in RCA does not require redoing triage if **`step1_output.md`** is already persisted.
- **Clearer ownership**: triage vs RCA vs fix prompts and skills can be tuned **per stage**.

## Costs / risks

- **Orchestration** must guarantee step order, artifact paths, and failure propagation (Jenkins / Argo already stage-oriented in spirit).
- **Duplicate system overhead**: if every stage resends the **full** `skills.yaml`, token cost can **repeat**; mitigations include **stage-scoped** skills, slimmer SKILL.md per step, or shared static prefix minimization.
- **Information loss** if prior-step files are **over-summarized**; caps must balance **budget vs fidelity**.
- **Total cost** may rise or fall depending on duplication vs today’s single-chain truncation.

## Relation to current script

`gemini_analysis.sh` runs **one Gemini CLI process per sequenced step**, each with its own JSON output file (`headless_output_stepN_*.json`), and writes **`stepN_output.md`** from that JSON. Step 2 appends capped or full **`step1_output.md`**; step 3 appends **both** **`step1_output.md`** and **`step2_output.md`** (with optional `GEMINI_STEP3_PRIOR_STEP*_BYTES` caps). See **`docs/workloads/common/agentic-ai/gemini.md`** → **Sequenced analysis**. Remaining product work is **hard budgets**, observability, and policy—beyond what the shell script enforces.

## Experimental note

This strategy is an **experimental example** for discussion and implementation guidance; **it will evolve** in future releases. Broader **Agentic AI** work (per-stage token budgets, enforcement, observability) is platform scope beyond what the shell scripts in this repository enforce.

## Open questions

- Per-stage **skills** split vs one YAML (maintenance vs token duplication).
- **Structured** handoff (e.g. JSON summary + pointers to log paths) vs raw markdown only.
- Where **precheck** runs (shell before CLI vs policy in wrapper).
