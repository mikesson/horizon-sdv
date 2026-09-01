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

// Jenkins folder: Cloud-Workstations/Workstation-Image-Chain (sibling of Workstation-Images).
// Leaf image jobs blockOn('...Images...') so keeping the orchestrator outside that folder
// avoids deadlock when this job triggers Preflight/GNOME/IDE children.
pipelineJob('Cloud-Workstations/Workstation-Image-Chain/Horizon AOSP Build Image Chain') {
  description("""
    <br/><h3 style="margin-bottom: 10px;">Workstation Image Chain Orchestrator</h3>
    <p>Builds the layered AOSP image chain in order:
      optional <code>horizon-preflight</code> → optional <code>horizon-gnome</code> →
      optional <code>horizon-asfp</code> / <code>horizon-android-studio</code> in parallel.</p>
    <p>Threads the published base tag as <code>BASE_IMAGE</code> between stages.
      Leave Preflight/GNOME unchecked to reuse <code>PREFLIGHT_IMAGE</code> / <code>GNOME_IMAGE</code>.
      Child IDE builds default on; uncheck either to skip that image.
      Enabled base stages always push; child <code>NO_PUSH</code> still applies to IDE images.
      Antigravity params are forwarded to GNOME only when that stage runs.
      Each leaf job remains independently triggerable.</p>
    <br/><div style="border-top: 1px solid #ccc; width: 100%;"></div><br/>
  """)

  parameters {
    stringParam {
      name('IMAGE_TAG')
      defaultValue('latest')
      description('<p>Tag applied to images built in this chain run.</p>')
      trim(true)
    }
    booleanParam {
      name('NO_PUSH')
      defaultValue(true)
      description('<p>If true, do not push child IDE images. Enabled base stages always push.</p>')
    }
    separator {
      name('Base Images')
      sectionHeader('Optional Base Images')
      sectionHeaderStyle("${HEADER_STYLE}")
      separatorStyle("${SEPARATOR_STYLE}")
    }
    booleanParam {
      name('BUILD_HORIZON_PREFLIGHT')
      defaultValue(false)
      description('<p>Build and push <code>horizon-preflight</code>. Unchecked: reuse <code>PREFLIGHT_IMAGE</code>.</p>')
    }
    stringParam {
      name('PREFLIGHT_IMAGE')
      defaultValue("${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${CLOUD_WS_HORIZON_PREFLIGHT_IMAGE_NAME}:latest")
      description('<p>Published Preflight URI used as GNOME <code>BASE_IMAGE</code> when Preflight is not built in-chain.</p>')
      trim(true)
    }
    stringParam {
      name('PREFLIGHT_WEB_REPO')
      defaultValue('')
      description('<p>Optional Preflight SPA git repo (passed through when building Preflight). Empty = vendored <code>preflight-web/</code>.</p>')
      trim(true)
    }
    stringParam {
      name('PREFLIGHT_WEB_DIR')
      defaultValue('')
      description('<p>Optional subdirectory in <code>PREFLIGHT_WEB_REPO</code> containing the SPA <code>package.json</code>.</p>')
      trim(true)
    }
    booleanParam {
      name('BUILD_HORIZON_GNOME')
      defaultValue(false)
      description('<p>Build and push <code>horizon-gnome</code>. Unchecked: reuse <code>GNOME_IMAGE</code> as child <code>BASE_IMAGE</code>.</p>')
    }
    stringParam {
      name('GNOME_IMAGE')
      defaultValue("${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${CLOUD_WS_HORIZON_GNOME_IMAGE_NAME}:latest")
      description('<p>Published GNOME URI used as child <code>BASE_IMAGE</code> when GNOME is not built in-chain.</p>')
      trim(true)
    }
    separator {
      name('Child IDE Images')
      sectionHeader('Child IDE Images')
      sectionHeaderStyle("${HEADER_STYLE}")
      separatorStyle("${SEPARATOR_STYLE}")
    }
    booleanParam {
      name('BUILD_HORIZON_ASFP')
      defaultValue(true)
      description('<p>Build <code>horizon-asfp</code> (Android Studio for Platform). Uncheck to skip.</p>')
    }
    booleanParam {
      name('BUILD_HORIZON_ANDROID_STUDIO')
      defaultValue(true)
      description('<p>Build <code>horizon-android-studio</code> (Android Studio). Uncheck to skip.</p>')
    }
    separator {
      name('Antigravity 2.0 Agent')
      sectionHeader('Antigravity 2.0 Agent (GNOME base)')
      sectionHeaderStyle("${HEADER_STYLE}")
      separatorStyle("${SEPARATOR_STYLE}")
    }
    booleanParam {
      name('INSTALL_ANTIGRAVITY_AGENT')
      defaultValue(true)
      description('<p>Forwarded to GNOME when that stage runs. Install Antigravity into the GNOME base.</p>')
    }
    stringParam {
      name('ANTIGRAVITY_2_VERSION')
      defaultValue('2.2.1')
      description('<p>Forwarded to GNOME. Version substring to verify; keep URL/SHA256 in sync.</p>')
      trim(true)
    }
    stringParam {
      name('ANTIGRAVITY_2_TARBALL_URL')
      defaultValue('https://storage.googleapis.com/antigravity-public/antigravity-hub/2.2.1-5287492581195776/linux-x64/Antigravity.tar.gz')
      description('<p>Forwarded to GNOME. linux-x64 Antigravity.tar.gz URL.</p>')
      trim(true)
    }
    stringParam {
      name('ANTIGRAVITY_2_TARBALL_SHA256')
      defaultValue('a6ba77046f92aa1ce21d8a0c67495471af4c2be11ba624e5d935821969b20989')
      description('<p>Forwarded to GNOME. SHA-256 of the Antigravity tarball (lowercase hex).</p>')
      trim(true)
    }
    separator {
      name('Common Parameters: Buildkit')
      sectionHeader('Common Parameters: Buildkit')
      sectionHeaderStyle("${HEADER_STYLE}")
      separatorStyle("${SEPARATOR_STYLE}")
    }
    stringParam {
      name('BUILDKIT_RELEASE_TAG')
      defaultValue("${BUILDKIT_RELEASE_TAG}")
      description('''<p>BuildKit tag (<a target="_blank" href=https://hub.docker.com/r/moby/buildkit>releases</a>). Passed to triggered jobs.</p>''')
      trim(true)
    }
    stringParam {
      name('DOCKER_CREDENTIALS_URL')
      defaultValue("${DOCKER_CREDENTIALS_URL}")
      description('<p>Docker credentials helper URL. Passed to triggered jobs.</p>')
      trim(true)
    }
  }

  // Block while Workstation Images builders are running.
  blockOn('Cloud*.*Workstation*.*Images.*') {
    // Possible values are 'GLOBAL' and 'NODE' (default).
    blockLevel('GLOBAL')
    // Possible values are 'ALL', 'BUILDABLE' and 'DISABLED' (default).
    scanQueueFor('BUILDABLE')
  }

  logRotator {
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
      scriptPath('workloads/cloud-workstations/pipelines/workstation-image-chain/horizon-aosp-build-image-chain/Jenkinsfile')
    }
  }
}
