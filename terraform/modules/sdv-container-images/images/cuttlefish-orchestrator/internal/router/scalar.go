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

package router

import (
	"net/http"
	"strings"
)

// scalarVersion is pinned together with its subresource-integrity hash; both
// are taken from Huma v2.38's own Scalar template so the pair is known-good.
const (
	scalarScriptURL = "https://unpkg.com/@scalar/api-reference@1.44.20/dist/browser/standalone.js"
	scalarScriptSRI = "sha384-tMz7GAo6dMy55x9tLFtH+sHtogji6Scmb+feBR31TAHmvSPRUTboK9H3M5NFaP4R"
)

// docsHandler serves the interactive API reference at /docs using Scalar.
//
// Huma's built-in docs route is disabled (DocsPath = "") because the pinned
// Huma version cannot pass renderer options; serving our own page lets us set
// Scalar's configuration: dark mode to match the console, and the models
// section hidden (the component-schema list is dominated by request/response
// wrapper types that already render inline on each operation).
//
// The CSP mirrors Huma's own Scalar template, including its 'unsafe-eval'
// concession (a Scalar limitation upstream also carries); the page is
// sandboxed, same-origin locked for data, and behind IAP like every other
// route.
func docsHandler(title string) http.HandlerFunc {
	csp := strings.Join([]string{
		"default-src 'none'",
		"base-uri 'none'",
		"connect-src 'self'",
		"img-src 'self' data:",
		"form-action 'none'",
		"frame-ancestors 'none'",
		"sandbox allow-same-origin allow-scripts allow-popups allow-popups-to-escape-sandbox",
		"script-src 'unsafe-eval' " + scalarScriptURL,
		"style-src 'unsafe-inline'",
	}, "; ")

	page := []byte(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="referrer" content="no-referrer">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>` + title + `</title>
  </head>
  <body>
    <script
      id="api-reference"
      data-url="openapi.json"
      data-configuration='{"darkMode":true,"hideModels":true,"defaultOpenAllTags":false,"metaData":{"title":"` + title + `"}}'
    ></script>
    <script src="` + scalarScriptURL + `" crossorigin integrity="` + scalarScriptSRI + `"></script>
  </body>
</html>`)

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		// Best effort: if the write fails the client disconnected.
		_, _ = w.Write(page)
	}
}
