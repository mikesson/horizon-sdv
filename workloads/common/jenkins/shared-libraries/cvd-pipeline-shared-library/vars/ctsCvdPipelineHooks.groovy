// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// CTS Execution hook stages for cvdPipeline: runs after the VM agent is allocated,
// bracketing Launch Virtual Devices / MTK Connect (see workloads/android/pipelines/tests/cts_execution/Jenkinsfile).

def call() {
  return [
    preLaunchStages: [
      [
        name: 'List Tests',
        steps: {
          if (env.CTS_TEST_LISTS_ONLY == 'true') {
            script {
              currentBuild.description = "${params.JENKINS_GCE_CLOUD_LABEL}" + "<br/>" + "Android Version: ${params.ANDROID_VERSION}" + "<br/>" + "$BUILD_USER"
            }
            sh '''
              ANDROID_VERSION=${ANDROID_VERSION} \
              CTS_DOWNLOAD_URL="${CTS_DOWNLOAD_URL}" \
              CUTTLEFISH_DOWNLOAD_URL="${CUTTLEFISH_DOWNLOAD_URL}" \
              ./workloads/android/pipelines/tests/cts_execution/cts_initialise.sh
              CTS_TEST_LISTS_ONLY="${CTS_TEST_LISTS_ONLY}" \
              ./workloads/android/pipelines/tests/cts_execution/cts_execution.sh
            '''
            archiveArtifacts artifacts: 'cts*.txt', followSymlinks: false, onlyIfSuccessful: false, allowEmptyArchive: true
          } else {
            sh 'echo "Skip Test list based on expressions"'
          }
        }
      ]
    ],
    postMtkConnectStages: [
      [
        name: 'CTS execution',
        steps: {
          if (env.CTS_TEST_LISTS_ONLY == 'false') {
            if (env.CUTTLEFISH_DOWNLOAD_URL &&
                currentBuild.currentResult == 'SUCCESS') {
              script {
                currentBuild.description = "${params.JENKINS_GCE_CLOUD_LABEL}" + "<br/>" + "Android Version: ${params.ANDROID_VERSION}" + "<br/>" + "$BUILD_USER"
              }
              catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {
                sh '''
                  ANDROID_VERSION=${ANDROID_VERSION} \
                  CTS_DOWNLOAD_URL="${CTS_DOWNLOAD_URL}" \
                  ./workloads/android/pipelines/tests/cts_execution/cts_initialise.sh
                  CTS_TESTPLAN="${CTS_TESTPLAN}" \
                  CTS_MODULE="${CTS_MODULE}" \
                  CTS_TIMEOUT="${CTS_TIMEOUT}" \
                  CTS_RETRY_STRATEGY="${CTS_RETRY_STRATEGY}" \
                  CTS_MAX_TESTCASE_RUN_COUNT="${CTS_MAX_TESTCASE_RUN_COUNT}" \
                  SHARD_COUNT="${NUM_INSTANCES}" \
                  ./workloads/android/pipelines/tests/cts_execution/cts_execution.sh
                '''
              }

              archiveArtifacts artifacts: 'cts*.txt', followSymlinks: false, onlyIfSuccessful: false, allowEmptyArchive: true
              archiveArtifacts artifacts: 'android-cts-results/invocation_summary.txt', followSymlinks: false, onlyIfSuccessful: false, allowEmptyArchive: true
              archiveArtifacts artifacts: 'android-cts-results/*.zip', followSymlinks: false, onlyIfSuccessful: false, allowEmptyArchive: true
              archiveArtifacts artifacts: 'android-cts-results-html/*.html', followSymlinks: false, onlyIfSuccessful: false, allowEmptyArchive: true

              publishHTML target: [
                allowMissing: true,
                alwaysLinkToLastBuild: true,
                keepAll: true,
                reportDir: "android-cts-results-html",
                reportFiles: 'test_result_failures_suite.html',
                reportName: 'CTS Results'
              ]
            } else {
              sh 'echo "Skip CTS execution based on expressions"'
            }
          }
        }
      ]
    ]
  ]
}
