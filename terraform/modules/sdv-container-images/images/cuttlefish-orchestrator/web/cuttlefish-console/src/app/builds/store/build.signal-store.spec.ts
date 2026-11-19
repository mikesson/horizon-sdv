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

import {provideZonelessChangeDetection} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {MatSnackBar} from '@angular/material/snack-bar';
import {provideRouter} from '@angular/router';
import {of, throwError} from 'rxjs';
import {beforeEach, describe, expect, it, vi} from 'vitest';
import type {Build, CreateBuildRequest} from '../models/build.model';
import {BuildApiService} from '../services/build-api.service';
import {BuildStore} from './build.signal-store';

function makeBuild(overrides: Partial<Build> = {}): Build {
  return {
    id: 'build-1',
    name: 'aosp-main-cf',
    source: 'android_build',
    buildId: '123456',
    target: 'aosp_cf_x86_64_only_phone-userdebug',
    branch: 'aosp-main',
    isDefault: false,
    ...overrides,
  };
}

describe('BuildStore', () => {
  let api: {
    listAll: ReturnType<typeof vi.fn>;
    create: ReturnType<typeof vi.fn>;
    update: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
  };
  let snackBar: {open: ReturnType<typeof vi.fn>};

  beforeEach(() => {
    api = {
      listAll: vi.fn(() =>
        of({
          items: [
            makeBuild({id: 'b-1', source: 'android_build', isDefault: true}),
            makeBuild({
              id: 'b-2',
              name: 'custom-gcs',
              source: 'url',
              buildId: undefined,
              target: undefined,
              artifactSources: [{type: 'gcs', url: 'gs://bkt/img.zip', role: 'default_build'}],
            }),
          ],
          total: 2,
          complete: true,
        })
      ),
      create: vi.fn((req: CreateBuildRequest) =>
        of(
          makeBuild({
            id: 'b-new',
            name: req.name,
            source: req.source,
            buildId: req.buildId,
            target: req.target,
          })
        )
      ),
      update: vi.fn((id: string, req: Partial<CreateBuildRequest>) =>
        of(makeBuild({id, name: req.name ?? 'updated', isDefault: req.isDefault ?? false}))
      ),
      delete: vi.fn(() => of(void 0)),
    };
    snackBar = {open: vi.fn()};

    TestBed.configureTestingModule({
      providers: [
        provideZonelessChangeDetection(),
        provideRouter([
          {path: 'builds', children: []},
          {path: 'devices/create', children: []},
        ]),
        {provide: BuildApiService, useValue: api},
        {provide: MatSnackBar, useValue: snackBar},
      ],
    });
  });

  it('loads builds and computes stats, defaultBuild, and availableBuildsForDevice', () => {
    const store = TestBed.inject(BuildStore);
    store.loadAll(undefined);

    expect(api.listAll).toHaveBeenCalled();
    expect(store.stats()).toEqual({total: 2, official: 1, custom: 1});
    expect(store.defaultBuild()?.id).toBe('b-1');
    expect(store.selectedBuild()).toBeNull();
    expect(store.availableBuildsForDevice()).toHaveLength(2);
  });

  it('creates, updates, and deletes builds via BuildApiService', () => {
    const store = TestBed.inject(BuildStore);
    store.loadAll(undefined);

    store.create({
      name: 'ci-build-2',
      source: 'android_build',
      buildId: '999999',
      target: 'aosp_cf_x86_64_only_phone-userdebug',
    });
    expect(api.create).toHaveBeenCalled();
    expect(store.entities().some(b => b.id === 'b-new')).toBe(true);

    store.update({id: 'b-new', request: {name: 'ci-build-renamed'}});
    expect(api.update).toHaveBeenCalledWith('b-new', {name: 'ci-build-renamed'});
    expect(store.entities().find(b => b.id === 'b-new')?.name).toBe('ci-build-renamed');

    store.delete('b-new');
    expect(api.delete).toHaveBeenCalledWith('b-new');
    expect(store.entities().some(b => b.id === 'b-new')).toBe(false);
    expect(snackBar.open).toHaveBeenCalled();
  });

  it('handles create failure without corrupting loaded builds', () => {
    const store = TestBed.inject(BuildStore);
    store.loadAll(undefined);

    api.create.mockReturnValue(throwError(() => new Error('duplicate build')));
    store.create({
      name: 'dup',
      source: 'android_build',
      buildId: '123456',
      target: 'aosp_cf_x86_64_only_phone-userdebug',
    });

    expect(store.entities()).toHaveLength(2);
    expect(store.error()).toContain('duplicate build');
  });
});
