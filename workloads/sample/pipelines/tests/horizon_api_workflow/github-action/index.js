// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Horizon API workflow via REST (Node 20 fetch), aligned with
// horizon-workflow-via-rest.py and tools/horizon CLI behavior.

import fs from 'node:fs';

function envInt(name, def) {
  const raw = String(process.env[name] ?? '').trim();
  if (!raw) return def;
  const n = parseInt(raw, 10);
  return Number.isFinite(n) ? n : def;
}

const RUNNING_POLL_LIMIT = envInt('RUNNING_POLL_LIMIT', 500);
const WAIT_NEW_ATTEMPTS = envInt('WAIT_NEW_ATTEMPTS', 120);
const WAIT_NEW_SLEEP = envInt('WAIT_NEW_SLEEP', 2);
const WAIT_TERMINAL_SECS = envInt('WAIT_TERMINAL_SECS', 3600);
const TERMINAL_POLL_INTERVAL = envInt('TERMINAL_POLL_INTERVAL', 5);
const LOG_STREAM_MAX_SECS = envInt('LOG_STREAM_MAX_SECS', 7200);
const LOG_WAIT_POD_SECS = envInt('LOG_WAIT_POD_SECS', 60);

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function apiBase() {
  const base = String(process.env.HORIZON_API_BASE_URL ?? '').trim();
  if (base) return base.replace(/\/$/, '');
  const d = String(process.env.HORIZON_DOMAIN ?? '').trim();
  if (d) return `https://${d}/horizon-api`;
  console.error('horizon-gha: set HORIZON_API_BASE_URL or HORIZON_DOMAIN');
  process.exit(1);
}

function keycloakBase() {
  const kb = String(process.env.KEYCLOAK_BASE ?? '').trim();
  if (kb) return kb.replace(/\/$/, '');
  const d = String(process.env.HORIZON_DOMAIN ?? '').trim();
  if (d) return `https://${d}/auth`;
  console.error('horizon-gha: set KEYCLOAK_BASE or HORIZON_DOMAIN for token');
  process.exit(1);
}

function singleLine(s) {
  return String(s).replace(/[\r\n]+/g, ' ').trim();
}

function formatNdjsonLine(line) {
  let o;
  try {
    o = JSON.parse(line);
  } catch {
    return `${line}\n`;
  }
  if (typeof o !== 'object' || o === null) return `${line}\n`;
  if (o.heartbeat) return null;
  if (o.result === 'done') {
    const ws = o.workflowStatus || '';
    const reason = o.reason || '';
    const detail = o.detail || '';
    const stage = ws || reason || 'done';
    const msg = detail || reason || '-';
    return `[${stage}] [-] [${singleLine(String(msg))}]\n`;
  }
  const ts = o.ts || '-';
  const stage =
    o.displayName || o.templateName || o.podName || 'log';
  const msg = o.msg == null ? '' : o.msg;
  return `[${stage}] [${ts}] [${singleLine(String(msg))}]\n`;
}

function buildSubmitBody(catalog, userRaw, module, template) {
  let user;
  try {
    user = userRaw.trim() ? JSON.parse(userRaw) : {};
  } catch (e) {
    console.error(`WORKFLOW_PARAMETERS_JSON: ${e.message}`);
    process.exit(1);
  }
  if (user == null) user = {};

  let entry;
  for (const e of catalog.entries || []) {
    if (e.module === module && e.templateName === template) {
      entry = e;
      break;
    }
  }
  if (!entry) {
    console.error(`catalog missing module=${module} template=${template}`);
    process.exit(1);
  }

  function stringParamEmpty(v) {
    if (v === null || v === undefined) return true;
    if (typeof v === 'string') return v === '';
    return false;
  }

  const paramsOut = {};
  for (const p of entry.parameters || []) {
    const name = String(p.name || '').trim();
    if (!name) continue;
    const def = p.default != null ? p.default : '';
    const uv = Object.prototype.hasOwnProperty.call(user, name)
      ? user[name]
      : undefined;
    const userSet = Object.prototype.hasOwnProperty.call(user, name);
    if (!userSet) {
      paramsOut[name] = def;
    } else if (stringParamEmpty(uv)) {
      paramsOut[name] = def !== '' ? def : '';
    } else {
      paramsOut[name] = uv;
    }
  }

  const parameters = {};
  for (const [k, v] of Object.entries(paramsOut)) {
    parameters[k] = v === null || v === undefined ? '' : String(v);
  }
  return JSON.stringify({ parameters });
}

