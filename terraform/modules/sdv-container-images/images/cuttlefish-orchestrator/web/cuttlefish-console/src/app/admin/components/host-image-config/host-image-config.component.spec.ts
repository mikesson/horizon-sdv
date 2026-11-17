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

/**
 * Driven through the rendered cards: the image status is only useful if an
 * operator can read it on the page, so the assertions read the DOM.
 */

import {provideHttpClient} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import {HostImageConfigComponent} from './host-image-config.component';

const NOW = '2026-10-18T00:00:00Z';

const LIST = {
  items: [
    {
      machine_family: 'n1',
      image_family: 'cco-host',
      enabled: true,
      created_at: NOW,
      updated_at: NOW,
      resolved: {ok: true, project: 'images-project', image_name: 'cco-host-20261018'},
    },
    {
      machine_family: 'c4a',
      image_family: 'cco-host-arm64',
      source_project: 'arm-images',
      enabled: true,
      created_at: NOW,
      updated_at: NOW,
      resolved: {
        ok: false,
        project: 'arm-images',
        reason: 'not_found',
        message: 'machine family c4a cannot boot a host',
      },
    },
  ],
  limit: 50,
  offset: 0,
  total: 2,
};

describe('HostImageConfigComponent', () => {
  let fixture: ComponentFixture<HostImageConfigComponent>;
  let httpMock: HttpTestingController;

  function root(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function card(family: string): HTMLElement {
    const match = Array.from(root().querySelectorAll('.config-card')).find(
      el => el.querySelector('.family-badge')?.textContent?.trim() === family
    );
    if (!match) {
      throw new Error(`no card for ${family}`);
    }
    return match as HTMLElement;
  }

  function detail(scope: HTMLElement, label: string): HTMLElement {
    const row = Array.from(scope.querySelectorAll('.detail-row')).find(
      el => el.querySelector('.detail-label')?.textContent?.trim() === label
    );
    const value = row?.querySelector('.detail-value');
    if (!(value instanceof HTMLElement)) {
      throw new Error(`no ${label} row`);
    }
    return value;
  }

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    fixture = TestBed.createComponent(HostImageConfigComponent);
    httpMock = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
    httpMock.expectOne('/v2/admin/host-image-configs').flush(LIST);
    fixture.detectChanges();
  });

  afterEach(() => httpMock.verify());

  it('shows the image a family resolves to', () => {
    const image = detail(card('n1'), 'Image');

    expect(image.textContent?.trim()).toBe('cco-host-20261018');
    expect(image.classList).toContain('image-ok');
  });

  it('shows why a family cannot boot', () => {
    const image = detail(card('c4a'), 'Image');

    expect(image.textContent?.trim()).toBe('not_found');
    expect(image.classList).toContain('image-unavailable');
  });

  it('names the deployment project for a row that sets none', () => {
    expect(detail(card('n1'), 'Source Project').textContent?.trim()).toBe(
      'images-project (deployment)'
    );
    expect(detail(card('c4a'), 'Source Project').textContent?.trim()).toBe('arm-images');
  });

  it('reads and offers no default image project', () => {
    httpMock.expectNone('/v2/admin/system-config/default-image-project');

    expect(root().textContent).not.toContain('Default Image Project');
  });
});
