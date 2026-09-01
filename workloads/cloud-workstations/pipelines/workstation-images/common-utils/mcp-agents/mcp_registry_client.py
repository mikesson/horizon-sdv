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
Shared MCP Gateway Registry client for Horizon agentic-AI tooling.

Provides Keycloak authentication, a single shared token cache, registry
sync, a background daemon, and an MCP client bridge. Product front-ends
(Gemini CLI, Antigravity) supply an ``AgentProfile`` that only declares
product-specific config paths and writers; all auth and transport logic
lives here so a single Horizon MCP onboarding is reused across products.
"""

import argparse
import contextlib
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, List

import requests
from requests import HTTPError
from dotenv import load_dotenv


# --- Load Environment Variables ---
def load_env_config():
    """
    Loads variables from ENV_FILE_PATH into os.environ.
    Search Hierarchy (first match returns):
    1. ENV_FILE_PATH (if set in terminal)
    2. ./.env (current working directory)
    3. ~/.gemini/.env (global fallback, retained for backward compatibility)
    """
    # override=False ensures that if you already 'export' a var in the terminal,
    # the .env file will NOT overwrite it.
    explicit_path_str = os.environ.get("ENV_FILE_PATH")
    if explicit_path_str:
        explicit_path = Path(explicit_path_str)
        if explicit_path.exists():
            return load_dotenv(dotenv_path=explicit_path, override=False)

    cwd_env = Path.cwd() / ".env"
    if cwd_env.exists():
        return load_dotenv(dotenv_path=cwd_env, override=False)

    home_env = Path.home() / ".gemini" / ".env"
    if home_env.exists():
        return load_dotenv(dotenv_path=home_env, override=False)


load_env_config()


# --- Constants ---
HORIZON_DOMAIN = os.environ.get("HORIZON_DOMAIN", "##DOMAIN##")  # e.g., myenv.horizon-sdv.com

# Keycloak
KEYCLOAK_URL = os.environ.get("KEYCLOAK_URL", f"https://{HORIZON_DOMAIN}/auth")
REALM = os.environ.get("REALM", "horizon")
CLIENT_ID = os.environ.get("CLIENT_ID", "mcp-gateway-registry-cli")
OIDC_URL = f"{KEYCLOAK_URL}/realms/{REALM}/protocol/openid-connect"
DEVICE_AUTH_URL = f"{OIDC_URL}/auth/device"
TOKEN_URL = f"{OIDC_URL}/token"

# MCP registry
MCP_REGISTRY_URL = os.environ.get("MCP_REGISTRY_URL", f"https://mcp.{HORIZON_DOMAIN}")

# Shared state home. Defaults to ~/.gemini (and honours legacy GEMINI_CONFIG_HOME)
# so a single onboarding / token cache is reused across all products.
STATE_HOME = Path(
    os.environ.get(
        "MCP_GATEWAY_STATE_HOME",
        os.environ.get("GEMINI_CONFIG_HOME", str(Path.home() / ".gemini")),
    )
)
TOKEN_FILE = STATE_HOME / "mcp-gateway-registry-token.json"

EXPIRY_SAFETY_SECONDS = 60   # Refresh tokens this many seconds before expiry
REQUEST_TIMEOUT_SECONDS = 30  # Timeout for HTTP requests


# --- Agent profile ---
@dataclass
class AgentProfile:
    """Product-specific behavior; the core stays product-agnostic."""

    key: str                                  # short id, used in state/log filenames
    display_name: str                         # human name for logs/help
    agent_bin: Path                           # absolute path of the product CLI (used in bridge entries)
    description: str                          # argparse description
    ensure_configs: Callable[[], None]        # create product config files/dirs
    writers: List[Callable[[list, bool, bool], None]] = field(default_factory=list)
    bridge_fallback_files: List[Path] = field(default_factory=list)
    extra_bridge_env: dict = field(default_factory=dict)


def _daemon_state_file(profile: AgentProfile) -> Path:
    return STATE_HOME / f"mcp-gateway-sync-state-{profile.key}.json"


def _daemon_log_file(profile: AgentProfile) -> Path:
    return STATE_HOME / f"mcp-gateway-sync-{profile.key}.log"


def _ensure_state_home():
    STATE_HOME.mkdir(parents=True, exist_ok=True)


def apply_writers(profile: AgentProfile, servers, *, prune=False, force=False):
    """Invoke every registered product writer with the fetched servers."""
    for writer in profile.writers:
        writer(servers, prune, force)


# --- Authentication ---
def device_login():
    """Initiates Device Authorization Flow and polls for token."""
    print(f"[*] Initiating Device Auth with {CLIENT_ID}...")

    try:
        resp = requests.post(DEVICE_AUTH_URL, data={"client_id": CLIENT_ID}, timeout=REQUEST_TIMEOUT_SECONDS)
        resp.raise_for_status()
        device_data = resp.json()

        device_code = device_data["device_code"]
        verification_uri = device_data["verification_uri_complete"]
        interval = device_data.get("interval", 5)
        expires_in = device_data.get("expires_in", 300)

        print(f"\nPlease authenticate via browser:\n\n   {verification_uri}\n")
        print(f"Waiting for login... (Expires in {expires_in}s)")

        start_time = time.time()
        while time.time() - start_time < expires_in:
            time.sleep(interval)

            token_resp = requests.post(TOKEN_URL, data={
                "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
                "device_code": device_code,
                "client_id": CLIENT_ID
            }, timeout=REQUEST_TIMEOUT_SECONDS)

            if token_resp.status_code == 200:
                print("\n[+] Login Successful!")
                return token_resp.json()

            err = token_resp.json().get("error")
            if err == "authorization_pending":
                continue
            elif err == "slow_down":
                interval += 2
            else:
                raise RuntimeError(f"Device flow failed: {err}")
    except HTTPError as e:
        status = e.response.status_code if e.response is not None else None
        if status == 401:
            raise RuntimeError(
                "Device authorization failed. This may indicate an issue with the Keycloak server. "
                "Please try again or contact your administrator."
            )
        elif status == 403:
            raise RuntimeError(
                "Access forbidden. Your account may not have permission to use this service. "
                "Please contact your administrator."
            )
        elif status is not None:
            raise RuntimeError(f"Server error ({status}): {e.response.text}")
        else:
            raise RuntimeError(f"HTTP error during device authorization: {e}")
    except requests.exceptions.ConnectionError:
        raise RuntimeError(f"Cannot connect to {DEVICE_AUTH_URL}. Check network/DNS.")
    except requests.exceptions.Timeout:
        raise RuntimeError("Device flow request timed out.")
    except requests.exceptions.RequestException as e:
        raise RuntimeError(f"Login failed: {e}")


def refresh_access_token(refresh_token):
    """Exchange a refresh token for a new access token pair."""
    try:
        resp = requests.post(TOKEN_URL, data={
            "grant_type": "refresh_token",
            "refresh_token": refresh_token,
            "client_id": CLIENT_ID
        }, timeout=REQUEST_TIMEOUT_SECONDS)
        resp.raise_for_status()
        return resp.json()
    except HTTPError as e:
        status = e.response.status_code if e.response is not None else None
        if status == 401:
            raise RuntimeError(
                "Refresh token expired or revoked. Please run --login to re-authenticate."
            )
        elif status == 403:
            raise RuntimeError(
                "Access forbidden. Your account may not have permission to access this service. "
                "Please contact your administrator."
            )
        elif status is not None:
            raise RuntimeError(
                f"Token refresh failed. Server error ({status}): {e.response.text}. "
                "Please run --login to re-authenticate."
            )
        else:
            raise RuntimeError(
                f"Token refresh failed due to HTTP error: {e}. "
                "Please run --login to re-authenticate."
            )
    except requests.exceptions.ConnectionError:
        raise RuntimeError(f"Cannot connect to {TOKEN_URL}. Check network/DNS.")
    except requests.exceptions.Timeout:
        raise RuntimeError("Token refresh request timed out.")
    except requests.exceptions.RequestException as e:
        raise RuntimeError(f"Token refresh failed: {e}")


def fetch_mcp_servers(access_token):
    """Fetch the registry server catalog using the current access token."""
    headers = {"Authorization": f"Bearer {access_token}"}
    resp = requests.get(f"{MCP_REGISTRY_URL}/api/servers", headers=headers, timeout=REQUEST_TIMEOUT_SECONDS)
    resp.raise_for_status()
    data = resp.json()
    return data.get("servers", [])


def save_token_file(token_data):
    """
    Save token file atomically to prevent corruption.

    1. Write to a temporary file first
    2. If successful, replace the real file
    3. If a crash happens, the temp file is abandoned (real file untouched)
    """
    _ensure_state_home()
    token_data = dict(token_data)
    token_data["obtained_at"] = int(time.time())

    fd, temp_path = tempfile.mkstemp(dir=STATE_HOME, prefix=".token-temp-", suffix=".json")
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(token_data, f, indent=2)
        shutil.move(temp_path, TOKEN_FILE)
    except Exception:
        try:
            os.unlink(temp_path)
        except OSError:
            pass
        raise


def load_token_file():
    """Read token JSON from disk and return it as a dict."""
    with open(TOKEN_FILE, 'r') as f:
        return json.load(f)


def is_access_token_fresh(token_data, *, safety_seconds=EXPIRY_SAFETY_SECONDS):
    """Checks if access token is still valid with a safety margin."""
    access_token = token_data.get("access_token")
    expires_in = token_data.get("expires_in")
    obtained_at = token_data.get("obtained_at")

    if not access_token or not isinstance(expires_in, (int, float)) or not isinstance(obtained_at, (int, float)):
        return False

    expires_at = int(obtained_at) + int(expires_in)
    now = int(time.time())
    return now < (expires_at - int(safety_seconds))


@contextlib.contextmanager
def _token_lock():
    """
    Best-effort cross-process exclusive lock around the read-refresh-write of the
    shared token file. Because a single token cache is reused across products
    (Gemini + Antigravity) and multiple sync loops may run, this serializes
    refreshes so two processes cannot each rotate (and thereby invalidate) the
    shared Keycloak refresh token. Degrades to a no-op if locking is unavailable.
    """
    _ensure_state_home()
    lock_path = STATE_HOME / ".mcp-gateway-token.lock"
    handle = None
    locked = False
    try:
        handle = open(lock_path, "a+")
        try:
            if sys.platform.startswith("win"):
                import msvcrt
                handle.seek(0)
                msvcrt.locking(handle.fileno(), msvcrt.LK_LOCK, 1)
            else:
                import fcntl
                fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
            locked = True
        except Exception:
            locked = False  # degrade gracefully; refresh still works, just unsynchronized
        yield
    finally:
        if handle is not None:
            try:
                if locked:
                    if sys.platform.startswith("win"):
                        import msvcrt
                        handle.seek(0)
                        msvcrt.locking(handle.fileno(), msvcrt.LK_UNLCK, 1)
                    else:
                        import fcntl
                        fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
            except Exception:
                pass
            handle.close()


def get_fresh_access_token_locked():
    """
    Return a valid access token, coalescing refreshes across processes.

    Under an exclusive lock: reload the token from disk; if another process has
    already refreshed it (now fresh), reuse it; otherwise refresh once and
    persist. Raises on refresh failure; returns None when no usable token exists.
    """
    with _token_lock():
        if not TOKEN_FILE.exists():
            return None

        try:
            token_data = load_token_file()
        except Exception:
            return None

        # Re-check under lock: a peer may have refreshed while we waited.
        if is_access_token_fresh(token_data):
            return token_data.get("access_token")

        refresh_token = token_data.get("refresh_token")
        if not refresh_token:
            return None

        new_tokens = refresh_access_token(refresh_token)
        save_token_file(new_tokens)
        return new_tokens.get("access_token")


def try_get_access_token_noninteractive():
    """Tries to get access token from the existing token file, refreshing if needed."""
    try:
        return get_fresh_access_token_locked()
    except Exception:
        return None


def get_access_token_interactive_fallback():
    """Gets access token, trying the existing session first, else interactive login."""
    access = try_get_access_token_noninteractive()
    if access:
        print("[*] Using existing session (no browser login needed).")
        return access

    token_data = device_login()
    save_token_file(token_data)
    return token_data["access_token"]


# --- Registry entry helpers ---
def build_bridge_env_payload(profile: AgentProfile):
    """Shared environment payload for bridge-based MCP server entries."""
    payload = {
        "HORIZON_DOMAIN": HORIZON_DOMAIN,
        "KEYCLOAK_URL": KEYCLOAK_URL,
        "REALM": REALM,
        "CLIENT_ID": CLIENT_ID,
        "MCP_REGISTRY_URL": MCP_REGISTRY_URL,
        "MCP_GATEWAY_STATE_HOME": str(STATE_HOME),
        # Marker to safely identify and prune only registry-managed entries.
        "MCP_GATEWAY_REGISTRY_MANAGED": "1",
    }
    payload.update(profile.extra_bridge_env)
    return payload


def build_bridge_server_entry(profile: AgentProfile, server_name, server_http_url):
    """
    Build a command-mode MCP entry that re-invokes the product agent in bridge
    mode. This avoids storing short-lived access tokens in cached config files.
    """
    server_env = build_bridge_env_payload(profile)
    server_env["httpUrl"] = server_http_url

    return {
        "command": sys.executable,
        "args": [str(profile.agent_bin), "--mcp-client-bridge", "--mcp-server", server_name],
        "env": server_env,
    }


def get_entry_http_url(entry):
    """Return MCP URL from either direct httpUrl or env.httpUrl entry formats."""
    if not isinstance(entry, dict):
        return ""

    direct_url = entry.get("httpUrl")
    if isinstance(direct_url, str) and direct_url.strip():
        return direct_url.strip()

    env_payload = entry.get("env")
    if isinstance(env_payload, dict):
        env_url = env_payload.get("httpUrl")
        if isinstance(env_url, str) and env_url.strip():
            return env_url.strip()

    return ""


def is_managed_server(server_http_url, registry_base_url):
    """
    Check if a server entry is managed by the MCP Gateway Registry.
    Managed servers are identified by their URL pointing to the registry.
    """
    if not isinstance(server_http_url, str) or not server_http_url.strip():
        return False

    normalized_url = server_http_url.rstrip("/")
    normalized_base = registry_base_url.rstrip("/")
    return normalized_url.startswith(normalized_base)


def is_registry_managed_entry(entry, registry_base_url):
    """Detect registry-managed entries across old and new entry shapes."""
    if not isinstance(entry, dict):
        return False

    env_payload = entry.get("env", {})
    if isinstance(env_payload, dict):
        managed_flag = str(env_payload.get("MCP_GATEWAY_REGISTRY_MANAGED", "")).strip().lower()
        if managed_flag in {"1", "true", "yes"}:
            return True

    # Backward compatibility: older entries may not have the marker flag.
    return is_managed_server(get_entry_http_url(entry), registry_base_url)


def sync_mcp_servers_file(profile: AgentProfile, config_path: Path, servers, *, prune=False, force=False):
    """
    Sync registry-managed MCP entries into a ``{ "mcpServers": {...} }`` JSON
    file. Uses command-mode bridge entries so tokens are not persisted here.
    Works for Gemini CLI ``settings.json``, Android Studio ``mcp.json`` and
    Antigravity ``mcp_config.json`` alike.
    """
    config_path = Path(config_path)
    try:
        with open(config_path, 'r') as f:
            config = json.load(f)
    except (OSError, json.JSONDecodeError):
        config = {}

    if not isinstance(config, dict):
        config = {}

    if force:
        config["mcpServers"] = {}

    if "mcpServers" not in config or not isinstance(config["mcpServers"], dict):
        config["mcpServers"] = {}

    existing = config["mcpServers"]
    base = MCP_REGISTRY_URL.rstrip("/")

    desired_names = set()
    for server in servers:
        s_name = server.get("display_name")
        s_path = server.get("path")
        if not s_name or not s_path:
            print(f"[!] Skipping server with missing fields: {server}")
            continue

        desired_names.add(s_name)
        s_path = "/" + s_path.strip("/")
        s_url = f"{base}{s_path}/mcp"

        existing[s_name] = build_bridge_server_entry(profile, s_name, s_url)
        print(f"[+] Upserted MCP server (registry): {s_name}")

    if prune:
        to_delete = [
            name for name, entry in existing.items()
            if is_registry_managed_entry(entry, base) and name not in desired_names
        ]
        for name in to_delete:
            del existing[name]
            print(f"[-] Pruned MCP server (registry, no longer exists): {name}")

    config_path.parent.mkdir(parents=True, exist_ok=True)
    with open(config_path, 'w') as f:
        json.dump(config, f, indent=2)

    print(f"[*] Updated {config_path}.")


def fetch_servers_with_auto_retry(access_token):
    """Fetch servers, retrying once on 401 Unauthorized after refreshing token."""
    try:
        return fetch_mcp_servers(access_token)
    except HTTPError as e:
        status = e.response.status_code if e.response is not None else None
        if status == 401:
            print("[*] Token expired, attempting refresh...")
            new_access = try_get_access_token_noninteractive()
            if new_access and new_access != access_token:
                try:
                    return fetch_mcp_servers(new_access)
                except HTTPError as e2:
                    if e2.response and e2.response.status_code == 401:
                        raise RuntimeError(
                            "Authentication failed even after token refresh. "
                            "Please run --login to re-authenticate."
                        )
                    raise
            else:
                raise RuntimeError(
                    "Token expired and refresh failed. "
                    "Please run --login to re-authenticate."
                )
        elif status == 403:
            raise RuntimeError(
                "Access forbidden (403). Ensure your user has appropriate MCP Registry access permissions. "
                "If you recently changed permissions, please wait a few minutes and try again."
            )
        elif status is not None:
            raise RuntimeError(f"Server error ({status}): {e.response.text}")
        else:
            raise RuntimeError(f"HTTP error while fetching servers: {e}")
    except requests.exceptions.ConnectionError:
        raise RuntimeError(f"Cannot connect to {MCP_REGISTRY_URL}. Check network/DNS.")
    except requests.exceptions.Timeout:
        raise RuntimeError(f"Request to {MCP_REGISTRY_URL} timed out.")
    except requests.exceptions.JSONDecodeError:
        raise RuntimeError(
            "Registry returned invalid JSON. "
            "Server may be experiencing issues. Please try again later."
        )
    except requests.exceptions.RequestException as e:
        raise RuntimeError(f"Request failed: {e}")


# --- Cross platform (Win + Linux) daemon helpers ---
def _pid_is_running(pid: int) -> bool:
    """Return True when a process with this PID appears active on the current OS."""
    if pid <= 0:
        return False

    if sys.platform.startswith("win"):
        try:
            result = subprocess.run(
                ["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
                capture_output=True, text=True, check=False, timeout=5
            )
            return str(pid) in result.stdout
        except (FileNotFoundError, subprocess.TimeoutExpired):
            return False
    else:
        try:
            os.kill(pid, 0)
            return True
        except OSError:
            return False


def _read_daemon_state(profile: AgentProfile):
    """Load daemon state JSON; return None when missing or unreadable."""
    state_file = _daemon_state_file(profile)
    if not state_file.exists():
        return None
    try:
        with open(state_file, 'r', encoding='utf-8') as f:
            return json.load(f)
    except Exception:
        return None


def _write_daemon_state(profile: AgentProfile, pid: int, mode: str):
    """Write daemon state atomically using the temp file pattern."""
    _ensure_state_home()
    fd, temp_path = tempfile.mkstemp(dir=STATE_HOME, prefix=".state-temp-", suffix=".json")
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump({
                "pid": pid,
                "mode": mode,
                "profile": profile.key,
                "started_at": int(time.time()),
            }, f, indent=2)
        shutil.move(temp_path, _daemon_state_file(profile))
    except Exception:
        try:
            os.unlink(temp_path)
        except OSError:
            pass
        raise


def _clear_daemon_state(profile: AgentProfile):
    """Best-effort removal of the daemon state file."""
    try:
        _daemon_state_file(profile).unlink(missing_ok=True)
    except Exception:
        pass


def daemon_status(profile: AgentProfile):
    """Check if a sync loop (daemon or foreground) is running for this profile."""
    state = _read_daemon_state(profile)
    if not state:
        return False, None, None

    pid = int(state.get("pid", 0)) or None
    mode = state.get("mode")

    # State is per-profile; guard against a foreign file at the same path.
    if state.get("profile") != profile.key:
        return False, pid, mode

    if pid and _pid_is_running(pid):
        return True, pid, mode

    _clear_daemon_state(profile)
    return False, None, None


def daemon_stop(profile: AgentProfile):
    """Stop an active sync loop process (foreground/daemon) and clear state."""
    running, pid, mode = daemon_status(profile)
    if not pid:
        print("[*] Sync is not running.")
        return

    if not running:
        print(f"[*] Sync is not running (stale PID {pid}). Cleaning state.")
        _clear_daemon_state(profile)
        return

    print(f"[*] Stopping sync (mode={mode}, PID {pid})...")
    try:
        if sys.platform.startswith("win"):
            subprocess.run(["taskkill", "/PID", str(pid), "/F"], check=False, timeout=5)
        else:
            os.kill(pid, signal.SIGTERM)
            time.sleep(0.5)
            if _pid_is_running(pid):
                os.kill(pid, signal.SIGKILL)
    except subprocess.TimeoutExpired:
        print("[!] Warning: Stop command timed out")
    finally:
        _clear_daemon_state(profile)

    print("[*] Stop requested.")


def _start_detached_sync_process(profile: AgentProfile):
    """Start the product agent in --watch mode as a detached background process."""
    python_exe = str(Path(sys.executable).resolve())
    args = [python_exe, str(profile.agent_bin), "--watch"]

    if sys.platform.startswith("win"):
        if python_exe.lower().endswith("python.exe"):
            pythonw_exe = python_exe[:-10] + "pythonw.exe"
            if Path(pythonw_exe).exists():
                python_exe = pythonw_exe
                args[0] = python_exe

        creationflags = 0
        if hasattr(subprocess, "CREATE_NEW_PROCESS_GROUP"):
            creationflags |= subprocess.CREATE_NEW_PROCESS_GROUP
        if hasattr(subprocess, "DETACHED_PROCESS"):
            creationflags |= subprocess.DETACHED_PROCESS

        p = subprocess.Popen(
            args, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL, creationflags=creationflags
        )
        return p.pid

    p = subprocess.Popen(
        args, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL, close_fds=True, start_new_session=True
    )
    return p.pid


def ensure_no_parallel_sync(profile: AgentProfile):
    """Guard against running multiple sync loops for the same profile."""
    running, pid, mode = daemon_status(profile)
    if running:
        raise SystemExit(
            f"Sync is already running (mode={mode}, PID={pid}). "
            f"Stop it first. If it's a daemon, use the '--daemon-stop' option."
        )


def daemon_start(profile: AgentProfile):
    """Start daemon background sync with verification."""
    ensure_no_parallel_sync(profile)

    spawned_pid = _start_detached_sync_process(profile)
    print(f"[*] Started background sync (initial PID {spawned_pid}).")

    max_wait = 5
    start = time.time()
    while time.time() - start < max_wait:
        time.sleep(0.5)
        running, actual_pid, _mode = daemon_status(profile)
        if running and actual_pid:
            print(f"[*] Daemon confirmed running (PID {actual_pid}).")
            return

    print(f"[!] Warning: Could not confirm daemon started within {max_wait}s.")
    print(f"[!] Check {_daemon_log_file(profile)} for errors.")


def sync_loop(profile: AgentProfile, *, prune=False, force=False):
    """
    Foreground sync loop (refresh token + re-apply product writers).
    When started via --daemon-start, runs detached in the background.
    """
    ensure_no_parallel_sync(profile)

    mode = "foreground" if sys.stdin.isatty() else "daemon"
    _write_daemon_state(profile, os.getpid(), mode)

    log_handle = None
    if mode == "daemon":
        log_handle = open(_daemon_log_file(profile), "a", encoding="utf-8", buffering=1)
        sys.stdout = log_handle
        sys.stderr = log_handle
        print(f"\n[*] Daemon started at {time.strftime('%Y-%m-%d %H:%M:%S')}")

    try:
        token_data = load_token_file()
    except Exception:
        raise SystemExit(f"Token file not found/invalid: {TOKEN_FILE}. Run --login first.")

    try:
        while True:
            refresh_token = token_data.get("refresh_token")
            if not refresh_token:
                raise SystemExit("No refresh_token available; login again.")

            expires_in = token_data.get("expires_in", 300)
            sleep_time = max(10, int(expires_in) - EXPIRY_SAFETY_SECONDS)
            try:
                time.sleep(sleep_time)
            except KeyboardInterrupt:
                raise

            # Coalesced, cross-process-safe refresh: only rotates the shared
            # refresh token if a peer hasn't already done so this cycle.
            try:
                access_token = get_fresh_access_token_locked()
            except RuntimeError as e:
                raise SystemExit(
                    f"Token refresh failed: {e}. SSO session likely expired. "
                    "Please run --login to re-authenticate."
                )
            except Exception as e:
                raise SystemExit(f"Unexpected error during token refresh: {e}")

            if not access_token:
                raise SystemExit(
                    "Token refresh failed (no valid token available). "
                    "Please run --login to re-authenticate."
                )

            # Reload persisted token for the next cycle's timing/refresh_token.
            try:
                token_data = load_token_file()
            except Exception:
                pass

            servers = fetch_mcp_servers(access_token)
            if not servers:
                print(f"[!] No servers in registry at {time.strftime('%H:%M:%S')}")
            else:
                apply_writers(profile, servers, prune=prune, force=force)
    except KeyboardInterrupt:
        if mode == "foreground":
            print("\n[*] Sync stopped by user (Ctrl+C)")
    except Exception as e:
        print(f"[!] Unexpected error in sync loop: {e}")
        import traceback
        traceback.print_exc()
    finally:
        _clear_daemon_state(profile)
        if log_handle:
            try:
                log_handle.flush()
                log_handle.close()
                sys.stdout = sys.__stdout__
                sys.stderr = sys.__stderr__
            except Exception:
                pass


# --- MCP client bridge ---
def _load_server_entry_from_files(profile: AgentProfile, server_name):
    """Load a server entry by name from the profile's fallback config files."""
    for config_file in profile.bridge_fallback_files:
        if not config_file:
            continue
        try:
            with open(config_file, 'r') as f:
                config = json.load(f)
        except (OSError, json.JSONDecodeError):
            continue
        if not isinstance(config, dict):
            continue
        mcp_servers = config.get("mcpServers", {})
        if not isinstance(mcp_servers, dict):
            continue
        entry = mcp_servers.get(server_name)
        if isinstance(entry, dict):
            return entry
    return None


