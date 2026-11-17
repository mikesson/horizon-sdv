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

import {ChangeDetectionStrategy, Component, computed, input, output} from '@angular/core';
import {MatButtonModule} from '@angular/material/button';
import {MatIconModule} from '@angular/material/icon';
import type {MachineType} from '../../../machine-types/models';
import type {LadderRung} from '../../models/autoscaler.model';
import {stockedOut} from '../../models/autoscaler.model';
import type {LadderRow, ShapeGroup, ShapeState} from './ladder-rung-card.component';
import {LadderRungCardComponent} from './ladder-rung-card.component';

/**
 * The shapes a POOL may be told to build.
 *
 * The rules mirror the ones the server refuses a shape with, and they depend on
 * the pool that will own the order. Bare metal belongs to a pool whose machines
 * are bare metal and nowhere else, an accelerator would be attached to a machine
 * that never asked for one, and a pool of accelerated machines cannot hold a
 * shape that carries none. One list valid in one pool and refused in another
 * cannot be one list, which is why the picker is filtered per pool.
 *
 * The server stays the authority. A catalog that moved while this page was open
 * is exactly the case that ends in a refusal.
 */
function admissible(machine: MachineType, pool: string): boolean {
  if (!machine.isEnabled) {
    return false;
  }
  if (pool === 'arm') {
    return machine.architecture === 'arm' && bareMetal(machine);
  }
  if (machine.architecture !== 'x86' || bareMetal(machine)) {
    return false;
  }
  return pool === 'gpu' ? carriesGpu(machine) : machine.gpuMode !== 'integrated';
}

/** Bare metal is named rather than flagged in the catalog the console reads. */
function bareMetal(machine: MachineType): boolean {
  return machine.id.endsWith('-metal');
}

/**
 * Whether a machine built on this shape really ends up with an accelerator,
 * welded on or attached at insert from the type the catalog names for it. A
 * shape the catalog names no GPU for is built without one, which in a pool whose
 * every create is fenced onto accelerated machines is a machine nothing can be
 * placed on.
 */
function carriesGpu(machine: MachineType): boolean {
  if (machine.gpuMode === 'attachable') {
    return (machine.defaultGpuType ?? '') !== '';
  }
  return machine.gpuMode === 'integrated';
}

/**
 * `3 slots per machine · nvidia-tesla-t4 × 1`, or nothing when no machine of
 * this shape is described. The accelerator is stated wherever a machine built
 * here would carry one. An attachable shape's card comes from the catalog's
 * curation, and an order that hides it hides what the rung costs.
 *
 * A shape the server refuses states the rule instead. There is no machine to
 * describe, and the rule is what the operator has to act on.
 */
function factsPhrase(rung: LadderRung | undefined): string {
  if (rung?.refused) {
    return rung.refused;
  }
  if (!rung || rung.slots <= 0) {
    return '';
  }
  const slots = `${rung.slots} ${rung.slots === 1 ? 'slot' : 'slots'} per machine`;
  return rung.gpuType ? `${slots} · ${rung.gpuType} × ${rung.gpuCount}` : slots;
}

/** `seen 09:54`, when the stockout a word rests on was last observed. */
function seenPhrase(at: Date | null): string {
  return at ? `seen ${at.toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'})}` : '';
}

/** What the family picker offers, in the catalog's own order. */
function groupByFamily(shapes: ReadonlyArray<{id: string; family: string}>): ShapeGroup[] {
  const groups: Array<{family: string; shapes: string[]}> = [];
  for (const shape of shapes) {
    const group = groups.find(held => held.family === shape.family);
    if (group) {
      group.shapes.push(shape.id);
    } else {
      groups.push({family: shape.family, shapes: [shape.id]});
    }
  }
  return groups;
}

/**
 * The three things the server can be saying about a shape, plus the two ways it
 * says nothing at all.
 *
 * Landing outranks a stockout. The shape provisioning would build can carry one
 * in some of its zones and still be the one being built. A shape no zone offers
 * is stocked out by the walk's own rule, and says so in its own words because
 * "0 of 0 zones" states a count where the reader needs a reason.
 */
