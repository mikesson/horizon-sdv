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

import type {ElementRef} from '@angular/core';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  resource,
  signal,
  viewChild,
} from '@angular/core';
import {FormField, form} from '@angular/forms/signals';
import {MatButtonModule} from '@angular/material/button';
import {MatDialog} from '@angular/material/dialog';
import {MatFormFieldModule} from '@angular/material/form-field';
import {MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatSnackBar} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {firstValueFrom} from 'rxjs';
import {
  clearAppData,
  forceStopApp,
  launchApp,
  listInstalledPackages,
  uninstallApp,
} from '../../services/adb/adb-packages';
import {injectAdbPanel} from '../../services/adb/adb-panel';
import {DeviceInstallService} from '../../services/adb/device-install.service';
import {buildInstallPlan} from '../../services/adb/install-plan';
import {AdbToolPanelComponent} from '../adb-tool-panel';
import {AppListComponent} from './app-list.component';
import {toAppInfo} from './app-row';
import {
  InstallConfirmDialogComponent,
  type InstallConfirmResult,
} from './install-confirm-dialog.component';
import {InstallOverlayComponent} from './install-overlay.component';

interface AppFilterDraft {
  search: string;
  thirdPartyOnly: boolean;
}

/**
 * App manager: lists installed packages and installs/uninstalls/launches/
 * force-stops apps and clears app data. The package list is a resource keyed on
 * device/third-party-toggle/ADB state; search filtering is a computed over it.
 * ADB gating, the APK drop target, and session plumbing live in the shared
 * shell + {@link injectAdbPanel}; the install lifecycle is delegated to
 * {@link DeviceInstallService} so progress survives leaving this panel.
 */
