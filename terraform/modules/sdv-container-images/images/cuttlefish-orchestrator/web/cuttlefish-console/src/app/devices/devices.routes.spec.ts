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
 * The create flow's routing: the chooser, the two modes, the handoff params
 * they pass along, the SDV preset entry, and the flag that hides Advanced.
 * The evidence is the URL the router settles on and the state the editors
 * open with, since every screen here renders something either way.
 */

import {provideHttpClient} from '@angular/common/http';
import {provideHttpClientTesting} from '@angular/common/http/testing';
import {provideZonelessChangeDetection, signal} from '@angular/core';
import {TestBed} from '@angular/core/testing';
import {By} from '@angular/platform-browser';
import {provideRouter, Router, withComponentInputBinding} from '@angular/router';
import {RouterTestingHarness} from '@angular/router/testing';
import {beforeEach, describe, expect, it, vi} from 'vitest';

// The Advanced editor embeds the JSON editor, whose codemirror-json-schema
// subpath imports do not resolve under the test runner.
vi.mock('codemirror-json-schema', () => ({jsonSchema: () => []}));

import {AuthStore} from '../auth/store/auth.signal-store';
import {FeatureFlagsService} from '../shared/services';
import {DeviceCreateAdvancedComponent} from './components/device-create-advanced/device-create-advanced.component';
import {DeviceCreateChooserComponent} from './components/device-create-chooser/device-create-chooser.component';
import {DeviceCreatePageComponent} from './components/device-create-page/device-create-page.component';
import {DEVICE_ROUTES} from './devices.routes';

function setUp(advancedEnabled: boolean): Promise<RouterTestingHarness> {
  localStorage.clear();
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    providers: [
      provideZonelessChangeDetection(),
      provideRouter([{path: 'devices', children: DEVICE_ROUTES}], withComponentInputBinding()),
      provideHttpClient(),
      provideHttpClientTesting(),
      {provide: AuthStore, useValue: {isLoading: signal(false), isAuthenticated: signal(true)}},
      {provide: FeatureFlagsService, useValue: {advancedCvdConfigEnabled: advancedEnabled}},
    ],
  });
  return RouterTestingHarness.create();
}

function url(): string {
  return TestBed.inject(Router).url;
}

function element(harness: RouterTestingHarness): HTMLElement {
  const root = harness.routeNativeElement;
  if (!root) {
    throw new Error('the route rendered no element');
  }
  return root;
}

function advancedEditor(harness: RouterTestingHarness): DeviceCreateAdvancedComponent {
  const debug = harness.routeDebugElement?.query(By.directive(DeviceCreateAdvancedComponent));
  if (!debug) {
    throw new Error('the Advanced editor is not rendered');
  }
  return debug.componentInstance as DeviceCreateAdvancedComponent;
}

/** Header controls by visible text; a mat-icon's ligature name is part of it. */
function headerHas(harness: RouterTestingHarness, text: string): boolean {
  return Array.from(
    element(harness).querySelectorAll('app-page-header a, app-page-header button')
  ).some(el => el.textContent?.includes(text));
}