def _resolve_bridge_server_entry(profile: AgentProfile, server_name):
    """
    Resolve target server entry for bridge mode.
    Prefer entry data passed in process env (works with cached client configs),
    then fall back to the profile's config files for older entries.
    """
    env_http_url = os.environ.get("httpUrl")
    if isinstance(env_http_url, str) and env_http_url.strip():
        return {"httpUrl": env_http_url.strip()}

    server_entry = _load_server_entry_from_files(profile, server_name)
    if server_entry:
        return server_entry

    raise RuntimeError(
        f"Could not resolve MCP server '{server_name}'. "
        "Expected env.httpUrl or a matching entry in the product config files."
    )


def _get_bridge_access_token():
    """Get a fresh access token from local cache, refreshing non-interactively when needed."""
    access_token = try_get_access_token_noninteractive()
    if access_token:
        return access_token

    raise RuntimeError(
        f"No valid access token available in {TOKEN_FILE}. "
        "Run --login to re-authenticate."
    )


def _bridge_read_message(stdin_buffer):
    """
    Read a single JSON-RPC message from stdin.
    Supports:
    - MCP stdio framed mode: Content-Length headers + payload bytes
    - NDJSON mode: one JSON object per line (legacy IDE behavior)
    Returns (message_obj, io_mode) where io_mode is "framed" or "ndjson".
    Returns (None, None) on EOF.
    """
    while True:
        first_line = stdin_buffer.readline()
        if not first_line:
            return None, None

        if not first_line.strip():
            continue

        stripped = first_line.lstrip()
        if stripped.startswith(b"{") or stripped.startswith(b"["):
            try:
                return json.loads(first_line.decode("utf-8").strip()), "ndjson"
            except (UnicodeDecodeError, json.JSONDecodeError) as e:
                raise RuntimeError(f"Invalid NDJSON message: {e}")

        if b":" in first_line:
            headers = {}
            line = first_line
            while line and line.strip():
                try:
                    header_text = line.decode("ascii", errors="strict").strip()
                except UnicodeDecodeError:
                    raise RuntimeError("Invalid non-ASCII header in framed MCP message.")

                if ":" not in header_text:
                    raise RuntimeError(f"Malformed MCP header line: {header_text}")

                k, v = header_text.split(":", 1)
                headers[k.strip().lower()] = v.strip()
                line = stdin_buffer.readline()

            if "content-length" not in headers:
                raise RuntimeError("Missing Content-Length header in framed MCP message.")

            try:
                payload_len = int(headers["content-length"])
            except ValueError:
                raise RuntimeError(f"Invalid Content-Length value: {headers['content-length']}")

            if payload_len < 0:
                raise RuntimeError(f"Negative Content-Length is invalid: {payload_len}")

            payload = stdin_buffer.read(payload_len)
            if payload is None or len(payload) < payload_len:
                raise RuntimeError("Unexpected EOF while reading framed MCP payload.")

            try:
                return json.loads(payload.decode("utf-8")), "framed"
            except (UnicodeDecodeError, json.JSONDecodeError) as e:
                raise RuntimeError(f"Invalid JSON payload in framed MCP message: {e}")

        try:
            return json.loads(first_line.decode("utf-8").strip()), "ndjson"
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise RuntimeError("Could not parse MCP stdin as framed or NDJSON message.")


