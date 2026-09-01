# Copyright (c) 2025-2026 Accenture, All Rights Reserved.
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

# Horizon Android Studio image.
# Thin child of the published horizon-gnome base image: adds Android Studio,
# the Android SDK/emulator, and Android-specific desktop integration on top of
# the inherited GNOME desktop, gnome-remote-desktop (RDP), Guacamole (DinD),
# Chrome, systemd, Gemini CLI/ADK, gemini-mcp-agent, and the Antigravity 2.0
# Agent already baked into horizon-gnome. Do not re-declare any of those; they
# are inherited unchanged.

ARG BASE_IMAGE
FROM ${BASE_IMAGE}

# Pinned Android Studio / command-line tools versions. Bump deliberately; do
# not let sdkmanager silently drift these via an unreviewed "--update".
# Bump ANDROID_STUDIO_VERSION and ANDROID_STUDIO_SHA256 together, in the same
# commit. SHA-256 is published per release/platform on
# https://developer.android.com/studio/archive (Linux zip entry).
ARG ANDROID_STUDIO_VERSION=2025.2.3.7
ARG ANDROID_STUDIO_SHA256=77905c01491857b3b6e70ca889f899c5f52c00d3f12dafd092e4fdbb5aaa7cff
ARG COMMAND_LINE_TOOLS_VERSION=11076708

ENV ANDROID_STUDIO_VERSION="${ANDROID_STUDIO_VERSION}"
ENV COMMAND_LINE_TOOLS_VERSION="${COMMAND_LINE_TOOLS_VERSION}"

# ANDROID_HOME points at the runtime overlay mount created by
# etc/workstation-startup.d/210_mount-android-sdk.sh, which merges the
# image-bundled SDK (/opt/google/Android/Sdk) with any user-installed
# packages persisted under /home/.workstation. The cmdline-tools bin
# directory is image-bundled and always available regardless of the overlay.
ENV ANDROID_HOME=/home/user/Android/Sdk
ENV ANDROID_SDK_ROOT=/home/user/Android/Sdk
ENV PATH="/opt/google/cmdline-tools/bin:${ANDROID_HOME}/platform-tools:${ANDROID_HOME}/emulator:${PATH}"

# Registers Android Studio as the primary application: the inherited
# desktop_apply_integration (google/scripts/build/desktop_integration.sh)
# pins it to the dock and autostarts it via /etc/xdg/autostart.
ENV WORKSTATION_DEFAULT_APP=jetbrains-studio

# Merge Android Studio assets: the SDK/IDE installation build hook, the
# XWayland-safe launcher, the desktop entry, and the hardened SDK overlay
# startup hook.
COPY assets/ /

# Run the inherited centralized workstation configuration: build-hooks.d
# installs Android Studio and the Android SDK, post-install-hooks.d applies
# any file patches, and desktop integration registers/autostarts the IDE.
RUN /google/scripts/build/configure_workstation.sh

# Hard build assertions. configure_workstation.sh's run_hooks() logs a
# warning and continues on hook failure instead of failing the build, so the
# Android Studio/SDK toolchain installed by 10_install_android_studio.sh must
# be verified explicitly here.
RUN test -x /opt/google/android-studio/bin/studio.sh && \
    test -x /usr/local/bin/android-studio-launcher && \
    test -x /opt/google/cmdline-tools/bin/sdkmanager && \
    test -x /opt/google/Android/Sdk/emulator/emulator && \
    test -f /usr/share/applications/jetbrains-studio.desktop && \
    test -d /opt/google/Android/Sdk/build-tools/35.0.0 && \
    test -d /opt/google/Android/Sdk/build-tools/35.0.0-rc1 && \
    test -d /opt/google/Android/Sdk/platforms/android-34 && \
    test -d /opt/google/Android/Sdk/platforms/android-35 && \
    test -d /opt/google/Android/Sdk/platform-tools && \
    test -d /opt/google/Android/Sdk/sources/android-35 && \
    test -d /opt/google/Android/Sdk/system-images/android-34/google_apis/x86_64 && \
    test -d /opt/google/Android/Sdk/system-images/android-34/google_apis_playstore/x86_64

# No ENTRYPOINT declared: Docker inherits /google/scripts/entrypoint.sh from
# horizon-gnome, which hands over to systemd after running
# /etc/workstation-startup.d hooks.
