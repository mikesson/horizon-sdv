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

# CVD pipeline shared library (`vars/`)

Jenkins [shared pipeline library](https://www.jenkins.io/doc/book/pipeline/shared-libraries/) steps for **Cuttlefish (CVD)** workflows: launch devices on a GCE VM, optional MTK Connect, optional CTS-specific stages, then **Diagnostics & Teardown** (AI Review, VM cleanup, MTK offline delete).

Consumer jobs load the library with:

`@Library('cvd-pipeline-shared-library') _`

## `cvdPipeline(Map config)`

Single entry point used by:

| Job | Jenkinsfile | Typical hooks |
|-----|-------------|----------------|
| **CVD Launcher** | `workloads/android/pipelines/tests/cvd_launcher/Jenkinsfile` | None (default Cuttlefish + MTK + keep-alive path only) |
| **CTS Execution** | `workloads/android/pipelines/tests/cts_execution/Jenkinsfile` | `preLaunchStages` + `postMtkConnectStages` from `ctsCvdPipelineHooks` |

### Stage order (GCE agent)

1. **Pre-launch** — optional; see `preLaunchStages`.
2. **Launch Virtual Devices** — `cvd_start_stop.sh --start` (honours `launcher_condition`).
3. **MTK Connect to Virtual Devices** — optional; `connect_condition`, success after launch.
4. **Post-MTK Connect** — optional; see `postMtkConnectStages`.
5. **Keep Devices Alive** / **MTK Connect Delete Testbench** / **Stop Virtual Devices** — gated by `keep_dev_alive_cond`, `stop_devices_cond`, etc.

Then **Diagnostics & Teardown** (Kubernetes agent): AI Review, remove VM on failure, delete offline testbenches.

### Notable `config` keys

| Key | Purpose |
|-----|--------|
| `preLaunchStages` | List of maps `[ name: String, steps: Closure ]` run **before** CVD launch. |
| `postMtkConnectStages` | Same shape, run **after** MTK Connect succeeds (or after launch if MTK skipped). |
| `customStageOne` / `customStageTwo` | **Legacy** aliases for `preLaunchStages` / `postMtkConnectStages`. |
| `launcher_condition`, `connect_condition`, `keep_dev_alive_cond`, `stop_devices_cond` | Lists of Groovy expressions evaluated with `evaluate()`; empty list means “always true” for that hook. |
| `aiReview` | Enables Gemini AI Review on failure in **Diagnostics**. Keys include `preset: 'cvd'` \| `'cts'`, optional `requireCtsNotListOnly`, `promptSequencedDir`, etc. See **`cvdPipeline.groovy`** for behavior and artifact filters. |
| `mtkConnectTunnelPort` | Overrides `MTK_CONNECT_TUNNEL_PORT` for MTK scripts. |
| `cleanup_container_timeout` | Hours for the Diagnostics pod `sleep` (default 4). |

On MTK Connect `--start` failure, the pipeline sets `env.MTK_CONNECT_STAGE_FAILED=true` so **AI Review is skipped** (avoids triaging CVD logs when the failure was MTK timeout/connect). See `cvdPipeline.groovy` for the exact gate.

### Implementation files

- **`cvdPipeline.groovy`** — pipeline definition, AI Review (Gemini), artifact copy filters for Diagnostics.
- **`ctsCvdPipelineHooks.groovy`** — CTS-only hook bodies (list tests, CTS execution, archives, HTML publisher).

## `ctsCvdPipelineHooks()`

Returns a map:

```groovy
[
  preLaunchStages: [ [ name: '...', steps: { ... } ], ... ],
  postMtkConnectStages: [ ... ]
]
```

Used only by **CTS Execution**; keeps `workloads/android/pipelines/tests/cts_execution/Jenkinsfile` limited to `cvdPipeline(...)` parameters and delegates CTS shell steps to this file.

To add or reorder CTS stages, edit **`ctsCvdPipelineHooks.groovy`** in the same repo revision as the job’s shared library (Jenkins must load a library version that contains this `vars/` file).

## References

- User-facing CTS doc: `docs/workloads/android/tests/cts_execution.md`
- User-facing CVD Launcher doc: `docs/workloads/android/tests/cvd_launcher.md`