def _bridge_write_message(message, io_mode):
    """Write one MCP message to stdout in framed or NDJSON format."""
    payload = json.dumps(message)
    if io_mode == "framed":
        payload_bytes = payload.encode("utf-8")
        sys.stdout.buffer.write(f"Content-Length: {len(payload_bytes)}\r\n\r\n".encode("ascii"))
        sys.stdout.buffer.write(payload_bytes)
        sys.stdout.buffer.flush()
        return

    sys.stdout.write(payload + "\n")
    sys.stdout.flush()


def _is_valid_jsonrpc_id(value):
    """Validate reply id shape accepted by MCP clients (reject null / bool)."""
    return isinstance(value, (str, int, float)) and not isinstance(value, bool)


def _write_bridge_error(req_id, message, io_mode):
    """Emit a JSON-RPC error response when the request has a valid id."""
    if not _is_valid_jsonrpc_id(req_id):
        print(f"[bridge] Non-request error (no valid id): {message}", file=sys.stderr)
        return

    err = {"jsonrpc": "2.0", "id": req_id, "error": {"code": -32603, "message": message}}
    _bridge_write_message(err, io_mode)


def run_mcp_client_bridge(profile: AgentProfile, server_name):
    """
    Hidden bridge mode called by MCP clients that cache config files.
    Reads JSON-RPC requests from stdin and forwards them to the remote MCP server.
    Authentication is injected per request from TOKEN_FILE, not from client config.
    """
    session = requests.Session()
    server_issued_session_id = None
    io_mode = None

    try:
        while True:
            client_request_data = None
            try:
                client_request_data, detected_mode = _bridge_read_message(sys.stdin.buffer)
                if client_request_data is None:
                    break
                if not isinstance(client_request_data, dict):
                    raise RuntimeError("Invalid JSON-RPC message: expected a JSON object.")

                if not io_mode:
                    io_mode = detected_mode or "ndjson"

                server_entry = _resolve_bridge_server_entry(profile, server_name)
                base_url = get_entry_http_url(server_entry)
                if not base_url:
                    raise RuntimeError(
                        f"Server '{server_name}' has no httpUrl configured "
                        "in either entry.httpUrl or entry.env.httpUrl."
                    )
                # Security guard: inject registry token only for registry-managed server URLs.
                if not is_managed_server(base_url, MCP_REGISTRY_URL.rstrip("/")):
                    raise RuntimeError(
                        "Refusing token injection for non-registry MCP server URL. "
                        "This bridge mode is only for MCP Gateway Registry managed servers."
                    )

                connector = "&" if "?" in base_url else "?"
                headers = {}
                raw_headers = server_entry.get("headers", {})
                if isinstance(raw_headers, dict):
                    headers.update(raw_headers)
                headers.pop("Authorization", None)

                access_token = _get_bridge_access_token()
                headers["Authorization"] = f"Bearer {access_token}"
                current_session_id = server_issued_session_id if server_issued_session_id else str(uuid.uuid4())

                if server_issued_session_id:
                    target_url = f"{base_url}{connector}mcpSessionId={current_session_id}"
                    headers.update({"mcp-session-id": current_session_id, "X-Session-Id": current_session_id})
                else:
                    target_url = f"{base_url}{connector}sessionId={current_session_id}"
                    headers.update({"X-Session-Id": current_session_id})

                headers.update({
                    "Content-Type": "application/json",
                    "Accept": "application/json, text/event-stream"
                })

                response = session.post(
                    target_url, json=client_request_data, headers=headers,
                    timeout=REQUEST_TIMEOUT_SECONDS, stream=True
                )
                try:
                    # If auth raced token expiry, retry once with a freshly refreshed token.
                    if response.status_code == 401:
                        response.close()
                        retry_token = try_get_access_token_noninteractive()
                        if retry_token and retry_token != access_token:
                            headers["Authorization"] = f"Bearer {retry_token}"
                            response = session.post(
                                target_url, json=client_request_data, headers=headers,
                                timeout=REQUEST_TIMEOUT_SECONDS, stream=True
                            )

                    if response.headers.get("mcp-session-id"):
                        server_issued_session_id = response.headers.get("mcp-session-id")

                    if response.status_code == 200:
                        for chunk in response.iter_lines():
                            if not chunk:
                                continue
                            try:
                                line_text = chunk.decode('utf-8').strip()
                                if not line_text or line_text.startswith(':') or line_text.startswith('event:'):
                                    continue

                                payload = None
                                if line_text.startswith("data:"):
                                    payload = line_text[5:].strip()
                                elif line_text.startswith("{") or line_text.startswith("["):
                                    payload = line_text

                                if payload:
                                    parsed_payload = json.loads(payload)
                                    if not isinstance(parsed_payload, dict):
                                        continue
                                    _bridge_write_message(parsed_payload, io_mode)
                            except (json.JSONDecodeError, UnicodeDecodeError):
                                continue
                    else:
                        if response.status_code in [400, 401, 403]:
                            server_issued_session_id = None

                        response_text = response.text.strip() if response.text else ""
                        if response_text:
                            response_text = response_text[:300]
                            message = f"MCP Client Bridge Error {response.status_code}: {response_text}"
                        else:
                            message = f"MCP Client Bridge Error {response.status_code}"
                        _write_bridge_error(client_request_data.get("id"), message, io_mode)
                finally:
                    response.close()

            except Exception as e:
                req_id = client_request_data.get("id") if isinstance(client_request_data, dict) else None
                _write_bridge_error(req_id, str(e), io_mode or "ndjson")
    finally:
        session.close()


