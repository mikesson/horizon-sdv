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
import { useEffect, useState } from 'react';

const STORAGE_KEY = 'devportal.maxWorkflowLogLines';

export const DEFAULT_MAX_WORKFLOW_LOG_LINES = 50_000;
export const MIN_MAX_WORKFLOW_LOG_LINES = 5_000;
/** Upper bound keeps in-browser log buffers from growing without a hard cap (~hundreds of MB at long lines). */
export const MAX_MAX_WORKFLOW_LOG_LINES = 200_000;

export function readMaxWorkflowLogLines(): number {
  if (typeof window === 'undefined') {
    return DEFAULT_MAX_WORKFLOW_LOG_LINES;
  }
  const raw = localStorage.getItem(STORAGE_KEY);
  const n = raw === null ? NaN : Number.parseInt(raw, 10);
  if (!Number.isFinite(n)) {
    return DEFAULT_MAX_WORKFLOW_LOG_LINES;
  }
  return Math.min(MAX_MAX_WORKFLOW_LOG_LINES, Math.max(MIN_MAX_WORKFLOW_LOG_LINES, n));
}

export function writeMaxWorkflowLogLines(n: number): number {
  const v = Math.min(
    MAX_MAX_WORKFLOW_LOG_LINES,
    Math.max(MIN_MAX_WORKFLOW_LOG_LINES, Math.round(n)),
  );
  if (typeof window !== 'undefined') {
    localStorage.setItem(STORAGE_KEY, String(v));
    window.dispatchEvent(new Event('devportal-max-log-lines'));
  }
  return v;
}

export function useMaxWorkflowLogLines(): number {
  const [v, setV] = useState(readMaxWorkflowLogLines);
  useEffect(() => {
    const sync = () => setV(readMaxWorkflowLogLines());
    window.addEventListener('storage', sync);
    window.addEventListener('devportal-max-log-lines', sync);
    return () => {
      window.removeEventListener('storage', sync);
      window.removeEventListener('devportal-max-log-lines', sync);
    };
  }, []);
  return v;
}
