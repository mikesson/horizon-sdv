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

pipelineJob('Cloud-Workstations/Workstation-Images/Horizon Preflight') {
  description("""
    <br/><h3 style="margin-bottom: 10px;">Workstation Image Builder</h3>
    <p>This job builds the Horizon Preflight provider image used as the base layer for <code>horizon-gnome</code> and IDE child images.</p>
    <h4 style="margin-bottom: 10px;">Image Configuration</h4>
    <p>The image is an Alpine asset-export layer. It packages the nginx Preflight loader SPA and templates, systemd orchestration units, machine-id setup, Cloud Logging setup, and <code>config_rendering</code> (ephemeral credential generation) under <code>/export/</code>. It is not a bootable desktop image by itself; <code>horizon-gnome</code> copies <code>/export/</code> into the GNOME base.</p>
    <h4 style="margin-bottom: 10px;">Pushing Changes to the Registry</h4>
    <p>To push changes to the registry, set the parameter <code>NO_PUSH=false</code>.</p>
    <p>The image will be pushed to <code>${CLOUD_REGION}-docker.pkg.dev/${CLOUD_PROJECT}/${CLOUD_WS_HORIZON_PREFLIGHT_IMAGE_NAME}</code></p>
    <h4 style="margin-bottom: 10px;">Verifying Changes</h4>
    <p>When working with new Dockerfile updates, it's recommended to set <code>NO_PUSH=true</code> to verify the changes before pushing the image to the registry.</p>
    <h4 style="margin-bottom: 10px;">Important Notes</h4>
    <p>This job need only be run once, or when there are updates to be applied based on Dockerfile changes. Optional <code>PREFLIGHT_WEB_REPO</code> / <code>PREFLIGHT_WEB_DIR</code> override the vendored SPA source; leave empty to build from the in-tree <code>preflight-web/</code>.</p>
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
      name('PREFLIGHT_WEB_REPO')
      defaultValue('')
      description('''<p>Optional: Git repository URL for the Preflight SPA. Leave empty to use the vendored <code>preflight-web/</code> directory.</p>''')
      trim(true)
    }
    stringParam {
      name('PREFLIGHT_WEB_DIR')
      defaultValue('')
      description('''<p>Optional: Subdirectory inside <code>PREFLIGHT_WEB_REPO</code> that contains the SPA <code>package.json</code> (for example <code>examples/preflight-web</code>).</p>''')
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
      scriptPath('workloads/cloud-workstations/pipelines/workstation-images/horizon-preflight/Jenkinsfile')
    }
  }
}
