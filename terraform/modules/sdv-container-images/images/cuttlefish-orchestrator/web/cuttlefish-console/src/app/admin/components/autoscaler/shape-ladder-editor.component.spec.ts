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

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import type {MachineType} from '../../../machine-types';
import type {LadderRung} from '../../models/autoscaler.model';
import {ShapeLadderEditorComponent} from './shape-ladder-editor.component';

function machineTypeOf(id: string, overrides: Partial<MachineType> = {}): MachineType {
  return {
    id,
    family: id.split('-')[0],
    name: id,
    category: 'general_purpose',
    architecture: 'x86',
    vcpus: 8,
    memoryGb: 30,
    fit: 3,
    gpuSupport: false,
    maxGpus: 0,
    defaultGpuCount: 0,
    gpuMode: 'none',
    integratedGpuCount: 0,
    isDefault: false,
    isEnabled: true,
    sortOrder: 1,
    ...overrides,
  };
}

/** One shape as the server judges it: placeable everywhere, nothing observed. */
function rungOf(machineType: string, over: Partial<LadderRung> = {}): LadderRung {
  return {
    machineType,
    machineFamily: machineType.split('-')[0],
    slots: 3,
    zones: 3,
    stockedOutZones: 0,
    newestStockoutAt: null,
    gpuType: '',
    gpuCount: 0,
    landing: false,
    refused: '',
    ...over,
  };
}

const CATALOG = [
  machineTypeOf('n1-standard-8'),
  machineTypeOf('n2-standard-8'),
  machineTypeOf('n2-standard-16'),
];

/** What the server says, when a case needs it to say anything. */
interface Served {
  rungs?: LadderRung[];
  defaultShape?: string;
  defaultSlots?: number;
  pool?: string;
}

