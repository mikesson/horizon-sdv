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

import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {DeviceInstance} from '../../models/device.model';
import {InstanceTileComponent} from './instance-tile.component';

describe('InstanceTileComponent status mark', () => {
  function render(instance: DeviceInstance): HTMLElement {
    const fixture = TestBed.createComponent(InstanceTileComponent);
    fixture.componentRef.setInput('instance', instance);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  beforeEach(() => {
    TestBed.configureTestingModule({imports: [InstanceTileComponent]});
  });

  it('draws the mark for the state the host reported', () => {
    const host = render({name: 'i1', state: 'Boot failed'});

    expect(host.querySelector('svg')?.dataset['mark']).toBe('triangle');
    expect(host.querySelector('.word')?.textContent?.trim()).toBe('failed');
  });

  // The tile hands the raw wire state over untouched. Substituting a label of
  // its own here (the "Not reported" this tile used to print) is what put a
  // second name for absent on the page.
  it('reads an instance the host did not list as absent', () => {
    const host = render({name: 'i1'});

    expect(host.querySelector('svg')?.dataset['mark']).toBe('square-dashed');
    expect(host.querySelector('.word')?.textContent?.trim()).toBe('absent');
  });

  // One instance, one mark. A second mark on the tile would be a claim about
  // some other state machine (the device's) thirty pixels from this one.
  it('carries exactly one mark', () => {
    const host = render({name: 'i1', state: 'Running'});

    expect(host.querySelectorAll('app-instance-mark').length).toBe(1);
    expect(host.querySelectorAll('svg[data-mark]').length).toBe(1);
  });

  // The word is the tile's whole account of the state, so it must not be
  // squeezed or clipped by the tile it sits in.
  it('leaves the word unwrapped and unabbreviated', () => {
    const host = render({name: 'i1', state: 'Unreachable'});
    const word = host.querySelector('.word');

    expect(word?.textContent?.trim()).toBe('unreachable');
    expect(word && getComputedStyle(word).whiteSpace).toBe('nowrap');
  });
});
