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

import type {ElementRef, OnDestroy} from '@angular/core';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  input,
  output,
  signal,
  viewChild,
} from '@angular/core';

/**
 * A resizable split pane component.
 *
 * Allows users to resize two panels by dragging a handle between them.
 * Supports horizontal (left/right) and vertical (top/bottom) orientations.
 * Persists size to localStorage when a storageKey is provided.
 *
 * @example
 * ```html
 * <app-resizable-split [direction]="'horizontal'" [initialRatio]="0.6" storageKey="device-player-split">
 *   <div primary>Primary content</div>
 *   <div secondary>Secondary content</div>
 * </app-resizable-split>
 * ```
 */
@Component({
  selector: 'app-resizable-split',
  imports: [],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div
      class="split-container"
      [class.horizontal]="direction() === 'horizontal'"
      [class.vertical]="direction() === 'vertical'"
      [class.dragging]="isDragging()"
      #container
    >
      <!-- Primary Panel -->
      <div
        class="primary-panel"
        [style]="primaryStyle()"
      >
        <ng-content select="[primary]" />
      </div>

      <!-- Drag Handle -->
      <div
        class="drag-handle"
        (mousedown)="onDragStart($event)"
        (touchstart)="onTouchStart($event)"
        [class.active]="isDragging()"
      >
        <div class="handle-bar"></div>
      </div>

      <!-- Secondary Panel -->
      <div
        class="secondary-panel"
        [style]="secondaryStyle()"
      >
        <ng-content select="[secondary]" />
      </div>
    </div>
  `,
  styles: `
      :host {
        display: block;
        width: 100%;
        height: 100%;
      }

      .split-container {
        display: flex;
        width: 100%;
        height: 100%;
        overflow: hidden;
      }

      .split-container.horizontal {
        flex-direction: row;
      }

      .split-container.vertical {
        flex-direction: column;
      }

      .primary-panel,
      .secondary-panel {
        overflow: hidden;
        min-width: 0;
        min-height: 0;
      }

      /* Drag Handle */
      .drag-handle {
        flex-shrink: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        background: var(--cco-surface-variant);
        transition: background var(--cco-duration-base) ease;
      }

      .horizontal .drag-handle {
        width: 6px;
        cursor: col-resize;
      }

      .vertical .drag-handle {
        height: 6px;
        cursor: row-resize;
      }

      .drag-handle:hover,
      .drag-handle.active {
        background: var(--cco-accent);
      }

      .handle-bar {
        background: var(--cco-text-secondary);
        border-radius: 2px;
        transition: background var(--cco-duration-base) ease;
      }

      .horizontal .handle-bar {
        width: 2px;
        height: 40px;
      }

      .vertical .handle-bar {
        width: 40px;
        height: 2px;
      }

      .drag-handle:hover .handle-bar,
      .drag-handle.active .handle-bar {
        background: var(--cco-surface);
      }

      /* Prevent text selection while dragging */
      .split-container.dragging {
        user-select: none;
      }

      .split-container.dragging * {
        pointer-events: none;
      }

      .split-container.dragging .drag-handle {
        pointer-events: auto;
      }
    `,
})
export class ResizableSplitComponent implements OnDestroy {
  readonly direction = input<'horizontal' | 'vertical'>('horizontal');

  /** Initial ratio for the primary panel (0-1). Default is 0.6 (60%). */
  readonly initialRatio = input(0.6);

  /** Minimum ratio for the primary panel (0-1). Default is 0.2 (20%). */
  readonly minRatio = input(0.2);

  /** Maximum ratio for the primary panel (0-1). Default is 0.9 (90%). */
  readonly maxRatio = input(0.9);

  readonly storageKey = input<string | undefined>(undefined);

  readonly ratioChange = output<number>();

  readonly containerRef = viewChild.required<ElementRef<HTMLDivElement>>('container');

  readonly isDragging = signal(false);
  readonly ratio = signal(0.6);

  readonly primaryStyle = computed(() => {
    const r = this.ratio();
    if (this.direction() === 'horizontal') {
      return {flex: `0 0 ${r * 100}%`};
    } else {
      return {flex: `0 0 ${r * 100}%`};
    }
  });

  readonly secondaryStyle = computed(() => {
    const r = 1 - this.ratio();
    if (this.direction() === 'horizontal') {
      return {flex: `0 0 ${r * 100}%`};
    } else {
      return {flex: `0 0 ${r * 100}%`};
    }
  });

  private readonly boundMouseMove = this.onMouseMove.bind(this);
  private readonly boundMouseUp = this.onMouseUp.bind(this);
  private readonly boundTouchMove = this.onTouchMove.bind(this);
  private readonly boundTouchEnd = this.onTouchEnd.bind(this);

  constructor() {
    effect(
      () => {
        const key = this.storageKey();
        const initial = this.initialRatio();

        if (key) {
          const stored = localStorage.getItem(key);
          if (stored) {
            const parsed = Number.parseFloat(stored);
            if (!Number.isNaN(parsed) && parsed >= 0 && parsed <= 1) {
              this.ratio.set(parsed);
              return;
            }
          }
        }
        this.ratio.set(initial);
      },
      {allowSignalWrites: true}
    );
  }

  ngOnDestroy(): void {
    this.removeListeners();
  }

  onDragStart(event: MouseEvent): void {
    event.preventDefault();
    this.isDragging.set(true);
    document.addEventListener('mousemove', this.boundMouseMove);
    document.addEventListener('mouseup', this.boundMouseUp);
  }

  onTouchStart(event: TouchEvent): void {
    event.preventDefault();
    this.isDragging.set(true);
    document.addEventListener('touchmove', this.boundTouchMove, {passive: false});
    document.addEventListener('touchend', this.boundTouchEnd);
  }

  private onMouseMove(event: MouseEvent): void {
    if (!this.isDragging()) {
      return;
    }
    this.updateRatio(event.clientX, event.clientY);
  }

  private onTouchMove(event: TouchEvent): void {
    if (!this.isDragging()) {
      return;
    }
    event.preventDefault();
    const touch = event.touches[0];
    this.updateRatio(touch.clientX, touch.clientY);
  }

  private onMouseUp(): void {
    this.finishDrag();
  }

  private onTouchEnd(): void {
    this.finishDrag();
  }

  private updateRatio(clientX: number, clientY: number): void {
    const container = this.containerRef().nativeElement;
    const rect = container.getBoundingClientRect();

    let newRatio: number;
    if (this.direction() === 'horizontal') {
      newRatio = (clientX - rect.left) / rect.width;
    } else {
      newRatio = (clientY - rect.top) / rect.height;
    }

    // Clamp to min/max
    newRatio = Math.max(this.minRatio(), Math.min(this.maxRatio(), newRatio));

    this.ratio.set(newRatio);
    this.ratioChange.emit(newRatio);
  }

  private finishDrag(): void {
    this.isDragging.set(false);
    this.removeListeners();

    // Persist to storage
    const key = this.storageKey();
    if (key) {
      localStorage.setItem(key, this.ratio().toString());
    }
  }

  private removeListeners(): void {
    document.removeEventListener('mousemove', this.boundMouseMove);
    document.removeEventListener('mouseup', this.boundMouseUp);
    document.removeEventListener('touchmove', this.boundTouchMove);
    document.removeEventListener('touchend', this.boundTouchEnd);
  }

  setRatio(ratio: number): void {
    const clamped = Math.max(this.minRatio(), Math.min(this.maxRatio(), ratio));
    this.ratio.set(clamped);
    this.ratioChange.emit(clamped);

    const key = this.storageKey();
    if (key) {
      localStorage.setItem(key, clamped.toString());
    }
  }

  reset(): void {
    this.ratio.set(this.initialRatio());
    this.ratioChange.emit(this.initialRatio());

    const key = this.storageKey();
    if (key) {
      localStorage.removeItem(key);
    }
  }
}
