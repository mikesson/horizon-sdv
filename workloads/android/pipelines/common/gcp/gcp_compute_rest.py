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

"""GCP Compute + OS Login REST helpers (metadata bearer token only; no gcloud).

Used by ``cf_create_instance_template.sh`` (Packer publish, orphan disks).
CVD/CTS ephemeral VMs use KCC ``ComputeInstance`` CRs (see ``cvd_argo_gce_ephemeral.sh``);
VM status and serial console use Compute REST (``get-instance-status``,
``get-serial-port-output``), not ``gcloud compute``.
Canonical path:
``workloads/android/pipelines/common/gcp/gcp_compute_rest.py``.
``cf_instance_template/cf_compute_rest.py`` is a compatibility shim.

Original scope (cf_create_instance_template.sh)

Why this exists
    On GKE (Argo / workload identity), ``gcloud`` often uses Application Default
    Credentials that are not the same identity as the pod's metadata-server
    token. The shell therefore mints a **metadata** OAuth token with
    **cloud-platform** scope and passes it as ``CF_COMPUTE_REST_TOKEN``. This
    module never reads gcloud or ADC; it only uses ``urllib`` + that bearer
    token.

Scope
    **Compute Engine v1:** image read/delete (global ``images.get`` /
    ``images.delete``), instance template read (global then legacy regional ``GET``),
    global and regional instance template delete, zonal
    instance stop/delete, plus **orphan Packer builder disks** (zonal
    ``disks.list`` / ``disks.delete``; ``size_gb`` may be ``any``, ``*``, or ``all``
    to match every unattached ``packer-*`` disk in the zone).
    **Cloud OS Login v1:** prune SSH public keys on the token identity's login profile
    (same token; ``oslogin.googleapis.com``). Uses ``oauth2.googleapis.com/tokeninfo`` to
    resolve ``email`` and calls ``GET .../v1/users/{encoded_email}/loginProfile`` for
    workload-identity / service-account tokens; falls back to ``users/me`` when email
    cannot be resolved (end-user tokens). ``DELETE`` uses the same ``users/{encoded_email}``
    prefix (not the numeric ``users/{id}`` in each key's ``name`` field), or OS Login
    returns **403** for service-account tokens.
    Not a general GCP SDK replacement.

Auth
    Environment variable ``CF_COMPUTE_REST_TOKEN``: bearer access token. Set by
    the shell via ``gcp_metadata_access_token()`` before each invocation. Required
    for all subcommands except ``prune-os-login-ssh-keys`` when missing (that
    subcommand prints ``0`` and exits **0**).

Debug (image delete only)
    If ``COMPUTE_IMAGE_DELETE_DEBUG`` is ``true`` (case-insensitive),
    ``delete-global-image`` logs token **email** and **expires_in** from OAuth2
    ``tokeninfo`` on **stderr** before DELETE. Do not turn on in shared CI.

Subcommands (stdout / exit codes)
    ``delete-global-image <project> <image_name>``
        ``DELETE .../global/images/{name}`` (120s). If the JSON body looks like a
        long-running **Operation**, poll ``operations.get`` until DONE (120×2s,
        GET timeout 180s). Prints **one** stdout line for ``cf_create`` to parse;
        **always exit 0** (best-effort):

        - ``removed`` — success (including 204, empty body, or non-operation 200)
        - ``absent`` — HTTP 404
        - ``warn <status>`` — other non-success HTTP (e.g. ``warn 403``)
        - ``warn error`` — network failure before HTTP status
        - ``warn TIMEOUT`` — poll loop exhausted
        - ``warn OPERATION`` — operation DONE with error

    ``get-global-image <project> <image_name>``
        ``GET .../global/images/{name}`` (read-only). Exit **0** if HTTP **200**;
        exit **1** if **404** or other HTTP / transport failure (stderr). Used by
        ``cf_create_instance_template.sh`` stage **2** assert (no ``gcloud``).

    ``get-instance-template <project> <region> <template_name>``
        ``GET .../global/instanceTemplates/{name}``; if HTTP **404**,
        ``GET .../regions/{region}/instanceTemplates/{name}`` (legacy). Exit **0**
        if either returns **200** (stage **2** preflight: GCP template exists even
        when the KCC CR is missing). Exit **1** if neither exists or on transport /
        non-404 HTTP error.

    ``delete-instance-template <project> <template_name>``
        ``DELETE .../global/instanceTemplates/{name}``, poll Operation like above.
        Exit **0** on success or 404; exit **1** on HTTP error or failed/timed-out
        operation (details on stderr).

    ``delete-regional-instance-template <project> <region> <template_name>``
        ``DELETE .../regions/{region}/instanceTemplates/{name}``, poll Operation like
        global delete. Exit **0** on success or 404; exit **1** on HTTP error or
        failed/timed-out operation (KCC regional templates).

    ``zone-instance {stop|delete} <project> <zone> <instance_name>``
        ``POST .../instances/{name}/stop`` (JSON ``{}``) or ``DELETE`` instance.
        Poll up to 180 iterations (300s read timeout per GET). **Always exit 0**
        (legacy shell parity); stderr on failure.

    ``get-serial-port-output <project> <zone> <instance_name> [--port N] [--start B]``
        ``GET .../instances/{name}/serialPort`` (read-only; API method
        ``instances.getSerialPortOutput``). Prints one
        JSON object to stdout: ``{"contents": "...", "next": "..."}`` using the
        API ``next`` byte offset (pass as ``--start`` on the next call). Exit **0**
        on HTTP **200**; exit **1** on other HTTP / transport failure (stderr).
        Used by ``cvd_argo_gce_ephemeral.sh`` for live guest console logs.

    ``get-instance-status <project> <zone> <instance_name>``
        ``GET .../instances/{name}`` (read-only). Prints ``status`` (e.g.
        ``RUNNING``) to stdout on HTTP **200**. Exit **0** with empty stdout on
        **404** (instance not visible yet). Exit **1** on other HTTP / transport
        failure. Used by ``cvd_argo_gce_ephemeral.sh`` to wait for the zonal VM.

    ``list-orphan-packer-disks <project> <zone> <size_gb>``
        ``GET .../zones/{zone}/disks`` (paginated). Prints **one disk name per line**
        to stdout for disks where: name matches ``^packer-``, the disk is
        **unattached** (``users`` missing, ``null``, or ``[]`` per API; non-list
        ``users`` is treated as unknown and skipped), and ``sizeGb`` equals
        ``<size_gb>`` **unless** ``<size_gb>`` is ``any``, ``*``, or ``all`` (case
        insensitive), in which case every matching unattached ``packer-*`` disk is
        listed regardless of size. Exit **0** on success (including no matches); exit
        **1** on list HTTP/transport failure or if ``<size_gb>`` is not a valid
        integer and not one of those keywords.

    ``delete-zonal-disk <project> <zone> <disk_name>``
        ``DELETE .../zones/{zone}/disks/{name}``; poll zonal Operation until DONE.
        **Always exit 0** (best-effort); stderr on failure or 404.

    ``cleanup-orphan-packer-disks <project> <zone> <size_gb>`` *(convenience)*
        Runs the same filter as ``list-orphan-packer-disks`` and **delete-zonal-disk**
        for each match (including all sizes when ``<size_gb>`` is ``any``, ``*``, or
        ``all``). **Always exit 0** (best-effort for shell wrappers): invalid
        ``<size_gb>`` or list failures log to stderr and still exit **0**; stderr per
        disk on delete issues.

    ``prune-os-login-ssh-keys`` *(no extra args)*
        ``GET`` login profile for the bearer (``users/{tokeninfo.email}`` when
        ``tokeninfo`` returns ``email``, else ``users/me``), then ``DELETE`` each
        listed ``sshPublicKeys`` entry. Prints removed count to **stdout** (digits only).
        Exit **0** always (best-effort). If ``CF_COMPUTE_REST_TOKEN`` is missing, prints
        ``0`` and exits **0** (shell skips prune without failing the job).
"""

