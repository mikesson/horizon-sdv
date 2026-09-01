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
Antigravity MCP agent: thin front-end over mcp_registry_client.

Responsibilities are:
  * Locate the Antigravity mcp_config.json.
  * Register a writer for it.
It reuses the shared Keycloak/MCP Gateway Registry token cache, so if
Gemini agent's login is already run no separate onboarding is required.
"""

import json
import os
import sys
from pathlib import Path

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


# Antigravity global MCP config. Default per Antigravity docs (~/.gemini/config/mcp_config.json);
# override with ANTIGRAVITY_MCP_FILE_PATH for CLI (~/.gemini/antigravity-cli/mcp_config.json)
# or workspace (.agents/mcp_config.json) layouts.
ANTIGRAVITY_MCP_FILE = Path(
    os.environ.get(
        "ANTIGRAVITY_MCP_FILE_PATH",
        Path.home() / ".gemini" / "config" / "mcp_config.json",
    )
)


def ensure_configs():
    """Create the shared state home and the Antigravity mcp_config.json shell."""
    core.STATE_HOME.mkdir(parents=True, exist_ok=True)
    ANTIGRAVITY_MCP_FILE.parent.mkdir(parents=True, exist_ok=True)
    if not ANTIGRAVITY_MCP_FILE.exists():
        with open(ANTIGRAVITY_MCP_FILE, 'w') as f:
            json.dump({"mcpServers": {}}, f, indent=2)


def _write_antigravity(servers, prune, force):
    core.sync_mcp_servers_file(PROFILE, ANTIGRAVITY_MCP_FILE, servers, prune=prune, force=force)


DESCRIPTION = """
    Configures and synchronizes MCP servers for Antigravity (Agent and CLI).

    This tool reuses the Horizon MCP Gateway Registry login shared with the Gemini agent, so no separate onboarding is needed.

    What it does:
      1. Authenticates: Reuses the shared MCP Gateway Registry session (Keycloak Device Flow); only prompts if no valid token exists.
      2. Fetches Config: Retrieves the list of available MCP servers.
      3. Updates Tools: Configures Antigravity by updating `~/.gemini/config/mcp_config.json` (override via ANTIGRAVITY_MCP_FILE_PATH).
      4. Keeps you logged in: Can run continuously in the background to refresh your token and server list.

    Registry-managed servers are written as command-mode bridge entries so a fresh authentication token is injected on every request.
"""


PROFILE = core.AgentProfile(
    key="antigravity",
    display_name="Antigravity",
    agent_bin=Path(os.environ.get("ANTIGRAVITY_MCP_AGENT_PATH", os.path.abspath(__file__))),
    description=DESCRIPTION,
    ensure_configs=ensure_configs,
    writers=[_write_antigravity],
    bridge_fallback_files=[ANTIGRAVITY_MCP_FILE],
)


if __name__ == "__main__":
    core.run_agent(PROFILE)
