#!/usr/bin/env python3
#
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
# Mint a GitHub App installation token and create a Kubernetes Secret for Argo
# Workflows git HTTPS artifacts (username=x-access-token, password=token).
from __future__ import annotations

import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request

try:
    import jwt
except ImportError:
    print("Missing dependency: pip install pyjwt cryptography", file=sys.stderr)
    sys.exit(1)


def _read_sa() -> tuple[str, str, str]:
    base = "/var/run/secrets/kubernetes.io/serviceaccount"
    with open(os.path.join(base, "namespace"), encoding="utf-8") as f:
        namespace = f.read().strip()
    with open(os.path.join(base, "token"), encoding="utf-8") as f:
        token = f.read().strip()
    ca = os.path.join(base, "ca.crt")
    return namespace, token, ca


def _github_jwt(app_id: str, private_key_pem: str) -> str:
    now = int(time.time())
    # PyJWT 2.12+ requires iss to be a str; GitHub accepts the App ID as a decimal string.
    iss = str(int(app_id.strip(), 10))
    payload = {
        "iat": now - 60,
        "exp": now + 9 * 60,
        "iss": iss,
    }
    return jwt.encode(payload, private_key_pem, algorithm="RS256")


def _installation_token(jwt_token: str, installation_id: str) -> str:
    req = urllib.request.Request(
        f"https://api.github.com/app/installations/{installation_id}/access_tokens",
        method="POST",
        headers={
            "Authorization": f"Bearer {jwt_token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "horizon-sdv-workflows",
        },
    )
    ctx = ssl.create_default_context()
    with urllib.request.urlopen(req, context=ctx, timeout=60) as resp:
        body = json.loads(resp.read().decode("utf-8"))
    return body["token"]


def _create_git_secret(
    namespace: str,
    sa_token: str,
    ca_path: str,
    secret_name: str,
    password: str,
) -> None:
    api = "https://kubernetes.default.svc"
    url = f"{api}/api/v1/namespaces/{namespace}/secrets"
    body_obj = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": secret_name},
        "type": "Opaque",
        "stringData": {"username": "x-access-token", "password": password},
    }
    data = json.dumps(body_obj).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={
            "Authorization": f"Bearer {sa_token}",
            "Content-Type": "application/json",
        },
    )
    ctx = ssl.create_default_context(cafile=ca_path)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=60) as resp:
            if resp.status not in (200, 201):
                print(f"Unexpected status creating secret: {resp.status}", file=sys.stderr)
                sys.exit(1)
    except urllib.error.HTTPError as e:
        if e.code == 409:
            # Idempotent retry: replace
            patch_url = f"{api}/api/v1/namespaces/{namespace}/secrets/{secret_name}"
            merge_patch = json.dumps({"stringData": {"username": "x-access-token", "password": password}})
            preq = urllib.request.Request(
                patch_url,
                data=merge_patch.encode("utf-8"),
                method="PATCH",
                headers={
                    "Authorization": f"Bearer {sa_token}",
                    "Content-Type": "application/merge-patch+json",
                },
            )
            with urllib.request.urlopen(preq, context=ctx, timeout=60) as resp:
                if resp.status not in (200,):
                    print(f"PATCH secret failed: {resp.status}", file=sys.stderr)
                    sys.exit(1)
            return
        print(e.read().decode("utf-8", errors="replace"), file=sys.stderr)
        raise


def main() -> None:
    app_id = os.environ.get("GITHUB_APP_ID", "").strip()
    installation_id = os.environ.get("GITHUB_APP_INSTALLATION_ID", "").strip()
    private_key = os.environ.get("GITHUB_APP_PRIVATE_KEY", "")
    uid = os.environ.get("WORKFLOW_UID", "").strip()
    if not app_id or not installation_id or not private_key or not uid:
        print(
            "GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, GITHUB_APP_PRIVATE_KEY, WORKFLOW_UID required",
            file=sys.stderr,
        )
        sys.exit(1)
    secret_name = f"{uid}-pipeline-git-creds"
    gh_jwt = _github_jwt(app_id, private_key)
    token = _installation_token(gh_jwt, installation_id)
    ns, sa_token, ca = _read_sa()
    _create_git_secret(ns, sa_token, ca, secret_name, token)
    print(f"Created secret {secret_name} in namespace {ns}")


if __name__ == "__main__":
    main()
