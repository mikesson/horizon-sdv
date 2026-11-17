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

import {afterEach, describe, expect, it, vi} from 'vitest';
import {BugreportArtifactSchema, RecordingArtifactSchema} from '../../devices/models/device.schema';
import {resolveBasePath} from './base-url';
import {downloadUrl} from './download';

describe('Subpath remediation', () => {
  let baseEl: HTMLBaseElement | null = null;

  const mountBase = (href: string): void => {
    baseEl?.remove();
    baseEl = document.createElement('base');
    baseEl.setAttribute('href', href);
    document.head.appendChild(baseEl);
  };

  afterEach(() => {
    baseEl?.remove();
    baseEl = null;
    vi.restoreAllMocks();
  });

  it('resolveBasePath is idempotent for bare subpath prefix with query or hash', () => {
    mountBase('/cuttlefish-orchestrator/');

    expect(resolveBasePath('/cuttlefish-orchestrator?x=1')).toBe('/cuttlefish-orchestrator?x=1');
    expect(resolveBasePath('/cuttlefish-orchestrator#sec')).toBe('/cuttlefish-orchestrator#sec');
    expect(resolveBasePath('/cuttlefish-orchestrator/v2/me?a=1#b')).toBe(
      '/cuttlefish-orchestrator/v2/me?a=1#b'
    );
    expect(resolveBasePath('/cuttlefish-orchestrator-other?x=1')).toBe(
      '/cuttlefish-orchestrator/cuttlefish-orchestrator-other?x=1'
    );
  });

  it('RecordingArtifactSchema and BugreportArtifactSchema prefix downloadPath under subpath', () => {
    mountBase('/cuttlefish-orchestrator/');

    const recording = RecordingArtifactSchema.parse({
      bucket: 'cco-artifacts',
      object: 'recordings/rec-1.mp4',
      gs_uri: 'gs://cco-artifacts/recordings/rec-1.mp4',
      download_path: '/v2/storage/buckets/cco-artifacts/objects/recordings/rec-1.mp4',
    });
    expect(recording.downloadPath).toBe(
      '/cuttlefish-orchestrator/v2/storage/buckets/cco-artifacts/objects/recordings/rec-1.mp4'
    );

    const bugreport = BugreportArtifactSchema.parse({
      bucket: 'cco-artifacts',
      object: 'bugreports/br-1.zip',
      gs_uri: 'gs://cco-artifacts/bugreports/br-1.zip',
      download_path: '/v2/storage/buckets/cco-artifacts/objects/bugreports/br-1.zip',
    });
    expect(bugreport.downloadPath).toBe(
      '/cuttlefish-orchestrator/v2/storage/buckets/cco-artifacts/objects/bugreports/br-1.zip'
    );

    let downloadedHref: string | null = null;
    const origCreateElement = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation((tagName: string) => {
      const el = origCreateElement(tagName);
      if (tagName.toLowerCase() === 'a') {
        const anchor = el as HTMLAnchorElement;
        vi.spyOn(anchor, 'click').mockImplementation(() => {
          downloadedHref = anchor.getAttribute('href');
        });
      }
      return el;
    });

    downloadUrl(recording.downloadPath, 'rec-1.mp4');
    expect(downloadedHref).toBe(
      '/cuttlefish-orchestrator/v2/storage/buckets/cco-artifacts/objects/recordings/rec-1.mp4'
    );
  });
});
