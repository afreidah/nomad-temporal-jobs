// -------------------------------------------------------------------------------
// Shared Heartbeat - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Covers the run-while-heartbeating control flow: returning fn's value, fn's
// error, and ctx cancellation. A long interval keeps the heartbeat ticker from
// firing so no activity context is needed.
// -------------------------------------------------------------------------------

package shared

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithHeartbeat_ReturnsValue(t *testing.T) {
	v, err := WithHeartbeat(context.Background(), time.Hour, func() (int, error) { return 42, nil })
	if err != nil || v != 42 {
		t.Fatalf("got (%d, %v), want (42, nil)", v, err)
	}
}

func TestWithHeartbeat_ReturnsError(t *testing.T) {
	_, err := WithHeartbeat(context.Background(), time.Hour, func() (int, error) {
		return 0, errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected fn's error to propagate")
	}
}

func TestWithHeartbeat_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WithHeartbeat(ctx, time.Hour, func() (int, error) {
		<-ctx.Done() // honor cancellation so the goroutine doesn't leak
		return 0, ctx.Err()
	})
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
}
