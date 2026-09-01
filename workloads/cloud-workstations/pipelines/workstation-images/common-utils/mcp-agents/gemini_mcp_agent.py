#!/usr/bin/env python3

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

"""
Gemini MCP agent: thin front-end over mcp_registry_client.

Product responsibilities only:
  * Locate Gemini CLI settings.json and (optionally) Android Studio / ASfP mcp.json.
  * Register writers for those files.
All authentication, token cache, sync and bridge logic is shared in the core,
so this reuses the same Horizon MCP onboarding as the Antigravity agent.
"""

import glob
import json
import os
import sys
from pathlib import Path

# Make the shared core importable whether co-located (repo / same dir) or
# installed to a lib dir referenced by HORIZON_MCP_LIB / default location.
_HERE = os.path.dirname(os.path.abspath(__file__))
for _candidate in (_HERE, os.environ.get("HORIZON_MCP_LIB", "/usr/local/lib/horizon-mcp")):
    if _candidate and _candidate not in sys.path:
        sys.path.insert(0, _candidate)

try:
    import mcp_registry_client as core  # noqa: E402
except ModuleNotFoundError:
    sys.stderr.write(
        "ERROR: could not import 'mcp_registry_client'. Ensure it is co-located with "
        "this script or set HORIZON_MCP_LIB to the directory that contains it "
        "(default: /usr/local/lib/horizon-mcp).\n"
    )
    raise


GEMINI_CONFIG_HOME = Path(os.environ.get("GEMINI_CONFIG_HOME", Path.home() / ".gemini"))
GEMINI_CLI_SETTINGS_FILE = GEMINI_CONFIG_HOME / "settings.json"


def discover_android_studio_mcp_file_path():
    """
    Detect the IDE environment and return the appropriate mcp.json path.
    Returns None if no Android Studio environment is detected (Code-OSS or non-Android mode).
    Handles both Android Studio and Android Studio for Platform installations.
    """
    env_override = os.environ.get("ANDROID_STUDIO_MCP_FILE_PATH")
    if env_override:
        return Path(env_override)

    google_root = Path.home() / ".config/Google"
    if google_root.exists():
        configs = glob.glob(str(google_root / "AndroidStudio*"))
        if configs:
            configs.sort(reverse=True)
            return Path(configs[0]) / "mcp.json"

    return None


ANDROID_STUDIO_MCP_FILE_PATH = discover_android_studio_mcp_file_path()


def ensure_configs():
    """Create Gemini CLI settings.json and, if detected, the Android Studio mcp.json."""
    core.STATE_HOME.mkdir(parents=True, exist_ok=True)
    GEMINI_CONFIG_HOME.mkdir(parents=True, exist_ok=True)
    if not GEMINI_CLI_SETTINGS_FILE.exists():
        with open(GEMINI_CLI_SETTINGS_FILE, 'w') as f:
            json.dump({}, f, indent=2)
    if ANDROID_STUDIO_MCP_FILE_PATH:
        ANDROID_STUDIO_MCP_FILE_PATH.parent.mkdir(parents=True, exist_ok=True)
        if not ANDROID_STUDIO_MCP_FILE_PATH.exists():
            with open(ANDROID_STUDIO_MCP_FILE_PATH, 'w') as f:
                json.dump({}, f, indent=2)


def _write_gemini_settings(servers, prune, force):
    core.sync_mcp_servers_file(PROFILE, GEMINI_CLI_SETTINGS_FILE, servers, prune=prune, force=force)


def _write_android_studio(servers, prune, force):
    if ANDROID_STUDIO_MCP_FILE_PATH:
        core.sync_mcp_servers_file(PROFILE, ANDROID_STUDIO_MCP_FILE_PATH, servers, prune=prune, force=force)


DESCRIPTION = """
    Configures and synchronizes MCP servers for Gemini CLI and Gemini Code Assist.

    This tool simplifies using MCP (Model Context Protocol) servers by handling authentication and configuration for you.

    What it does:
      1. Authenticates: Connects to the MCP Gateway Registry using a browser-based login (Keycloak Device Flow).
      2. Fetches Config: Retrieves your authentication token (JWT) and the list of available MCP servers.
      3. Updates Tools:
        * Configures Gemini CLI by updating `~/.gemini/settings.json`.
        * Configures Gemini Code Assist in Android Studio and Android Studio for Platform (ASfP) when detected.
      4. Keeps you logged in: Can run continuously in the background to automatically refresh your token and server list.

    Special Note for Gemini clients:
    Some clients cache MCP config files. To avoid stale-token failures, registry-managed servers are configured through an
    MCP-client bridge command. The bridge forwards requests and injects a fresh authentication token from the shared token file on every request.
"""


PROFILE = core.AgentProfile(
    key="gemini",
    display_name="Gemini CLI / Code Assist",
    agent_bin=Path(os.environ.get("GEMINI_MCP_AGENT_PATH", os.path.abspath(__file__))),
    description=DESCRIPTION,
    ensure_configs=ensure_configs,
    writers=[_write_gemini_settings, _write_android_studio],
    bridge_fallback_files=[GEMINI_CLI_SETTINGS_FILE]
    + ([ANDROID_STUDIO_MCP_FILE_PATH] if ANDROID_STUDIO_MCP_FILE_PATH else []),
    extra_bridge_env={"GEMINI_CONFIG_HOME": str(GEMINI_CONFIG_HOME)},
)


if __name__ == "__main__":
    core.run_agent(PROFILE)
