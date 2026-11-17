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

/** Log entry severity levels. */
export type LogLevel = 'V' | 'D' | 'I' | 'W' | 'E' | 'F';

/** A log entry as shown in the viewer (mapped from binary logcat). */
export interface LogEntry {
  id: number;
  timestamp: Date;
  /** Precomputed HH:MM:SS.mmm; the viewer renders 10k rows, so format once. */
  timeLabel: string;
  level: LogLevel;
  tag: string;
  message: string;
  pid?: number;
  tid?: number;
  raw: string;
}

/** Client-side log filter configuration. */
export interface LogFilter {
  minLevel?: LogLevel;
  tags?: string[];
  excludeTags?: string[];
  searchText?: string;
  showOnlyErrors?: boolean;
}

const LOG_LEVEL_PRIORITY: Record<LogLevel, number> = {
  V: 0,
  D: 1,
  I: 2,
  W: 3,
  E: 4,
  F: 5,
};

/** Applies level/tag/search filtering to log entries. */
export function applyLogFilter(entries: readonly LogEntry[], filter: LogFilter): LogEntry[] {
  // A flat chain of independent guard clauses, one per optional filter field.
  // The score counts the number of filters, not nesting; splitting it into
  // per-field helpers would scatter the predicate without removing a branch.
  // The suppression sits on the line above the predicate because that is what
  // the rule reports; a line further up suppresses the comment, not the code.
  // biome-ignore lint/complexity/noExcessiveCognitiveComplexity: one guard per filter field.
  return entries.filter(entry => {
    if (filter.minLevel) {
      if (LOG_LEVEL_PRIORITY[entry.level] < LOG_LEVEL_PRIORITY[filter.minLevel]) {
        return false;
      }
    }

    if (filter.showOnlyErrors) {
      if (entry.level !== 'E' && entry.level !== 'F') {
        return false;
      }
    }

    if (filter.tags && filter.tags.length > 0) {
      const matchesTag = filter.tags.some(tag =>
        entry.tag.toLowerCase().includes(tag.toLowerCase())
      );
      if (!matchesTag) {
        return false;
      }
    }

    if (filter.excludeTags && filter.excludeTags.length > 0) {
      const excludeMatches = filter.excludeTags.some(tag =>
        entry.tag.toLowerCase().includes(tag.toLowerCase())
      );
      if (excludeMatches) {
        return false;
      }
    }

    if (filter.searchText) {
      const searchLower = filter.searchText.toLowerCase();
      const matchesSearch =
        entry.message.toLowerCase().includes(searchLower) ||
        entry.tag.toLowerCase().includes(searchLower);
      if (!matchesSearch) {
        return false;
      }
    }

    return true;
  });
}
