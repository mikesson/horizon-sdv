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
  CircularProgress,
  FormControlLabel,
  Grid,
  Switch,
  TextField,
  Typography,
  Chip,
} from '@mui/material';
import { apiMm } from '../../utils/api';
import type { ModuleResponse, StatusResponse } from '../../types';
import { deploymentStatus } from '../../moduleStatus';

async function fetchModules(): Promise<ModuleResponse[]> {
  const r = await apiMm('/modules');
  if (!r.ok) {
    throw new Error(`modules: ${r.status}`);
  }
  return r.json() as Promise<ModuleResponse[]>;
}

async function fetchStatus(idOrName: string): Promise<StatusResponse> {
  const r = await apiMm(`/modules/${encodeURIComponent(idOrName)}/status`);
  if (!r.ok) {
    throw new Error(`status: ${r.status}`);
  }
  return r.json() as Promise<StatusResponse>;
}

export function ModulesTab() {
  const [mods, setMods] = useState<ModuleResponse[]>([]);
  const [statuses, setStatuses] = useState<Record<string, StatusResponse>>({});
  const [refDraft, setRefDraft] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);

  const refresh = useCallback(async (): Promise<boolean> => {
    try {
      const list = await fetchModules();
      setRefDraft((prev) => {
        const next = { ...prev };
        for (const m of list) {
          const serverRef =
            (m.targetRevision ?? m.clusterTargetRevision ?? '').trim() || '';
          if (next[m.name] === undefined) {
            next[m.name] = serverRef;
          }
        }
        return next;
      });
      const st: Record<string, StatusResponse> = {};
      await Promise.all(
        list.map(async (m) => {
          try {
            st[m.name] = await fetchStatus(m.name);
          } catch {
            st[m.name] = {};
          }
        })
      );
      setMods(list);
      setStatuses(st);
      setError(null);
      const allReady = list.every((m) => {
        const label = deploymentStatus(m.enabled, st[m.name]);
        if (!m.enabled) {
          return label === 'NOT INSTALLED';
        }
        return label === 'READY';
      });
      return allReady;
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Load failed');
      return true;
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    let timeoutId = 0;

    const schedule = (delay: number) => {
      timeoutId = window.setTimeout(async () => {
        if (cancelled) {
          return;
        }
        const allReady = await refresh();
        if (cancelled) {
          return;
        }
        schedule(allReady ? 8000 : 3000);
      }, delay);
    };

    schedule(0);
    return () => {
      cancelled = true;
      window.clearTimeout(timeoutId);
    };
  }, [refresh]);

  const applyRef = async (m: ModuleResponse) => {
    const ref = (refDraft[m.name] ?? '').trim();
    if (!ref) {
      setError('Git ref cannot be empty');
      return;
    }
    setBusy(m.name);
    setError(null);
    try {
      const r = await apiMm(`/modules/${encodeURIComponent(m.name)}/target-revision`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ targetRevision: ref }),
      });
      if (!r.ok) {
        const t = await r.text();
        throw new Error(t || `set ref ${r.status}`);
      }
      await refresh();
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Request failed');
    } finally {
      setBusy(null);
    }
  };

  const toggle = async (m: ModuleResponse, enable: boolean) => {
    setBusy(m.name);
    setError(null);
    try {
      if (enable) {
        const refTrim = (refDraft[m.name] ?? '').trim();
        const init: RequestInit = {
          method: 'POST',
          headers: refTrim ? { 'Content-Type': 'application/json' } : undefined,
          body: refTrim ? JSON.stringify({ targetRevision: refTrim }) : undefined,
        };
        const r = await apiMm(`/modules/${encodeURIComponent(m.name)}/enable`, init);
        if (!r.ok) {
          const t = await r.text();
          throw new Error(t || `enable ${r.status}`);
        }
      } else {
        const r = await apiMm(`/modules/${encodeURIComponent(m.name)}/disable`, {
          method: 'DELETE',
        });
        if (r.status === 409) {
          const j = (await r.json()) as { hardDependents?: string[] };
          throw new Error(
            `Cannot disable: required by: ${(j.hardDependents ?? []).join(', ') || 'other modules'}`
          );
        }
        if (!r.ok) {
          const t = await r.text();
          throw new Error(t || `disable ${r.status}`);
        }
      }
      await refresh();
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Request failed');
    } finally {
      setBusy(null);
    }
  };

  if (loading && mods.length === 0) {
    return (
      <Box display="flex" justifyContent="center" p={4}>
        <CircularProgress />
      </Box>
    );
  }

  return (
    <Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Enable or disable modules. Hard dependencies are enabled automatically. Set each
        module&apos;s Git ref (branch, tag, or commit) before enabling or use Apply on an
        enabled module to switch refs. Dependent modules are shown on each card.
      </Typography>
      {error && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      <Grid container spacing={2}>
        {mods.map((m) => {
          const st = statuses[m.name];
          const label = deploymentStatus(m.enabled, st);
          const color =
            label === 'READY'
              ? 'success'
              : label === 'NOT INSTALLED'
                ? 'default'
                : label === 'UPDATE IN PROGRESS'
                  ? 'info'
                  : 'warning';
          return (
            <Grid size={{ xs: 12, sm: 6, md: 4 }} key={m.id || m.name}>
              <Card variant="outlined">
                <CardContent>
                  <Typography variant="h6">{m.name}</Typography>
                  <Box sx={{ my: 1, display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
                    <Chip size="small" label={label} color={color} />
                  </Box>
                  <Typography variant="caption" color="text.secondary" display="block" sx={{ mb: 0.5 }}>
                    Effective ref: {m.targetRevision || m.clusterTargetRevision || '—'}
                  </Typography>
                  <TextField
                    size="small"
                    fullWidth
                    label="Git ref"
                    placeholder={m.clusterTargetRevision || 'branch / tag / SHA'}
                    value={refDraft[m.name] ?? ''}
                    disabled={busy === m.name}
                    onChange={(e) =>
                      setRefDraft((prev) => ({ ...prev, [m.name]: e.target.value }))
                    }
                    sx={{ mt: 1 }}
                  />
                  <Box sx={{ mt: 1, display: 'flex', gap: 1, flexWrap: 'wrap' }}>
                    {m.enabled ? (
                      <Button
                        size="small"
                        variant="outlined"
                        disabled={busy === m.name}
                        onClick={() => void applyRef(m)}
                      >
                        Apply ref
                      </Button>
                    ) : null}
                  </Box>
                  <FormControlLabel
                    sx={{ mt: 1, display: 'block' }}
                    control={
                      <Switch
                        checked={m.enabled}
                        disabled={busy === m.name}
                        onChange={(_, v) => void toggle(m, v)}
                      />
                    }
                    label={m.enabled ? 'Enabled' : 'Disabled'}
                  />
                  {(m.hardDependencies?.length || m.softDependencies?.length) ? (
                    <Typography variant="caption" display="block" color="text.secondary" sx={{ mt: 1 }}>
                      {m.hardDependencies?.length ? (
                        <>Hard deps: {m.hardDependencies.join(', ')}. </>
                      ) : null}
                      {m.softDependencies?.length ? (
                        <>Soft deps: {m.softDependencies.join(', ')}</>
                      ) : null}
                    </Typography>
                  ) : null}
                  {m.enabled && (m.hardDependents?.length || m.softDependents?.length) ? (
                    <Typography variant="caption" display="block" color="text.secondary">
                      {m.hardDependents?.length ? (
                        <>Hard dependents: {m.hardDependents.join(', ')}. </>
                      ) : null}
                      {m.softDependents?.length ? (
                        <>Soft dependents: {m.softDependents.join(', ')}</>
                      ) : null}
                    </Typography>
                  ) : null}
                </CardContent>
              </Card>
            </Grid>
          );
        })}
      </Grid>
    </Box>
  );
}