function stateOf(
  rung: LadderRung | undefined,
  inCatalog: boolean
): {state: ShapeState; word: string; seen: string} {
  if (!rung) {
    return inCatalog
      ? {state: 'unsaved', word: 'not saved yet', seen: ''}
      : {state: 'dropped', word: 'not in the catalog', seen: ''};
  }
  if (rung.refused) {
    return {state: 'dropped', word: 'refused', seen: ''};
  }
  if (rung.landing) {
    return {state: 'building', word: 'building here now', seen: ''};
  }
  if (rung.zones === 0) {
    return {state: 'stocked-out', word: 'no zone offers this shape', seen: ''};
  }
  if (stockedOut(rung)) {
    return {
      state: 'stocked-out',
      word: `stocked out · ${rung.stockedOutZones} of ${rung.zones} zones`,
      seen: seenPhrase(rung.newestStockoutAt),
    };
  }
  return {state: 'available', word: 'available', seen: ''};
}

/**
 * The machine shapes the fleet prefers, in order, drawn as the fall-through it
 * is. A card per shape, a connector saying what hands over to what, and beside
 * each one what provisioning is doing about it right now.
 *
 * The setting is the ORDER. The first shape is what the fleet wants, and each
 * one below it is what a machine is built as when the shape above is stocked
 * out. Moving a shape is therefore its own action (no remove-and-re-add), and
 * every action is a real button that Tab reaches.
 *
 * The live column is matched to the served state by SHAPE, never by position.
 * A stockout is a fact about a machine family, so it stays true of a shape an
 * operator has just dragged somewhere else, while the order under it is the
 * draft they are authoring.
 *
 * Every change emits a NEW array and never mutates the one it was given. The
 * draft is compared against the server's copy structurally, so mutating in
 * place would leave both sides equal and Save would never appear.
 */
@Component({
  selector: 'app-shape-ladder-editor',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [MatButtonModule, MatIconModule, LadderRungCardComponent],
  template: `
    <ol class="order">
      @for (row of rows(); track row.machineType) {
        <li>
          @if (!$first) {
            <p class="fall">
              <mat-icon>arrow_downward</mat-icon>
              when stocked out
            </p>
          }
          <app-ladder-rung-card
            [row]="row"
            (shapeChange)="setShape(row.index, $event)"
            (moveUp)="moveUp(row.index)"
            (moveDown)="moveDown(row.index)"
            (remove)="remove(row.index)" />
        </li>
      }
    </ol>

    @if (rows().length === 0) {
      <div class="ghost">
        <p class="ghost-shape">{{ defaultName() }}</p>
        @if (defaultFacts(); as facts) {
          <p class="ghost-facts">{{ facts }}</p>
        }
        <p class="ghost-copy">
          No order set. Every machine is built as this shape, the one the catalog marks default.
          Set an order to name the shape the fleet prefers, and the shapes it builds instead when
          that one is stocked out.
        </p>
      </div>
    }

    <div class="actions">
      <button type="button" mat-stroked-button [disabled]="!canAdd()" (click)="add()">
        <mat-icon>add</mat-icon>
        Add a shape
      </button>
      @if (countNote(); as note) {
        <span class="count">{{ note }}</span>
      }
    </div>

    @if (verdict(); as sentence) {
      <p class="verdict">{{ sentence }}</p>
    }
  `,
  styles: `
    :host {
      display: block;
      max-width: 44rem;
    }

    .order {
      margin: 0 0 var(--cco-space-12);
      padding: 0;
      list-style: none;
    }

    /* The hand-over is what the order means, so it is drawn as a line between
       two cards instead of a sentence inside one. */
    .fall {
      display: flex;
      align-items: center;
      gap: var(--cco-space-4);
      margin: var(--cco-space-4) 0 var(--cco-space-4) var(--cco-space-24);
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .fall mat-icon {
      font-size: var(--cco-icon-sm);
      width: var(--cco-icon-sm);
      height: var(--cco-icon-sm);
    }

    /* What a fleet with no order builds, drawn as the card it would be so the
       empty state is the same picture with one shape in it. */
    .ghost {
      padding: var(--cco-space-12) var(--cco-space-16);
      margin-bottom: var(--cco-space-12);
      border: 1px dashed var(--cco-text-disabled);
      border-radius: var(--cco-radius-sm);
    }

    .ghost-shape {
      margin: 0;
      font-family: var(--cco-font-mono);
      color: var(--cco-text);
    }

    .ghost-facts,
    .ghost-copy,
    .count,
    .verdict {
      margin: var(--cco-space-4) 0 0;
      font-size: var(--cco-text-label);
      color: var(--cco-text-secondary);
    }

    .ghost-copy {
      max-width: 60ch;
    }

    .actions {
      display: flex;
      align-items: center;
      gap: var(--cco-space-12);
    }

    .verdict {
      max-width: 60ch;
      color: var(--cco-warning);
    }
  `,
})
export class ShapeLadderEditorComponent {
  /** The pool that will own the order, which decides what may be in it. */
  readonly pool = input.required<string>();
  readonly ladder = input.required<string[]>();
  readonly maxRungs = input.required<number>();
  readonly machineTypes = input<MachineType[]>([]);
  /** The stored order as the server judges it now, matched by shape. */
  readonly rungs = input<LadderRung[]>([]);
  /** The shape a fleet with no order is built as, and what it holds. */
  readonly defaultShape = input<string>('');
  readonly defaultSlots = input<number>(0);
  readonly ladderChange = output<string[]>();