from __future__ import annotations

import argparse
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# Shell export name; must match cf_create_instance_template.sh /
# cf_environment.sh.
TOKEN_ENV = "CF_COMPUTE_REST_TOKEN"

# HTTP statuses retried when polling long-running Compute operations.
_TRANSIENT_OPERATION_HTTP = frozenset({429, 500, 502, 503, 504})

# ---------------------------------------------------------------------------
# Shared: HTTP, Compute Operation polling, token / OS Login URL helpers
# ---------------------------------------------------------------------------


def _http(
    method: str,
    url: str,
    token: str | None,
    *,
    data: bytes | None = None,
    timeout: int = 180,
) -> tuple[int, str]:
    """Return (HTTP status, response body text).

    HTTPError becomes (code, body); OSError propagates.
    """
    ctx = ssl.create_default_context()
    req = urllib.request.Request(url, method=method, data=data)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if method == "POST" and data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=timeout) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        body = e.read().decode() if e.fp else ""
        return e.code, body


def _access_token_email(token: str) -> str | None:
    """Return OAuth2 ``tokeninfo`` ``email`` for this access token, or None."""
    url = "https://oauth2.googleapis.com/tokeninfo?" + urllib.parse.urlencode(
        {"access_token": token},
    )
    try:
        st, body = _http("GET", url, None, timeout=15)
    except OSError:
        return None
    if st != 200 or not body.strip():
        return None
    try:
        d = json.loads(body)
        email = d.get("email")
        if isinstance(email, str) and "@" in email:
            return email
    except (json.JSONDecodeError, TypeError):
        return None
    return None


def _os_login_login_profile_url(token: str) -> str:
    """OS Login ``getLoginProfile`` URL for the identity behind ``token``.

    ``users/me`` only matches end-user credentials. GKE workload identity
    returns a service-account access token; OS Login requires ``users/{url-
    encoded email}``.
    """
    email = _access_token_email(token)
    if email:
        return (
            "https://oslogin.googleapis.com/v1/users/"
            + urllib.parse.quote(email, safe="")
            + "/loginProfile"
        )
    return "https://oslogin.googleapis.com/v1/users/me/loginProfile"


