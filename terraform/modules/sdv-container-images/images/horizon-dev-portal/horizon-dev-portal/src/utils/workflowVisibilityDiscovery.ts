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
import type { WorkflowSummary } from '../types';

const CANONICAL_SOURCES = ['api', 'developer-portal', 'horizon-cli'] as const;

/** Walk OpenAPI 3 JSON and collect string enums relevant to workflow submitted-from. */
export function extractSubmittedFromEnumsFromOpenAPI(doc: unknown): string[] {
  const out = new Set<string>();
  const visit = (node: unknown) => {
    if (node === null || node === undefined) {
      return;
    }
    if (Array.isArray(node)) {
      node.forEach(visit);
      return;
    }
    if (typeof node !== 'object') {
      return;
    }
    const o = node as Record<string, unknown>;
    if (o.name === 'X-Horizon-Submitted-From' && o.schema && typeof o.schema === 'object') {
      const sch = o.schema as Record<string, unknown>;
      if (Array.isArray(sch.enum)) {
        for (const v of sch.enum) {
          if (typeof v === 'string' && v.trim()) {
            out.add(v.trim());
          }
        }
      }
    }
    const props = o.properties;
    if (props && typeof props === 'object' && !Array.isArray(props)) {
      const p = props as Record<string, unknown>;
      const sf = p.submittedFrom;
      if (sf && typeof sf === 'object') {
        const sch = sf as Record<string, unknown>;
        if (Array.isArray(sch.enum)) {
          for (const v of sch.enum) {
            if (typeof v === 'string' && v.trim()) {
              out.add(v.trim());
            }
          }
        }
      }
    }
    for (const v of Object.values(o)) {
      visit(v);
    }
  };
  visit(doc);
  for (const c of CANONICAL_SOURCES) {
    out.add(c);
  }
  return [...out].sort((a, b) => a.localeCompare(b));
}

export function collectSubmittedFromFromWorkflows(items: WorkflowSummary[]): string[] {
  const out = new Set<string>();
  for (const w of items) {
    const s = (w.submittedFrom ?? '').trim();
    if (s) {
      out.add(s);
    }
  }
  return [...out].sort((a, b) => a.localeCompare(b));
}

export function mergeSourceOptions(openapiEnums: string[], fromWorkflows: string[]): string[] {
  const out = new Set<string>();
  openapiEnums.forEach((x) => out.add(x));
  fromWorkflows.forEach((x) => out.add(x));
  return [...out].sort((a, b) => a.localeCompare(b));
}

/** No restriction when allowed is null/undefined. Empty array = hide all. */
export function workflowPassesSubmittedFromFilter(
  w: WorkflowSummary,
  allowed: string[] | null | undefined
): boolean {
  if (allowed == null) {
    return true;
  }
  if (allowed.length === 0) {
    return false;
  }
  const sf = (w.submittedFrom ?? '').trim();
  if (!sf) {
    return false;
  }
  return allowed.includes(sf);
}