describe('ShapeLadderEditorComponent', () => {
  let fixture: ComponentFixture<ShapeLadderEditorComponent>;
  let emitted: string[][];

  /** Renders the editor over `ladder` and records every order it emits. */
  function render(
    ladder: string[],
    maxRungs = 8,
    machineTypes: MachineType[] = CATALOG,
    served: Served = {}
  ): HTMLElement {
    fixture = TestBed.createComponent(ShapeLadderEditorComponent);
    fixture.componentRef.setInput('pool', served.pool ?? 'default');
    fixture.componentRef.setInput('ladder', ladder);
    fixture.componentRef.setInput('maxRungs', maxRungs);
    fixture.componentRef.setInput('machineTypes', machineTypes);
    fixture.componentRef.setInput('rungs', served.rungs ?? []);
    fixture.componentRef.setInput('defaultShape', served.defaultShape ?? '');
    fixture.componentRef.setInput('defaultSlots', served.defaultSlots ?? 0);
    fixture.componentInstance.ladderChange.subscribe((next: string[]) => emitted.push(next));
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  function click(root: HTMLElement, ariaLabel: string): void {
    const button = root.querySelector<HTMLButtonElement>(`button[aria-label="${ariaLabel}"]`);
    if (!button) {
      throw new Error(`no ${ariaLabel} button`);
    }
    button.click();
    fixture.detectChanges();
  }

  function buttonWith(root: HTMLElement, label: string): HTMLButtonElement {
    const button = Array.from(root.querySelectorAll('button')).find(b =>
      b.textContent?.includes(label)
    );
    if (!button) {
      throw new Error(`no ${label} button`);
    }
    return button;
  }

  function textOf(el: Element | null | undefined): string {
    return (el?.textContent ?? '').replace(/\s+/g, ' ').trim();
  }

  /** The state word beside every card, in the order the cards are drawn. */
  function words(root: HTMLElement): string[] {
    return Array.from(root.querySelectorAll('app-ladder-rung-card .word')).map(textOf);
  }

  function states(root: HTMLElement): Array<string | null> {
    return Array.from(root.querySelectorAll('app-ladder-rung-card')).map(card =>
      card.getAttribute('data-state')
    );
  }

  /** Opens one shape's picker and returns the overlay panel it put up. */
  function openPicker(root: HTMLElement, machineType: string): HTMLElement {
    click(root, `${machineType}, change this shape`);
    const panels = document.querySelectorAll<HTMLElement>('.mat-mdc-menu-panel');
    const panel = panels[panels.length - 1];
    if (!panel) {
      throw new Error(`the picker for ${machineType} did not open`);
    }
    return panel;
  }

  beforeEach(() => {
    emitted = [];
    TestBed.configureTestingModule({});
  });

  afterEach(() => {
    // The picker leaves an overlay behind, and the next test would find it.
    fixture.destroy();
  });

  // The order IS the feature. The first shape is what the fleet wants and
  // everything below it is what a stockout hands over to. Moving a shape has to
  // be its own affordance, because remove-and-re-add loses the position it was
  // being moved to.
  it('moves a shape up in the order', () => {
    const root = render(['n2-standard-8', 'n1-standard-8']);

    click(root, 'Move n1-standard-8 up');

    expect(emitted).toEqual([['n1-standard-8', 'n2-standard-8']]);
  });

  it('moves a shape down in the order', () => {
    const root = render(['n2-standard-8', 'n1-standard-8', 'n2-standard-16']);

    click(root, 'Move n1-standard-8 down');

    expect(emitted).toEqual([['n2-standard-8', 'n2-standard-16', 'n1-standard-8']]);
  });

  // The ends have nowhere to go, and an arrow that silently did nothing would
  // read as a broken control rather than as the top of the order.
  it('offers no move past either end of the order', () => {
    const root = render(['n2-standard-8', 'n1-standard-8']);

    expect(
      root.querySelector<HTMLButtonElement>('button[aria-label="Move n2-standard-8 up"]')?.disabled
    ).toBe(true);
    expect(
      root.querySelector<HTMLButtonElement>('button[aria-label="Move n1-standard-8 down"]')
        ?.disabled
    ).toBe(true);
  });

  // THE property the whole editor is built on. The draft is compared against the
  // server's copy structurally, so a mutation applied in place would leave both
  // sides equal, the order would change on screen and no Save would ever
  // appear. Asserted as identity AND as the input being untouched, because
  // either alone passes for a mutation that also copies.
  it('emits a new array for every mutation and never touches the one it was given', () => {
    const ladder = ['n2-standard-8', 'n1-standard-8'];
    const root = render(ladder);

    click(root, 'Move n1-standard-8 up');
    click(root, 'Remove n2-standard-8');
    buttonWith(root, 'Add a shape').click();
    fixture.detectChanges();

    expect(emitted).toHaveLength(3);
    for (const next of emitted) {
      expect(next).not.toBe(ladder);
    }
    expect(ladder).toEqual(['n2-standard-8', 'n1-standard-8']);
  });

  it('removes a shape from the order', () => {
    const root = render(['n2-standard-8', 'n1-standard-8']);

    click(root, 'Remove n2-standard-8');

    expect(emitted).toEqual([['n1-standard-8']]);
  });

  // Adding offers a shape no position holds yet: a repeated shape re-probes a
  // stockout the fleet already found, and the server refuses one.
  it('adds a shape the order does not already hold', () => {
    const root = render(['n2-standard-8']);

    buttonWith(root, 'Add a shape').click();
    fixture.detectChanges();

    expect(emitted).toEqual([['n2-standard-8', 'n1-standard-8']]);
  });

  // The bound is the server's, published beside the policy. Offering a ninth
  // shape would build a request whose only possible answer is a refusal.
  it('stops offering shapes at the rung bound', () => {
    const root = render(['n2-standard-8', 'n1-standard-8'], 2);

    expect(buttonWith(root, 'Add a shape').disabled).toBe(true);
  });

  // The other reason Add stops: nothing admissible is left. A catalog with one
  // usable shape can hold an order one shape long and no longer.
  it('stops offering shapes when the catalog has none left', () => {
    const root = render(['n1-standard-8'], 8, [machineTypeOf('n1-standard-8')]);

    expect(buttonWith(root, 'Add a shape').disabled).toBe(true);
  });

  // What the catalog says can run Cuttlefish on the CPU path, mirrored so the
  // picker never offers a shape the server is going to refuse.
  it('offers no shape the fleet may not be built from', () => {
    const root = render([], 8, [
      machineTypeOf('n1-standard-8'),
      machineTypeOf('g2-standard-8', {gpuMode: 'integrated'}),
      machineTypeOf('c4a-standard-8-metal'),
      machineTypeOf('t2a-standard-8', {architecture: 'arm'}),
      machineTypeOf('n2-standard-8', {isEnabled: false}),
    ]);

    buttonWith(root, 'Add a shape').click();
    fixture.detectChanges();

    expect(emitted).toEqual([['n1-standard-8']]);
  });

  // The hand-over is what the order means, so it is drawn between the cards
  // instead of left to be inferred from their sequence.
  it('draws the hand-over between cards and marks the shape it starts at', () => {
    const root = render(['n2-standard-8', 'n1-standard-8', 'n2-standard-16']);

    expect(Array.from(root.querySelectorAll('.fall')).map(textOf)).toEqual([
      'arrow_downward when stocked out',
      'arrow_downward when stocked out',
    ]);
    expect(Array.from(root.querySelectorAll('.mark')).map(textOf)).toEqual(['preferred']);
  });

  // Matched by SHAPE and not by position: a stockout is a fact about a machine
  // family, so it stays true of a shape that has just been moved, while the
  // order under it is the draft being authored.
  it('says what the server is doing about each shape, wherever it now sits', () => {
    const root = render(['n2-standard-8', 'n1-standard-8', 'n2-standard-16'], 8, CATALOG, {
      rungs: [
        rungOf('n1-standard-8', {
          stockedOutZones: 3,
          newestStockoutAt: new Date('2026-08-14T09:54:00Z'),
        }),
        // Landing outranks a stockout it still has zones left under.
        rungOf('n2-standard-8', {landing: true, stockedOutZones: 1, slots: 7}),
        rungOf('n2-standard-16'),
      ],
    });

    expect(words(root)).toEqual(['building here now', 'stocked out · 3 of 3 zones', 'available']);
    expect(states(root)).toEqual(['building', 'stocked-out', 'available']);
    expect(root.textContent).toContain('7 slots per machine');
    expect(root.textContent).toMatch(/seen \d{1,2}:\d{2}/);
  });

  // A shape no zone offers is stepped past for the same reason as one every
  // zone is out of, and "0 of 0 zones" is a count where the reader needs a
  // reason.
  it('states a shape no zone offers in words rather than as a count', () => {
    const root = render(['n2-standard-8'], 8, CATALOG, {
      rungs: [rungOf('n2-standard-8', {zones: 0})],
    });

    expect(words(root)).toEqual(['no zone offers this shape']);
    expect(root.textContent).not.toContain('0 of 0');
  });

  // Two different silences with two different remedies: one is saved and gone
  // from the catalog, the other is an edit nobody has saved yet.
  it('separates a shape the server has not judged from one the catalog dropped', () => {
    const root = render(['n2-standard-8', 'g2-standard-8'], 8, CATALOG, {rungs: []});

    expect(words(root)).toEqual(['not saved yet', 'not in the catalog']);
    expect(root.textContent).not.toContain('slots per machine');
  });

  // No card is marked when the walk has nowhere to land, so the state has to be
  // said in words or it is drawn nowhere at all.
  it('says in words when no shape in the order can be built', () => {
    const stocked = {stockedOutZones: 3};
    const root = render(['n2-standard-8', 'n1-standard-8'], 8, CATALOG, {
      rungs: [rungOf('n2-standard-8', stocked), rungOf('n1-standard-8', stocked)],
    });

    expect(root.textContent).toContain('Every shape in the order is stocked out');
  });

  it('says nothing of the kind while a shape is being built', () => {
    const root = render(['n2-standard-8', 'n1-standard-8'], 8, CATALOG, {
      rungs: [
        rungOf('n2-standard-8', {stockedOutZones: 3}),
        rungOf('n1-standard-8', {landing: true}),
      ],
    });

    expect(root.textContent).not.toContain('Every shape in the order is stocked out');
  });

  // The picker offers the catalog's own grouping, so a family that has run out
  // is stepped past as a family rather than shape by shape.
  it('offers the admissible shapes grouped by family, the held one first', () => {
    const root = render(['n2-standard-8']);
    const panel = openPicker(root, 'n2-standard-8');

    expect(
      Array.from(panel.querySelectorAll('[role="group"]')).map(group =>
        group.getAttribute('aria-label')
      )
    ).toEqual(['n2 machines', 'n1 machines']);
    expect(Array.from(panel.querySelectorAll('[mat-menu-item]')).map(textOf)).toEqual([
      'n2-standard-8',
      'n2-standard-16',
      'n1-standard-8',
    ]);
  });

  it('emits the order with the picked shape in place', () => {
    const root = render(['n2-standard-8', 'n1-standard-8']);
    const panel = openPicker(root, 'n2-standard-8');

    panel.querySelectorAll<HTMLElement>('[mat-menu-item]')[1].click();
    fixture.detectChanges();

    expect(emitted).toEqual([['n2-standard-16', 'n1-standard-8']]);
  });

  // The empty state is the same picture with one shape in it: what every
  // machine is built as, and it is a fact of the catalog rather than a gap.
  it('draws the catalog’s default shape when no order is set', () => {
    const root = render([], 8, CATALOG, {defaultShape: 'n1-standard-8', defaultSlots: 7});

    expect(textOf(root.querySelector('.ghost-shape'))).toBe('n1-standard-8');
    expect(root.textContent).toContain('7 slots per machine');
    expect(root.textContent).toContain('Every machine is built as this shape');
    expect(root.querySelectorAll('app-ladder-rung-card')).toHaveLength(0);
  });

  // The accelerator is part of what a rung costs: a gpu-pool machine carries
  // one, and an order that showed only slots would hide it.
  it('states the GPU a machine built on a rung would carry', () => {
    const root = render(['n1-standard-8'], 8, CATALOG, {
      rungs: [
        {
          machineType: 'n1-standard-8',
          machineFamily: 'n1',
          slots: 3,
          zones: 3,
          stockedOutZones: 0,
          newestStockoutAt: null,
          gpuType: 'nvidia-tesla-t4',
          gpuCount: 1,
          landing: true,
          refused: '',
        },
      ],
    });

    expect(root.textContent).toContain('3 slots per machine · nvidia-tesla-t4 × 1');
  });
});
