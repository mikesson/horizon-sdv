#!/bin/bash
#
# Copyright 2024 Google Inc. All Rights Reserved.
# Modifications Copyright (c) 2025-2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

echo "Persisting Android SDKs"

# Provide Android SDKs via overlay mount containing both User Downloaded SDKs and SDKs bundled with the image.
image_sdk=/opt/google/Android/Sdk
user_sdk=/home/.workstation/UserSdk
overlay_work_dir=/home/.workstation/OverlayWorkDir
sdk_dir=/home/user/Android/Sdk

# Modification: this hook can run more than once per boot (once via
# start_systemd.sh's run_hooks, and again via the base image's
# workstation-startup.service -> /usr/bin/workstation-startup). Guard both
# code paths below so a second invocation is a no-op instead of stacking a
# duplicate overlay mount or failing on an already-created symlink/dirs.
if mountpoint -q "${sdk_dir}" 2>/dev/null || findmnt -n "${sdk_dir}" >/dev/null 2>&1; then
  echo "Android SDK overlay already mounted at ${sdk_dir}, skipping."
  exit 0
fi

# If we are not using a persistent home directory, just symlink the SDK
# directory as any downloaded packages will not be persisted anyway.
if [[ ! $(grep "/dev/disk/by-id/google-" /proc/mounts | grep "/home") ]]; then
  echo "No persistent disk mounted, user downloaded SDKs will not be persisted."
  if [[ -L "${sdk_dir}" ]]; then
    if [[ "$(readlink -f "${sdk_dir}")" == "$(readlink -f "${image_sdk}")" ]]; then
      echo "Symlink already present at ${sdk_dir}, skipping."
      exit 0
    fi
    echo "Error: ${sdk_dir} is a symlink pointing elsewhere ($(readlink -f "${sdk_dir}")). Refusing to overwrite." >&2
    exit 1
  elif [[ -e "${sdk_dir}" ]]; then
    echo "Error: ${sdk_dir} already exists and is not the expected symlink. Refusing to overwrite." >&2
    exit 1
  fi
  mkdir -p "$(dirname "${sdk_dir}")"
  chown -R user:user "$(dirname "${sdk_dir}")"
  ln -s "${image_sdk}" "${sdk_dir}"
  exit 0
fi

# User has opted out of using the overlay mount.
if [[ ! -d "${user_sdk}" && -d "${sdk_dir}" ]]; then
  echo "Android SDK cannot be mounted."
  echo "To recieve the latest SDK updates, please delete ${sdk_dir} and restart your workstation."
  exit 0
fi

if [[ -L "${sdk_dir}" ]]; then
  echo "Error: ${sdk_dir} is an unexpected symlink ($(readlink -f "${sdk_dir}")); expected a persisted overlay mountpoint. Refusing to mount." >&2
  exit 1
fi

# create requisite directory structure to persist user downloaded SDKs via overlay mount
if [[ ! -d "${user_sdk}" || ! -d "${overlay_work_dir}" || ! -d "${sdk_dir}" ]]; then
  mkdir -p "${user_sdk}" "${overlay_work_dir}" "${sdk_dir}"
  chown -R user:user "$(dirname "${user_sdk}")"
  chown -R user:user "$(dirname "${sdk_dir}")"
fi

echo "User downloaded SDKs will be persisted under ${user_sdk}"
mount overlay -t overlay -o lowerdir="${image_sdk}",upperdir="${user_sdk}",workdir="${overlay_work_dir}" "${sdk_dir}"
