#!/usr/bin/env bash

# Copyright (c) 2024-2026 Accenture, All Rights Reserved.
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
#
# Description:
# This script creates the SDK addon file and devices.xml for AVD targets.
# It then packs AVD image for use with Android Studio.
#
# This script does the following:
#
#  1. Retrieves the reference SDK addon file for use with AVD images
#     and Android Studio.
#     - Clones the Android SDK tools repository (sparse) if file
#       does not exist.
#  2. Creates a devices.xml file based on the template.
#  3. Updates the archives to include the devices.xml file.
#  4. Creates the SDK Addons file.
#  5. Adds SDK addons to archives for upload.
#
# The following variables must be set before running this script:
#  - AAOS_LUNCH_TARGET: the target device.
#  - ANDROID_VERSION: the Android version (default: 14).
#        Determines the API level.
#
# Example usage:
# AAOS_LUNCH_TARGET=sdk_car_x86_64-ap1a-userdebug \
# ANDROID_VERSION=14 \
# ./workloads/android/pipelines/builds/aaos_builder/aaos_avd_sdk.sh

# Include common functions and variables.
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")"/aaos_environment.sh "$0"

# SDK addon xml example: aaos-sys-img2-1.xml
declare -r aaos_sys_image_file="device/generic/car/tools/aaos-sys-img2-1.xml"
# Template devices.xml
declare -r aaos_devices_file="device/generic/car/tools/x86_64/devices.xml"

# Dynamic values required in addon.
declare avd_sha1=''
declare avd_size=''
declare avd_image_url=''
declare output_dir=''
declare -r aaos_arch="${AAOS_ARCH}${AAOS_ARCH_ABI}"

# Create the SDK Addons file for use with AVD images and Android Studio.
function create_sdk_addons() {
    # Make a copy
    cp -f "${aaos_sys_image_file}" "${AAOS_SDK_ADDON_FILE}"

    # Ensure XML is well formed.
    if [[ "$(head -n 1 "${AAOS_SDK_ADDON_FILE}")" =~ ^\<\yxml ]]; then
        echo "File is well formed."
    else
        sed -i "1i <?xml version=\'1.0\' encoding=\'utf-8\'?>" "${AAOS_SDK_ADDON_FILE}"
    fi

    # Add channel references
    sed -i '0,/<remotePackage>/ s|<remotePackage|<channel id="channel-0">dev</channel>\n    <remotePackage|' "${AAOS_SDK_ADDON_FILE}"
    sed -i '/<uses-license ref=.*/a \        <channelRef ref="channel-0"/>' "${AAOS_SDK_ADDON_FILE}"

    # Update remote name
    local package_path="system-images;android-${ANDROID_API_LEVEL};aaos-horizon-sdv;${aaos_arch}"
    sed -i "s|<remotePackage path=\"[^\"]*\">|<remotePackage path=\"${package_path}\">|" "${AAOS_SDK_ADDON_FILE}"

    # Update Tag Display Name and ID.
    sed -i "/<tag>/,/<\/tag>/ s|<display>.*</display>|<display>Horizon SDV</display>|" "${AAOS_SDK_ADDON_FILE}"
    sed -i "/<tag>/,/<\/tag>/ s|<id>.*</id>|<id>android-automotive-playstore</id>|" "${AAOS_SDK_ADDON_FILE}"
    # Update Vendor Display Name and ID.
    sed -i "/<vendor>/,/<\/vendor>/ s|<display>.*</display>|<display>Horizon SDV</display>|" "${AAOS_SDK_ADDON_FILE}"
    sed -i "/<vendor>/,/<\/vendor>/ s|<id>.*</id>|<id>horizon-sdv</id>|" "${AAOS_SDK_ADDON_FILE}"
    # Update API Level
    sed -i "s|<api-level>[^<]*</api-level>|<api-level>${ANDROID_API_LEVEL}</api-level>|" "${AAOS_SDK_ADDON_FILE}"
    # Update ARCH
    sed -i "s|<abi>[^<]*</abi>|<abi>${aaos_arch}</abi>|" "${AAOS_SDK_ADDON_FILE}"
    # Update Display Name
    display_name="Horizon SDV AAOS - ${JOB_NAME}-${AAOS_BUILD_NUMBER}"
    sed -i "s|<display-name>\(.*\)</display-name>|<display-name>${display_name}</display-name>|" "${AAOS_SDK_ADDON_FILE}"
    # Update archive details.
    sed -i "s|<size>[^<]*</size>|<size>${avd_size}</size>|" "${AAOS_SDK_ADDON_FILE}"
    sed -i "s|<checksum>[^<]*</checksum>|<checksum>${avd_sha1}</checksum>|" "${AAOS_SDK_ADDON_FILE}"
    sed -i "s|<url>[^<]*</url>|<url>${avd_image_url}</url>|" "${AAOS_SDK_ADDON_FILE}"

    # Add the SDK Addons file to the archives.
    cp -f "${AAOS_SDK_ADDON_FILE}" "${output_dir}"
}