def _maybe_compute_image_delete_debug(token: str) -> None:
    """If COMPUTE_IMAGE_DELETE_DEBUG=true, stderr who the token belongs to
    (tokeninfo); never stdout."""
    if os.environ.get(
        "COMPUTE_IMAGE_DELETE_DEBUG",
            "").strip().lower() != "true":
        return
    url = "https://oauth2.googleapis.com/tokeninfo?" + urllib.parse.urlencode(
        {"access_token": token},
    )
    try:
        st, body = _http("GET", url, None, timeout=10)
    except OSError as e:
        sys.stderr.write(
            "COMPUTE_IMAGE_DELETE_DEBUG: tokeninfo request failed: %s\n" % e,
        )
        return
    if st != 200 or not body.strip():
        sys.stderr.write(
            "COMPUTE_IMAGE_DELETE_DEBUG: tokeninfo unavailable (HTTP %s)\n" %
            st, )
        return
    try:
        d = json.loads(body)
        sys.stderr.write(
            "COMPUTE_IMAGE_DELETE_DEBUG: tokeninfo email= %s expires_in= %s\n"
            % (d.get("email", "?"), d.get("expires_in", "?")),
        )
    except (json.JSONDecodeError, TypeError) as e:
        sys.stderr.write(
            "COMPUTE_IMAGE_DELETE_DEBUG: tokeninfo parse failed: %s\n" % e,
        )


def _wait_operation_done(
    op: dict,
    token: str,
    project: str,
    *,
    max_iterations: int,
    sleep_s: float = 2.0,
    get_timeout: int = 180,
) -> tuple[bool, str]:
    """Poll a Compute Operation until status DONE or timeout.

    Uses ``selfLink`` from the operation JSON when present; otherwise builds
    ``.../zones|regions|global/operations/{name}`` from ``zone`` / ``region`` /
    ``name`` fields (Compute returns partial URLs).
    """
    for _ in range(max_iterations):
        st = op.get("status")
        if st == "DONE":
            if op.get("error"):
                return False, json.dumps(op.get("error"))
            return True, ""
        time.sleep(sleep_s)

        op_url = op.get("selfLink")
        if isinstance(op_url, str) and op_url:
            pass
        else:
            name = op.get("name")
            if not name:
                return False, "operation missing name in %s" % json.dumps(op)[
                    :500]

            zone = op.get("zone")
            region = op.get("region")
            if isinstance(zone, str) and zone:
                zone_name = zone.rstrip("/").split("/")[-1]
                op_url = (
                    "https://compute.googleapis.com/compute/v1/projects/"
                    + project
                    + "/zones/"
                    + zone_name
                    + "/operations/"
                    + name
                )
            elif isinstance(region, str) and region:
                region_name = region.rstrip("/").split("/")[-1]
                op_url = (
                    "https://compute.googleapis.com/compute/v1/projects/"
                    + project
                    + "/regions/"
                    + region_name
                    + "/operations/"
                    + name
                )
            else:
                op_url = (
                    "https://compute.googleapis.com/compute/v1/projects/"
                    + project
                    + "/global/operations/"
                    + name
                )

        transient_attempts = 0
        max_transient = 8
        while True:
            try:
                st2, body2 = _http("GET", op_url, token, timeout=get_timeout)
            except OSError as e:
                transient_attempts += 1
                if transient_attempts > max_transient:
                    return False, "operations.get failed: %s" % e
                delay = min(2.0 * (2**(transient_attempts - 1)), 30.0)
                sys.stderr.write(
                    "operations.get network error (transient): %s; "
                    "retry %s/%s in %.0fs\n" %
                    (e, transient_attempts, max_transient, delay),
                )
                time.sleep(delay)
                continue
            if st2 == 200:
                op = json.loads(body2) if body2.strip() else {}
                break
            if (st2 in _TRANSIENT_OPERATION_HTTP
                    and transient_attempts < max_transient):
                transient_attempts += 1
                delay = min(2.0 * (2**(transient_attempts - 1)), 30.0)
                sys.stderr.write(
                    "operations.get HTTP %s (transient); "
                    "retry %s/%s in %.0fs\n" %
                    (st2, transient_attempts, max_transient, delay),
                )
                time.sleep(delay)
                continue
            return False, "operations.get HTTP %s" % st2
    return False, "operation timeout"


def _looks_like_compute_operation(d: object) -> bool:
    """True if JSON looks like a v1 Operation (kind/name/status heuristics)."""
    if not isinstance(d, dict):
        return False
    kind = d.get("kind", "")
    if isinstance(kind, str) and "operation" in kind.lower():
        return True
    return isinstance(d.get("name"), str) and "status" in d


# ---------------------------------------------------------------------------
# CF instance template: global images and instance templates
# (cf_create_instance_template.sh — Packer publish / stage 2 assert / delete)
# ---------------------------------------------------------------------------


def cmd_get_global_image(project: str, image_name: str, token: str) -> int:
    """GET global image; exit 0 only on HTTP 200 (for stage-2 assert)."""
    url = (
        "https://compute.googleapis.com/compute/v1/projects/"
        + project
        + "/global/images/"
        + image_name
    )
    try:
        st, body = _http("GET", url, token, timeout=60)
    except OSError as e:
        sys.stderr.write("get global image request failed: %s\n" % e)
        return 1
    if st == 200:
        return 0
    if st == 404:
        sys.stderr.write(
            "images.get %s/%s: not found (HTTP 404)\n" % (project, image_name),
        )
        return 1
    sys.stderr.write("images.get HTTP %s: %s\n" % (st, body[:1200]))
    return 1


