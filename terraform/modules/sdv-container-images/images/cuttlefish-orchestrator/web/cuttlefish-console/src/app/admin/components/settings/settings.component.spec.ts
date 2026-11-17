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
 * Driven through the rendered DOM rather than the component's methods: the
 * capabilities these cover are reachable only through a button or a switch in
 * the template, so a test that called the methods would keep passing after the
 * markup that reaches them was deleted.
 */

import {provideHttpClient} from '@angular/common/http';
import {HttpTestingController, provideHttpClientTesting} from '@angular/common/http/testing';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {provideRouter} from '@angular/router';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import {SettingsComponent} from './settings.component';

const BASE = '/v2/admin/system-config';

/** What the fixture's server holds before the page touches anything. */
const SERVED = {
  lifetimes: {
    device_ttl_hours: 4,
    extend_hours: 2,
    extend_count: 3,
    host_ttl_hours: 10,
    is_set: false,
    max_device_ttl_hours: 8,
    max_extend_hours: 4,
    max_extend_count: 10,
    max_host_ttl_hours: 168,
    max_devices_per_host: 16,
    host_image_backed_slots: 10,
    grace_period_seconds: 900,
  },
  'zone-pin': {value: 'europe-west4-a', is_set: true},
  'quota-margin': {value: 10, is_set: false},
  'v1-zone-hint': {value: true, is_set: true},
  'spot-auto-default': {value: false, is_set: false},
  'spot-min-uptime': {single_seconds: 600, batch_seconds: 3600, is_set: false},
  'default-gpu-machine-type': {value: '', is_set: false},
  'default-upstream-build': {branch: 'aosp-main', target: 'phone', is_set: true},
  'docker-host': {machine_type: 'n1-standard-8', ttl_hours: 12, max_ttl_hours: 168, is_set: false},
  'artifact-buckets': {
    bugreport_bucket: 'stored-bugreports',
    recording_bucket: '',
    default_bucket: 'deployment-default',
    capture_on_error: false,
    is_set: true,
  },
} as const;

