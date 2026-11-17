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

import {CommonModule} from '@angular/common';
import {provideHttpClient, withFetch, withInterceptorsFromDi} from '@angular/common/http';
import type {ApplicationConfig} from '@angular/core';
import {
  importProvidersFrom,
  inject,
  LOCALE_ID,
  provideAppInitializer,
  provideZonelessChangeDetection,
} from '@angular/core';
// Material
import {MatButtonModule} from '@angular/material/button';
import {MatCardModule} from '@angular/material/card';
import {MatCheckboxModule} from '@angular/material/checkbox';
import {MatDividerModule} from '@angular/material/divider';
import {MatExpansionModule} from '@angular/material/expansion';
import {MAT_FORM_FIELD_DEFAULT_OPTIONS, MatFormFieldModule} from '@angular/material/form-field';
import {MAT_ICON_DEFAULT_OPTIONS, MatIconModule} from '@angular/material/icon';
import {MatInputModule} from '@angular/material/input';
import {MatListModule} from '@angular/material/list';
import {MatProgressBarModule} from '@angular/material/progress-bar';
import {MatSelectModule} from '@angular/material/select';
import {MatSlideToggleModule} from '@angular/material/slide-toggle';
import {MatSnackBarModule} from '@angular/material/snack-bar';
import {MatTooltipModule} from '@angular/material/tooltip';
import {BrowserModule} from '@angular/platform-browser';
import {provideRouter, withComponentInputBinding} from '@angular/router';
import {catchError, firstValueFrom, of} from 'rxjs';
import {HTTP_INTERCEPTOR_PROVIDERS} from '../http_interceptors';
import {routes} from './app.routes';
import {AuthService} from './auth/services/auth.service';
import {AuthStore} from './auth/store/auth.signal-store';

/**
 * Loads the current user at app startup.
 *
 * This runs BEFORE the router initializes, ensuring that auth guards
 * have access to the correct authentication state.
 *
 * If the user is not authenticated (401/403), this completes without error
 * and the auth guards will redirect to /unauthorized.
 */
async function initializeAuth(
  authService: AuthService,
  authStore: InstanceType<typeof AuthStore>
): Promise<void> {
  try {
    const user = await firstValueFrom(
      authService.loadCurrentUser().pipe(
        catchError(_err => {
          return of(null);
        })
      )
    );

    if (user) {
      authStore.setUser(user);
    } else {
      authStore.setNotAuthenticated();
    }
  } catch (err) {
    console.error('[AppInit] Error loading user:', err);
    // Ensure we unblock guards even on error
    authStore.setNotAuthenticated();
  }
}

/**
 * The main application configuration object.
 *
 * This configuration bootstraps the Angular application with:
 * - Zoneless Change Detection: Opting out of Zone.js for performance and granular reactivity.
 * - Signal-based State Management: Initializing NgRx for global state.
 * - Material Design: Global default options for UI components.
 * - HTTP Interceptors: Global handling of auth tokens and error responses.
 */
export const appConfig: ApplicationConfig = {
  providers: [
    importProvidersFrom(
      BrowserModule,
      CommonModule,
      // Material Modules
      MatButtonModule,
      MatCardModule,
      MatCheckboxModule,
      MatIconModule,
      MatSlideToggleModule,
      MatTooltipModule,
      MatDividerModule,
      MatInputModule,
      MatFormFieldModule,
      MatSelectModule,
      MatListModule,
      MatExpansionModule,
      MatProgressBarModule,
      MatSnackBarModule
    ),
    // HttpClient with modern functional API (replaces HttpClientModule)
    // withFetch() uses the Fetch API for better streaming and performance
    // withInterceptorsFromDi() enables class-based interceptors registered via DI
    provideHttpClient(withFetch(), withInterceptorsFromDi()),
    provideRouter(routes, withComponentInputBinding()),
    provideZonelessChangeDetection(),
    HTTP_INTERCEPTOR_PROVIDERS,
    {provide: LOCALE_ID, useValue: 'en-US'},
    {
      provide: MAT_FORM_FIELD_DEFAULT_OPTIONS,
      useValue: {appearance: 'outline'},
    },
    {
      // Every mat-icon in the app relies on the default font set; none passes
      // fontSet. Setting it here switches all of them at once, off the 2022
      // frozen Material Icons and onto the set the Cloud Console renders.
      provide: MAT_ICON_DEFAULT_OPTIONS,
      useValue: {fontSet: 'material-symbols-outlined'},
    },
    // This ensures auth guards have the correct state when they run
    provideAppInitializer(() => initializeAuth(inject(AuthService), inject(AuthStore))),
  ],
};