def _global_instance_template_url(project: str, template_name: str) -> str:
    return (
        "https://compute.googleapis.com/compute/v1/projects/"
        + project
        + "/global/instanceTemplates/"
        + template_name
    )


def _regional_instance_template_url(
        project: str,
        region: str,
        template_name: str) -> str:
    return (
        "https://compute.googleapis.com/compute/v1/projects/"
        + project
        + "/regions/"
        + region
        + "/instanceTemplates/"
        + template_name
    )


def cmd_get_instance_template(
        project: str,
        region: str,
        template_name: str,
        token: str) -> int:
    """GET global then regional instanceTemplates/{name}; exit 0 if either is
    HTTP 200."""
    global_url = _global_instance_template_url(project, template_name)
    try:
        st_g, body_g = _http("GET", global_url, token, timeout=60)
    except OSError as e:
        sys.stderr.write(
            "get global instance template request failed: %s\n" %
            e)
        return 1
    if st_g == 200:
        return 0
    if st_g != 404:
        sys.stderr.write(
            "instanceTemplates.get (global) HTTP %s: %s\n" % (
                st_g, body_g[:1200]),
        )
        return 1

    regional_url = _regional_instance_template_url(
        project, region, template_name)
    try:
        st, body = _http("GET", regional_url, token, timeout=60)
    except OSError as e:
        sys.stderr.write(
            "get regional instance template request failed: %s\n" %
            e)
        return 1
    if st == 200:
        return 0
    if st == 404:
        sys.stderr.write(
            "instanceTemplates.get: %r not found globally or in region %s "
            "in project %s\n" % (template_name, region, project),
        )
        return 1
    sys.stderr.write(
        "instanceTemplates.get (regional) HTTP %s: %s\n" % (st, body[:1200]),
    )
    return 1


def cmd_delete_global_image(project: str, image_name: str, token: str) -> int:
    """Best-effort global image delete; single machine-readable stdout line
    (see module doc)."""
    _maybe_compute_image_delete_debug(token)
    url = (
        "https://compute.googleapis.com/compute/v1/projects/"
        + project
        + "/global/images/"
        + image_name
    )
    try:
        st, body = _http("DELETE", url, token, timeout=120)
    except OSError as e:
        sys.stderr.write("delete global image request failed: %s\n" % e)
        print("warn error")
        return 0
    if st == 404:
        print("absent")
        return 0
    if st == 204:
        print("removed")
        return 0
    if st != 200:
        print("warn %s" % st)
        return 0

    if not body.strip():
        print("removed")
        return 0
    try:
        op = json.loads(body)
    except json.JSONDecodeError:
        print("removed")
        return 0
    if not _looks_like_compute_operation(op):
        print("removed")
        return 0

    ok, err = _wait_operation_done(
        op, token, project, max_iterations=120, sleep_s=2.0, get_timeout=180)
    if ok:
        print("removed")
        return 0
    sys.stderr.write("global image delete operation: %s\n" % err)
    if err == "operation timeout":
        print("warn TIMEOUT")
    else:
        print("warn OPERATION")
    return 0


def cmd_delete_instance_template(
        project: str,
        template_name: str,
        token: str) -> int:
    """Strict-ish global instance template delete: exit 1 on real failure (see
    module doc)."""
    base = (
        "https://compute.googleapis.com/compute/v1/projects/"
        + project
        + "/global"
    )
    st, body = _http("DELETE", base +
                     "/instanceTemplates/" +
                     template_name, token, timeout=180)
    if st == 404:
        return 0
    if st != 200:
        sys.stderr.write(
            "instanceTemplates.delete HTTP %s: %s\n" % (st, body[:800]),
        )
        return 1
    op = json.loads(body) if body.strip() else {}
    ok, err = _wait_operation_done(
        op, token, project, max_iterations=120, sleep_s=2.0, get_timeout=180)
    if ok:
        return 0
    if err == "operation timeout":
        sys.stderr.write("instance template delete operation timeout\n")
    else:
        sys.stderr.write(err + "\n")
    return 1


def cmd_delete_regional_instance_template(
        project: str,
        region: str,
        template_name: str,
        token: str) -> int:
    """Regional instance template delete (KCC spec.region); 404 => success.

    Same polling as global.
    """
    base = (
        "https://compute.googleapis.com/compute/v1/projects/"
        + project
        + "/regions/"
        + region
    )
    st, body = _http("DELETE", base +
                     "/instanceTemplates/" +
                     template_name, token, timeout=180)
    if st == 404:
        return 0
    if st != 200:
        sys.stderr.write(
            "regional instanceTemplates.delete HTTP %s: %s\n" % (
                st, body[:800]),
        )
        return 1
    op = json.loads(body) if body.strip() else {}
    ok, err = _wait_operation_done(
        op, token, project, max_iterations=120, sleep_s=2.0, get_timeout=180)
    if ok:
        return 0
    if err == "operation timeout":
        sys.stderr.write(
            "regional instance template delete operation timeout\n")
    else:
        sys.stderr.write(err + "\n")
    return 1


