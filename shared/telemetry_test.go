// -------------------------------------------------------------------------------
// Shared Telemetry - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the pure peer.service attribute builder and the span-helper wrappers
// (they run against the global no-op tracer, so no collector is needed).
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"testing"
)

func TestPeerServiceAttr(t *testing.T) {
	kv := PeerServiceAttr("nomad")
	if string(kv.Key) != "peer.service" {
		t.Errorf("key = %q, want peer.service", kv.Key)
	}
	if kv.Value.AsString() != "nomad" {
		t.Errorf("value = %q, want nomad", kv.Value.AsString())
	}
}

func TestSpanHelpers(t *testing.T) {
	if Tracer() == nil {
		t.Fatal("Tracer() returned nil")
	}
	ctx := context.Background()

	if c, s := StartSpan(ctx, "op"); c == nil || s == nil {
		t.Error("StartSpan returned nil")
	} else {
		s.End()
	}
	if c, s := StartClientSpan(ctx, "op"); c == nil || s == nil {
		t.Error("StartClientSpan returned nil")
	} else {
		s.End()
	}
	if c, s := StartPeerSpan(ctx, "nomad", "op"); c == nil || s == nil {
		t.Error("StartPeerSpan returned nil")
	} else {
		s.End()
	}
}
