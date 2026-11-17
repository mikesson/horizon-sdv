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

// Package middleware provides the HTTP middleware for the Cloud Cuttlefish
// Orchestrator API: authentication, request identity, logging, recovery,
// and connection protection.
//
// # Middleware
//
//   - Auth: validates the caller (the IAP assertion header, per AuthConfig)
//     and attaches the auth.Principal to the context
//   - RequestID: generates or propagates a request ID
//   - ClientInfo: resolves the real client IP into the request context
//   - Logger: structured request logging
//   - Recovery: panic recovery with error logging
//   - CSRFProtection: blocks unsafe-method cross-site browser requests
//     (net/http.CrossOriginProtection)
//   - RequestDeadlines: per-request read/write connection deadlines, with the
//     caller classifying each request (streaming and long-poll routes run
//     unbounded; handlers that wait out a host operation get a longer write
//     deadline than ordinary API calls)
//
// # Context helpers
//
//   - UserFromContext: the authenticated Principal
//   - RequestIDFromContext: the request ID
//   - IPFromContext / MustIPFromContext: the resolved client IP
package middleware