function workflowParametersJsonPayload() {
  const path = String(process.env.WORKFLOW_PARAMETERS_JSON_FILE ?? '').trim();
  if (path && fs.existsSync(path)) {
    let raw = fs.readFileSync(path, 'utf8');
    raw = raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
    if (raw.startsWith('\ufeff')) raw = raw.slice(1);
    return raw;
  }
  let raw = String(process.env.WORKFLOW_PARAMETERS_JSON ?? '{}');
  raw = raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
  if (raw.startsWith('\ufeff')) raw = raw.slice(1);
  return raw;
}

function phaseToExit(ph) {
  if (ph === 'Succeeded') return 0;
  if (ph === 'Aborted') return 2;
  if (ph === 'Failed' || ph === 'Error' || ph === '') return 1;
  return 1;
}

function terminalPhase(ph) {
  return ['Succeeded', 'Failed', 'Error', 'Aborted'].includes(ph);
}

function setOutput(name, value) {
  const out = process.env.GITHUB_OUTPUT;
  if (out) {
    fs.appendFileSync(out, `${name}=${value}\n`, { encoding: 'utf8' });
  }
}

class HorizonRestClient {
  constructor() {
    this._bearer = '';
    this._api = apiBase();
  }

  async refreshBearer() {
    const tok = String(process.env.HORIZON_ACCESS_TOKEN ?? '').trim();
    if (tok) {
      this._bearer = tok;
      return;
    }
    const realm = String(process.env.KEYCLOAK_REALM ?? 'horizon').trim() || 'horizon';
    const clientId =
      String(process.env.KEYCLOAK_CLIENT_ID ?? 'horizon-api-ci').trim() ||
      'horizon-api-ci';
    const secret = String(process.env.KEYCLOAK_CLIENT_SECRET ?? '').trim();
    if (!secret) {
      console.error(
        'horizon-gha: set KEYCLOAK_CLIENT_SECRET or HORIZON_ACCESS_TOKEN'
      );
      process.exit(1);
    }
    const tokenUrl = `${keycloakBase()}/realms/${realm}/protocol/openid-connect/token`;
    const body = new URLSearchParams({
      grant_type: 'client_credentials',
      client_id: clientId,
      client_secret: secret,
    });
    const resp = await fetch(tokenUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString(),
    });
    const text = await resp.text();
    if (!resp.ok) {
      console.error(`horizon-gha: token HTTP ${resp.status}: ${text}`);
      process.exit(1);
    }
    let data;
    try {
      data = JSON.parse(text);
    } catch (e) {
      console.error(`horizon-gha: token parse: ${e.message}`);
      process.exit(1);
    }
    const at = data.access_token || '';
    if (!at) {
      console.error(`horizon-gha: token error: ${text}`);
      process.exit(1);
    }
    this._bearer = at;
  }

  headersJson(extra) {
    const x = extra && typeof extra === 'object' ? extra : {};
    return {
      Authorization: `Bearer ${this._bearer}`,
      Accept: 'application/json',
      ...x,
    };
  }

  async requestJson(method, path, bodyBytes, extraHeaders) {
    const url = this._api + path;
    const envToken = Boolean(
      String(process.env.HORIZON_ACCESS_TOKEN ?? '').trim()
    );
    for (let attempt = 0; attempt < 2; attempt++) {
      if (!this._bearer) await this.refreshBearer();
      const headers = { ...this.headersJson(extraHeaders) };
      const opts = { method, headers };
      if (bodyBytes != null) {
        opts.body = bodyBytes;
        if (!headers['Content-Type']) headers['Content-Type'] = 'application/json';
      }
      const resp = await fetch(url, opts);
      if (resp.status === 401 && attempt === 0 && !envToken) {
        this._bearer = '';
        await this.refreshBearer();
        continue;
      }
      if (!resp.ok) {
        const errBody = await resp.text();
        console.error(
          `horizon-gha: ${method} ${url} HTTP ${resp.status} ${errBody}`
        );
        process.exit(1);
      }
      return await resp.text();
    }
    throw new Error('request retry exhausted');
  }

  async httpGet(path) {
    return this.requestJson('GET', path, null, null);
  }

  async httpPostSubmit(path, body) {
    return this.requestJson('POST', path, body, {
      'Content-Type': 'application/json',
      'X-Horizon-Submitted-From': 'rest-api',
    });
  }

  async workflowPhase(wf) {
    const enc = encodeURIComponent(wf);
    const data = JSON.parse(await this.httpGet(`/v1/workflows/${enc}`));
    return String(data.phase || '').trim();
  }

  async hasPodHint(wf) {
    const enc = encodeURIComponent(wf);
    const data = JSON.parse(await this.httpGet(`/v1/workflows/${enc}`));
    for (const n of data.nodes || []) {
      const pn = n.podName;
      if (typeof pn === 'string' && pn.trim()) return true;
    }
    return false;
  }

  async streamLogLines(wf, shouldStop, ac) {
    const enc = encodeURIComponent(wf);
    const phase = await this.workflowPhase(wf);
    const follow = terminalPhase(phase) ? 'false' : 'true';
    const path = `/v1/workflows/${enc}/log?follow=${follow}&container=main`;
    const url = this._api + path;
    console.error(
      `━━ Horizon log stream ━━ ${url} (max-time=${LOG_STREAM_MAX_SECS}s follow=${follow} format=FORMATTED)`
    );
    const deadline = Date.now() + LOG_STREAM_MAX_SECS * 1000;
    const envToken = Boolean(
      String(process.env.HORIZON_ACCESS_TOKEN ?? '').trim()
    );

    let resp;
    for (let attempt = 0; attempt < 2; attempt++) {
      if (!this._bearer) await this.refreshBearer();
      try {
        resp = await fetch(url, {
          method: 'GET',
          headers: {
            Authorization: `Bearer ${this._bearer}`,
            Accept: 'application/x-ndjson',
            'Accept-Encoding': 'identity',
          },
          signal: ac.signal,
        });
        if (resp.status === 401 && attempt === 0 && !envToken) {
          this._bearer = '';
          await this.refreshBearer();
          continue;
        }
        if (!resp.ok) {
          const t = await resp.text();
          throw new Error(`log HTTP ${resp.status}: ${t}`);
        }
        break;
      } catch (e) {
        if (e.name === 'AbortError') return;
        throw e;
      }
    }
    if (!resp || !resp.body) {
      console.error('━━ log stream end ━━');
      return;
    }

    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    try {
      while (!shouldStop() && Date.now() < deadline) {
        let chunk;
        try {
          const r = await reader.read();
          if (r.done) break;
          chunk = r.value;
        } catch (e) {
          if (e.name === 'AbortError' || shouldStop()) break;
          throw e;
        }
        if (shouldStop()) break;
        buffer += decoder.decode(chunk, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';
        for (const line of lines) {
          if (shouldStop()) break;
          const s = line.replace(/\r$/, '');
          if (!s.trim()) continue;
          const out = formatNdjsonLine(s);
          if (out != null) process.stdout.write(out);
        }
      }
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* ignore */
      }
    }
    if (buffer.trim()) {
      const out = formatNdjsonLine(buffer.replace(/\r$/, ''));
      if (out != null) process.stdout.write(out);
    }
    console.error('━━ log stream end ━━');
  }

  async cmdAbort(wf) {
    const enc = encodeURIComponent(wf);
    const url = `${this._api}/v1/workflows/${enc}/abort`;
    const envToken = Boolean(
      String(process.env.HORIZON_ACCESS_TOKEN ?? '').trim()
    );
    for (let attempt = 0; attempt < 2; attempt++) {
      if (!this._bearer) await this.refreshBearer();
      try {
        const resp = await fetch(url, {
          method: 'POST',
          headers: this.headersJson({ Accept: 'application/json' }),
        });
        if (resp.status === 401 && attempt === 0 && !envToken) {
          this._bearer = '';
          await this.refreshBearer();
          continue;
        }
        return;
      } catch {
        return;
      }
    }
  }
}

