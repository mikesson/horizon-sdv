#!/usr/bin/env python3
#
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
#
# Horizon API workflow steps via REST (stdlib Python), aligned with horizon-workflow-via-rest.sh
# and tools/horizon CLI behavior.
#
# Env: HORIZON_DOMAIN or HORIZON_API_BASE_URL; KEYCLOAK_BASE or HORIZON_DOMAIN for token;
#      KEYCLOAK_REALM (default horizon), KEYCLOAK_CLIENT_ID (default horizon-api-ci),
#      KEYCLOAK_CLIENT_SECRET or HORIZON_ACCESS_TOKEN;
#      WORKFLOW_MODULE (default sample), WORKFLOW_TEMPLATE, WORKFLOW_PARAMETERS_JSON.
# Optional tuning: RUNNING_POLL_LIMIT, WAIT_NEW_ATTEMPTS, WAIT_NEW_SLEEP, WAIT_TERMINAL_SECS,
# TERMINAL_POLL_INTERVAL, LOG_STREAM_MAX_SECS, LOG_WAIT_POD_SECS.

from __future__ import annotations

import json
import os
import re
import ssl
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.client import HTTPResponse
from typing import Any, Optional


def env_int(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        return int(raw)
    except ValueError:
        return default


RUNNING_POLL_LIMIT = env_int("RUNNING_POLL_LIMIT", 500)
WAIT_NEW_ATTEMPTS = env_int("WAIT_NEW_ATTEMPTS", 120)
WAIT_NEW_SLEEP = env_int("WAIT_NEW_SLEEP", 2)
WAIT_TERMINAL_SECS = env_int("WAIT_TERMINAL_SECS", 3600)
TERMINAL_POLL_INTERVAL = env_int("TERMINAL_POLL_INTERVAL", 5)
LOG_STREAM_MAX_SECS = env_int("LOG_STREAM_MAX_SECS", 7200)
LOG_WAIT_POD_SECS = env_int("LOG_WAIT_POD_SECS", 60)
KEYCLOAK_REALM = os.environ.get("KEYCLOAK_REALM", "horizon").strip() or "horizon"
KEYCLOAK_CLIENT_ID = os.environ.get("KEYCLOAK_CLIENT_ID", "horizon-api-ci").strip() or "horizon-api-ci"
WORKFLOW_MODULE = os.environ.get("WORKFLOW_MODULE", "sample").strip() or "sample"


def api_base() -> str:
    if os.environ.get("HORIZON_API_BASE_URL", "").strip():
        return os.environ["HORIZON_API_BASE_URL"].strip().rstrip("/")
    d = os.environ.get("HORIZON_DOMAIN", "").strip()
    if d:
        return f"https://{d}/horizon-api"
    print("horizon-workflow-via-rest.py: set HORIZON_API_BASE_URL or HORIZON_DOMAIN", file=sys.stderr)
    sys.exit(1)


def keycloak_base() -> str:
    if os.environ.get("KEYCLOAK_BASE", "").strip():
        return os.environ["KEYCLOAK_BASE"].strip().rstrip("/")
    d = os.environ.get("HORIZON_DOMAIN", "").strip()
    if d:
        return f"https://{d}/auth"
    print("horizon-workflow-via-rest.py: set KEYCLOAK_BASE or HORIZON_DOMAIN for token", file=sys.stderr)
    sys.exit(1)


class HorizonRestClient:
    def __init__(self) -> None:
        self._bearer: str = ""
        self._api = api_base()
        self._ctx = ssl.create_default_context()

    def refresh_bearer(self) -> None:
        tok = os.environ.get("HORIZON_ACCESS_TOKEN", "").strip()
        if tok:
            self._bearer = tok
            return
        token_url = f"{keycloak_base()}/realms/{KEYCLOAK_REALM}/protocol/openid-connect/token"
        secret = os.environ.get("KEYCLOAK_CLIENT_SECRET", "").strip()
        if not secret:
            print(
                "horizon-workflow-via-rest.py: set KEYCLOAK_CLIENT_SECRET or HORIZON_ACCESS_TOKEN",
                file=sys.stderr,
            )
            sys.exit(1)
        data = urllib.parse.urlencode(
            {
                "grant_type": "client_credentials",
                "client_id": KEYCLOAK_CLIENT_ID,
                "client_secret": secret,
            }
        ).encode("utf-8")
        req = urllib.request.Request(
            token_url,
            data=data,
            headers={"Content-Type": "application/x-www-form-urlencoded"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, context=self._ctx) as resp:
                body = json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            print(f"horizon-workflow-via-rest.py: token HTTP {e.code}: {e.read().decode('utf-8', errors='replace')}", file=sys.stderr)
            sys.exit(1)
        tok = body.get("access_token") or ""
        if not tok:
            print(f"horizon-workflow-via-rest.py: token error: {body}", file=sys.stderr)
            sys.exit(1)
        self._bearer = tok

    def _headers_json(self, extra: Optional[dict[str, str]] = None) -> dict[str, str]:
        h = {
            "Authorization": f"Bearer {self._bearer}",
            "Accept": "application/json",
        }
        if extra:
            h.update(extra)
        return h

    def http_get(self, path: str) -> str:
        return self._request_json("GET", path, None, None)

    def http_post_submit(self, path: str, body: str) -> str:
        return self._request_json(
            "POST",
            path,
            body.encode("utf-8"),
            {
                "Content-Type": "application/json",
                "X-Horizon-Submitted-From": "rest-api",
            },
        )

    def _request_json(
        self,
        method: str,
        path: str,
        body: Optional[bytes],
        extra_headers: Optional[dict[str, str]],
    ) -> str:
        url = self._api + path
        env_token = bool(os.environ.get("HORIZON_ACCESS_TOKEN", "").strip())
        for attempt in range(2):
            if not self._bearer:
                self.refresh_bearer()
            headers = self._headers_json(extra_headers)
            if body is not None and "Content-Type" not in headers:
                headers["Content-Type"] = "application/json"
            req = urllib.request.Request(url, data=body, headers=headers, method=method)
            try:
                with urllib.request.urlopen(req, context=self._ctx) as resp:
                    return resp.read().decode("utf-8")
            except urllib.error.HTTPError as e:
                err_body = e.read().decode("utf-8", errors="replace")
                if e.code == 401 and attempt == 0 and not env_token:
                    self.refresh_bearer()
                    continue
                print(
                    f"horizon-workflow-via-rest.py: {method} {url} HTTP {e.code} {err_body}",
                    file=sys.stderr,
                )
                sys.exit(1)
        raise RuntimeError("request retry exhausted")

    @staticmethod
    def _terminal_phase(ph: str) -> bool:
        return ph in ("Succeeded", "Failed", "Error", "Aborted")

    def stream_log_lines(self, wf: str, stop: threading.Event) -> None:
        """Stream NDJSON log lines to stdout (formatted like bash); respects stop and max time."""
        enc = urllib.parse.quote(wf, safe="")
        phase = self.workflow_phase(wf)
        follow = "false" if self._terminal_phase(phase) else "true"
        path = f"/v1/workflows/{enc}/log?follow={follow}&container=main"
        url = self._api + path
        print(
            f"━━ Horizon log stream ━━ {url} (max-time={LOG_STREAM_MAX_SECS}s follow={follow} format=FORMATTED)",
            file=sys.stderr,
        )
        deadline = time.monotonic() + LOG_STREAM_MAX_SECS
        req = urllib.request.Request(
            url,
            headers={
                "Authorization": f"Bearer {self._bearer}",
                "Accept": "application/x-ndjson",
                "Accept-Encoding": "identity",
            },
            method="GET",
        )
        env_token = bool(os.environ.get("HORIZON_ACCESS_TOKEN", "").strip())
        resp: Optional[HTTPResponse] = None
        for attempt in range(2):
            try:
                resp = urllib.request.urlopen(req, context=self._ctx, timeout=300)
                break
            except urllib.error.HTTPError as e:
                _ = e.read()
                if e.code == 401 and attempt == 0 and not env_token:
                    self.refresh_bearer()
                    req = urllib.request.Request(
                        url,
                        headers={
                            "Authorization": f"Bearer {self._bearer}",
                            "Accept": "application/x-ndjson",
                            "Accept-Encoding": "identity",
                        },
                        method="GET",
                    )
                    continue
                raise
        if resp is None:
            return
        try:
            sk = getattr(resp, "socket", None)
            if sk is not None:
                try:
                    sk.settimeout(30.0)
                except OSError:
                    pass
            while not stop.is_set() and time.monotonic() < deadline:
                try:
                    line = resp.readline()
                except Exception:
                    break
                if stop.is_set():
                    break
                if not line:
                    break
                s = line.decode("utf-8", errors="replace").rstrip("\r\n")
                if not s.strip():
                    continue
                out = format_ndjson_line(s)
                if out is not None:
                    sys.stdout.write(out)
                    sys.stdout.flush()
        finally:
            resp.close()
        print("━━ log stream end ━━")

    def workflow_phase(self, wf: str) -> str:
        enc = urllib.parse.quote(wf, safe="")
        data = json.loads(self.http_get(f"/v1/workflows/{enc}"))
        return (data.get("phase") or "").strip()

    def has_pod_hint(self, wf: str) -> bool:
        enc = urllib.parse.quote(wf, safe="")
        data = json.loads(self.http_get(f"/v1/workflows/{enc}"))
        for n in data.get("nodes") or []:
            pn = n.get("podName")
            if isinstance(pn, str) and pn.strip():
                return True
        return False


def format_ndjson_line(line: str) -> Optional[str]:
    try:
        o = json.loads(line)
    except json.JSONDecodeError:
        return line + "\n"
    if not isinstance(o, dict):
        return line + "\n"
    if o.get("heartbeat"):
        return None
    if o.get("result") == "done":
        ws = o.get("workflowStatus") or ""
        reason = o.get("reason") or ""
        detail = o.get("detail") or ""
        stage = ws or reason or "done"
        msg = detail or reason or "-"
        return f"[{stage}] [-] [{_single_line(str(msg))}]\n"
    ts = o.get("ts") or "-"
    stage = (
        o.get("displayName")
        or o.get("templateName")
        or o.get("podName")
        or "log"
    )
    msg = o.get("msg")
    if msg is None:
        msg = ""
    return f"[{stage}] [{ts}] [{_single_line(str(msg))}]\n"


def _single_line(s: str) -> str:
    return re.sub(r"[\r\n]+", " ", s).strip()


def build_submit_body(catalog: dict[str, Any], user_raw: str, module: str, template: str) -> str:
    try:
        user = json.loads(user_raw) if user_raw.strip() else {}
    except json.JSONDecodeError as e:
        print(f"WORKFLOW_PARAMETERS_JSON: {e}", file=sys.stderr)
        sys.exit(1)
    if user is None:
        user = {}

    entry = None
    for e in catalog.get("entries") or []:
        if e.get("module") == module and e.get("templateName") == template:
            entry = e
            break
    if entry is None:
        print(f"catalog missing module={module} template={template}", file=sys.stderr)
        sys.exit(1)

    def string_param_empty(v: Any) -> bool:
        if v is None:
            return True
        if isinstance(v, str):
            return v == ""
        return False

    params_out: dict[str, Any] = {}
    for p in entry.get("parameters") or []:
        name = (p.get("name") or "").strip()
        if not name:
            continue
        default = p.get("default") or ""
        uv = user.get(name) if name in user else None
        user_set = name in user
        if not user_set:
            params_out[name] = default
        elif string_param_empty(uv):
            params_out[name] = default if default != "" else ""
        else:
            params_out[name] = uv

    out = {"parameters": {k: ("" if v is None else str(v)) for k, v in params_out.items()}}
    return json.dumps(out)


def phase_to_exit(ph: str) -> int:
    if ph == "Succeeded":
        return 0
    if ph == "Aborted":
        return 2
    if ph in ("Failed", "Error", ""):
        return 1
    return 1


def workflow_parameters_json_payload() -> str:
    path = os.environ.get("WORKFLOW_PARAMETERS_JSON_FILE", "").strip()
    if path and os.path.isfile(path):
        with open(path, encoding="utf-8") as f:
            raw = f.read()
    else:
        raw = os.environ.get("WORKFLOW_PARAMETERS_JSON", "{}")
    raw = raw.replace("\r\n", "\n").replace("\r", "\n")
    if raw.lstrip().startswith("\ufeff"):
        raw = raw.lstrip("\ufeff")
    return raw


def cmd_submit(c: HorizonRestClient) -> None:
    c.refresh_bearer()
    catalog = json.loads(c.http_get("/v1/catalog"))
    user_json = workflow_parameters_json_payload()
    template = os.environ.get("WORKFLOW_TEMPLATE", "").strip()
    if not template:
        print("horizon-workflow-via-rest.py: WORKFLOW_TEMPLATE is required", file=sys.stderr)
        sys.exit(1)
    body = build_submit_body(catalog, user_json, WORKFLOW_MODULE, template)
    mod_enc = urllib.parse.quote(WORKFLOW_MODULE, safe="")
    tpl_enc = urllib.parse.quote(template, safe="")
    path = f"/v1/modules/{mod_enc}/workflowTemplates/{tpl_enc}/submit"

    before = json.loads(c.http_get(f"/v1/workflows/running?limit={RUNNING_POLL_LIMIT}"))
    before_names = {item.get("name") for item in (before.get("items") or []) if item.get("name")}

    c.http_post_submit(path, body)

    wf = ""
    for _ in range(WAIT_NEW_ATTEMPTS):
        after = json.loads(c.http_get(f"/v1/workflows/running?limit={RUNNING_POLL_LIMIT}"))
        for item in after.get("items") or []:
            n = item.get("name")
            if n and n not in before_names:
                wf = n
                break
        if wf:
            break
        time.sleep(WAIT_NEW_SLEEP)
    if not wf:
        print("horizon-workflow-via-rest.py: no new workflow in running list (check Argo Events)", file=sys.stderr)
        sys.exit(1)
    sys.stdout.write(wf)


def wait_for_pod_hint_verbose(c: HorizonRestClient, wf: str) -> None:
    if LOG_WAIT_POD_SECS <= 0:
        print(f"Logs: opening live stream for workflow {wf} (pod wait disabled).", file=sys.stderr)
        return
    print(f"Logs: waiting for workflow pods (workflow {wf}, up to {LOG_WAIT_POD_SECS}s) …", file=sys.stderr)
    deadline = time.monotonic() + LOG_WAIT_POD_SECS
    while time.monotonic() < deadline:
        if c.has_pod_hint(wf):
            print("Logs: pods are visible; streaming output below.", file=sys.stderr)
            return
        time.sleep(2)
        print("Logs: still waiting for pod assignment …", file=sys.stderr)
    print("Logs: opening stream anyway (pods may appear shortly).", file=sys.stderr)


def cmd_wait_logs(c: HorizonRestClient, wf: str) -> None:
    c.refresh_bearer()
    stop = threading.Event()

    def bg() -> None:
        try:
            wait_for_pod_hint_verbose(c, wf)
            c.stream_log_lines(wf, stop)
        except Exception as e:
            print(f"logs: {e}", file=sys.stderr)

    t = threading.Thread(target=bg, daemon=True)
    t.start()

    deadline = time.monotonic() + WAIT_TERMINAL_SECS
    ph = ""
    while time.monotonic() < deadline:
        ph = c.workflow_phase(wf)
        if ph in ("Succeeded", "Failed", "Error", "Aborted"):
            break
        time.sleep(TERMINAL_POLL_INTERVAL)

    stop.set()
    t.join(timeout=30)

    if ph not in ("Succeeded", "Failed", "Error", "Aborted"):
        print(f"horizon-workflow-via-rest.py: timeout waiting for terminal phase on {wf}", file=sys.stderr)
        sys.exit(1)
    sys.exit(phase_to_exit(ph))


def cmd_show(c: HorizonRestClient, wf: str) -> None:
    c.refresh_bearer()
    enc = urllib.parse.quote(wf, safe="")
    raw = c.http_get(f"/v1/workflows/{enc}")
    data = json.loads(raw)
    summary = {
        k: data.get(k)
        for k in ("name", "phase", "workflowTemplate", "submittedFrom", "archivedLogs", "outputArtifacts")
        if k in data
    }
    print(json.dumps(summary, indent=2))
    print("━━ GCS / artifact URIs ━━")
    al = data.get("archivedLogs") or {}
    comb = al.get("combined") or {}
    if comb.get("gcsUri"):
        print(f"archivedLogs.combined: {comb['gcsUri']}")
    for step in al.get("steps") or []:
        uri = step.get("gcsUri")
        if uri:
            print(f"archivedLogs.step: {uri}")
    for art in data.get("outputArtifacts") or []:
        if isinstance(art, dict):
            print(f"outputArtifact: {art.get('name')} {art.get('gcsUri')}")


def cmd_abort(c: HorizonRestClient, wf: str) -> None:
    """Best-effort abort (matches horizon workflow abort || true)."""
    enc = urllib.parse.quote(wf, safe="")
    url = c._api + f"/v1/workflows/{enc}/abort"
    env_token = bool(os.environ.get("HORIZON_ACCESS_TOKEN", "").strip())
    for attempt in range(2):
        if not c._bearer:
            c.refresh_bearer()
        req = urllib.request.Request(
            url,
            method="POST",
            headers=c._headers_json({"Accept": "application/json"}),
        )
        try:
            urllib.request.urlopen(req, context=c._ctx)
            return
        except urllib.error.HTTPError as e:
            _ = e.read()
            if e.code == 401 and attempt == 0 and not env_token:
                c.refresh_bearer()
                continue
            return
        except Exception:
            return


def main() -> None:
    if len(sys.argv) < 2:
        print(
            "usage: horizon-workflow-via-rest.py submit | wait-logs <workflowName> | show <workflowName> | abort <workflowName>",
            file=sys.stderr,
        )
        sys.exit(2)
    cmd = sys.argv[1]
    c = HorizonRestClient()
    if cmd == "submit":
        cmd_submit(c)
    elif cmd == "wait-logs":
        if len(sys.argv) < 3:
            sys.exit(2)
        cmd_wait_logs(c, sys.argv[2])
    elif cmd == "show":
        if len(sys.argv) < 3:
            sys.exit(2)
        cmd_show(c, sys.argv[2])
    elif cmd == "abort":
        if len(sys.argv) < 3:
            sys.exit(2)
        cmd_abort(c, sys.argv[2])
    else:
        print(
            "usage: horizon-workflow-via-rest.py submit | wait-logs <name> | show <name> | abort <name>",
            file=sys.stderr,
        )
        sys.exit(2)


if __name__ == "__main__":
    main()