# --- CLI ---
def run_agent(profile: AgentProfile):
    """CLI entrypoint: parse args and dispatch to login/sync/daemon/bridge modes."""
    parser = argparse.ArgumentParser(
        description=profile.description,
        formatter_class=argparse.RawDescriptionHelpFormatter
    )

    primary_mode = parser.add_mutually_exclusive_group()
    primary_mode.add_argument(
        "--login", action="store_true",
        help="Default mode. Interactive login if needed. Reuses existing token/refresh token first to avoid repeated browser login."
    )
    primary_mode.add_argument(
        "--watch", action="store_true",
        help="Continuously sync server list and refresh token in foreground mode. Use Ctrl+C to stop."
    )
    primary_mode.add_argument(
        "--daemon-status", action="store_true",
        help="Show background sync status and exit."
    )
    primary_mode.add_argument(
        "--daemon-stop", action="store_true",
        help="Stop background sync and exit."
    )
    primary_mode.add_argument(
        "--mcp-client-bridge", action="store_true",
        help=argparse.SUPPRESS  # Hidden option for internal use only
    )

    parser.add_argument(
        "--mcp-server", action="store",
        help=argparse.SUPPRESS  # Hidden option for internal use only
    )

    parser.add_argument(
        "--daemon-start", action="store_true",
        help="Continuously sync server list and refresh token in (background) daemon mode."
    )

    config_mode = parser.add_mutually_exclusive_group()
    config_mode.add_argument(
        "--prune", action="store_true",
        help="Sync removes managed servers no longer in registry. Does not affect non-managed servers."
    )
    config_mode.add_argument(
        "--force", action="store_true",
        help="Sync replaces entire mcpServers block with only registry servers (more destructive, removes all non-managed servers)."
    )

    # Some MCP clients may append extra launcher args in command mode.
    if "--mcp-client-bridge" in sys.argv:
        args, _ = parser.parse_known_args()
    else:
        args = parser.parse_args()

    profile.ensure_configs()

    # --- Argument validation ---
    if (args.daemon_status or args.daemon_stop or args.watch) and args.daemon_start:
        parser.error("--daemon-start is not valid with options --watch, --daemon-status, or --daemon-stop.")
    if (args.daemon_status or args.daemon_stop) and (args.prune or args.force):
        parser.error("--prune and --force are not valid with options --daemon-status or --daemon-stop.")
    if args.mcp_client_bridge or args.mcp_server:
        is_valid_pair = args.mcp_client_bridge and args.mcp_server
        other_active_args = [
            arg for arg in vars(args)
            if arg not in ("mcp_client_bridge", "mcp_server")
            and getattr(args, arg) != parser.get_default(arg)
        ]
        if not is_valid_pair or other_active_args:
            parser.error("--mcp-client-bridge and --mcp-server must be used together and without any other options.")

    # Bridge mode can run from cached command entries even when this env var is not exported in shell.
    if (not args.mcp_client_bridge) and (not HORIZON_DOMAIN or HORIZON_DOMAIN == "##DOMAIN##"):
        raise SystemExit(
            "ERROR: HORIZON_DOMAIN environment variable not set.\n"
            "Example: export HORIZON_DOMAIN=myenv.horizon-sdv.com"
        )

    # --- Entry points ---
    if args.mcp_client_bridge:
        run_mcp_client_bridge(profile, args.mcp_server)
        return

    if args.daemon_status:
        running, pid, mode = daemon_status(profile)
        if running:
            print(f"[*] Sync is running (mode={mode}, PID {pid}).")
        else:
            print("[*] Sync is not running.")
        return

    if args.daemon_stop:
        daemon_stop(profile)
        return

    if args.watch:
        print("[*] Starting foreground continuous sync (Ctrl+C to stop)...")
        print(f"[*] Options: prune={args.prune}, force={args.force}")
        sync_loop(profile, prune=args.prune, force=args.force)
        return

    # Default flow
    if not args.login:
        args.login = True

    ensure_no_parallel_sync(profile)
    access_token = get_access_token_interactive_fallback()
    servers = fetch_servers_with_auto_retry(access_token)
    if not servers:
        raise RuntimeError(f"No servers returned from {MCP_REGISTRY_URL}/api/servers.")

    if args.force:
        print("[!] --force is set: this will replace the entire mcpServers block.")

    apply_writers(profile, servers, prune=args.prune, force=args.force)

    if args.daemon_start:
        daemon_start(profile)
        print("[*] Done.")
        return

    is_running, pid, mode = daemon_status(profile)
    if is_running:
        print(f"[*] Background sync is already active (PID {pid}, mode={mode}).")
    else:
        try:
            answer = input("Start background sync for this session now? (y/N): ").strip().lower()
            if answer in ("y", "yes"):
                daemon_start(profile)
            elif answer in ("n", "no", ""):
                print("[*] Background sync not started.")
        except KeyboardInterrupt:
            print("\n[*] Operation cancelled.")

    print("[*] Done.")
