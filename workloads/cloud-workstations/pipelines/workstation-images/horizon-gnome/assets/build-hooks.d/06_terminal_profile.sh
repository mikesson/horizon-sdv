#!/bin/bash

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

set -euo pipefail

# Default GNOME Terminal profile: DejaVu Sans Mono 12 for readability over RDP.
# Inherited by every GNOME thin-child. fonts-dejavu is already in GUI_PKGS.

echo "Configuring GNOME Terminal default font profile..."

mkdir -p /etc/dconf/profile /etc/dconf/db/local.d

printf 'user-db:user\nsystem-db:local\n' > /etc/dconf/profile/user

printf "%s\n" \
  "[org/gnome/terminal/legacy/profiles:]" \
  "default='b1dcc9dd-5262-4d8d-a863-c897e6d979b9'" \
  "list=['b1dcc9dd-5262-4d8d-a863-c897e6d979b9']" \
  "" \
  "[org/gnome/terminal/legacy/profiles:/:b1dcc9dd-5262-4d8d-a863-c897e6d979b9]" \
  "use-system-font=false" \
  "font='DejaVu Sans Mono 12'" \
  "cell-width-scale=1.0" \
  "cell-height-scale=1.0" \
  > /etc/dconf/db/local.d/00-horizon-terminal-font

dconf update
