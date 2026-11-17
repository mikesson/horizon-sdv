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

import {ChangeDetectionStrategy, Component, computed, inject, signal} from '@angular/core';
import {toObservable, toSignal} from '@angular/core/rxjs-interop';
import {MatButtonModule} from '@angular/material/button';
import {MAT_DIALOG_DATA, MatDialogModule, MatDialogRef} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatProgressSpinnerModule} from '@angular/material/progress-spinner';
import {MatSelectModule} from '@angular/material/select';
import {concat, of, timer} from 'rxjs';
import {switchMap} from 'rxjs/operators';
import type {ShapeResolution} from '../../../machine-types/services/machine-type-api.service';
import {MachineTypeApiService} from '../../../machine-types/services/machine-type-api.service';

/** Which families a custom shape may be built on, as the dialog is opened. */
export interface CustomShapeBuilderData {
  families: string[];
  initialFamily: string;
}

/** How long a keystroke waits before Compute Engine is asked about the name. */
const VALIDATE_DEBOUNCE_MS = 400;

/**
 * Builds a custom machine name and asks Compute Engine whether it will build
 * one, before the name goes anywhere it matters.
 *
 * CCO holds no opinion about what a family permits (the vCPU increment, the
 * memory band, which series offer custom shapes at all). Compute Engine answers
 * for a specific name, with the machine's real size when it agrees and the rule
 * that was broken when it does not, and both of those are rendered here
 * verbatim. A per-family table of the rules would be exactly the maintenance
 * this catalog exists to abolish.
 */
@Component({
  selector: 'app-custom-shape-builder',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [
    MatButtonModule,
    MatDialogModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressSpinnerModule,
    MatSelectModule,
  ],
  template: `
    <h2 mat-dialog-title class="title">
      <mat-icon>tune</mat-icon>
      Custom shape
    </h2>
    <mat-dialog-content>
      <p class="lede">
        Compute Engine builds a custom machine on demand. It decides whether this one is legal,
        and the answer below is its own.
      </p>

      <div class="fields">
        <mat-form-field appearance="outline">
          <mat-label>Family</mat-label>
          <mat-select [value]="family()" (valueChange)="family.set($event)">
            @for (option of data.families; track option) {
              <mat-option [value]="option">{{ option }}</mat-option>
            }
          </mat-select>
        </mat-form-field>

        <mat-form-field appearance="outline">
          <mat-label>vCPUs</mat-label>
          <input matInput type="number" min="1" [value]="vcpus()" (input)="setVcpus($event)" />
        </mat-form-field>

        <mat-form-field appearance="outline">
          <mat-label>Memory (MB)</mat-label>
          <input matInput type="number" min="256" [value]="memoryMb()" (input)="setMemory($event)" />
        </mat-form-field>
      </div>

      <p class="name mono">{{ machineType() }}</p>

      @if (pending()) {
        <p class="status">
          <mat-spinner aria-label="Checking the name" diameter="16" />
          Asking Compute Engine…
        </p>
      } @else if (resolved(); as answer) {
        @if (answer.shape; as shape) {
          <p class="ok">
            <mat-icon>check_circle</mat-icon>
            {{ shape.vcpus }} vCPU · {{ shape.memoryGb }} GB · holds {{ shape.fit }}
            {{ shape.fit === 1 ? 'device' : 'devices' }}
          </p>
        } @else {
          <p class="refusal">
            <mat-icon>error</mat-icon>
            {{ answer.refusal }}
          </p>
        }
      }
    </mat-dialog-content>
    <mat-dialog-actions align="end">
      <button type="button" mat-button (click)="dialogRef.close()">Cancel</button>
      <button type="button" mat-flat-button [disabled]="!usable()" (click)="use()">Use shape</button>
    </mat-dialog-actions>
  `,
  styles: `
    .title {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
    }

    .lede {
      color: var(--cco-text-secondary);
      font-size: var(--cco-text-sm);
    }

    .fields {
      display: flex;
      flex-wrap: wrap;
      gap: var(--cco-space-12);
    }

    .fields mat-form-field {
      flex: 1 1 140px;
    }

    .name {
      font-family: var(--cco-font-mono);
      font-size: var(--cco-text-sm);
    }

    .status,
    .ok,
    .refusal {
      display: flex;
      align-items: center;
      gap: var(--cco-space-8);
      font-size: var(--cco-text-sm);
      margin: 0;
    }

    .status {
      color: var(--cco-text-secondary);
    }

    .ok {
      color: var(--cco-success);
    }

    .refusal {
      color: var(--cco-error);
    }

    .ok mat-icon,
    .refusal mat-icon,
    .status mat-icon {
      font-size: var(--cco-icon-sm);
      width: var(--cco-icon-sm);
      height: var(--cco-icon-sm);
    }
  `,
})
export class CustomShapeBuilderComponent {
  protected readonly data = inject<CustomShapeBuilderData>(MAT_DIALOG_DATA);
  protected readonly dialogRef =
    inject<MatDialogRef<CustomShapeBuilderComponent, string>>(MatDialogRef);
  private readonly api = inject(MachineTypeApiService);

  protected readonly family = signal(this.data.initialFamily);
  protected readonly vcpus = signal(8);
  protected readonly memoryMb = signal(32768);

  /**
   * The custom-name grammar, which is the whole of what the console knows: the
   * series, the word, the width and the memory in megabytes.
   */
  protected readonly machineType = computed(
    () => `${this.family()}-custom-${this.vcpus()}-${this.memoryMb()}`
  );

  /**
   * The verdict on the name as it stands, null while there is not one yet.
   *
   * Each name emits null first and its answer after the debounce, so an edit
   * clears the previous verdict at the keystroke rather than leaving the old
   * shape's "holds 3 devices" standing under a name nobody has asked about.
   */
  protected readonly resolved = toSignal<ShapeResolution | null>(
    toObservable(this.machineType).pipe(
      switchMap(name =>
        concat(of(null), timer(VALIDATE_DEBOUNCE_MS).pipe(switchMap(() => this.api.resolve(name))))
      )
    ),
    {initialValue: null}
  );

  /** True while the debounce or the request is still between name and answer. */
  protected readonly pending = computed(() => this.resolved() === null);

  protected readonly usable = computed(() => this.resolved()?.shape != null);

  protected setVcpus(event: Event): void {
    this.vcpus.set(numberFrom(event, this.vcpus()));
  }

  protected setMemory(event: Event): void {
    this.memoryMb.set(numberFrom(event, this.memoryMb()));
  }

  protected use(): void {
    const shape = this.resolved()?.shape;
    if (shape) {
      this.dialogRef.close(shape.id);
    }
  }
}

function numberFrom(event: Event, fallback: number): number {
  const value = Number((event.target as HTMLInputElement).value);
  return Number.isFinite(value) && value > 0 ? Math.trunc(value) : fallback;
}