# ---------------------------------------------------------------------------
# CF instance template: orphan Packer zonal disks (list / delete helpers)
# ---------------------------------------------------------------------------


def _list_zone_disks(project: str, zone: str,
                     token: str) -> tuple[list[dict], str | None]:
    """Paginated ``GET .../zones/{zone}/disks`` (Compute v1 ``disks.list``).

    Returns ``(disk_resource_dicts, error_message)``. On first non-200 or
    transport error, returns ``([], "<message>")``; on success,
    ``error_message`` is ``None``.
    """
    items: list[dict] = []
    page_token: str | None = ""
    while True:
        url = (
            "https://compute.googleapis.com/compute/v1/projects/"
            + project
            + "/zones/"
            + zone
            + "/disks"
        )
        if page_token:
            url = url + "?" + urllib.parse.urlencode({"pageToken": page_token})
        try:
            st, body = _http("GET", url, token, timeout=180)
        except OSError as e:
            return [], "disks.list request failed: %s" % e
        if st != 200:
            return [], "disks.list HTTP %s: %s" % (st, body[:800])
        data = json.loads(body) if body.strip() else {}
        items.extend(data.get("items") or [])
        page_token = data.get("nextPageToken") or None
        if not page_token:
            break
    return items, None


def _disk_size_gb(disk: dict) -> int | None:
    """Parse Compute ``sizeGb`` string field; ``None`` if missing or
    invalid."""
    raw = disk.get("sizeGb")
    if raw is None:
        return None
    try:
        return int(raw)
    except (TypeError, ValueError):
        return None


def _parse_orphan_packer_size_gb(
        size_gb_str: str) -> tuple[int | None, str | None]:
    """Return ``(want_size_gb, None)`` or ``(None, None)`` for any-size
    keyword.

    Keywords ``any``, ``*``, ``all`` (case-insensitive) mean do not filter on
    size. On invalid integer (and not a keyword), returns ``(None,
    "<error>")``.
    """
    raw = size_gb_str.strip()
    low = raw.lower()
    if low in ("any", "*", "all"):
        return None, None
    try:
        return int(raw), None
    except ValueError:
        return None, "invalid sizeGb %r" % size_gb_str


def _orphan_packer_disk_names(
        items: list[dict],
        want_size_gb: int | None) -> list[str]:
    """Names of zonal disks likely left by the Packer googlecompute builder.

    Keeps disks whose name starts with ``packer-``, ``users`` is
    absent/null/``[]`` (unattached; non-list ``users`` is skipped). When
    ``want_size_gb`` is set, ``sizeGb`` must equal it; when ``None``, any
    positive parsed size matches. Disks with missing or invalid ``sizeGb`` are
    skipped.
    """
    out: list[str] = []
    for d in items:
        name = d.get("name")
        if not isinstance(name, str) or not name.startswith("packer-"):
            continue
        users = d.get("users")
        if isinstance(users, list) and len(users) > 0:
            continue
        if users not in (None, []):
            continue
        sz = _disk_size_gb(d)
        if sz is None:
            continue
        if want_size_gb is not None and sz != want_size_gb:
            continue
        out.append(name)
    return out


def _log_orphan_packer_disks_if_any(cmd: str, names: list[str]) -> None:
    """One stderr line when unattached packer-* disks match (otherwise
    silent)."""
    if not names:
        return
    sys.stderr.write(
        "%s: %d orphan packer-* disk(s): %s\n" %
        (cmd, len(names), ", ".join(names)))


def cmd_list_orphan_packer_disks(
        project: str,
        zone: str,
        size_gb_str: str,
        token: str) -> int:
    """CLI: print matching disk names; exit 1 on bad size or list failure."""
    want, parse_err = _parse_orphan_packer_size_gb(size_gb_str)
    if parse_err:
        sys.stderr.write("list-orphan-packer-disks: %s\n" % parse_err)
        return 1
    items, err = _list_zone_disks(project, zone, token)
    if err:
        sys.stderr.write("%s\n" % err)
        return 1
    names = _orphan_packer_disk_names(items, want)
    _log_orphan_packer_disks_if_any("list-orphan-packer-disks", names)
    for name in names:
        print(name)
    return 0


def cmd_delete_zonal_disk(
        project: str,
        zone: str,
        disk_name: str,
        token: str) -> int:
    """Best-effort single disk delete (parity with zone-instance)."""
    url = (
        "https://compute.googleapis.com/compute/v1/projects/"
        + project
        + "/zones/"
        + zone
        + "/disks/"
        + urllib.parse.quote(disk_name, safe="")
    )
    try:
        st, body = _http("DELETE", url, token, timeout=300)
    except OSError as e:
        sys.stderr.write(
            "disks.delete request failed for %s: %s\n" %
            (disk_name, e))
        return 0
    if st == 404:
        return 0
    if st != 200:
        sys.stderr.write("disks.delete HTTP %s for %s: %s\n" %
                         (st, disk_name, body[:800]))
        return 0
    op = json.loads(body) if body.strip() else {}
    ok, err = _wait_operation_done(
        op, token, project, max_iterations=180, get_timeout=300)
    if not ok:
        sys.stderr.write("disks.delete op for %s: %s\n" % (disk_name, err))
    return 0


