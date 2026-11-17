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
import {MatIcon} from '@angular/material/icon';
import {MatMenuModule} from '@angular/material/menu';
import {MatTooltipModule} from '@angular/material/tooltip';
import {RouterLink, RouterOutlet} from '@angular/router';
import {AuthStore} from './auth/store/auth.signal-store';
import type {NavItem} from './shared/components';
import {NavSectionComponent} from './shared/components';
import type {Appearance} from './shared/services/appearance.service';
import {AppearanceService} from './shared/services/appearance.service';

@Component({
  selector: 'app-root',
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './app.component.html',
  styleUrl: './app.component.scss',
  imports: [
    MatIcon,
    MatMenuModule,
    MatTooltipModule,
    RouterLink,
    RouterOutlet,
    NavSectionComponent,
  ],
})
export class AppComponent {
  private readonly authStore = inject(AuthStore);
  private readonly appearanceService = inject(AppearanceService);

  protected readonly currentUser = this.authStore.user;
  protected readonly navOpen = signal(true);

  // The console's own three options, in its own order.
  protected readonly appearanceOptions: ReadonlyArray<{
    value: Appearance;
    label: string;
    icon: string;
  }> = [
    {value: 'light', label: 'Light', icon: 'light_mode'},
    {value: 'dark', label: 'Dark', icon: 'dark_mode'},
    {value: 'system', label: 'Same as device', icon: 'brightness_auto'},
  ];

  protected readonly appearance = this.appearanceService.appearance;

  protected readonly appearanceIcon = computed(
    () => this.appearanceOptions.find(o => o.value === this.appearance())?.icon ?? 'brightness_auto'
  );

  // Every signed-in user sees the full menu. Authentication is the only gate.
  protected readonly mainItems: NavItem[] = [{label: 'Dashboard', icon: 'dashboard', route: '/'}];

  // Device-first IA: devices and builds are the product, hosts are the
  // capacity fleet underneath them.
  protected readonly resourceItems: NavItem[] = [
    {label: 'Devices', icon: 'phone_android', route: '/devices'},
    {label: 'Batches', icon: 'dataset', route: '/batches'},
    {label: 'Builds', icon: 'build', route: '/builds'},
  ];

  protected readonly infrastructureItems: NavItem[] = [
    {label: 'Hosts', icon: 'dns', route: '/hosts'},
  ];

  protected setAppearance(value: Appearance): void {
    this.appearanceService.set(value);
  }

  protected readonly adminItems: NavItem[] = [
    {label: 'Capacity', icon: 'data_usage', route: '/admin/capacity'},
    {label: 'Control Plane', icon: 'engineering', route: '/admin/control-plane'},
    {label: 'Autoscaler', icon: 'stacks', route: '/admin/autoscaler'},
    {label: 'Catalog', icon: 'category', route: '/admin/catalog'},
    {label: 'Settings', icon: 'tune', route: '/admin/settings'},
    {label: 'Activity', icon: 'history', route: '/admin/activity'},
  ];

  protected readonly developerItems: NavItem[] = [
    {label: 'API Documentation', icon: 'api', route: '/docs', external: true},
  ];

  protected toggleNav(): void {
    this.navOpen.update(open => !open);
  }
}
