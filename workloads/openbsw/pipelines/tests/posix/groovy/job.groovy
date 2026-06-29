// Copyright (c) 2025-2026 Accenture, All Rights Reserved.
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

// Description:
// Groovy file for defining a Jenkins Pipeline Job for testing the OpenBSW
// POSIX application.
pipelineJob('OpenBSW/Tests/POSIX') {
  description("""
    <br/><h3 style="margin-bottom: 10px;">OpenBSW POSIX Test Job</h3>
    <p>This job is used to test a prior build of the OpenBSW POSIX reference application via <a href="http://${HORIZON_DOMAIN}/mtk-connect/portal/testbenches" target="_blank">MTK Connect</a>.</p>
    <h4 style="margin-bottom: 10px;">Reference documentation:</h4>
    <ul>
      <li><a href="https://eclipse-openbsw.github.io/openbsw/sphinx_docs/doc/dev/index.html" target="_blank">Welcome to Eclipse OpenBSW.</a></li>
      <li><a href="https://eclipse-openbsw.github.io/openbsw/sphinx_docs/doc/dev/learning/unit_tests/index.html" target="_blank">Building and Running Unit Tests.</a></li>
      <li><a href="https://eclipse-openbsw.github.io/openbsw/sphinx_docs/doc/dev/learning/setup/setup_posix_build.html#setup-posix-build" target="_blank">POSIX Platform.</a></li>
      <li><a href="https://eclipse-openbsw.github.io/openbsw/sphinx_docs/doc/dev/learning/setup/setup_s32k148_ubuntu_build.html" target="_blank">S32K148 Platform.</a></li>
      <li><a href="https://eclipse-openbsw.github.io/openbsw/sphinx_docs/doc/learning/console/index.html" target="_blank">Application Console.</a></li>
    </ul>
    <h4 style="margin-bottom: 10px;">Job overview</h4>
    <p>Devices are initialized and remain active for a configured period so you can exercise the application through MTK Connect.<br/>
    After <code>POSIX_KEEP_ALIVE_TIME</code>, devices, testbenches, and the test instance are stopped in a controlled manner.</p>
    <h4 style="margin-bottom: 10px;">Mandatory parameters</h4>
    <ul>
      <li><code>OPENBSW_DOWNLOAD_URL</code> — GCS path to the POSIX output from <b>BSW Builder</b> (folder that contains <code>posix.tgz</code>). Example: <code>gs://…/OpenBSW/Builds/BSW_Builder/&lt;BUILD_NUMBER&gt;/posix/</code>. For Rust, use an artifact built with <code>RTOS_PLATFORM=rust</code> (tree includes <code>build/posix-rust/…</code>).</li>
    </ul>
    <h4 style="margin-bottom: 10px;">POSIX application test execution guide</h4>
    <p>Use this guide after artifacts are on the host: bring up networking, start the reference ELF, then run pyTest if needed.</p>
    <p><b>Working directory:</b> run everything below from <code>\${HOME}/posix</code> — that is where this job unpacks <code>posix.tgz</code> (<code>tools/</code>, <code>build/</code>, <code>test/</code> live there). If your shell is in <code>\${HOME}</code> instead, prefix these paths with <code>posix/</code>.</p>
    <h4 style="margin-bottom: 10px;">One-time setup</h4>
    <p>Run once per machine boot (or when networking was reset). Use <code>sudo</code> if the host requires it for <code>ip link</code> / CAN:</p>
    <pre><code class="language-bash">cd "\${HOME}/posix"
# Bring up Ethernet
./tools/enet/bring-up-ethernet.sh
# Bring up virtual CAN on vcan0
./tools/can/bring-up-vcan0.sh</code></pre>
    <h4 style="margin-bottom: 10px;">Launch the reference application</h4>
    <p>Starts the POSIX reference application console (match the preset you built in BSW Builder):</p>
    <p><b>posix-freertos</b></p>
    <pre><code class="language-bash">./build/posix-freertos/executables/referenceApp/application/Release/app.referenceApp.elf</code></pre>
    <p><b>posix-threadx</b></p>
    <pre><code class="language-bash">./build/posix-threadx/executables/referenceApp/application/Release/app.referenceApp.elf</code></pre>
    <p><b>posix-rust</b></p>
    <pre><code class="language-bash">./build/posix-rust/executables/referenceApp/application/Release/app.referenceApp.elf</code></pre>
    <ul>
      <li>Keep this running while testing.</li>
      <li>Stop with Ctrl+C when done.</li>
    </ul>
    <h4 style="margin-bottom: 10px;">Run POSIX pyTest</h4>
    <p>From <code>\${HOME}/posix/test/pyTest</code> (after <code>cd "\${HOME}/posix"</code>):</p>
    <p><b>posix-freertos</b></p>
    <pre><code>cd "\${HOME}/posix/test/pyTest" && pytest --target=posix --app=freertos</code></pre>
    <p><b>posix-threadx</b></p>
    <pre><code>cd "\${HOME}/posix/test/pyTest" && pytest --target=posix --app=threadx</code></pre>
    <p><b>posix-rust</b></p>
    <pre><code>cd "\${HOME}/posix/test/pyTest" && pytest --target=posix --app=rust</code></pre>
    <br/><div style="border-top: 1px solid #ccc; width: 100%;"></div><br/>""")

  parameters {
    stringParam {
      name('OPENBSW_DOWNLOAD_URL')
      defaultValue('')
      description("""<p>Storage URL pointing to the POSIX artifact folder (contains <code>posix.tgz</code>), e.g.<br/>
        <code>gs://${OPENBSW_BUILD_BUCKET_ROOT_NAME}/OpenBSW/Builds/BSW_Builder/&lt;BUILD_NUMBER&gt;/posix/</code><br/><br/>
        <b>Note:</b>
          <ul><li>If the build number is a single digit, zero-pad it (e.g. <code>01</code>–<code>09</code>) where your bucket layout requires it.</li></ul></p>""")
      trim(true)
    }

    stringParam {
      name('IMAGE_TAG')
      defaultValue("${OPENBSW_IMAGE_TAG}")
      description('''<p>Docker image template to use.</p>
        <p>Note: tag may only contain 'abcdefghijklmnopqrstuvwxyz0123456789_-./'</p>''')
      trim(true)
    }

    choiceParam {
      name('POSIX_KEEP_ALIVE_TIME')
      choices(['5', '15', '30', '60', '90', '120', '180'])
      description('''<p>Time in minutes, to keep host instance alive before stopping.</p>''')
    }

    stringParam {
      name('NUM_HOST_INSTANCES')
      defaultValue('2')
      description('''<p>Number of host instances to create.</p>
        <p>i.e. the number of devices to create in MTK Connect testbench.</p>''')
      trim(true)
    }

    booleanParam {
      name('MTK_CONNECT_PUBLIC')
      defaultValue(false)
      description('''<p>When checked, the MTK Connect testbench is visible to everyone.<br/>
        By default, testbenches are private and only visible to their creator and MTK Connect administrators.</p>''')
    }
  }

  logRotator {
    artifactDaysToKeep(60)
    artifactNumToKeep(100)
    daysToKeep(7)
    numToKeep(50)
  }

  definition {
    cpsScm {
      lightweight()
      scm {
        git {
          remote {
            url("${HORIZON_SCM_URL}")
            credentials('jenkins-scm-creds')
          }
          branch("*/${HORIZON_SCM_BRANCH}")
        }
      }
      scriptPath('workloads/openbsw/pipelines/tests/posix/Jenkinsfile')
    }
  }
}
