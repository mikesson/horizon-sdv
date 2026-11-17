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

/**
 * User represents an authenticated user in the system.
 *
 * Cloud Run IAP authenticates every request at the edge; the verified
 * identity is the whole auth model. There are no roles or permissions, and
 * any authenticated user has full access.
 */
export interface User {
  email: string;
  displayName?: string;
}

/**
 * AuthState represents the current authentication state.
 *
 * Note: Session expiration is managed by IAP, not the frontend.
 */
export interface AuthState {
  user: User | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  error: string | null;
}

export const initialAuthState: AuthState = {
  user: null,
  isAuthenticated: false,
  isLoading: true,
  error: null,
};