function create_devices_xml() {
    # Make a copy
    cp "${aaos_devices_file}" devices.xml

    # Device Name, ID and Manufaturer
    sed -i "s|<d:name>\(.*\)</d:name>|<d:name>Horizon SDV AAOS</d:name>|" devices.xml
    sed -i "s|<d:id>\(.*\)</d:id>|<d:id>horizon_sdv_aaos</d:id>|" devices.xml
    sed -i "s|<d:manufacturer>\(.*\)</d:manufacturer>|<d:manufacturer>Horizon SDV</d:manufacturer>|" devices.xml
    # Hardware
    sed -i "s|<d:screen-size>\(.*\)</d:screen-size>|<d:screen-size>normal</d:screen-size>|" devices.xml
    sed -i "s|<d:diagonal-length>\(.*\)</d:diagonal-length>|<d:diagonal-length>11.3</d:diagonal-length>|" devices.xml
    sed -i "s|<d:pixel-density>\(.*\)</d:pixel-density>|<d:pixel-density>mdpi</d:pixel-density>|" devices.xml
    sed -i "s|<d:x-dimension>\(.*\)</d:x-dimension>|<d:x-dimension>1152</d:x-dimension>|" devices.xml
    sed -i "s|<d:y-dimension>\(.*\)</d:y-dimension>|<d:y-dimension>1536</d:y-dimension>|" devices.xml
    sed -i "s|<d:xdpi>\(.*\)</d:xdpi>|<d:xdpi>180</d:xdpi>|" devices.xml
    sed -i "s|<d:ydpi>\(.*\)</d:ydpi>|<d:ydpi>180</d:ydpi>|" devices.xml
    # Update RAM and Storage (smaller)
    sed -i 's|<d:ram unit="KiB">[0-9]*</d:ram>|<d:ram unit="GiB">2</d:ram>|' devices.xml
    sed -i 's|<d:internal-storage unit="KiB">[0-9]*</d:internal-storage>|<d:internal-storage="GiB">2</d:internal-storage>|' devices.xml
    # Arch and API Level
    sed -i "/<d:abi>/,/<\/d:abi>/ s|x86_64|${aaos_arch}|" devices.xml
    sed -i "s|<d:api-level>[^<]*</d:api-level>|<d:api-level>${ANDROID_API_LEVEL}</d:api-level>|" devices.xml
}

# Add SDK addons to archives and devices.xml to system image zip
function update_archives() {
    for artifact in "${AAOS_ARTIFACT_LIST[@]}"; do
        for file in ${artifact}; do
            # Skip if file does not exist (e.g. glob did not expand; zip -u would create it)
            [ -f "${file}" ] || continue
            if [[ $(basename "${file}") =~ ^"${AAOS_SDK_SYSTEM_IMAGE_PREFIX}" ]]; then
                zip -u "${file}" devices.xml

                # Update SDK Addons envs with image details.
                avd_sha1="$(sha1sum "${file}" | awk '{print $1}')"
                avd_size="$(${STAT_CMD} "${file}")"
                avd_image_url="$(basename "${file}")"
                output_dir="$(dirname "${file}")"
                break
            fi
        done
    done
}

function main() {
    # Create the devices.xml file.
    create_devices_xml

    # Update the archives.
    update_archives

    # Create the SDK Addons file.
    create_sdk_addons
}


# Run only if target and arch are applicable.
case "${AAOS_LUNCH_TARGET}" in
    sdk_car*)
        echo "AAOS_LUNCH_TARGET=${AAOS_LUNCH_TARGET} supported."
        main
        ;;
    *)
        echo "NOOP: AAOS_LUNCH_TARGET=${AAOS_LUNCH_TARGET} ignored"
        ;;
esac

# If this fails, so be it.
exit 0
