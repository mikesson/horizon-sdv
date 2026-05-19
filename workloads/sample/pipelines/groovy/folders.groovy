// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Groovy file for creating folders in Jenkins for Sample pipelines (Horizon API integration).

folder('Sample') {
  displayName('Sample Workflows')
  description('<p>Demonstration pipelines that drive Horizon API and Argo Workflows (e.g. <code>sample-smoke-test</code>).</p>')
}

folder('Sample/Environment') {
  displayName('Environment')
  description('<p>Jobs that build execution images for Sample pipelines (tools: curl, jq, python3).</p>')
}

folder('Sample/Tests') {
  displayName('Tests')
  description('<p>Jobs that submit exposed workflow templates via Horizon API, stream logs, and report results.</p>')
}
