#!/usr/bin/env python3
#
# Copyright (c) 2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#        http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# ruff: noqa: E501

"""Cloud Logging REST helpers for CVD/CTS ephemeral GCE guest app logs.

Uses the metadata bearer token (``CF_COMPUTE_REST_TOKEN``) and
``logging.googleapis.com/v2/entries:list`` — no ``google-cloud-logging`` SDK.

Guest stdout/stderr reach Cloud Logging when the VM runs the Google Cloud Ops
Agent and the guest startup script pipes output through ``systemd-cat`` (see
``cvd_argo_guest_startup.sh``).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

TOKEN_ENV = "CF_COMPUTE_REST_TOKEN"
CURSOR_PREFIX = "CVD_ARGO_CLOUD_LOG_CURSOR="

# Syslog identifier set by cvd_argo_guest_startup.sh (systemd-cat -t).
GUEST_SYSLOG_ID = "cvd-argo-guest"

# App log line prefixes written by guest scripts (serial + journal).
_APP_MARKERS = (
    "[cvd-argo-guest]",
    "[cvd-argo-remote]",
    "[cvd-argo]",
)

# Journal/syslog prefix before the app line (RFC3339 or classic syslog date).
_SYSLOG_GUEST_PREFIX = re.compile(
    r"^(?:\d{4}-\d{2}-\d{2}T[\d.:+-]+Z?\s+"
    r"|\w{3}\s+\d{1,2}\s+[\d:]{8}\s+)"
    r"\S+\s+cvd-argo-guest\[\d+\]:\s*",
)
_ANSI_ESCAPE = re.compile(r"\x1b\[[0-9;]*m|#033\[[0-9;]*m")
_OPS_AGENT_NOISE = re.compile(r"\[input:tail:|\[ info\] \[output:")


def _http_post_json(
    url: str,
    token: str,
    body: dict,
    *,
    timeout: int = 60,
) -> tuple[int, str]:
    ctx = ssl.create_default_context()
    data = json.dumps(body).encode()
    req = urllib.request.Request(url, method="POST", data=data)
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=timeout) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode() if e.fp else ""


def _format_guest_log_line(text: str) -> str:
    """Strip syslog wrapper and ANSI so cloud live logs match serial-style lines."""
    line = _ANSI_ESCAPE.sub("", text).strip()
    line = _SYSLOG_GUEST_PREFIX.sub("", line, count=1)
    return line.strip()


def _entry_text(entry: dict) -> str:
    tp = entry.get("textPayload")
    if isinstance(tp, str) and tp:
        return tp
    jp = entry.get("jsonPayload")
    if isinstance(jp, dict):
        for key in ("MESSAGE", "message", "msg"):
            val = jp.get(key)
            if isinstance(val, str) and val:
                return val
    return ""


def build_guest_app_log_filter(
    project: str,
    zone: str,
    vm_name: str,
    *,
    after_rfc3339: str | None = None,
) -> str:
    """Logging filter: one GCE instance, syslog/journal, CVD/CTS app markers only."""
    vm_name = vm_name.replace('"', '\\"')
    parts = [
        'resource.type="gce_instance"',
        'resource.labels.project_id="%s"' % project.replace('"', ""),
        'resource.labels.zone="%s"' % zone.replace('"', ""),
        'labels."compute.googleapis.com/resource_name"="%s"' % vm_name,
        "("
        + " OR ".join(
            'textPayload:"%s"' % m.replace('"', '\\"') for m in _APP_MARKERS
        )
        + ' OR jsonPayload.SYSLOG_IDENTIFIER="%s"'
        % GUEST_SYSLOG_ID
        + ' OR jsonPayload.MESSAGE:"cvd-argo"'
        + ' OR jsonPayload.message:"cvd-argo"'
        + ")",
    ]
    if after_rfc3339:
        parts.append('timestamp > "%s"' % after_rfc3339.replace('"', ""))
    return " AND ".join(parts)


def _list_guest_app_logs_page(
    project: str,
    filt: str,
    page_size: int,
    page_token: str | None,
    token: str,
) -> tuple[list[dict], str | None, int]:
    url = "https://logging.googleapis.com/v2/entries:list"
    body: dict = {
        "resourceNames": ["projects/" + project],
        "filter": filt,
        # asc often returns zero entries on the first page; desc works (API quirk).
        "orderBy": "timestamp desc",
        "pageSize": page_size,
    }
    if page_token:
        body["pageToken"] = page_token
    try:
        st, raw = _http_post_json(url, token, body, timeout=90)
    except OSError as e:
        sys.stderr.write("entries:list request failed: %s\n" % e)
        return [], None, 1
    if st != 200:
        sys.stderr.write("entries:list HTTP %s: %s\n" % (st, raw[:2000]))
        return [], None, 1
    try:
        data = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError as e:
        sys.stderr.write("entries:list JSON error: %s\n" % e)
        return [], None, 1
    entries = data.get("entries") or []
    if not isinstance(entries, list):
        entries = []
    next_token = data.get("nextPageToken")
    if not isinstance(next_token, str) or not next_token.strip():
        next_token = None
    return entries, next_token, 0


def _sort_entries_chronologically(entries: list[dict]) -> list[dict]:
    return sorted(
        (e for e in entries if isinstance(e, dict)),
        key=lambda e: e.get("timestamp") or "",
    )


def _print_log_records(entries: list[dict]) -> None:
    """Emit one JSON line per log entry for decode-guest-log-lines."""
    for entry in entries:
        if not isinstance(entry, dict):
            continue
        text = _entry_text(entry)
        if not text:
            continue
        print(
            json.dumps(
                {
                    "timestamp": entry.get("timestamp", ""),
                    "text": text,
                    "insertId": entry.get("insertId", ""),
                },
                ensure_ascii=False,
            )
        )


def cmd_list_guest_app_logs(
    project: str,
    zone: str,
    vm_name: str,
    after_rfc3339: str | None,
    page_size: int,
    page_token: str | None,
    all_pages: bool,
    token: str,
) -> int:
    """POST entries:list; stdout one JSON object per line: {timestamp,text,insertId}."""
    filt = build_guest_app_log_filter(
        project, zone, vm_name, after_rfc3339=after_rfc3339,
    )
    token_cursor = page_token
    collected: list[dict] = []
    while True:
        entries, next_token, rc = _list_guest_app_logs_page(
            project, filt, page_size, token_cursor, token,
        )
        if rc != 0:
            return rc
        if all_pages:
            collected.extend(entries)
        else:
            entries = _sort_entries_chronologically(entries)
        if not all_pages:
            _print_log_records(entries)
            if next_token:
                print(json.dumps({"nextPageToken": next_token}))
            return 0
        if not next_token:
            break
        token_cursor = next_token
    if all_pages:
        _print_log_records(_sort_entries_chronologically(collected))
    return 0


def cmd_decode_guest_log_lines() -> int:
    """Read list-guest-app-logs JSON on stdin; emit [guest] lines and a cursor on stdout.

    Last stdout line is ``CVD_ARGO_CLOUD_LOG_CURSOR=<rfc3339>`` for incremental polling.
    Dedupes by insertId and normalized line text within one poll batch.
    """
    last_ts = ""
    seen_ids: set[str] = set()
    seen_lines: set[str] = set()
    for raw in sys.stdin:
        line = raw.strip()
        if not line or '"nextPageToken"' in line:
            continue
        try:
            data = json.loads(line)
        except json.JSONDecodeError:
            continue
        text = data.get("text", "")
        ts = data.get("timestamp", "")
        insert_id = data.get("insertId", "")
        if not isinstance(text, str) or not text:
            continue
        if isinstance(insert_id, str) and insert_id:
            if insert_id in seen_ids:
                continue
            seen_ids.add(insert_id)
        for part in text.splitlines():
            formatted = _format_guest_log_line(part)
            if not formatted or _OPS_AGENT_NOISE.search(formatted):
                continue
            if formatted in seen_lines:
                continue
            seen_lines.add(formatted)
            print("[guest] %s" % formatted, flush=True)
        if isinstance(ts, str) and ts:
            last_ts = ts
    if last_ts:
        print("%s%s" % (CURSOR_PREFIX, last_ts), flush=True)
    return 0


def _token() -> str | None:
    t = os.environ.get(TOKEN_ENV, "").strip()
    return t or None


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Cloud Logging REST (metadata bearer token).",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser(
        "list-guest-app-logs",
        help=(
            "entries:list for one ephemeral GCE instance; app markers only "
            "(stdout/stderr via Ops Agent + journal)."
        ),
    )
    p.add_argument("project")
    p.add_argument("zone")
    p.add_argument(
        "vm_name",
        help="GCE instance name (KCC ComputeInstance resourceID / CVD_ARGO_VM_NAME).",
    )
    p.add_argument(
        "--after",
        default="",
        help="RFC3339 timestamp; only entries strictly after this time.",
    )
    p.add_argument("--page-size", type=int, default=100)
    p.add_argument("--page-token", default="")
    p.add_argument(
        "--all-pages",
        action="store_true",
        help="Drain nextPageToken until empty (flush at workflow end).",
    )

    sub.add_parser(
        "decode-guest-log-lines",
        help=(
            "Parse list-guest-app-logs JSON from stdin; emit [guest] lines on stdout; "
            "last line is CVD_ARGO_CLOUD_LOG_CURSOR=<timestamp>."
        ),
    )

    args = parser.parse_args(argv)
    if args.command == "decode-guest-log-lines":
        return cmd_decode_guest_log_lines()
    token = _token()
    if not token:
        sys.stderr.write("%s is not set or empty\n" % TOKEN_ENV)
        return 1
    if args.command == "list-guest-app-logs":
        after = args.after.strip() or None
        page_token = args.page_token.strip() or None
        return cmd_list_guest_app_logs(
            args.project,
            args.zone,
            args.vm_name,
            after,
            args.page_size,
            page_token,
            args.all_pages,
            token,
        )
    return 1


if __name__ == "__main__":
    sys.exit(main())
