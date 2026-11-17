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

package auth

import "context"

// ContextKey is the type for context keys used by the auth package.
type ContextKey string

const (
	// PrincipalContextKey is the context key for the authenticated principal.
	PrincipalContextKey ContextKey = "auth.principal"
)

// PrincipalFromContext extracts the authenticated principal from context.
// Returns nil and false if no principal is present.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(PrincipalContextKey).(*Principal)
	return p, ok
}

// ContextWithPrincipal returns a new context with the principal attached.
func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, PrincipalContextKey, p)
}
