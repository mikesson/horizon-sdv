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
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import {ResizableSplitComponent} from './resizable-split.component';

interface Inputs {
  storageKey?: string;
  initialRatio?: number;
  minRatio?: number;
  maxRatio?: number;
  direction?: 'horizontal' | 'vertical';
}

async function render(inputs: Inputs = {}): Promise<{
  fixture: ComponentFixture<ResizableSplitComponent>;
  component: ResizableSplitComponent;
  emitted: number[];
}> {
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [ResizableSplitComponent],
    providers: [provideZonelessChangeDetection()],
  }).compileComponents();
  const fixture = TestBed.createComponent(ResizableSplitComponent);
  for (const [key, value] of Object.entries(inputs)) {
    fixture.componentRef.setInput(key, value);
  }
  const emitted: number[] = [];
  fixture.componentInstance.ratioChange.subscribe(r => emitted.push(r));
  fixture.detectChanges();
  return {fixture, component: fixture.componentInstance, emitted};
}

/** The container's measured box, which jsdom reports as all-zero by default. */
function stubContainer(
  fixture: ComponentFixture<ResizableSplitComponent>,
  rect: {left: number; top: number; width: number; height: number}
): void {
  const el = fixture.componentInstance.containerRef().nativeElement;
  el.getBoundingClientRect = () => ({
    left: rect.left,
    top: rect.top,
    width: rect.width,
    height: rect.height,
    right: rect.left + rect.width,
    bottom: rect.top + rect.height,
    x: rect.left,
    y: rect.top,
    toJSON: () => ({}),
  });
}

function handle(fixture: ComponentFixture<ResizableSplitComponent>): HTMLElement {
  return fixture.nativeElement.querySelector('.drag-handle') as HTMLElement;
}

/** Only the two coordinates the component reads; jsdom builds no real Touch. */
function touchPoint(x: number, y: number): Touch {
  const point = {clientX: x, clientY: y};
  return point as unknown as Touch;
}

