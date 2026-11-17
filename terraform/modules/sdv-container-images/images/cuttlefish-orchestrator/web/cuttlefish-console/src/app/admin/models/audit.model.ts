/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/** One audit-log entry from GET /v2/admin/audit. */
export interface AuditEntry {
  id: string;
  timestamp: Date;
  actorEmail: string;
  /** Dotted action code, e.g. "device.create" or "quota.update". */
  action: string;
  /** host | device | quota | system | task. */
  resourceType: string;
  resourceId: string;
  changes: Record<string, unknown>;
  metadata: Record<string, unknown>;
}

/** A page of audit entries plus the filtered total. */
export interface AuditPage {
  items: AuditEntry[];
  total: number;
}

/** Server-side filters for the audit list; empty fields are omitted. */
export interface AuditFilter {
  actorEmail?: string;
  action?: string;
  resourceType?: string;
  resourceId?: string;
  /**
   * Metadata kind the writer stamped, the discriminator that separates
   * settings changes (system_config) from the other admin.config writes.
   */
  kind?: string;
  /** Only entries at or after this time. */
  startTime?: Date;
  /** Only entries at or before this time. */
  endTime?: Date;
}

/** The category quick-filter: each value is a metadata kind writers stamp. */
export const AUDIT_KINDS: ReadonlyArray<{value: string; label: string}> = [
  {value: '', label: 'All categories'},
  {value: 'system_config', label: 'Settings changes'},
  {value: 'host_image_config', label: 'Host image config'},
  {value: 'machine_type', label: 'Machine-type curation'},
  {value: 'zone', label: 'Zone curation'},
];

/** The action codes the backend emits, for the Activity filter select. */
export const AUDIT_ACTIONS: readonly string[] = [
  'host.create',
  'host.start',
  'host.stop',
  'host.delete',
  'host.retry',
  'host.update',
  'device.create',
  'device.start',
  'device.stop',
  'device.delete',
  'device.pin',
  'device.unpin',
  'device.extend',
  'quota.update',
  'admin.config',
  'admin.purge',
  'task.retry',
  'task.delete',
];

/** Route to the resource an entry concerns; null when not linkable. */
export function auditResourceRoute(entry: AuditEntry): string[] | null {
  if (!entry.resourceId) {
    return null;
  }
  switch (entry.resourceType) {
    case 'device':
      return ['/devices', entry.resourceId];
    case 'host':
      return ['/hosts', entry.resourceId];
    default:
      return null;
  }
}