# ---------------------------------------------------------------------------
# CF instance template: cleanup-orphan-packer-disks subcommand
# (CLI wrapper — calls list/delete helpers in the orphan-disk section above)
# ---------------------------------------------------------------------------


def cmd_cleanup_orphan_packer_disks(
        project: str,
        zone: str,
        size_gb_str: str,
        token: str) -> int:
    """CLI: list+delete matching disks; always exit 0 (see module
    docstring)."""
    want, parse_err = _parse_orphan_packer_size_gb(size_gb_str)
    if parse_err:
        sys.stderr.write("cleanup-orphan-packer-disks: %s\n" % parse_err)
        return 0
    items, err = _list_zone_disks(project, zone, token)
    if err:
        sys.stderr.write("cleanup-orphan-packer-disks: %s\n" % err)
        return 0
    names = _orphan_packer_disk_names(items, want)
    _log_orphan_packer_disks_if_any("cleanup-orphan-packer-disks", names)
    for name in names:
        sys.stderr.write("cleanup-orphan-packer-disks: deleting %s\n" % name)
        cmd_delete_zonal_disk(project, zone, name, token)
    return 0


# ---------------------------------------------------------------------------
# Zonal instances: stop / delete (CF Packer via zone-instance subcommand)
# ---------------------------------------------------------------------------


def cmd_zone_instance(
        action: str,
        project: str,
        zone: str,
        instance_name: str,
        token: str) -> int:
    """Zonal stop/delete; always exit 0 like the historical shell wrappers."""
    base = "https://compute.googleapis.com/compute/v1/projects/" + \
        project + "/zones/" + zone
    if action == "stop":
        st, body = _http(
            "POST",
            base + "/instances/" + instance_name + "/stop",
            token,
            data=b"{}",
            timeout=300,
        )
    elif action == "delete":
        st, body = _http(
            "DELETE",
            base + "/instances/" + instance_name,
            token,
            timeout=300,
        )
    else:
        return 0
    if st == 404:
        return 0
    if st not in (200,):
        sys.stderr.write("instances.%s HTTP %s: %s\n" %
                         (action, st, body[:800]))
        return 0
    op = json.loads(body) if body.strip() else {}
    ok, err = _wait_operation_done(
        op, token, project, max_iterations=180, get_timeout=300)
    if not ok:
        sys.stderr.write("instances.%s op: %s\n" % (action, err))
    return 0


# ---------------------------------------------------------------------------
# Zonal instances: serial port output (CVD/CTS ephemeral pod logs)
# ---------------------------------------------------------------------------


def cmd_get_serial_port_output(
    project: str,
    zone: str,
    instance_name: str,
    port: int,
    start: int,
    token: str,
) -> int:
    """GET instances.getSerialPortOutput → .../serialPort (not POST); stdout JSON."""
    query = urllib.parse.urlencode({"port": port, "start": start})
    url = (
        "https://compute.googleapis.com/compute/v1/projects/"
        + urllib.parse.quote(project, safe="")
        + "/zones/"
        + urllib.parse.quote(zone, safe="")
        + "/instances/"
        + urllib.parse.quote(instance_name, safe="")
        + "/serialPort?"
        + query
    )
    try:
        st, body = _http("GET", url, token, timeout=60)
    except OSError as e:
        sys.stderr.write("getSerialPortOutput request failed: %s\n" % e)
        return 1
    if st != 200:
        sys.stderr.write(
            "getSerialPortOutput HTTP %s: %s\n" % (st, body[:1200]),
        )
        return 1
    try:
        data = json.loads(body) if body.strip() else {}
    except json.JSONDecodeError as e:
        sys.stderr.write("getSerialPortOutput JSON error: %s\n" % e)
        return 1
    contents = data.get("contents")
    if contents is None:
        contents = ""
    elif not isinstance(contents, str):
        contents = str(contents)
    nxt = data.get("next", start)
    if isinstance(nxt, (int, float)):
        nxt = str(int(nxt))
    elif not isinstance(nxt, str):
        nxt = str(start)
    print(json.dumps({"contents": contents, "next": nxt}))
    return 0


def cmd_get_instance_status(
    project: str,
    zone: str,
    instance_name: str,
    token: str,
) -> int:
    """GET instances.get; stdout status field only (CVD/CTS ephemeral VM wait)."""
    url = (
        "https://compute.googleapis.com/compute/v1/projects/"
        + urllib.parse.quote(project, safe="")
        + "/zones/"
        + urllib.parse.quote(zone, safe="")
        + "/instances/"
        + urllib.parse.quote(instance_name, safe="")
    )
    try:
        st, body = _http("GET", url, token, timeout=60)
    except OSError as e:
        sys.stderr.write("getInstance HTTP request failed: %s\n" % e)
        return 1
    if st == 404:
        return 0
    if st != 200:
        sys.stderr.write("getInstance HTTP %s: %s\n" % (st, body[:1200]))
        return 1
    try:
        data = json.loads(body) if body.strip() else {}
    except json.JSONDecodeError as e:
        sys.stderr.write("getInstance JSON error: %s\n" % e)
        return 1
    status = data.get("status")
    if status is not None and status != "":
        print(status)
    return 0


