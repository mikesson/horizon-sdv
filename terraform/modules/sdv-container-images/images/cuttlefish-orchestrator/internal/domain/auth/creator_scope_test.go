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

import (
	"context"
	"errors"
	"testing"
)

func TestCreatorScope(t *testing.T) {
	t.Parallel()
	authed := ContextWithPrincipal(t.Context(), &Principal{Email: "me@example.com"})

	tests := []struct {
		name      string
		ctx       context.Context
		createdBy string
		mine      bool
		want      string
		wantErr   error
	}{
		{name: "unscoped", ctx: authed, want: ""},
		{name: "explicit created_by passes through", ctx: authed, createdBy: "other@example.com", want: "other@example.com"},
		{name: "mine resolves to the principal", ctx: authed, mine: true, want: "me@example.com"},
		{name: "both set rejects", ctx: authed, createdBy: "other@example.com", mine: true, wantErr: ErrCreatorScopeConflict},
		{name: "mine without a principal rejects", ctx: t.Context(), mine: true, wantErr: ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CreatorScope(tt.ctx, tt.createdBy, tt.mine)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("CreatorScope() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreatorScope() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("CreatorScope() = %q, want %q", got, tt.want)
			}
		})
	}
}