describe('SettingsComponent', () => {
  let fixture: ComponentFixture<SettingsComponent>;
  let httpMock: HttpTestingController;

  /** Answers the page's initial fan-out. */
  function serveLoad(): void {
    for (const [path, body] of Object.entries(SERVED)) {
      httpMock.expectOne(`${BASE}/${path}`).flush(body);
    }
    fixture.detectChanges();
  }

  function root(): HTMLElement {
    return fixture.nativeElement as HTMLElement;
  }

  function cardTitled(title: string): HTMLElement {
    // The section label sits beside the card, not inside it: the settings page
    // reads label-left, controls-right.
    const match = Array.from(root().querySelectorAll('app-settings-card')).find(
      card => card.querySelector('.section-intro h2')?.textContent?.trim() === title
    );
    if (!match) {
      throw new Error(`no settings card titled ${title}`);
    }
    return match as HTMLElement;
  }

  function buttonLabelled(scope: HTMLElement, label: string): HTMLButtonElement {
    const match = Array.from(scope.querySelectorAll('button')).find(
      el => el.textContent?.trim() === label
    );
    if (!match) {
      throw new Error(`no button labelled ${label}`);
    }
    return match;
  }

  function fieldLabelled(label: string): HTMLInputElement {
    const field = Array.from(root().querySelectorAll('mat-form-field')).find(
      el => el.querySelector('mat-label')?.textContent?.trim() === label
    );
    const input = field?.querySelector('input');
    if (!input) {
      throw new Error(`no input labelled ${label}`);
    }
    return input;
  }

  function type(label: string, value: string): void {
    const input = fieldLabelled(label);
    input.value = value;
    input.dispatchEvent(new Event('input'));
    fixture.detectChanges();
  }

  function flipSwitch(scope: HTMLElement): void {
    const control = scope.querySelector('button[role="switch"]');
    if (!(control instanceof HTMLButtonElement)) {
      throw new Error('the card renders no switch');
    }
    control.click();
    fixture.detectChanges();
  }

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    fixture = TestBed.createComponent(SettingsComponent);
    httpMock = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
  });

  afterEach(() => httpMock.verify());

  // IAM misconfiguration is invisible until a capture fails hours later, so
  // the probe has to ask for write specifically, and never for read alone.
  it('probes a bucket for write access and reports the answer', () => {
    serveLoad();
    const artifacts = cardTitled('Artifacts');

    buttonLabelled(artifacts, 'Verify write access').click();
    fixture.detectChanges();

    const req = httpMock.expectOne('/v2/storage/verify-access');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toMatchObject({bucket: 'stored-bugreports', check_write: true});
    req.flush({orchestrator_access: {has_access: true}, can_proceed: true});
    fixture.detectChanges();

    expect(buttonLabelled(artifacts, 'Writable')).toBeTruthy();
  });

  it('says so when the bucket is not writable', () => {
    serveLoad();
    const artifacts = cardTitled('Artifacts');

    buttonLabelled(artifacts, 'Verify write access').click();
    fixture.detectChanges();
    httpMock
      .expectOne('/v2/storage/verify-access')
      .flush({orchestrator_access: {has_access: false}, can_proceed: false});
    fixture.detectChanges();

    expect(buttonLabelled(artifacts, 'Not writable')).toBeTruthy();
  });

  it('probes the recording bucket against the deployment default', () => {
    serveLoad();
    const artifacts = cardTitled('Artifacts');

    // The recording override is empty in the fixture, so the effective bucket
    // is the deployment default and that is what must be probed.
    const buttons = Array.from(artifacts.querySelectorAll('button')).filter(
      el => el.textContent?.trim() === 'Verify write access'
    );
    buttons[1].click();
    fixture.detectChanges();

    const req = httpMock.expectOne('/v2/storage/verify-access');
    expect(req.request.body).toMatchObject({bucket: 'deployment-default', check_write: true});
    req.flush({orchestrator_access: {has_access: true}, can_proceed: true});
  });

  // The endpoint carries all three keys, so the toggle's body must come from
  // the server's copy or it persists bucket text nobody saved.
  it('flipping capture-on-error never writes an unsaved bucket edit', () => {
    serveLoad();
    type('Bugreport bucket', 'typed-but-not-saved');

    flipSwitch(cardTitled('Automatic capture'));

    const req = httpMock.expectOne(`${BASE}/artifact-buckets`);
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({
      bugreport_bucket: 'stored-bugreports',
      recording_bucket: '',
      capture_on_error: true,
    });
    req.flush({});
    fixture.detectChanges();

    // The typed edit is still the operator's to save.
    expect(fieldLabelled('Bugreport bucket').value).toBe('typed-but-not-saved');
    expect(buttonLabelled(cardTitled('Artifacts'), 'Save').disabled).toBe(false);
  });

  it('a failed flip keeps the sibling form and its unsaved edit on screen', () => {
    serveLoad();
    type('Zone pin', 'europe-west4-b');

    flipSwitch(cardTitled('Legacy API'));
    httpMock.expectOne(`${BASE}/v1-zone-hint`).flush('nope', {status: 503, statusText: 'nope'});
    fixture.detectChanges();

    // The failure belongs to the switch's card, not the form's.
    expect(cardTitled('Legacy API').querySelector('app-error-banner')).toBeTruthy();
    expect(cardTitled('Placement').querySelector('app-error-banner')).toBeNull();
    expect(fieldLabelled('Zone pin').value).toBe('europe-west4-b');
  });

  // admin.config is also emitted by catalog, machine-type and host-image
  // writes; only the settings ones belong on this page.
  it('leaves the other sections usable when one fails to load', () => {
    for (const [path, body] of Object.entries(SERVED)) {
      const req = httpMock.expectOne(`${BASE}/${path}`);
      if (path === 'artifact-buckets') {
        req.flush('down', {status: 503, statusText: 'down'});
      } else {
        req.flush(body);
      }
    }
    fixture.detectChanges();

    expect(cardTitled('Artifacts').querySelector('app-error-banner')).toBeTruthy();
    expect(fieldLabelled('Zone pin').value).toBe('europe-west4-a');
  });

  // The lifetimes card exists for its derived footer. It shows the ceiling and
  // the lease-raise mechanic nobody could otherwise compute. The 15-minute
  // margin must come from the wire's grace_period_seconds (900) and never from
  // a retyped constant. The values here differ from the compiled defaults so
  // a footer ignoring the draft goes red.
  it('derives the longest session and the lease consequence as the operator types', () => {
    serveLoad();

    type('Device session (hours)', '6');
    type('Extensions', '2');
    type('Hours each', '3');

    const derived = cardTitled('Session and lease lifetimes').querySelector('.derived');
    expect(derived?.textContent).toContain(
      'Longest possible session: 12 h (6 h + 2 × 3 h of extensions).'
    );
    expect(derived?.textContent).toContain('15-minute teardown margin');

    type('Extensions', '0');
    expect(derived?.textContent).toContain('no extensions once created');
  });

  it('saves the lifetime group as one PUT carrying all four values', () => {
    serveLoad();
    type('Device session (hours)', '6');

    buttonLabelled(cardTitled('Session and lease lifetimes'), 'Save').click();
    fixture.detectChanges();

    const req = httpMock.expectOne(`${BASE}/lifetimes`);
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({
      device_ttl_hours: 6,
      extend_hours: 2,
      extend_count: 3,
      host_ttl_hours: 10,
    });
    req.flush(SERVED.lifetimes);
    httpMock.expectOne(`${BASE}/lifetimes`).flush({...SERVED.lifetimes, device_ttl_hours: 6});
  });
});