# ---------------------------------------------------------------------------
# OS Login: prune SSH public keys (CF pre-Packer only)
# ---------------------------------------------------------------------------


def cmd_prune_os_login_ssh_keys(token: str) -> int:
    """Remove all SSH public keys from the token identity's OS Login
    profile."""
    token_email = _access_token_email(token)
    url = _os_login_login_profile_url(token)
    try:
        st, body = _http("GET", url, token, timeout=60)
    except OSError as e:
        sys.stderr.write(
            "prune-os-login: loginProfile request failed: %s\n" %
            e)
        print("0")
        return 0
    if st != 200:
        sys.stderr.write(
            "prune-os-login: loginProfile HTTP %s: %s\n" % (st, body[:1200]),
        )
        print("0")
        return 0
    try:
        profile = json.loads(body) if body.strip() else {}
    except json.JSONDecodeError as e:
        sys.stderr.write("prune-os-login: loginProfile JSON error: %s\n" % e)
        print("0")
        return 0

    keys_obj = profile.get("sshPublicKeys")
    if not isinstance(keys_obj, dict) or not keys_obj:
        print("0")
        return 0

    removed = 0
    for map_key, entry in keys_obj.items():
        if not isinstance(entry, dict):
            continue
        name = entry.get("name")
        # getLoginProfile: sshPublicKeys[].name is
        # users/{numericId}/sshPublicKeys/{fp}.
        # DELETE with workload-identity GSA must use the same user segment as
        # credential (service account email), not the numeric id, or OS Login
        # returns 403.
        fingerprint: str | None = None
        if isinstance(map_key, str) and map_key:
            fingerprint = map_key
        fp_field = entry.get("fingerprint")
        if not fingerprint and isinstance(fp_field, str) and fp_field:
            fingerprint = fp_field
        if not fingerprint and isinstance(name, str) and "/" in name:
            parts = name.split("/")
            if len(parts) >= 2 and parts[-2] == "sshPublicKeys":
                fingerprint = parts[-1]
        if not fingerprint:
            sys.stderr.write(
                "prune-os-login: sshPublicKey missing fingerprint, "
                "skip one entry\n",
            )
            continue

        if token_email:
            del_url = (
                "https://oslogin.googleapis.com/v1/users/"
                + urllib.parse.quote(token_email, safe="")
                + "/sshPublicKeys/"
                + urllib.parse.quote(fingerprint, safe="")
            )
        elif isinstance(name, str) and name:
            enc = "/".join(urllib.parse.quote(seg, safe="")
                           for seg in name.split("/"))
            del_url = "https://oslogin.googleapis.com/v1/" + enc
        else:
            sys.stderr.write(
                "prune-os-login: sshPublicKey missing name, skip one entry\n")
            continue
        try:
            st_d, body_d = _http("DELETE", del_url, token, timeout=60)
        except OSError as e:
            sys.stderr.write(
                "prune-os-login: delete %s failed: %s\n" %
                (del_url, e))
            continue
        if st_d in (200, 204):
            removed += 1
            continue
        if st_d == 404:
            continue
        sys.stderr.write("prune-os-login: delete %s HTTP %s: %s\n" %
                         (del_url, st_d, body_d[:800]), )

    print("%d" % removed)
    return 0


def _token() -> str | None:
    """Bearer from env; None if missing (caller prints error and exits 1)."""
    t = os.environ.get(TOKEN_ENV, "").strip()
    return t or None


