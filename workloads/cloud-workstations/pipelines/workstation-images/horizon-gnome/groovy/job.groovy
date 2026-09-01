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

pipelineJob('Cloud-Workstations/Workstation-Images/Horizon GNOME') {
  description("""
    <br/><h3 style="margin-bottom: 10px;">Workstation Image Builder</h3>
    <p>This job builds the Horizon GNOME base image used by IDE child images (ASfP, Android Studio).</p>
    <h4 style="margin-bottom: 10px;">Image Configuration</h4>
    <p>Bootable headless GNOME desktop with <code>gnome-remote-desktop</code> (internal RDP), Apache Guacamole (<code>guacd</code> + web app) via in-image Docker, Gemini CLI, Python ADK, the Antigravity 2.0 Agent, and Horizon customizations (<code>gemini-mcp-agent</code>, <code>HORIZON_DOMAIN</code>/<code>GOOGLE_CLOUD_PROJECT</code>). Consumes the published <code>horizon-preflight</code> image via the <code>BASE_IMAGE</code> parameter. GNOME thin-children (Android Studio, ASfP) inherit the Antigravity 2.0 Agent from this base; do not re-install it there.</p>
    <h4 style="margin-bottom: 10px;">Pushing Changes to the Registry</h4>
    <p>To push changes to the registry, set the parameter <code>NO_PUSH=false</code>.</p>
    <p>The image will be pushed to <code>${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${CLOUD_WS_HORIZON_GNOME_IMAGE_NAME}</code></p>
    <h4 style="margin-bottom: 10px;">Verifying Changes</h4>
    <p>When working with new Dockerfile updates, it's recommended to set <code>NO_PUSH=true</code> to verify the changes before pushing the image to the registry.</p>
    <h4 style="margin-bottom: 10px;">Important Notes</h4>
    <p>Requires a previously built and pushed <code>horizon-preflight</code> image. Set <code>BASE_IMAGE</code> to that published tag. TigerVNC and Chrome Remote Desktop remain disabled.</p>
    <br/><div style="border-top: 1px solid #ccc; width: 100%;"></div><br/>
  """)

  parameters {
    stringParam {
      name('IMAGE_TAG')
      defaultValue('latest')
      description('''<p><b>Mandatory:</b> Image tag for the Workstation image.</p>''')
      trim(true)
    }
    booleanParam {
      name('NO_PUSH')
      defaultValue(true)
      description('''<p>Build only, do not push to registry.</p>''')
    }
    stringParam {
      name('BASE_IMAGE')
      defaultValue("${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${CLOUD_WS_HORIZON_PREFLIGHT_IMAGE_NAME}:latest")
      description('''<p><b>Mandatory:</b> Full URI of the published <code>horizon-preflight</code> image used as <code>BASE_IMAGE</code> (preflight provider).</p>''')
      trim(true)
    }
    stringParam {
      name('CWS_BASE_IMAGE_TAG')
      defaultValue('latest')
      description('''<p>Tag of the Google <code>predefined/base</code> Cloud Workstations image this image is built on. Pin to a specific tag for reproducible builds.</p>''')
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
      description('''<p>BuildKit tag, see <a target="_blank"  href=https://hub.docker.com/r/moby/buildkit>buildkit releases</a>.</p>''')
      trim(true)
    }
    stringParam {
      name('DOCKER_CREDENTIALS_URL')
      defaultValue("${DOCKER_CREDENTIALS_URL}")
      description('''<p>Docker credentials helper URL, e.g. <a target="_blank" href=https://cloud.google.com/artifact-registry/docs/docker/authentication#standalone-helper>credentials helper</a>.</p>''')
      trim(true)
    }
    separator {
      name('Antigravity 2.0 Agent')
      sectionHeader('Antigravity 2.0 Agent')
      sectionHeaderStyle("${HEADER_STYLE}")
      separatorStyle("${SEPARATOR_STYLE}")
    }
    booleanParam {
      name('INSTALL_ANTIGRAVITY_AGENT')
      defaultValue(true)
      description('''<p>Install the Antigravity 2.0 Agent (desktop app) into this base image. GNOME thin-children (Android Studio, ASfP) inherit it; leave enabled unless explicitly opting a build out.</p>''')
    }
    stringParam {
      name('ANTIGRAVITY_2_VERSION')
      defaultValue('2.2.1')
      description('''<p>Antigravity 2.0 Agent version substring to verify at build time.</p>
      <p>Note: along with version, correspondingly update ANTIGRAVITY_2_TARBALL_URL and ANTIGRAVITY_2_TARBALL_SHA256.</p>''')
      trim(true)
    }
    stringParam {
      name('ANTIGRAVITY_2_TARBALL_URL')
      defaultValue('https://storage.googleapis.com/antigravity-public/antigravity-hub/2.2.1-5287492581195776/linux-x64/Antigravity.tar.gz')
      description('<p>Direct linux-x64 Antigravity.tar.gz URL (tarball mode).</p>')
      trim(true)
    }
    stringParam {
      name('ANTIGRAVITY_2_TARBALL_SHA256')
      defaultValue('a6ba77046f92aa1ce21d8a0c67495471af4c2be11ba624e5d935821969b20989')
      description('<p>SHA-256 of the Antigravity 2.0 Agent tarball (must match URL). Use lowercase hex.</p>')
      trim(true)
    }
  }

  // Block build if certain jobs are running.
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
      scriptPath('workloads/cloud-workstations/pipelines/workstation-images/horizon-gnome/Jenkinsfile')
    }
  }
}