async function cmdSubmit(c) {
  await c.refreshBearer();
  const catalog = JSON.parse(await c.httpGet('/v1/catalog'));
  const userJson = workflowParametersJsonPayload();
  const template = String(process.env.WORKFLOW_TEMPLATE ?? '').trim();
  if (!template) {
    console.error('horizon-gha: WORKFLOW_TEMPLATE is required');
    process.exit(1);
  }
  const module =
    String(process.env.WORKFLOW_MODULE ?? 'sample').trim() || 'sample';
  const body = buildSubmitBody(catalog, userJson, module, template);
  const modEnc = encodeURIComponent(module);
  const tplEnc = encodeURIComponent(template);
  const path = `/v1/modules/${modEnc}/workflowTemplates/${tplEnc}/submit`;

  const before = JSON.parse(
    await c.httpGet(`/v1/workflows/running?limit=${RUNNING_POLL_LIMIT}`)
  );
  const beforeNames = new Set(
    (before.items || [])
      .map((item) => item.name)
      .filter(Boolean)
  );

  await c.httpPostSubmit(path, body);

  let wf = '';
  for (let i = 0; i < WAIT_NEW_ATTEMPTS; i++) {
    const after = JSON.parse(
      await c.httpGet(`/v1/workflows/running?limit=${RUNNING_POLL_LIMIT}`)
    );
    for (const item of after.items || []) {
      const n = item.name;
      if (n && !beforeNames.has(n)) {
        wf = n;
        break;
      }
    }
    if (wf) break;
    await sleep(WAIT_NEW_SLEEP * 1000);
  }
  if (!wf) {
    console.error(
      'horizon-gha: no new workflow in running list (check Argo Events)'
    );
    process.exit(1);
  }
  return wf;
}

