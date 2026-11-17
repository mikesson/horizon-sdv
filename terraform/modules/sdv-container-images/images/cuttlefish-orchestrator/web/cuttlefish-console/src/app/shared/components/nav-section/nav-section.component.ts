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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import {toSignal} from '@angular/core/rxjs-interop';
import {MatIconModule} from '@angular/material/icon';
import {NavigationEnd, Router, RouterModule} from '@angular/router';
import {filter} from 'rxjs/operators';
import {resolveBasePath} from '../../utils';

/**
 * Represents a navigation item within a section.
 */
export interface NavItem {
  label: string;
  icon: string;
  route: string;
  external?: boolean;
}

/**
 * Collapsible navigation section component following GCP Console patterns.
 *
 * Implements progressive disclosure with smooth expand/collapse animations.
 * Supports localStorage persistence for expansion state.
 *
 * @example
 * ```html
 * <app-nav-section
 *   sectionId="infrastructure"
 *   label="Infrastructure"
 *   [items]="infrastructureItems"
 *   [defaultExpanded]="true">
 * </app-nav-section>
 * ```
 */
@Component({
  selector: 'app-nav-section',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterModule, MatIconModule],
  template: `
    <div class="nav-section">
      <button type="button"
        class="section-header"
        (click)="toggle()"
        [attr.aria-expanded]="expanded()"
        [attr.aria-controls]="'nav-section-' + sectionId()">
        <mat-icon class="chevron-icon" [class.expanded]="expanded()">chevron_right</mat-icon>
        <span class="section-label">{{ label() }}</span>
      </button>

      <div
        class="section-items"
        [class.collapsed]="!expanded()"
        [id]="'nav-section-' + sectionId()">
        <div class="section-items__inner">
          @for (item of items(); track item.route) {
            @if (item.external) {
              <a class="nav-item" [href]="externalHref(item.route)" target="_blank" rel="noopener noreferrer">
                <mat-icon class="nav-item__icon">{{ item.icon }}</mat-icon>
                <span class="nav-item__label">{{ item.label }}</span>
                <mat-icon class="nav-item__ext">open_in_new</mat-icon>
              </a>
            } @else {
              <!-- The active item was styled but never announced: a screen
                   reader had no way to tell which page it was on. -->
              <a
                class="nav-item"
                [routerLink]="item.route"
                routerLinkActive="active"
                ariaCurrentWhenActive="page"
                [routerLinkActiveOptions]="{exact: item.route === '/'}">
                <mat-icon class="nav-item__icon">{{ item.icon }}</mat-icon>
                <span class="nav-item__label">{{ item.label }}</span>
              </a>
            }
          }
        </div>
      </div>
    </div>
  `,
  styles: `
    .nav-section {
      padding: 4px 0;
    }

    .section-header {
      display: flex;
      align-items: center;
      width: 100%;
      padding: 8px 16px 8px 12px;
      border: none;
      background: transparent;
      cursor: pointer;
      text-align: left;
      font-family: inherit;
      transition: background-color var(--cco-duration-base) ease;

      &:hover {
        background-color: var(--cco-surface-hover);
      }

      &:focus-visible {
        outline: 2px solid var(--cco-accent);
        outline-offset: -2px;
      }
    }

    .chevron-icon {
      font-size: 18px;
      width: 18px;
      height: 18px;
      color: var(--cco-text-secondary);
      margin-right: 8px;
      transition: transform var(--cco-duration-slow) ease-in-out;

      &.expanded {
        transform: rotate(90deg);
      }
    }

    .section-label {
      font-size: 11px;
      font-weight: 500;
      text-transform: uppercase;
      color: var(--cco-text-secondary);
      letter-spacing: 0.8px;
    }

    /* height 0 -> auto via the grid-rows fraction trick; inner wrapper must clip */
    .section-items {
      display: grid;
      grid-template-rows: 1fr;
      opacity: 1;
      transition: grid-template-rows var(--cco-duration-slow) ease-in-out, opacity var(--cco-duration-slow) ease-in-out;

      &.collapsed {
        grid-template-rows: 0fr;
        opacity: 0;
      }
    }

    .section-items__inner {
      min-height: 0;
      overflow: hidden;
    }

    .nav-item {
      display: flex;
      align-items: center;
      gap: 16px;
      margin: 2px 8px;
      padding: 8px 16px;
      border-radius: var(--cco-radius-pill);
      color: var(--cco-text-secondary);
      text-decoration: none;
      font-size: 14px;
      line-height: 20px;
      cursor: pointer;
      transition: background-color var(--cco-duration-base) ease, color var(--cco-duration-base) ease;

      &:hover {
        background-color: var(--cco-surface-hover);
        color: var(--cco-text);
      }

      &.active {
        background-color: var(--cco-nav-active);
        color: var(--cco-text);
        font-weight: 500;
      }

      &.active .nav-item__icon {
        color: var(--cco-accent);
      }
    }

    .nav-item__icon {
      font-size: 20px;
      width: 20px;
      height: 20px;
      flex-shrink: 0;
    }

    .nav-item__label {
      flex: 1;
    }

    .nav-item__ext {
      font-size: 14px;
      width: 14px;
      height: 14px;
      flex-shrink: 0;
      color: var(--cco-text-secondary);
    }
  `,
})
export class NavSectionComponent {
  private readonly router = inject(Router);

  readonly sectionId = input.required<string>();

  readonly label = input.required<string>();

  readonly items = input.required<NavItem[]>();

  readonly defaultExpanded = input<boolean>(true);

  readonly expandedChange = output<boolean>();

  readonly expanded = signal<boolean>(true);

  private readonly storageKey = computed(() => `nav-section-${this.sectionId()}-expanded`);

  constructor() {
    effect(
      () => {
        const stored = localStorage.getItem(this.storageKey());
        if (stored !== null) {
          this.expanded.set(stored === 'true');
        } else {
          this.expanded.set(this.defaultExpanded());
        }
      },
      {allowSignalWrites: true}
    );

    // Auto-expand section when navigating to a route within it
    const navEnd = toSignal(this.router.events.pipe(filter(e => e instanceof NavigationEnd)));

    effect(
      () => {
        const event = navEnd();
        if (event) {
          const currentUrl = this.router.url;
          const hasActiveItem = this.items().some(
            item => !item.external && currentUrl.startsWith(item.route)
          );
          if (hasActiveItem && !this.expanded()) {
            this.expanded.set(true);
            this.persistState(true);
          }
        }
      },
      {allowSignalWrites: true}
    );
  }

  toggle(): void {
    const newState = !this.expanded();
    this.expanded.set(newState);
    this.persistState(newState);
    this.expandedChange.emit(newState);
  }

  expand(): void {
    if (!this.expanded()) {
      this.expanded.set(true);
      this.persistState(true);
      this.expandedChange.emit(true);
    }
  }

  collapse(): void {
    if (this.expanded()) {
      this.expanded.set(false);
      this.persistState(false);
      this.expandedChange.emit(false);
    }
  }

  protected externalHref(route: string): string {
    return resolveBasePath(route);
  }

  private persistState(expanded: boolean): void {
    localStorage.setItem(this.storageKey(), String(expanded));
  }
}
