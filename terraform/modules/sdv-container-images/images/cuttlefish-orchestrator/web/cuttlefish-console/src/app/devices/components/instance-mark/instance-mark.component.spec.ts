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
import {InstanceMarkComponent} from './instance-mark.component';

const SVG_NS = 'http://www.w3.org/2000/svg';

/** Every value a tile can be asked to draw; the mapping's own spec pins what each means. */
const STATES = [
  'Running',
  'Preparing',
  'Starting',
  'Stopping',
  'Stopped',
  'Unreachable',
  undefined,
  '',
  'Prepare failed',
  'Boot failed',
  'Cancelled',
  'Unspecified',
  'Unknown',
];

describe('InstanceMarkComponent', () => {
  function render(state: string | undefined): HTMLElement {
    const fixture = TestBed.createComponent(InstanceMarkComponent);
    fixture.componentRef.setInput('state', state);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  function mark(host: HTMLElement): SVGSVGElement {
    const svg = host.querySelector('svg');
    if (!svg) {
      throw new Error('no mark rendered');
    }
    return svg;
  }

  /** The drawn shapes, without the control-flow comment anchors between them. */
  function shapes(svg: SVGSVGElement): string {
    return Array.from(svg.children)
      .map(child => child.outerHTML)
      .join('');
  }

  beforeEach(() => {
    TestBed.configureTestingModule({imports: [InstanceMarkComponent]});
  });

  it('draws the mark and writes the word for a running instance', () => {
    const host = render('Running');

    expect(mark(host).dataset['mark']).toBe('disc');
    expect(host.querySelector('.word')?.textContent?.trim()).toBe('running');
    expect(host.style.color).toBe('var(--cco-success)');
  });

  // The guarantee the component exists to hold: a shape on its own names
  // nothing, so no value may render one without the word beside it. Deleting
  // the word span, or letting any state answer with an empty word, goes red for
  // all thirteen.
  it('never renders a mark without its word', () => {
    for (const state of STATES) {
      const host = render(state);

      expect(mark(host)).toBeTruthy();
      expect(host.querySelector('.word')?.textContent?.trim()).not.toBe('');
    }
  });

  // The mark contributes nothing to the accessible name, so what a screen
  // reader announces is the word and only the word. An aria-hidden dropped from
  // the svg, or a <title> added inside it, would announce the shape twice.
  it('names itself with the word alone', () => {
    const host = render('Unreachable');

    expect(mark(host).getAttribute('aria-hidden')).toBe('true');
    expect(host.textContent?.trim()).toBe('unreachable');
  });

  // Nine marks that draw nine shapes. Two cases drawing the same geometry (the
  // filled disc and the half-filled one, say) is invisible to a data-mark
  // assertion and is exactly the confusion the vocabulary exists to prevent.
  it('draws a different shape for every mark', () => {
    const drawn = new Map<string, string>();
    for (const state of STATES) {
      const svg = mark(render(state));
      drawn.set(String(svg.dataset['mark']), shapes(svg));
    }

    expect(drawn.size).toBe(9);
    expect(new Set(drawn.values()).size).toBe(9);
  });

  // Control flow inside an <svg> that emits HTML-namespace children paints
  // nothing at all, and every assertion above still passes on the empty pixels.
  it('draws its shapes in the SVG namespace', () => {
    for (const state of STATES) {
      const svg = mark(render(state));

      expect(svg.namespaceURI).toBe(SVG_NS);
      for (const child of Array.from(svg.children)) {
        expect(child.namespaceURI).toBe(SVG_NS);
      }
    }
  });

  // currentColor is what carries the mark into both themes; a shape that pins
  // its own stroke or fill colour would survive the theme switch unchanged.
  it('takes its colour from the host and nothing else', () => {
    for (const state of STATES) {
      const svg = mark(render(state));

      expect(svg.getAttribute('stroke')).toBe('currentColor');
      expect(shapes(svg)).not.toMatch(/#[0-9a-f]{3}|rgba?\(|var\(/i);
    }
  });
});