async function waitForPodHintVerbose(c, wf) {
  if (LOG_WAIT_POD_SECS <= 0) {
    console.error(
      `Logs: opening live stream for workflow ${wf} (pod wait disabled).`
    );
    return;
  }
  console.error(
    `Logs: waiting for workflow pods (workflow ${wf}, up to ${LOG_WAIT_POD_SECS}s) …`
  );
  const deadline = Date.now() + LOG_WAIT_POD_SECS * 1000;
  while (Date.now() < deadline) {
    if (await c.hasPodHint(wf)) {
      console.error('Logs: pods are visible; streaming output below.');
      return;
    }
    await sleep(2000);
    console.error('Logs: still waiting for pod assignment …');
  }
  console.error('Logs: opening stream anyway (pods may appear shortly).');
}

async function cmdWaitLogs(c, wf, ac) {
  await c.refreshBearer();
  let stop = false;
  const logAc = new AbortController();
  const onAbort = () => {
    stop = true;
    logAc.abort();
  };
  if (ac?.signal) {
    ac.signal.addEventListener('abort', onAbort);
  }

  const logTask = (async () => {
    try {
      await waitForPodHintVerbose(c, wf);
      await c.streamLogLines(wf, () => stop, logAc);
    } catch (e) {
      console.error(`logs: ${e.message || e}`);
    }
  })();

  const deadline = Date.now() + WAIT_TERMINAL_SECS * 1000;
  let ph = '';
  while (Date.now() < deadline) {
    if (ac?.signal?.aborted) {
      stop = true;
      logAc.abort();
      await Promise.race([logTask, sleep(30000)]);
      await c.cmdAbort(wf).catch(() => {});
      return 130;
    }
    ph = await c.workflowPhase(wf);
    if (terminalPhase(ph)) break;
    await sleep(TERMINAL_POLL_INTERVAL * 1000);
  }
  stop = true;
  logAc.abort();
  await Promise.race([logTask, sleep(30000)]);

  if (!terminalPhase(ph)) {
    console.error(`horizon-gha: timeout waiting for terminal phase on ${wf}`);
    return 1;
  }
  return phaseToExit(ph);
}