  /** Catalog order, so the picker reads the same way the Catalog page does. */
  private readonly offered = computed(() =>
    this.machineTypes().filter(machine => admissible(machine, this.pool()))
  );

  /** Shapes no position holds yet, which is what an added one can be. */
  private readonly unused = computed(() => {
    const held = this.ladder();
    return this.offered().filter(machine => !held.includes(machine.id));
  });

  protected readonly rows = computed<LadderRow[]>(() => {
    const ladder = this.ladder();
    const unused = this.unused();
    const served = this.rungs();
    const offered = this.offered();
    return ladder.map((machineType, index) => {
      const rung = served.find(item => item.machineType === machineType);
      const catalogued = offered.find(machine => machine.id === machineType);
      return {
        index,
        position: index + 1,
        machineType,
        // The shape this position holds leads its own picker, which keeps one
        // the catalog has since dropped visible and removable instead of blank.
        options: groupByFamily([
          {id: machineType, family: catalogued?.family ?? machineType.split('-')[0]},
          ...unused.map(machine => ({id: machine.id, family: machine.family})),
        ]),
        first: index === 0,
        last: index === ladder.length - 1,
        facts: factsPhrase(rung),
        ...stateOf(rung, catalogued !== undefined),
      };
    });
  });

  protected readonly canAdd = computed(
    () => this.ladder().length < this.maxRungs() && this.unused().length > 0
  );

  protected readonly countNote = computed(() => {
    const used = this.ladder().length;
    return used === 0 ? '' : `${used} of ${this.maxRungs()} shapes`;
  });

  protected readonly defaultName = computed(
    () => this.defaultShape() || 'the catalog’s default shape'
  );

  protected readonly defaultFacts = computed(() => {
    const slots = this.defaultSlots();
    return slots > 0 ? `${slots} ${slots === 1 ? 'slot' : 'slots'} per machine` : '';
  });

  /**
   * The state the words have to carry, because no card is marked in it. Every
   * shape stocked out is where provisioning has nothing left to try.
   */
  protected readonly verdict = computed(() => {
    const served = this.rungs();
    if (served.length === 0) {
      return '';
    }
    const probable = served.filter(rung => !rung.refused);
    if (probable.length === 0) {
      return 'Every shape in the order is refused, so machines are built as the catalog’s default shape.';
    }
    return probable.some(rung => rung.landing)
      ? ''
      : 'Every shape in the order is stocked out, so the fleet cannot build a machine right now.';
  });

  protected add(): void {
    const next = this.unused()[0];
    if (next) {
      this.ladderChange.emit([...this.ladder(), next.id]);
    }
  }

  protected remove(index: number): void {
    this.ladderChange.emit(this.ladder().filter((_, i) => i !== index));
  }

  protected setShape(index: number, machineType: string): void {
    this.ladderChange.emit(
      this.ladder().map((current, i) => (i === index ? machineType : current))
    );
  }

  protected moveUp(index: number): void {
    this.swap(index, index - 1);
  }

  protected moveDown(index: number): void {
    this.swap(index, index + 1);
  }

  /**
   * A new array every time, built by index, because the emitted order is the setting.
   * The ends are held by the disabled arrows on the first and last cards, which
   * is why nothing here re-checks them.
   */
  private swap(from: number, to: number): void {
    const ladder = this.ladder();
    const next = [...ladder];
    next[from] = ladder[to];
    next[to] = ladder[from];
    this.ladderChange.emit(next);
  }
}
