// -------------------------------------------------------------------------------
// Shared Temporal Presets - Unit Tests
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Pins the fleet-wide retry/activity-option defaults and verifies each builder
// returns a fresh value (callers must never share a mutable policy).
// -------------------------------------------------------------------------------

package shared

import (
	"testing"
	"time"
)

func TestStandardRetry(t *testing.T) {
	p := StandardRetry()
	if p.InitialInterval != time.Second {
		t.Errorf("InitialInterval = %v, want 1s", p.InitialInterval)
	}
	if p.BackoffCoefficient != 2.0 {
		t.Errorf("BackoffCoefficient = %v, want 2.0", p.BackoffCoefficient)
	}
	if p.MaximumInterval != time.Minute {
		t.Errorf("MaximumInterval = %v, want 1m", p.MaximumInterval)
	}
	if p.MaximumAttempts != 3 {
		t.Errorf("MaximumAttempts = %d, want 3", p.MaximumAttempts)
	}
	// Each call must yield a distinct pointer so callers can't mutate a shared policy.
	if StandardRetry() == p {
		t.Error("StandardRetry should return a fresh pointer each call")
	}
}

func TestNoRetry(t *testing.T) {
	if got := NoRetry().MaximumAttempts; got != 1 {
		t.Errorf("NoRetry MaximumAttempts = %d, want 1", got)
	}
}

func TestQuickActivityOptions(t *testing.T) {
	o := QuickActivityOptions()
	if o.StartToCloseTimeout != 5*time.Minute {
		t.Errorf("StartToClose = %v, want 5m", o.StartToCloseTimeout)
	}
	if o.ScheduleToCloseTimeout != 15*time.Minute {
		t.Errorf("ScheduleToClose = %v, want 15m", o.ScheduleToCloseTimeout)
	}
	if o.RetryPolicy == nil {
		t.Error("RetryPolicy should be set")
	}
}

func TestLongActivityOptions(t *testing.T) {
	o := LongActivityOptions()
	if o.StartToCloseTimeout != 30*time.Minute {
		t.Errorf("StartToClose = %v, want 30m", o.StartToCloseTimeout)
	}
	if o.ScheduleToCloseTimeout != 60*time.Minute {
		t.Errorf("ScheduleToClose = %v, want 60m", o.ScheduleToCloseTimeout)
	}
	if o.HeartbeatTimeout != 2*time.Minute {
		t.Errorf("HeartbeatTimeout = %v, want 2m", o.HeartbeatTimeout)
	}
	if o.RetryPolicy == nil {
		t.Error("RetryPolicy should be set")
	}
}