describe('ResizableSplitComponent', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('starts at the initial ratio when nothing is stored', async () => {
    const {component} = await render({initialRatio: 0.4});

    expect(component.ratio()).toBe(0.4);
    // The two panels must always partition the container; a split that does
    // not sum to 100% leaves a gap or overlaps.
    expect(component.primaryStyle().flex).toBe('0 0 40%');
    expect(component.secondaryStyle().flex).toBe('0 0 60%');
  });

  it('restores a stored ratio in preference to the initial one', async () => {
    localStorage.setItem('split-a', '0.75');

    const {component} = await render({storageKey: 'split-a', initialRatio: 0.4});

    expect(component.ratio()).toBe(0.75);
  });

  it('falls back to the initial ratio when the stored value is unusable', async () => {
    for (const stored of ['not-a-number', '1.5', '-0.2', '']) {
      localStorage.setItem('split-b', stored);

      const {component} = await render({storageKey: 'split-b', initialRatio: 0.35});

      expect(component.ratio(), `stored ${JSON.stringify(stored)}`).toBe(0.35);
    }
  });

  it('clamps setRatio to the configured bounds and emits the clamped value', async () => {
    const {component, emitted} = await render({minRatio: 0.3, maxRatio: 0.8});

    component.setRatio(0.95);
    expect(component.ratio()).toBe(0.8);

    component.setRatio(0.05);
    expect(component.ratio()).toBe(0.3);

    // The emitted value is what a parent persists or lays out from, so it has
    // to be the clamped one, never the request.
    expect(emitted).toEqual([0.8, 0.3]);
  });

  it('persists through setRatio only when a storage key is configured', async () => {
    const keyed = await render({storageKey: 'split-c'});
    keyed.component.setRatio(0.55);
    expect(localStorage.getItem('split-c')).toBe('0.55');

    const unkeyed = await render({});
    unkeyed.component.setRatio(0.55);
    expect(localStorage.length).toBe(1); // still only split-c
  });

  it('resets to the initial ratio and announces it', async () => {
    const {component, emitted} = await render({initialRatio: 0.6});
    component.setRatio(0.85);
    emitted.length = 0;

    component.reset();

    expect(component.ratio()).toBe(0.6);
    expect(emitted).toEqual([0.6]);
  });

  it('tracks a horizontal drag against the container box and persists on release', async () => {
    const {fixture, component, emitted} = await render({
      storageKey: 'split-d',
      direction: 'horizontal',
    });
    stubContainer(fixture, {left: 100, top: 0, width: 400, height: 200});

    handle(fixture).dispatchEvent(new MouseEvent('mousedown', {bubbles: true}));
    expect(component.isDragging()).toBe(true);

    // 300px into a box starting at 100 and 400 wide => half way.
    document.dispatchEvent(new MouseEvent('mousemove', {clientX: 300, clientY: 0}));
    expect(component.ratio()).toBeCloseTo(0.5, 5);
    expect(emitted).toEqual([0.5]);

    document.dispatchEvent(new MouseEvent('mouseup'));
    expect(component.isDragging()).toBe(false);
    expect(localStorage.getItem('split-d')).toBe('0.5');
  });

  it('measures a vertical drag against the container height', async () => {
    const {fixture, component} = await render({direction: 'vertical'});
    stubContainer(fixture, {left: 0, top: 50, width: 400, height: 200});

    handle(fixture).dispatchEvent(new MouseEvent('mousedown', {bubbles: true}));
    document.dispatchEvent(new MouseEvent('mousemove', {clientX: 0, clientY: 150}));

    // Vertical reads clientY against top/height; a horizontal reading here
    // would have produced 0 from clientX.
    expect(component.ratio()).toBeCloseTo(0.5, 5);
  });

  it('ignores pointer movement once the drag has ended', async () => {
    const {fixture, component} = await render({});
    stubContainer(fixture, {left: 0, top: 0, width: 400, height: 200});

    handle(fixture).dispatchEvent(new MouseEvent('mousedown', {bubbles: true}));
    document.dispatchEvent(new MouseEvent('mousemove', {clientX: 200, clientY: 0}));
    document.dispatchEvent(new MouseEvent('mouseup'));
    const settled = component.ratio();

    document.dispatchEvent(new MouseEvent('mousemove', {clientX: 360, clientY: 0}));

    expect(component.ratio()).toBe(settled);
  });

  it('tracks a touch drag the same way as a mouse drag', async () => {
    const {fixture, component, emitted} = await render({
      storageKey: 'split-e',
      direction: 'horizontal',
    });
    stubContainer(fixture, {left: 0, top: 0, width: 400, height: 200});
    const touchAt = (x: number): TouchEvent =>
      new TouchEvent('touchmove', {touches: [touchPoint(x, 0)], cancelable: true});

    handle(fixture).dispatchEvent(new TouchEvent('touchstart', {bubbles: true, cancelable: true}));
    expect(component.isDragging()).toBe(true);

    document.dispatchEvent(touchAt(100));
    expect(component.ratio()).toBeCloseTo(0.25, 5);

    document.dispatchEvent(new TouchEvent('touchend'));
    expect(component.isDragging()).toBe(false);
    expect(emitted).toEqual([0.25]);
    expect(localStorage.getItem('split-e')).toBe('0.25');
  });

  it('ignores touch movement once the touch drag has ended', async () => {
    const {fixture, component} = await render({});
    stubContainer(fixture, {left: 0, top: 0, width: 400, height: 200});

    handle(fixture).dispatchEvent(new TouchEvent('touchstart', {bubbles: true, cancelable: true}));
    document.dispatchEvent(new TouchEvent('touchend'));
    const settled = component.ratio();

    document.dispatchEvent(
      new TouchEvent('touchmove', {
        touches: [touchPoint(360, 0)],
        cancelable: true,
      })
    );

    expect(component.ratio()).toBe(settled);
  });

  it('detaches its document listeners on destroy', async () => {
    const {fixture, component} = await render({});
    stubContainer(fixture, {left: 0, top: 0, width: 400, height: 200});
    handle(fixture).dispatchEvent(new MouseEvent('mousedown', {bubbles: true}));
    const beforeDestroy = component.ratio();

    fixture.destroy();
    document.dispatchEvent(new MouseEvent('mousemove', {clientX: 360, clientY: 0}));

    // A live listener on a destroyed component is the leak this guards.
    expect(component.ratio()).toBe(beforeDestroy);
  });
});
