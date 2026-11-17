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
 * Pins the card's two competing click targets. The whole card selects, and the
 * actions menu inside it must not: the guard that keeps them apart used to be a
 * click handler on a wrapper div, which made a non-interactive element the only
 * thing standing between opening a menu and silently changing the selection.
 * It now rides the trigger button, and this is what proves the move preserved
 * the behaviour rather than just satisfying a linter.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import {InstanceCardComponent} from './instance-card.component';

describe('InstanceCardComponent selection guard', () => {
  let fixture: ComponentFixture<InstanceCardComponent>;
  let root: HTMLElement;
  let selections: number;

  beforeEach(() => {
    TestBed.configureTestingModule({imports: [InstanceCardComponent]});
    fixture = TestBed.createComponent(InstanceCardComponent);
    fixture.componentRef.setInput('instance', {
      id: 'i-1',
      name: 'instance-1',
      template: 'phone',
      cpus: 4,
      memoryMb: 4096,
      build: 'sdv_core_cf-img-user.zip',
    });
    fixture.componentRef.setInput('selected', false);
    fixture.componentRef.setInput('canDelete', true);

    selections = 0;
    fixture.componentInstance.cardSelect.subscribe(() => {
      selections++;
    });
    fixture.detectChanges();
    root = fixture.nativeElement as HTMLElement;
  });

  it('selects the card when the body is clicked', () => {
    const body = root.querySelector<HTMLElement>('.card-content');
    expect(body).not.toBeNull();

    body?.dispatchEvent(new MouseEvent('click', {bubbles: true}));
    expect(selections).toBe(1);
  });

  it('does not select the card when the actions menu is opened', () => {
    const trigger = root.querySelector<HTMLElement>('.action-button');
    expect(trigger).not.toBeNull();

    trigger?.dispatchEvent(new MouseEvent('click', {bubbles: true}));
    expect(selections).toBe(0);
  });

  it('names the build the instance boots', () => {
    expect(root.querySelector('.instance-build')?.textContent?.trim()).toBe(
      'sdv_core_cf-img-user.zip'
    );
  });

  it('keeps the actions trigger a real button so it is keyboard reachable', () => {
    const trigger = root.querySelector<HTMLElement>('.action-button');
    expect(trigger?.tagName).toBe('BUTTON');
  });
});
