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
import {provideRouter} from '@angular/router';
import {describe, expect, it} from 'vitest';
import {DeviceCreateChooserComponent} from './device-create-chooser.component';

function render(params: {hostId?: string; buildId?: string} = {}): HTMLElement {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    providers: [provideZonelessChangeDetection(), provideRouter([])],
  });
  const fixture = TestBed.createComponent(DeviceCreateChooserComponent);
  if (params.hostId) {
    fixture.componentRef.setInput('hostId', params.hostId);
  }
  if (params.buildId) {
    fixture.componentRef.setInput('buildId', params.buildId);
  }
  fixture.detectChanges();
  return fixture.nativeElement as HTMLElement;
}

function cards(root: HTMLElement): HTMLAnchorElement[] {
  return Array.from(root.querySelectorAll<HTMLAnchorElement>('a.mode-card'));
}

/** The name a screen reader announces: the card's title, by aria-labelledby. */
function accessibleName(root: HTMLElement, card: HTMLAnchorElement): string {
  const id = card.getAttribute('aria-labelledby') ?? '';
  return root.querySelector(`#${id}`)?.textContent?.trim() ?? '';
}

describe('DeviceCreateChooserComponent', () => {
  it('offers Simple, Advanced and the SDV pair as links to their modes', () => {
    const root = render();

    const links = cards(root);
    expect(links.map(card => accessibleName(root, card))).toEqual([
      'Simple',
      'Advanced',
      'AAOS SDV Core + IVI',
    ]);
    expect(links.map(card => card.getAttribute('href'))).toEqual([
      '/devices/create/simple',
      '/devices/create/advanced',
      '/devices/create/advanced?preset=sdv-pair',
    ]);
  });

  // Named by the title alone, so the facts list is not read out as the name,
  // and described by the one-sentence summary.
  it('describes each card with its summary', () => {
    const root = render();

    expect(
      cards(root).map(card =>
        root.querySelector(`#${card.getAttribute('aria-describedby')}`)?.textContent?.trim()
      )
    ).toEqual([
      'Answer a few questions and CCO picks the host, build and settings.',
      'Full CVD configuration, as a form or as the JSON config.',
      'An Android Automotive SDV pair, set up for you in Advanced mode.',
    ]);
  });

  // The SDV pair takes a build per instance. A handoff build would become the
  // device build and fill both instances' gaps, so that card drops it. The
  // host stays, because the SDV host warning reads it.
  it('carries a handoff host into every mode and the build into all but the SDV pair', () => {
    const root = render({hostId: 'h1', buildId: 'b1'});

    expect(cards(root).map(card => card.getAttribute('href'))).toEqual([
      '/devices/create/simple?hostId=h1&buildId=b1',
      '/devices/create/advanced?hostId=h1&buildId=b1',
      '/devices/create/advanced?hostId=h1&preset=sdv-pair',
    ]);
  });
});