async function cmdShow(c, wf) {
  await c.refreshBearer();
  const enc = encodeURIComponent(wf);
  const raw = await c.httpGet(`/v1/workflows/${enc}`);
  const data = JSON.parse(raw);
  const summary = {};
  for (const k of [
    'name',
    'phase',
    'workflowTemplate',
    'submittedFrom',
    'archivedLogs',
    'outputArtifacts',
  ]) {
    if (k in data) summary[k] = data[k];
  }
  console.log(JSON.stringify(summary, null, 2));
  console.log('━━ GCS / artifact URIs ━━');
  const al = data.archivedLogs || {};
  const comb = al.combined || {};
  if (comb.gcsUri) console.log(`archivedLogs.combined: ${comb.gcsUri}`);
  for (const step of al.steps || []) {
    const uri = step.gcsUri;
    if (uri) console.log(`archivedLogs.step: ${uri}`);
  }
  for (const art of data.outputArtifacts || []) {
    if (art && typeof art === 'object') {
      console.log(`outputArtifact: ${art.name} ${art.gcsUri}`);
    }
  }
}

function input(name) {
  const u = name.toUpperCase().replace(/ /g, '_');
  return String(process.env[`INPUT_${u}`] ?? '').trim();
}

function applyInputsFromGitHub() {
  const im = input('workflow_module');
  if (im) process.env.WORKFLOW_MODULE = im;
  const tt = input('workflow_template');
  if (tt) process.env.WORKFLOW_TEMPLATE = tt;
  const pj = input('workflow_parameters_json');
  if (pj !== '') process.env.WORKFLOW_PARAMETERS_JSON = pj;

  const dom = input('horizon_domain');
  if (dom) process.env.HORIZON_DOMAIN = dom;
  const api = input('horizon_api_base_url');
  if (api) process.env.HORIZON_API_BASE_URL = api;
  const kb = input('keycloak_base');
  if (kb) process.env.KEYCLOAK_BASE = kb;
  const cid = input('keycloak_client_id');
  if (cid) process.env.KEYCLOAK_CLIENT_ID = cid;
  const kr = input('keycloak_realm');
  if (kr) process.env.KEYCLOAK_REALM = kr;

}

async function main() {
  applyInputsFromGitHub();

  if (
    !String(process.env.HORIZON_ACCESS_TOKEN ?? '').trim() &&
    !String(process.env.KEYCLOAK_CLIENT_SECRET ?? '').trim()
  ) {
    console.error(
      'horizon-gha: provide secrets keycloak_client_secret and/or horizon_access_token'
    );
    process.exit(1);
  }

  const c = new HorizonRestClient();

  const ac = new AbortController();
  process.once('SIGINT', () => {
    console.error('horizon-gha: SIGINT — cancelling wait');
    ac.abort();
  });
  process.once('SIGTERM', () => {
    console.error('horizon-gha: SIGTERM — cancelling wait');
    ac.abort();
  });

  const wf = await cmdSubmit(c);
  setOutput('horizon_workflow_name', wf);
  console.error(`Horizon workflow: ${wf}`);

  const waitRc = await cmdWaitLogs(c, wf, ac);
  if (waitRc === 130) {
    process.exit(130);
  }

  await cmdShow(c, wf);

  if (waitRc === 2) process.exit(2);
  if (waitRc !== 0) process.exit(waitRc);
  process.exit(0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