@Component({
  selector: 'app-app-manager',
  imports: [
    FormField,
    MatIconModule,
    MatButtonModule,
    MatTooltipModule,
    MatInputModule,
    MatFormFieldModule,
    MatSlideToggleModule,
    AdbToolPanelComponent,
    AppListComponent,
    InstallOverlayComponent,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <app-adb-tool-panel
      [adbState]="adb.state()"
      [isLoading]="packages.isLoading()"
      [error]="packagesError()"
      waitingHint="The app manager will load when ADB is ready"
      loadingLabel="Loading apps..."
      dropIcon="android"
      dropLabel="Drop APKs or app bundles (.apks/.xapk) to install"
      accent="#3fb950"
      (filesDropped)="startInstall($event)"
      (retry)="refresh()"
    >
      <div toolbar class="toolbar">
        <mat-form-field class="search-field" appearance="outline" subscriptSizing="dynamic">
          <mat-label>Search apps</mat-label>
          <input matInput [formField]="filterForm.search" placeholder="Package name" />
          @if (filters().search) {
            <button type="button" matSuffix mat-icon-button aria-label="Clear search" (click)="clearSearch()">
              <mat-icon>close</mat-icon>
            </button>
          }
        </mat-form-field>

        <mat-slide-toggle [formField]="filterForm.thirdPartyOnly" color="primary">
          Third-party only
        </mat-slide-toggle>

        <div class="spacer"></div>

        <button type="button" mat-icon-button (click)="refresh()" matTooltip="Refresh" aria-label="Refresh">
          <mat-icon>refresh</mat-icon>
        </button>
        <button type="button" mat-icon-button (click)="triggerInstall()" matTooltip="Install APK" aria-label="Install APK">
          <mat-icon>add</mat-icon>
        </button>

        <input
          #apkInput
          type="file"
          accept=".apk,.apks,.xapk,.apkm"
          multiple
          (change)="onApkSelected($event)"
          style="display: none"
        />
      </div>

      <app-app-list
        [apps]="filteredApps()"
        (launch)="launchApp($event)"
        (forceStop)="forceStop($event)"
        (clearData)="clearData($event)"
        (uninstall)="uninstall($event)"
      />

      @if (install(); as state) {
        <app-install-overlay
          overlay
          [state]="state"
          (cancelInstall)="cancelInstall()"
          (dismiss)="dismissInstall()"
          (applyRemedy)="applyRemedy()"
          (launch)="launchInstalled()"
        />
      }

      <div footer class="stats-bar">
        <span>{{ filteredApps().length }} apps</span>
        @if (filters().thirdPartyOnly) {
          <span>(third-party only)</span>
        }
      </div>
    </app-adb-tool-panel>
  `,
  styles: `
      .toolbar {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 8px 12px;
        background: var(--cco-surface-variant);
        border-bottom: 1px solid var(--cco-surface-variant);
      }

      .toolbar button {
        color: var(--cco-text-secondary);
      }

      .search-field {
        width: 200px;
      }

      .spacer {
        flex: 1;
      }

      .stats-bar {
        display: flex;
        gap: 8px;
        padding: 4px 12px;
        background: var(--cco-surface-variant);
        border-top: 1px solid var(--cco-surface-variant);
        font-size: 11px;
        color: var(--cco-text-disabled);
      }

      :host ::ng-deep .search-field {
        .mat-mdc-text-field-wrapper {
          background: var(--cco-surface-variant);
        }

        .mdc-notched-outline__leading,
        .mdc-notched-outline__notch,
        .mdc-notched-outline__trailing {
          border-color: var(--cco-border) !important;
        }

        .mat-mdc-input-element {
          color: var(--cco-text);
        }

        .mat-mdc-form-field-label {
          color: var(--cco-text-secondary);
        }
      }
    `,
})
export class AppManagerComponent {
  private readonly installService = inject(DeviceInstallService);
  private readonly dialog = inject(MatDialog);
  private readonly snackBar = inject(MatSnackBar);

  readonly deviceId = input.required<string>();
  readonly instance = input.required<string>();

  /** The Android VM this panel talks to; ADB sessions are per instance. */
  private readonly target = computed(() => ({
    deviceId: this.deviceId(),
    instance: this.instance(),
  }));

  private readonly apkInput = viewChild.required<ElementRef<HTMLInputElement>>('apkInput');

  protected readonly adb = injectAdbPanel(this.target);

  protected readonly install = computed(() => this.installService.state(this.target())());

  private readonly filterDraft = signal<AppFilterDraft>({search: '', thirdPartyOnly: true});
  protected readonly filters = this.filterDraft.asReadonly();
  protected readonly filterForm = form(this.filterDraft);

  protected readonly packages = resource({
    params: () => {
      const id = this.deviceId();
      const thirdPartyOnly = this.filters().thirdPartyOnly;
      // Reload once an install finishes so a freshly installed app appears.
      const installDone = this.install()?.phase === 'done';
      return this.adb.state() === 'connected' ? {id, thirdPartyOnly, installDone} : undefined;
    },
    loader: async ({params}) => {
      const device = await this.adb.connectedDevice();
      const packages = await listInstalledPackages(device, params.thirdPartyOnly);
      return packages.map(toAppInfo);
    },
  });

  protected readonly filteredApps = computed(() => {
    const apps = this.packages.hasValue() ? this.packages.value() : [];
    const query = this.filters().search.toLowerCase();
    if (!query) {
      return apps;
    }
    return apps.filter(app => app.packageName.toLowerCase().includes(query));
  });

  protected readonly packagesError = computed(() => {
    const error = this.packages.error();
    return error ? (error instanceof Error ? error.message : 'Failed to load apps') : null;
  });

  protected clearSearch(): void {
    this.filterDraft.update(draft => ({...draft, search: ''}));
  }

  protected refresh(): void {
    this.packages.reload();
  }

  protected launchApp(packageName: string): Promise<void> {
    return this.adb.run(device => launchApp(device, packageName), {
      success: `Launched ${packageName}`,
      failure: 'Failed to launch',
    });
  }

  protected forceStop(packageName: string): Promise<void> {
    return this.adb.run(device => forceStopApp(device, packageName), {
      success: `Force stopped ${packageName}`,
      failure: 'Failed to stop',
    });
  }

  protected async clearData(packageName: string): Promise<void> {
    const confirmed = await this.adb.confirm({
      title: 'Clear App Data',
      message: `Clear all data for ${packageName}? This cannot be undone.`,
      confirmText: 'Clear data',
      confirmColor: 'warn',
      icon: 'delete_sweep',
      iconColor: 'warn',
    });
    if (!confirmed) {
      return;
    }
    await this.adb.run(device => clearAppData(device, packageName), {
      success: `Cleared data for ${packageName}`,
      failure: 'Failed to clear data',
    });
  }

  protected async uninstall(packageName: string): Promise<void> {
    const confirmed = await this.adb.confirm({
      title: 'Uninstall App',
      message: `Uninstall ${packageName}?`,
      confirmText: 'Uninstall',
      confirmColor: 'warn',
      icon: 'delete',
      iconColor: 'warn',
    });
    if (!confirmed) {
      return;
    }
    await this.adb.run(device => uninstallApp(device, packageName), {
      success: `Uninstalled ${packageName}`,
      failure: 'Uninstall failed',
      after: () => this.packages.reload(),
    });
  }

  protected triggerInstall(): void {
    this.apkInput().nativeElement.click();
  }

  protected async onApkSelected(event: Event): Promise<void> {
    const input = event.target as HTMLInputElement;
    const files = Array.from(input.files ?? []);
    input.value = '';
    if (files.length > 0) {
      await this.startInstall(files);
    }
  }

  protected cancelInstall(): void {
    this.installService.cancel(this.target());
  }

  protected dismissInstall(): void {
    this.installService.dismiss(this.target());
  }

  protected applyRemedy(): void {
    this.installService.applyRemedy(this.target());
  }

  protected async launchInstalled(): Promise<void> {
    const packageName = this.install()?.installedPackage;
    if (packageName) {
      await this.launchApp(packageName);
    }
    this.installService.dismiss(this.target());
  }

  /**
   * Builds an install plan (expanding .apks/.xapk/.apkm archives and parsing
   * manifests), previews it for confirmation, then hands it to the per-device
   * install service so progress survives leaving this panel.
   */
  protected async startInstall(files: File[]): Promise<void> {
    const plan = await buildInstallPlan(files);
    if (plan.units.length === 0) {
      const message = plan.warnings[0] ?? 'No installable APK files';
      this.snackBar.open(message, 'Close', {duration: 5000});
      return;
    }

    const needsConfirm =
      plan.ambiguous ||
      plan.warnings.length > 0 ||
      plan.units.length > 1 ||
      plan.units[0].files.length > 1;

    let allSplits = false;
    if (needsConfirm) {
      const dialogRef = this.dialog.open<
        InstallConfirmDialogComponent,
        {plan: typeof plan},
        InstallConfirmResult
      >(InstallConfirmDialogComponent, {data: {plan}, width: '600px'});
      const confirmed = await firstValueFrom(dialogRef.afterClosed()).catch(() => undefined);
      if (!confirmed) {
        return;
      }
      if (plan.ambiguous && confirmed.grouping === 'separate') {
        plan.units = plan.units[0].files.map(file => ({
          packageName: file.info?.package ?? null,
          versionName: file.info?.versionName,
          files: [file],
        }));
      }
      // A manual multi-file drop is an explicit split set, so install as given.
      allSplits = plan.units.some(unit => unit.files.length > 1);
    }

    this.installService.install(this.target(), plan, {allSplits});
  }
}