# ---------------------------------------------------------------------------
# CLI entrypoint (argparse subcommands → handlers above)
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    argv = argv if argv is not None else sys.argv[1:]
    parser = argparse.ArgumentParser(
        description=(
            "GCP REST helpers for cf_create_instance_template.sh — "
            "Compute Engine v1 and Cloud OS Login v1; metadata bearer "
            "token only; see module docstring."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Requires CF_COMPUTE_REST_TOKEN (cloud-platform scope), except "
            "prune-os-login-ssh-keys with no token prints 0 and exits 0. "
            "Optional: COMPUTE_IMAGE_DELETE_DEBUG=true "
            "(delete-global-image only).",
        ),
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_img = sub.add_parser(
        "delete-global-image",
        help=(
            "DELETE global/images/{name}; poll async Operation when API "
            "returns one."
        ),
    )
    p_img.add_argument("project")
    p_img.add_argument("image_name")

    p_img_get = sub.add_parser(
        "get-global-image",
        help="GET global/images/{name}; exit 0 if image exists (HTTP 200).",
    )
    p_img_get.add_argument("project")
    p_img_get.add_argument("image_name")

    p_tmpl_get = sub.add_parser(
        "get-instance-template",
        help=(
            "GET global then regional instanceTemplates/{name}; exit 0 if "
            "either exists."
        ),
    )
    p_tmpl_get.add_argument("project")
    p_tmpl_get.add_argument("region")
    p_tmpl_get.add_argument("template_name")

    p_tmpl = sub.add_parser(
        "delete-instance-template",
        help="DELETE global instanceTemplates/{name} and poll until DONE.",
    )
    p_tmpl.add_argument("project")
    p_tmpl.add_argument("template_name")

    p_tmpl_reg = sub.add_parser(
        "delete-regional-instance-template",
        help=(
            "DELETE regions/{region}/instanceTemplates/{name}; poll until "
            "DONE."
        ),
    )
    p_tmpl_reg.add_argument("project")
    p_tmpl_reg.add_argument("region")
    p_tmpl_reg.add_argument("template_name")

    p_zone = sub.add_parser(
        "zone-instance",
        help=(
            "POST instances.stop or DELETE instances (best-effort; exits 0 "
            "like legacy shell)."
        ),
    )
    p_zone.add_argument("action", choices=("stop", "delete"))
    p_zone.add_argument("project")
    p_zone.add_argument("zone")
    p_zone.add_argument("instance_name")

    p_serial = sub.add_parser(
        "get-serial-port-output",
        help=(
            "GET instances.getSerialPortOutput; stdout JSON "
            "{contents, next} (CVD/CTS ephemeral logs)."
        ),
    )
    p_serial.add_argument("project")
    p_serial.add_argument("zone")
    p_serial.add_argument("instance_name")
    p_serial.add_argument(
        "--port",
        type=int,
        default=1,
        help="Serial port number (default 1).",
    )
    p_serial.add_argument(
        "--start",
        type=int,
        default=0,
        help="Byte offset for incremental reads (default 0).",
    )

    p_inst_get = sub.add_parser(
        "get-instance-status",
        help=(
            "GET instances/{name}; stdout status (RUNNING, …); empty on 404 "
            "(CVD/CTS ephemeral VM wait)."
        ),
    )
    p_inst_get.add_argument("project")
    p_inst_get.add_argument("zone")
    p_inst_get.add_argument("instance_name")

    p_list_orphan = sub.add_parser(
        "list-orphan-packer-disks",
        help=(
            "List unattached packer-* disks (size_gb integer, or any/* /all "
            "for all sizes)."
        ),
    )
    p_list_orphan.add_argument("project")
    p_list_orphan.add_argument("zone")
    p_list_orphan.add_argument("size_gb")

    p_del_disk = sub.add_parser(
        "delete-zonal-disk",
        help=(
            "DELETE zones/{zone}/disks/{name}; poll Operation "
            "(best-effort exit 0)."
        ),
    )
    p_del_disk.add_argument("project")
    p_del_disk.add_argument("zone")
    p_del_disk.add_argument("disk_name")

    p_cleanup_orphan = sub.add_parser(
        "cleanup-orphan-packer-disks",
        help=(
            "Delete unattached packer-* disks (size_gb or any/* /all; "
            "best-effort exit 0)."
        ),
    )
    p_cleanup_orphan.add_argument("project")
    p_cleanup_orphan.add_argument("zone")
    p_cleanup_orphan.add_argument("size_gb")

    sub.add_parser(
        "prune-os-login-ssh-keys",
        help=(
            "DELETE sshPublicKeys on token identity loginProfile "
            "(tokeninfo email; best-effort)."
        ),
    )

    args = parser.parse_args(argv)
    token = _token()

    if args.command == "prune-os-login-ssh-keys":
        if not token:
            sys.stderr.write("%s is not set or empty\n" % TOKEN_ENV)
            print("0")
            return 0
        return cmd_prune_os_login_ssh_keys(token)

    if not token:
        sys.stderr.write("%s is not set or empty\n" % TOKEN_ENV)
        return 1

    if args.command == "delete-global-image":
        return cmd_delete_global_image(args.project, args.image_name, token)
    if args.command == "get-global-image":
        return cmd_get_global_image(args.project, args.image_name, token)
    if args.command == "get-instance-template":
        return cmd_get_instance_template(
            args.project, args.region, args.template_name, token
        )
    if args.command == "delete-instance-template":
        return cmd_delete_instance_template(
            args.project, args.template_name, token)
    if args.command == "delete-regional-instance-template":
        return cmd_delete_regional_instance_template(
            args.project, args.region, args.template_name, token
        )
    if args.command == "zone-instance":
        return cmd_zone_instance(
            args.action,
            args.project,
            args.zone,
            args.instance_name,
            token,
        )
    if args.command == "get-serial-port-output":
        return cmd_get_serial_port_output(
            args.project,
            args.zone,
            args.instance_name,
            args.port,
            args.start,
            token,
        )
    if args.command == "get-instance-status":
        return cmd_get_instance_status(
            args.project,
            args.zone,
            args.instance_name,
            token,
        )
    if args.command == "list-orphan-packer-disks":
        return cmd_list_orphan_packer_disks(
            args.project, args.zone, args.size_gb, token)
    if args.command == "delete-zonal-disk":
        return cmd_delete_zonal_disk(
            args.project, args.zone, args.disk_name, token)
    if args.command == "cleanup-orphan-packer-disks":
        return cmd_cleanup_orphan_packer_disks(
            args.project, args.zone, args.size_gb, token)
    return 1


if __name__ == "__main__":
    sys.exit(main())
