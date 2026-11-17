// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"fmt"
	"net/http"
)

// CSRFProtection creates middleware that blocks unsafe-method cross-site
// browser requests using net/http.CrossOriginProtection (Sec-Fetch-Site with
// an Origin fallback).
//
// The browser console authenticates via the ambient IAP session cookie, so
// its state-changing requests need this protection. Non-browser clients
// (cfctl, CI) send neither Sec-Fetch-Site nor Origin and pass through
// untouched; their bearer-token auth is CSRF-immune anyway. With the SPA and
// API served from the same Cloud Run origin, same-origin XHRs pass
// automatically; trustedOrigins covers cross-origin development setups such
// as the local Angular dev server.
func CSRFProtection(trustedOrigins []string) (Middleware, error) {
	protection := http.NewCrossOriginProtection()
	for _, origin := range trustedOrigins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("adding trusted origin %q: %w", origin, err)
		}
	}
	return protection.Handler, nil
}
