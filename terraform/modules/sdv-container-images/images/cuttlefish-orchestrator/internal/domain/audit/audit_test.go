// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
)

type recordingSink struct {
	entries []Entry
}

func (s *recordingSink) LogAsync(entry Entry) {
	s.entries = append(s.entries, entry)
}

func TestBuilderAndEntryJSON(t *testing.T) {
	t.Parallel()

	before := time.Now()
	entry := New(ActionDevicePin, ResourceDevice).
		WithActor("alice@example.com").
		WithResource("dev-123").
		WithMetadata(map[string]any{"reason": "ci", "ttl_hours": 4}).
		Build()
	after := time.Now()

	if entry.Action != ActionDevicePin {
		t.Errorf("Action = %q, want %q", entry.Action, ActionDevicePin)
	}
	if entry.ResourceType != ResourceDevice {
		t.Errorf("ResourceType = %q, want %q", entry.ResourceType, ResourceDevice)
	}
	if entry.ActorEmail != "alice@example.com" {
		t.Errorf("ActorEmail = %q, want alice@example.com", entry.ActorEmail)
	}
	if entry.ResourceID == nil || *entry.ResourceID != "dev-123" {
		t.Errorf("ResourceID = %v, want dev-123", entry.ResourceID)
	}
	if entry.Timestamp.Before(before) || entry.Timestamp.After(after) {
		t.Errorf("Timestamp = %v out of [%v, %v]", entry.Timestamp, before, after)
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("json.Marshal(Entry) failed: %v", err)
	}
	var decoded Entry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(Entry) failed: %v", err)
	}
	if decoded.Action != ActionDevicePin || decoded.ResourceID == nil || *decoded.ResourceID != "dev-123" {
		t.Errorf("round-tripped entry mismatch: %+v", decoded)
	}

	emptyResource := New(ActionConfigChange, ResourceSystem).
		WithResource("").
		Build()
	if emptyResource.ResourceID != nil {
		t.Errorf("WithResource(\"\") set ResourceID = %q, want nil", *emptyResource.ResourceID)
	}
}

func TestLogChange(t *testing.T) {
	t.Parallel()

	// Nil sink must safely no-op.
	LogChange(t.Context(), nil, ActionDeviceCreate, ResourceDevice, "dev-1", nil)

	// Missing principal leaves ActorEmail empty.
	sink := &recordingSink{}
	LogChange(context.Background(), sink, ActionHostLeaseShorten, ResourceHost, "host-1", map[string]any{
		MetaReclaim: ReclaimDedicatedEmpty,
	})
	if len(sink.entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(sink.entries))
	}
	if sink.entries[0].ActorEmail != "" {
		t.Errorf("expected empty ActorEmail without principal, got %q", sink.entries[0].ActorEmail)
	}
	if sink.entries[0].Metadata[MetaReclaim] != ReclaimDedicatedEmpty {
		t.Errorf("metadata = %v, want reclaim=%s", sink.entries[0].Metadata, ReclaimDedicatedEmpty)
	}

	// Authenticated principal populates ActorEmail.
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Email: "ci@example.com"})
	LogChange(ctx, sink, ActionDeviceExtend, ResourceDevice, "dev-2", map[string]any{"hours": 2})
	if len(sink.entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(sink.entries))
	}
	if sink.entries[1].ActorEmail != "ci@example.com" {
		t.Errorf("ActorEmail = %q, want ci@example.com", sink.entries[1].ActorEmail)
	}
	if sink.entries[1].ResourceID == nil || *sink.entries[1].ResourceID != "dev-2" {
		t.Errorf("ResourceID = %v, want dev-2", sink.entries[1].ResourceID)
	}
}
