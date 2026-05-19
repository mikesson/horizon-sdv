// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
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
import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Checkbox,
  CircularProgress,
  FormControlLabel,
  FormGroup,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { apiHorizon, apiMm } from '../../utils/api';
import type { WorkflowListResponse, WorkflowSummary, WorkflowsVisibilityDTO } from '../../types';
import {
  collectSubmittedFromFromWorkflows,
  extractSubmittedFromEnumsFromOpenAPI,
  mergeSourceOptions,
} from '../../utils/workflowVisibilityDiscovery';
import {
  DEFAULT_MAX_WORKFLOW_LOG_LINES,
  MAX_MAX_WORKFLOW_LOG_LINES,
  MIN_MAX_WORKFLOW_LOG_LINES,
  readMaxWorkflowLogLines,
  writeMaxWorkflowLogLines,
} from '../../utils/logBufferSettings';

export function SettingsTab() {
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [savedMsg, setSavedMsg] = useState<string | null>(null);

  const [options, setOptions] = useState<string[]>([]);
  /** null = no restriction (show all). Empty = hide all. */
  const [selected, setSelected] = useState<string[] | null>(null);

  const [logBufferLines, setLogBufferLines] = useState(String(DEFAULT_MAX_WORKFLOW_LOG_LINES));
  const [logBufferErr, setLogBufferErr] = useState<string | null>(null);
  const [logBufferSaved, setLogBufferSaved] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    setSavedMsg(null);
    try {
      const [mmRes, openRes, runRes, histRes] = await Promise.all([
        apiMm('/settings/workflows-visibility'),
        apiHorizon('/openapi.json'),
        apiHorizon('/v1/workflows/running?limit=200'),
        apiHorizon('/v1/workflows/history?limit=200'),
      ]);

      let openapiEnums: string[] = [];
      if (openRes.ok) {
        try {
          const doc = (await openRes.json()) as unknown;
          openapiEnums = extractSubmittedFromEnumsFromOpenAPI(doc);
        } catch {
          openapiEnums = extractSubmittedFromEnumsFromOpenAPI({});
        }
      } else {
        openapiEnums = extractSubmittedFromEnumsFromOpenAPI({});
      }

      const wfItems: WorkflowSummary[] = [];
      if (runRes.ok) {
        const jr = (await runRes.json()) as WorkflowListResponse;
        wfItems.push(...(jr.items || []));
      }
      if (histRes.ok) {
        const jh = (await histRes.json()) as WorkflowListResponse;
        wfItems.push(...(jh.items || []));
      }
      const observed = collectSubmittedFromFromWorkflows(wfItems);
      const merged = mergeSourceOptions(openapiEnums, observed);
      setOptions(merged);

      if (!mmRes.ok) {
        throw new Error(`settings: ${mmRes.status}`);
      }
      const cur = (await mmRes.json()) as WorkflowsVisibilityDTO;
      if (cur.allowedSubmittedFrom === undefined) {
        setSelected(null);
      } else {
        setSelected([...(cur.allowedSubmittedFrom ?? [])]);
      }

      setLogBufferLines(String(readMaxWorkflowLogLines()));
      setLogBufferErr(null);
      setLogBufferSaved(null);
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'load failed');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    setSaving(true);
    setErr(null);
    setSavedMsg(null);
    try {
      const r = await apiMm('/settings/workflows-visibility', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(
          selected === null ? { allowedSubmittedFrom: null } : { allowedSubmittedFrom: selected }
        ),
      });
      if (!r.ok) {
        const t = await r.text();
        throw new Error(t || `save ${r.status}`);
      }
      setSavedMsg('Saved.');
      void load();
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'save failed');
    } finally {
      setSaving(false);
    }
  };

  const saveLogBuffer = () => {
    setLogBufferErr(null);
    setLogBufferSaved(null);
    const n = Number.parseInt(logBufferLines.trim(), 10);
    if (!Number.isFinite(n)) {
      setLogBufferErr('Enter a whole number.');
      return;
    }
    if (n < MIN_MAX_WORKFLOW_LOG_LINES || n > MAX_MAX_WORKFLOW_LOG_LINES) {
      setLogBufferErr(
        `Must be between ${MIN_MAX_WORKFLOW_LOG_LINES.toLocaleString()} and ${MAX_MAX_WORKFLOW_LOG_LINES.toLocaleString()}.`
      );
      return;
    }
    const v = writeMaxWorkflowLogLines(n);
    setLogBufferLines(String(v));
    setLogBufferSaved(`Saved. Log dialogs will buffer up to ${v.toLocaleString()} lines (browser memory scales with line count and line length).`);
  };

  if (loading) {
    return (
      <Box display="flex" justifyContent="center" p={4}>
        <CircularProgress />
      </Box>
    );
  }

  const noRestriction = selected === null;
  const checked = (v: string) => (selected === null ? true : selected.includes(v));

  const toggleOne = (v: string) => {
    if (selected === null) {
      setSelected(options.length ? [...options] : []);
    }
    setSelected((prev) => {
      const base = prev === null ? [...options] : [...prev];
      const i = base.indexOf(v);
      if (i >= 0) {
        base.splice(i, 1);
      } else {
        base.push(v);
      }
      return base;
    });
  };

  return (
    <Stack spacing={2}>
      <Typography variant="body2" color="text.secondary">
        Control which workflow runs appear under <strong>Running Workflows</strong> and{' '}
        <strong>History</strong> in each module, based on the{' '}
        <code>horizon-sdv.io/submitted-from</code> label (REST API, Developer Portal, Horizon CLI, or
        overrides). Options combine Horizon OpenAPI enums with values seen in recent workflows.
      </Typography>
      {err && (
        <Alert severity="error" onClose={() => setErr(null)}>
          {err}
        </Alert>
      )}
      {savedMsg && (
        <Alert severity="success" onClose={() => setSavedMsg(null)}>
          {savedMsg}
        </Alert>
      )}
      {logBufferErr && (
        <Alert severity="error" onClose={() => setLogBufferErr(null)}>
          {logBufferErr}
        </Alert>
      )}
      {logBufferSaved && (
        <Alert severity="success" onClose={() => setLogBufferSaved(null)}>
          {logBufferSaved}
        </Alert>
      )}
      <Card variant="outlined">
        <CardContent>
          <Typography variant="h6" gutterBottom>
            Workflows visibility
          </Typography>
          <FormControlLabel
            control={
              <Checkbox
                checked={noRestriction}
                onChange={(_, c) => {
                  if (c) {
                    setSelected(null);
                  } else {
                    setSelected([...options]);
                  }
                }}
              />
            }
            label="Show all sources (no filter)"
          />
          <Typography variant="caption" color="text.secondary" display="block" sx={{ mb: 1 }}>
            Uncheck to restrict to specific sources below. An empty selection hides all workflows.
          </Typography>
          <FormGroup>
            {options.map((v) => (
              <FormControlLabel
                key={v}
                control={
                  <Checkbox
                    checked={checked(v)}
                    disabled={noRestriction}
                    onChange={() => toggleOne(v)}
                  />
                }
                label={<code>{v}</code>}
              />
            ))}
          </FormGroup>
          <Button sx={{ mt: 2 }} variant="contained" disabled={saving} onClick={() => void save()}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </CardContent>
      </Card>

      <Card variant="outlined">
        <CardContent>
          <Typography variant="h6" gutterBottom>
            Workflow log buffer (browser)
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            Maximum number of lines kept in memory for the live / archived log viewer on module workflow pages. Higher
            values use more RAM in this browser tab (roughly proportional to total text size). Allowed range:{' '}
            {MIN_MAX_WORKFLOW_LOG_LINES.toLocaleString()}–{MAX_MAX_WORKFLOW_LOG_LINES.toLocaleString()} (default{' '}
            {DEFAULT_MAX_WORKFLOW_LOG_LINES.toLocaleString()}). Stored only in this browser (localStorage).
          </Typography>
          <TextField
            label="Max lines"
            type="number"
            size="small"
            inputProps={{
              min: MIN_MAX_WORKFLOW_LOG_LINES,
              max: MAX_MAX_WORKFLOW_LOG_LINES,
              step: 1000,
            }}
            value={logBufferLines}
            onChange={(e) => setLogBufferLines(e.target.value)}
            sx={{ maxWidth: 280 }}
          />
          <Button sx={{ mt: 2, display: 'block' }} variant="contained" onClick={saveLogBuffer}>
            Save buffer limit
          </Button>
        </CardContent>
      </Card>
    </Stack>
  );
}