describe('device create routes', () => {
  describe('with Advanced mode enabled', () => {
    let harness: RouterTestingHarness;

    beforeEach(async () => {
      harness = await setUp(true);
    });

    it('opens on the chooser', async () => {
      await harness.navigateByUrl('/devices/create', DeviceCreateChooserComponent);

      expect(url()).toBe('/devices/create');
    });

    it('opens each mode on its own path', async () => {
      await harness.navigateByUrl('/devices/create/simple', DeviceCreatePageComponent);
      expect(element(harness).querySelector('app-device-create-wizard')).not.toBeNull();

      await harness.navigateByUrl('/devices/create/advanced', DeviceCreatePageComponent);
      expect(element(harness).querySelector('app-device-create-advanced')).not.toBeNull();
    });

    it('sends an unknown mode back to the chooser with its handoff', async () => {
      await harness.navigateByUrl('/devices/create/expert?hostId=h1');

      expect(url()).toBe('/devices/create?hostId=h1');
    });

    it('offers Change mode in both modes and Continue in Advanced in Simple only', async () => {
      await harness.navigateByUrl('/devices/create/simple?hostId=h1', DeviceCreatePageComponent);
      expect(headerHas(harness, 'Change mode')).toBe(true);
      expect(headerHas(harness, 'Continue in Advanced')).toBe(true);
      // The Plan step renders eagerly inside the stepper, so its link is in
      // the DOM before the step is reached.
      expect(element(harness).querySelector('.customize-link')).not.toBeNull();
      const change = element(harness).querySelector('a.change-mode');
      expect(change?.getAttribute('href')).toBe('/devices/create?hostId=h1');

      await harness.navigateByUrl('/devices/create/advanced', DeviceCreatePageComponent);
      expect(headerHas(harness, 'Change mode')).toBe(true);
      expect(headerHas(harness, 'Continue in Advanced')).toBe(false);
    });

    it('pre-selects a handoff host and build in Advanced', async () => {
      await harness.navigateByUrl(
        '/devices/create/advanced?hostId=h1&buildId=b1',
        DeviceCreatePageComponent
      );

      const editor = advancedEditor(harness);
      expect(editor.hostChoice()).toEqual({kind: 'host', id: 'h1'});
      expect(editor.buildChoice()).toEqual({kind: 'registered', id: 'b1'});
    });

    it('pre-answers Simple from a handoff', async () => {
      const page = await harness.navigateByUrl(
        '/devices/create/simple?hostId=h1&buildId=b1',
        DeviceCreatePageComponent
      );

      expect(page.intent().host).toEqual({kind: 'host', id: 'h1'});
      expect(page.intent().build).toEqual({kind: 'registered', id: 'b1'});
    });

    it('applies the SDV preset once and drops it from the URL', async () => {
      await harness.navigateByUrl(
        '/devices/create/advanced?preset=sdv-pair&hostId=h1',
        DeviceCreatePageComponent
      );
      await harness.fixture.whenStable();

      const editor = advancedEditor(harness);
      expect(editor.instances().map(i => i.name)).toEqual(['core', 'ivi']);
      expect(editor.hostChoice()).toEqual({kind: 'host', id: 'h1'});
      // Left in the URL, a reload would apply the preset again over the
      // pair the user has edited since.
      expect(url()).toBe('/devices/create/advanced?hostId=h1');
    });

    it('ignores the preset in Simple', async () => {
      await harness.navigateByUrl(
        '/devices/create/simple?preset=sdv-pair',
        DeviceCreatePageComponent
      );
      await harness.fixture.whenStable();

      expect(url()).toBe('/devices/create/simple');
    });

    it('carries the Simple answers into Advanced on the same page instance', async () => {
      const page = await harness.navigateByUrl(
        '/devices/create/simple?hostId=h1',
        DeviceCreatePageComponent
      );
      page.intent.update(intent => ({...intent, name: 'carried-name', usage: 'headless'}));

      page.continueInAdvanced();
      await harness.fixture.whenStable();

      expect(url()).toBe('/devices/create/advanced?hostId=h1');
      // A new instance would have started from the stored draft, not from the
      // answers above, so the seed only arrives if the page was reused.
      expect(harness.routeDebugElement?.componentInstance).toBe(page);
      const editor = advancedEditor(harness);
      expect(editor.instances().map(i => i.name)).toEqual(['carried-name']);
      expect(editor.instances()[0].gpuMode).toBe('swiftshader');
      expect(editor.hostChoice()).toEqual({kind: 'host', id: 'h1'});
    });
  });

  describe('with Advanced mode disabled', () => {
    let harness: RouterTestingHarness;

    beforeEach(async () => {
      harness = await setUp(false);
    });

    it('skips the chooser and keeps the handoff', async () => {
      await harness.navigateByUrl('/devices/create?buildId=b1', DeviceCreatePageComponent);

      expect(url()).toBe('/devices/create/simple?buildId=b1');
    });

    it('does not open Advanced by URL', async () => {
      await harness.navigateByUrl('/devices/create/advanced', DeviceCreatePageComponent);

      expect(url()).toBe('/devices/create/simple');
      expect(element(harness).querySelector('app-device-create-advanced')).toBeNull();
    });

    it('shows no way into Advanced', async () => {
      await harness.navigateByUrl('/devices/create/simple', DeviceCreatePageComponent);

      expect(headerHas(harness, 'Change mode')).toBe(false);
      expect(headerHas(harness, 'Continue in Advanced')).toBe(false);
      expect(element(harness).querySelector('.customize-link')).toBeNull();
    });
  });
});
